package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// productionSchemaDDL is CHAOS-3789's live-schema-parity fixture. Every
// column type below was read directly off production ClickHouse via
// system.columns (dev-health-clickhouse-1, 2026-08-13), restricted to the
// columns internal/contextfabric/devhealthsource/tables.go's producers
// actually SELECT. CHAOS-3789 exists because this same package's own
// hand-authored fixtures (this file's sibling
// clickhouse_org_isolation_integration_test.go, and clickhouse_test.go's
// fakeScanner) modeled git_pull_requests.number and
// git_pull_request_reviews.number as int64/Int64 when the real column is
// UInt32, and no test ever caught the drift -- the class this test closes,
// not just the one column.
//
// One table per entityTables entry (tables.go) -- keep this list and that
// one in sync; a table added there without a row here silently drops out
// of TestLiveSchemaParityAcrossEveryProducer's coverage assertion below,
// which fails loudly if that happens.
var productionSchemaDDL = []string{
	`CREATE TABLE repos (id UUID, org_id String, repo String, provider String, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY (org_id, id)`,
	`CREATE TABLE work_items (work_item_id String, repo_id UUID, org_id String, title String, status String, url String, parent_id String, updated_at DateTime64(3)) ENGINE = ReplacingMergeTree ORDER BY (org_id, work_item_id)`,
	`CREATE TABLE git_pull_requests (repo_id UUID, org_id String, number UInt32, title Nullable(String), state Nullable(String), last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY (org_id, repo_id, number)`,
	`CREATE TABLE deployments (repo_id UUID, org_id String, deployment_id String, status Nullable(String), environment Nullable(String), deployed_at Nullable(DateTime64(3, 'UTC')), started_at Nullable(DateTime64(3, 'UTC')), last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY deployment_id`,
	`CREATE TABLE operational_incidents (id String, org_id String, service_id Nullable(String), title String, normalized_status Nullable(String), raw_status Nullable(String), normalized_severity Nullable(String), raw_severity Nullable(String), started_at Nullable(DateTime64(6, 'UTC')), source_event_at Nullable(DateTime64(6, 'UTC')), observed_at DateTime64(6, 'UTC'), is_deleted UInt8) ENGINE = ReplacingMergeTree ORDER BY id`,
	`CREATE TABLE operational_service_repository_mappings (org_id String, service_id String, repo_id Nullable(UUID), is_active UInt8) ENGINE = ReplacingMergeTree ORDER BY (org_id, service_id)`,
	`CREATE TABLE work_item_dependencies (source_work_item_id String, target_work_item_id String, relationship_type String, org_id String, last_synced DateTime64(3)) ENGINE = ReplacingMergeTree ORDER BY (org_id, source_work_item_id, target_work_item_id)`,
	`CREATE TABLE work_graph_deployment_incident_edges (edge_id String, deployment_id String, incident_id String, repo_id Nullable(UUID), org_id UUID, observed_at DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY edge_id`,
	`CREATE TABLE git_pull_request_reviews (review_id String, repo_id UUID, org_id String, number UInt32, state String, submitted_at DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY (org_id, review_id)`,
	`CREATE TABLE ci_pipeline_runs (run_id String, repo_id UUID, org_id String, branch Nullable(String), status Nullable(String), started_at DateTime64(3, 'UTC'), finished_at Nullable(DateTime64(3, 'UTC'))) ENGINE = ReplacingMergeTree ORDER BY (org_id, run_id)`,
}

