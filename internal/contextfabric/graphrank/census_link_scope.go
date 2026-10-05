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
// therefore the handle's satisfiers that the repository-to-issue walk reaches
// (ResolveDeps.LinkScopedWorkItems, the walk that serves a repository's work
// items), and its cross-check and its commit test the issue with
// AuthorizedThroughLink.

var (
	errLinkScopeUnavailable = errors.New("graphrank: the link walk of a requested repository scope is not available")
	errLinkScopeCut         = errors.New("graphrank: the link walk of a requested repository scope was cut")
	errLinkScopeUnlisted    = errors.New("graphrank: the work item census did not list its satisfiers")
)

// linkScope is the work-item population of one requested repository scope,
// read once per round and shared by the census and its cross-check.
type linkScope struct {
	read     func(ctx context.Context) ([]string, bool, error)
	once     sync.Once
	members  map[string]bool
	complete bool
	err      error
}

// newLinkScope returns the population of the request's repository scope, or
// nil when there is no scope or no walk to read it with.
func newLinkScope(deps ResolveDeps, scope contextfabric.RequestedScope) *linkScope {
	if deps.LinkScopedWorkItems == nil || len(scope.RepositorySlugs) == 0 {
		return nil
	}
	return &linkScope{read: func(ctx context.Context) ([]string, bool, error) {
		return deps.LinkScopedWorkItems(ctx, scope)
	}}
}

// population is the scope's whole work-item population. A cut walk is an
// error: a census inside a partial population is not a census.
func (l *linkScope) population(ctx context.Context) (map[string]bool, error) {
	if l == nil {
		return nil, errLinkScopeUnavailable
	}
	l.once.Do(func() {
		ids, complete, err := l.read(ctx)
		l.complete, l.err = complete, err
		l.members = make(map[string]bool, len(ids))
		for _, id := range ids {
			l.members[id] = true
		}
	})
	switch {
	case l.err != nil:
		return nil, l.err
	case !l.complete:
		return nil, errLinkScopeCut
	}
	return l.members, nil
}

// withinLinkScope keeps, of a work-item census read over the whole
// organization, the satisfiers inside the scope's population: the census of
// the handle under the requested repository scope. The result carries
// RepositoryFilterApplied, so the round cross-checks it as any filtered census.
// A census that did not list its satisfiers cannot be scoped and is an error.
func withinLinkScope(ctx context.Context, scope *linkScope, outcome CensusOutcome) (CensusOutcome, error) {
	members, err := scope.population(ctx)
	if err != nil {
		return CensusOutcome{}, err
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
	var kept []string
	for _, id := range ids {
		if members[id] {
			kept = append(kept, id)
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
// the link walk: the satisfier it names is then in the requested repository
// scope through its link, and is tested with AuthorizedThroughLink.
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
