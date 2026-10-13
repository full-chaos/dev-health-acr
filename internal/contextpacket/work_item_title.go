package contextpacket

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-go/readers"
)

const (
	nilRepositoryUUID = "00000000-0000-0000-0000-000000000000"

	workGraphSourceID = "work_graph.v1"
	// maxReadableRepositorylessIssueIDs bounds the gate result; an issue past
	// the bound is named by its key alone.
	maxReadableRepositorylessIssueIDs = 5000
)

// workGraphIssueEndpointIDsSQL is every issue key that is an endpoint of the
// scoped repository's work-graph edges.
const workGraphIssueEndpointIDsSQL = `(SELECT source_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND source_type = 'issue' ` +
	`UNION ALL SELECT target_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = {repo_id:UUID} AND target_type = 'issue')`

// workGraphIssueTitlesSQL yields one row per issue key that is an endpoint of
// the scoped repository's edges, with the title only when exactly one distinct
// title is readable for that key and it fits a label (an empty title
// otherwise): every readable title counts toward a collision, however long. The work_items
// key (org_id, repo_id, work_item_id) bounds the read to the scoped
// repository's own rows and rows with no repository. The scoped repository is
// authorized upstream; a row with no repository is readable only when its key
// is in readable_repositoryless_issue_ids, which the caller's grant decided
// (ReadableRepositorylessIssueIDs).
const workGraphIssueTitlesSQL = `SELECT w.work_item_id AS id, if(uniqExact(w.title) = 1 AND lengthUTF8(any(w.title)) <= 1000, any(w.title), '') AS title ` +
	`FROM work_items AS w FINAL ` +
	`WHERE w.org_id = {org_id:String} AND w.repo_id IN ({repo_id:UUID}, toUUID('` + nilRepositoryUUID + `')) AND w.work_item_id IN ` + workGraphIssueEndpointIDsSQL + ` ` +
	`AND (w.repo_id = {repo_id:UUID} OR w.work_item_id IN {readable_repositoryless_issue_ids:Array(String)}) ` +
	`AND w.title != '' GROUP BY w.work_item_id`

// workGraphIssueTitlesMapSQL is the issue titles as one scalar map (issue key
// to title). ClickHouse evaluates identical scalar subqueries once per
// statement, so the labels look the map up without a join (warm, about 3.7 MB
// against 5.0 MB for a join per endpoint). It is inlined at each use because a
// named alias would be an unqualified name the org-scope sweep cannot tell
// from a column.
const workGraphIssueTitlesMapSQL = `(SELECT mapFromArrays(groupArray(id), groupArray(title)) FROM (` + workGraphIssueTitlesSQL + `))`

func workGraphIssueTitleOf(id string) string {
	return "arrayElement(" + workGraphIssueTitlesMapSQL + ", " + id + ")"
}

// RepositorylessIssueReader is optionally implemented by an executor that can
// decide which repository-less issues the caller may read.
type RepositorylessIssueReader interface {
	ReadableRepositorylessIssueIDs(context.Context, ReadPlan) ([]string, error)
}

// ReadableRepositorylessIssueIDs returns the keys of the scoped repository's
// issue endpoints that have no repository of their own and that the caller's
// repository grant authorizes: the shared work-item authorization relation
// admits such an issue through its project's owning teams or through a native
// pull request link into a granted repository. The relation reads whole
// organization relations, so it runs once per packet, on one thread, apart
// from the catalog statement.
func (e *ClickHouseSourceExecutor) ReadableRepositorylessIssueIDs(ctx context.Context, plan ReadPlan) (_ []string, err error) {
	if e == nil || e.client == nil {
		return nil, fmt.Errorf("contextpacket: clickhouse query client is required")
	}
	scope := readers.WorkItemScopeSQL(readers.AuthorizationScope{RepositorySelectors: &readers.RepositorySelectorScope{Granted: plan.WorkItemGrant}})
	statement := `SELECT w.work_item_id FROM work_items AS w FINAL ` + scope.JoinSQL + ` ` +
		`WHERE w.org_id = {org_id:String} AND w.repo_id = toUUID('` + nilRepositoryUUID + `') AND w.work_item_id IN ` + workGraphIssueEndpointIDsSQL + ` ` +
		`AND (` + scope.AuthorizationExpr + `) GROUP BY w.work_item_id LIMIT ` + fmt.Sprint(maxReadableRepositorylessIssueIDs) + ` SETTINGS max_threads = 1`
	bindings := append([]ClickHouseBinding{{Name: "org_id", Value: plan.OrgID}, {Name: "repo_id", Value: plan.RepoID}}, scope.Bindings...)
	rows, err := e.client.Query(ctx, statement, bindings)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
