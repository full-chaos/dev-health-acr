package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// teamOwnedOrReachedRepositories is every repository a team has: those it owns
// and those only its owned projects' linked pull requests reach (the set the
// pull request and blocker rollups use). A reads-per-team fact that does not
// roll up repositories uses it to decide whether a team "owns no repository":
// a team that reaches one is not served that statement.
func teamOwnedOrReachedRepositories(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (map[string][]teamOwnedRepo, error) {
	owned, err := teamOwnedRepositories(ctx, client, orgID, teamIDs, bound)
	if err != nil {
		return nil, err
	}
	reached, err := teamProjectReachedRepositories(ctx, client, orgID, teamIDs, bound)
	if err != nil {
		return nil, err
	}
	for teamID, repos := range reachedOnlyRepositories(reached, owned) {
		for _, repo := range repos {
			owned[teamID] = append(owned[teamID], teamOwnedRepo{key: repo.key, name: repo.name})
		}
	}
	return owned, nil
}
