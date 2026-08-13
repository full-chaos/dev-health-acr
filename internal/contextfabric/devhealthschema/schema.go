// Package devhealthschema is the SINGLE declared snapshot of the
// production ClickHouse column types the Context Fabric readers depend on.
//
// It exists because the same type drift bit this codebase twice. CHAOS-3789
// found devhealthsource scanning git_pull_requests.number (UInt32) into an
// *int64, which clickhouse-go rejects outright -- every live row failed
// Scan. CHAOS-3781 codex round 2 then found the IDENTICAL defect surviving
// in devhealthfacts, a different package reading the same column. Both
// times the tests agreed with the bug, because each package hand-authored
// its own fixtures and modeled the column as int64.
//
// One declaration, imported by every guard and fixture, is what stops a
// third occurrence: a fixture cannot disagree with the parity guard when
// both are rendered from this map.
//
// The types are read directly off production ClickHouse via
// system.columns and must not be inferred from a neighbouring table --
// the subtypes genuinely diverge. work_items.created_at is DateTime64(3)
// while git_pull_requests.created_at is DateTime64(3, 'UTC'), and the
// operational_* tables use precision 6 where most use 3. A guessed type
// yields a test that passes against a schema production does not have,
// which is worse than a failing one.
//
// SCOPE: only the columns the readers actually SELECT. Drift in a column
// nobody reads cannot break anything, and declaring all 279 columns of
// these tables would add churn without adding a guarantee.
//
// This package is imported only by tests. It is a normal package rather
// than a _test.go file so that BOTH devhealthsource and devhealthfacts can
// import it -- a declaration inside either package's external test package
// would be unreachable from the other.
package devhealthschema

import (
	"fmt"
	"sort"
	"strings"
)

// Column is one column's name and its exact ClickHouse type string, in the
// form system.columns.type reports it.
type Column struct {
	Name string
	Type string
}

