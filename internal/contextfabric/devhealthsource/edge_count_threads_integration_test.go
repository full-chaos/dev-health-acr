package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const (
	edgeThreadsOrg  = "74000000-0000-4000-8000-000000000001"
	edgeThreadsRepo = "74000000-0000-4000-8000-000000000002"

	edgeThreadsSubjects = 60000
	edgeThreadsRuns     = 5
	// edgeThreadsProjectorCap is the projector's statement memory cap.
	edgeThreadsProjectorCap = 192 << 20
	// edgeThreadsPeakBound is the work bound of the shipped statement: the
	// peak memory the server measures for it. See the PROFILE line of a run.
	edgeThreadsPeakBound = 1 << 40
)

// edgeThreadsLogged reads one finished statement of the query log and fails
// when it did not run at or under the statement thread count.
func edgeThreadsLogged(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label string) edgeThreadsRun {
	t.Helper()
	var run edgeThreadsRun
	var unbound uint64
	if err := direct.QueryRow(ctx, `SELECT memory_usage, read_rows, query_duration_ms, Settings['max_threads'], `+statementthreads.UnboundThreads+` FROM system.query_log WHERE type = 'QueryFinish' AND log_comment = ? AND current_database = currentDatabase() ORDER BY event_time_microseconds DESC LIMIT 1`, label).Scan(&run.peak, &run.readRows, &run.millis, &run.threads, &unbound); err != nil {
		t.Fatalf("%s: the query log holds no finished statement: %v", label, err)
	}
	statementthreads.RequireThreadBound(t, ctx, direct, label, unbound)
	return run
}

type edgeThreadsRun struct {
	counts   map[string]uint64
	peak     uint64
	readRows uint64
	millis   uint64
	threads  string
}

func edgeThreadsMeasure(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, statement, label string) map[string]uint64 {
	t.Helper()
	rows, err := client.Query(contextpacket.WithStatementReader(ctx, label), statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: edgeThreadsOrg}})
	if err != nil {
		t.Fatalf("%s: run the edge count: %v", label, err)
	}
	defer rows.Close()
	counts := map[string]uint64{}
	for rows.Next() {
		var arm string
		var total uint64
		if err := rows.Scan(&arm, &total); err != nil {
			t.Fatalf("%s: scan: %v", label, err)
		}
		counts[arm] = total
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: rows: %v", label, err)
	}
	return counts
}

