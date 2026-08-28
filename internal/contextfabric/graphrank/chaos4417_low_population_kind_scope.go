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
// machinery never engages, and the shared cut can ALSO crowd repository
// out of the kind_offer boundary before this ticket (case 2's own shape:
// "kind_offer boundary_kinds_before_repair=[ci_pipeline_run]").
//
// SHAPE (team-lead ruling, R4, superseding an earlier LoneFloor-commit
// design that reached codex round 3 before being caught): this rescue
// NEVER commits. It reuses CHAOS-4154's completeness mechanism
// (buildConfirmedKindScopedSnapshot, chaos4154_confirmed_kind_scope.go --
// deliberately REUSED, not reimplemented) run PRE-CONFIRMATION to produce
// OFFER candidates only, exactly the same "additive, never touches
// candidatesBySubject/pool, never reaches resolution.Committed" contract
// CHAOS-4038's own coverage floor already carries for these same three
// kinds (chaos4038_kind_coverage.go's offerOnlyPool split) -- this ticket
// is that same contract's EXHAUSTIVE-census sibling, not a new authority.
//
// WHY NOT COMMIT (the R3 codex finding this design closes, not merely
// works around): a statistical LoneFloor/TopFloor decision cannot survive
// resolution-wide searchTruncated soundly -- only STRING EQUALITY can
// (AGENTS.md, internal/contextfabric/AGENTS.md:313, CHAOS-3810: "no
// unseen row can outrank it"). CHAOS-4154 gets around this not by proving
// no unseen rival of ANY kind exists, but by CALLER AUTHORITY: a
// receipt-confirmed kind makes every OTHER kind categorically
// out-of-scope, not merely improbable. CHAOS-4417 has no such authority
// pre-confirmation -- request.ExpectedKinds (the only "the interpreted
// question implies a kind" signal available) is DELIBERATELY walled off
// from candidate-pool narrowing today (ports.go's own doc comment, CHAOS-
// 3972 P3/P1.D): it may narrow the evidence round's pooled-kind
// hypothesis ONLY, and only behind kindInsensitivityProof (a re-run
// agreement check) as a named HARD PRECONDITION, which this ticket does
// not build. Follow-up ticket: "Turn-1 commit under interpretation kind
// authority requires kindInsensitivityProof wiring" (CF, Medium, related
// 4417/3972) -- not built here.
//
// So: an exhaustively-proven-complete repository/project/team candidate
// this rescue finds is offered (subject_candidate / expected_kind
// disclosures), and COMMITS at turn 2 through the EXISTING,
// already-sound confirmed-kind receipt path (CHAOS-4132/CHAOS-4154) once
// the caller confirms which kind/candidate was meant -- the two-turn
// shape the corpus harness already measures.
//
// chaos4417LowPopulationScopedKinds mirrors aliasLookupScopedKinds
// (repository/project/team, subject.go) -- the SAME three kinds CHAOS-4154
// already trusts buildConfirmedKindScopedSnapshot's completeness contract
// for (its own identity-census-enumerable set). work_item/ci_pipeline_run/
// pull_request/pull_request_review stay OUT deliberately: their
// populations are exactly what searchTruncated exists to protect against.
// Derived from the SAME map applyKindCoverageFloor/isAliasLookupScopedKind
// already share (never a second, independently maintained kind list), via
// the SAME sortedKinds helper kindCoverageOrder already uses, so iteration
// order is deterministic without a third hand-maintained ordering.
var chaos4417LowPopulationScopedKinds = sortedKinds(aliasLookupScopedKinds)

// lowPopulationKindScopeOutcome* is the closed vocabulary
// applyLowPopulationKindOffers' own summary "low_population_kind_scope"
// event (empty LowPopulationKindScopeKind) reports on
// LowPopulationKindScopeOutcome -- fired exactly once per call, regardless
// of outcome, so a reader can always tell why this rescue produced (or did
// not produce) offers without reconstructing it from the per-kind events
// alone.
const (
	// lowPopulationKindScopeOutcomeVectorConfigured: deps.VectorMechanismConfigured
	// is true, so every kind's completeness plan is foreclosed
	// (confirmedKindScopePlanIncomplete) before any SearchKind call --
	// short-circuited without spending the I/O.
	lowPopulationKindScopeOutcomeVectorConfigured = "vector_configured"
	// lowPopulationKindScopeOutcomeOfferOnly (team-lead ruling): at least
	// one kind's census came back confirmedKindScopeComplete with one or
	// more candidates -- those candidates were added to the offer union.
	// Never a commit signal.
	lowPopulationKindScopeOutcomeOfferOnly = "offer_only"
	// lowPopulationKindScopeOutcomeNoLowPopCandidates (team-lead ruling):
	// every attempted kind's census completed (or was skipped for being
	// incomplete) but nothing was found to offer.
	lowPopulationKindScopeOutcomeNoLowPopCandidates = "no_low_pop_candidates"
	// lowPopulationKindScopeOutcomeError: buildConfirmedKindScopedSnapshot
	// returned a genuine backend error, propagated to the caller.
	lowPopulationKindScopeOutcomeError = "error"
)

// applyLowPopulationKindOffers runs buildConfirmedKindScopedSnapshot once
// per chaos4417LowPopulationScopedKinds member and returns every candidate
// from a kind whose census came back confirmedKindScopeComplete -- for the
// caller (resolve.go) to union into its own offer-only candidate set
// (coverageCandidates, alongside CHAOS-4038's own contribution), never
// into candidatesBySubject/pool and never adopted as a commit.
//
// Unlike a commit-shaped mechanism, an incomplete kind's census does NOT
// abort the whole call: an offer is a suggestion, not a proof, so a
// truncated/degraded/plan_incomplete kind simply contributes nothing
// (skipped, not fatal) while any OTHER kind's own complete census still
// offers normally. This is deliberately looser than
// buildConfirmedKindScopedSnapshot's own confirmed-kind commit caller
// (resolve.go's CHAOS-4154 call site), which must fail closed because it
// is deciding a COMMIT -- see this file's own top doc comment for why
// commit-shaped trust is unavailable to this ticket at all.
//
// A genuine buildConfirmedKindScopedSnapshot error still aborts and
// propagates, exactly like every other retrieval pass in this package --
// a real backend fault is never silently downgraded to "nothing to offer"
// either.
func applyLowPopulationKindOffers(
	ctx context.Context,
	principal storage.Principal,
	request contextfabric.InvestigationRequest,
	deps ResolveDeps,
	terms []string,
	aliasClaimantsByTerm map[string][]CandidateNode,
	aliasIdentityComplete bool,
	effectiveSearchLimit int,
) (offered []contextfabric.SubjectCandidate, err error) {
	outcome := lowPopulationKindScopeOutcomeNoLowPopCandidates
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
		return nil, nil
	}
	for _, kind := range chaos4417LowPopulationScopedKinds {
		scopedPool, _, _, _, _, scopeState, scopeTraversalDegraded, scopeAuthzDropped, _, scopeErr :=
			buildConfirmedKindScopedSnapshot(ctx, principal, request, deps, terms, aliasClaimantsByTerm, aliasIdentityComplete, kind, effectiveSearchLimit)
		if scopeErr != nil {
			outcome = lowPopulationKindScopeOutcomeError
			return nil, scopeErr
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
			continue
		}
		for _, candidate := range scopedPool {
			offered = append(offered, candidate)
		}
	}
	if len(offered) > 0 {
		outcome = lowPopulationKindScopeOutcomeOfferOnly
	}
	return offered, nil
}
