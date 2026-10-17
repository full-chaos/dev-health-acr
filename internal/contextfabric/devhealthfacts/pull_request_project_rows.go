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
// rather than guessed. An issue's own repository column is never used. The
// link rows come from the one shared reader (admittedIssueLinksSQL).
//
// One row per (pull request, project), with the strongest link tier among the
// links that reach that project; the tier is named on the row so a consumer
// never presents a lower tier as native.
// pullRequestProjectRowCap is the most rows one read admits; the statement
// asks for one more so a full page is told apart from a cut one.
const pullRequestProjectRowCap = 5000

const pullRequestProjectRowLimit = "5001"

var pullRequestProjectLinksSQL = `SELECT concat(toString(lk.pr_repo_id), ':', toString(lk.pr_number)) AS pr_key,
       pj.provider, pj.id, any(pj.name),
       toUInt8(max(lk.link_rank)),
       argMax(lk.provenance, lk.link_rank),
       groupUniqArrayIf(concat(toString(lk.link_rank), '|', lk.provenance, '|', lk.work_item_id), lk.link_rank < 3)
FROM ` + admittedIssueLinksSQL + ` AS lk
INNER JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND w.work_item_id = lk.work_item_id
INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = w.provider AND p.join_key = w.project_id
INNER JOIN projects AS pj FINAL ON pj.org_id = {org_id:String} AND pj.provider = p.provider AND pj.id = p.id
WHERE concat(toString(lk.pr_repo_id), ':', toString(lk.pr_number)) IN {ids:Array(String)}
  AND p.key_resolution_count = 1
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
	skipped := 0
	var rows []pullRequestProjectLinkRow
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadPullRequestProjects", pullRequestProjectLinksSQL, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r pullRequestProjectLinkRow
		var rank uint8
		scanned++
		if scanned > pullRequestProjectRowCap {
			skipped++
			return nil
		}
		if err := row.Scan(&r.key, &r.provider, &r.id, &r.name, &rank, &r.tier, &r.fallbacks); err != nil {
			return err
		}
		r.rank = rank
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	rows, err = dropCoveredProjectLinks(ctx, p.facts.client, orgID, rows)
	if err != nil {
		return nil, false, err
	}
	for _, r := range rows {
		canonical, omitted, deriveErr := identity.Derive(identity.KindProject, []string{r.provider, r.id}, nil)
		if deriveErr != nil || omitted || !validLinkTier(r.tier) {
			continue
		}
		out[r.key] = append(out[r.key], pullRequestProjectRow{canonicalID: canonical, label: r.name, tier: r.tier, provider: r.provider, id: r.id})
	}
	for key := range out {
		rows := out[key]
		sort.Slice(rows, func(i, j int) bool { return rows[i].canonicalID < rows[j].canonicalID })
	}
	return out, skipped > 0, nil
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
func pullRequestProjectBreakdown(projects []pullRequestProjectRow) (value contextfabric.FactValue, evidence []string, omitted int, ok bool) {
	if len(projects) == 0 {
		return contextfabric.FactValue{}, nil, 0, false
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
	}
	rows, omitted = capFactValueRows(rows)
	// Evidence only for the rows the table keeps: a cited project must be one
	// the served table names.
	for _, project := range projects[:len(rows)] {
		evidence = append(evidence, evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, project.provider+":"+project.id))
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"scope_id"},
		Observations: []string{"scope", "scope_name", "link_tier"},
		Rows:         rows,
	}), evidence, omitted, true
}
