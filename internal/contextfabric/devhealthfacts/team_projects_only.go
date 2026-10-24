package devhealthfacts

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// teamOwnedProjectSummary is what a team that owns no repository says about the
// projects it does own: how many, and up to maxNamedOwnedProjects of their
// project keys (the project id when a project has no key). It is read for the
// wording of a limitation only; no repository is ever inferred from a project
// (a project reaches repositories only through its issues' linked pull
// requests, teamProjectReachedRepositories).
type teamOwnedProjectSummary struct {
	count int64
	names []string
}

const maxNamedOwnedProjects = 3

// teamOwnedProjectSummaries reads, for each team, the projects it owns under
// the read's ownership rule (teamOwnedProjectsAsOfSQL). A team that owns none
// is absent from the map. One row per team, so the read is bounded by the
// team chunk.
func teamOwnedProjectSummaries(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (map[string]teamOwnedProjectSummary, error) {
	out := map[string]teamOwnedProjectSummary{}
	err := forEachTeamIDChunk(teamIDs, func(chunk []string) error {
		statement := `SELECT t.team_id, toInt64(uniqExact(t.provider, t.id)), arraySlice(arraySort(groupUniqArray(if(ifNull(p.project_key, '') != '', p.project_key, t.id))), 1, ` + fmt.Sprint(maxNamedOwnedProjects) + `)
FROM ` + teamOwnedProjectsAsOfSQL(bound) + ` AS t
LEFT JOIN (SELECT provider, id, project_key FROM projects FINAL WHERE org_id = {org_id:String}) AS p ON p.provider = t.provider AND p.id = t.id
WHERE t.team_id IN {ids:Array(String)}
GROUP BY t.team_id
ORDER BY t.team_id`
		return readers.QueryOrgScopedNamed(ctx, client, "ReadTeamOwnedProjectSummaries", statement, orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
			var teamID string
			var summary teamOwnedProjectSummary
			if err := row.Scan(&teamID, &summary.count, &summary.names); err != nil {
				return err
			}
			out[teamID] = summary
			return nil
		}, boundReaderBindings(bound)...)
	})
	return out, err
}

// teamOwnsProjectsOnlyReason is the per-team limitation of a team that owns
// project(s) but reaches no repository: it names the projects and says why no
// repository exists for it. Not a measured zero.
func teamOwnsProjectsOnlyReason(summary teamOwnedProjectSummary) string {
	named := strings.Join(summary.names, ", ")
	if more := summary.count - int64(len(summary.names)); more > 0 {
		named += fmt.Sprintf(" (+%d more)", more)
	}
	noun := "project"
	if summary.count > 1 {
		noun = "projects"
	}
	return fmt.Sprintf("devhealthfacts: the team owns %s %s; no linked pull request reaches a repository, so no repository rollup applies; this is not a measured zero", noun, named)
}

// teamsOwnProjectsOnlyReason is the kind-wide reason when every queried team is
// not applicable and every one of them owns a project.
const teamsOwnProjectsOnlyReason = "devhealthfacts: the requested teams own projects; no linked pull request reaches a repository, so no repository rollup applies; this is not a measured zero"

// teamsOwnProjectsOnlyNote is the request note when N of M requested teams own
// a project and reach no repository.
func teamsOwnProjectsOnlyNote(owning, requested int) string {
	return fmt.Sprintf("devhealthfacts: %d of %d requested teams own projects; no linked pull request reaches a repository for them, so no repository rollup applies to them; this is not a measured zero", owning, requested)
}

// applyTeamsOwningProjectsOnly rewrites the not-applicable wording that
// applyTeamsOwningNothing set for a team that owns a project: such a team does
// not "own no repository in synced ownership" in the sense the old text claims;
// it owns work whose issues link no pull request. Teams that own no project
// keep the old text byte for byte. The not-applicable STATE is unchanged: only
// the cause text moves, because a repository-derived kind has nothing to roll up
// either way. A read error is returned so the caller fails loudly instead of
// serving the wrong cause.
func applyTeamsOwningProjectsOnly(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, result *contextfabric.FactProviderResult, queried []contextfabric.SubjectRef, requested int, bound factTimeBound) error {
	if len(result.NotApplicableSubjects) == 0 {
		return nil
	}
	var teams []contextfabric.SubjectRef
	var ids []string
	for _, subject := range queried {
		if subject.Kind != contextfabric.SubjectTeam {
			continue
		}
		if _, na := result.NotApplicableSubjects[contextfabric.FactSubjectKey(subject)]; !na {
			continue
		}
		raw := strings.TrimPrefix(subject.CanonicalID, teamPrefix)
		if raw == "" || raw == subject.CanonicalID {
			continue
		}
		teams = append(teams, subject)
		ids = append(ids, raw)
	}
	if len(ids) == 0 {
		return nil
	}
	summaries, err := teamOwnedProjectSummaries(ctx, client, orgID, ids, bound)
	if err != nil {
		return err
	}
	owning := 0
	for i, subject := range teams {
		summary, ok := summaries[ids[i]]
		if !ok || summary.count == 0 {
			continue
		}
		owning++
		reason := teamOwnsProjectsOnlyReason(summary)
		key := contextfabric.FactSubjectKey(subject)
		if old := result.SubjectReasons[key]; strings.Contains(old, teamNoOwnedRepositoriesReason) {
			result.SubjectReasons[key] = strings.Replace(old, teamNoOwnedRepositoriesReason, reason, 1)
		} else {
			addSubjectReasons(result, []contextfabric.SubjectRef{subject}, reason)
		}
	}
	if owning > 0 && owning == len(result.NotApplicableSubjects) {
		if result.State == contextfabric.SourceNotApplicable && result.Reason == teamNoOwnedRepositoriesReason {
			result.Reason = teamsOwnProjectsOnlyReason
		}
		result.RequestNote = strings.Replace(result.RequestNote, teamsOwningNoRepositoriesReason(owning, requested), teamsOwnProjectsOnlyNote(owning, requested), 1)
	}
	return nil
}
