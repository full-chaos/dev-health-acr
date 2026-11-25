package devhealthsource_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const (
	c14624Org  = "a1462400-0000-4000-8000-00000000000a"
	c14624Seed = "toDateTime64('2025-03-03 00:00:00', 3, 'UTC')"
	// c14624ProjectorMemoryCap is cmd/acr-projector projectorStatementMaxMemoryUsage (runtime.go:407; package main, not importable).
	c14624ProjectorMemoryCap = 192 << 20
)

// Against a real server, the total count statements the projector's client
// sends run at 2 threads: query_log records max_threads 2 for each of them,
// and the counts are the counts of the same statements without the pin.
func TestCHAOS14624TotalCountStatementsRunAtTwoThreads(t *testing.T) {
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
	// The projector's client stack: the hosted read budget, the projector memory cap and the statement threads (runtime.go:428-440).
	maxBytes := config.DefaultClickHouseMaxBytesToRead
	raw, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: &maxBytes,
	})
	if err != nil {
		t.Fatalf("open the projector query client: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	projector := statementthreads.Capped(raw, c14624ProjectorMemoryCap)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	exec := func(statement string) {
		t.Helper()
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	repo := func(expr string) string {
		return fmt.Sprintf("toUUID(concat('a1462400', '-0000-4000-8000-', leftPad(toString(%s), 12, '0')))", expr)
	}
	// devhealthschema:not-a-production-replica the table names here fill tables that productionSchemaDDL rendered above; no schema is defined in this file
	exec(fmt.Sprintf("INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT %s, '%s', concat('acme/r', toString(number)), 'github', %s FROM numbers(3)", repo("number"), c14624Org, c14624Seed))
	exec(fmt.Sprintf("INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at, last_synced) SELECT concat('W-', toString(number)), %s, '%s', 'github', 't', 'open', '', '', '', %s, %s FROM numbers(5)", repo("number % 3"), c14624Org, c14624Seed, c14624Seed))
	exec(fmt.Sprintf("INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) SELECT %s, toUInt32(number + 1), 'pr', 'open', %s, %s, '%s' FROM numbers(6)", repo("number % 3"), c14624Seed, c14624Seed, c14624Org))
	exec(fmt.Sprintf("INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id) SELECT %s, toUInt32(number + 1), concat('rev-', toString(number)), 'APPROVED', %s, %s, '%s' FROM numbers(4)", repo("number % 3"), c14624Seed, c14624Seed, c14624Org))
	exec(fmt.Sprintf("INSERT INTO deployments (repo_id, deployment_id, status, environment, deployed_at, started_at, last_synced, org_id) SELECT %s, concat('D-', toString(number)), 'success', 'prod', %s, %s, %s, '%s' FROM numbers(4)", repo("number % 3"), c14624Seed, c14624Seed, c14624Seed, c14624Org))
	exec(fmt.Sprintf("INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) SELECT %s, concat('R-', toString(number)), 'success', %s, %s, '%s' FROM numbers(7)", repo("number % 3"), c14624Seed, c14624Seed, c14624Org))
	exec(fmt.Sprintf("INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, service_id, repo_id, is_active) SELECT '%s', %s, concat('map-', toString(number)), concat('svc-', toString(number)), %s, 1 FROM numbers(2)", c14624Org, c14624Seed, repo("number")))
	exec(fmt.Sprintf("INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, is_deleted) SELECT '%s', %s, concat('inc-', toString(number)), %s, %s, concat('svc-', toString(number %% 2)), 'i', 0 FROM numbers(3)", c14624Org, c14624Seed, c14624Seed, c14624Seed))
	exec(fmt.Sprintf("INSERT INTO teams (id, name, updated_at, org_id, provider, project_keys, is_active) SELECT concat('team-', toString(number)), 'n', %s, '%s', 'github', [], 1 FROM numbers(2)", c14624Seed, c14624Org))
	exec(fmt.Sprintf("INSERT INTO projects (id, org_id, provider, name, is_active, state, url, updated_at) SELECT concat('P-', toString(number)), '%s', 'github', 'p', 1, 'backlog', '', %s FROM numbers(2)", c14624Org, c14624Seed))

	// The counts of the same statements without the pin, through the same client under another reader.
	var baseline []uint64
	for i, statement := range devhealthsource.TotalCountStatementsForTest() {
		rows, err := projector.Query(contextpacket.WithStatementReader(ctx, "chaos14624_baseline"), statement, []contextpacket.ClickHouseBinding{{Name: "org_id", Value: c14624Org}})
		if err != nil {
			t.Fatalf("baseline statement %d: %v", i, err)
		}
		var total uint64
		for rows.Next() {
			if err := rows.Scan(&total); err != nil {
				t.Fatalf("baseline statement %d scan: %v", i, err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("baseline statement %d: %v", i, err)
		}
		_ = rows.Close()
		baseline = append(baseline, total)
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(projector)
	if err != nil {
		t.Fatal(err)
	}
	teamsSource, err := devhealthsource.NewTeamsProjectsSource(projector, true)
	if err != nil {
		t.Fatal(err)
	}
	counted := 0
	sum := map[string]uint64{}
	for _, read := range []func(context.Context, string) (map[string]int64, error){
		func(ctx context.Context, org string) (map[string]int64, error) {
			counts, err := source.ProjectionSourceCounts(ctx, org)
			out := map[string]int64{}
			for k, v := range counts {
				out[string(k)] = v
			}
			return out, err
		},
		func(ctx context.Context, org string) (map[string]int64, error) {
			counts, err := teamsSource.ProjectionSourceCounts(ctx, org)
			out := map[string]int64{}
			for k, v := range counts {
				out[string(k)] = v
			}
			return out, err
		},
	} {
		counts, err := read(ctx, c14624Org)
		if err != nil {
			t.Fatalf("ProjectionSourceCounts: %v", err)
		}
		for kind, n := range counts {
			counted++
			sum[kind] = uint64(n)
		}
	}
	var total, want uint64
	for _, n := range sum {
		total += n
	}
	for _, n := range baseline {
		want += n
	}
	if counted != len(baseline) || total != want || want == 0 {
		t.Fatalf("ProjectionSourceCounts: %d kinds, total %d; the unpinned statements: %d statements, total %d", counted, total, len(baseline), want)
	}
	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Fatal(err)
	}
	rows, err := direct.Query(ctx, "SELECT Settings['max_threads'] AS threads, count() FROM system.query_log WHERE type = 'QueryFinish' AND current_database = currentDatabase() AND log_comment = 'projector_count_check_source_count' GROUP BY threads ORDER BY threads")
	if err != nil {
		t.Fatal(err)
	}
	logged := map[string]uint64{}
	for rows.Next() {
		var threads string
		var n uint64
		if err := rows.Scan(&threads, &n); err != nil {
			t.Fatal(err)
		}
		logged[threads] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(logged) != 1 || logged["2"] != uint64(len(baseline)) {
		t.Fatalf("query_log max_threads of the %d total count statements: %v, want all at \"2\"", len(baseline), logged)
	}
	t.Logf("TOTALS %d statements at max_threads 2, counts %v = the unpinned statements (total %d)", len(baseline), sum, want)
}
