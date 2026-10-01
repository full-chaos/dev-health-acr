package devhealthfacts

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// RepoLessWorkItemAdmitter decides whether a caller may read a work item that
// carries no repository (the zero repository UUID: every Linear work item).
// It applies the rule the scope expander applies, through the same
// readers.WorkItemScopeSQL relation and the same admitsWorkItem decision:
// the organization-wide grant admits it; a repository-restricted caller is
// admitted only through a project-ownership or native pull-request-link path
// to a repository its grant names.
type RepoLessWorkItemAdmitter struct {
	client contextpacket.ClickHouseQueryClient
}

// NewRepoLessWorkItemAdmitter builds the admitter over the shared query
// boundary.
func NewRepoLessWorkItemAdmitter(client contextpacket.ClickHouseQueryClient) *RepoLessWorkItemAdmitter {
	return &RepoLessWorkItemAdmitter{client: client}
}

// AdmitsRepoLessWorkItem reports whether principal may read the repo-less
// work item workItemID of its organization. A work item with no repo-less
// row is not admitted.
func (a *RepoLessWorkItemAdmitter) AdmitsRepoLessWorkItem(ctx context.Context, principal storage.Principal, workItemID string) (bool, error) {
	if a == nil || a.client == nil {
		return false, errors.New("devhealthfacts: repo-less work item admitter has no query client")
	}
	rendered := readers.WorkItemScopeSQL(workItemRepositoryAuthorization(principal, nil))
	statement := `SELECT
` + workItemScopeAuthorizationColumnsSQL(rendered) + `
FROM work_items AS w FINAL
` + rendered.JoinSQL + `
WHERE w.org_id = {org_id:String} AND w.work_item_id = {work_item_id:String} AND toString(w.repo_id) = '` + zeroRepositoryID + `'
GROUP BY w.work_item_id`
	statement = readers.WithSettings(statement, readers.Settings{MaxRowsToRead: workItemScopeSelectionMaxRowsToRead})
	bindings := append([]contextpacket.ClickHouseBinding{{Name: "org_id", Value: principal.OrgID}, {Name: "work_item_id", Value: workItemID}}, rendered.Bindings...)
	rows, err := a.client.Query(ctx, statement, bindings)
	if err != nil {
		return false, workItemScopeReadError(err)
	}
	defer rows.Close()
	admittedAny := false
	for rows.Next() {
		var authorized uint8
		var paths, repositories, excluded []string
		if err := rows.Scan(&authorized, &paths, &repositories, &excluded); err != nil {
			return false, err
		}
		if authorized != 1 {
			return false, rows.Err()
		}
		admitted, _ := admitsWorkItem(principal, noRepositorySentinelForScope, true, paths, repositories)
		admittedAny = admitted
	}
	return admittedAny, rows.Err()
}
