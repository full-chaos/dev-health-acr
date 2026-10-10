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

// TestLiveRepositoryStartIgnoresSupersededVersionsBeforeTheyMerge: the two
// evidence tables are ReplacingMergeTree on last_synced, and until parts merge
// an obsolete version of a pull request or a work item sits beside the current
// one. The seeded rows are never merged (merges are stopped, no OPTIMIZE), so a
// minimum taken over every stored version reads the obsolete created_at.
//
// Needs Docker (ClickHouse container); CI runs it.
func TestLiveRepositoryStartIgnoresSupersededVersionsBeforeTheyMerge(t *testing.T) {
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
	reader, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/" + database, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the query client: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	// devhealthschema:not-a-production-replica these statements stop merges on tables the production DDL created; no column, type or engine is declared here.
	for _, table := range []string{"git_pull_requests", "work_items"} {
		if err := admin.Exec(ctx, "SYSTEM STOP MERGES "+database+"."+table); err != nil {
			t.Fatalf("stop merges on %s: %v", table, err)
		}
	}

	const orgID = "86830000-0000-4000-8000-000000009130"
	const repoID = "86830000-0000-4000-8000-0000000091b1"
	synced := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	obsolete := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	current := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
	superseded := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, repoID, orgID, "acme/versions", "github", synced, synced); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
	for _, version := range []struct {
		created, lastSynced time.Time
	}{{obsolete, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, {current, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}} {
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (toUUID(?), ?, 1, 'PR', 'open', ?, ?)`, repoID, orgID, version.created, version.lastSynced); err != nil {
			t.Fatalf("seed pull request version: %v", err)
		}
	}
	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables the production DDL created; no column, type or engine is declared here.
	for _, version := range []struct {
		created, lastSynced time.Time
	}{{obsolete.Add(time.Hour), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, {superseded, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}} {
		if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, created_at, updated_at, last_synced, org_id) VALUES (toUUID(?), 'ITEM-1', 'github', 'item', 'task', 'open', ?, ?, ?, ?)`, repoID, version.created, version.lastSynced, version.lastSynced, orgID); err != nil {
			t.Fatalf("seed work item version: %v", err)
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
	requireStart(t, starts, "repository:"+repoID, &current)
}
