package devhealthfacts

import (
	"context"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/readers"
)

// pullRequestProjectScope is the scope value a project row of a pull request's
// project_breakdown carries; the grouped-cohort reader selects rows by it.
const pullRequestProjectScope = "project"

// pullRequestProjectLinksSQL reads, per pull request, the projects it reaches
// through the issue link of record: pull request <- work_graph_issue_pr link
// <- issue (never a pull-request-typed item) <- project. It is the same
// strict reading projectLinkedRepositories applies in the other direction: a
// link counts only when the pull request it names exists and carries a known
// tier, and a project key that resolves to more than one project is omitted
// rather than guessed. An issue's own repository column is never used.
//
// One row per (pull request, project), with the strongest link tier among the
// links that reach that project; the tier is named on the row so a consumer
// never presents a lower tier as native.
const pullRequestProjectRowLimit = "5000"

var pullRequestProjectLinksSQL = `SELECT concat(toString(l.repo_id), ':', toString(l.pr_number)) AS pr_key,
       pj.provider, pj.id, any(pj.name),
       toUInt8(max(` + linkTierRankSQL + `)),
       argMax(l.provenance, ` + linkTierRankSQL + `)
FROM work_graph_issue_pr AS l FINAL
INNER JOIN git_pull_requests AS pr FINAL ON ` + linkedPullRequestOnSQL + `
INNER JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
INNER JOIN ` + projectKeyResolutionSubquery + ` AS p ON p.join_key = w.project_id
INNER JOIN projects AS pj FINAL ON pj.org_id = l.org_id AND pj.id = p.id
WHERE l.org_id = {org_id:String}
  AND concat(toString(l.repo_id), ':', toString(l.pr_number)) IN {ids:Array(String)}
  AND ` + linkTierRankSQL + ` > 0
  AND p.key_resolution_count = 1
  AND ` + workItemIsIssueSQL("w") + `
GROUP BY pr_key, pj.provider, pj.id
ORDER BY pr_key, pj.provider, pj.id
LIMIT ` + pullRequestProjectRowLimit

// pullRequestProjectRow is one project a pull request reaches.
type pullRequestProjectRow struct {
	canonicalID string
	label       string
	tier        string
	provider    string
	id          string
}

// readPullRequestProjects returns, keyed by "repoID:number", the projects each
// pull request of ids reaches, sorted by canonical id. A pull request with no
// admitted link has no entry: absent, never an empty or zero row.
func (p *PullRequestsProvider) readPullRequestProjects(ctx context.Context, orgID string, ids []string) (projects map[string][]pullRequestProjectRow, truncated bool, err error) {
	out := map[string][]pullRequestProjectRow{}
	if len(ids) == 0 {
		return out, false, nil
	}
	scanned := 0
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadPullRequestProjects", pullRequestProjectLinksSQL, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var key, provider, id, name, tier string
		var rank uint8
		scanned++
		if err := row.Scan(&key, &provider, &id, &name, &rank, &tier); err != nil {
			return err
		}
		canonical, omitted, deriveErr := identity.Derive(identity.KindProject, []string{provider, id}, nil)
		if deriveErr != nil || omitted || !validLinkTier(tier) {
			return nil
		}
		out[key] = append(out[key], pullRequestProjectRow{canonicalID: canonical, label: name, tier: tier, provider: provider, id: id})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	for key := range out {
		rows := out[key]
		sort.Slice(rows, func(i, j int) bool { return rows[i].canonicalID < rows[j].canonicalID })
	}
	return out, scanned >= 5000, nil
}

// validLinkTier is the closed provenance vocabulary of the link of record.
func validLinkTier(tier string) bool {
	switch tier {
	case "native", "explicit_text", "heuristic":
		return true
	}
	return false
}

// pullRequestProjectBreakdown is the project_breakdown table of one pull
// request: one row per project it reaches, carrying the project's canonical
// id, its name and the link tier.
func pullRequestProjectBreakdown(projects []pullRequestProjectRow) (value contextfabric.FactValue, evidence []string, ok bool) {
	if len(projects) == 0 {
		return contextfabric.FactValue{}, nil, false
	}
	rows := make([]contextfabric.FactValueRow, 0, len(projects))
	for _, project := range projects {
		fields := map[string]contextfabric.FactValue{
			"scope":     contextfabric.StringFactValue(pullRequestProjectScope),
			"scope_id":  contextfabric.StringFactValue(project.canonicalID),
			"link_tier": contextfabric.StringFactValue(project.tier),
		}
		if project.label != "" {
			fields["scope_name"] = contextfabric.StringFactValue(project.label)
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: fields})
		evidence = append(evidence, evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, project.provider+":"+project.id))
	}
	rows, _ = capFactValueRows(rows)
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"scope_id"},
		Observations: []string{"scope", "scope_name", "link_tier"},
		Rows:         rows,
	}), evidence, true
}
