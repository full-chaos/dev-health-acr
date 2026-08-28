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

// applyLowPopulationKindScopedRescue builds ONE isolated, cross-kind
// candidate population -- the union of buildConfirmedKindScopedSnapshot's
// own result for EVERY chaos4417LowPopulationScopedKinds member -- and
// re-evaluates the commit gate ONCE over that union.
//
// codex R1 finding (P1, confirmed): an EARLIER version of this function
// ran the gate SEPARATELY per kind and committed whenever exactly one
// kind's isolated call happened to clear ITS OWN LoneFloor. That is
// unsound: a repository candidate at 0.80 clears LoneFloor (0.72) when
// evaluated ALONE, but the SAME 0.80 candidate fails TopFloor (0.88)
// against a genuine 0.71 project rival once both are visible to the SAME
// gate call -- exactly the cross-kind ambiguity TopFloor/TopGap exist to
// catch. Per-kind gate calls can never see that rival, so they would
// commit where the ordinary (untruncated) resolution-wide gate would
// clarify. Unioning every kind's population into ONE pool and calling
// ResolveFromMergedCandidatesWithGateAndBasis exactly once restores the
// SAME cross-kind arbitration LoneFloor/TopFloor already provide for an
// ordinary untruncated resolution -- this mechanism only widens WHICH
// population the gate is allowed to decide over when the ordinary one
// truncated, never how the gate itself grades candidates once it does
// (CHAOS-4154's own do-not-build list, chaos4154_confirmed_kind_scope.go).
//
// codex R1 finding (P1, confirmed): a SECOND, independent defect in the
// same earlier version let a kind whose OWN scoped census was incomplete
// (truncated/degraded/plan_incomplete) be silently skipped while a
// DIFFERENT, complete kind still committed alone -- the skipped kind's
// unseen population could hide a genuine rival. This function now fails
// CLOSED: unless EVERY attempted kind's snapshot is
// confirmedKindScopeComplete, no union is built and no gate call runs at
// all -- an incomplete sibling scope aborts the WHOLE rescue, not just its
// own contribution. Trying kinds in chaos4417LowPopulationScopedKinds'
// deterministic order and stopping at the FIRST incomplete one (rather
// than always attempting all three) also directly addresses the R1
// I/O-fan-out finding below: the common case where any one kind is
// genuinely large/truncated for this org now costs at most that one
// kind's exhaustive pass, not three.
//
// codex R1 finding (P1, confirmed): deps.VectorMechanismConfigured==true
// makes buildConfirmedKindScopedSnapshot return confirmedKindScopePlanIncomplete
// for EVERY kind, unconditionally (chaos4154_confirmed_kind_scope.go's own
// documented consequence) -- so on any deployment with a live vector
// mechanism this rescue can never produce a "every kind complete" union
// regardless of how many kinds it tries. Checked ONCE, up front, before
// any SearchKind call: paying for up to three exhaustive per-term passes
// (each up to len(terms) calls -- a real request can carry dozens of
// terms) to reach a foregone conclusion is exactly the unbounded-fan-out
// cost R1 flagged, and skipping it here also skips the wasted
// attemptConfirmedKindVectorCensus shadow census buildConfirmedKindScopedSnapshot
// would otherwise run per kind for a census outcome this caller has no use
// for (chaos4417LowPopulationScopedKinds' populations have no vector arm
// this rescue is authorized to evaluate -- CHAOS-4154's own REJECTED
// section explains why building one blind is out of scope).
//
// codex R1 finding (P2, confirmed): a scoped call's "corroboration" trace
// event (resolution.go) is NOT one of the stages discardableDecisionTracer
// buffers (only "decision"/"ranked_cut" are), so it reached the real
// tracer immediately even when the scoped resolution it describes was
// never merged into 'pool' and never kept -- corrupting any consumer
// (e.g. the two-turn harness's expected_subject_in_pool field) that reads
// a "corroboration" event as proof a candidate reached the real,
// committable pool. discardableDecisionTracer now buffers "corroboration"
// too (resolve.go) -- a general fix, not scoped to this file, since
// CHAOS-4154's own confirmed-kind call site shares the exact same
// discardableDecisionTracer type and the exact same latent leak whenever
// its scoped resolution does not commit.
// lowPopulationKindScopeOutcome* (codex R1 P2, confirmed) is the closed
// vocabulary applyLowPopulationKindScopedRescue's own summary
// "low_population_kind_scope" event (empty LowPopulationKindScopeKind,
// fired via a defer covering every return path) reports on
// LowPopulationKindScopeOutcome -- so a reader can always tell WHY this
// rescue ended the way it did without having to reconstruct it from the
// per-kind events and the (possibly discarded) union decision event
// together.
const (
	// lowPopulationKindScopeOutcomeVectorConfigured: deps.VectorMechanismConfigured
	// is true, so every kind's completeness plan is foreclosed
	// (confirmedKindScopePlanIncomplete) before any SearchKind call --
	// short-circuited without spending the I/O (codex R1 P1 fan-out
	// finding).
	lowPopulationKindScopeOutcomeVectorConfigured = "vector_configured"
	// lowPopulationKindScopeOutcomeIncompleteSibling: at least one of
	// chaos4417LowPopulationScopedKinds did not reach
	// confirmedKindScopeComplete -- the whole rescue fails closed (codex
	// R1 P1 finding), remaining kinds not attempted.
	lowPopulationKindScopeOutcomeIncompleteSibling = "incomplete_sibling"
	// lowPopulationKindScopeOutcomeNoCandidates: every kind completed but
	// the union population was empty -- nothing for the gate to decide.
	lowPopulationKindScopeOutcomeNoCandidates = "no_candidates"
	// lowPopulationKindScopeOutcomeGateInvalid: gate.Validate() failed --
	// the SAME conjunct every other commit path in this resolution
	// requires.
	lowPopulationKindScopeOutcomeGateInvalid = "gate_invalid"
	// lowPopulationKindScopeOutcomeDeclined: the union gate ran but did
	// not commit (ambiguous, or below LoneFloor/TopFloor) -- the ordinary
	// unscoped resolution's own ambiguous/clarification outcome stands.
	lowPopulationKindScopeOutcomeDeclined = "declined"
	// lowPopulationKindScopeOutcomeCommitted: the union gate committed
	// exactly the resolution this function returned.
	lowPopulationKindScopeOutcomeCommitted = "committed"
	// lowPopulationKindScopeOutcomeError: buildConfirmedKindScopedSnapshot
	// returned a genuine backend error, propagated to the caller.
	lowPopulationKindScopeOutcomeError = "error"
)

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
	// outcome (codex R1 P2, confirmed): a closed-vocabulary summary of WHY
	// this rescue attempt ended the way it did, emitted exactly once
	// (deferred below) regardless of which return path fires -- see
	// lowPopulationKindScopeOutcome* consts' own doc comment. This is a
	// DEDICATED stage event, distinct from "decision" (never subject to
	// discardableDecisionTracer's "last decision event describes the
	// returned resolution" invariant), so it is never silently swallowed
	// the way the union gate's own "decision" event is when this rescue
	// declines to commit (chaos4154_confirmed_kind_scope.go's own
	// discard-on-non-commit convention, reused below for that event
	// specifically).
	outcome := lowPopulationKindScopeOutcomeDeclined
	if deps.ResolutionTracer != nil {
		defer func() {
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID:                     request.RequestID,
				Stage:                         "low_population_kind_scope",
				LowPopulationKindScopeOutcome: outcome,
			})
		}()
	}
	if deps.VectorMechanismConfigured {
		outcome = lowPopulationKindScopeOutcomeVectorConfigured
		return contextfabric.SubjectResolution{}, nil, nil, false, nil
	}
	unionPool := make(map[string]contextfabric.SubjectCandidate)
	unionObservationParentKey := make(map[string]string)
	unionObservationBlocked := make(map[string]bool)
	unionIdentity := identityClaimants{}
	unionIdentityTerms := identityMatchTerms{}
	for _, kind := range chaos4417LowPopulationScopedKinds {
		scopedPool, scopedObservationParentKey, scopedObservationBlocked, scopedIdentity, scopedIdentityTerms, scopeState, scopeTraversalDegraded, scopeAuthzDropped, _, scopeErr :=
			buildConfirmedKindScopedSnapshot(ctx, principal, request, deps, terms, aliasClaimantsByTerm, aliasIdentityComplete, kind, effectiveSearchLimit)
		if scopeErr != nil {
			outcome = lowPopulationKindScopeOutcomeError
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
		if scopeState != confirmedKindScopeComplete {
			// Fail closed (codex R1 P1): an incomplete sibling scope may
			// hide a genuine rival the union gate below would need to see.
			// Stop trying further kinds too -- this rescue cannot fire
			// regardless of what they find, so their own exhaustive
			// per-term passes would be pure waste (codex R1 P1 fan-out
			// finding).
			outcome = lowPopulationKindScopeOutcomeIncompleteSibling
			return contextfabric.SubjectResolution{}, nil, nil, false, nil
		}
		mergeSubjectCandidatePool(unionPool, scopedPool)
		for key, value := range scopedObservationParentKey {
			if _, exists := unionObservationParentKey[key]; !exists {
				unionObservationParentKey[key] = value
			}
		}
		for key, value := range scopedObservationBlocked {
			unionObservationBlocked[key] = unionObservationBlocked[key] || value
		}
		mergeIdentityClaimants(unionIdentity, scopedIdentity)
		for key, entries := range scopedIdentityTerms {
			unionIdentityTerms[key] = append(unionIdentityTerms[key], entries...)
		}
	}
	if len(unionPool) == 0 {
		outcome = lowPopulationKindScopeOutcomeNoCandidates
		return contextfabric.SubjectResolution{}, nil, nil, false, nil
	}
	if !gateValid {
		outcome = lowPopulationKindScopeOutcomeGateInvalid
		return contextfabric.SubjectResolution{}, nil, nil, false, nil
	}
	scopedTracer := &discardableDecisionTracer{real: deps.ResolutionTracer}
	scopedResolution, scopedBases, scopedDigests := ResolveFromMergedCandidatesWithGateAndBasis(
		unionPool, unionObservationParentKey, unionObservationBlocked, request.Options.MaxSubjectCandidates,
		request.Options.AllowClarification, false, nil, 0, false, effectiveSearchLimit, 0,
		unscopedVisibility, gate, unionIdentity, unionIdentityTerms, aliasIdentityComplete,
		scopedTracer, request.RequestID, "", false, true,
	)
	if len(scopedResolution.Committed) == 0 {
		outcome = lowPopulationKindScopeOutcomeDeclined
		return contextfabric.SubjectResolution{}, nil, nil, false, nil
	}
	outcome = lowPopulationKindScopeOutcomeCommitted
	scopedResolution.RetrievalDegraded = retrievalDegraded || coverageFloorDegraded
	scopedTracer.keep()
	return scopedResolution, scopedBases, scopedDigests, true, nil
}

