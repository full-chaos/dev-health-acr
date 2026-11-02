package graphrank

import (
	"context"
	"strconv"
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
// included, and a read that cannot run is counted once per prefix. Every resolver
// call of a turn reads the turn's memo (contextfabric.WithWorkItemKeyPrefixMemo),
// so a prefix is read once per turn, however often the turn resolves. If any key is
// left unread, the turn cannot tell one handle from several: the unread key may
// name a second subject, which would refuse a decisive outcome as multi_handle.
// So such a turn binds no handle at all, and answers by search.
//
// A repository reference with "#" is first read as a GitHub pull request
// (CHAOS-12642): GitHub issues and pull requests share one number space, so the
// text cannot tell which owner/repo#N names. A pull request row with that
// repository and number makes the handle a pull_request_repo_reference; any
// other reading leaves the reference to the prefix read below, as before.
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
	rows := ResolutionTraceEvent{RequestID: request.RequestID, Stage: "repository_reference_row"}
	referencesRead := false
	referenceReads := 0
	kept := make([]BoundHandle, 0, len(bound))
	for _, handle := range bound {
		if isPullRequestReferenceShape(handle) {
			rows.RepositoryReferences++
			row, fresh := readRepositoryReferenceRow(scoped, principal, handle.Value, deps, &referenceReads)
			referencesRead = referencesRead || fresh
			switch row {
			case repositoryReferencePullRequestRow:
				rows.RepositoryReferencePullRequestRows++
				kept = append(kept, AsPullRequestReference(handle))
				continue
			case repositoryReferenceWorkItemRow:
				rows.RepositoryReferenceWorkItemRows++
			case repositoryReferenceNotFound:
				rows.RepositoryReferencesNotFound++
			default:
				rows.RepositoryReferencesUnread++
			}
		}
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
	if referencesRead && deps.ResolutionTracer != nil {
		deps.ResolutionTracer.Trace(rows)
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
	// One turn resolves subjects more than once; the turn's memo keeps each read,
	// a failed one included, so a prefix is read once per turn (CHAOS-12463).
	grant, bound := contextfabric.CensusGrantFrom(ctx)
	key := principal.OrgID + "\x00" + strconv.FormatBool(bound) + "\x00" + strings.Join(grant, "\x01") + "\x00" + prefix
	if read, found := contextfabric.WorkItemKeyPrefixReadFrom(ctx, key); found {
		return read.Seen, read.Read
	}
	if deps.WorkItemKeyPrefix == nil || reads >= maxWorkItemKeyPrefixReads {
		return false, false
	}
	outcome, err := deps.WorkItemKeyPrefix(ctx, principal.OrgID, prefix)
	read := contextfabric.WorkItemKeyPrefixRead{Seen: outcome.Seen, Read: err == nil && (!bound || outcome.GrantApplied)}
	if !read.Read {
		read.Seen = false
	}
	contextfabric.RecordWorkItemKeyPrefixRead(ctx, key, read)
	return read.Seen, read.Read
}

// The stored row a repository reference with "#" names (CHAOS-12642).
const (
	repositoryReferenceUnread = iota
	repositoryReferencePullRequestRow
	repositoryReferenceWorkItemRow
	repositoryReferenceNotFound
)

// isPullRequestReferenceShape reports a repository reference that a GitHub pull
// request can take: GitLab writes "!" for a merge request, so a "!" reference
// keeps its reading.
func isPullRequestReferenceShape(handle BoundHandle) bool {
	return handle.Grammar == workItemRepoReferenceGrammar && strings.Contains(handle.Value, "#")
}

// IsPullRequestReferenceShape reports a handle the stored rows may show to be a
// GitHub pull request (CHAOS-12642). find_subjects handle mode reads it the same
// way a turn does.
func IsPullRequestReferenceShape(handle BoundHandle) bool { return isPullRequestReferenceShape(handle) }

// AsPullRequestReference is the pull_request_repo_reference handle for a
// reference the stored rows showed to be a pull request: same value and span.
func AsPullRequestReference(handle BoundHandle) BoundHandle {
	handle.Kind, handle.Grammar = contextfabric.SubjectPullRequest, pullRequestRepoReferenceGrammar
	return handle
}

// readRepositoryReferenceRow decides what owner/repo#N names, by stored rows and
// in this order (lead ruling, CHAOS-12642): a GitHub pull request row with that
// repository and number, else a work item with that key, else nothing. On GitHub
// issues and pull requests share one number space, so at most one of the first
// two exists, and nothing is guessed. Both reads are the census, under the
// caller's grant, and the turn's memo keeps each, so a reference is read once per
// turn. A read that cannot run, or does not close, decides nothing: the reference
// keeps its work item reading. reads counts the census reads the call has made:
// they stay inside maxWorkItemKeyPrefixReads, like the prefix reads.
func readRepositoryReferenceRow(ctx context.Context, principal storage.Principal, reference string, deps ResolveDeps, reads *int) (row int, fresh bool) {
	pullRequest, ok, fresh := readReferenceCensus(ctx, principal, contextfabric.SubjectPullRequest, reference, deps, reads)
	switch {
	case !ok:
		return repositoryReferenceUnread, fresh
	case pullRequest:
		return repositoryReferencePullRequestRow, fresh
	}
	workItem, ok, workItemFresh := readReferenceCensus(ctx, principal, contextfabric.SubjectWorkItem, reference, deps, reads)
	fresh = fresh || workItemFresh
	switch {
	case !ok:
		return repositoryReferenceUnread, fresh
	case workItem:
		return repositoryReferenceWorkItemRow, fresh
	}
	return repositoryReferenceNotFound, fresh
}

// readReferenceCensus is one census of the reference as kind. fresh is false
// when the turn's memo already held the read. A read that cannot run (no census
// wired, or the read budget spent) is kept as unread too, so the turn reports it
// once.
func readReferenceCensus(ctx context.Context, principal storage.Principal, kind CensusKind, reference string, deps ResolveDeps, reads *int) (seen, ok, fresh bool) {
	grant, bound := contextfabric.CensusGrantFrom(ctx)
	key := "repository_reference\x00" + string(kind) + "\x00" + principal.OrgID + "\x00" + strconv.FormatBool(bound) + "\x00" + strings.Join(grant, "\x01") + "\x00" + reference
	if read, found := contextfabric.WorkItemKeyPrefixReadFrom(ctx, key); found {
		return read.Seen, read.Read, false
	}
	var read contextfabric.WorkItemKeyPrefixRead
	if deps.CensusFunc != nil && *reads < maxWorkItemKeyPrefixReads {
		*reads++
		outcome, err := censusOnce(ctx, grantEnforced(deps.CensusFunc), principal.OrgID, kind, reference)
		read = contextfabric.WorkItemKeyPrefixRead{Seen: outcome.Count > 0, Read: err == nil && !outcome.ClosureMismatch && !outcome.SatisfierSetClosureMismatch}
	}
	if !read.Read {
		read.Seen = false
	}
	contextfabric.RecordWorkItemKeyPrefixRead(ctx, key, read)
	return read.Seen, read.Read, true
}

// censusOnce runs one handle census with no anchor, and turns a panic into an
// error like namedHandleCensus does.
func censusOnce(ctx context.Context, census CensusFunc, orgID string, kind CensusKind, value string) (outcome CensusOutcome, err error) {
	defer func() {
		if recover() != nil {
			outcome, err = CensusOutcome{}, errCensusPanicked
		}
	}()
	return census(ctx, orgID, kind, value, true, "", "", false)
}
