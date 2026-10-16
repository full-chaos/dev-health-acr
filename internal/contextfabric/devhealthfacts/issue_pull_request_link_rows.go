package devhealthfacts

// The issue <> pull request link of record is work_graph_issue_pr, one row per
// (pull request repository, issue, pull request number) carrying a provenance
// tier: native 3 > explicit_text 2 > heuristic 1; a lower tier counts only
// where no native row covers its pull request or its issue (nativeFirstLinkSQL).
// Every reader of the link in
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

// nativeFirstLinkSQL is the fallback-only rule of the link of record (ops
// issueprlinks.NativeLinks.Outranks): an explicit_text or heuristic row counts
// only when no native row covers its pull request (repo_id, pr_number) or its
// issue. A native row always counts. l is the work_graph_issue_pr alias. Ops
// retracts the covered rows at the source; the native sets are read without
// FINAL because a native row is never replaced by a lower tier (version_rank),
// so any stored native row is a current one; this keeps a read right before that
// build has run for an organization.
//
// scope is a condition over the unaliased work_graph_issue_pr columns that
// names the link rows being checked ("" for every row of the organization).
// The native sets are read only for the issues and pull requests those rows
// name, so a scoped read does not grow with the organization's unrelated links.
func nativeFirstLinkSQL(scope string) string {
	issueScope, pullRequestScope := "", ""
	if scope != "" {
		checked := ` WHERE org_id = {org_id:String} AND ` + scope + `)`
		issueScope = ` AND work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr` + checked
		pullRequestScope = ` AND (repo_id, pr_number) IN (SELECT repo_id, pr_number FROM work_graph_issue_pr` + checked
	}
	return `(l.provenance = 'native' OR (
    NOT (l.work_item_id IN (SELECT work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND provenance = 'native'` + issueScope + `))
    AND (l.repo_id, l.pr_number) NOT IN (SELECT repo_id, pr_number FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND provenance = 'native'` + pullRequestScope + `)))`
}

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
	workItems, pullRequests, among, scope := `work_items AS wi FINAL`, `git_pull_requests AS pr FINAL`, ``, ``
	if candidates != "" {
		scope = `work_item_id IN ` + candidates
		workItems = `(SELECT org_id, work_item_id, type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN ` + candidates + `) AS wi`
		pullRequests = `(SELECT org_id, repo_id, number FROM git_pull_requests FINAL WHERE org_id = {org_id:String} AND (repo_id, number) IN (SELECT repo_id, pr_number FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND work_item_id IN ` + candidates + `)) AS pr`
		among = ` AND l.work_item_id IN ` + candidates
	}
	return `(
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, l.provenance AS provenance, ` + linkTierRankSQL + ` AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN ` + pullRequests + ` ON ` + linkedPullRequestOnSQL + `
  LEFT JOIN ` + workItems + ` ON wi.org_id = l.org_id AND wi.work_item_id = l.work_item_id
  WHERE l.org_id = {org_id:String}` + among + ` AND ` + nativeFirstLinkSQL(scope) + ` AND ` + workItemIsIssueSQL("wi") + `
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
// items to issues joins it on work_item_id, and so reads the work item table
// once.
var admittedLinkedPullRequestRowsSQL = `(
  SELECT l.work_item_id AS work_item_id, l.repo_id AS pr_repo_id, l.pr_number AS pr_number, ` + linkTierRankSQL + ` AS link_rank
  FROM work_graph_issue_pr AS l FINAL
  INNER JOIN git_pull_requests AS pr FINAL ON ` + linkedPullRequestOnSQL + `
  WHERE l.org_id = {org_id:String} AND ` + linkTierRankSQL + ` > 0 AND ` + nativeFirstLinkSQL("") + `
)`
