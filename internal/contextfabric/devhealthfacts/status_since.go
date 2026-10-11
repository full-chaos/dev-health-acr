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
// (repository, work item) pair in {keys} ("<repository uuid>:<work item id>").
// The pairs are the rows the SCOPED status read just returned in the same
// request, so an item the caller may not read is never named here and the
// authorization relation is not repeated: repeating it would run the whole
// project/ownership/link relation a second time per status read. The read is
// narrowed to the sort key prefix (org_id, repo_id, work_item_id) and bounded
// by the same SETTINGS and row limit as the status read. Ties on occurred_at
// break on last_synced and then to_status, so the pick is deterministic.
func statusSinceStatement(settings readers.Settings) string {
	return readers.WithSettings(withRowLimit(`SELECT toString(t.repo_id) AS repo_key, t.work_item_id AS item_key,
	argMax(ifNull(t.to_status, ''), tuple(t.occurred_at, t.last_synced, ifNull(t.to_status, ''))) AS latest_to_status,
	max(t.occurred_at) AS entered_at
FROM work_item_transitions AS t FINAL
WHERE t.org_id = {org_id:String}
	AND t.repo_id IN {repo_ids:Array(UUID)}
	AND t.work_item_id IN {ids:Array(String)}
	AND concat(toString(t.repo_id), ':', t.work_item_id) IN {keys:Array(String)}
GROUP BY t.repo_id, t.work_item_id
ORDER BY repo_key, item_key`), settings)
}

type statusEntry struct {
	toStatus  string
	enteredAt time.Time
}

// readStatusEntries reads the latest transition of every (repository, item)
// pair in pairs, keyed "<repository uuid>:<work item id>". pairs come only
// from the scoped status read.
func (p *StatusProvider) readStatusEntries(ctx context.Context, orgID string, pairs [][2]string, settings readers.Settings) (map[string]statusEntry, error) {
	out := make(map[string]statusEntry, len(pairs))
	if len(pairs) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(pairs))
	repoIDs := make([]string, 0, len(pairs))
	keys := make([]string, 0, len(pairs))
	seenID, seenRepo := map[string]bool{}, map[string]bool{}
	for _, pair := range pairs {
		keys = append(keys, pair[0]+":"+pair[1])
		if !seenRepo[pair[0]] {
			seenRepo[pair[0]] = true
			repoIDs = append(repoIDs, pair[0])
		}
		if !seenID[pair[1]] {
			seenID[pair[1]] = true
			ids = append(ids, pair[1])
		}
	}
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadWorkItemStatusSince", statusSinceStatement(settings), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var repoKey, itemKey, toStatus string
		var enteredAt time.Time
		if err := row.Scan(&repoKey, &itemKey, &toStatus, &enteredAt); err != nil {
			return err
		}
		out[repoKey+":"+itemKey] = statusEntry{toStatus: toStatus, enteredAt: enteredAt}
		return nil
	}, readers.Binding{Name: "repo_ids", Value: repoIDs}, readers.Binding{Name: "keys", Value: keys})
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
