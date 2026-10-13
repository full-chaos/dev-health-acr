package devhealthfacts

// The issue <> pull request link of record is work_graph_issue_pr, one row per
// (pull request repository, issue, pull request number) carrying a provenance
// tier: native 3 > explicit_text 2 > heuristic 1. Every reader of the link in
// this package reads it through issueLinkRowsSQL (or its admitted subset), so
// one rule decides whether a row counts:
//
//   - the pull request the row names exists (a stale link is skipped, as the
//     canonical link projection skips it), and
//   - the work item is an issue (a work item typed pr or merge_request is not).
//
// repo_id of a link row is the PULL REQUEST's repository, never the issue's
// own repository column.

// linkTierRankSQL ranks the three provenance tiers of the link of record,
// ops' spelling; any other value ranks 0. l is the work_graph_issue_pr alias.
const linkTierRankSQL = `multiIf(l.provenance = 'native', 3, l.provenance = 'explicit_text', 2, l.provenance = 'heuristic', 1, 0)`

// linkedPullRequestOnSQL joins a link row (l) to the pull request (pr) it names.
const linkedPullRequestOnSQL = `pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number`

// workItemIsIssueSQL is the predicate that excludes pull-request-typed work
// items; alias is the work_items alias.
func workItemIsIssueSQL(alias string) string {
	return `ifNull(` + alias + `.type, '') NOT IN ('pr', 'merge_request')`
}

// issueLinkRowsSQL is the organization-scoped link rows whose pull request
// exists and whose work item is an issue, columns work_item_id, pr_repo_id
// (the pull request's repository), pr_number, provenance, link_rank (0 for a tier outside
// the vocabulary, which a caller that must count it can still see). Rows are
// not de-duplicated per issue: a caller aggregates per repository and takes
// the strongest tier with argMax(provenance, link_rank).
var issueLinkRowsSQL = issueLinkRowsAmongSQL("")

// issueLinkRowsAmongSQL is issueLinkRowsSQL over the link rows of the work
// item ids candidates (a subquery) names, with the work item rows and the pull
// request rows read for those ids' links only; "" is every link row of the
// organization. Every row of a kept id, and every version of a kept pull
// request, is still read, so the rule over a kept id is unchanged.
func issueLinkRowsAmongSQL(candidates string) string {
	workItems, pullRequests, among := `work_items AS wi FINAL`, `git_pull_requests AS pr FINAL`, ``
	if candidates != "" {
		workItems = `(SELECT org_id, work_item_id, type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN ` + candidates + `) AS wi`
		pullRequests = `(SELECT org_id, repo_id, number FROM git_pull_requests FINAL WHERE org_id = {org_id:String} AND (repo_id, number) IN (SELECT repo_id, pr_number FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND work_item_id IN ` + candidates + `)) AS pr`
		among = ` AND l.work_item_id IN ` + candidates
	}
	return `(
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, l.provenance AS provenance, ` + linkTierRankSQL + ` AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN ` + pullRequests + ` ON ` + linkedPullRequestOnSQL + `
  LEFT JOIN ` + workItems + ` ON wi.org_id = l.org_id AND wi.work_item_id = l.work_item_id
  WHERE l.org_id = {org_id:String}` + among + ` AND ` + workItemIsIssueSQL("wi") + `
)`
}

// admittedIssueLinksSQL is issueLinkRowsSQL restricted to the known tiers: the
// links that name a repository.
var admittedIssueLinksSQL = admittedIssueLinksAmongSQL("")

// admittedIssueLinksAmongSQL is admittedIssueLinksSQL over the work item ids
// candidates names (see issueLinkRowsAmongSQL).
func admittedIssueLinksAmongSQL(candidates string) string {
	return `(SELECT work_item_id, pr_repo_id, pr_number, provenance, link_rank FROM ` + issueLinkRowsAmongSQL(candidates) + ` WHERE link_rank > 0)`
}

// issuesWithLinkedPullRequestSQL is the set of issue ids that have at least
// one admitted link row.
var issuesWithLinkedPullRequestSQL = `(SELECT work_item_id FROM ` + admittedIssueLinksSQL + `)`

// linkedWorkItemIDsSQL is every work item id with a link row in the
// organization, of any tier and version: a superset of the admitted link rows'
// work items, used to prune work_items by primary key before a join to them.
const linkedWorkItemIDsSQL = `(SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String})`

// linkedWorkItemIDsDistinctSQL is linkedWorkItemIDsSQL with one row per id,
// the hashed side of a semi join to work_items: the join keeps the linked
// ids out of an IN set, whose hash table cost several hundred bytes per
// organization link.
const linkedWorkItemIDsDistinctSQL = `(SELECT DISTINCT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String})`

// admittedLinkedPullRequestRowsSQL is the admitted link rows (known tier) whose
// pull request exists, columns work_item_id, pr_repo_id, pr_number, link_rank,
// without reading work_items: a caller that has already restricted the work
// items to issues joins it on work_item_id, and so reads the work item table
// once.
const admittedLinkedPullRequestRowsSQL = `(
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, ` + linkTierRankSQL + ` AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN git_pull_requests AS pr FINAL ON ` + linkedPullRequestOnSQL + `
  WHERE l.org_id = {org_id:String} AND ` + linkTierRankSQL + ` > 0
)`
