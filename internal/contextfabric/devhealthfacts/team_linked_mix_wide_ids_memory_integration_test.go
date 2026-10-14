package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// linkedWideIDProdShape is linkedMemoryProdShape with unit ids as wide as
// prod's: the scope ids of the latest membership run measure 65 to 80
// characters each there, wu-<n> is 3 to 8.
var linkedWideIDProdShape = func() linkedProdShape {
	s := linkedMemoryProdShape
	s.wideUnitIDs = true
	return s
}()

const (
	// Prod served the rev 88 statement at 233.98 MiB; the seed calibrates when
	// that form, measured without the API cap, peaks within 15% of it.
	wideIDProdRev88MiB     = 233.98
	wideIDCalibrationSlack = 0.15
	// A form measured without the API cap still stops here, so a runaway
	// statement fails this row instead of the runner.
	wideIDUncappedCeiling = 2 << 30
)

// readonlyQueryClient runs every statement as the prod API user does, with
// readonly=2, keeping the statement memory cap the capped client above it set:
// a driver settings option replaces the settings it finds.
type readonlyQueryClient struct {
	inner contextpacket.ClickHouseQueryClient
}

func (c readonlyQueryClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	settings := clickhousedriver.Settings{"readonly": 2}
	if capBytes, ok := contextpacket.StatementMemoryCapFromContext(ctx); ok {
		settings["max_memory_usage"] = capBytes
	}
	return c.inner.Query(clickhousedriver.Context(ctx, clickhousedriver.WithSettings(settings)), statement, bindings)
}

type linkedFormRun struct {
	mib  float64
	ms   uint64
	code int32
	err  error
}

// runLinkedForm runs statement once on client for team T-0 and returns its
// peak from query_log, finished or failed, and the server's error code.
func runLinkedForm(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, orgID, name, statement, settings string, bindings []readers.Binding) linkedFormRun {
	t.Helper()
	tag := fmt.Sprintf("wide-%s-%d", name, time.Now().UnixNano())
	sql := statement + "\nSETTINGS log_comment = '" + tag + "'"
	if settings != "" {
		sql += ", " + settings
	}
	err := readers.QueryOrgScopedNamed(ctx, client, "LinkedMixWideIDs", sql, orgID, []string{linkedShapeTeam},
		func(readers.RowScanner) error { return nil }, bindings...)
	if flushErr := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); flushErr != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", flushErr)
	}
	var mem, ms uint64
	var code int32
	if qErr := direct.QueryRow(ctx, `SELECT memory_usage, query_duration_ms, exception_code FROM system.query_log
WHERE type != 'QueryStart' AND log_comment = ? ORDER BY event_time_microseconds DESC LIMIT 1`, tag).Scan(&mem, &ms, &code); qErr != nil {
		t.Fatalf("%s: no query_log row for %s (measurement did not happen): %v (run error %v)", name, tag, qErr, err)
	}
	var exception *clickhousedriver.Exception
	if errors.As(err, &exception) {
		code = exception.Code
	}
	return linkedFormRun{mib: float64(mem) / (1 << 20), ms: ms, code: code, err: err}
}

// assertReadonlyInForce shows readonly=2 is in force on the served client, not
// only set: lifting readonly through it is refused by the server (code 164,
// READONLY). A write cannot reach the server through this client at all: the
// client refuses any statement that is not a read before it sends it.
func assertReadonlyInForce(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string) {
	t.Helper()
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadonlyProbe", "SELECT 1 SETTINGS readonly = 0", orgID, nil, func(readers.RowScanner) error { return nil })
	var exception *clickhousedriver.Exception
	if !errors.As(err, &exception) || exception.Code != 164 {
		t.Errorf("readonly is not in force on the served client: lifting it returned %v, want code 164 (READONLY)", err)
	}
	t.Logf("readonly=2 in force on the served client: lifting readonly refused (code 164)")
}

func linkedFormRuns(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, orgID, name, statement, settings string, bindings []readers.Binding) []linkedFormRun {
	t.Helper()
	runLinkedForm(t, ctx, client, direct, orgID, name+"-cold", statement, settings, bindings)
	out := make([]linkedFormRun, 0, linkedShapeRuns)
	for run := 0; run < linkedShapeRuns; run++ {
		out = append(out, runLinkedForm(t, ctx, client, direct, orgID, fmt.Sprintf("%s-%d", name, run), statement, settings, bindings))
	}
	return out
}

