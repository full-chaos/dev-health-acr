package falkorgraph

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12329: a requested project narrowing is SCOPE, not authorization. The
// walk's admission (treeWalkState.admitted, through graphrank's node rule)
// refuses a node outside the requested projects together with a node the
// caller's grants hide, and the link read used to count both as denied: the
// answer then said that work items were "outside this principal's authorized
// scope", and an all-excluded answer was served degraded, for a caller allowed
// to see everything. The link read now asks the same question without the
// narrowing: a row that passes then is out of the requested scope, never a
// denial (not in linkDeniedTargets, so no authorization-gap disclosure and no
// floor from it). What the grants hide is still denied.

// withoutProjectNarrowing is the walk state with the request's project
// narrowing removed: what the caller's grants and the rest of the request
// decide. It records nothing: the unverified-narrowing flag is set only under
// a requested narrowing (graphrank.ProjectNarrowingUnproven).
func (s treeWalkState) withoutProjectNarrowing() treeWalkState {
	s.scope.ProjectIDs, s.scope.ProjectNarrowing = nil, nil
	// CHAOS-12361: an anchor the narrowing alone refuses is not denied here.
	s.anchorDenied = s.anchorDeniedUnscoped
	return s
}

// narrowsByProject reports whether the request narrows by project at all.
func (s treeWalkState) narrowsByProject() bool {
	return len(s.scope.ProjectIDs) > 0
}

// CHAOS-12361: a cohort member walk (teamAnchorMembers) decides its anchor
// once, with the request's project narrowing. An anchor the narrowing alone
// refuses (its own project list is outside the requested projects) is out of
// scope, not hidden: every member under it is then out of scope too, never
// denied. teamAnchorMembers carries the anchor's decision without the
// narrowing to the walk on ctx, so the walk's signature stays as it is.
type anchorUnscopedDeniedKey struct{}

// withAnchorUnscopedDenied records, for the walk ctx starts, whether the
// anchor is denied even without the request's project narrowing.
func withAnchorUnscopedDenied(ctx context.Context, denied bool) context.Context {
	return context.WithValue(ctx, anchorUnscopedDeniedKey{}, denied)
}

// anchorUnscopedDenied is the recorded decision, or fallback when none was
// recorded: then the anchor's decision is taken as is (no scope reading).
func anchorUnscopedDenied(ctx context.Context, fallback bool) bool {
	if denied, ok := ctx.Value(anchorUnscopedDeniedKey{}).(bool); ok {
		return denied
	}
	return fallback
}

// scopeOnly reports whether a hit the walk refuses is refused only by the
// request's project narrowing: the caller's grants and the rest of the
// request admit it.
func (s treeWalkState) scopeOnly(hop treeHop, h walkHit) bool {
	return s.narrowsByProject() && s.withoutProjectNarrowing().authorizedHit(hop, h)
}

// excludeByScope records a node the requested project narrowing left out of
// a cohort member walk: counted apart, never denied.
func (s treeWalkState) excludeByScope(n *node) {
	if s.out.hopProjectExcluded == nil {
		s.out.hopProjectExcluded = map[string]bool{}
	}
	s.out.hopProjectExcluded[canonicalIDOf(n)] = true
}

// anchorOutOfProjectScope reports whether an anchor is refused only by the
// request's project narrowing: its own project list is outside the requested
// projects, while the caller's grants and the rest of the request admit it.
func anchorOutOfProjectScope(principal storage.Principal, scope contextfabric.RequestedScope, attributes map[string]interface{}) bool {
	if len(scope.ProjectIDs) == 0 || authorizedNodeAttributes(principal, scope, attributes) {
		return false
	}
	unscoped := scope
	unscoped.ProjectIDs, unscoped.ProjectNarrowing = nil, nil
	return authorizedNodeAttributes(principal, unscoped, attributes)
}
