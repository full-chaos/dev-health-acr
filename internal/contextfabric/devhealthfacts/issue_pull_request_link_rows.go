package devhealthfacts

// The issue <> pull request link of record is work_graph_issue_pr, one row per
// (pull request repository, issue, pull request number) carrying a provenance
// tier: native 3 > explicit_text 2 > heuristic 1. Every reader of the link in
// this package reads it through issueLinkRowsSQL (or its admitted subset), so
// one rule decides whether a row counts:
//
//   - the pull request the row names exists (a stale link is skipped, as the
//     canonical link projection skips it), and
//   - the work item is an issue (a work item typed pr or merge_request is not), and
//   - the id names one work item row. work_items is keyed by (org_id, repo_id,
//     work_item_id) and a link row names no issue repository, so nothing says
//     which of several rows a link means. Such a link counts in no read here,
//     the rule of the link producer (devhealthsource
//     issuePullRequestLinkAmbiguous). An id with one row stays, in any repository.
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

// linkWorkItemsSQL is the work items of the ids (a subquery) as one row per id,
// the join side of a link read: id_rows counts the id's rows, pull-request-typed
// ones too, and type means one row only when id_rows is 1.
func linkWorkItemsSQL(ids string) string {
	return `(SELECT org_id, work_item_id, count() AS id_rows, any(type) AS type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN ` + ids + ` GROUP BY org_id, work_item_id) AS wi`
}

// linkIDOnOneRowSQL holds for a link row that does not name several work item
// rows; wi is linkWorkItemsSQL. A link row with no work item row holds.
const linkIDOnOneRowSQL = `ifNull(wi.id_rows, 0) <= 1`

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
	workItems, pullRequests, among := linkWorkItemsSQL(linkedWorkItemIDsSQL), `git_pull_requests AS pr FINAL`, ``
	if candidates != "" {
		workItems = linkWorkItemsSQL(candidates)
		pullRequests = `(SELECT org_id, repo_id, number FROM git_pull_requests FINAL WHERE org_id = {org_id:String} AND (repo_id, number) IN (SELECT repo_id, pr_number FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND work_item_id IN ` + candidates + `)) AS pr`
		among = ` AND l.work_item_id IN ` + candidates
	}
	return `(
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, l.provenance AS provenance, ` + linkTierRankSQL + ` AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN ` + pullRequests + ` ON ` + linkedPullRequestOnSQL + `
  LEFT JOIN ` + workItems + ` ON wi.org_id = l.org_id AND wi.work_item_id = l.work_item_id
  WHERE l.org_id = {org_id:String}` + among + ` AND ` + workItemIsIssueSQL("wi") + ` AND ` + linkIDOnOneRowSQL + `
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

// admittedLinkedPullRequestRowsSQL is the admitted link rows (known tier) whose
// pull request exists, columns work_item_id, pr_repo_id, pr_number, link_rank,
// without reading work_items: a caller that has already restricted the work
// items to issues, and to ids on one row (teamLinkCandidatesStatement), joins it
// on work_item_id, and so reads the work item table once.
var admittedLinkedPullRequestRowsSQL = admittedLinkedPullRequestRowsAmongSQL("")

// admittedLinkedPullRequestRowsAmongSQL is admittedLinkedPullRequestRowsSQL
// pruned to the link rows whose work item id hashes into candidateHashes (a
// UInt64 subquery), with the pull request rows kept for those links only; ""
// is every link row of the organization. A hash prune is a superset: a caller
// joins the rows to its own work items on the real id, which drops a row a
// colliding hash let through. The pull request filter names only pull request
// columns, so it is applied to the join's right side before it is filled.
// Every version of a kept link and of a kept pull request is still read, so
// FINAL keeps the same row of each.
func admittedLinkedPullRequestRowsAmongSQL(candidateHashes string) string {
	among := ``
	if candidateHashes != "" {
		among = ` AND cityHash64(l.work_item_id) IN ` + candidateHashes + `
    AND (pr.repo_id, pr.number) IN (SELECT lc.repo_id, lc.pr_number FROM work_graph_issue_pr AS lc WHERE lc.org_id = {org_id:String} AND cityHash64(lc.work_item_id) IN ` + candidateHashes + `)`
	}
	return `(
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, ` + linkTierRankSQL + ` AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN git_pull_requests AS pr FINAL ON ` + linkedPullRequestOnSQL + `
  WHERE l.org_id = {org_id:String}` + among + ` AND ` + linkTierRankSQL + ` > 0
)`
}

// linkCoversPullRequestSQL holds for a link row that counts in its pull
// request's cover, the link rows of a read that decides the fallback-only rule;
// wi is linkWorkItemsSQL. A row whose id names several work items counts only
// when it is native: it gives no link, and it still shows the provider attached
// an issue to that pull request (the entity-tree rule), as the link producer's
// outranked-link tombstone does. A lower-tier one says less: it covers nothing.
var linkCoversPullRequestSQL = `((` + linkIDOnOneRowSQL + ` AND ` + workItemIsIssueSQL("wi") + `) OR (NOT ` + linkIDOnOneRowSQL + ` AND l.provenance = 'native'))`

// strongestLinkTierOfPullRequestsSQL is, for each pull request named by
// {pull_request_keys} ("<repo id>:<number>"), the strongest admitted link tier
// across the issues of every project, requested or not: columns cover_repo_id,
// cover_number, cover_rank. It applies the admission rule of
// issueLinkRowsAmongSQL (the pull request exists, the work item is an issue,
// the tier is in the vocabulary) and reads the links of those pull requests
// only, with one difference (linkCoversPullRequestSQL): a native row whose id
// names several work items still covers its pull request.
var strongestLinkTierOfPullRequestsSQL = `(
  SELECT toString(l.repo_id) AS cover_repo_id, l.pr_number AS cover_number, max(` + linkTierRankSQL + `) AS cover_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN (SELECT org_id, repo_id, number FROM git_pull_requests FINAL WHERE org_id = {org_id:String} AND concat(toString(repo_id), '` + projectOriginKeySeparator + `', toString(number)) IN {pull_request_keys:Array(String)}) AS pr ON ` + linkedPullRequestOnSQL + `
  LEFT JOIN ` + linkWorkItemsSQL(`(SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND concat(toString(repo_id), '`+projectOriginKeySeparator+`', toString(pr_number)) IN {pull_request_keys:Array(String)})`) + ` ON wi.org_id = l.org_id AND wi.work_item_id = l.work_item_id
  WHERE l.org_id = {org_id:String} AND concat(toString(l.repo_id), '` + projectOriginKeySeparator + `', toString(l.pr_number)) IN {pull_request_keys:Array(String)} AND ` + linkCoversPullRequestSQL + ` AND ` + linkTierRankSQL + ` > 0
  GROUP BY cover_repo_id, cover_number
)`