func linkedFormSpread(runs []linkedFormRun) (lowest, highest float64) {
	lowest = -1
	for _, r := range runs {
		if lowest < 0 || r.mib < lowest {
			lowest = r.mib
		}
		if r.mib > highest {
			highest = r.mib
		}
	}
	return lowest, highest
}

func readWideIDForm(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.TrimRight(string(b), "\n")
}

// servedLinkedRead is one warm served team investment read: the peaks and
// server durations of the linked set and mix statements, and the read's wall
// time.
type servedLinkedRead struct {
	setMiB, mixMiB float64
	setMs, mixMs   uint64
	wall           time.Duration
}

// servedLinkedSetAndMixReads drives the team investment read through the
// provider and returns its warm reads after one cold read. A refused external
// table fails the read, and the row with it.
func servedLinkedSetAndMixReads(t *testing.T, ctx context.Context, provider contextfabric.FactProvider, direct clickhousedriver.Conn, orgID string) []servedLinkedRead {
	t.Helper()
	var out []servedLinkedRead
	for run := 0; run < linkedShapeRuns+1; run++ {
		mark := serverNowMicros(t, ctx, direct)
		began := time.Now()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{teamSubject(linkedShapeTeam)},
		})
		wall := time.Since(began)
		if err != nil {
			t.Fatalf("ReadFacts (a refused external table fails here): %v: %s", err, readFailureCause(err))
		}
		factFor(t, result, teamSubject(linkedShapeTeam))
		setPeaks := statementMemorySince(t, ctx, direct, statementFilter{Table: "work_graph_issue_pr", QueryContains: "groupArray(r), groupArray(n), groupArray(t)"}, mark)
		mixPeaks := statementMemorySince(t, ctx, direct, statementFilter{Table: "work_unit_investments", QueryContains: "uniqExact(m.repo_uuid)"}, mark)
		if len(setPeaks) != 1 || len(mixPeaks) != 1 {
			t.Errorf("measurement did not happen: %d linked set and %d linked mix statements logged on a served read, want 1 and 1", len(setPeaks), len(mixPeaks))
			continue
		}
		if run == 0 {
			continue
		}
		read := servedLinkedRead{wall: wall}
		for _, p := range setPeaks {
			read.setMiB = float64(p) / (1 << 20)
		}
		for _, p := range mixPeaks {
			read.mixMiB = float64(p) / (1 << 20)
		}
		var setRuns, mixRuns uint64
		var setReadonly, mixReadonly string
		if err := direct.QueryRow(ctx, `SELECT
	maxIf(query_duration_ms, is_set), maxIf(query_duration_ms, is_mix),
	countIf(is_set), countIf(is_mix),
	anyIf(Settings['readonly'], is_set), anyIf(Settings['readonly'], is_mix)
FROM (
	SELECT query_duration_ms, Settings,
		has(tables, concat(currentDatabase(), '.work_graph_issue_pr')) AND position(query, 'groupArray(r), groupArray(n), groupArray(t)') > 0 AS is_set,
		has(tables, concat(currentDatabase(), '.work_unit_investments')) AND position(query, 'uniqExact(m.repo_uuid)') > 0 AS is_mix
	FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'
)`, mark).Scan(&read.setMs, &read.mixMs, &setRuns, &mixRuns, &setReadonly, &mixReadonly); err != nil {
			t.Fatalf("read the served statements' durations (measurement did not happen): %v", err)
		}
		if setRuns != 1 || mixRuns != 1 || setReadonly != "2" || mixReadonly != "2" {
			t.Errorf("the served linked set and mix statements did not each run once under readonly=2: set %d run(s) readonly %q, mix %d run(s) readonly %q", setRuns, setReadonly, mixRuns, mixReadonly)
		}
		out = append(out, read)
	}
	return out
}

func highestOf(values []float64) float64 {
	highest := 0.0
	for _, v := range values {
		if v > highest {
			highest = v
		}
	}
	return highest
}

