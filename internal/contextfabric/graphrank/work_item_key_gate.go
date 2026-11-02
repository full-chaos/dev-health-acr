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

// bindQuestionHandles is the handles a turn reads. A pull request, a CI run and a
// repository reference bind on their text. A ticket key's shape proves nothing:
// UTF-8, GPT-5 and CVE-2026 have it. So a key counts only when the caller can
// read a work item of that prefix (CHAOS-12463). Then OPS-99999 is a named ticket
// the census can prove absent, and UTF-8 is a word, and neither takes the decisive
// outcome from a pull request the same question names.
//
// The read runs under the same grant as the census, so what it shows a restricted
// caller is inside its grant: a prefix that exists only outside it reads as a
// word. A read that cannot run leaves the token unbound and is counted.
func bindQuestionHandles(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps) []BoundHandle {
	bound := BindHandles(request.Question)
	scoped := contextfabric.ScopeCensusToCaller(ctx, principal)
	prefixes := map[string]bool{}
	event := ResolutionTraceEvent{RequestID: request.RequestID, Stage: "work_item_key_gate"}
	kept := make([]BoundHandle, 0, len(bound))
	for _, handle := range bound {
		if handle.Grammar != workItemTicketKeyGrammar {
			kept = append(kept, handle)
			continue
		}
		event.WorkItemKeyTokens++
		prefix := handle.Value[:strings.LastIndexByte(handle.Value, '-')+1]
		seen, read := prefixes[prefix]
		if !read {
			var ok bool
			seen, ok = readWorkItemKeyPrefix(scoped, principal, prefix, deps, len(prefixes))
			if !ok {
				event.WorkItemKeysUnread++
				continue
			}
			prefixes[prefix] = seen
		}
		if seen {
			event.WorkItemKeysHeld++
			kept = append(kept, handle)
		}
	}
	if event.WorkItemKeyTokens > 0 && deps.ResolutionTracer != nil {
		deps.ResolutionTracer.Trace(event)
	}
	return kept
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
