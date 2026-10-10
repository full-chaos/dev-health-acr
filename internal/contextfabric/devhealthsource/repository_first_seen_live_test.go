package devhealthsource_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestLiveRepositoryStartIsTheFirstSeenNotTheSyncStamp runs the real source
// against a real ClickHouse under a max_bytes_to_read limit that the wide
// pull request bodies exceed. Three repositories are re-stamped (created_at =
// last_synced): one with an early pull request and an earlier work item, one
// with a pull request only, one with nothing. A fourth carries a real
// created_at. The evidence read must succeed under the limit (it reads
// narrow columns), serve the page in one statement, and set each start; the
// read's bytes, rows and duration are logged from the query log.
//
// Needs Docker (ClickHouse container); CI runs it.
func TestLiveRepositoryStartIsTheFirstSeenNotTheSyncStamp(t *testing.T) {
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
	limit := pullRequestReadByteLimit
	reader, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second, MaxBytesToRead: &limit,
	})
	if err != nil {
		t.Fatalf("open the limited query client: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}

	const orgID = "86830000-0000-4000-8000-000000009129"
	repos := map[string]string{
		"both":    "86830000-0000-4000-8000-0000000091a1",
		"pull":    "86830000-0000-4000-8000-0000000091a2",
		"nothing": "86830000-0000-4000-8000-0000000091a3",
		"real":    "86830000-0000-4000-8000-0000000091a4",
		"epoch":   "86830000-0000-4000-8000-0000000091a5",
	}
	synced := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	firstPull := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	firstItem := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	realCreated := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	for name, id := range repos {
		created := synced
		if name == "real" {
			created = realCreated
		}
		// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, id, orgID, "acme/"+name, "github", created, synced); err != nil {
			t.Fatalf("seed repo %s: %v", name, err)
		}
	}
	const pulls = 20000
	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
	for _, name := range []string{"both", "pull"} {
		// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, body, created_at, last_synced)
SELECT toUUID(?), ?, toUInt32(number + 1), 'PR', 'open', repeat('b', 1000), toDateTime64(?, 3, 'UTC') + toIntervalSecond(number), toDateTime64(?, 3, 'UTC')
FROM numbers(?)`, repos[name], orgID, firstPull, synced, pulls/2); err != nil {
			t.Fatalf("seed pull requests of %s: %v", name, err)
		}
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced)
VALUES (toUUID(?), ?, 1, 'PR', 'open', toDateTime64(0, 3, 'UTC'), ?)`, repos["epoch"], orgID, synced); err != nil {
		t.Fatalf("seed an unset-created_at pull request: %v", err)
	}
	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
	if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, created_at, updated_at, last_synced, org_id)
VALUES (toUUID(?), 'ITEM-1', 'github', 'first', 'task', 'open', ?, ?, ?, ?)`, repos["both"], firstItem, synced, synced, orgID); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
	for _, table := range []string{"git_pull_requests", "work_items", "repos"} {
		if err := direct.Exec(ctx, "OPTIMIZE TABLE "+table+" FINAL"); err != nil {
			t.Fatalf("merge the seeded parts of %s: %v", table, err)
		}
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(reader)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, _, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	starts := map[string]*time.Time{}
	for _, entity := range batch.Entities {
		if entity.Subject.Kind == contextfabric.SubjectRepository {
			starts[entity.Subject.CanonicalID] = entity.ValidFrom
		}
	}
	requireStart(t, starts, "repository:"+repos["both"], &firstItem)
	requireStart(t, starts, "repository:"+repos["pull"], &firstPull)
	requireStart(t, starts, "repository:"+repos["nothing"], nil)
	requireStart(t, starts, "repository:"+repos["real"], &realCreated)
	requireStart(t, starts, "repository:"+repos["epoch"], nil)

	if err := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		t.Logf("read_stats unavailable: flush logs: %v", err)
		return
	}
	rows, err := direct.Query(ctx, `SELECT read_rows, read_bytes, query_duration_ms FROM system.query_log
WHERE type = 'QueryFinish' AND current_database = ? AND query LIKE '%AS repo_id_text%' ORDER BY event_time DESC LIMIT 1`, database)
	if err != nil {
		t.Logf("read_stats unavailable: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var readRows, readBytes uint64
		var durationMs uint64
		if err := rows.Scan(&readRows, &readBytes, &durationMs); err != nil {
			t.Logf("read_stats unavailable: %v", err)
			return
		}
		t.Logf("read_stats of the first-seen read (%d pull requests, 1 work item, 4 repositories): read_rows=%d read_bytes=%d query_duration_ms=%d", pulls, readRows, readBytes, durationMs)
	}
}
