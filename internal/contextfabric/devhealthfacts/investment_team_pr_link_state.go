package devhealthfacts

import (
	"context"
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// investmentTeamPRsWithoutLinkedWork is the closed reason token of a team that
// owns repositories, has no work unit in the window, and whose pull requests
// in the window are all without an admitted link to tracked work. It is not a
// defect and not compute lag: a unit is built from tracked work, and these
// pull requests name none.
const investmentTeamPRsWithoutLinkedWork = "investment_team_prs_without_linked_work"

// investmentTeamPRsWithoutLinkedWorkRule states what the read proved and what
// it did not. A pull request is in the window when it was created before the
// window end and was not merged or closed before the window start (no window
// on the current axis). A link is admitted when work_graph_issue_pr has a row
// of a known provenance tier (native, explicit_text, heuristic) for it whose
// work item is an issue.
const investmentTeamPRsWithoutLinkedWorkRule = "no pull request in the window has an admitted link to tracked work (window: created before the end, not merged or closed before the start; admitted link: a work_graph_issue_pr row of a known provenance tier whose work item is an issue)"

// teamNoLinkedWork is a team with pull requests in the window and none linked.
type teamNoLinkedWork struct {
	subject      contextfabric.SubjectRef
	pullRequests int64
}

func investmentTeamPRsWithoutLinkedWorkOwnReason(pullRequests int64) string {
	return fmt.Sprintf("%s: %d pull request(s) in the team's repositories in the requested window; %s", investmentTeamPRsWithoutLinkedWork, pullRequests, investmentTeamPRsWithoutLinkedWorkRule)
}

// teamPRLinkChunk bounds how many repositories one statement reads; a chunk
// yields one row per repository, so its row ceiling is the chunk.
const teamPRLinkChunk = 500

// teamPRLinkRepositorySet is the requested repositories as a set the primary
// key can prune on; an array expression with a lambda is not.
const teamPRLinkRepositorySet = `(SELECT toUUIDOrZero(arrayJoin({ids:Array(String)})))`

// The work item type is read only for work items stored in the requested
// repositories (the table is sorted by org_id, repo_id, work_item_id, so an id
// lookup alone reads the organization). A linked work item stored in another
// repository has no row here and counts as admitted, the same as a missing row
// does in the shared link rule.
// teamPRLinkStatement counts, per requested repository, the pull requests in
// the window and how many of them have an admitted link. Both tables are read
// by their primary key prefix (org_id, repo_id), so the read grows with the
// requested repositories and not with the organization.
func teamPRLinkStatement(bound factTimeBound, limit int) string {
	return withRowLimitOf(`SELECT toString(pr.repo_id) AS repo_id, count() AS pull_requests,
	countIf((pr.repo_id, pr.number) IN (
		SELECT l.repo_id, l.pr_number
		FROM work_graph_issue_pr AS l FINAL
		LEFT JOIN (
			SELECT work_item_id, type FROM work_items FINAL
			WHERE org_id = {org_id:String} AND repo_id IN `+teamPRLinkRepositorySet+` AND work_item_id IN (
				SELECT work_item_id FROM work_graph_issue_pr
				WHERE org_id = {org_id:String} AND repo_id IN `+teamPRLinkRepositorySet+`
			)
		) AS wi ON wi.work_item_id = l.work_item_id
		WHERE l.org_id = {org_id:String} AND l.repo_id IN `+teamPRLinkRepositorySet+`
			AND `+linkTierRankSQL+` > 0 AND `+workItemIsIssueSQL("wi")+`
	)) AS linked
FROM git_pull_requests AS pr FINAL
WHERE pr.org_id = {org_id:String} AND pr.repo_id IN `+teamPRLinkRepositorySet+``+
		themeInvestmentRangePredicate(bound, "pr.created_at", "ifNull(pr.merged_at, ifNull(pr.closed_at, toDateTime64('2999-12-31 00:00:00', 6, 'UTC')))")+`
GROUP BY pr.repo_id
ORDER BY pr.repo_id`, limit)
}

// readTeamPRLinkState returns, per repository id of repoIDs that has any pull
// request in the window, its pull request count and its admitted-link count.
func (p *InvestmentProvider) readTeamPRLinkState(ctx context.Context, orgID string, repoIDs []string, bound factTimeBound) (pullRequests, linked map[string]int64, err error) {
	pullRequests, linked = map[string]int64{}, map[string]int64{}
	if len(repoIDs) == 0 {
		return pullRequests, linked, nil
	}
	sorted := append([]string(nil), repoIDs...)
	sort.Strings(sorted)
	statement := teamPRLinkStatement(bound, teamPRLinkChunk+1)
	for start := 0; start < len(sorted); start += teamPRLinkChunk {
		chunk := sorted[start:min(start+teamPRLinkChunk, len(sorted))]
		got := 0
		err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamPullRequestLinkState", statement, orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
			var repoID string
			var prs, links uint64
			if err := row.Scan(&repoID, &prs, &links); err != nil {
				return err
			}
			got++
			pullRequests[repoID] = int64(prs)
			linked[repoID] = int64(links)
			return nil
		}, boundReaderBindings(bound)...)
		if err != nil {
			return nil, nil, err
		}
		if got > teamPRLinkChunk {
			return nil, nil, fmt.Errorf("team pull request link state returned %d rows for %d repositories", got, len(chunk))
		}
	}
	return pullRequests, linked, nil
}

