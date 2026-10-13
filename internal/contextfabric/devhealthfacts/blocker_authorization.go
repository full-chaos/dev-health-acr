package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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

const blockerEndedLinksBinding = "blocker_ended_links"

// blockerEndedLinksBound is the most ended links that ride as one Array
// binding; above it the end rule stays inside each statement.
var blockerEndedLinksBound = 5000

// blockerScope resolves, once per request, the two sets every blocker
// statement is narrowed by: the authorized endpoints (blockerAuthorization) and
// the links the provider no longer reports. Each is read in a statement of its
// own and carried into the blocker statements as a binding, so none of them
// builds either relation again.
func (p *BlockersProvider) blockerScope(ctx context.Context, principal storage.Principal, orgID string, requestedScope []string, settings readers.Settings) (authorized, current string, bindings []readers.Binding, err error) {
	authorized, bindings, err = p.blockerAuthorization(ctx, principal, orgID, requestedScope, settings)
	if err != nil {
		return "", "", nil, err
	}
	statement := readers.WithSettings(fmt.Sprintf("SELECT %s FROM %s ORDER BY 1 LIMIT %d", "concat(s, char(31), t, char(31), ifNull(rt, ''))", relationEndedKeysSQL(), blockerEndedLinksBound+1), withMaxResultRows(settings, blockerEndedLinksBound+1))
	ended := []string{}
	if err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadBlockerEndedLinks", statement, orgID, nil, func(row readers.RowScanner) error {
		var key string
		if err := row.Scan(&key); err != nil {
			return err
		}
		ended = append(ended, key)
		return nil
	}); err != nil {
		return "", "", nil, err
	}
	if len(ended) > blockerEndedLinksBound {
		logBlockerEndedLinks(ctx, "in_statement", len(ended), true)
		return authorized, relationCurrentSQL("d"), bindings, nil
	}
	logBlockerEndedLinks(ctx, "materialized", len(ended), false)
	current = ` AND ` + relationLinkKeySQL("d") + ` NOT IN {` + blockerEndedLinksBinding + `:Array(String)}`
	return authorized, current, append(bindings, readers.Binding{Name: blockerEndedLinksBinding, Value: ended}), nil
}

func withMaxResultRows(settings readers.Settings, rows int) readers.Settings {
	settings.MaxResultRows = uint64(rows)
	return settings
}

// logBlockerEndedLinks records how the end rule reached the blocker
// statements: the ended links read once and bound (materialized), or kept
// inside each statement because they exceeded the bound (the read stops one
// past it, so the count is then a lower bound, marked by over_bound).
func logBlockerEndedLinks(ctx context.Context, mode string, ended int, overBound bool) {
	level := slog.LevelInfo
	if overBound {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "devhealthfacts.blocker_ended_links", "mode", contextfabric.SanitizeLogAttr(mode), "ended_link_count", ended, "over_bound", overBound, "bound", blockerEndedLinksBound)
}