// ProductionColumns is the snapshot: table name -> the columns Context
// Fabric reads from it, with production types.
//
// Columns are listed in PRODUCTION POSITION ORDER, not alphabetically:
// a fixture rendered from this map is then a positional replica of the
// real table, so a seed that lists values without naming columns lands
// them where production would.
//
// Read from dev-health-clickhouse-1 (database `default`) on 2026-08-13.
// Regenerate with the query in this package's doc, and see
// devhealthsource's freshness test, which fails when production drifts
// from what is declared here.
var ProductionColumns = map[string][]Column{
	"backfill_log": {
		{Name: "job_id", Type: "String"},
		{Name: "org_id", Type: "String"},
		{Name: "chunk_index", Type: "UInt32"},
		{Name: "provider", Type: "String"},
		{Name: "items_synced", Type: "UInt32"},
		{Name: "duration_ms", Type: "UInt64"},
		{Name: "status", Type: "String"},
		{Name: "error_message", Type: "String"},
		{Name: "created_at", Type: "DateTime64(3)"},
	},
	"capacity_forecasts": {
		{Name: "forecast_id", Type: "String"},
		{Name: "computed_at", Type: "DateTime64(3, 'UTC')"},
		{Name: "team_id", Type: "Nullable(String)"},
		{Name: "work_scope_id", Type: "Nullable(String)"},
		{Name: "backlog_size", Type: "UInt32"},
		{Name: "p50_days", Type: "Nullable(UInt16)"},
		{Name: "throughput_mean", Type: "Float64"},
		{Name: "throughput_stddev", Type: "Float64"},
		{Name: "insufficient_history", Type: "UInt8"},
		{Name: "high_variance", Type: "UInt8"},
		{Name: "org_id", Type: "String"},
	},
	"ci_pipeline_runs": {
		{Name: "repo_id", Type: "UUID"},
		{Name: "run_id", Type: "String"},
		{Name: "status", Type: "Nullable(String)"},
		{Name: "started_at", Type: "DateTime64(3, 'UTC')"},
		{Name: "finished_at", Type: "Nullable(DateTime64(3, 'UTC'))"},
		{Name: "org_id", Type: "String"},
		{Name: "branch", Type: "Nullable(String)"},
	},
	"compounding_risk_daily": {
		{Name: "org_id", Type: "String"},
		{Name: "day", Type: "Date"},
		{Name: "scope", Type: "Enum8('repo' = 1, 'team' = 2)"},
		{Name: "scope_id", Type: "String"},
		{Name: "compounding_risk", Type: "Nullable(Float64)"},
		{Name: "severity", Type: "Enum8('unknown' = 0, 'low' = 1, 'elevated' = 2, 'high' = 3)"},
		{Name: "computed_at", Type: "DateTime"},
	},
	"deployments": {
		{Name: "repo_id", Type: "UUID"},
		{Name: "deployment_id", Type: "String"},
		{Name: "status", Type: "Nullable(String)"},
		{Name: "environment", Type: "Nullable(String)"},
		{Name: "started_at", Type: "Nullable(DateTime64(3, 'UTC'))"},
		{Name: "finished_at", Type: "Nullable(DateTime64(3, 'UTC'))"},
		{Name: "deployed_at", Type: "Nullable(DateTime64(3, 'UTC'))"},
		{Name: "last_synced", Type: "DateTime64(3, 'UTC')"},
		{Name: "org_id", Type: "String"},
	},
	"estimate_coverage_metrics_daily": {
		{Name: "day", Type: "Date"},
		{Name: "provider", Type: "String"},
		{Name: "work_scope_id", Type: "String"},
		{Name: "team_id", Type: "Nullable(String)"},
		{Name: "estimated_count", Type: "UInt32"},
		{Name: "unestimated_count", Type: "UInt32"},
		{Name: "backlog_size", Type: "UInt32"},
		{Name: "ratio", Type: "Nullable(Float64)"},
		{Name: "computed_at", Type: "DateTime64(3, 'UTC')"},
		{Name: "org_id", Type: "String"},
	},
	"git_pull_request_reviews": {
		{Name: "repo_id", Type: "UUID"},
		{Name: "number", Type: "UInt32"},
		{Name: "review_id", Type: "String"},
		{Name: "state", Type: "String"},
		{Name: "submitted_at", Type: "DateTime64(3, 'UTC')"},
		{Name: "org_id", Type: "String"},
	},
	"git_pull_requests": {
		{Name: "repo_id", Type: "UUID"},
		{Name: "number", Type: "UInt32"},
		{Name: "title", Type: "Nullable(String)"},
		{Name: "state", Type: "Nullable(String)"},
		{Name: "created_at", Type: "DateTime64(3, 'UTC')"},
		{Name: "merged_at", Type: "Nullable(DateTime64(3, 'UTC'))"},
		{Name: "closed_at", Type: "Nullable(DateTime64(3, 'UTC'))"},
		{Name: "last_synced", Type: "DateTime64(3, 'UTC')"},
		{Name: "org_id", Type: "String"},
	},
	"investment_metrics_daily": {
		{Name: "day", Type: "Date"},
		{Name: "team_id", Type: "LowCardinality(Nullable(String))"},
		{Name: "investment_area", Type: "LowCardinality(String)"},
		{Name: "project_stream", Type: "LowCardinality(String)"},
		{Name: "delivery_units", Type: "UInt32"},
		{Name: "work_items_completed", Type: "UInt32"},
		{Name: "prs_merged", Type: "UInt32"},
		{Name: "churn_loc", Type: "UInt64"},
		{Name: "cycle_p50_hours", Type: "Float64"},
		{Name: "computed_at", Type: "DateTime"},
		{Name: "org_id", Type: "String"},
	},
	"operational_incidents": {
		{Name: "org_id", Type: "String"},
		{Name: "id", Type: "String"},
		{Name: "source_event_at", Type: "Nullable(DateTime64(6, 'UTC'))"},
		{Name: "observed_at", Type: "DateTime64(6, 'UTC')"},
		{Name: "raw_status", Type: "Nullable(String)"},
		{Name: "raw_severity", Type: "Nullable(String)"},
		{Name: "normalized_status", Type: "Nullable(String)"},
		{Name: "normalized_severity", Type: "Nullable(String)"},
		{Name: "service_id", Type: "Nullable(String)"},
		{Name: "title", Type: "String"},
		{Name: "started_at", Type: "Nullable(DateTime64(6, 'UTC'))"},
		{Name: "resolved_at", Type: "Nullable(DateTime64(6, 'UTC'))"},
		{Name: "is_deleted", Type: "UInt8"},
		{Name: "deleted_at", Type: "Nullable(DateTime64(6, 'UTC'))"},
	},
	"operational_service_repository_mappings": {
		{Name: "org_id", Type: "String"},
		{Name: "service_id", Type: "String"},
		{Name: "repo_id", Type: "Nullable(UUID)"},
		{Name: "is_active", Type: "UInt8"},
	},
	"recommendations_daily": {
		{Name: "team_id", Type: "LowCardinality(String)"},
		{Name: "org_id", Type: "String"},
		{Name: "rule_id", Type: "LowCardinality(String)"},
		{Name: "rule_version", Type: "LowCardinality(String)"},
		{Name: "window_start", Type: "Date"},
		{Name: "window_end", Type: "Date"},
		{Name: "fired", Type: "Bool"},
		{Name: "severity", Type: "LowCardinality(String)"},
		{Name: "title", Type: "String"},
		{Name: "rationale", Type: "String"},
		{Name: "success_criterion", Type: "String"},
		{Name: "computed_at", Type: "DateTime64(3, 'UTC')"},
	},
	"repo_metrics_daily": {
		{Name: "repo_id", Type: "UUID"},
		{Name: "day", Type: "Date"},
		{Name: "commits_count", Type: "UInt32"},
		{Name: "prs_merged", Type: "UInt32"},
		{Name: "median_pr_cycle_hours", Type: "Float64"},
		{Name: "mttr_hours", Type: "Nullable(Float64)"},
		{Name: "change_failure_rate", Type: "Float64"},
		{Name: "computed_at", Type: "DateTime('UTC')"},
		{Name: "bus_factor", Type: "UInt32"},
		{Name: "code_ownership_gini", Type: "Float64"},
		{Name: "org_id", Type: "String"},
	},
	"repos": {
		{Name: "id", Type: "UUID"},
		{Name: "repo", Type: "String"},
		{Name: "created_at", Type: "DateTime64(3, 'UTC')"},
		{Name: "last_synced", Type: "DateTime64(3, 'UTC')"},
		{Name: "org_id", Type: "String"},
		{Name: "provider", Type: "String"},
	},
	"work_graph_deployment_incident_edges": {
		{Name: "edge_id", Type: "String"},
		{Name: "org_id", Type: "UUID"},
		{Name: "deployment_id", Type: "String"},
		{Name: "incident_id", Type: "String"},
		{Name: "repo_id", Type: "Nullable(UUID)"},
		{Name: "observed_at", Type: "DateTime64(3, 'UTC')"},
	},
	"work_item_dependencies": {
		{Name: "source_work_item_id", Type: "String"},
		{Name: "target_work_item_id", Type: "String"},
		{Name: "relationship_type", Type: "String"},
		{Name: "last_synced", Type: "DateTime64(3)"},
		{Name: "org_id", Type: "String"},
	},
	"work_items": {
		{Name: "repo_id", Type: "UUID"},
		{Name: "work_item_id", Type: "String"},
		{Name: "title", Type: "String"},
		{Name: "status", Type: "String"},
		{Name: "created_at", Type: "DateTime64(3)"},
		{Name: "updated_at", Type: "DateTime64(3)"},
		{Name: "completed_at", Type: "Nullable(DateTime64(3))"},
		{Name: "closed_at", Type: "Nullable(DateTime64(3))"},
		{Name: "parent_id", Type: "String"},
		{Name: "url", Type: "String"},
		{Name: "org_id", Type: "String"},
	},
}

