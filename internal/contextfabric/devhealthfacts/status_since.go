package devhealthfacts

import (
	"context"
	"sort"
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

// statusSinceInstantCap is the most transitions of one item at its last
// instant that the read keeps. An instant with more is not read: its status
// is not decided and the item answers transition_inconsistent.
const statusSinceInstantCap = 16

// statusSinceStatement returns, for each (repository, work item) pair in
// {keys} ("<repository uuid>:<work item id>"), the transitions of the item's
// LAST instant: how many there are, their (from_status, to_status) pairs in
// string order (at most statusSinceInstantCap + 1 of them), and the instant.
// One row per pair, whatever the history holds.
// The pairs are the rows the SCOPED status read just returned in the same
// request, so an item the caller may not read is never named here and the
// authorization relation is not repeated: repeating it would run the whole
// project/ownership/link relation a second time per status read. The read is
// narrowed to the sort key prefix (org_id, repo_id, work_item_id) and bounded
// by the same SETTINGS as the status read and by the status read's probe
// limit (one past the output bound), so every pair it names can return its
// row whatever order the two statements sort in. The key ends with
// tie_ordinal, so FINAL keeps every transition of one instant; the order
// among them is the status chain (lastToStatusOfInstant), not tie_ordinal and
// not last_synced.
func statusSinceStatement(settings readers.Settings) string {
	return readers.WithSettings(withRowLimitOf(`SELECT repo_key, item_key, count() AS instant_rows,
	arrayMap(p -> p.1, arraySort(groupArray(17)((from_status, to_status)))) AS from_statuses,
	arrayMap(p -> p.2, arraySort(groupArray(17)((from_status, to_status)))) AS to_statuses,
	max(occurred_at) AS entered_at
FROM (
	SELECT toString(t.repo_id) AS repo_key, t.work_item_id AS item_key,
		ifNull(t.from_status, '') AS from_status, ifNull(t.to_status, '') AS to_status, t.occurred_at AS occurred_at,
		max(t.occurred_at) OVER (PARTITION BY t.repo_id, t.work_item_id) AS last_at
	FROM work_item_transitions AS t FINAL
	WHERE t.org_id = {org_id:String}
		AND t.repo_id IN {repo_ids:Array(UUID)}
		AND t.work_item_id IN {ids:Array(String)}
		AND concat(toString(t.repo_id), ':', t.work_item_id) IN {keys:Array(String)}
)
WHERE occurred_at = last_at
GROUP BY repo_key, item_key
ORDER BY repo_key, item_key`, maxFactRowsProbe), settings)
}

type statusEntry struct {
	toStatus  string
	enteredAt time.Time
	// capped: the last instant holds more than statusSinceInstantCap
	// transitions, so toStatus is not decided.
	capped bool
}

type instantTransition struct{ from, to string }

// lastToStatusOfInstant is the status an item holds after the transitions of
// its last instant, ordered as the ops readers order them: dev-health-ops
// internal/jobs/metrics/workitemmetrics/order.go, OrderTransitions (:8-33) and
// nextTransitionInChain (:35-52). Inside one instant the status chain is
// walked: the transition that leaves the prior status, else a chain head (its
// from_status is no other remaining transition's to_status), else the first in
// (from_status, to_status) string order. It never reads the item's current
// status.
//
// The one difference from ops: ops starts the walk of an instant at the status
// the previous instant ended on. That needs the whole history of the item, so
// this read does not have it and starts with no prior (ops' known = false
// branch). The two orders differ only when the last instant has no chain head
// (a cycle) or several heads AND the previous status would have decided
// between them. entered_at is the same for every transition of the instant, so
// a different pick can only turn transition_observed into
// transition_inconsistent or back; it never moves the time.
func lastToStatusOfInstant(transitions []instantTransition) string {
	remaining := append([]instantTransition(nil), transitions...)
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].from != remaining[j].from {
			return remaining[i].from < remaining[j].from
		}
		return remaining[i].to < remaining[j].to
	})
	prior, known, last := "", false, ""
	for len(remaining) > 0 {
		pick := nextTransitionInChain(remaining, prior, known)
		prior, known, last = remaining[pick].to, true, remaining[pick].to
		remaining = append(remaining[:pick], remaining[pick+1:]...)
	}
	return last
}

func nextTransitionInChain(remaining []instantTransition, prior string, known bool) int {
	if known {
		for i, transition := range remaining {
			if transition.from == prior {
				return i
			}
		}
	}
	for i, transition := range remaining {
		head := true
		for j, other := range remaining {
			if j != i && other.to == transition.from {
				head = false
				break
			}
		}
		if head {
			return i
		}
	}
	return 0
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
		var repoKey, itemKey string
		var instantRows uint64
		var fromStatuses, toStatuses []string
		var enteredAt time.Time
		if err := row.Scan(&repoKey, &itemKey, &instantRows, &fromStatuses, &toStatuses, &enteredAt); err != nil {
			return err
		}
		entry := statusEntry{enteredAt: enteredAt}
		if instantRows > statusSinceInstantCap || len(fromStatuses) != len(toStatuses) || uint64(len(toStatuses)) != instantRows {
			entry.capped = true
		} else {
			transitions := make([]instantTransition, len(toStatuses))
			for i := range toStatuses {
				transitions[i] = instantTransition{from: fromStatuses[i], to: toStatuses[i]}
			}
			entry.toStatus = lastToStatusOfInstant(transitions)
		}
		out[repoKey+":"+itemKey] = entry
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
type statusSinceResult struct {
	since, seconds contextfabric.FactValue
	basis          string
}

func statusSinceFields(currentStatus string, entry statusEntry, found bool, now time.Time) statusSinceResult {
	absent := func(basis string) statusSinceResult {
		return statusSinceResult{since: contextfabric.NullFactValue(), seconds: contextfabric.NullFactValue(), basis: basis}
	}
	if !found || currentStatus == "" {
		return absent(statusSinceBasisNone)
	}
	if entry.capped || entry.toStatus != currentStatus || entry.enteredAt.IsZero() || entry.enteredAt.After(now) {
		return absent(statusSinceBasisInconsistent)
	}
	return statusSinceResult{
		since:   contextfabric.StringFactValue(entry.enteredAt.UTC().Format(time.RFC3339)),
		seconds: contextfabric.IntegerFactValue(int64(now.Sub(entry.enteredAt) / time.Second)),
		basis:   statusSinceBasisObserved,
	}
}
