package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4154: confirmed-kind truncation scoping.
//
// PROBLEM (see the ticket for the full trace): searchTruncated
// (resolve.go) is resolution-wide, one-way, and OR'd from every retrieval
// stage. resolution.go's commit-gate switch (~line 679) checks it BEFORE
// LoneFloor/TopFloor, so a genuinely complete, kind-scoped retrieval for a
// receipt-confirmed kind (contextfabric.ConfirmedExpectedKind, CHAOS-3900
// P1.D) can never commit once an EARLIER, UNRELATED, unscoped stage tripped
// the bit -- CHAOS-4132's rescue can populate the pool with the right
// candidate and the resolution still clarifies (case 57's exact shape).
//
// RATIFIED FIX SHAPE (sol-max consult, CHAOS-4154 ticket, "PASS WITH
// AMENDMENTS", posted verbatim in the ticket's comment thread): build a
// fully ISOLATED, authoritative confirmed-kind-scoped candidate snapshot
// that REPLACES the decision population for one re-evaluation of
// LoneFloor/TopFloor -- never merges or unions with the unscoped pool, and
// never lets an unscoped candidate's confidence, exact-match override, or
// any other decision-grade attribute reach the isolated gate call. The
// resolution-wide searchTruncated bit itself is NEVER mutated or cleared;
// the isolated call passes its OWN, separately-proven completeness in as a
// dedicated parameter (ResolveFromMergedCandidatesWithGateAndBasis's
// confirmedKindScopedBasis, resolution.go) instead.
//
// MECHANISM CHOICE, AND WHY (sol named three acceptable completeness
// contracts; this file's own history includes a REJECTED design worth
// recording so it is not re-attempted):
//
// REJECTED: treating deps.AliasLookup's identity-universe enumeration
// (CHAOS-3884, isAliasLookupScopedKind: repository/project/team) as an
// INDEPENDENT completeness proof that subsumes the vector channel too,
// because it is a full row enumeration rather than a ranked search. This
// was the initial design and team-lead's own explicit ask (fire the fix on
// a vector-enabled deployment too, using the small enumerable-kind
// populations). It does not hold, for two separate reasons, either one
// fatal on its own:
//   - AliasLookup's own match predicate (MatchIdentityRows) is label/alias/
//     provider-alias EQUALITY only. falkorgraph's own per-kind search-text
//     composition (search_text.go: teamSearchText/projectSearchText/
//     repositorySearchText) additionally indexes a team's description and
//     project_keys, a project's state, and a repository's tags -- none of
//     which AliasLookup ever inspects. A rival matching one of THOSE fields
//     is a genuine lexical competitor the ordinary fulltext/vector index
//     would surface, and the identity census would silently miss it: full
//     enumeration of ROWS is not full enumeration of RELEVANCE when the
//     match predicate checked per row is narrower than what retrieval
//     actually scores on.
//   - Independent of the first point: exact-equality row enumeration says
//     nothing about EMBEDDING/semantic similarity at all. A vector-channel
//     rival can be found via proximity to the query's embedding with zero
//     literal token overlap on ANY field -- label, alias, tag, or
//     description. Enumerating every row and checking string equality
//     against each never evaluates that axis, so it cannot prove "no
//     vector-surfaced rival exists" regardless of how narrow or wide the
//     per-row match predicate is.
//
// So AliasLookup's completeness is real but SCOPED: it proves there is no
// missed identity-EXACT claimant, nothing broader. That is still valuable
// -- it is folded in below as an ADDITIVE confidence-quality improvement
// (a genuine identity-trust bump for an isAliasLookupScopedKind candidate,
// via the SAME mergeSearchResults path an ordinary AliasLookup merge
// already uses) -- but it never DECIDES completeness on its own.
//
// ADOPTED: sol's mechanism 2 ("SearchKind is contractually proven to
// subsume the entire lexical-plus-vector candidate universe"), true only
// when this deployment has no live vector mechanism at all (see
// ResolveDeps.VectorMechanismConfigured's own doc comment). An EXHAUSTIVE
// per-term SearchKind pass -- every term, no early exit on the first
// satisfying hit (CHAOS-4132's rescue keeps its own early exit; this is a
// SEPARATE, additional pass with a different job) -- reuses the SAME
// fulltext index and SAME per-kind search-text composition ordinary
// lexical Search() does (kindScopedFulltextSearchNodes/runFulltextQuery,
// falkorgraph), so it covers every field that index covers -- label,
// aliases, tags, description, project_keys, state, whatever the kind's own
// template composes -- not merely label/alias. That proves the LEXICAL
// channel complete; deps.VectorMechanismConfigured==false is what proves
// there is no OTHER channel left to worry about. Mechanism 1 ("a kind-
// scoped equivalent for every gate-relevant retrieval channel, including
// SearchQuestion()") was considered and REJECTED for the vector channel
// specifically: this repo already has a ratified precedent against
// building a kind-scoped vector arm without live calibration --
// falkorgraph.kindScopedFulltextSearchNodes' own doc comment (CHAOS-4038)
// is explicit that db.idx.vector.queryNodes has no per-kind predicate
// today, and calibrating a new over-fetch depth blind, with no live corpus
// to validate against, is "exactly the kind of guess this repo's own
// CHAOS-3834/CHAOS-3829 calibration discipline rejects." Building one now,
// under this ticket, would repeat that mistake in a HIGHER-stakes spot (a
// false completeness proof feeding a commit decision, not a recall-lift
// floor).
//
// CONSEQUENCE, stated plainly: this mechanism is a documented no-op on any
// deployment with a live vector mechanism configured, for EVERY
// confirmedKind value, not only work_item. There is no sound way, without
// new calibrated vector-retrieval infrastructure this ticket does not
// build, to prove the confirmed-kind snapshot complete while a vector arm
// could have surfaced a rival elsewhere. The natural follow-up, once a live
// measurement slot exists to calibrate a kind-scoped vector arm, is the
// same shape as CHAOS-4038's own deferred note.
//
// DO-NOT-BUILD LIST (sol-max ruling, verbatim, so the next engineer meets
// it here and not only in the ticket):
//   - Do not mutate or reinterpret the resolution-wide searchTruncated bit.
//   - Do not detach kindScopedComplete from the exact snapshot it certifies.
//   - Do not let any unscoped row or score influence the scoped statistical
//     gate.
//   - Do not infer completeness from non-emptiness, counts, observed
//     truncation rates, limit+1, or top-result stability.
//   - Do not retain the early exit on the completeness-producing path (this
//     is why the SearchKind pass below always walks every term, unlike
//     applyConfirmedKindRescue's poolHasKind early exit).
//   - Do not omit a candidate-producing retrieval mechanism from the
//     completeness contract -- see VectorMechanismConfigured above, and see
//     the REJECTED section above for why the identity census specifically
//     must never be promoted back into a completeness decider.
//   - Do not change LoneFloor, TopFloor, TopGap, vector-only restrictions,
//     identity checks, or other veto semantics (resolution.go's gate runs
//     completely unmodified over whatever population it is handed).
//   - Do not let the always-on scoped pass accidentally alter the earlier
//     exact_index or identity_fast_path branches through shared-map
//     mutation (this is why the isolated pool below is a FRESH map, never
//     the unscoped candidatesBySubject).
//   - Do not place question text, labels, or other corpus material in the
//     new telemetry (confirmedKindScopeState/PopulationBasis are closed
//     vocabulary; see ResolutionTraceEvent's own doc comments).
//
// confirmedKindScopeState is the closed vocabulary
// ConfirmedKindScopeState/PopulationBasis telemetry carries.
const (
	// confirmedKindScopeNotAttempted: this mechanism's own trigger condition
	// was reached (confirmed kind, resolution-wide searchTruncated, nothing
	// committed yet -- see resolve.go's call site) but deps.SearchKind is
	// nil, so there is nothing to try. A "confirmed_kind_scope" trace event
	// still fires (resolve.go's call site emits it unconditionally once the
	// trigger fires) -- its own presence proves the mechanism was reached,
	// this state says it had nothing to try.
	confirmedKindScopeNotAttempted = "not_attempted"
	// confirmedKindScopeComplete: every exhaustive per-term SearchKind call
	// succeeded, untruncated and non-degraded, AND no live vector mechanism
	// exists for this deployment. The ONLY state that lets resolve.go
	// re-evaluate the commit gate over this isolated snapshot.
	confirmedKindScopeComplete = "complete"
	// confirmedKindScopeTruncated: at least one exhaustive SearchKind call
	// hit its own row bound -- a genuine same-kind rival may have been cut
	// off. The snapshot this call built is discarded; the ordinary
	// (unscoped) ambiguous/clarification outcome stands.
	confirmedKindScopeTruncated = "truncated"
	// confirmedKindScopeFailed: at least one exhaustive SearchKind call
	// reported a retrieval mechanism unavailable (degraded) for that term.
	// Treated exactly like truncated -- an incomplete read must not
	// masquerade as a proof.
	confirmedKindScopeFailed = "failed"
	// confirmedKindScopePlanIncomplete: the lexical channel WAS exhaustively
	// covered (every term, untruncated, non-degraded), but this deployment
	// has a live vector mechanism (deps.VectorMechanismConfigured==true), so
	// the completeness PLAN itself cannot rule out a vector-surfaced rival
	// (sol's amendment 2: "SearchKind exhaustion proves lexical completeness
	// only"). The snapshot is discarded even though the lexical pass itself
	// succeeded -- see this file's own "REJECTED" section for why the
	// identity census cannot close this gap either.
	confirmedKindScopePlanIncomplete = "plan_incomplete"
)

