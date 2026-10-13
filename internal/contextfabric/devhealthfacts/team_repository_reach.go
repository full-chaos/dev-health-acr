package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// teamsOwningAnyProject returns the requested teams that have at least one
// project ownership row (any validity): the only teams a project-link read can
// reach a repository for. A read with none of them skips the link reads.
func teamsOwningAnyProject(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string) ([]string, error) {
	var out []string
	err := forEachTeamIDChunk(teamIDs, func(chunk []string) error {
		return readers.QueryOrgScopedNamed(ctx, client, "ReadTeamsOwningProjects", withRowLimit(`SELECT DISTINCT team_id FROM team_project_ownership FINAL
WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}
ORDER BY team_id`), orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
			var teamID string
			if err := row.Scan(&teamID); err != nil {
				return err
			}
			out = append(out, teamID)
			return nil
		})
	})
	return out, err
}

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
	projectTeams, err := teamsOwningAnyProject(ctx, client, orgID, teamIDs)
	if err != nil || len(projectTeams) == 0 {
		return owned, err
	}
	reached, err := teamProjectReachedRepositories(ctx, client, orgID, projectTeams, bound)
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

// projectReachedRepositoriesFactValue is the table naming the repositories a
// team reaches only through its owned projects' linked pull requests, with
// each one's weakest link tier. Each row is a repository subject reference the
// subject gate checks. omitted counts the rows the table bound dropped;
// project_reached_repository_count carries the full total.
func projectReachedRepositoriesFactValue(repos []teamReachedRepo) (value contextfabric.FactValue, omitted int, ok bool) {
	if len(repos) == 0 {
		return contextfabric.FactValue{}, 0, false
	}
	rows := make([]contextfabric.FactValueRow, 0, len(repos))
	for _, repo := range repos {
		fields := map[string]contextfabric.FactValue{"repository_id": contextfabric.StringFactValue(repo.key)}
		if repo.name != "" {
			fields["repository_name"] = contextfabric.StringFactValue(repo.name)
		}
		if tier := blockerLinkTierName(repo.linkRank); tier != "" {
			fields["link_tier"] = contextfabric.StringFactValue(tier)
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: fields})
	}
	rows, omitted = capFactValueRows(rows)
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"repository_id"},
		Observations: []string{"repository_name", "link_tier"},
		Rows:         rows,
	}), omitted, true
}
