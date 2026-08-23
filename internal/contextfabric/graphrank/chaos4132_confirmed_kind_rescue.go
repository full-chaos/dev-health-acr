package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4132: a receipt-confirmed kind (contextfabric.ConfirmedExpectedKind,
// CHAOS-3900 P1.D) skips the CHAOS-4038 coverage floor entirely (resolve.go's
// own `if confirmedKind == nil` guard) on the assumption that ordinary
// Search/SearchQuestion/AliasLookup already has candidates of the confirmed
// kind in the pool -- true in general (P1.D's own "nothing left to
// disambiguate" reasoning), false precisely for a kind whose ONLY route into
// a PRIOR turn's pool was the coverage floor itself. Confirming exactly that
// kind, one turn later, then finds filterCandidatesByConfirmedKind
// (chaos3900_structure_offers.go) narrowing an ordinary-search-only pool
// that never had the kind at all down to nothing -- a GUARANTEED no_match
// (unresolved.go's resolveTerminalStatus returns InvestigationNoMatch
// unconditionally for an empty candidate pool, before its own
// AllowClarification branch is ever reached), regardless of how correct the
// confirmed answer actually is. Diagnosed against the CHAOS-3742 two-turn
// trial corpus's index 57 (positive arm, member=expected_kind): recurred
// across every run since before CHAOS-4108/4117/4118, invisible until
// CHAOS-4120's own false_no_match gate widening caught it.
//
// applyConfirmedKindRescue is the fix: when filterCandidatesByConfirmedKind
// leaves the pool empty, run a small, bounded, kind-scoped supplemental
// retrieval pass -- up to kindCoverageMaxTermsPerKind SearchKind calls,
// stopping at the FIRST term that satisfies the confirmed kind (poolHasKind,
// the SAME "one candidate is enough, stop spending" early exit
// applyKindCoverageFloor already uses) -- scoped to JUST the confirmed kind
// (reusing the SAME deps.SearchKind primitive and the SAME
// kindCoverageQueryLimit/kindCoverageMaxTermsPerKind bounds
// applyKindCoverageFloor already uses, chaos4038_kind_coverage.go) before
// conceding. In the common favorable case (the first term already
// satisfies the kind) this is exactly one call; it is never more than
// kindCoverageMaxTermsPerKind. Deliberately never invoked when the pool
// already has candidates of the confirmed kind -- this closes the
// coverage-floor-only-route gap without reopening the "spend extra
// kind-scoped calls when there is nothing left to disambiguate" waste
// CHAOS-3900 P1.D's own optimization exists to avoid; see resolve.go's own
// call site for the exact gating condition and
// TestResolveSubjects_ConfirmedKindRescueSkippedWhenPoolAlreadySatisfied
// for the negative-control pin (zero SearchKind calls when unneeded).
//
// nil deps.SearchKind is a no-op, matching applyKindCoverageFloor's own
// convention -- a backend that does not implement kind-scoped search cannot
// be rescued, and this returns cleanly rather than erroring.
//
// A genuine SearchKind failure aborts and propagates as an error, exactly
// like every other retrieval pass in this file.
//
// truncated/degraded ARE folded into the commit gate's own
// searchTruncated/retrievalDegraded inputs by resolve.go's call site --
// codex review round 1 (MEDIUM, confirmed) -- UNLIKE applyKindCoverageFloor's
// own coverageTruncated/coverageFloorDegraded, which stay purely
// observational because the floor only ever adds ONE candidate among many
// OTHER kinds' candidates an unrelated commit does not depend on. This
// rescue is different: when it fires, candidatesBySubject was EMPTY before
// it ran, so whatever it finds becomes the ENTIRE population the gate
// decides over. A truncated rescue call may have cut off a genuine rival
// candidate of the SAME confirmed kind -- exactly the risk searchTruncated
// exists to gate on -- so treating this rescue's own truncation as
// gate-blind noise would let an incomplete read masquerade as a confident
// commit. See resolve.go's own call site comment and
// TestResolveSubjects_ConfirmedKindRescueTruncationBlocksALoneCandidateCommit.
func applyConfirmedKindRescue(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, pool map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, identity identityClaimants, identityTerms identityMatchTerms, kind contextfabric.SubjectKind) (added []contextfabric.SubjectCandidate, traversalDegraded int, authzDropped int, truncated bool, degraded bool, err error) {
	if deps.SearchKind == nil {
		return nil, 0, 0, false, false, nil
	}
	boundedTerms := terms
	if len(boundedTerms) > kindCoverageMaxTermsPerKind {
		boundedTerms = boundedTerms[:kindCoverageMaxTermsPerKind]
	}
	for _, term := range boundedTerms {
		results, kindTruncated, kindDegraded, searchErr := deps.SearchKind(ctx, term, kind, kindCoverageQueryLimit)
		if searchErr != nil {
			return added, traversalDegraded, authzDropped, truncated, degraded, searchErr
		}
		if kindTruncated {
			truncated = true
		}
		if kindDegraded {
			degraded = true
		}
		// allowExactMatch=true / vectorArmSimilarity=nil: the SAME rationale
		// applyKindCoverageFloor's own call site gives -- terms here are the
		// SAME genuine caller-derived subject terms every ordinary pass
		// already used, and this is a lexical coverage rescue, never a
		// vector-arm competitor.
		termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, results, pool, observationParentKey, observationBlocked, true, nil, identity, identityTerms)
		traversalDegraded += termTraversalDegraded
		authzDropped += termAuthzDropped
		if poolHasKind(pool, kind) {
			break
		}
	}
	added = candidatesOfKind(pool, kind)
	return added, traversalDegraded, authzDropped, truncated, degraded, nil
}