// buildConfirmedKindScopedSnapshot builds the isolated, confirmed-kind-only
// candidate population this ticket's gate re-evaluation consumes. The
// EXHAUSTIVE SearchKind pass is what decides completeness (see this file's
// own top-level doc comment); for isAliasLookupScopedKind kinds
// (repository/project/team) with a complete AliasLookup read, this
// resolution's own already-fetched aliasClaimantsByTerm (zero additional
// I/O) is ALSO folded into the SAME pool first, purely as a confidence-
// quality improvement (a genuine identity-trust bump where it applies) --
// it never sets or upgrades the returned state on its own.
//
// Either contribution merges into ONE FRESH pool/observationParentKey/
// observationBlocked map set, never the caller's unscoped ones, so nothing
// either pass finds can silently merge into (or be overwritten by) an
// unscoped entry for the same subject.
//
// scopedIdentity/scopedIdentityTerms are ALSO fresh, built ONLY from what
// this attempt itself finds -- see their own doc comment below (codex
// review finding, Medium, confirmed) for why reusing the caller's
// whole-resolution identity/identityTerms maps here was wrong: it let an
// unrelated UNSCOPED, possibly cross-kind claimant veto a scoped candidate
// through identityCollision, and left mutation residue behind for the
// LATER CHAOS-3896 evidence-census re-decision to consult even when this
// attempt was ultimately discarded. This pass's own exhaustiveness (every
// term, for the confirmed kind) still catches a genuine SAME-KIND
// collision, since a colliding same-kind claimant is found by this SAME
// pass and recorded into these SAME fresh maps -- nothing is lost, only
// the meaningless cross-kind veto the confirmed-kind receipt's own
// scope-narrowing authority (P1.D) already resolves.
//
// searchLimit is the caller's own effectiveSearchLimit -- the SAME real
// per-call bound an ordinary Search call in this resolution is clamped to
// -- so a confirmed-kind commit earns exactly the trust an untruncated
// ORDINARY resolution already gets, scoped to just this kind's own
// candidates.
//
// No early exit on a satisfying hit (unlike CHAOS-4132's
// applyConfirmedKindRescue, a DIFFERENT pass with a DIFFERENT job): this
// call's entire purpose is proving no OTHER same-kind rival exists among
// this resolution's own terms, which requires walking every term
// regardless of what earlier terms already found.
//
// A genuine SearchKind failure aborts and propagates as an error, exactly
// like every other retrieval pass in this package.
func buildConfirmedKindScopedSnapshot(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, aliasClaimantsByTerm map[string][]CandidateNode, aliasIdentityComplete bool, kind contextfabric.SubjectKind, searchLimit int) (pool map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, scopedIdentity identityClaimants, scopedIdentityTerms identityMatchTerms, state string, traversalDegraded int, authzDropped int, err error) {
	if deps.SearchKind == nil {
		return nil, nil, nil, nil, nil, confirmedKindScopeNotAttempted, 0, 0, nil
	}
	pool = make(map[string]contextfabric.SubjectCandidate)
	observationParentKey = make(map[string]string)
	observationBlocked = make(map[string]bool)
	// scopedIdentity/scopedIdentityTerms (codex review finding, Medium,
	// confirmed): FRESH, built ONLY from what this attempt itself finds --
	// NEVER the caller's whole-resolution identity/identityTerms maps. An
	// earlier version reused the shared maps and mutated them via
	// mergeSearchResults/recordIdentityClaim regardless of whether this
	// attempt ultimately succeeded, which (a) let an unrelated UNSCOPED
	// claimant veto a scoped candidate through identityCollision even
	// though the confirmed-kind receipt already resolves any cross-kind
	// ambiguity (P1.D: a receipt-confirmed kind IS the caller's own
	// authority over which kind is in play, so a same-term claimant of a
	// DIFFERENT kind was never a real competitor for this resolution), and
	// (b) left residue behind for the LATER CHAOS-3896 evidence-census
	// re-decision to consult even when this attempt was discarded
	// (truncated/failed/plan_incomplete). A genuine SAME-KIND collision is
	// still caught: this pass is exhaustive over every term for the
	// confirmed kind, so a colliding same-kind claimant is found by THIS
	// SAME pass and recorded into these SAME fresh maps.
	scopedIdentity = identityClaimants{}
	scopedIdentityTerms = identityMatchTerms{}

	// Identity-census contribution FIRST (confidence-quality only, never a
	// completeness decider -- see this file's own "REJECTED" section).
	// Merging it before the SearchKind pass below means a subject BOTH
	// passes find keeps its identity-trust-bumped entry (MergeCandidates'
	// higher-confidence-wins rule), exactly like the unscoped resolution's
	// own AliasLookup-then-Search ordering already produces for the same
	// node.
	if isAliasLookupScopedKind(kind) && aliasIdentityComplete {
		identityTraversalDegraded, identityAuthzDropped := mergeIdentityCensusCandidates(ctx, principal, request, deps, aliasClaimantsByTerm, kind, pool, observationParentKey, observationBlocked, scopedIdentity, scopedIdentityTerms)
		traversalDegraded += identityTraversalDegraded
		authzDropped += identityAuthzDropped
	}

	anyTruncated := false
	anyDegraded := false
	for _, term := range terms {
		results, truncated, degraded, searchErr := deps.SearchKind(ctx, term, kind, searchLimit)
		if searchErr != nil {
			return pool, observationParentKey, observationBlocked, scopedIdentity, scopedIdentityTerms, confirmedKindScopeFailed, traversalDegraded, authzDropped, searchErr
		}
		if truncated {
			anyTruncated = true
		}
		if degraded {
			anyDegraded = true
		}
		// allowExactMatch=true: these are the SAME genuine caller-derived
		// subject terms every other pass in this resolution already used.
		// vectorArmSimilarity=nil: SearchKind is lexical-only (see
		// kindScopedFulltextSearchNodes' own doc comment) -- there is
		// nothing for this pass to contribute to CHAOS-3829's carve-out,
		// which this mechanism disables for its own gate call regardless
		// (resolve.go's call site).
		termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, results, pool, observationParentKey, observationBlocked, true, nil, scopedIdentity, scopedIdentityTerms)
		traversalDegraded += termTraversalDegraded
		authzDropped += termAuthzDropped
	}
	switch {
	case anyTruncated:
		return pool, observationParentKey, observationBlocked, scopedIdentity, scopedIdentityTerms, confirmedKindScopeTruncated, traversalDegraded, authzDropped, nil
	case anyDegraded:
		return pool, observationParentKey, observationBlocked, scopedIdentity, scopedIdentityTerms, confirmedKindScopeFailed, traversalDegraded, authzDropped, nil
	case deps.VectorMechanismConfigured:
		return pool, observationParentKey, observationBlocked, scopedIdentity, scopedIdentityTerms, confirmedKindScopePlanIncomplete, traversalDegraded, authzDropped, nil
	default:
		return pool, observationParentKey, observationBlocked, scopedIdentity, scopedIdentityTerms, confirmedKindScopeComplete, traversalDegraded, authzDropped, nil
	}
}

