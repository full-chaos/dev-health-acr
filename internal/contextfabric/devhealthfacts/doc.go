// Package devhealthfacts implements contextfabric.FactProvider for the
// canonical Dev Health fact kinds that are backed by a real ClickHouse table
// already read in this repository (internal/contextfabric/devhealthsource):
// FactIdentity and FactMembership from repos/work_items, FactStatus,
// FactActualCompletion, FactWork, FactBlockers, and FactRequiredChildren
// from work_items/work_item_dependencies, FactPullRequests and FactReviews
// from git_pull_requests/git_pull_request_reviews, FactContinuousIntegration
// from ci_pipeline_runs, FactDeployments from deployments, and FactIncidents
// from operational_incidents. Every query reuses
// internal/contextpacket.ClickHouseQueryClient -- the same read boundary
// devhealthsource uses -- and is scoped to the caller's storage.Principal
// org and to only the subjects a FactQuery asked about, never the whole
// organization.
//
// CHAOS-3780 added seven more kinds, each backed by a genuinely precomputed
// Dev Health Ops ClickHouse table this package only ever reads, never
// recomputes (§19.6.3 -- Ops stays the authority for health, workload,
// investment, readiness, and deficiency formulas):
//
//   - FactMetrics from repo_metrics_daily (latest day per repository).
//   - FactHealth from compounding_risk_daily (repo- and team-scoped
//     precomputed risk severity/score).
//   - FactWorkload from capacity_forecasts (team-level Monte Carlo capacity
//     forecast; the table has no person-level column at all).
//   - FactInvestment from investment_metrics_daily (per team/investment
//     area/project stream, latest day, passthrough).
//   - FactReadiness from estimate_coverage_metrics_daily (per-team backlog
//     estimate-coverage ratio -- a narrow, honest slice of "readiness";
//     no broader canonical readiness signal exists yet).
//   - FactOperationalDeficiencies from recommendations_daily (fired=1 rows
//     only -- rule outcomes Ops' own rule engine already decided, never
//     re-evaluated here).
//   - FactSourceHealth from backfill_log (per-provider ingestion job
//     outcome, scoped to the organization subject -- there is no finer
//     per-repository/per-team ingestion-health column).
//
// FactEvidence is deliberately NOT implemented: no ClickHouse table maps
// honestly to it. report_provenance is schema-present but empty in every
// environment checked, and its artifact_id/plan_id vocabulary has no
// established correspondence to any contextfabric.SubjectKind -- binding it
// would mean inventing a subject-ID convention this package has never used,
// which §19.6.3 forbids ("stub data... for a kind with no canonical
// source"). NewProviders leaves FactEvidence unregistered;
// contextfabric.FactCapabilityRegistry already degrades an unregistered kind
// to SourceUnconfigured (fact_registry.go's ReadFacts), which is the honest
// behavior for a kind with no backing source.
//
// Neither FactMetrics nor FactEvidence is a target of
// contractsv1.ContextFabricDriverCategoryRequiresClaimedFact's closed
// category->kind table (internal/contracts/v1/context_fabric_types.go) --
// no driver category cites either kind as a required claimed fact, so
// registering FactMetrics live and leaving FactEvidence unconfigured does
// not affect that closure requirement either way.
package devhealthfacts
