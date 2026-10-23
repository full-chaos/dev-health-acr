package devhealthsource_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const (
	ciRunsOrganization  = "86830000-0000-4000-8000-0000000010a8"
	ciRunsRepositories  = 100
	ciRunsRows          = 60000
	ciRunsForeignOrgs   = 20
	ciRunsForeignRepos  = 1500
	ciRunsPageLimit     = 500
	ciRunsWholeReadRows = 200000
)

// TestLiveCIRunsPageReadMemory reads CI run pages through main's statement and
// through the current one on a store shaped like prod's: one organization with
// many runs in several unmerged versions, beside other organizations holding
// many more repositories. Every statement variant must return the same rows as
// main's for every cursor, and the current statement must peak well below
// main's, as the server's own query log measures it.
//
// Needs Docker (ClickHouse container). Written to be run by CI.
func TestLiveCIRunsPageReadMemory(t *testing.T) {
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
	inner, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxResultRows: &unlimitedRows, MaxBytesToRead: &unlimitedBytes,
	})
	if err != nil {
		t.Fatalf("open the query client: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	reader := statementthreads.Unbounded(inner)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	for _, table := range []string{"ci_pipeline_runs", "repos"} {
		if err := direct.Exec(ctx, "SYSTEM STOP MERGES "+table); err != nil {
			t.Fatalf("stop merges on %s: %v", table, err)
		}
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedCIRunsStore(t, ctx, direct, base)

	cursors := []struct {
		name  string
		since time.Time
		after string
	}{
		{name: "catch-up"},
		{name: "mid", since: base.Add(30000 * time.Second), after: "x"},
		{name: "steady", since: base.Add(time.Duration(ciRunsRows-100) * time.Second)},
		{name: "future", since: base.Add(60 * 24 * time.Hour)},
	}
	variants := []string{"legacy", "current", "joinpruned", "prewhere", "joinpruned+prewhere"}
	for _, cursor := range cursors {
		var want []string
		for _, variant := range variants {
			name := fmt.Sprintf("ci-%s-%s", cursor.name, variant)
			got, err := devhealthsource.ReadCIRunsForTest(contextpacket.WithStatementReader(ctx, name), reader, variant, ciRunsOrganization, cursor.since, cursor.after, ciRunsWholeReadRows)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if variant == "legacy" {
				want = got
				if cursor.name != "future" && len(want) == 0 {
					t.Fatalf("%s: main's statement returned no row: the seed does not exercise the read", name)
				}
				continue
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: %d rows differ from main's %d rows", name, len(got), len(want))
			}
			page, err := devhealthsource.ReadCIRunsForTest(ctx, reader, variant, ciRunsOrganization, cursor.since, cursor.after, ciRunsPageLimit)
			if err != nil {
				t.Errorf("%s page: %v", name, err)
			} else if len(page) > ciRunsPageLimit+1 || !slices.Equal(page, want[:min(len(want), len(page))]) {
				t.Errorf("%s page: not a prefix of main's rows", name)
			}
		}
	}

	if err := admin.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatalf("flush the query log: %v", err)
	}
	var unbound uint64
	if err := admin.QueryRow(ctx, "SELECT "+statementthreads.UnboundThreadsCount+" FROM system.query_log WHERE current_database = ? AND type = 'QueryFinish' AND log_comment LIKE 'ci-%'", database).Scan(&unbound); err != nil {
		t.Fatalf("read the statement threads from the query log: %v", err)
	}
	statementthreads.RequireThreadBound(t, ctx, admin, "CI run statements", unbound)
	rows, err := admin.Query(ctx, `SELECT log_comment, max(memory_usage), max(read_rows), max(read_bytes), count()
FROM system.query_log WHERE current_database = ? AND type = 'QueryFinish' AND log_comment LIKE 'ci-%' GROUP BY log_comment ORDER BY log_comment`, database)
	if err != nil {
		t.Fatalf("read the query log: %v", err)
	}
	defer rows.Close()
	var table strings.Builder
	fmt.Fprintf(&table, "%-34s %14s %12s %14s %6s\n", "statement", "memory_usage", "read_rows", "read_bytes", "runs")
	measured := map[string]uint64{}
	for rows.Next() {
		var comment string
		var memory, readRows, readBytes, runs uint64
		if err := rows.Scan(&comment, &memory, &readRows, &readBytes, &runs); err != nil {
			t.Fatalf("scan the query log: %v", err)
		}
		measured[comment] = memory
		fmt.Fprintf(&table, "%-34s %14d %12d %14d %6d\n", comment, memory, readRows, readBytes, runs)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the query log: %v", err)
	}
	t.Log("\n" + table.String())
	if len(measured) == 0 {
		t.Fatal("the query log holds no CI run statement: the measure did not happen")
	}
	for _, cursor := range cursors {
		legacy, current := measured["ci-"+cursor.name+"-legacy"], measured["ci-"+cursor.name+"-current"]
		if legacy == 0 || current == 0 {
			t.Errorf("%s: no memory value for main's statement (%d) or the current one (%d)", cursor.name, legacy, current)
		}
	}
}

