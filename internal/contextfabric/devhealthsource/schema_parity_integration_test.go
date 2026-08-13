package devhealthsource_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// columnSpec is one column's name and exact ClickHouse type string, in the
// same form system.columns.type reports it.
type columnSpec struct {
	name   string
	chType string
}

// productionColumns is CHAOS-3789's live-schema-parity snapshot: one entry
// per ClickHouse table, listing every column
// internal/contextfabric/devhealthsource/tables.go's producers SELECT, with
// the exact type read directly off production ClickHouse via system.columns
// (dev-health-clickhouse-1, org 70d529e0-3c06-4597-8480-794fd02328b6,
// dated 2026-08-13). CHAOS-3789 exists because this same package's own
// hand-authored fixtures (this file's sibling
// clickhouse_org_isolation_integration_test.go, and clickhouse_test.go's
// fakeScanner) modeled git_pull_requests.number and
// git_pull_request_reviews.number as int64/Int64 when the real column is
// UInt32, and no test ever caught the drift -- the class this pair of tests
// closes, not just the one column.
//
// This snapshot drives two tests, and neither alone is a complete guard:
//   - TestLiveSchemaParityAcrossEveryProducer (this file) builds a
//     testcontainer typed exactly like this map and proves the Go code's
//     Scan() destinations match it -- catches code-vs-snapshot drift.
//   - TestProductionSchemaSnapshotStaysFreshAgainstLiveClickHouse
//     (schema_parity_live_freshness_test.go), gated behind
//     ACR_CLICKHOUSE_INTEGRATION_DSN, re-reads system.columns from whatever
//     ClickHouse that DSN names and fails -- telling the maintainer to
//     regenerate this snapshot -- the moment production drifts from what's
//     hardcoded here. It only runs where a live/dev ClickHouse is reachable;
//     testcontainers-only environments (plain `go test`, most CI) skip it
//     and still get the first test's guarantee.
//
// One table per entityTables entry (tables.go) except work_items_hierarchy,
// which self-joins work_items rather than naming a distinct ClickHouse
// table -- see export_test.go's EntityTableNamesForTest and this file's
// wantCanonicalID coverage check, which derives its table inventory from
// entityTables directly so an added producer without a matching entry here
// fails loudly instead of going silently unasserted.
var productionColumns = map[string][]columnSpec{
	"repos": {
		{"id", "UUID"}, {"org_id", "String"}, {"repo", "String"}, {"provider", "String"}, {"last_synced", "DateTime64(3, 'UTC')"}, {"created_at", "DateTime64(3, 'UTC')"},
	},
	"work_items": {
		{"work_item_id", "String"}, {"repo_id", "UUID"}, {"org_id", "String"}, {"title", "String"}, {"status", "String"}, {"url", "String"}, {"parent_id", "String"}, {"updated_at", "DateTime64(3)"}, {"created_at", "DateTime64(3)"}, {"completed_at", "Nullable(DateTime64(3))"}, {"closed_at", "Nullable(DateTime64(3))"},
	},
	"git_pull_requests": {
		{"repo_id", "UUID"}, {"org_id", "String"}, {"number", "UInt32"}, {"title", "Nullable(String)"}, {"state", "Nullable(String)"}, {"last_synced", "DateTime64(3, 'UTC')"}, {"created_at", "DateTime64(3, 'UTC')"}, {"merged_at", "Nullable(DateTime64(3, 'UTC'))"}, {"closed_at", "Nullable(DateTime64(3, 'UTC'))"},
	},
	"deployments": {
		{"repo_id", "UUID"}, {"org_id", "String"}, {"deployment_id", "String"}, {"status", "Nullable(String)"}, {"environment", "Nullable(String)"}, {"deployed_at", "Nullable(DateTime64(3, 'UTC'))"}, {"started_at", "Nullable(DateTime64(3, 'UTC'))"}, {"last_synced", "DateTime64(3, 'UTC')"}, {"finished_at", "Nullable(DateTime64(3, 'UTC'))"},
	},
	"operational_incidents": {
		{"id", "String"}, {"org_id", "String"}, {"service_id", "Nullable(String)"}, {"title", "String"}, {"normalized_status", "Nullable(String)"}, {"raw_status", "Nullable(String)"}, {"normalized_severity", "Nullable(String)"}, {"raw_severity", "Nullable(String)"}, {"started_at", "Nullable(DateTime64(6, 'UTC'))"}, {"source_event_at", "Nullable(DateTime64(6, 'UTC'))"}, {"observed_at", "DateTime64(6, 'UTC')"}, {"is_deleted", "UInt8"}, {"resolved_at", "Nullable(DateTime64(6, 'UTC'))"}, {"deleted_at", "Nullable(DateTime64(6, 'UTC'))"},
	},
	"operational_service_repository_mappings": {
		{"org_id", "String"}, {"service_id", "String"}, {"repo_id", "Nullable(UUID)"}, {"is_active", "UInt8"},
	},
	"work_item_dependencies": {
		{"source_work_item_id", "String"}, {"target_work_item_id", "String"}, {"relationship_type", "String"}, {"org_id", "String"}, {"last_synced", "DateTime64(3)"},
	},
	"work_graph_deployment_incident_edges": {
		{"edge_id", "String"}, {"deployment_id", "String"}, {"incident_id", "String"}, {"repo_id", "Nullable(UUID)"}, {"org_id", "UUID"}, {"observed_at", "DateTime64(3, 'UTC')"},
	},
	"git_pull_request_reviews": {
		{"review_id", "String"}, {"repo_id", "UUID"}, {"org_id", "String"}, {"number", "UInt32"}, {"state", "String"}, {"submitted_at", "DateTime64(3, 'UTC')"},
	},
	"ci_pipeline_runs": {
		{"run_id", "String"}, {"repo_id", "UUID"}, {"org_id", "String"}, {"branch", "Nullable(String)"}, {"status", "Nullable(String)"}, {"started_at", "DateTime64(3, 'UTC')"}, {"finished_at", "Nullable(DateTime64(3, 'UTC'))"},
	},
}

