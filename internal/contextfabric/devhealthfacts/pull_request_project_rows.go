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
//
// The fallback-only rule (ops issueprlinks.NativeLinks.Outranks) needs a
// second statement only when a row is below native: a link below native counts
// only when no native row covers its pull request or its issue.
// pullRequestProjectFallbackLinksSQL reads, for the pull requests of those rows
// only, the decision with a fixed-size state per group: the pull request side
// is one flag per group (every link of the group names the same pull request),
// the issue side is folded into maxIf/argMaxIf/countIf over the group's links,
// so no group collects its links, however many there are. A read whose rows
// are all native sends the first statement only.
//
// pullRequestProjectRowCap is the most rows one read admits; the statement
// asks for one more so a full page is told apart from a cut one.
const pullRequestProjectRowCap = 5000

const pullRequestProjectRowLimit = "5001"

const pullRequestProjectKeySQL = `concat(toString(lk.pr_repo_id), ':', toString(lk.pr_number))`

// pullRequestProjectLinksFromSQL is the (pull request, project) groups of the
// requested pull requests ({ids}).
var pullRequestProjectLinksFromSQL = `
FROM ` + admittedIssueLinksSQL + ` AS lk
INNER JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND w.work_item_id = lk.work_item_id
INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = w.provider AND p.join_key = w.project_id
INNER JOIN projects AS pj FINAL ON pj.org_id = {org_id:String} AND pj.provider = p.provider AND pj.id = p.id
WHERE ` + pullRequestProjectKeySQL + ` IN {ids:Array(String)}
  AND p.key_resolution_count = 1
GROUP BY pr_key, pj.provider, pj.id`

var pullRequestProjectLinksSQL = `SELECT ` + pullRequestProjectKeySQL + ` AS pr_key,
       pj.provider, pj.id, any(pj.name),
       toUInt8(max(lk.link_rank)),
       argMax(lk.provenance, lk.link_rank)` + pullRequestProjectLinksFromSQL + `
ORDER BY pr_key, pj.provider, pj.id
LIMIT ` + pullRequestProjectRowLimit

// pullRequestProjectNativePullRequestsSQL is the asked pull requests that
// have a native row, as "repoID:number".
const pullRequestProjectNativePullRequestsSQL = `(SELECT concat(toString(repo_id), ':', toString(pr_number)) FROM work_graph_issue_pr
  WHERE org_id = {org_id:String} AND provenance = 'native' AND concat(toString(repo_id), ':', toString(pr_number)) IN {ids:Array(String)})`

// pullRequestProjectNativeIssuesSQL is the issues linked to an asked pull
// request that have a native row anywhere in the organization.
const pullRequestProjectNativeIssuesSQL = `(SELECT work_item_id FROM work_graph_issue_pr
  WHERE org_id = {org_id:String} AND provenance = 'native' AND work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr
    WHERE org_id = {org_id:String} AND concat(toString(repo_id), ':', toString(pr_number)) IN {ids:Array(String)}))`

// pullRequestProjectFallbackSQL is a link below native; ...UncoveredSQL is one
// whose issue has no native row.
const (
	pullRequestProjectFallbackSQL  = `lk.link_rank < 3`
	pullRequestProjectUncoveredSQL = pullRequestProjectFallbackSQL + ` AND lk.work_item_id NOT IN ` + pullRequestProjectNativeIssuesSQL
)

// pullRequestProjectFallbackLinksSQL is the fallback-only decision for the
// below-native groups of the asked pull requests ({ids}: the pull requests of
// the first statement's rows below native).
var pullRequestProjectFallbackLinksSQL = `SELECT ` + pullRequestProjectKeySQL + ` AS pr_key,
       pj.provider, pj.id,
       toUInt8(pr_key IN ` + pullRequestProjectNativePullRequestsSQL + `),
       countIf(` + pullRequestProjectFallbackSQL + `),
       countIf(` + pullRequestProjectFallbackSQL + ` AND lk.work_item_id IN ` + pullRequestProjectNativeIssuesSQL + `),
       toUInt8(maxIf(lk.link_rank, ` + pullRequestProjectUncoveredSQL + `)),
       argMaxIf(lk.provenance, lk.link_rank, ` + pullRequestProjectUncoveredSQL + `)` + pullRequestProjectLinksFromSQL + `
HAVING max(lk.link_rank) < 3
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
		if err := row.Scan(&r.key, &r.provider, &r.id, &r.name, &rank, &r.tier); err != nil {
			return err
		}
		r.rank = rank
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if err := p.readPullRequestProjectFallbacks(ctx, orgID, rows); err != nil {
		return nil, false, err
	}
	rows = dropCoveredProjectLinks(ctx, rows)
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

// readPullRequestProjectFallbacks fills the fallback-only decision of every
// row below native from pullRequestProjectFallbackLinksSQL, read for those
// rows' pull requests only; it reads nothing when every row is native. A row
// below native the decision does not name keeps no uncovered link (its links
// changed between the two reads) and is dropped.
func (p *PullRequestsProvider) readPullRequestProjectFallbacks(ctx context.Context, orgID string, rows []pullRequestProjectLinkRow) error {
	keys := map[string]struct{}{}
	for _, r := range rows {
		if r.rank < nativeRank {
			keys[r.key] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	type groupKey struct{ key, provider, id string }
	decided := map[groupKey]pullRequestProjectLinkRow{}
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadPullRequestProjectFallbacks", pullRequestProjectFallbackLinksSQL, orgID, sortedKeys(keys), func(row contextpacket.ClickHouseRowScanner) error {
		var r pullRequestProjectLinkRow
		if err := row.Scan(&r.key, &r.provider, &r.id, &r.pullRequestCovered, &r.fallbackLinks, &r.issueCoveredLinks, &r.remainingRank, &r.remainingTier); err != nil {
			return err
		}
		decided[groupKey{r.key, r.provider, r.id}] = r
		return nil
	})
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].rank >= nativeRank {
			continue
		}
		d := decided[groupKey{rows[i].key, rows[i].provider, rows[i].id}]
		rows[i].pullRequestCovered, rows[i].fallbackLinks, rows[i].issueCoveredLinks = d.pullRequestCovered, d.fallbackLinks, d.issueCoveredLinks
		rows[i].remainingRank, rows[i].remainingTier = d.remainingRank, d.remainingTier
	}
	return nil
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