// OrderBy gives each table a sort key for its CREATE TABLE. Tables absent
// here fall back to the first declared column, which is enough for a
// fixture that only needs to accept and return rows.
var OrderBy = map[string]string{
	"repos":                                   "(org_id, id)",
	"work_items":                              "(org_id, work_item_id)",
	"git_pull_requests":                       "(org_id, repo_id, number)",
	"git_pull_request_reviews":                "(org_id, review_id)",
	"ci_pipeline_runs":                        "(org_id, run_id)",
	"deployments":                             "(org_id, deployment_id)",
	"operational_incidents":                   "(org_id, id)",
	"work_item_dependencies":                  "(org_id, source_work_item_id, target_work_item_id)",
	"repo_metrics_daily":                      "(org_id, repo_id, day)",
	"compounding_risk_daily":                  "(org_id, scope, scope_id, day)",
	"estimate_coverage_metrics_daily":         "(org_id, team_id, day)",
	"capacity_forecasts":                      "(org_id, team_id, forecast_id)",
	"investment_metrics_daily":                "(org_id, team_id, day)",
	"recommendations_daily":                   "(org_id, team_id, rule_id, window_end)",
	"backfill_log":                            "(org_id, job_id)",
	"operational_service_repository_mappings": "(org_id, service_id, repo_id)",
	"work_graph_deployment_incident_edges":    "(org_id, edge_id)",
}

