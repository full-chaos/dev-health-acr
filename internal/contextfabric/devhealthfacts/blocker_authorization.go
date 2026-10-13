package devhealthfacts

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const blockerAuthorizedIDsBinding = "blocker_authorized_ids"

// blockerAuthorizedIDsBound is the most authorized blocker endpoints that ride
// as one Array binding; it stays under the statement text limit the endpoint
// subqueries exist to avoid (about 12,000 ids).
var blockerAuthorizedIDsBound = 5000

// blockerAuthorization is the SQL set the blocker statements test both ends of
// an edge against. An organization-wide caller is authorized by the work item
// table alone. Any other caller's relation (project ownership, team repository
// ownership, links) is read ONCE here, in a statement of its own, and the
// statements take the authorized endpoint ids as a binding, so no statement
// carries the relation on top of its own reads. Above the bound the relation
// stays inside each statement.
func (p *BlockersProvider) blockerAuthorization(ctx context.Context, principal storage.Principal, orgID string, requestedScope []string, settings readers.Settings) (string, []readers.Binding, error) {
	scope := workItemRepositoryAuthorization(principal, requestedScope)
	set, bindings := workItemAuthorizedIDsAmongSQL(scope, blockerEndpointIDsSQL)
	if organizationWideAuthorization(scope) {
		return set, bindings, nil
	}
	// The statement's result is the id list itself, so its row cap is the bound
	// (one past it tells "too many"), not the reader's per-fact probe cap.
	settings.MaxResultRows = uint64(blockerAuthorizedIDsBound + 1)
	statement := readers.WithSettings(fmt.Sprintf("SELECT work_item_id FROM %s ORDER BY work_item_id LIMIT %d", set, blockerAuthorizedIDsBound+1), settings)
	ids := []string{}
	if err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadBlockerAuthorizedIDs", statement, orgID, nil, func(row readers.RowScanner) error {
		var id string
		if err := row.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}, bindings...); err != nil {
		return "", nil, err
	}
	if len(ids) > blockerAuthorizedIDsBound {
		return set, bindings, nil
	}
	return "{" + blockerAuthorizedIDsBinding + ":Array(String)}", []readers.Binding{{Name: blockerAuthorizedIDsBinding, Value: ids}}, nil
}

// blockerAuthorizationMemo keeps the authorization one team read resolved so
// its later statements reuse it instead of reading the relation again.
type blockerAuthorizationMemo struct {
	sql      string
	bindings []readers.Binding
	settings readers.Settings
}
