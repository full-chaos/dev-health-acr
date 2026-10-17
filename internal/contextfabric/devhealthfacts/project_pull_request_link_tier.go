package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// projectPullRequestLinkTiers returns, keyed by "<repo id>:<number>", the
// strongest provenance tier of the issue-to-pull-request link rows (ops table
// work_graph_issue_pr) that tie a pull request of the given repositories to an
// issue of the requested projects. A pull request with no such link is absent:
// it was reached through its repository only.
func (e *ScopeExpander) projectPullRequestLinkTiers(ctx context.Context, orgID string, projectIDs, repoIDs []string) (map[string]string, error) {
	if len(projectIDs) == 0 || len(repoIDs) == 0 {
		return nil, nil
	}
	workItemIDs := requestedProjectWorkItemIDsSQL(requestedProjectIDsPredicate)
	statement := `SELECT toString(l.pr_repo_id), l.pr_number, argMax(l.provenance, l.link_rank)
FROM work_items AS w FINAL
INNER JOIN ` + currentResolvedProjectsSQL + ` AS p ON p.join_key = w.project_id
INNER JOIN ` + admittedIssueLinksAmongSQL(workItemIDs) + ` AS l ON l.work_item_id = w.work_item_id
WHERE w.org_id = {org_id:String} AND w.work_item_id IN ` + workItemIDs + ` AND p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)} AND toString(l.pr_repo_id) IN {repo_ids:Array(String)}
GROUP BY toString(l.pr_repo_id), l.pr_number`
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "project_ids", Value: projectIDs},
		{Name: "repo_ids", Value: repoIDs},
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tiers := map[string]string{}
	for rows.Next() {
		var repoID, tier string
		var number uint32
		if err := rows.Scan(&repoID, &number, &tier); err != nil {
			return nil, err
		}
		tiers[fmt.Sprintf("%s:%d", repoID, number)] = tier
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slog.Default().DebugContext(ctx, "context_fabric: project pull request link tiers", "projects", len(projectIDs), "repositories", len(repoIDs), "linked_pull_requests", len(tiers))
	return tiers, nil
}

// pullRequestLinkTierTargets maps each target pull request that holds a link
// tier to that tier, keyed by FactSubjectKey.
func pullRequestLinkTierTargets(targets []contextfabric.SubjectRef, tiers map[string]string) map[string]string {
	var out map[string]string
	for _, target := range targets {
		id, found := strings.CutPrefix(target.CanonicalID, "pull_request:")
		tier, ok := tiers[id]
		if !found || !ok {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[contextfabric.FactSubjectKey(target)] = tier
	}
	return out
}
