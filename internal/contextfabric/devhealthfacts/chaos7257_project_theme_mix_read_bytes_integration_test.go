package devhealthfacts_test

// CHAOS-7257: the project theme-mix statements must read
// work_unit_investments within the ClickHouse byte budget acr-api runs under.
//
// Prod (acr 6fc07dbe, helm rev 20, 2026-09-30) answered every project
// investment read with "devhealthfacts: query project theme mix failed":
// ClickHouse Code 307 TOO_MANY_BYTES, 65.83 MiB read against the 64 MiB
// max_bytes_to_read, on the owning-team roll-up statement (readProjectThemeMix).
// The statement inlined the latest-row CTE at three references (repo_linked,
// evidence_resolved, excluded_no_repo_link), so it scanned the ~40 MB table
// three times. CHAOS-6594 fixed the same class for the repository and team mix;
// this is the project statement's turn (CHAOS-6611 is the design note).
//
// The fixture below has the SHAPE of the prod table (prod: 20,780 rows,
// structural_evidence_json ~14 MiB, subcategory_distribution_json ~8 MiB,
// 136,531 membership rows in 2 runs) and runs under the prod cap. The
// measurement is EXECUTED: read_bytes comes from the server's query_log for
// every statement that touched work_unit_investments. A measurement that did
// not happen (no log row, no statement) fails; it never skips.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriverv2 "github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-7271: phased project mix reads. chaos7271RollupStatements is the roll-up
// mix's statement count (scope, repo themes, repo bugfix, evidence arm); the native mix adds
// scope, placement and unit values.
const (
	chaos7271RollupStatements  = 4
	chaos7271PhasedStatements  = 8
	chaos7271MainRollupBytes1x = 39384274
	chaos7271MainNativeBytes1x = 39687121
	// The phases re-read the narrow columns (work_unit_id, computed_at, repo_id)
	// once each, so a mix reads about a tenth more than the one statement did at
	// 1x (measured: +11.9% roll-up, +9.7% native); the byte cost of not reading
	// every wide column in one statement.
	chaos7271MaxMixGrowth = 1.15
)

// chaos7257ProdMaxBytesToRead is ACR_CLICKHOUSE_MAX_BYTES_TO_READ on prod (the
// acr-api startup log field clickhouse_max_bytes_to_read = 67108864). It is
// the budget the production statement must fit in; the fix never raises it.
const chaos7257ProdMaxBytesToRead uint64 = 67108864

// Shape of the prod table the fixture mimics (rows, not bytes: bytes come out
// of the row widths below and are measured, not asserted).
const (
	chaos7257Units       = 20000              // prod: 20,780 work_unit_investments rows
	chaos7271GrowthUnits = 2 * chaos7257Units // CHAOS-7271: the table at twice the prod row count
	chaos7257Repos       = 20
	chaos7257Projects    = 5 // the five prod project subjects that failed 5 of 5
	chaos7257NullRepoMod = 7 // every 7th unit has no repo_id: reached only by the evidence vote
)

type chaos7257Shape struct {
	orgID    string
	at       time.Time
	projects []string // project ids, index i owned by team-i
	repos    []string // repo labels, repo j owned by team (j % projects)
}

func (s chaos7257Shape) team(i int) string { return fmt.Sprintf("team-%d", i) }

func chaos7257Themes(i int) map[string]float64 {
	base := []float64{0.30, 0.25, 0.20, 0.15, 0.10}
	out := map[string]float64{}
	for k, theme := range []string{"feature_delivery", "operational", "maintenance", "quality", "risk"} {
		out[theme] = base[(k+i)%5]
	}
	return out
}

func chaos7257Subcategories(i int) map[string]float64 {
	out := map[string]float64{}
	for k := 0; k < 11; k++ {
		out[fmt.Sprintf("subcategory.detail_class_%02d_of_theme", k)] = 0.05 + float64((i+k)%7)/100
	}
	out[readers.BugfixSubcategoryKey] = 0.25
	return out
}

