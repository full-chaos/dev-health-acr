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
// FactMetrics, FactHealth, FactWorkload, FactInvestment, FactReadiness,
// FactOperationalDeficiencies, FactSourceHealth, and FactEvidence are
// deliberately NOT implemented here: there is no canonical Dev Health Ops
// adapter client for any of them in this repository today (mirrors
// devhealthsource/teams_projects.go's TeamsProjectsSource, which is a
// documented no-op for the same reason -- no canonical team/project
// source exists yet). NewProviders leaves those eight kinds unregistered;
// contextfabric.FactCapabilityRegistry already degrades an unregistered
// kind to SourceUnconfigured (fact_registry.go's ReadFacts), which is the
// honest behavior for a kind with no backing source -- this package does
// not stub fake data for them.
package devhealthfacts