// mergeSubjectCandidatePool unions src into dst, keeping the higher-
// confidence entry for a subject key present in both -- the SAME
// MergeCandidates rule mergeSearchResults itself uses for two findings of
// the same subject. Subject keys cannot collide ACROSS kinds (the key
// space is kind-qualified), so in practice this only ever matters if a
// future caller passes overlapping pools; MergeCandidates handles it
// correctly regardless.
func mergeSubjectCandidatePool(dst, src map[string]contextfabric.SubjectCandidate) {
	for key, candidate := range src {
		if existing, exists := dst[key]; exists {
			dst[key] = MergeCandidates(existing, candidate)
			continue
		}
		dst[key] = candidate
	}
}

// mergeIdentityClaimants unions src into dst -- both are the SAME
// class -> term -> subjectKey -> true shape identityClaimants always is
// (chaos3884_identity.go), so union is a plain three-level set union.
// Merging identity claims ACROSS kinds (not just within one) matters here
// specifically because chaos4417LowPopulationScopedKinds IS
// isAliasLookupScopedKind's own set -- an alias/provider-key claim shared
// by, say, a repository and a team is exactly the cross-kind identity
// collision identityCollision/identityCrossClassRivalClaimant exist to
// catch, and neither can see it if each kind's claims stay in its own
// isolated map.
func mergeIdentityClaimants(dst, src identityClaimants) {
	for class, terms := range src {
		if dst[class] == nil {
			dst[class] = map[string]map[string]bool{}
		}
		for term, claimants := range terms {
			if dst[class][term] == nil {
				dst[class][term] = map[string]bool{}
			}
			for subjectKey := range claimants {
				dst[class][term][subjectKey] = true
			}
		}
	}
}
