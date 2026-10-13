package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextpacket"

type ScopeProbeFragment struct {
	Name, SQL string
	Bindings  []contextpacket.ClickHouseBinding
}

func ScopeProbeFragmentsForTest(orgID string) []ScopeProbeFragment {
	b := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}, {Name: "project_ids", Value: []string{"P", "Q"}}}
	cand := requestedProjectWorkItemIDsSQL(requestedProjectIDsPredicate)
	keys := `(SELECT repo_id, work_item_id FROM work_items WHERE org_id = {org_id:String} AND work_item_id IN ` + cand + `)`
	lkeys := `(SELECT repo_id, work_item_id FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND work_item_id IN ` + cand + `)`
	return []ScopeProbeFragment{
		{"candIDs", "SELECT count() FROM " + cand, b},
		{"wFinalIDs", "SELECT count() FROM work_items AS w FINAL WHERE w.org_id = {org_id:String} AND w.work_item_id IN " + cand, b},
		{"wFinalKeys", "SELECT count() FROM work_items AS w FINAL WHERE w.org_id = {org_id:String} AND (w.repo_id, w.work_item_id) IN " + keys, b},
		{"wFinalAll", "SELECT count() FROM work_items AS w FINAL WHERE w.org_id = {org_id:String}", b},
		{"linksAmong", "SELECT count() FROM " + admittedIssueLinksAmongSQL(cand), b},
		{"lFinalIDs", "SELECT count() FROM work_graph_issue_pr AS l FINAL WHERE l.org_id = {org_id:String} AND l.work_item_id IN " + cand, b},
		{"lFinalKeys", "SELECT count() FROM work_graph_issue_pr AS l FINAL WHERE l.org_id = {org_id:String} AND (l.repo_id, l.work_item_id) IN " + lkeys, b},
		{"prPrune", "SELECT count() FROM git_pull_requests FINAL WHERE org_id = {org_id:String} AND (repo_id, number) IN (SELECT repo_id, pr_number FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND work_item_id IN " + cand + ")", b},
		{"projects", "SELECT count() FROM " + currentResolvedProjectsSQL, b},
	}
}
