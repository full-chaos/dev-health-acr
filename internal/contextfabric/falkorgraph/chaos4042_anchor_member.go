package falkorgraph

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AnchorMember is CHAOS-4042 PR3's own (sol-max ruling) production
// implementation of graphrank.GraphAnchorMemberFunc: a pinned-epoch,
// single-node read plus re-authorization for one redeemed anchor receipt's
// selected (kind, canonicalID).
//
// This is deliberately composition, not new query machinery: binding.
// GraphKey is ALREADY the pinned epoch's own distinct graph namespace
// (graphKeyForEpoch's build-aside-and-swap design, CHAOS-3898) -- a read
// via effectiveKey(ctx, orgID, binding) is epoch-addressed BY CONSTRUCTION,
// so no separate epoch-comparison step exists or is needed. The node fetch
// (nodeByKindID) and the authorization check (graphrank.AuthorizedAttributes)
// are the SAME primitives resolveEdge (queries.go) already composes for an
// edge's own two endpoints.
//
// ErrNotFound on the WHOLE graph key (the epoch was already retired -- see
// GraphAnchorMemberResult.Unverifiable's own doc comment) is deliberately
// NOT folded into "node not found" the way reader.go's identity-universe
// existence check folds a never-bootstrapped graph into "claimant missing"
// -- that convention is safe there because a never-bootstrapped graph and
// an empty graph are observationally identical for THAT read's own
// completeness question. Here they are not: a retired epoch says nothing
// about whether the claimant exists in the CURRENT epoch, so collapsing it
// into "not found" would let a stale binding manufacture a false
// claim-lost verdict, which the ruling forbids.
func (a *Adapter) AnchorMember(ctx context.Context, principal storage.Principal, scope contextfabric.RequestedScope, binding contextfabric.ResolvedGraphBinding, kind contextfabric.SubjectKind, canonicalID string) (graphrank.GraphAnchorMemberResult, error) {
	key, err := a.effectiveKey(ctx, principal.OrgID, binding)
	if err != nil {
		return graphrank.GraphAnchorMemberResult{}, err
	}
	n, err := a.nodeByKindID(ctx, key, principal.OrgID, string(kind), canonicalID, temporalFilter{})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return graphrank.GraphAnchorMemberResult{Unverifiable: true}, nil
		}
		return graphrank.GraphAnchorMemberResult{}, err
	}
	if n == nil {
		return graphrank.GraphAnchorMemberResult{Exists: false}, nil
	}
	candidate := toCandidateNode(n)
	return graphrank.GraphAnchorMemberResult{
		Exists:     true,
		Authorized: graphrank.AuthorizedAttributes(principal, scope, candidate.Attributes),
	}, nil
}

var _ graphrank.GraphAnchorMemberFunc = (*Adapter)(nil).AnchorMember
