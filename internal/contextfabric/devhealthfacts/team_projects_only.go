package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"
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
// team chunk. checked holds every team of a chunk that completed: on an error
// those teams were still answered (absent = owns none), the others were not.
func teamOwnedProjectSummaries(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (summaries map[string]teamOwnedProjectSummary, checked map[string]struct{}, err error) {
	out := map[string]teamOwnedProjectSummary{}
	done := map[string]struct{}{}
	err = forEachTeamIDChunk(teamIDs, func(chunk []string) error {
		statement := `SELECT t.team_id, toInt64(uniqExact(t.provider, t.id)), arraySlice(arraySort(groupUniqArray(if(ifNull(p.project_key, '') != '', p.project_key, t.id))), 1, ` + fmt.Sprint(maxNamedOwnedProjects) + `)
FROM ` + teamOwnedProjectsAsOfSQL(bound) + ` AS t
LEFT JOIN (SELECT provider, id, project_key FROM projects FINAL WHERE org_id = {org_id:String}) AS p ON p.provider = t.provider AND p.id = t.id
WHERE t.team_id IN {ids:Array(String)}
GROUP BY t.team_id
ORDER BY t.team_id`
		rows := map[string]teamOwnedProjectSummary{}
		if err := readers.QueryOrgScopedNamed(ctx, client, "ReadTeamOwnedProjectSummaries", statement, orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
			var teamID string
			var summary teamOwnedProjectSummary
			if err := row.Scan(&teamID, &summary.count, &summary.names); err != nil {
				return err
			}
			rows[teamID] = summary
			return nil
		}, boundReaderBindings(bound)...); err != nil {
			return err
		}
		// A chunk's rows count only once the chunk completed, so a team is in out
		// exactly when it is in done.
		for id, summary := range rows {
			out[id] = summary
		}
		for _, id := range chunk {
			done[id] = struct{}{}
		}
		return nil
	})
	return out, done, err
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

// teamProjectCheckFailedReason is the per-team limitation when the project read
// failed: the check did not run, so ownership through projects is unknown. It
// replaces the "owns no repository" text, which would present a missing check
// as a measured absence.
const teamProjectCheckFailedReason = "devhealthfacts: the project-link check could not run for this team, so ownership through projects is unknown; no repository rollup applies; this is not a measured zero"

// teamsProjectCheckFailedReason is the kind-wide form when every queried team is
// not applicable and the check could not run for any of them.
const teamsProjectCheckFailedReason = "devhealthfacts: the project-link check could not run for the requested teams, so ownership through projects is unknown; no repository rollup applies; this is not a measured zero"

// teamsProjectCheckFailedNote is the request note when N of M requested teams
// were told the check could not run.
func teamsProjectCheckFailedNote(failed, requested int) string {
	return fmt.Sprintf("devhealthfacts: the project-link check could not run for %d of %d requested teams, so ownership through projects is unknown for them; no repository rollup applies to them; this is not a measured zero", failed, requested)
}

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
func applyTeamsOwningProjectsOnly(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, result *contextfabric.FactProviderResult, queried []contextfabric.SubjectRef, requested int, bound factTimeBound) teamProjectCauses {
	if len(result.NotApplicableSubjects) == 0 {
		return teamProjectCauses{}
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
		return teamProjectCauses{}
	}
	summaries, checked, err := teamOwnedProjectSummaries(ctx, client, orgID, ids, bound)
	var unchecked []contextfabric.SubjectRef
	if err != nil {
		// Best effort: this read only explains an already-decided not-applicable
		// state, so a failure changes no fact, no state and fails no kind. The
		// cause text says the check did not run instead of claiming a measured
		// absence.
		attrs := []any{"action", contextfabric.SanitizeLogAttr("query team owned projects"), "error_type", contextfabric.SanitizeLogAttr(fmt.Sprintf("%T", err))}
		if code, ok := clickHouseExceptionCode(err); ok {
			attrs = append(attrs, "clickhouse_code", int(code))
		}
		slog.WarnContext(ctx, "devhealthfacts.read_failed", attrs...)
		// A chunk that completed before the failure answered its teams: they
		// keep the result below; only the teams of a chunk that did not
		// complete are unchecked.
		for i, subject := range teams {
			if _, ok := checked[ids[i]]; !ok {
				unchecked = append(unchecked, subject)
			}
		}
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
	applyTeamProjectCheckFailed(result, unchecked, requested)
	causes := teamProjectCauses{owning: owning, unchecked: len(unchecked)}
	if owning > 0 && owning == len(result.NotApplicableSubjects) {
		if result.State == contextfabric.SourceNotApplicable && result.Reason == teamNoOwnedRepositoriesReason {
			result.Reason = teamsOwnProjectsOnlyReason
		}
		result.RequestNote = strings.Replace(result.RequestNote, teamsOwningNoRepositoriesReason(owning, requested), teamsOwnProjectsOnlyNote(owning, requested), 1)
	}
	return causes
}

// teamProjectCauses counts, among the not-applicable teams, those whose cause
// moved off "owns no repository": teams that own a project, and teams whose
// project check did not run.
type teamProjectCauses struct {
	owning    int
	unchecked int
}

// applyTeamProjectCheckFailed rewrites the not-applicable wording that
// applyTeamsOwningNothing set for the teams whose project read failed. The
// state is unchanged: only the cause text moves, because the check did not
// run and ownership through projects is unknown.
func applyTeamProjectCheckFailed(result *contextfabric.FactProviderResult, teams []contextfabric.SubjectRef, requested int) {
	failed := 0
	for _, subject := range teams {
		key := contextfabric.FactSubjectKey(subject)
		if old := result.SubjectReasons[key]; strings.Contains(old, teamNoOwnedRepositoriesReason) {
			result.SubjectReasons[key] = strings.Replace(old, teamNoOwnedRepositoriesReason, teamProjectCheckFailedReason, 1)
		} else {
			addSubjectReasons(result, []contextfabric.SubjectRef{subject}, teamProjectCheckFailedReason)
		}
		failed++
	}
	if failed > 0 && failed == len(result.NotApplicableSubjects) {
		if result.State == contextfabric.SourceNotApplicable && result.Reason == teamNoOwnedRepositoriesReason {
			result.Reason = teamsProjectCheckFailedReason
		}
		result.RequestNote = strings.Replace(result.RequestNote, teamsOwningNoRepositoriesReason(failed, requested), teamsProjectCheckFailedNote(failed, requested), 1)
	}
}
