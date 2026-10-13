package devhealthfacts

import "strings"

// scopeCandidateSets is every candidate set a scope expander statement filters
// by, rendered by the production builders.
func scopeCandidateSets() []string {
	return []string{
		requestedProjectHistoryWorkItemIDsSQL(requestedProjectIDsPredicate),
		requestedProjectWorkItemIDsSQL(requestedProjectIDsPredicate),
		requestedProjectWorkItemIDsSQL(requestedProjectKeysPredicate),
		teamOwnedProjectWorkItemIDsSQL,
	}
}

// OrgWideScopeStatementForTest rewrites a served scope expander statement to
// its organization-wide form (every candidate filter removed, every link,
// transition and work item read over the whole organization), the statement
// the expander sent before it filtered first. It reports whether the
// statement carried a candidate filter, and fails (ok false) when a filter is
// left after the rewrite.
func OrgWideScopeStatementForTest(statement string) (orgWide string, filtered, ok bool) {
	out := strings.ReplaceAll(statement, "INNER JOIN "+teamOwnedProjectWorkItemsSQL+" AS w ON ", "INNER JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND ")
	out = strings.ReplaceAll(out, " AND (repo_id, work_item_id) IN "+teamAttributedWorkItemKeysSQL, "")
	for _, candidates := range scopeCandidateSets() {
		out = strings.ReplaceAll(out, issueLinkRowsAmongSQL(candidates), issueLinkRowsSQL)
		out = strings.ReplaceAll(out, membershipTouchesAsOfAmongSQL(candidates), membershipTouchesAsOfAmongSQL(""))
		out = strings.ReplaceAll(out, "LEFT JOIN (SELECT work_item_id, type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN "+candidates+") AS w ON w.work_item_id = a.work_item_id", "LEFT JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND w.work_item_id = a.work_item_id")
		out = strings.ReplaceAll(out, " AND w.work_item_id IN "+candidates, "")
		out = strings.ReplaceAll(out, " AND subject_id IN "+candidates, "")
	}
	ok = !strings.Contains(out, teamAttributedWorkItemKeysSQL) && !strings.Contains(out, teamOwnedProjectWorkItemIDsSQL)
	for _, candidates := range scopeCandidateSets() {
		ok = ok && !strings.Contains(out, candidates)
	}
	return out, out != statement, ok
}