// TestLiveSchemaParityAcrossEveryProducer seeds one real ClickHouse
// (production-typed, per productionSchemaDDL) row per table entityTables
// reads, runs the actual NextProjectionBatch path (not a fake), and asserts
// every table contributed at least one candidate. A Scan() type mismatch
// against any producer's column -- the CHAOS-3789 class of bug -- surfaces
// here as a query error instead of on a live organization.
func TestLiveSchemaParityAcrossEveryProducer(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	const orgID = "10000000-0000-4000-8000-000000000001"
	const repoID = "20000000-0000-4000-8000-000000000002"
	const repoSlug = "acme/parity-service"

	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}

	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}

	mustSeed("repos", `INSERT INTO repos VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, repoSlug, "github", at)
	mustSeed("work_items parent", `INSERT INTO work_items VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-PARENT", repoID, orgID, "Parent work item", "open", "", "", at)
	mustSeed("work_items child", `INSERT INTO work_items VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-CHILD", repoID, orgID, "Child work item", "open", "", "WI-PARENT", at)
	mustSeed("git_pull_requests", `INSERT INTO git_pull_requests VALUES (?, ?, ?, ?, ?, ?)`,
		repoID, orgID, uint32(4242), "Parity PR", "open", at)
	mustSeed("deployments", `INSERT INTO deployments VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID, orgID, "deploy-parity-1", "success", "production", at, at, at)
	mustSeed("operational_service_repository_mappings", `INSERT INTO operational_service_repository_mappings VALUES (?, ?, ?, ?)`,
		orgID, "svc-parity", repoID, uint8(1))
	mustSeed("operational_incidents", `INSERT INTO operational_incidents VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"incident-parity-1", orgID, "svc-parity", "Parity incident", "open", "open", "low", "low", at, at, at, uint8(0))
	mustSeed("work_item_dependencies", `INSERT INTO work_item_dependencies VALUES (?, ?, ?, ?, ?)`,
		"WI-CHILD", "WI-PARENT", "blocks", orgID, at)
	mustSeed("work_graph_deployment_incident_edges", `INSERT INTO work_graph_deployment_incident_edges VALUES (?, ?, ?, ?, ?, ?)`,
		"edge-parity-1", "deploy-parity-1", "incident-parity-1", repoID, orgID, at)
	mustSeed("git_pull_request_reviews", `INSERT INTO git_pull_request_reviews VALUES (?, ?, ?, ?, ?, ?)`,
		"review-parity-1", repoID, orgID, uint32(4242), "approved", at)
	mustSeed("ci_pipeline_runs", `INSERT INTO ci_pipeline_runs VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"run-parity-1", repoID, orgID, "main", "success", at, at)

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch against production-typed schema: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	// One canonical ID per table this fixture seeded, keyed by the table
	// name entityTables (tables.go) uses -- proof that table's producer
	// actually scanned a row, not just that the batch as a whole is
	// non-empty.
	wantCanonicalID := map[string]string{
		"repos":                                "repository:" + repoID,
		"work_items":                           "work_item:WI-CHILD",
		"git_pull_requests":                    "pull_request:" + repoID + ":4242",
		"deployments":                          "deployment:deploy-parity-1",
		"operational_incidents":                "incident:incident-parity-1",
		"work_item_dependencies":               "relationship:work_item_dependency:WI-CHILD:WI-PARENT:blocks",
		"work_items_hierarchy":                 "relationship:work_item_hierarchy:WI-CHILD:WI-PARENT",
		"work_graph_deployment_incident_edges": "relationship:deployment_incident:edge-parity-1",
		"git_pull_request_reviews":             "pull_request_review:review-parity-1",
		"ci_pipeline_runs":                     "ci_pipeline_run:run-parity-1",
	}

	seen := map[string]bool{}
	for _, entity := range batch.Entities {
		for table, canonicalID := range wantCanonicalID {
			if entity.Subject.CanonicalID == canonicalID {
				seen[table] = true
			}
		}
	}
	for _, relationship := range batch.Relationships {
		for table, canonicalID := range wantCanonicalID {
			if relationship.RelationshipID == canonicalID {
				seen[table] = true
			}
		}
	}

	for table := range wantCanonicalID {
		if !seen[table] {
			t.Errorf("producer for table %q never contributed its expected candidate -- its Scan() may not have matched the production-typed schema, or the fixture row above didn't join; entities=%+v relationships=%+v", table, batch.Entities, batch.Relationships)
		}
	}
}
