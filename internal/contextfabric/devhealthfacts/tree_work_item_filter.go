package devhealthfacts

import (
	"context"
	"log/slog"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// TreeWorkItemFilterReader implements contextfabric.TreeWorkItemFilter: it
// applies a status or completion-window qualifier to the members of a graph
// walk, on the canonical work_items facts of those members.
//
// STATUS PARITY with the project path (work_item_membership.go,
// workItemMembershipS1StatementFor: `w.status = {status_filter}`): the status
// reader selects `ifNull(w.status, ”)` from the same FINAL work_items
// column, so the value compared here is the value the project statement
// compares. The only difference is a NULL status, which the reader reads as ”
// and the project statement compares as NULL (never equal to a non-empty
// filter); with a non-empty filter both exclude it, and an unset filter is
// not applied by either. work_items.status is a non-Nullable String in the
// schema, so the case does not arise on a conforming table.
//
// COMPLETION: completed means completed_at is non-null (the same definition
// ActualCompletionProvider serves); the window is [start, end), the half-open
// window the project statement applies to w.completed_at.
type TreeWorkItemFilterReader struct{ facts clickhouseFacts }

var _ contextfabric.TreeWorkItemFilter = (*TreeWorkItemFilterReader)(nil)

// NewTreeWorkItemFilterReader returns a filter reading through client.
func NewTreeWorkItemFilterReader(client contextpacket.ClickHouseQueryClient) *TreeWorkItemFilterReader {
	return &TreeWorkItemFilterReader{facts: clickhouseFacts{client: client}}
}

// FilterTreeWorkItems implements contextfabric.TreeWorkItemFilter.
func (r *TreeWorkItemFilterReader) FilterTreeWorkItems(ctx context.Context, principal storage.Principal, request contextfabric.TreeWorkItemFilterRequest) ([]string, int, error) {
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return nil, 0, err
	}
	wantStatus := request.Status != ""
	wantWindow := !request.CompletedStart.IsZero() && !request.CompletedEnd.IsZero()

	ids, bySubject, rejected := v2Index(request.Members, identity.KindWorkItem)
	unread := rejected
	scope := workItemRepositoryAuthorization(principal, request.RequestedRepositoryScope)
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return nil, 0, readFailure("query tree work item filter", settingsErr)
	}

	// Distinct keys, in request order, so a repeated member is read once.
	seen := make(map[string]struct{}, len(ids))
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		keys = append(keys, id)
	}

	kept := make([]string, 0, len(keys))
	truncatedBatches := 0
	for start := 0; start < len(keys); start += maxFactRowsPerQuery {
		end := start + maxFactRowsPerQuery
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[start:end]

		// The status read answers presence for the unqualified and the
		// status-qualified cases; the completion read is added only for a
		// window, and a member must be present in every read it needs.
		var statusRows map[string]string
		var completionRows map[string]readers.WorkItemCompletionRow
		overflow := false
		if wantStatus || !wantWindow {
			rows, readErr := readers.ReadWorkItemStatusWithScopeAndRowLimit(ctx, r.facts.client, orgID, batch, scope, settings, maxFactRowsProbe)
			if readErr != nil {
				return nil, 0, readFailure("query tree work item status", readErr)
			}
			overflow = overflow || len(rows) > maxFactRowsPerQuery
			statusRows = make(map[string]string, len(rows))
			for _, row := range rows {
				statusRows[row.RepoID+":"+row.ID] = row.Status
			}
		}
		if wantWindow {
			rows, readErr := readers.ReadWorkItemCompletionWithScopeAndRowLimit(ctx, r.facts.client, orgID, batch, readers.TimeBound{}, scope, settings, maxFactRowsProbe)
			if readErr != nil {
				return nil, 0, readFailure("query tree work item completion", readErr)
			}
			overflow = overflow || len(rows) > maxFactRowsPerQuery
			completionRows = make(map[string]readers.WorkItemCompletionRow, len(rows))
			for _, row := range rows {
				completionRows[row.RepoID+":"+row.ID] = row
			}
		}

		// A read that crossed the output bound dropped rows we cannot name,
		// so nothing in the batch is trusted: it is all unread.
		if overflow {
			truncatedBatches++
			unread += len(batch)
			continue
		}

		for _, key := range batch {
			if statusRows != nil {
				if _, ok := statusRows[key]; !ok {
					unread++
					continue
				}
			}
			var completion readers.WorkItemCompletionRow
			if wantWindow {
				row, ok := completionRows[key]
				if !ok {
					unread++
					continue
				}
				completion = row
			}
			if wantStatus && statusRows[key] != request.Status {
				continue
			}
			if wantWindow {
				completedAt := completion.CompletedAt
				if completion.IsCompleted == 0 || completedAt.Before(request.CompletedStart) || !completedAt.Before(request.CompletedEnd) {
					continue
				}
			}
			kept = append(kept, bySubject[key].CanonicalID)
		}
	}
	sort.Strings(kept)

	slog.Debug("context_fabric_tree_work_item_filter",
		"members", len(request.Members), "kept", len(kept), "unread", unread,
		"undecodable", rejected, "truncated_batches", truncatedBatches,
		"status_qualifier", wantStatus, "window_qualifier", wantWindow)
	if truncatedBatches > 0 {
		slog.Warn("context_fabric_tree_work_item_filter_truncated", "truncated_batches", truncatedBatches)
	}
	return kept, unread, nil
}