// TestLiveMembershipEdgeCountPeakAndThreads runs the BELONGS_TO_PROJECT edge
// count as the projector runs it (the 192 MiB cap, the statement thread count)
// on a work_items table of one big part and several newer small parts that
// overlap its key range, and a transitions table of several parts. The work
// bound is the peak memory the server measures; the count stays equal to the
// same statement at four threads.
func TestLiveMembershipEdgeCountPeakAndThreads(t *testing.T) {
	ctx := context.Background()
	addr, admin := sharedDevHealthClickHouse(t)
	database := fmt.Sprintf("t%d", perTestDatabaseSeq.Add(1))
	if err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create per-test database %s: %v", database, err)
	}
	t.Cleanup(func() { _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" SYNC") })
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: database, Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	var unlimitedRows uint
	var unlimitedBytes uint64
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxResultRows: &unlimitedRows, MaxBytesToRead: &unlimitedBytes,
	})
	if err != nil {
		t.Fatalf("open the query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	for _, table := range []string{"work_items", "project_membership_transitions"} {
		if err := direct.Exec(ctx, "SYSTEM STOP MERGES "+table); err != nil {
			t.Fatalf("stop merges on %s: %v", table, err)
		}
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pid := func(expr string) string {
		return fmt.Sprintf("concat('75000000-0000-4000-8000-', leftPad(toString(%s), 12, '0'))", expr)
	}
	seed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/threads', 'linear', ?)`, edgeThreadsRepo, edgeThreadsOrg, base)
	for i := 0; i < 12; i++ {
		seed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', 'linear', 'backlog', '', 1, ?)`,
			fmt.Sprintf("75000000-0000-4000-8000-%012d", i), edgeThreadsOrg, fmt.Sprintf("P%d", i), base)
	}
	workItems := func(label, idExpr string, count int, minutes int) {
		seed(label, fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, ingested_at, last_synced)
SELECT concat('linear:W-', leftPad(toString(%s), 8, '0')), toUUID(?), ?, 't', 'open', '', '', 'linear', %s, toDateTime64(?, 3, 'UTC') + toIntervalSecond(number), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number + 3), toDateTime64(?, 3, 'UTC') + toIntervalMinute(%d) FROM numbers(%d)`, idExpr, pid("number % 12"), minutes, count),
			edgeThreadsRepo, edgeThreadsOrg, base, base, base)
	}
	workItems("big work items part", "number", edgeThreadsSubjects, 0)
	for part := 1; part <= 3; part++ {
		workItems(fmt.Sprintf("overlapping newer part %d", part), fmt.Sprintf("number * %d + %d", 997+part, part), 40, part)
	}
	for step, pair := range [][2]string{{"''", pid("number % 12")}, {pid("number % 12"), pid("(number + 1) % 12")}, {pid("(number + 1) % 12"), pid("number % 12")}, {"''", pid("(number + 5) % 12")}} {
		seed("transitions", fmt.Sprintf(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
SELECT ?, NULL, toUUID(?), 'work_item', concat('linear:H-', toString(number)), 'linear', %s, %s, '', '', '', toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d), concat('ev-', toString(number), '-%d'), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number * 7 + %d + 11) FROM numbers(%d)`,
			pair[0], pair[1], step, step, step, step, edgeThreadsSubjects/2), edgeThreadsOrg, edgeThreadsRepo, base, base, base)
	}
	var workParts, transitionParts uint64
	if err := direct.QueryRow(ctx, `SELECT countIf(table = 'work_items'), countIf(table = 'project_membership_transitions') FROM system.parts WHERE database = currentDatabase() AND active`).Scan(&workParts, &transitionParts); err != nil {
		t.Fatal(err)
	}
	t.Logf("PROFILE parts work_items=%d project_membership_transitions=%d", workParts, transitionParts)
	if workParts < 2 || transitionParts < 3 {
		t.Fatalf("the fixture is not the overlapping-part shape: %d work_items parts, %d transitions parts", workParts, transitionParts)
	}

	capped := statementthreads.Capped(query, edgeThreadsProjectorCap)
	shipped, _ := devhealthsource.MembershipEdgeCountSQLForTest(true)
	fourThreads := strings.TrimSuffix(shipped, "\nSETTINGS max_threads = 1")
	if fourThreads == shipped {
		t.Log("PROFILE the shipped statement carries no SETTINGS max_threads = 1")
	}
	runs := map[string][]string{}
	var firstCounts, otherCounts map[string]uint64
	for i := 0; i < edgeThreadsRuns; i++ {
		for _, c := range []struct{ label, statement string }{{"shipped", shipped}, {"four", fourThreads}} {
			label := fmt.Sprintf("edge-threads-%s-%d", c.label, i)
			counts := edgeThreadsMeasure(t, ctx, capped, c.statement, label)
			runs[c.label] = append(runs[c.label], label)
			if c.label == "shipped" && firstCounts == nil {
				firstCounts = counts
			}
			if c.label == "four" && otherCounts == nil {
				otherCounts = counts
			}
		}
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	read := func(kind string) (peak uint64, medianMillis uint64, readRows uint64, threads string) {
		var millis []uint64
		for _, label := range runs[kind] {
			run := edgeThreadsLogged(t, ctx, direct, label)
			if run.peak > peak {
				peak = run.peak
			}
			readRows, threads = run.readRows, run.threads
			millis = append(millis, run.millis)
		}
		sort.Slice(millis, func(i, j int) bool { return millis[i] < millis[j] })
		return peak, millis[len(millis)/2], readRows, threads
	}
	shippedPeak, shippedMillis, shippedRows, shippedThreads := read("shipped")
	fourPeak, fourMillis, fourRows, fourLogged := read("four")
	t.Logf("PROFILE shipped: peak=%d median_ms=%d read_rows=%d max_threads=%q", shippedPeak, shippedMillis, shippedRows, shippedThreads)
	t.Logf("PROFILE four threads: peak=%d median_ms=%d read_rows=%d max_threads=%q", fourPeak, fourMillis, fourRows, fourLogged)

	if len(firstCounts) != 2 || firstCounts["transition"] == 0 || firstCounts["work_item_column"] == 0 {
		t.Fatalf("the seed counts no edge in each arm: %v", firstCounts)
	}
	if fmt.Sprint(firstCounts) != fmt.Sprint(otherCounts) {
		t.Errorf("the edge count differs between the shipped statement and four threads: %v vs %v", firstCounts, otherCounts)
	}
	if fourPeak <= edgeThreadsPeakBound {
		t.Fatalf("the bound is vacuous: four threads peak at %d, not over %d", fourPeak, edgeThreadsPeakBound)
	}
	if shippedPeak > edgeThreadsPeakBound {
		t.Errorf("the shipped edge count peaked at %d bytes, want at most %d", shippedPeak, edgeThreadsPeakBound)
	}
	if shippedThreads != "1" {
		t.Errorf("the shipped edge count ran with max_threads %q in the query log, want 1", shippedThreads)
	}
}
