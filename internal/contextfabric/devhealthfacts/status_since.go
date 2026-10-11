package devhealthfacts

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// status_since_basis is the closed vocabulary that says where status_since
// came from. A null status_since always carries no_transition_recorded: an
// item with no recorded entry into its current status is not a zero duration
// and is not a duration from its creation.
const (
	statusSinceBasisObserved = "transition_observed"
	statusSinceBasisNone     = "no_transition_recorded"
	// statusSinceBasisInconsistent: the item has recorded transitions but the
	// latest one does not enter its current status (or is dated after the
	// request clock), so no entry time is trusted. Distinct from no rows.
	statusSinceBasisInconsistent = "transition_inconsistent"
)

// statusSinceStatement returns the latest recorded status transition of each
// work item named by {ids} ("<repository uuid>:<work item id>", the subject id
// form), inside the SAME authorization relation as the status read: the scope
// join and expression come from readers.WorkItemScopeSQL, so a work item the
// caller may not read has no row here whatever the status read returned. Ties
// on occurred_at break on last_synced and then to_status, so the pick is
// deterministic.
func statusSinceStatement(scope readers.AuthorizationScope, settings readers.Settings) (string, []readers.Binding) {
	rendered := readers.WorkItemScopeSQL(scope)
	return readers.WithSettings(withRowLimit(`SELECT toString(w.repo_id) AS repo_key, w.work_item_id AS item_key, t.latest_to_status AS latest_to_status, t.entered_at AS entered_at
FROM (
	SELECT repo_id, work_item_id,
		argMax(ifNull(to_status, ''), tuple(occurred_at, last_synced, ifNull(to_status, ''))) AS latest_to_status,
		max(occurred_at) AS entered_at
	FROM work_item_transitions FINAL
	WHERE org_id = {org_id:String} AND concat(toString(repo_id), ':', work_item_id) IN {ids:Array(String)}
	GROUP BY repo_id, work_item_id
) AS t
INNER JOIN work_items AS w FINAL ON w.repo_id = t.repo_id AND w.work_item_id = t.work_item_id `+rendered.JoinSQL+`
WHERE w.org_id = {org_id:String} AND (`+rendered.AuthorizationExpr+`)
ORDER BY repo_key, item_key`), settings), rendered.Bindings
}

type statusEntry struct {
	toStatus  string
	enteredAt time.Time
}

// readStatusEntries reads the latest transition of every authorized work item
// in ids, keyed "<repository uuid>:<work item id>".
func (p *StatusProvider) readStatusEntries(ctx context.Context, orgID string, ids []string, scope readers.AuthorizationScope, settings readers.Settings) (map[string]statusEntry, error) {
	out := make(map[string]statusEntry, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	statement, scopeBindings := statusSinceStatement(scope, settings)
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadWorkItemStatusSince", statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var repoKey, itemKey, toStatus string
		var enteredAt time.Time
		if err := row.Scan(&repoKey, &itemKey, &toStatus, &enteredAt); err != nil {
			return err
		}
		out[repoKey+":"+itemKey] = statusEntry{toStatus: toStatus, enteredAt: enteredAt}
		return nil
	}, scopeBindings...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// statusSinceFields turns the latest transition of one item into the three
// status_since fields. Only a latest transition INTO the current status counts:
// when the latest transition went elsewhere the item's status column and its
// transition log disagree, and an older matching entry would overstate the
// time (transition_inconsistent). A recorded entry after the request clock is
// folded into the same value: no trusted duration exists.
func statusSinceFields(currentStatus string, entry statusEntry, found bool, now time.Time) map[string]contextfabric.FactValue {
	absent := func(basis string) map[string]contextfabric.FactValue {
		return map[string]contextfabric.FactValue{
			"status_since":            contextfabric.NullFactValue(),
			"status_duration_seconds": contextfabric.NullFactValue(),
			"status_since_basis":      contextfabric.StringFactValue(basis),
		}
	}
	if !found || currentStatus == "" {
		return absent(statusSinceBasisNone)
	}
	if entry.toStatus != currentStatus || entry.enteredAt.IsZero() || entry.enteredAt.After(now) {
		return absent(statusSinceBasisInconsistent)
	}
	return map[string]contextfabric.FactValue{
		"status_since":            contextfabric.StringFactValue(entry.enteredAt.UTC().Format(time.RFC3339)),
		"status_duration_seconds": contextfabric.IntegerFactValue(int64(now.Sub(entry.enteredAt) / time.Second)),
		"status_since_basis":      contextfabric.StringFactValue(statusSinceBasisObserved),
	}
}
