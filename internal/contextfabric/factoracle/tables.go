package factoracle

// The tables of the extract, by name.
//
// devhealthschema:not-a-production-replica these names are an INDEX into devhealthschema.ProductionColumns and EngineFull: every column list, column type and engine the extract uses is read from there (declaredColumns, devhealthschema.DDL); nothing physical is declared in this package.
const (
	tableWorkUnitInvestments          = "work_unit_investments"
	tableWorkUnitSupersessions        = "work_unit_supersessions"
	tableWorkUnitMembershipRuns       = "work_unit_membership_runs"
	tableWorkUnitMembership           = "work_unit_membership"
	tableRepos                        = "repos"
	tableTeams                        = "teams"
	tableTeamRepoOwnership            = "team_repo_ownership"
	tableCapacityForecasts            = "capacity_forecasts"
	tableCompoundingRiskDaily         = "compounding_risk_daily"
	tableWorkItemMetricsDaily         = "work_item_metrics_daily"
	tableEstimateCoverageMetricsDaily = "estimate_coverage_metrics_daily"
)