// seedCHAOS7257ProdShape seeds ownership, work items, membership and a
// prod-shaped work_unit_investments table for one org.
func seedCHAOS7257ProdShape(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, units int) chaos7257Shape {
	t.Helper()
	shape := chaos7257Shape{orgID: orgID, at: ts(2026, 9, 20, 0, 0, 0)}
	at := shape.at
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	for i := 0; i < chaos7257Projects; i++ {
		project := repoUUID(fmt.Sprintf("project-%d", i))
		shape.projects = append(shape.projects, project)
		exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, orgID, "linear", nil, "Project "+project, uint8(1), "active", "", at)
		exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			shape.team(i), shape.team(i), at, orgID, "linear", uint8(1))
		exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", shape.team(i), project, nil, "native", at, nil, at)
	}
	// The last project is ALSO owned by team-0: a team owning more than one
	// project and a project owned by more than one team.
	exec("second project owner", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", shape.team(0), shape.projects[chaos7257Projects-1], nil, "native", at, nil, at)
	for j := 0; j < chaos7257Repos; j++ {
		label := fmt.Sprintf("mix-%d", j)
		shape.repos = append(shape.repos, label)
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at)
		exec("repo ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", shape.team(j%chaos7257Projects), repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at)
	}

	// work_unit_investments: prod row widths (structural ~670 B, subcategory
	// ~390 B, theme ~100 B), 1 in 25 units recomputed (two versions).
	wui, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`)
	if err != nil {
		t.Fatalf("prepare work_unit_investments: %v", err)
	}
	wita, err := direct.PrepareBatch(ctx, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at)`)
	if err != nil {
		t.Fatalf("prepare work_item_team_attributions: %v", err)
	}
	items, err := direct.PrepareBatch(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id)`)
	if err != nil {
		t.Fatalf("prepare work_items: %v", err)
	}
	members, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)`)
	if err != nil {
		t.Fatalf("prepare work_unit_membership: %v", err)
	}
	themeNames := []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}
	for i := 0; i < units; i++ {
		unit := repoUUID(fmt.Sprintf("wu-%d", i))
		var repoID any
		nullRepo := i%chaos7257NullRepoMod == 3
		if !nullRepo {
			repoID = repoUUID(shape.repos[i%chaos7257Repos])
		}
		prs := make([]string, 0, 12)
		for k := 0; k < 12; k++ {
			prs = append(prs, fmt.Sprintf("%q", fmt.Sprintf("%s#pr%d", repoUUID(shape.repos[(i+k)%chaos7257Repos]), 1000+i*16+k)))
		}
		issue := fmt.Sprintf("linear:CH-%d", i)
		evidence := fmt.Sprintf(`{"issues":["ghpr:acme/%s#%d",%q,"jira:OPS-%d"],"prs":[%s]}`,
			shape.repos[i%chaos7257Repos], 5000+i, issue, i, strings.Join(prs, ","))
		from := at.Add(-time.Duration(i%20) * 24 * time.Hour)
		versions := 1
		if i%25 == 0 {
			versions = 2
		}
		for v := 0; v < versions; v++ {
			effort := float64(1 + i%9)
			if v == 0 && versions == 2 {
				effort = 1000 // stale version: must never be counted
			}
			if err := wui.Append(unit, from, at, repoID, effort, chaos7257Themes(i), chaos7257Subcategories(i), evidence, at.Add(time.Duration(v)*time.Hour), orgID); err != nil {
				t.Fatalf("append work unit: %v", err)
			}
		}
		// Project-native path: the unit's linear item sits in one project.
		if err := items.Append("00000000-0000-0000-0000-000000000000", issue, "linear", "title", "issue", "open", "", shape.projects[i%chaos7257Projects], "", "", at, at, nil, "", "", at, orgID); err != nil {
			t.Fatalf("append work item: %v", err)
		}
		// Evidence-vote path: a unit without repo_id reaches a team through the
		// attribution of its PR-side work items (the first three refs).
		if nullRepo {
			for k := 0; k < 3; k++ {
				label := shape.repos[(i+k)%chaos7257Repos]
				if err := wita.Append(orgID, repoUUID(label), fmt.Sprintf("ghpr:acme/%s#%d", label, 1000+i*16+k),
					shape.team((i+k)%chaos7257Repos%chaos7257Projects), "team", "linked_issue", uint8(1), "high", at); err != nil {
					t.Fatalf("append attribution: %v", err)
				}
			}
		}
		// Membership: an old run listing every unit under four categories, and
		// the current run listing 9 in 10 under three (prod: 136,531 rows, 2 runs).
		for c := 0; c < 4; c++ {
			if err := members.Append(orgID, "issue", issue, unit, "theme", themeNames[c], at.Add(-48*time.Hour), "run-1"); err != nil {
				t.Fatalf("append membership: %v", err)
			}
		}
		if i%10 != 0 {
			for c := 0; c < 3; c++ {
				if err := members.Append(orgID, "issue", issue, unit, "theme", themeNames[c], at.Add(-24*time.Hour), "run-2"); err != nil {
					t.Fatalf("append membership: %v", err)
				}
			}
		}
	}
	for i, batch := range []clickhousedriver.Batch{wui, wita, items, members} {
		if err := batch.Send(); err != nil {
			t.Fatalf("send batch %d of 4 (units, attributions, work items, membership): %v", i, err)
		}
	}
	exec("run-1", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-1", at.Add(-48*time.Hour))
	exec("run-2", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-2", at.Add(-24*time.Hour))
	exec("supersession", `INSERT INTO work_unit_supersessions (org_id, superseded_work_unit_id, superseded_at) VALUES (?,?,?)`, orgID, repoUUID("wu-1"), at)
	return shape
}

// newScopedCHAOS7257Client gives the calling test a database of its own on the
// package's ONE shared ClickHouse container (chaos5270_shared_container_test.go)
// and a query client and native connection bound to it. The statements under
// test name their tables unqualified, so the tables (and `SYSTEM STOP MERGES`,
// and the query_log filter on currentDatabase()) are private to the test while
// the container boot -- about 10-15s each, which is what this package's serial
// wall time is made of -- is paid once for the whole package. tune adjusts the
// production query client's Options (the ClickHouse limits acr-api sets).
func newScopedCHAOS7257Client(t *testing.T, tune func(*runtimeclickhouse.Options)) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
	t.Helper()
	ctx := context.Background()
	_, shared := sharedClickHouseFixture(t)
	addr := sharedClickHouseAddrFor(t)
	database := "t7257_" + strings.ToLower(strings.ReplaceAll(sanitizeOrgSuffix(t.Name()), "-", "_"))
	if err := shared.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create database %s: %v", database, err)
	}
	direct, err := clickhousedriverv2.Open(&clickhousedriverv2.Options{
		Addr: []string{addr}, Auth: clickhousedriverv2.Auth{Database: database, Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native connection to %s: %v", database, err)
	}
	options := runtimeclickhouse.Options{DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second}
	if tune != nil {
		tune(&options)
	}
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(options)
	if err != nil {
		t.Fatalf("open production query client on %s: %v", database, err)
	}
	t.Cleanup(func() {
		logCleanupErr("close scoped query client", query.Close())
		logCleanupErr("close scoped native connection", direct.Close())
		logCleanupErr("drop scoped database", shared.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" SYNC"))
	})
	return query, direct
}

