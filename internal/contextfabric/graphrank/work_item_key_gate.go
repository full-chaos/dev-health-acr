package graphrank

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// maxWorkItemKeyPrefixReads bounds the prefix reads of one turn. A question that
// names more prefixes than this is a list, and a list decides nothing here.
const maxWorkItemKeyPrefixReads = 8

// WorkItemKeyPrefixOutcome is one prefix read. GrantApplied mirrors
// CensusOutcome.GrantApplied: under a bound grant, a read that does not report it
// decides nothing.
type WorkItemKeyPrefixOutcome struct {
	Seen         bool
	GrantApplied bool
}

// WorkItemKeyPrefixFunc reports whether the caller can read a work item whose key
// starts with prefix (which ends in "-"), under the grant bound on ctx.
type WorkItemKeyPrefixFunc func(ctx context.Context, orgID, prefix string) (WorkItemKeyPrefixOutcome, error)

// bindQuestionHandles is the handles a turn reads. A pull request and a CI run
// bind on their text. A work item key's shape proves nothing: UTF-8, GPT-5 and
// CVE-2026 have a ticket key's shape, and any URL with a path and a fragment has a
// repository reference's. So a key counts only when the caller can read a work
// item whose key starts with the same prefix: the ticket key's prefix with its
// "-", or the reference's repository path with its "#" or "!" (CHAOS-12463). Then
// OPS-99999 is a named ticket the census can prove absent, and UTF-8 is a word,
// and neither takes the decisive outcome from a pull request the same question
// names.
//
// The read runs under the same grant as the census, so what it shows a restricted
// caller is inside its grant: a prefix that exists only outside it reads as a
// word. A turn reads at most maxWorkItemKeyPrefixReads prefixes, failures
// included, and a read that cannot run is counted once per prefix. If any key is
// left unread, the turn cannot tell one handle from several: the unread key may
// name a second subject, which would refuse a decisive outcome as multi_handle.
// So such a turn binds no handle at all, and answers by search.
func bindQuestionHandles(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps) []BoundHandle {
	bound := BindHandles(request.Question)
	scoped := contextfabric.ScopeCensusToCaller(ctx, principal)
	const (
		prefixHeld = iota + 1
		prefixNotHeld
		prefixUnread
	)
	prefixes := map[string]int{}
	event := ResolutionTraceEvent{RequestID: request.RequestID, Stage: "work_item_key_gate"}
	kept := make([]BoundHandle, 0, len(bound))
	for _, handle := range bound {
		prefix, isKey := workItemKeyPrefix(handle)
		if !isKey {
			kept = append(kept, handle)
			continue
		}
		event.WorkItemKeyTokens++
		state, read := prefixes[prefix]
		if !read {
			state = prefixNotHeld
			switch seen, ok := readWorkItemKeyPrefix(scoped, principal, prefix, deps, len(prefixes)); {
			case !ok:
				state = prefixUnread
			case seen:
				state = prefixHeld
			}
			prefixes[prefix] = state
		}
		switch state {
		case prefixHeld:
			event.WorkItemKeysHeld++
			kept = append(kept, handle)
		case prefixUnread:
			event.WorkItemKeysUnread++
		}
	}
	if event.WorkItemKeyTokens > 0 && deps.ResolutionTracer != nil {
		deps.ResolutionTracer.Trace(event)
	}
	if event.WorkItemKeysUnread > 0 {
		return nil
	}
	return kept
}

// workItemKeyPrefix is the prefix a work item key shares with its tracker's other
// keys, or false for a handle of another grammar.
func workItemKeyPrefix(handle BoundHandle) (string, bool) {
	switch handle.Grammar {
	case workItemTicketKeyGrammar:
		return handle.Value[:strings.LastIndexByte(handle.Value, '-')+1], true
	case workItemRepoReferenceGrammar:
		return handle.Value[:strings.LastIndexAny(handle.Value, "#!")+1], true
	}
	return "", false
}

func readWorkItemKeyPrefix(ctx context.Context, principal storage.Principal, prefix string, deps ResolveDeps, reads int) (seen, ok bool) {
	if deps.WorkItemKeyPrefix == nil || reads >= maxWorkItemKeyPrefixReads {
		return false, false
	}
	outcome, err := deps.WorkItemKeyPrefix(ctx, principal.OrgID, prefix)
	if err != nil {
		return false, false
	}
	if _, bound := contextfabric.CensusGrantFrom(ctx); bound && !outcome.GrantApplied {
		return false, false
	}
	return outcome.Seen, true
}