// The team linked pull request mix at the largest prod organization's shape
// with unit ids as wide as prod's. Prod served the rev 88 statement at 233.98
// MiB and refused the rev 89 statement with code 241 before it read a row;
// with short unit ids the same seed measured 80 MiB for the rev 89 form, so
// the width of the scope ids is the dimension the short seed lacked.
//
// Three forms run on one seed. The rev 88 text calibrates (measured without
// the cap, its highest read within 15% of prod's peak) and the text with the linked set and the scope inlined (the
// form the production statement replaced) must be refused with code 241 under
// the API cap: if either does not hold, the seed does not reproduce prod and
// no bound below means anything. The production read sends both as external
// tables; each of its two statements stays under the peak bound on every warm
// read, and its rows equal the rev 88 form's for every team of the seed.
func TestTeamLinkedMixMemoryWithProdWidthUnitIDs(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-linked-wide-ids"
	query, direct, _, ids := newLinkedProdShapeStore(t, ctx, orgID, linkedWideIDProdShape)
	width := 0
	for _, id := range ids {
		width += len(id)
	}
	scopeJSON, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded: %d scope ids, average width %.1f characters, scope literal %.2f MiB", len(ids), float64(width)/float64(len(ids)), float64(len(scopeJSON))/(1<<20))
	bindings := []readers.Binding{{Name: "scope_json", Value: string(scopeJSON)}}
	capped := contextpacket.NewMemoryCappedQueryClient(readonlyQueryClient{inner: query}, contextpacket.APIStatementMaxMemoryUsage)
	provider := findProvider(t, devhealthfacts.NewProviders(capped), contextfabric.FactInvestment)
	assertReadonlyInForce(t, ctx, capped, orgID)
	rev88 := readWideIDForm(t, "team_linked_mix_rev88.sql")
	inlined := readWideIDForm(t, "team_linked_mix_inline_sets.sql")
	uncapped := fmt.Sprintf("max_memory_usage = %d, max_result_rows = 0", wideIDUncappedCeiling)

	rev88Runs := linkedFormRuns(t, ctx, query, direct, orgID, "rev88-uncapped", rev88, uncapped, bindings)
	inlinedRuns := linkedFormRuns(t, ctx, capped, direct, orgID, "inline-sets", inlined, "", bindings)
	inlinedFree := runLinkedForm(t, ctx, query, direct, orgID, "inline-sets-uncapped", inlined, uncapped, bindings)
	served := servedLinkedSetAndMixReads(t, ctx, provider, direct, orgID)
	var setPeaks, mixPeaks []float64
	for _, r := range served {
		setPeaks = append(setPeaks, r.setMiB)
		mixPeaks = append(mixPeaks, r.mixMiB)
	}

	rev88Low, rev88High := linkedFormSpread(rev88Runs)
	inlinedLow, inlinedHigh := linkedFormSpread(inlinedRuns)
	var b strings.Builder
	fmt.Fprintf(&b, "prod-width unit ids, API cap %d MiB, %d warm runs after one cold run:\n", contextpacket.APIStatementMaxMemoryUsage>>20, linkedShapeRuns)
	for _, form := range []struct {
		name string
		runs []linkedFormRun
	}{{"rev88 (uncapped)", rev88Runs}, {"inline_sets", inlinedRuns}} {
		for i, r := range form.runs {
			fmt.Fprintf(&b, "  %-16s run %d: peak %8.2f MiB, %d ms, code %d\n", form.name, i, r.mib, r.ms, r.code)
		}
	}
	fmt.Fprintf(&b, "  inline_sets uncapped (ceiling %d MiB): peak %.2f MiB code %d\n", wideIDUncappedCeiling>>20, inlinedFree.mib, inlinedFree.code)
	for i, r := range served {
		fmt.Fprintf(&b, "  served read %d: linked set %.2f MiB %d ms (the added round trip), linked mix %.2f MiB %d ms, whole investment read %d ms\n", i, r.setMiB, r.setMs, r.mixMiB, r.mixMs, r.wall.Milliseconds())
	}
	calLow, calHigh := wideIDProdRev88MiB*(1-wideIDCalibrationSlack), wideIDProdRev88MiB*(1+wideIDCalibrationSlack)
	fmt.Fprintf(&b, "  bounds: calibration rev88 uncapped highest in [%.2f, %.2f] MiB (prod %.2f; lowest %.2f, highest %.2f); inline_sets refused with 241 on every run under the cap (lowest %.2f, highest %.2f); served highest under %.0f MiB",
		calLow, calHigh, wideIDProdRev88MiB, rev88Low, rev88High, inlinedLow, inlinedHigh, linkedShapePeakBoundMiB)
	t.Log(b.String())

	for i, r := range rev88Runs {
		if r.err != nil {
			t.Errorf("calibration did not happen: rev 88 form run %d failed without the cap: %v", i, r.err)
		}
	}
	if rev88High < calLow || rev88High > calHigh {
		t.Errorf("calibration did not happen: the rev 88 form's highest read %.2f MiB is outside [%.2f, %.2f] MiB (prod %.2f MiB)", rev88High, calLow, calHigh, wideIDProdRev88MiB)
	}
	for i, r := range inlinedRuns {
		if r.code != 241 {
			t.Errorf("calibration did not happen: the inlined-sets form run %d was not refused with code 241 (peak %.2f MiB, error %v); prod refused it", i, r.mib, r.err)
		}
	}
	if highest := highestOf(setPeaks); highest > linkedShapePeakBoundMiB {
		t.Errorf("the served linked set statement peaked at %.2f MiB, bound %.0f MiB", highest, linkedShapePeakBoundMiB)
	}
	if highest := highestOf(mixPeaks); highest > linkedShapePeakBoundMiB {
		t.Errorf("the served linked mix statement peaked at %.2f MiB, bound %.0f MiB", highest, linkedShapePeakBoundMiB)
	}

	teams := make([]string, linkedWideIDProdShape.teams)
	for i := range teams {
		teams[i] = fmt.Sprintf("T-%d", i)
	}
	want := readLinkedMixRows(t, ctx, query, orgID, teams, devhealthfacts.LinkedMixFragment{Name: "rev88", SQL: rev88, Bindings: bindings})
	got, err := devhealthfacts.TeamLinkedMixRowsForTest(ctx, provider, orgID, teams)
	if err != nil {
		t.Fatalf("production linked mix read: %v", err)
	}
	compareLinkedMixRows(t, "all teams", got, want)
	if len(want) < 2 {
		t.Errorf("value equality did not happen: the rev 88 form returned %d team rows", len(want))
	}
	if share := got[linkedShapeRepeatedTeam].Theme["feature_delivery"]; share < linkedShapeRepeatedEffort/2 || share > linkedShapeRepeatedEffort/2+1e6 {
		t.Errorf("production: team %s feature_delivery effort %v, want half of the repeated-key unit", linkedShapeRepeatedTeam, share)
	}

	// A team whose owned projects link no pull request: the production read
	// sends no mix statement; the inlined statement returns no row either.
	const noLinks = "T-without-links"
	inlineIDs := devhealthfacts.LinkedMixFragment{Name: "inline_sets", SQL: devhealthfacts.TeamLinkedMixInlineStatementForTest(false), Bindings: bindings}
	gotNone, err := devhealthfacts.TeamLinkedMixRowsForTest(ctx, provider, orgID, []string{noLinks})
	if err != nil {
		t.Errorf("production linked mix read for a team without links: %v", err)
	} else {
		compareLinkedMixRows(t, "empty linked set", gotNone, readLinkedMixRows(t, ctx, query, orgID, []string{noLinks}, inlineIDs))
	}

	// A newer complete membership run with no unit: the scope resolves to the
	// scope subqueries (an empty run is never sent as ids), and the production
	// read equals the inlined statement under them for every team.
	if err := direct.Exec(ctx, `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?, 'run-empty', ?)`, orgID, time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	inlineSubquery := devhealthfacts.LinkedMixFragment{Name: "inline_sets_subquery_scope", SQL: devhealthfacts.TeamLinkedMixInlineStatementForTest(true)}
	gotEmptyRun, err := devhealthfacts.TeamLinkedMixRowsForTest(ctx, provider, orgID, teams)
	if err != nil {
		t.Errorf("production linked mix read under an empty membership run: %v", err)
	} else {
		compareLinkedMixRows(t, "empty membership run", gotEmptyRun, readLinkedMixRows(t, ctx, query, orgID, teams, inlineSubquery))
	}
}

// compareLinkedMixRows holds the production read's rows equal to a reference
// form's, every team and every field.
func compareLinkedMixRows(t *testing.T, label string, got map[string]devhealthfacts.LinkedMixValues, want map[string]linkedMixRow) {
	t.Helper()
	t.Logf("value equality, %s: production %d team rows, reference %d", label, len(got), len(want))
	if len(got) != len(want) {
		t.Errorf("%s: production returned %d team rows, the reference %d", label, len(got), len(want))
	}
	for team, w := range want {
		g, ok := got[team]
		if !ok || g.Repos != w.repos || !closeEnough(g.Bugfix, w.bugfix) || len(g.Theme) != len(w.theme) {
			t.Errorf("%s: team %s: production %+v, reference %+v", label, team, g, w)
			continue
		}
		for theme, v := range w.theme {
			if gv, ok := g.Theme[theme]; !ok || !closeEnough(gv, v) {
				t.Errorf("%s: team %s theme %s: production %v, reference %v", label, team, theme, g.Theme[theme], v)
			}
		}
	}
}
