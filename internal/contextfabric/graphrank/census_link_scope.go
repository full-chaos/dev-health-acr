package graphrank

import (
	"context"
	"errors"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// A work item is in a requested repository scope when it is linked to a pull
// request of a repository the scope names: the entity tree (Repository <> Pull
// request <> Issue <> Project) relates an issue to a repository only through
// its linked pull requests, never through the issue's own repository column.
// The census of a work-item handle under a requested repository scope is
// therefore the handle's satisfiers that link into the scope
// (ResolveDeps.LinkScopedWorkItemsOf, a read keyed on the satisfiers by the
// rule of the walk that serves a repository's work items, CHAOS-12505), and
// its cross-check and its commit test the issue with AuthorizedThroughLink.

var (
	errLinkScopeUnavailable = errors.New("graphrank: the keyed link read of a requested repository scope is not available")
	errLinkScopeCut         = errors.New("graphrank: the keyed link read of a requested repository scope was cut")
	errLinkScopeUnlisted    = errors.New("graphrank: the work item census did not list its satisfiers")
)

// linkScope is the keyed link read of one requested repository scope, shared by
// the census and its cross-check within one round. What it decided for an id is
// kept, so the cross-check of the census's own ids reads nothing new. A failed
// or cut read fails every later call of the round: a census inside a partial
// answer is not a census.
type linkScope struct {
	read    func(ctx context.Context, ids []string) (map[string]string, bool, error)
	mu      sync.Mutex
	decided map[string]string
	err     error
}

// newLinkScope returns the keyed link read of the request's repository scope,
// or nil when there is no scope or no read to answer it with.
func newLinkScope(deps ResolveDeps, scope contextfabric.RequestedScope) *linkScope {
	if deps.LinkScopedWorkItemsOf == nil || len(scope.RepositorySlugs) == 0 {
		return nil
	}
	return &linkScope{decided: map[string]string{}, read: func(ctx context.Context, ids []string) (map[string]string, bool, error) {
		return deps.LinkScopedWorkItemsOf(ctx, scope, ids)
	}}
}

// linked returns, of ids, the ones in the scope, each with the tier of its
// strongest admitted link. Only the ids not decided before are read.
func (l *linkScope) linked(ctx context.Context, ids []string) (map[string]string, error) {
	if l == nil {
		return nil, errLinkScopeUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	var unread []string
	seen := map[string]bool{}
	for _, id := range ids {
		if _, done := l.decided[id]; !done && !seen[id] {
			seen[id] = true
			unread = append(unread, id)
		}
	}
	if len(unread) > 0 {
		tiers, complete, err := l.read(ctx, unread)
		switch {
		case err != nil:
			l.err = err
			return nil, err
		case !complete:
			l.err = errLinkScopeCut
			return nil, l.err
		}
		for _, id := range unread {
			l.decided[id] = tiers[id]
		}
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if tier := l.decided[id]; tier != "" {
			out[id] = tier
		}
	}
	return out, nil
}

// withinLinkScope keeps, of a work-item census read over the whole
// organization, the satisfiers that link into the scope: the census of the
// handle under the requested repository scope. The result carries
// RepositoryFilterApplied, so the round cross-checks it as any filtered census.
// A census that did not list its satisfiers cannot be scoped and is an error.
// Each kept satisfier is recorded on the call's context with its link tier,
// so the fact read and the disclosures of this call (and of no other) know
// the read admitted it.
func withinLinkScope(ctx context.Context, scope *linkScope, outcome CensusOutcome) (CensusOutcome, error) {
	if scope == nil {
		return CensusOutcome{}, errLinkScopeUnavailable
	}
	if outcome.ClosureMismatch {
		return outcome, nil
	}
	var ids []string
	switch {
	case outcome.Count == 0:
	case outcome.Count == 1:
		if outcome.SatisfierCanonicalID == "" {
			return CensusOutcome{}, errLinkScopeUnlisted
		}
		ids = []string{outcome.SatisfierCanonicalID}
	default:
		if outcome.SatisfierSetClosureMismatch || len(outcome.SatisfierCanonicalIDs) != outcome.Count {
			return CensusOutcome{}, errLinkScopeUnlisted
		}
		ids = outcome.SatisfierCanonicalIDs
	}
	tiers, err := scope.linked(ctx, ids)
	if err != nil {
		return CensusOutcome{}, err
	}
	var kept []string
	for _, id := range ids {
		if tier, ok := tiers[id]; ok {
			kept = append(kept, id)
			contextfabric.RecordWorkItemCensusLinkedSatisfier(ctx, id, tier)
		}
	}
	outcome.Count, outcome.SatisfierCanonicalID, outcome.SatisfierCanonicalIDs = len(kept), "", nil
	switch {
	case len(kept) == 1:
		outcome.SatisfierCanonicalID = kept[0]
	case len(kept) > 1:
		outcome.SatisfierCanonicalIDs = kept
	}
	outcome.RepositoryFilterApplied = true
	return outcome, nil
}

// linkScopedSatisfier reports whether a round scoped its work-item census by
// the keyed link read: the satisfier it names is then in the requested
// repository scope through its link, and is tested with AuthorizedThroughLink.
func linkScopedSatisfier(attestation Attestation, kind contextfabric.SubjectKind) bool {
	if kind != contextfabric.SubjectWorkItem {
		return false
	}
	for _, ka := range attestation.Kinds {
		if ka.Kind == kind && ka.RepositoryFilterApplied {
			return true
		}
	}
	return false
}

// linkScopeFailure names why a work item census could not be scoped by the
// keyed link read, as a closed value for the log line.
func linkScopeFailure(err error) string {
	switch {
	case errors.Is(err, errLinkScopeUnavailable):
		return "link_read_unavailable"
	case errors.Is(err, errLinkScopeCut):
		return "link_read_cut"
	case errors.Is(err, errLinkScopeUnlisted):
		return "satisfiers_unlisted"
	}
	return "link_read_failed"
}