func createCHAOS7257Tables(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL(
		"projects", "teams", "team_project_ownership", "team_repo_ownership", "repos",
		"work_unit_investments", "work_unit_supersessions", "work_unit_membership_runs", "work_unit_membership",
		"investment_metrics_daily", "work_item_team_attributions", "work_items", "project_membership_transitions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create view: %v", err)
	}
}

// chaos7257Statement is one statement the server logged as touching
// work_unit_investments.
type chaos7257Statement struct {
	Type      string
	Code      int32
	ReadBytes uint64
	Query     string
}

// queryLogSinceMark is the query_log window statementsSince applies: rows
// logged strictly after the mark, a server-clock instant in whole
// microseconds bound as fromUnixTimestamp64Micro(?).
//
// The mark MUST be bound at the column's own type and precision. The earlier
// window bound a time.Time through clickhouse-go's positional `?`, which the
// driver renders as toDateTime('YYYY-MM-DD HH:MM:SS') -- whole seconds.
// system.query_log carries `INDEX event_time_microseconds_index
// event_time_microseconds TYPE minmax GRANULARITY 1`, and ClickHouse's minmax
// analysis of a DateTime64(6) column against a DateTime constant (an internal
// TYPE_MISMATCH, "Expected: DateTime. Got: Decimal64") skips every granule
// whose newest row falls in the constant's own second. Whenever the read and
// the flush finished inside the mark's second, the one granule holding both
// statements was skipped and the window came back empty ("measurement did not
// happen"), although both statements were logged. A microsecond DateTime64
// bound is analysed exactly; TestQueryLogWindowKeepsAStatementLoggedInTheMarksOwnSecondAgainstRealClickHouse
// pins it on a table with query_log's index shape.
const queryLogSinceMark = `event_time_microseconds > fromUnixTimestamp64Micro(?)`

