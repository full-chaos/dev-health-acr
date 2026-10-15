package devhealthfacts

import (
	"context"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/relationcurrent"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const blockerAuthorizedIDsBinding = "blocker_authorized_ids"

// blockerAuthorizedIDsBound is the most authorized blocker endpoints that ride
// as one Array binding; it stays under the statement text limit the endpoint
// subqueries exist to avoid (about 12,000 ids).
var blockerAuthorizedIDsBound = 0

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
		logBlockerAuthorization(ctx, "in_statement", len(ids), true)
		return set, bindings, nil
	}
	logBlockerAuthorization(ctx, "materialized", len(ids), false)
	return "{" + blockerAuthorizedIDsBinding + ":Array(String)}", []readers.Binding{{Name: blockerAuthorizedIDsBinding, Value: ids}}, nil
}

// blockerAuthorizationMemo keeps the authorization one team read resolved so
// its later statements reuse it instead of reading the relation again.
type blockerAuthorizationMemo struct {
	sql      string
	current  string
	bindings []readers.Binding
	settings readers.Settings
}

// logBlockerAuthorization records which way the blocker statements were
// authorized: the organization-wide shape, the relation read once with its ids
// carried as a binding, or the relation kept inside each statement because the
// endpoints exceeded the bound (the read stops one past the bound, so the count
// is then a lower bound, marked by over_bound). mode is a closed vocabulary.
func logBlockerAuthorization(ctx context.Context, mode string, ids int, overBound bool) {
	level := slog.LevelInfo
	if overBound {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "devhealthfacts.blocker_authorization", "mode", contextfabric.SanitizeLogAttr(mode), "authorized_endpoint_count", ids, "over_bound", overBound, "bound", blockerAuthorizedIDsBound)
}

// blockerScope resolves, once per request, the authorized endpoints
// (blockerAuthorization) and the end rule the statements carry: the links the
// provider no longer reports are tested inside each statement, which measured
// no heavier than carrying them as a binding (hash pair, tuple set).
func (p *BlockersProvider) blockerScope(ctx context.Context, principal storage.Principal, orgID string, requestedScope []string, settings readers.Settings) (authorized, current string, bindings []readers.Binding, err error) {
	authorized, bindings, err = p.blockerAuthorization(ctx, principal, orgID, requestedScope, settings)
	if err != nil {
		return "", "", nil, err
	}
	return authorized, relationcurrent.BlockerCurrentSQL("d"), bindings, nil
}
