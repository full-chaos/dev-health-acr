package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4417: repository (and project/team) candidates lost to the shared,
// cross-kind MaxSubjectCandidates cut BEFORE any receipt has confirmed a
// kind. See .remember/context-fabric/drafts/repo-subject-diagnosis-2026-08-28.md
// (repo root) for the executed repro this fixes: an org with 11
// `repository` nodes vs 37,001 `ci_pipeline_run` (+ thousands of
// pull_request/pull_request_review/work_item) nodes ties every lexical
// repository match at base_confidence=0.5 (resolve.go corroboration), an
// unscoped Search call truncates at request.Options.MaxSubjectCandidates
// (=20), and resolution.go's commit-gate switch's `case searchTruncated:
// ambiguous = true` (~line 724) fires before LoneFloor/TopFloor ever
// evaluate the correct repository candidate -- this is turn 1, no
// confirmedKind exists yet, so CHAOS-4132/CHAOS-4154's confirmed-kind
// machinery never engages. Same family CHAOS-4183 Shape A / CHAOS-4234
// fixed for the offer-boundary layer generally, never evaluated for a kind
// whose population skew (11:37001) is far worse than either ticket
// measured.
//
// This is CHAOS-4154's own completeness mechanism
// (buildConfirmedKindScopedSnapshot, chaos4154_confirmed_kind_scope.go --
// deliberately REUSED, not reimplemented: that function is already
// kind-parameterized and carries its own hard-won completeness contract,
// including the DO-NOT-BUILD list in its doc comment) run
// PRE-CONFIRMATION, for a small, fixed set of kinds whose population in
// this org's graph is small enough for an exhaustive kind-scoped
// SearchKind pass to be cheap and completeness-provable.
//
// chaos4417LowPopulationScopedKinds mirrors aliasLookupScopedKinds
// (repository/project/team, subject.go) -- the SAME three kinds CHAOS-4154
// already trusts buildConfirmedKindScopedSnapshot's completeness contract
// for (its own identity-census-enumerable set). work_item/ci_pipeline_run/
// pull_request/pull_request_review stay OUT deliberately: their
// populations are exactly what searchTruncated exists to protect against,
// and this ticket's own repro (11 repository vs 37,001 ci_pipeline_run
// nodes) is the textbook case for why a bounded per-kind SearchKind pass
// over one of THOSE kinds would not be cheap or safely exhaustive.
// Derived from the SAME map applyKindCoverageFloor/isAliasLookupScopedKind
// already share (never a second, independently maintained kind list), via
// the SAME sortedKinds helper kindCoverageOrder already uses, so iteration
// order is deterministic without a third hand-maintained ordering.
var chaos4417LowPopulationScopedKinds = sortedKinds(aliasLookupScopedKinds)

// applyLowPopulationKindScopedRescue runs buildConfirmedKindScopedSnapshot
// once per chaos4417LowPopulationScopedKinds member and re-evaluates the
// commit gate over whichever snapshots come back confirmedKindScopeComplete.
//
// Unlike CHAOS-4154's confirmed-kind call site, this caller does not get to
// assume there is only one candidate kind in play -- nothing has confirmed
// which kind (if any) the question meant, so it tries every member of the
// set. It commits ONLY when EXACTLY ONE kind's isolated, proven-complete
// population produces a commit (ok=true). More than one kind independently
// committing is treated as cross-kind ambiguity and is NOT resolved here:
// the zero-tolerance wrong-commit bar (CHAOS-4085/CHAOS-4149) means
// silently preferring one kind over another with no ranking signal between
// them would be worse than leaving the ordinary ambiguous/clarify outcome
// standing -- see TestApplyLowPopulationKindScopedRescue_MultipleKindsCommitStaysAmbiguous.
//
// The caller (resolve.go) applies the SAME "resolution stays the first
// pass's own unless this returns ok" discipline CHAOS-4154's own call site
// already established: nothing here ever mutates the caller's own
// candidatesBySubject/resolution, only returns a fresh replacement when it
// actually has one.
//
// A genuine buildConfirmedKindScopedSnapshot error aborts and propagates,
// exactly like every other retrieval pass in this package.
func applyLowPopulationKindScopedRescue(
	ctx context.Context,
	principal storage.Principal,
	request contextfabric.InvestigationRequest,
	deps ResolveDeps,
	terms []string,
	aliasClaimantsByTerm map[string][]CandidateNode,
	aliasIdentityComplete bool,
	effectiveSearchLimit int,
	unscopedVisibility bool,
	gate CommitGatePolicy,
	gateValid bool,
	retrievalDegraded bool,
	coverageFloorDegraded bool,
) (resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, ok bool, err error) {
	type scopedCommit struct {
		resolution contextfabric.SubjectResolution
		bases      contextfabric.CommitBasisSet
		digests    contextfabric.CommitDecisionDigestSet
		tracer     *discardableDecisionTracer
	}
	var commits []scopedCommit
	for _, kind := range chaos4417LowPopulationScopedKinds {
		scopedPool, scopedObservationParentKey, scopedObservationBlocked, scopedIdentity, scopedIdentityTerms, scopeState, scopeTraversalDegraded, scopeAuthzDropped, _, scopeErr :=
			buildConfirmedKindScopedSnapshot(ctx, principal, request, deps, terms, aliasClaimantsByTerm, aliasIdentityComplete, kind, effectiveSearchLimit)
		if scopeErr != nil {
			return contextfabric.SubjectResolution{}, nil, nil, false, scopeErr
		}
		if scopeTraversalDegraded > 0 && deps.TraversalDegraded != nil {
			deps.TraversalDegraded(ctx, principal.OrgID, scopeTraversalDegraded)
		}
		if scopeAuthzDropped > 0 && deps.SubjectCandidatesAuthzDropped != nil {
			deps.SubjectCandidatesAuthzDropped(ctx, principal.OrgID, scopeAuthzDropped)
		}
		if deps.ResolutionTracer != nil {
			candidateCount := 0
			if scopeState == confirmedKindScopeComplete {
				candidateCount = len(scopedPool)
			}
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID:                            request.RequestID,
				Stage:                                "low_population_kind_scope",
				LowPopulationKindScopeKind:           string(kind),
				LowPopulationKindScopeState:          scopeState,
				LowPopulationKindScopeCandidateCount: candidateCount,
			})
		}
		if scopeState != confirmedKindScopeComplete || !gateValid {
			continue
		}
		scopedTracer := &discardableDecisionTracer{real: deps.ResolutionTracer}
		scopedResolution, scopedBases, scopedDigests := ResolveFromMergedCandidatesWithGateAndBasis(
			scopedPool, scopedObservationParentKey, scopedObservationBlocked, request.Options.MaxSubjectCandidates,
			request.Options.AllowClarification, false, nil, 0, false, effectiveSearchLimit, 0,
			unscopedVisibility, gate, scopedIdentity, scopedIdentityTerms, aliasIdentityComplete,
			scopedTracer, request.RequestID, "", false, true,
		)
		if len(scopedResolution.Committed) > 0 {
			commits = append(commits, scopedCommit{scopedResolution, scopedBases, scopedDigests, scopedTracer})
		}
	}
	if len(commits) != 1 {
		return contextfabric.SubjectResolution{}, nil, nil, false, nil
	}
	winner := commits[0]
	winner.resolution.RetrievalDegraded = retrievalDegraded || coverageFloorDegraded
	winner.tracer.keep()
	return winner.resolution, winner.bases, winner.digests, true, nil
}