// statementsSince returns what query_log holds for SELECTs over
// work_unit_investments logged after markMicros (serverNowMicros).
func statementsSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) []chaos7257Statement {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT type, exception_code, read_bytes, substring(query, 1, 4000) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_unit_investments'))
ORDER BY event_time_microseconds`, markMicros)
	if err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	defer rows.Close()
	var out []chaos7257Statement
	for rows.Next() {
		var s chaos7257Statement
		if err := rows.Scan(&s.Type, &s.Code, &s.ReadBytes, &s.Query); err != nil {
			t.Fatalf("scan query_log: %v", err)
		}
		out = append(out, s)
	}
	// A read that ends on a server exception returns no further rows; without
	// this check it would read as "no statement logged" instead of failing.
	if err := rows.Err(); err != nil {
		t.Fatalf("read query_log rows (measurement did not happen): %v", err)
	}
	return out
}

// serverNowMicros is the server's own clock in whole microseconds, so a
// query_log window never depends on the test host's clock or timezone, and
// never passes through a time.Time the driver would round to seconds.
func serverNowMicros(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) int64 {
	t.Helper()
	var now int64
	if err := direct.QueryRow(ctx, `SELECT toUnixTimestamp64Micro(now64(6))`).Scan(&now); err != nil {
		t.Fatalf("read server clock: %v", err)
	}
	return now
}

// The query_log window must keep a statement logged in the mark's own second.
// The table mirrors query_log's event_time_microseconds skip index (read from
// the server, never assumed), holds one row half a second into a second, and
// is asked for rows after a mark one microsecond before it: the row must come
// back. The control proves the fixture reproduces the hazard: the old bind (a
// time.Time through the driver's positional `?`) loses that row here.
func TestQueryLogWindowKeepsAStatementLoggedInTheMarksOwnSecondAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, direct := newScopedCHAOS7257Client(t, nil)

	// A fresh server creates system.query_log at its first flush.
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	var indexType string
	var granularity uint64
	if err := direct.QueryRow(ctx, `SELECT type, granularity FROM system.data_skipping_indices
WHERE database = 'system' AND table = 'query_log' AND expr = 'event_time_microseconds'`).Scan(&indexType, &granularity); err != nil {
		t.Fatalf("read query_log's event_time_microseconds skip index (the hazard's premise): %v", err)
	}
	if err := direct.Exec(ctx, fmt.Sprintf(`CREATE TABLE query_log_window (event_time DateTime, event_time_microseconds DateTime64(6),
  INDEX event_time_microseconds_index event_time_microseconds TYPE %s GRANULARITY %d) ENGINE = MergeTree ORDER BY event_time`, indexType, granularity)); err != nil {
		t.Fatalf("create query_log_window: %v", err)
	}
	logged := time.Date(2026, 9, 30, 19, 46, 5, 555189000, time.UTC)
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO query_log_window (event_time, event_time_microseconds)`)
	if err != nil {
		t.Fatalf("prepare query_log_window: %v", err)
	}
	if err := batch.Append(logged.Truncate(time.Second), logged); err != nil {
		t.Fatalf("append query_log_window: %v", err)
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send query_log_window: %v", err)
	}
	count := func(predicate string, mark any) uint64 {
		t.Helper()
		var n uint64
		if err := direct.QueryRow(ctx, `SELECT count() FROM query_log_window WHERE `+predicate, mark).Scan(&n); err != nil {
			t.Fatalf("count query_log_window WHERE %s: %v", predicate, err)
		}
		return n
	}
	for _, c := range []struct {
		name string
		mark int64
		want uint64
	}{
		{"mark 1us before the row, same second", logged.UnixMicro() - 1, 1},
		{"mark at the row (strictly after)", logged.UnixMicro(), 0},
		{"mark in the previous second", logged.Add(-time.Second).UnixMicro(), 1},
		{"mark at the second's first microsecond", logged.Truncate(time.Second).UnixMicro(), 1},
	} {
		if got := count(queryLogSinceMark, c.mark); got != c.want {
			t.Errorf("%s: %s kept %d rows, want %d", c.name, queryLogSinceMark, got, c.want)
		}
	}
	if got := count(`event_time_microseconds > ?`, logged.Add(-time.Microsecond)); got != 0 {
		t.Errorf("control: the old time.Time bind kept %d rows, want 0 -- the fixture no longer reproduces the skip-index hazard, so the window cases above prove nothing; re-derive it", got)
	}
}

