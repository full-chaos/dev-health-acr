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
	// Prod served the rev 88 statement at 233.98 MiB of the 256 MiB cap; the
	// seed calibrates when that form lands inside this band under the cap.
	wideIDRev88FloorMiB = 200.0
	// A form measured without the API cap still stops here, so a runaway
	// statement fails this row instead of the runner.
	wideIDUncappedCeiling = 2 << 30
)

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
			t.Fatalf("measurement did not happen: %d linked set and %d linked mix statements logged on a served read, want 1 and 1", len(setPeaks), len(mixPeaks))
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
		if err := direct.QueryRow(ctx, `SELECT
	maxIf(query_duration_ms, position(query, 'groupArray(r), groupArray(n), groupArray(t)') > 0),
	maxIf(query_duration_ms, position(query, 'uniqExact(m.repo_uuid)') > 0)
FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'`, mark).Scan(&read.setMs, &read.mixMs); err != nil {
			t.Fatalf("read the served statements' durations (measurement did not happen): %v", err)
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
// Three forms run on one seed. The rev 88 text calibrates (inside the band
// under the cap) and the text with the linked set and the scope inlined (the
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
	capped := contextpacket.NewMemoryCappedQueryClient(query, contextpacket.APIStatementMaxMemoryUsage)
	provider := findProvider(t, devhealthfacts.NewProviders(capped), contextfabric.FactInvestment)
	rev88 := readWideIDForm(t, "team_linked_mix_rev88.sql")
	inlined := readWideIDForm(t, "team_linked_mix_inline_sets.sql")
	uncapped := fmt.Sprintf("max_memory_usage = %d, max_result_rows = 0", wideIDUncappedCeiling)

	rev88Runs := linkedFormRuns(t, ctx, capped, direct, orgID, "rev88", rev88, "", bindings)
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
	}{{"rev88", rev88Runs}, {"inline_sets", inlinedRuns}} {
		for i, r := range form.runs {
			fmt.Fprintf(&b, "  %-12s run %d: peak %8.2f MiB, %d ms, code %d\n", form.name, i, r.mib, r.ms, r.code)
		}
	}
	fmt.Fprintf(&b, "  inline_sets uncapped (ceiling %d MiB): peak %.2f MiB code %d\n", wideIDUncappedCeiling>>20, inlinedFree.mib, inlinedFree.code)
	for i, r := range served {
		fmt.Fprintf(&b, "  served read %d: linked set %.2f MiB %d ms (the added round trip), linked mix %.2f MiB %d ms, whole investment read %d ms\n", i, r.setMiB, r.setMs, r.mixMiB, r.mixMs, r.wall.Milliseconds())
	}
	fmt.Fprintf(&b, "  bounds: rev88 calibration [%.0f, %d) MiB (lowest %.2f, highest %.2f); inline_sets refused with 241 on every run (lowest %.2f, highest %.2f); served highest under %.0f MiB",
		wideIDRev88FloorMiB, contextpacket.APIStatementMaxMemoryUsage>>20, rev88Low, rev88High, inlinedLow, inlinedHigh, linkedShapePeakBoundMiB)
	t.Log(b.String())

	for i, r := range rev88Runs {
		if r.err != nil || r.mib < wideIDRev88FloorMiB {
			t.Errorf("calibration did not happen: rev 88 form run %d peaked %.2f MiB (error %v), prod served it at 233.98 MiB", i, r.mib, r.err)
		}
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
	t.Logf("value equality over %d teams: production %d team rows, rev 88 form %d", len(teams), len(got), len(want))
	if len(want) < 2 {
		t.Fatalf("value equality did not happen: the rev 88 form returned %d team rows", len(want))
	}
	if len(got) != len(want) {
		t.Errorf("production returned %d team rows, the rev 88 form %d", len(got), len(want))
	}
	for team, w := range want {
		g, ok := got[team]
		if !ok || g.Repos != w.repos || !closeEnough(g.Bugfix, w.bugfix) || len(g.Theme) != len(w.theme) {
			t.Errorf("team %s: production %+v, rev 88 form %+v", team, g, w)
			continue
		}
		for theme, v := range w.theme {
			if gv, ok := g.Theme[theme]; !ok || !closeEnough(gv, v) {
				t.Errorf("team %s theme %s: production %v, rev 88 form %v", team, theme, g.Theme[theme], v)
			}
		}
	}
	if share := got[linkedShapeRepeatedTeam].Theme["feature_delivery"]; share < linkedShapeRepeatedEffort/2 || share > linkedShapeRepeatedEffort/2+1e6 {
		t.Errorf("production: team %s feature_delivery effort %v, want half of the repeated-key unit", linkedShapeRepeatedTeam, share)
	}
}