// productionTableOrderBy is the ENGINE/ORDER BY suffix
// TestLiveSchemaParityAcrossEveryProducer's testcontainer needs per table --
// not part of the live-schema-parity snapshot itself (system.columns
// doesn't report engine/sort-key information), just what makes each
// ReplacingMergeTree fixture valid SQL. allow_nullable_key columns are kept
// out of every ORDER BY below for the same reason
// operational_service_repository_mappings drops repo_id from its key.
var productionTableOrderBy = map[string]string{
	"repos":                 "ReplacingMergeTree ORDER BY (org_id, id)",
	"work_items":            "ReplacingMergeTree ORDER BY (org_id, work_item_id)",
	"git_pull_requests":     "ReplacingMergeTree ORDER BY (org_id, repo_id, number)",
	"deployments":           "ReplacingMergeTree ORDER BY deployment_id",
	"operational_incidents": "ReplacingMergeTree ORDER BY id",
	"operational_service_repository_mappings": "ReplacingMergeTree ORDER BY (org_id, service_id)",
	"work_item_dependencies":                  "ReplacingMergeTree ORDER BY (org_id, source_work_item_id, target_work_item_id)",
	"work_graph_deployment_incident_edges":    "ReplacingMergeTree ORDER BY edge_id",
	"git_pull_request_reviews":                "ReplacingMergeTree ORDER BY (org_id, review_id)",
	"ci_pipeline_runs":                        "ReplacingMergeTree ORDER BY (org_id, run_id)",
}

// productionSchemaTableOrder fixes DDL iteration order (map order is
// random in Go; CREATE TABLE order doesn't matter to ClickHouse, but a
// stable order keeps a failed `create table` error legible).
var productionSchemaTableOrder = []string{
	"repos", "work_items", "git_pull_requests", "deployments", "operational_incidents",
	"operational_service_repository_mappings", "work_item_dependencies",
	"work_graph_deployment_incident_edges", "git_pull_request_reviews", "ci_pipeline_runs",
}

// productionSchemaDDL renders productionColumns into CREATE TABLE
// statements -- the single source of truth both the testcontainer fixture
// below and the live-freshness check share, so updating one without the
// other is not a way to accidentally get out of sync.
func productionSchemaDDL() []string {
	statements := make([]string, 0, len(productionSchemaTableOrder))
	for _, table := range productionSchemaTableOrder {
		columns := make([]string, 0, len(productionColumns[table]))
		for _, column := range productionColumns[table] {
			columns = append(columns, column.name+" "+column.chType)
		}
		statements = append(statements, fmt.Sprintf("CREATE TABLE %s (%s) ENGINE = %s", table, strings.Join(columns, ", "), productionTableOrderBy[table]))
	}
	return statements
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
	for _, statement := range productionSchemaDDL() {
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

	mustSeed("repos", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, repoSlug, "github", at)
	mustSeed("work_items parent", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-PARENT", repoID, orgID, "Parent work item", "open", "", "", at)
	mustSeed("work_items child", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-CHILD", repoID, orgID, "Child work item", "open", "", "WI-PARENT", at)
	mustSeed("git_pull_requests", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`,
		repoID, orgID, uint32(4242), "Parity PR", "open", at)
	mustSeed("deployments", `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID, orgID, "deploy-parity-1", "success", "production", at, at, at)
	mustSeed("operational_service_repository_mappings", `INSERT INTO operational_service_repository_mappings VALUES (?, ?, ?, ?)`,
		orgID, "svc-parity", repoID, uint8(1))
	mustSeed("operational_incidents", `INSERT INTO operational_incidents (id, org_id, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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

	// CHAOS-3789 codex round-1 F2: derive the table inventory from
	// entityTables (tables.go) itself, via export_test.go, instead of
	// trusting wantCanonicalID's key set to stay in sync by hand -- a
	// producer added to entityTables with no matching entry here now fails
	// this test instead of silently going unasserted, and a stale
	// wantCanonicalID entry for a since-removed producer fails too.
	tableNames := devhealthsource.EntityTableNamesForTest()
	matched := map[string]bool{}
	for _, table := range tableNames {
		canonicalID, ok := wantCanonicalID[table]
		if !ok {
			t.Errorf("entityTables (tables.go) lists table %q with no matching entry in this test's wantCanonicalID -- add a seed row above and an expected canonical/relationship ID here", table)
			continue
		}
		matched[table] = true
		if !seen[table] {
			t.Errorf("producer for table %q never contributed its expected candidate (%q) -- its Scan() may not have matched the production-typed schema, or the fixture row above didn't join; entities=%+v relationships=%+v", table, canonicalID, batch.Entities, batch.Relationships)
		}
	}
	for table := range wantCanonicalID {
		if !matched[table] {
			t.Errorf("wantCanonicalID has entry %q but entityTables (tables.go) no longer lists it -- remove the stale entry", table)
		}
	}
}
