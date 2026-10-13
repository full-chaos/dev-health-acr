package devhealthfacts

import (
	"strings"

	"github.com/full-chaos/dev-health-go/readers"
)

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
	current := readers.OwnershipValidityPredicate(readers.TimeBound{})
	out = strings.ReplaceAll(out, teamOwnedProjectsSQL, "(SELECT provider, id, team_id FROM "+readers.ProjectOwnershipCatalogJoinSQL(current)+")")
	out = strings.ReplaceAll(out, requestedTeamRepositoryOwnershipSQL, readers.TeamRepositoryOwnershipSQL(current))
	out = strings.ReplaceAll(out, " AND (repo_id, work_item_id) IN "+teamAttributedWorkItemKeysSQL, "")
	for _, candidates := range scopeCandidateSets() {
		out = strings.ReplaceAll(out, issueLinkRowsAmongSQL(candidates), issueLinkRowsSQL)
		out = strings.ReplaceAll(out, membershipTouchesAsOfAmongSQL(candidates), membershipTouchesAsOfAmongSQL(""))
		out = strings.ReplaceAll(out, "LEFT JOIN (SELECT work_item_id, type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN "+candidates+") AS w ON w.work_item_id = a.work_item_id", "LEFT JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND w.work_item_id = a.work_item_id")
		out = strings.ReplaceAll(out, " AND w.work_item_id IN "+candidates, "")
		out = strings.ReplaceAll(out, " AND subject_id IN "+candidates, "")
	}
	ok = !strings.Contains(out, teamAttributedWorkItemKeysSQL) && !strings.Contains(out, teamOwnedProjectWorkItemIDsSQL) && !strings.Contains(out, requestedTeamsPredicate)
	for _, candidates := range scopeCandidateSets() {
		ok = ok && !strings.Contains(out, candidates)
	}
	return out, out != statement, ok
}

// OrgWideScopeReadsForTest names each organization-wide read a statement
// still carries: the whole organization's link rows, a work item table joined
// or streamed whole, the unfiltered membership touches or transition
// subjects.
func OrgWideScopeReadsForTest(statement string) []string {
	var found []string
	for name, fragment := range map[string]string{
		"organization link rows":           issueLinkRowsSQL,
		"organization work items in link":  "work_items AS wi FINAL",
		"organization touches":             membershipTouchesAsOfAmongSQL(""),
		"organization transition subjects": "subject_kind = 'work_item'\n    )",
		"work items joined whole":          "JOIN work_items AS w FINAL ON",
		"organization work items joined":   "FROM work_items FINAL WHERE org_id = {org_id:String}) AS w",
		"work items streamed whole":        "WHERE w.org_id = {org_id:String} AND p.key_resolution_count",
	} {
		if strings.Contains(statement, fragment) {
			found = append(found, name)
		}
	}
	return found
}
