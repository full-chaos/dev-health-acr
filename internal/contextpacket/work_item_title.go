package contextpacket

import (
	"github.com/full-chaos/dev-health-go/readers"
)

const nilRepositoryUUID = "00000000-0000-0000-0000-000000000000"

// workItemTitleScope renders the shared work-item authorization relation for a
// caller's repository grant. The SQL text does not depend on the grant, only
// the bindings do.
func workItemTitleScope(grant readers.RepositorySelectorSet) readers.WorkItemScopeSQLResult {
	return readers.WorkItemScopeSQL(readers.AuthorizationScope{RepositorySelectors: &readers.RepositorySelectorScope{Granted: grant}})
}

// workGraphIssueTitlesSQL yields one row per issue key that is an endpoint of
// the scoped repository's edges, with the title only when exactly one distinct
// title is readable for that key by the caller (an empty title otherwise).
// Rows are read by the work_items key (org_id, repo_id, work_item_id): the
// scoped repository's own rows and rows with no repository. A row with no
// repository is readable only through the shared authorization relation (the
// caller's project grant or a native pull request link into a granted
// repository), so the grant decides, never the issue's own repository as a
// relation.
var workGraphIssueTitlesSQL = func() string {
	scope := workItemTitleScope(readers.RepositorySelectorSet{})
	readable := "(" + scope.AuthorizationExpr + ") AND lengthUTF8(w.title) BETWEEN 1 AND 1000"
	return `SELECT w.work_item_id AS id, if(uniqExactIf(w.title, ` + readable + `) = 1, anyIf(w.title, ` + readable + `), '') AS title ` +
		`FROM work_items AS w FINAL ` + scope.JoinSQL + ` ` +
		`WHERE w.org_id = {org_id:String} AND w.repo_id IN ({repo_id:UUID}, toUUID('` + nilRepositoryUUID + `')) AND w.work_item_id IN ` +
		`(SELECT source_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND source_type = 'issue' ` +
		`UNION ALL SELECT target_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND target_type = 'issue') ` +
		`GROUP BY w.work_item_id`
}()
