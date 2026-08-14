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
		{Name: "last_synced", Type: "DateTime64(3, 'UTC')"},
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
		{Name: "last_synced", Type: "DateTime64(3, 'UTC')"},
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
		{Name: "source_version_at", Type: "DateTime64(6, 'UTC')"},
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
		{Name: "source_version_at", Type: "DateTime64(6, 'UTC')"},
		{Name: "id", Type: "String"},
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
		{Name: "ref", Type: "Nullable(String)"},
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
		{Name: "source", Type: "LowCardinality(String)"},
		{Name: "observed_at", Type: "DateTime64(3, 'UTC')"},
		{Name: "computed_at", Type: "DateTime64(3, 'UTC')"},
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
		{Name: "last_synced", Type: "DateTime64(3)"},
		{Name: "org_id", Type: "String"},
	},
}

// EngineFull is each table's COMPLETE physical definition, exactly as
// system.tables.engine_full reports it: engine, version column, PARTITION
// BY, ORDER BY and SETTINGS in one string.
//
// CHAOS-3781 round-4 R4-1: this replaces separate hand-maintained Engines
// and OrderBy maps that carried only the engine CLASS. Dropping
// ReplacingMergeTree's VERSION column changed dedup semantics -- FINAL on
// a versionless table keeps an arbitrary row among those sharing a sort
// key, while production keeps the one with the highest version. Any
// fixture built from the class alone was therefore proving the wrong
// thing about exactly the FINAL behaviour several providers depend on.
//
// It is ONE field on purpose. The engine class, the version column and the
// sorting key are all facets of a single physical definition, and the
// three previous rounds each found a different hand-authored facet drifted
// from live. A field that cannot be authored separately cannot drift
// separately.
var EngineFull = map[string]string{
	"backfill_log":                            "MergeTree ORDER BY (org_id, job_id, chunk_index) SETTINGS index_granularity = 8192",
	"capacity_forecasts":                      "ReplacingMergeTree(computed_at) ORDER BY (org_id, forecast_id) SETTINGS index_granularity = 8192",
	"ci_pipeline_runs":                        "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, run_id) SETTINGS index_granularity = 8192",
	"compounding_risk_daily":                  "MergeTree PARTITION BY toYYYYMM(day) ORDER BY (org_id, scope, scope_id, day, computed_at) SETTINGS index_granularity = 8192",
	"deployments":                             "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, deployment_id) SETTINGS index_granularity = 8192",
	"estimate_coverage_metrics_daily":         "ReplacingMergeTree(computed_at) PARTITION BY toYYYYMM(day) ORDER BY (org_id, day, provider, work_scope_id, ifNull(team_id, '')) SETTINGS index_granularity = 8192",
	"git_pull_request_reviews":                "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, number, review_id) SETTINGS index_granularity = 8192",
	"git_pull_requests":                       "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, number) SETTINGS index_granularity = 8192",
	"investment_metrics_daily":                "MergeTree PARTITION BY toYYYYMM(day) ORDER BY (org_id, day, team_id, investment_area, project_stream) SETTINGS allow_nullable_key = 1, index_granularity = 8192",
	"operational_incidents":                   "ReplacingMergeTree(source_version_at) ORDER BY (org_id, id) SETTINGS index_granularity = 8192",
	"operational_service_repository_mappings": "ReplacingMergeTree(source_version_at) ORDER BY (org_id, id) SETTINGS index_granularity = 8192",
	"recommendations_daily":                   "ReplacingMergeTree(computed_at) PARTITION BY toYYYYMM(window_end) ORDER BY (org_id, team_id, rule_id, window_end) SETTINGS index_granularity = 8192",
	"repo_metrics_daily":                      "MergeTree PARTITION BY toYYYYMM(day) ORDER BY (org_id, repo_id, day) SETTINGS index_granularity = 8192",
	"repos":                                   "ReplacingMergeTree(last_synced) ORDER BY (org_id, id) SETTINGS index_granularity = 8192",
	"work_graph_deployment_incident_edges":    "ReplacingMergeTree(computed_at) PARTITION BY toYYYYMM(observed_at) ORDER BY (org_id, deployment_id, incident_id, source) SETTINGS index_granularity = 8192",
	"work_item_dependencies":                  "ReplacingMergeTree(last_synced) ORDER BY (org_id, source_work_item_id, target_work_item_id, relationship_type) SETTINGS index_granularity = 8192",
	"work_items":                              "ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, work_item_id) SETTINGS index_granularity = 8192",
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
		engine, ok := EngineFull[table]
		if !ok {
			panic("devhealthschema: no declared engine for table " + table)
		}
		statements = append(statements, fmt.Sprintf(
			"CREATE TABLE %s (%s) ENGINE = %s",
			table, strings.Join(rendered, ", "), withNullableKeySetting(engine)))
	}
	return statements
}

// withNullableKeySetting appends allow_nullable_key to a definition's
// SETTINGS. Several production sort keys are Nullable (team_id,
// work_scope_id); the fixture carries the DECLARED types rather than
// altering them to satisfy the default, because altering them would
// rebuild the exact drift these guards exist to catch.
func withNullableKeySetting(engineFull string) string {
	if strings.Contains(engineFull, "allow_nullable_key") {
		return engineFull
	}
	if strings.Contains(engineFull, "SETTINGS") {
		return engineFull + ", allow_nullable_key = 1"
	}
	return engineFull + " SETTINGS allow_nullable_key = 1"
}
