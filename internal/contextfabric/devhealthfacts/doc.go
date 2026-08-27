// Package devhealthfacts implements contextfabric.FactProvider for the
// canonical Dev Health fact kinds that are backed by a real ClickHouse table
// already read in this repository (internal/contextfabric/devhealthsource):
// FactIdentity and FactMembership from repos/work_items, FactStatus,
// FactActualCompletion, FactWork, FactBlockers, and FactRequiredChildren
// from work_items/work_item_dependencies, FactPullRequests and FactReviews
// from git_pull_requests/git_pull_request_reviews, FactContinuousIntegration
// from ci_pipeline_runs (per-run status; CHAOS-4347 adds a
// repository-scoped aggregate shape from cicd_metrics_daily -- see ci.go),
// FactDeployments from deployments (per-deployment status/environment;
// CHAOS-4347 adds a repository-scoped aggregate shape from
// deploy_metrics_daily -- see deployments.go), and FactIncidents from
// operational_incidents. Every query reuses
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
//     CHAOS-4347 widens this to [repository, team, project]: team reads
//     team_metrics_daily directly (a genuinely team-scoped rollup, not a
//     repository proxy); project rolls up through
//     team_project_ownership -> team_metrics_daily, summing additive
//     counts across owning teams while keeping each team's own rate in a
//     disclosed per-team Rows breakdown rather than averaging them. See
//     metrics.go's package doc comment for the full design and why this is
//     NOT the CHAOS-4099 activity-proxy path.
//   - FactHealth from compounding_risk_daily (repo- and team-scoped
//     precomputed risk severity/score). CHAOS-4363 widens this to add
//     project: a project rolls up BOTH its owning teams' own compounding_risk_daily
//     rows (team_project_ownership) and those teams' repositories'
//     (team_repo_ownership, one hop further), into one renderable
//     risk_breakdown table tagged by scope.
//   - FactWorkload from capacity_forecasts (team-level Monte Carlo capacity
//     forecast; the table has no person-level column at all). CHAOS-4363
//     widens this to add project (team_project_ownership rollup, per-team
//     forecast breakdown -- Monte Carlo stats are never summed/averaged).
//   - FactInvestment from investment_metrics_daily (per team/investment
//     area/project stream, latest day, passthrough). CHAOS-4363 widens this
//     to add project (team_project_ownership rollup, per-team breakdown --
//     investment_area/project_stream partitions are never summed across
//     teams). investment_classifications_daily is NOT read for this: its
//     live schema (verified 2026-08-27) carries no team_id column.
//   - FactReadiness from estimate_coverage_metrics_daily (per-team backlog
//     estimate-coverage ratio -- a narrow, honest slice of "readiness";
//     no broader canonical readiness signal exists yet). CHAOS-4363 widens
//     this to add project (team_project_ownership rollup, per-team
//     breakdown -- estimate/backlog counts are never summed across teams
//     tracking different work scopes).
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
// CHAOS-4347 also added a renderable-table capability to
// contextfabric.FactValue (Rows/FactValueRow, model.go) and mirrored it,
// additively, into contractsv1.ContextFabricClaimedFact/ProjectedFact
// (context_fabric_types.go / context_fabric_answer_projection.go) as
// contractsv1.ContextFabricClaimedFactRow, so a fact whose evidence is
// genuinely a set of rows -- a project's per-team metrics rollup, for
// instance -- has somewhere to put a table instead of forcing a lossy
// single scalar into an answer. This package's own MetricsProvider is the
// first (and, as of CHAOS-4347, only) producer of a Rows-bearing fact.
// Populating a ProjectedFact.Rows from an LLM synthesis call is
// deliberately NOT part of this change -- that is a genkitruntime/prompt
// concern with its own version-bump discipline (a prompt version bumps on
// every text change), not a fact-provider one.
//
// CHAOS-4364 adds two more v1-additive fact kinds, following the SAME
// "widen by a real table join, never a proxy" discipline CHAOS-4347
// established for FactMetrics:
//
//   - FactFlow from work_item_metrics_daily + work_item_cycle_times
//     (team, per-work_scope_id Rows plus a team-scoped average of
//     flow_efficiency/active_time_hours/wait_time_hours), rolled up to
//     project via team_project_ownership, plus a distinct repository shape
//     reading repo_metrics_daily's PR pickup/review-timing columns.
//   - FactLandscape from ic_landscape_rolling_30d (team, aggregated to
//     (team_id, map_name) -- never per-identity, see landscape.go's
//     package doc comment for why), rolled up to project via
//     team_project_ownership.
//
// See flow.go and landscape.go for the full design and SQL.
//
// Neither FactMetrics nor FactEvidence is a target of
// contractsv1.ContextFabricDriverCategoryRequiresClaimedFact's closed
// category->kind table (internal/contracts/v1/context_fabric_types.go) --
// no driver category cites either kind as a required claimed fact, so
// registering FactMetrics live and leaving FactEvidence unconfigured does
// not affect that closure requirement either way.
package devhealthfacts
