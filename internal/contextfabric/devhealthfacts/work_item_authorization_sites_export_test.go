package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// AuthorizationRelationForTest renders the library authorization relation of
// principal's grant over the ids candidates (a parenthesized subquery) names;
// "" is every id of the organization. It never takes the organization-wide
// shortcut, so the library relation itself is measured for every principal.
func AuthorizationRelationForTest(principal storage.Principal, candidates string) (string, []readers.Binding) {
	return workItemAuthorizedIDsLibrarySQL(workItemRepositoryAuthorization(principal, nil), candidates)
}

// RepoLessIssueStatementForTest mirrors the statement of
// contextpacket.ClickHouseSourceExecutor.ReadableRepositorylessIssueIDs
// (work_item_title.go:69-73) over an endpoint subquery of the caller's: the
// same relation, join and authorization expression, one thread. It is a copy,
// so a change to the original is not seen here.
func RepoLessIssueStatementForTest(principal storage.Principal, endpoints string) (string, []readers.Binding) {
	scope := readers.WorkItemScopeSQL(workItemRepositoryAuthorization(principal, nil))
	statement := `SELECT w.work_item_id FROM work_items AS w FINAL ` + scope.JoinSQL + ` ` +
		`WHERE w.org_id = {org_id:String} AND w.repo_id = toUUID('00000000-0000-0000-0000-000000000000') AND w.work_item_id IN ` + endpoints + ` ` +
		`AND (` + scope.AuthorizationExpr + `) GROUP BY w.work_item_id LIMIT 5000 SETTINGS max_threads = 1`
	return statement, scope.Bindings
}
