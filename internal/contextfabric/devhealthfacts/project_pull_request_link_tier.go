package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// projectPullRequestLinkTiers returns, keyed by "<repo id>:<number>" and then by
// provider-qualified requested project key, the provenance tier of the
// issue-to-pull-request link rows (ops table work_graph_issue_pr) that tie one
// of the target pull requests to an issue of that project. A project with no
// such link to the pull request is absent: it reached the pull request through
// its repository only.
//
// Native cover: a pull request that has a native link from any issue gets no
// lower-tier link, so a requested project keeps a link only when its tier equals
// the strongest admitted tier of the pull request across every project. The read
// is bounded to the target pull requests.
func (e *ScopeExpander) projectPullRequestLinkTiers(ctx context.Context, orgID string, projectKeys []string, targets []contextfabric.SubjectRef) (map[string]map[string]string, error) {
	pullRequestKeys := make([]string, 0, len(targets))
	for _, target := range targets {
		if key, found := strings.CutPrefix(target.CanonicalID, "pull_request:"); found {
			pullRequestKeys = append(pullRequestKeys, key)
		}
	}
	if len(projectKeys) == 0 || len(pullRequestKeys) == 0 {
		return nil, nil
	}
	workItemIDs := requestedProjectWorkItemIDsSQL(requestedProjectKeysPredicate)
	statement := `SELECT ` + projectQualifiedKeySQL + `, toString(l.pr_repo_id), l.pr_number, argMax(l.provenance, l.link_rank)
FROM work_items AS w FINAL
INNER JOIN ` + currentResolvedProjectsSQL + ` AS p ON p.join_key = w.project_id
INNER JOIN ` + admittedIssueLinksAmongSQL(workItemIDs) + ` AS l ON l.work_item_id = w.work_item_id
INNER JOIN ` + strongestLinkTierOfPullRequestsSQL + ` AS c ON c.cover_repo_id = toString(l.pr_repo_id) AND c.cover_number = l.pr_number
WHERE w.org_id = {org_id:String} AND w.work_item_id IN ` + workItemIDs + ` AND p.key_resolution_count = 1 AND ` + projectQualifiedKeySQL + ` IN {project_ids:Array(String)}
  AND l.link_rank = c.cover_rank AND concat(toString(l.pr_repo_id), '` + projectOriginKeySeparator + `', toString(l.pr_number)) IN {pull_request_keys:Array(String)}
GROUP BY ` + projectQualifiedKeySQL + `, toString(l.pr_repo_id), l.pr_number`
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "project_ids", Value: projectKeys},
		{Name: "pull_request_keys", Value: pullRequestKeys},
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tiers := map[string]map[string]string{}
	pairs := map[string]int{}
	for rows.Next() {
		var projectKey, repoID, tier string
		var number uint32
		if err := rows.Scan(&projectKey, &repoID, &number, &tier); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s:%d", repoID, number)
		if tiers[key] == nil {
			tiers[key] = map[string]string{}
		}
		tiers[key][projectKey] = tier
		pairs[tier]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slog.Default().InfoContext(ctx, "context_fabric: project pull request link tiers", "projects", len(projectKeys), "target_pull_requests", len(pullRequestKeys), "linked_pull_requests", len(tiers),
		"native", pairs["native"], "explicit_text", pairs["explicit_text"], "heuristic", pairs["heuristic"])
	return tiers, nil
}

// pullRequestLinkTierTargets maps each target pull request to the link tier of
// each requested project that holds one, keyed by FactSubjectKey of the target
// and then of the project subject.
func pullRequestLinkTierTargets(targets []contextfabric.SubjectRef, tiers map[string]map[string]string, originsByKey map[string]contextfabric.SubjectRef) map[string]map[string]string {
	var out map[string]map[string]string
	for _, target := range targets {
		id, found := strings.CutPrefix(target.CanonicalID, "pull_request:")
		if !found {
			continue
		}
		for projectKey, tier := range tiers[id] {
			origin, ok := originsByKey[projectKey]
			if !ok {
				continue
			}
			if out == nil {
				out = map[string]map[string]string{}
			}
			key := contextfabric.FactSubjectKey(target)
			if out[key] == nil {
				out[key] = map[string]string{}
			}
			out[key][contextfabric.FactSubjectKey(origin)] = tier
		}
	}
	return out
}