// mergeIdentityCensusCandidates folds this resolution's own already-fetched
// deps.AliasLookup result (aliasClaimantsByTerm), filtered to kind, into
// pool -- see buildConfirmedKindScopedSnapshot's own doc comment for why
// this is a confidence-quality addition only, never a completeness
// decider. Merges through the SAME mergeSearchResults path an ordinary
// (unscoped) AliasLookup merge already uses, so mechanism/confidence
// computation is byte-identical to how the unscoped resolution would have
// scored the identical node.
//
// Zero additional I/O: filtering an already-fetched map is pure Go. A
// nil/empty aliasClaimantsByTerm (nothing matched any term) is simply a
// no-op here.
func mergeIdentityCensusCandidates(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, aliasClaimantsByTerm map[string][]CandidateNode, kind contextfabric.SubjectKind, pool map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, identity identityClaimants, identityTerms identityMatchTerms) (traversalDegraded int, authzDropped int) {
	for term, nodes := range aliasClaimantsByTerm {
		var sameKind []CandidateNode
		for _, node := range nodes {
			subject, ok := NodeSubject(node)
			if !ok || subject.Kind != kind {
				continue
			}
			sameKind = append(sameKind, node)
		}
		if len(sameKind) == 0 {
			continue
		}
		// allowExactMatch=true, vectorArmSimilarity=nil: the SAME call
		// shape resolve.go's own (unscoped) AliasLookup merge already uses
		// for these exact nodes -- see that call site's own comment.
		termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, sameKind, pool, observationParentKey, observationBlocked, true, nil, identity, identityTerms)
		traversalDegraded += termTraversalDegraded
		authzDropped += termAuthzDropped
	}
	return traversalDegraded, authzDropped
}