func seedCIRunsStore(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, base time.Time) {
	t.Helper()
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v\n%s", label, err, statement)
		}
	}
	repoUUID := func(prefix, n string) string {
		return fmt.Sprintf("toUUID(concat('86830000-0000-4000-%s-', leftPad(toString(%s), 12, '0')))", prefix, n)
	}
	exec("repositories", fmt.Sprintf(`INSERT INTO repos (id, org_id, repo, provider, created_at, last_synced)
SELECT %s, ?, concat('acme/', repeat('r', 120), toString(number)), 'github', toDateTime64(?, 3, 'UTC'), toDateTime64(?, 3, 'UTC') FROM numbers(%d)`,
		repoUUID("8000", "number"), ciRunsRepositories), ciRunsOrganization, base, base)
	for org := 0; org < ciRunsForeignOrgs; org++ {
		exec("foreign repositories", fmt.Sprintf(`INSERT INTO repos (id, org_id, repo, provider, created_at, last_synced)
SELECT %s, ?, concat('other%d/', repeat('f', 120), toString(number)), 'github', toDateTime64(?, 3, 'UTC'), toDateTime64(?, 3, 'UTC') FROM numbers(%d)`,
			repoUUID("9000", fmt.Sprintf("number + %d", (org+1)*100000)), org, ciRunsForeignRepos), fmt.Sprintf("86830000-0000-4000-9000-%012d", org), base, base)
	}
	runs := func(label, filter, status, shift string) {
		exec(label, fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, finished_at, last_synced, org_id, pipeline_name, branch)
SELECT %s, concat('run-', toString(number)), ?, toDateTime64(?, 3, 'UTC'), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number) + %s, ?, 'ci', 'main'
FROM numbers(%d) WHERE %s`, repoUUID("8000", fmt.Sprintf("number %% %d", ciRunsRepositories)), shift, ciRunsRows, filter), status, base, base, base, ciRunsOrganization)
	}
	runs("first versions", "1 = 1", "running", "toIntervalSecond(0)")
	runs("second versions", "number % 2 = 0", "success", "toIntervalSecond(3)")
	// Version-column semantics: a run whose newest version arrives first with a
	// later stamp, an older stamp arriving later, and two versions of one stamp.
	exec("late older stamp", `INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES
(toUUID('86830000-0000-4000-8000-000000000001'), 'late', 'newer-stamp-first', ?, ?, ?)`, base, base.Add(40000*time.Second), ciRunsOrganization)
	exec("late older stamp, arriving second", `INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES
(toUUID('86830000-0000-4000-8000-000000000001'), 'late', 'older-stamp-last', ?, ?, ?)`, base, base.Add(10*time.Second), ciRunsOrganization)
	for _, status := range []string{"tie-a", "tie-b"} {
		exec("tied stamps", `INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES
(toUUID('86830000-0000-4000-8000-000000000002'), 'tie', ?, ?, ?, ?)`, status, base, base.Add(50000*time.Second), ciRunsOrganization)
	}
}