// splitTeamsWithoutLinkedWork reads the pull request link state of the
// repositories of every team that has no work unit, in one pass, and moves the
// teams whose pull requests in the window are all without an admitted link out
// of the no-unit list. A team with no pull request, or with at least one
// linked pull request, stays in it: that is a quiet team or a unit not yet
// computed.
//
// caused names the teams (by team id) that already carry a more specific cause
// token for their empty window; they are never given this one, so the order of
// the empty-reason tokens holds whichever change lands first. A team in caused
// is neither read nor moved.
func (p *InvestmentProvider) splitTeamsWithoutLinkedWork(ctx context.Context, orgID string, noUnit []contextfabric.SubjectRef, caused map[string]bool, bySubject map[string]contextfabric.SubjectRef, owned map[string][]string, reachedOnly map[string][]teamReachedRepo, bound factTimeBound) (stay []contextfabric.SubjectRef, moved []teamNoLinkedWork, err error) {
	teamOfSubject := make(map[string]string, len(noUnit))
	for teamID, subject := range bySubject {
		teamOfSubject[contextfabric.FactSubjectKey(subject)] = teamID
	}
	teamRepos := make(map[string][]string, len(noUnit))
	repoSet := map[string]bool{}
	for _, subject := range noUnit {
		teamID := teamOfSubject[contextfabric.FactSubjectKey(subject)]
		if caused[teamID] {
			continue
		}
		seen := map[string]bool{}
		for _, repoID := range owned[teamID] {
			seen[repoID] = true
		}
		for _, repo := range reachedOnly[teamID] {
			seen[repo.key] = true
		}
		for repoID := range seen {
			teamRepos[teamID] = append(teamRepos[teamID], repoID)
			repoSet[repoID] = true
		}
	}
	repoIDs := make([]string, 0, len(repoSet))
	for repoID := range repoSet {
		repoIDs = append(repoIDs, repoID)
	}
	pullRequests, linked, err := p.readTeamPRLinkState(ctx, orgID, repoIDs, bound)
	if err != nil {
		return nil, nil, err
	}
	for _, subject := range noUnit {
		teamID := teamOfSubject[contextfabric.FactSubjectKey(subject)]
		if caused[teamID] {
			stay = append(stay, subject)
			continue
		}
		var prs, links int64
		for _, repoID := range teamRepos[teamID] {
			prs += pullRequests[repoID]
			links += linked[repoID]
		}
		if prs > 0 && links == 0 {
			moved = append(moved, teamNoLinkedWork{subject: subject, pullRequests: prs})
			continue
		}
		stay = append(stay, subject)
	}
	return stay, moved, nil
}