// Engines is each table's production engine, read from system.tables.
// The readers query the ReplacingMergeTree tables with FINAL, which is a
// query error against a plain MergeTree, so this cannot be simplified.
var Engines = map[string]string{
	"repos":                                   "ReplacingMergeTree",
	"work_items":                              "ReplacingMergeTree",
	"git_pull_requests":                       "ReplacingMergeTree",
	"git_pull_request_reviews":                "ReplacingMergeTree",
	"ci_pipeline_runs":                        "ReplacingMergeTree",
	"deployments":                             "ReplacingMergeTree",
	"operational_incidents":                   "ReplacingMergeTree",
	"work_item_dependencies":                  "ReplacingMergeTree",
	"estimate_coverage_metrics_daily":         "ReplacingMergeTree",
	"capacity_forecasts":                      "ReplacingMergeTree",
	"recommendations_daily":                   "ReplacingMergeTree",
	"repo_metrics_daily":                      "MergeTree",
	"compounding_risk_daily":                  "MergeTree",
	"investment_metrics_daily":                "MergeTree",
	"backfill_log":                            "MergeTree",
	"operational_service_repository_mappings": "ReplacingMergeTree",
	"work_graph_deployment_incident_edges":    "ReplacingMergeTree",
}

// DDL renders CREATE TABLE statements for the named tables, in a
// deterministic order so a failure message is stable. A caller passing no
// names gets every declared table.
//
// Each table uses its PRODUCTION engine (see Engines), not a uniform one.
// That is load-bearing rather than cosmetic: the readers query the
// ReplacingMergeTree tables with FINAL, and FINAL against a plain
// MergeTree is a query error -- so a fixture that simplified the engine
// would fail every provider for a reason that has nothing to do with the
// type parity under test, which is exactly the false red it produced when
// first written.
func DDL(tables ...string) []string {
	if len(tables) == 0 {
		for table := range ProductionColumns {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	statements := make([]string, 0, len(tables))
	for _, table := range tables {
		columns, ok := ProductionColumns[table]
		if !ok {
			panic("devhealthschema: no declared columns for table " + table)
		}
		rendered := make([]string, 0, len(columns))
		for _, column := range columns {
			rendered = append(rendered, column.Name+" "+column.Type)
		}
		orderBy, ok := OrderBy[table]
		if !ok {
			orderBy = columns[0].Name
		}
		// allow_nullable_key: several production sort keys are Nullable
		// (team_id, work_scope_id). The whole point of this fixture is
		// to carry the DECLARED production types, so the setting is
		// relaxed rather than the types being altered to fit the
		// default -- altering them would silently rebuild the exact
		// drift these guards exist to catch.
		engine, ok := Engines[table]
		if !ok {
			engine = "MergeTree"
		}
		statements = append(statements, fmt.Sprintf(
			"CREATE TABLE %s (%s) ENGINE = %s ORDER BY %s SETTINGS allow_nullable_key = 1",
			table, strings.Join(rendered, ", "), engine, orderBy))
	}
	return statements
}
