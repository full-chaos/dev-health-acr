package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// projectTeamRollupWithheld reports whether a project subject's team-ownership
// roll-up is withheld from this caller (CHAOS-12299 F5): yes for a
// project-only caller, no for every other class.
//
// The project facts of health, flow, landscape, metrics, readiness and
// workload are built only from the rows of the teams that own the project
// (team_project_ownership) and, for health, those teams' repositories
// (team_repo_ownership). Every field of such a fact, its severity, risk and
// daily series included, rests on those team and repository rows; none is
// intrinsic to the project. So a project-only caller, whom the graph denies
// every team and repository, is served no such project fact: the provider
// reads none of those rows and returns no fact for the subject. Its typed
// limitation is CHAOS-12330; until then the subject is simply without a fact.
func projectTeamRollupWithheld(principal storage.Principal) bool {
	return auth.ClassifyGrant(principal) == auth.GrantProjectOnly
}
