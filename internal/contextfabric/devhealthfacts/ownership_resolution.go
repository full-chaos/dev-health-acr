package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/ownershipresolve"

// CHAOS-7073 (ruling K11): every fact read that asks "which repositories does
// this team own" resolves team_repo_ownership rows the way ops does
// (teamscope RepoCondition): a NULL repo_id row resolves by (org_id,
// provider, lower-cased name) against repos, a row's own repo_id wins, and the
// resolved id must exist in repos. The clause-by-clause rule and its reasons
// are documented on package ownershipresolve, the one definition shared with
// the graph's repository->team edge (CHAOS-7119). The validity window stays
// ownershipValidityPredicate, passed in as the filter by every caller.

// ownedRepositoriesSource returns a derived table (team_id, repo_key,
// repo_full_name) of the currently resolvable owned repositories. The rule
// lives in ownershipresolve (CHAOS-7119 moved it there so the graph edge
// shares it); this wrapper renders the default, byte-identical table, pinned
// by TestOwnedRepositoriesSourceGoldenCHAOS7119.
func ownedRepositoriesSource(filter string) string {
	return ownershipresolve.OwnedRepositoriesSource(filter, ownershipresolve.Options{})
}
