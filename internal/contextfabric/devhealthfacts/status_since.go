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
)

// statusSinceStatement returns the latest recorded status transition of each
// (repository, work item) pair named by {keys}, each "<repository uuid>:<work
// item id>" like the work item subject ids. Ties on occurred_at break on last_synced and then to_status,
// so the pick is deterministic.
func statusSinceStatement() string {
	return withRowLimit(`SELECT toString(t.repo_id) AS repo_key, t.work_item_id AS item_key,
	argMax(ifNull(t.to_status, ''), tuple(t.occurred_at, t.last_synced, ifNull(t.to_status, ''))) AS latest_to_status,
	max(t.occurred_at) AS entered_at
FROM work_item_transitions AS t FINAL
WHERE t.org_id = {org_id:String}
	AND t.work_item_id IN {ids:Array(String)}
	AND concat(toString(t.repo_id), ':', t.work_item_id) IN {keys:Array(String)}
GROUP BY t.repo_id, t.work_item_id
ORDER BY repo_key, item_key`)
}

type statusEntry struct {
	toStatus  string
	enteredAt time.Time
}

// readStatusEntries reads the latest transition of every (repo, item) pair in
// pairs. The caller passes only items the scoped status read already
// returned, so an item the caller may not read is never probed.
func (p *StatusProvider) readStatusEntries(ctx context.Context, orgID string, pairs [][2]string) (map[string]statusEntry, error) {
	out := make(map[string]statusEntry, len(pairs))
	if len(pairs) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(pairs))
	keys := make([]string, 0, len(pairs))
	seen := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		keys = append(keys, pair[0]+":"+pair[1])
		if !seen[pair[1]] {
			seen[pair[1]] = true
			ids = append(ids, pair[1])
		}
	}
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadWorkItemStatusSince", statusSinceStatement(), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var repoKey, itemKey, toStatus string
		var enteredAt time.Time
		if err := row.Scan(&repoKey, &itemKey, &toStatus, &enteredAt); err != nil {
			return err
		}
		out[repoKey+":"+itemKey] = statusEntry{toStatus: toStatus, enteredAt: enteredAt}
		return nil
	}, readers.Binding{Name: "keys", Value: keys})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// statusSinceFields turns the latest transition of one item into the three
// status_since fields. Only a latest transition INTO the current status counts:
// when the latest transition went elsewhere the item's status column and its
// transition log disagree, and an older matching entry would overstate the
// time. A recorded entry after the request clock is not a duration either.
func statusSinceFields(currentStatus string, entry statusEntry, found bool, now time.Time) map[string]contextfabric.FactValue {
	none := map[string]contextfabric.FactValue{
		"status_since":            contextfabric.NullFactValue(),
		"status_duration_seconds": contextfabric.NullFactValue(),
		"status_since_basis":      contextfabric.StringFactValue(statusSinceBasisNone),
	}
	if !found || currentStatus == "" || entry.toStatus != currentStatus || entry.enteredAt.IsZero() || entry.enteredAt.After(now) {
		return none
	}
	return map[string]contextfabric.FactValue{
		"status_since":            contextfabric.StringFactValue(entry.enteredAt.UTC().Format(time.RFC3339)),
		"status_duration_seconds": contextfabric.IntegerFactValue(int64(now.Sub(entry.enteredAt) / time.Second)),
		"status_since_basis":      contextfabric.StringFactValue(statusSinceBasisObserved),
	}
}