func TestProjectThemeMixFitsTheClickHouseByteBudgetAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	checkProjectThemeMixByteBudget(t, chaos7257Units, false)
}

// CHAOS-7271: the same read on a table of twice the prod row count. Every
// statement of the read must stay under half of max_bytes_to_read: the cap is
// where a read fails, and prod's table is growing towards it.
func TestProjectThemeMixStaysUnderHalfTheByteBudgetAtTwiceTheTableAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	checkProjectThemeMixByteBudget(t, chaos7271GrowthUnits, true)
}

func checkProjectThemeMixByteBudget(t *testing.T, units int, growth bool) {
	t.Helper()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, func(o *runtimeclickhouse.Options) {
		cap := chaos7257ProdMaxBytesToRead
		o.MaxBytesToRead = &cap
		rows := uint(1_000_000)
		o.MaxResultRows = &rows
	})
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7257-bytes"
	shape := seedCHAOS7257ProdShape(t, ctx, direct, orgID, units)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)

	var table uint64
	if err := direct.QueryRow(ctx, `SELECT sum(data_uncompressed_bytes) FROM system.parts WHERE database = currentDatabase() AND table = 'work_unit_investments' AND active`).Scan(&table); err != nil {
		t.Fatalf("read table size: %v", err)
	}
	t.Logf("work_unit_investments uncompressed: %d bytes (%.1f MiB)", table, float64(table)/(1<<20))

	subjects := make([]contextfabric.SubjectRef, 0, len(shape.projects))
	for _, project := range shape.projects {
		subjects = append(subjects, projectSubject("linear", project))
	}
	mark := serverNowMicros(t, ctx, direct)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	statements := statementsSince(t, ctx, direct, mark)
	for i, s := range statements {
		t.Logf("statement %d: %s code=%d read_bytes=%d (%.1f MiB)", i, s.Type, s.Code, s.ReadBytes, float64(s.ReadBytes)/(1<<20))
	}
	if err != nil {
		t.Fatalf("ReadFacts under max_bytes_to_read=%d failed: %v\nserver: %s", chaos7257ProdMaxBytesToRead, err, lastServerException(ctx, direct))
	}
	if len(statements) == 0 {
		t.Fatal("measurement did not happen: query_log holds no statement over work_unit_investments")
	}
	// Every project is served complete: a mix source and the five shares.
	served := map[string]contextfabric.CanonicalFact{}
	for _, fact := range result.Facts {
		served[fact.Subject.Label] = fact
	}
	for _, project := range shape.projects {
		fact, ok := served[project]
		if !ok {
			t.Fatalf("project %s: no investment fact served (facts %d, reason %q)", project, len(result.Facts), result.Reason)
		}
		if source := factString(t, fact, "investment_mix_source"); source == "" {
			t.Fatalf("project %s: empty investment_mix_source", project)
		}
		if value, ok := fact.Fields["theme_feature_delivery"]; !ok || value.Number == nil {
			t.Fatalf("project %s: theme_feature_delivery = %#v, want a number", project, value)
		}
	}
	// CHAOS-7271: the read is phased (investment_project_mix_phased.go): per mix
	// a scope statement, then one statement per column group. No statement
	// reads every wide column, and the phases of one mix read at most
	// chaos7271MaxMixGrowth times what the one statement of that mix read on main.
	if len(statements) != chaos7271PhasedStatements {
		t.Fatalf("project investment read logged %d statements over work_unit_investments, want %d (roll-up: scope, repo arm, evidence arm; native: scope, placement, theme values, bugfix values)",
			len(statements), chaos7271PhasedStatements)
	}
	var rollupSum, nativeSum uint64
	for i, s := range statements {
		if s.Type != "QueryFinish" {
			t.Fatalf("statement %d ended %s code %d, want QueryFinish", i, s.Type, s.Code)
		}
		if s.ReadBytes >= chaos7257ProdMaxBytesToRead/2 {
			t.Errorf("statement %d read %d bytes = %.1f%% of the %d-byte cap, want under 50%%", i, s.ReadBytes, 100*float64(s.ReadBytes)/float64(chaos7257ProdMaxBytesToRead), chaos7257ProdMaxBytesToRead)
		}
		if i < chaos7271RollupStatements {
			rollupSum += s.ReadBytes
		} else {
			nativeSum += s.ReadBytes
		}
	}
	if !growth {
		// main 3307db10 read 39,384,274 and 39,687,121 bytes for the one
		// statement of each mix on this fixture (measured).
		if float64(rollupSum) > chaos7271MaxMixGrowth*chaos7271MainRollupBytes1x {
			t.Errorf("roll-up phases read %d bytes in total, more than %.2fx the %d the single statement read on main", rollupSum, chaos7271MaxMixGrowth, chaos7271MainRollupBytes1x)
		}
		if float64(nativeSum) > chaos7271MaxMixGrowth*chaos7271MainNativeBytes1x {
			t.Errorf("native phases read %d bytes in total, more than %.2fx the %d the single statement read on main", nativeSum, chaos7271MaxMixGrowth, chaos7271MainNativeBytes1x)
		}
	}
	t.Logf("per-mix totals: roll-up %d bytes, native %d bytes", rollupSum, nativeSum)

	// The fixture must reproduce the incident, or the assertions above prove
	// nothing: the two statements this change replaced (kept as the parity
	// oracles) fail on the same fixture under the same cap with Code 307.
	ids := make([]string, 0, len(shape.projects))
	for _, project := range shape.projects {
		ids = append(ids, "linear:"+project)
	}
	noRows := func(contextpacket.ClickHouseRowScanner) error { return nil }
	window := devhealthfacts.ProjectMixWindow{}
	for name, run := range map[string]func() error{
		"roll-up": func() error {
			return readers.QueryOrgScopedNamed(ctx, query, "OracleProjectRollup", devhealthfacts.OracleProjectRollupStatement(window), orgID, ids, noRows)
		},
		"native": func() error {
			return readers.QueryOrgScopedNamed(ctx, query, "OracleProjectNative", devhealthfacts.OracleProjectNativeStatement(window, 201), orgID, ids, noRows,
				readers.Binding{Name: "bugfix_key", Value: readers.BugfixSubcategoryKey})
		},
	} {
		err := run()
		if code, exceeded := runtimeclickhouse.QueryBudgetExceededCode(err); !exceeded || code != 307 {
			t.Errorf("the replaced %s statement did not fail with Code 307 under the %d-byte cap (err = %v): the fixture no longer reproduces the incident", name, chaos7257ProdMaxBytesToRead, err)
		} else {
			t.Logf("replaced %s statement: Code %d TOO_MANY_BYTES under the cap, as on prod", name, code)
		}
	}
}
