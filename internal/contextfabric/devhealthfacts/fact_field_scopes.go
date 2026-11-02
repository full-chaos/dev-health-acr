package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// factFieldScopes states the span of every numeric scalar of the kinds a
// comparison may read (CHAOS-12513). A kind outside this table has no scope, so a
// comparison leaves its fields out. chaos12513_field_scopes_test.go holds this
// table and the declarations to one set: a numeric scalar with no row here fails,
// and so does a row with no declaration.
//
// window is only for a value the provider computes over the read's window. A wrong
// window puts two different spans side by side and reads as a comparison. A wrong
// latest_day only leaves a cell empty, so a field that was not checked stays
// latest_day or not_compared, never window.
var factFieldScopes = map[contextfabric.FactKind]map[string]contextfabric.FactFieldScope{
	contextfabric.FactMetrics: {
		// window_pr_cycle.go: over every pull request merged in the window.
		"window_pr_cycle_hours_median": contextfabric.FactScopeWindow,
		"window_pr_count":              contextfabric.FactScopeWindow,
		// window_change_failure.go: the summed stored counts of the window.
		"change_failure_rate":                                     contextfabric.FactScopeWindow,
		"change_failure_deployments_count":                        contextfabric.FactScopeWindow,
		"change_failure_failed_deployments_count":                 contextfabric.FactScopeWindow,
		"change_failure_status_failed_deployments_count":          contextfabric.FactScopeWindow,
		"change_failure_incident_linked_failed_deployments_count": contextfabric.FactScopeWindow,
		"change_failure_status_known_deployments_count":           contextfabric.FactScopeWindow,
		"change_failure_status_coverage":                          contextfabric.FactScopeWindow,
		// metrics.go: the scalar siblings of the newest row of daily_metrics.
		"commits_count":         contextfabric.FactScopeLatestDay,
		"prs_merged":            contextfabric.FactScopeLatestDay,
		"median_pr_cycle_hours": contextfabric.FactScopeLatestDay,
		"revert_rate":           contextfabric.FactScopeLatestDay,
		"bus_factor":            contextfabric.FactScopeLatestDay,
		"code_ownership_gini":   contextfabric.FactScopeLatestDay,
		"mttr_hours":            contextfabric.FactScopeLatestDay,
		// A team's or project's day counters. No daily row proves their day, so
		// they stay out of a table until one does.
		"after_hours_commits_count": contextfabric.FactScopeLatestDay,
		"weekend_commits_count":     contextfabric.FactScopeLatestDay,
		"after_hours_commit_ratio":  contextfabric.FactScopeLatestDay,
		"weekend_commit_ratio":      contextfabric.FactScopeLatestDay,
		// How much the read covered, not a measure of the subject.
		"day_count":        contextfabric.FactScopeNotCompared,
		"revert_rate_days": contextfabric.FactScopeNotCompared,
		"team_count":       contextfabric.FactScopeNotCompared,
	},
	contextfabric.FactPullRequests: {
		// Counted by each pull request's own event time inside the window.
		"pull_requests_opened_window":          contextfabric.FactScopeWindow,
		"pull_requests_merged_window":          contextfabric.FactScopeWindow,
		"pull_requests_closed_unmerged_window": contextfabric.FactScopeWindow,
		// A state of now has no span to state.
		"pull_requests_open_current": contextfabric.FactScopeNotCompared,
		// What the read covered or left out, not a measure of the subject.
		"pull_requests_by_state_omitted_count": contextfabric.FactScopeNotCompared,
		"pull_requests_without_state_count":    contextfabric.FactScopeNotCompared,
		"owned_repository_count":               contextfabric.FactScopeNotCompared,
		"repositories_with_data_count":         contextfabric.FactScopeNotCompared,
		"repositories_without_data_count":      contextfabric.FactScopeNotCompared,
		"owned_repositories_omitted_count":     contextfabric.FactScopeNotCompared,
		"repository_breakdown_omitted_count":   contextfabric.FactScopeNotCompared,
		"project_reached_repository_count":     contextfabric.FactScopeNotCompared,
	},
}

// withFieldScopes writes the table onto a kind's declarations.
func withFieldScopes(kind contextfabric.FactKind, fields []contextfabric.FactFieldDeclaration) []contextfabric.FactFieldDeclaration {
	scopes := factFieldScopes[kind]
	if len(scopes) == 0 {
		return fields
	}
	out := make([]contextfabric.FactFieldDeclaration, len(fields))
	copy(out, fields)
	for index := range out {
		out[index].Scope = scopes[out[index].Name]
	}
	return out
}

// FactFieldScopes returns the table, for the tests that hold it to the catalogue.
func FactFieldScopes() map[contextfabric.FactKind]map[string]contextfabric.FactFieldScope {
	return factFieldScopes
}
