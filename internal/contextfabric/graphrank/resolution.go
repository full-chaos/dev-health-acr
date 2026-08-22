package graphrank

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CommitGatePolicy names the three confidence/margin thresholds
// ResolveFromMergedCandidatesWithGate's commit decision applies BEFORE the
// CHAOS-3829 margin-rescue carve-out ever runs (vectorMarginCommitThreshold
// is a separate parameter, not part of this policy -- it already has its
// own calibrated-per-identity threading via ResolveDeps/RetrievalPolicy).
//
// CHAOS-3857 (gate-threshold sweep): this type exists so a measurement
// harness can override these three constants without touching this file's
// decision logic, mirroring the EXACT seam VectorMarginCommitThreshold/
// CalibratedTopK already use for the SAME reason (see ResolveDeps' own doc
// comments). It is a MEASUREMENT/SWEEP surface, not a recommended
// deployment knob -- chris picks the eventual ratified operating point
// from the sweep's measured commit-rate-vs-wrong-commit curve, not this
// type's defaults. The zero value is NOT valid production configuration on
// its own; see DefaultCommitGatePolicy for the calibrated values every
// production call site uses.
type CommitGatePolicy struct {
	// LoneFloor is the minimum Confidence a SINGLE eligible candidate must
	// clear to auto-commit alone. Production value: 0.72, the CHAOS-3778
	// calibration -- see DefaultCommitGatePolicy's doc comment for the
	// CHAOS-3857 sweep that evaluated moving it, and why it stayed.
	LoneFloor float64
	// TopFloor is the minimum Confidence the TOP of two-or-more eligible
	// candidates must clear before a gap-based commit is even considered.
	// Calibrated production value: 0.88 (unchanged by CHAOS-3857's sweep).
	TopFloor float64
	// TopGap is the minimum Confidence gap the top candidate must hold over
	// the second-ranked eligible candidate for the top-of-two commit to
	// fire. Calibrated production value: 0.12 (unchanged by CHAOS-3857's
	// sweep).
	TopGap float64
}

// DefaultCommitGatePolicy returns the CURRENT, ratified production
// commit-gate thresholds. It always passes Validate() -- this is asserted
// by TestCommitGatePolicyValidate's "valid" case, so a future edit to the
// calibrated constants that accidentally produces an invalid policy fails
// loudly instead of silently making every production commit decision
// commit-nothing (see Validate's own doc comment for why that is the
// evaluator's fail-closed behavior for an invalid policy).
// ResolveFromMergedCandidates and every existing production/test call site
// use exactly this policy; only CHAOS-3857's sweep tooling (and
// falkorgraph's env-driven override, when an operator explicitly sets one)
// calls ResolveFromMergedCandidatesWithGate with anything else.
//
// CHAOS-3857 (gate-threshold sweep, chris, 2026-08) swept LoneFloor/
// TopFloor+TopGap/M independently against the full 50-case corpus: a
// 12-cell grid, hard-gated on wrong_commit=0 and clean controls, ran clean
// at LoneFloor=0.68, and a direct-API confirmation run at that point
// (committed 2/50, 0 wrong, clean controls) also passed. Chris ratified
// 0.68 on that evidence.
//
// It was then REJECTED during ship verification, on two counter-examples
// neither the corpus nor the confirmation run happened to contain --
// recorded here because "the sweep passed" is not the whole story, and a
// future re-attempt at lowering LoneFloor needs to know what actually broke
// it, not just that something did:
//
//  1. Vector-only arithmetic: LoneFloor=0.68 sits below
//     falkorgraph.vectorRelevanceCeiling (0.70), which breaks AC-3778-3 ("a
//     vector hit alone never commits") by pure arithmetic -- that
//     invariant had only ever held because vectorRelevanceCeiling was
//     coincidentally below the OLD LoneFloor (0.72), not because anything
//     enforced it. isVectorOnlyCandidate (below) closes this structurally
//     now, regardless of what LoneFloor is set to.
//  2. Lexical wrong-commit, on live infrastructure:
//     TestLiveRelationshipProjectionNeverDowngradesAnEndpointsOwnAuthorization
//     (falkorgraph, a pre-existing, unmodified test against a real
//     FalkorDB) searched for a work item named "Repo-less work item" and
//     got a DIFFERENT subject, "Repo-backed work item", back as a
//     false-positive lexical match: 3 of 4 tokens overlap, which
//     fulltextRelevanceFloor/span normalizes to 0.6875 -- inside
//     [0.68, 0.72), so it auto-committed the WRONG subject at
//     LoneFloor=0.68 where it correctly stayed ambiguous at 0.72
//     (confirmed causally, both directions, by toggling this constant and
//     rerunning the live test). See
//     TestLexicalThreeOfFourTokenOverlapStaysAmbiguousAtTheDefaultGate
//     (resolution_gate_policy_test.go) for the fast, unit-level pin of this
//     exact case. Lexical-only auto-commit is intentional design in
//     general (falkorgraph/queries.go's fulltextRelevanceCeiling doc
//     comment) -- this is not that invariant breaking, it is a NEW
//     population (candidates in [0.68, 0.72) specifically, reachable ONLY
//     if LoneFloor is ever lowered again) that the swept corpus never
//     happened to exercise turning out, empirically, to contain a real
//     false-positive shape (two subjects with mostly-shared,
//     generically-templated tokens and one differing word). IMPORTANT for
//     any future reader of isVectorOnlyCandidate: this is NOT AC-3778-3 and
//     must not be "fixed" as if it were the same hole -- AC-3778-3 is
//     vector-specific by name and ratification, lexical-alone auto-commit
//     at its own band ceiling is deliberate CHAOS-3778/D11-era design, and
//     a mechanism-identity guard analogous to isVectorOnlyCandidate would
//     be the WRONG fix here even though the symptom (a wrong commit in
//     [0.68, 0.72)) looks superficially similar -- the actual fix, if
//     LoneFloor is ever lowered again, is re-running (or tightening) the
//     lexical scoring/ambiguity evaluation this counter-example exposed,
//     not gating lexical-only candidates out of the commit gate entirely.
//
// Both counter-examples share the same shape: zero committed cases in
// either the 12-cell sweep or the confirmation run ever actually landed a
// confidence in the newly-exposed [0.68, 0.72) band (verified against the
// stored result JSONs) -- the clean pass was a true negative on the
// CORPUS, not evidence the band itself was safe. LoneFloor=0.68 bought no
// measured lift and cost a demonstrated wrong-commit class; it was
// abandoned rather than patched further.
//
// LoneFloor stays 0.72, which is (once again, not merely "still") exactly
// mechanism.go's CorroboratedFloor. That equality is coincidence-by-
// calibration -- CHAOS-3778 picked both against the same target, not a
// structural link (CorroboratedConfidence, mechanism.go, never receives a
// CommitGatePolicy at all, and CHAOS-3857's sweep tooling could move one
// without the other) -- see mechanism.go's own doc comment for the full
// account of that decoupling and why it matters even though the two values
// currently agree again. What IS structural, and stays regardless of
// either constant's value: isVectorOnlyCandidate (below) guards the
// vector-only population by mechanism identity, not by arithmetic --
// shipped anyway even though it is provably inert at today's LoneFloor
// (0.72 > vectorRelevanceCeiling's 0.70 with room to spare), because it is
// what makes a FUTURE env-override attempt at lowering LoneFloor safe on
// the vector side without needing this exact investigation to repeat.
func DefaultCommitGatePolicy() CommitGatePolicy {
	return CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.88, TopGap: 0.12}
}

// Validate reports whether g is a usable commit-gate policy: every
// threshold finite and in (0, 1], TopGap strictly less than TopFloor (a
// gap as wide as or wider than the floor itself is nonsensical -- it would
// mean "the top candidate must beat the second by more than the minimum
// confidence it must ITSELF clear"), and LoneFloor no greater than TopFloor
// (a lone-candidate bar higher than the top-of-two bar would make the
// EASIER-to-satisfy gate the STRICTER one, backwards from what these two
// gates are for).
//
// CHAOS-3857 sol review F1 (P1): a partial override that zeroes exactly
// one field (e.g. only LoneFloor set to 0 by a malformed sweep cell, TopFloor/
// TopGap left at their calibrated defaults) is NOT caught by "is the whole
// struct the zero value" -- {0, 0.88, 0.12} is a DIFFERENT, live, and
// dangerous policy: LoneFloor=0 auto-commits every lone candidate
// regardless of confidence. Validate closes that gap by checking the
// ACTUAL resolved policy's field values, not merely whether the struct as
// a whole is zero.
//
// Called at BOTH layers, deliberately redundant (sol F1): the env-var
// boundary (falkorgraph.EmbedderFromEnv) calls this and REJECTS loudly --
// composition fails at startup, so a broken sweep cell is caught
// immediately, before a single investigation runs under it. This
// function's OWN caller, ResolveFromMergedCandidatesWithGate, calls it
// again and, having no error return of its own, instead makes the
// confidence-threshold commit decision and the CHAOS-3829 margin-rescue
// carve-out both evaluate to "commit nothing" when Validate fails -- fail
// CLOSED, never fail open, and never silently substitute
// DefaultCommitGatePolicy() in its place (a substitution would mask the
// very configuration mistake this exists to surface). The redundancy is
// intentional: ResolveFromMergedCandidatesWithGate is EXPORTED and callable
// directly (CHAOS-3857's own sweep tooling does exactly that), so safety
// cannot rest on the env boundary alone catching every path in.
func (g CommitGatePolicy) Validate() error {
	fields := []struct {
		name  string
		value float64
	}{{"LoneFloor", g.LoneFloor}, {"TopFloor", g.TopFloor}, {"TopGap", g.TopGap}}
	for _, f := range fields {
		if math.IsNaN(f.value) || math.IsInf(f.value, 0) || f.value <= 0 || f.value > 1 {
			return fmt.Errorf("commit gate policy: %s must be a finite number greater than 0 and at most 1, got %v", f.name, f.value)
		}
	}
	if g.TopGap >= g.TopFloor {
		return fmt.Errorf("commit gate policy: TopGap (%v) must be less than TopFloor (%v)", g.TopGap, g.TopFloor)
	}
	if g.LoneFloor > g.TopFloor {
		return fmt.Errorf("commit gate policy: LoneFloor (%v) must not exceed TopFloor (%v)", g.LoneFloor, g.TopFloor)
	}
	return nil
}

// isVectorOnlyCandidate reports whether candidate's ENTIRE recognized
// mechanism set is exactly {MatchVector} -- a single-mechanism,
// uncorroborated vector hit.
//
// AC-3778-3 (CHAOS-3778, ratified): "a vector hit alone never commits a
// subject." This has always held by ARITHMETIC alone -- falkorgraph's
// vector band ceiling (0.70) sits strictly below the lone-candidate gate
// (0.72), so no vector-only confidence can reach it. That is a coincidence
// of the two constants' values, not an enforced rule: nothing in this
// file's decision logic reads a candidate's mechanism set at all.
// CHAOS-3857's sweep ratified, then REJECTED, a lower LoneFloor (0.68,
// which sits below the vector ceiling and broke this exact arithmetic --
// see DefaultCommitGatePolicy's doc comment for the full record) --
// LoneFloor reverted to 0.72, so the arithmetic holds again today. This
// function makes the invariant STRUCTURAL anyway, deliberately not relying
// on that arithmetic remaining true: a candidate whose only recognized
// mechanism is MatchVector is excluded from both confidence-threshold gates
// below, by mechanism identity, regardless of what its own Confidence or
// the gate's own threshold values happen to be. A future band or policy
// change -- including another attempt at lowering LoneFloor -- can no
// longer silently reopen this hole.
//
// Scope, deliberately narrow -- read this before touching either call
// site:
//   - Applies to the two CommitGatePolicy gates only (the lone-candidate
//     case and the top-of-two case in the switch below). Those are the
//     ONLY paths AC-3778-3 was ever ratified against.
//   - Does NOT apply to the CHAOS-3829 vector-margin rescue
//     (vectorMarginCommit, below). That is a separate, independently
//     ratified commit path with its own precondition set -- in particular
//     vectorArmCorroborated requires BOTH MatchVector AND MatchLexical to
//     be present, which is structurally impossible for a candidate this
//     function calls vector-only (a single recognized mechanism). A
//     candidate the rescue commits is never vector-only in this sense, so
//     there is no case to guard there, and this function must not be
//     extended to that path -- doing so would conflate two independently
//     ratified invariants that happen to share the word "vector".
//   - Does NOT apply to the CHAOS-3810 exact-label-match override
//     (exactIndex, above). An exact match always carries MatchExact
//     alongside whatever mechanism originally surfaced the node
//     (NodeCandidate merges both), so it is never vector-only under this
//     definition -- no special case is needed to exclude it.
//   - A 2+-mechanism candidate (e.g. MatchVector+MatchTraversalParent) is
//     never vector-only either, and does not need this guard at all:
//     CorroboratedConfidence already lifts it to >= CorroboratedFloor
//     (0.72), comfortably above LoneFloor regardless of where LoneFloor
//     itself is set.
func isVectorOnlyCandidate(mechanisms []contextfabric.MatchMechanism) bool {
	merged := MergeMechanisms(mechanisms)
	return len(merged) == 1 && merged[0] == contextfabric.MatchVector
}

// ResolveFromMergedCandidates is ResolveFromMergedCandidatesWithGate
// (below) called with DefaultCommitGatePolicy() -- the calibrated
// production thresholds, which always pass Validate(). Every existing
// caller (production's one call site in resolve.go historically, and
// every graphrank test) keeps calling this exact function, unchanged, and
// gets byte-identical behavior: WithGate's new Validate() check (sol
// review F1) is a genuine addition to what THAT function does, but is a
// no-op FOR THIS CALLER specifically, because DefaultCommitGatePolicy()
// is -- and is asserted to remain -- always valid. See
// ResolveFromMergedCandidatesWithGate's doc comment for the full
// algorithm description.
func ResolveFromMergedCandidates(candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, max int, allowClarification bool, searchTruncated bool, vectorArmSimilarity map[string]float64, vectorMarginCommitThreshold float64, retrievalDegraded bool, effectiveSearchLimit int, calibratedTopK int, unscopedVisibility bool) contextfabric.SubjectResolution {
	return ResolveFromMergedCandidatesWithGate(candidatesBySubject, observationParentKey, observationBlocked, max, allowClarification, searchTruncated, vectorArmSimilarity, vectorMarginCommitThreshold, retrievalDegraded, effectiveSearchLimit, calibratedTopK, unscopedVisibility, DefaultCommitGatePolicy(), nil, nil, false, nil, "", "")
}

// ResolveFromMergedCandidatesWithGate implements the class fix for Codex
// round-3 findings "2" and "3": both were the same defect -- truncation ran
// BEFORE the semantic decision phases (parent-aware eligibility, commit
// priority), so whichever candidates truncation happened to keep could
// silently exclude the one the decision phase would have picked. This
// function runs in explicit phases with truncation LAST:
//
//  1. gather -- candidatesBySubject is already fully assembled by the
//     caller (hints, receipts, hybrid search, and traversal all merged).
//  2. parent resolution + eligibility -- already computed by the caller
//     (observationParentKey, observationBlocked).
//  3. commit decision, over the FULL untruncated candidate set: a
//     receipt-derived exact match (State=Committed, Confidence==1) always
//     wins outright; otherwise the parent-aware confidence heuristics
//     decide a single auto-committed subject, or ambiguity. A committed
//     subject is decided BEFORE truncation and can never be dropped by it.
//  4. truncation LAST: committed subject(s) first, then the canonical
//     parent of any retained observation, then everything else by
//     confidence/stable key.
//
// Ported unchanged from zepgraph.resolveFromMergedCandidates, except for
// searchTruncated (Codex round-3 review of the D11/AC-3778-0 fix -- see its
// doc comment below). Note this is a DIFFERENT truncation than phase 4's:
// phase 4 truncates the FINAL, already-decided candidate LIST down to max
// entries for the response; searchTruncated is about the INPUT candidate
// SET possibly being incomplete before phase 3's commit decision ever runs.
//
// CHAOS-3829 (chris-ratified, recorded 2026-08-15/16): vectorArmSimilarity
// and vectorMarginCommitThreshold feed an ADDITIVE commit-path carve-out,
// evaluated ONLY once the gates above have already decided ambiguous with
// nothing committed -- see the rescue block after the switch below, and
// vectorMarginCommit's own doc comment for the full precondition set and
// the soundness argument for why it may fire even when searchTruncated is
// true. retrievalDegraded (codex r4 J2, accepted) additionally gates the
// rescue -- see the rescue block's own comment for why. effectiveSearchLimit
// and calibratedTopK (codex r5 K1+K2, both accepted) together replace the
// original codex r1 F1 max>=2 test with a tighter, TWO-SIDED envelope on the
// REAL per-call returned-row bound -- see the rescue block's own comment for
// the full argument and why both P1s land as one unified condition rather
// than two independent ones. unscopedVisibility (codex r7 M1, accepted,
// SECURITY class) is a further, independent conjunct -- see the rescue
// block's own comment for the scope-existence-oracle hazard it closes.
//
// CHAOS-3857: the lone-candidate and top-of-two gates below both
// additionally exclude a vector-only candidate by mechanism identity, not
// merely by its confidence value -- see isVectorOnlyCandidate's own doc
// comment for why this guard exists (a sweep-ratified attempt to lower
// LoneFloor to 0.68 broke the arithmetic coincidence that used to enforce
// AC-3778-3; the guard makes the invariant structural instead, and ships
// even though LoneFloor's own value reverted, so a future lowering attempt
// is safe on the vector side by construction) and its precise scope (the
// CHAOS-3829 rescue below is deliberately untouched).
// identity/identityTerms/aliasIdentityComplete (CHAOS-3884) carry the
// keyed-identity-lookup collision-detection side channel resolve.go builds
// during merge: identity is the per-(key class, normalized term) claimant
// SET spanning every isAliasLookupScopedKind subject this resolution found
// (repository, project, team, work_item -- counting is broader than
// commit eligibility, HIGH-5); identityTerms is the per-subject list of
// which (class,term) pairs actually produced an identity mechanism for it
// (MEDIUM-B: uniqueness binds to the producing term, not any term);
// aliasIdentityComplete is true only when the identity reader's OWN
// completeness guarantee held for this resolution (its lookup succeeded
// AND no claimant it found was absent from the graph -- a graph-missing
// claimant folds into "not complete" here, at the SOURCE, rather than as a
// separate threaded flag: an incomplete identity view must disable the
// SAME fast path a truncated ordinary search would, for the same reason).
// nil/false from any caller that does not wire the identity reader --
// identityCollision reads a nil map exactly like an empty one, and
// aliasIdentityComplete=false disables not just the dedicated fast path
// but also LoneFloor/TopFloor for any identity-trust candidate specifically
// (identityTrustUnproven, chaos3884_identity.go -- codex xhigh review
// finding, CHAOS-3891, 2026-08-17: identityCollision alone cannot see a
// claimant an incomplete read never returned, so those strength gates
// needed the same completeness guard the fast path already had; design
// sign-off by reviewer-3884 the same day -- see identityTrustUnproven's own
// doc comment for the full argument), so every pre-CHAOS-3884 call site
// (ResolveFromMergedCandidates, every existing test) is still
// byte-identical -- aliasIdentityComplete=false there is the SAME nil/false
// default it always was, and identityTrustUnproven returns false
// unconditionally for a candidate with no identity mechanism at all.
// evidenceCensusAttestedKey (CHAOS-3896 Slice C, design brief v6 §1.4/R5)
// is SubjectKey(subject) for the ONE satisfier a source census named --
// empty for every caller that has not run a census (every pre-Slice-C call
// site, and every call ResolveFromMergedCandidates' own wrapper makes),
// which disables the rescue below entirely and leaves this function
// byte-identical to before this ticket. A non-empty value is meaningful
// ONLY when candidatesBySubject already contains that key: resolve.go
// (this package's one production caller) sets it exclusively on a SECOND,
// re-decision call made after it has already, itself: run the source
// census (RunShadowEvidenceRound's Attestation, Outcome==ShadowWouldCommit
// -- censusComplete, exactly one satisfier, no non-censused-kind survivor),
// proved that satisfier exists as a keyed GRAPH node (design brief's
// graph_missing_satisfier fail-closed pin -- absent node means this stays
// empty, never mints a phantom key), and merged the node into `candidates`
// through the SAME NodeCandidate/MergeCandidates path every ordinary
// search result uses. This function does no I/O of its own -- exactly the
// existing vectorArmSimilarity/identity precedent (both are also I/O-derived
// side channels a caller gathers BEFORE calling in) -- it only ever reads
// this key back out of a candidate set the caller already enriched.
//
// CHAOS-4085: this is the BASIS-DISCARDING wrapper, kept at its original
// signature so the ~30 existing call sites (all tests, plus
// ResolveFromMergedCandidates above) are untouched. Production goes
// through ResolveFromMergedCandidatesWithGateAndBasis, which returns the
// same resolution PLUS the per-committed-subject CommitBasisSet the engine
// needs. Discarding the basis here is safe by construction: an absent
// basis reads as CommitBasisUnknown, which IdentityProven reports false,
// which is the STRICT treatment -- see contextfabric.CommitBasis.
func ResolveFromMergedCandidatesWithGate(candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, max int, allowClarification bool, searchTruncated bool, vectorArmSimilarity map[string]float64, vectorMarginCommitThreshold float64, retrievalDegraded bool, effectiveSearchLimit int, calibratedTopK int, unscopedVisibility bool, gate CommitGatePolicy, identity identityClaimants, identityTerms identityMatchTerms, aliasIdentityComplete bool, tracer ResolutionTracer, requestID string, evidenceCensusAttestedKey string) contextfabric.SubjectResolution {
	resolution, _ := ResolveFromMergedCandidatesWithGateAndBasis(candidatesBySubject, observationParentKey, observationBlocked, max, allowClarification, searchTruncated, vectorArmSimilarity, vectorMarginCommitThreshold, retrievalDegraded, effectiveSearchLimit, calibratedTopK, unscopedVisibility, gate, identity, identityTerms, aliasIdentityComplete, tracer, requestID, evidenceCensusAttestedKey)
	return resolution
}

// ResolveFromMergedCandidatesWithGateAndBasis is
// ResolveFromMergedCandidatesWithGate's real implementation, additionally
// returning the CommitBasisSet: for every subject in the returned
// Committed list, WHICH CLASS OF PROOF the commit stood on, recorded at the
// point of commit where that path's own conjuncts are still in scope.
//
// The basis cannot be reconstructed by a caller from the returned
// resolution -- that is the whole reason it is returned rather than
// derived. A candidate carrying Confidence==1 and MatchAlias may have
// committed through identity_fast_path (complete enumeration, unique
// claimant within and across classes: proof) or through lone_floor with
// aliasIdentityComplete false (an unproven uniqueness claim): identical
// candidates, different standing. See contextfabric.CommitBasis for the
// full account.
func ResolveFromMergedCandidatesWithGateAndBasis(candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, max int, allowClarification bool, searchTruncated bool, vectorArmSimilarity map[string]float64, vectorMarginCommitThreshold float64, retrievalDegraded bool, effectiveSearchLimit int, calibratedTopK int, unscopedVisibility bool, gate CommitGatePolicy, identity identityClaimants, identityTerms identityMatchTerms, aliasIdentityComplete bool, tracer ResolutionTracer, requestID string, evidenceCensusAttestedKey string) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet) {
	bases := make(contextfabric.CommitBasisSet)
	candidates := make([]contextfabric.SubjectCandidate, 0, len(candidatesBySubject))
	for _, candidate := range candidatesBySubject {
		candidates = append(candidates, candidate)
	}
	// Phase 2.5 (CHAOS-3778): apply the corroborated band EXACTLY ONCE, here
	// -- after the caller has finished assembling the full candidate set, and
	// before both the ranking sort and the phase-3 commit decision that read
	// Confidence. Order matters twice over: applying it after the sort would
	// leave the ranking computed from stale base confidences, and applying it
	// incrementally during the merge would feed an already-corroborated value
	// back in as a base (see CorroboratedConfidence's doc comment).
	//
	// Every candidate goes through it, not just the multi-mechanism ones: the
	// function returns a single-mechanism base unchanged, so a uniform pass is
	// both simpler and impossible to forget for a new candidate source.
	//
	// rawBase (CHAOS-3896 Slice C, codex xhigh review finding, confirmed):
	// every candidate's OWN pre-corroboration Confidence, captured here by
	// subject key, BEFORE this loop overwrites it. The evidence_census
	// rescue below reads THIS map, never candidates[index].Confidence
	// directly -- a candidate that already carries 2+ REAL mechanisms
	// (independent of any census witness) gets corroborated to
	// >=CorroboratedFloor by THIS SAME loop, and evidence_census's own
	// evidenceStrength formula is only sound when applied to the TRUE raw,
	// single-mechanism-equivalent base ("raw-bases re-entry", design brief
	// §1.4) -- applying it to an ALREADY-corroborated value double-applies
	// the corroboration arithmetic and can push a candidate that should
	// have refused (raw base below LoneFloor under the brief's own formula)
	// over the floor instead. This is reachable in production even though
	// it never triggers on the shadow-measurement corpus (design brief §0:
	// every currently-stalled candidate there is single-mechanism) --
	// `searchTruncated` can short-circuit the switch below BEFORE the
	// lone_floor case ever inspects a genuinely multi-mechanism candidate's
	// confidence, leaving it reachable here with its raw base still
	// un-committed.
	rawBase := make(map[string]float64, len(candidates))
	for index := range candidates {
		base := candidates[index].Confidence
		rawBase[SubjectKey(candidates[index].Subject)] = base
		candidates[index].Confidence = CorroboratedConfidence(candidates[index].MatchMechanisms, base)
		if tracer != nil {
			// The identity-gate STORY (was FromKeyedIdentityLookup honored)
			// is NOT reconstructed here (team-lead ruling, 2026-08-17,
			// guardrail 6) -- it is emitted from WITHIN NodeCandidate
			// itself (candidate.go), where the real gate inputs are local
			// variables, as its own "identity_gate" stage event. This
			// corroboration event stays scoped to what genuinely belongs
			// at THIS post-merge point: the base-to-final confidence
			// transition and the distinct mechanism count.
			tracer.Trace(ResolutionTraceEvent{
				RequestID: requestID, Stage: "corroboration", Subject: candidates[index].Subject,
				BaseConfidence: base, FinalConfidence: candidates[index].Confidence,
				DistinctMechanisms: DistinctMechanismCount(candidates[index].MatchMechanisms),
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Confidence == candidates[j].Confidence {
			return SubjectKey(candidates[i].Subject) < SubjectKey(candidates[j].Subject)
		}
		return candidates[i].Confidence > candidates[j].Confidence
	})
	resolution := contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{}}
	if len(candidates) == 0 {
		resolution.Candidates = candidates
		if tracer != nil {
			tracer.Trace(ResolutionTraceEvent{RequestID: requestID, Stage: "decision", Outcome: "no_commit", SearchTruncated: searchTruncated})
		}
		return resolution, bases
	}

	// Phase 3: commit decision over the FULL, untruncated ranked set.
	committedIndex := make(map[int]bool)
	// commitGate (reviewer-3884 design review, 2026-08-17): a closed
	// vocabulary naming WHICH commit path actually fired --
	// "pre_committed_exact_hint" | "exact_index" | "identity_fast_path" |
	// "lone_floor" | "top_of_two" | "vector_margin_rescue" -- set once, at
	// the point of commit, in every branch below (including this loop and
	// the CHAOS-3829 rescue further down) that sets committedIndex/
	// resolution.Committed. Empty for a non-committed outcome.
	// WinningMechanism alone cannot answer "which GATE committed this" -- a
	// MatchAlias candidate can commit via identity_fast_path OR lone_floor,
	// both reporting the identical mechanism string -- and that distinction
	// is exactly what makes the identityTrustUnproven-affected population
	// (candidates the completeness fix blocks at lone_floor/top_of_two --
	// see identityTrustGateBlocked below) countable from traces instead of
	// merely inferred.
	commitGate := ""
	for index, candidate := range candidates {
		if candidate.State == contextfabric.ResolutionCommitted && candidate.Confidence == 1 {
			committedIndex[index] = true
			resolution.Committed = append(resolution.Committed, candidate.Subject)
			// pre_committed_exact_hint: a candidate that ARRIVED already
			// State==Committed (ExactHint's own keyed lookup, upstream of
			// this function entirely) -- distinct from "exact_index" below,
			// which is THIS function's own exact-label-match tier over
			// candidates that arrived Proposed.
			commitGate = "pre_committed_exact_hint"
			// CHAOS-4085: caller-canonical-id proof. The only producer of
			// this arrival state is resolve.go's SubjectHint branch, which
			// takes a canonical id the CALLER supplied, re-reads it from
			// this org's graph by keyed lookup (deps.ExactHint), and
			// re-authorizes it (AuthorizedAttributes + NodeCandidate)
			// before stamping Confidence=1/State=Committed. No ranking and
			// no truncation boundary participate.
			bases.Record(candidate.Subject, contextfabric.CommitBasisCallerCanonicalID)
		}
	}
	ambiguous := false
	// identityTrustGateBlocked (codex xhigh review finding, 2026-08-17,
	// team-lead-ratified "close it now"): set inside the commitIndex/
	// identityIndex scope below, once, to whether the top-ranked
	// commit-eligible candidate (candidates[commitIndex[0]] -- the SAME
	// candidate both the LoneFloor and TopFloor cases evaluate,
	// commitIndex being confidence-sorted) was refused the ordinary
	// strength gates specifically because identityTrustUnproven fired for
	// it, independent of whichever switch branch actually ran. Declared
	// here (outside the if len(committedIndex)==0 block commitIndex/
	// identityIndex are scoped to) so it survives to the decision-stage
	// trace emission at the bottom of this function, purely for
	// OBSERVABILITY -- it never feeds back into any commit decision
	// itself.
	identityTrustGateBlocked := false
	// tiedStatisticalTop (CHAOS-4085 observability, team-lead addition
	// 2026-08-22): the TIE half of tiedStatisticalTopUnderTruncation's
	// conjunct, captured for the decision-stage trace independently of
	// whether the refusal fired -- see ResolutionTraceEvent.TiedStatisticalTop
	// for why the tie and the truncation are emitted as two fields rather
	// than one "refused" boolean. Assigned inside the commit block below,
	// where commitIndex exists; stays false for a resolution that committed
	// on arrival (the pre-committed hint fast path never computes one).
	tiedStatisticalTop := false
	// CHAOS-3857 sol review F1: computed ONCE, gates BOTH the
	// confidence-threshold decision below (the two commitIndex cases in
	// the switch) and the CHAOS-3829 margin-rescue block further down --
	// an invalid gate must disable both, since the rescue is itself an
	// ALTERNATE commit path for the exact ambiguity the confidence
	// thresholds would otherwise decide. The exact-label-match and
	// searchTruncated cases below are DELIBERATELY unaffected: neither
	// reads gate at all, so an operator's broken sweep cell cannot make a
	// caller-verified exact match stop committing.
	gateValid := gate.Validate() == nil
	if len(committedIndex) == 0 {
		// Observation-kind subjects (documents, episodes) are auto-commit-
		// eligible via the confidence heuristics below only when traversal
		// confirmed they have no canonical parent and did not merely fail
		// to determine one (P1-1). When a parent candidate DOES exist (or
		// traversal errored), the observation stays excluded so the parent
		// -- necessarily lower-confidence, being one hop removed -- still
		// gets the chance to compete on its own terms, rather than the
		// (higher-relevance-by-construction, or simply unverifiable)
		// observation always outranking it by raw score alone.
		commitIndex := make([]int, 0, len(candidates))
		for index, candidate := range candidates {
			if IsObservationSubjectKind(candidate.Subject.Kind) && observationBlocked[SubjectKey(candidate.Subject)] {
				continue
			}
			commitIndex = append(commitIndex, index)
		}
		// CHAOS-3810: the exact label/name match override, evaluated BEFORE
		// the truncation branch below.
		//
		// NodeCandidate documents that an exact match sets Confidence to 1.0
		// "regardless of Relevance". That statement was false in practice for
		// every real corpus: falkorgraph's fulltextSearchNodes caps every
		// candidate at fulltextRelevanceFloor as soon as the result set
		// truncates, and a 20k+ subject graph with MaxSubjectCandidates=10
		// truncates on essentially every search -- so the searchTruncated
		// branch ran first and forced the whole resolution ambiguous, override
		// or not. Nothing auto-committed, ever, including a candidate whose
		// label was character-for-character the subject term.
		//
		// Why an exact match is allowed to outrank truncation, when a merely
		// high-scoring one is not: truncation says "a competing candidate may
		// have been dropped before this function ever saw it", and that is a
		// real hazard for a RELEVANCE SCORE, which only ranks candidates
		// against each other. String equality is not a ranking -- the term IS
		// this subject's label. The only dropped row that could genuinely
		// compete is one carrying the IDENTICAL label, and that case is
		// handled honestly rather than assumed away: exactness is required to
		// be UNIQUE among eligible candidates (len(exactIndex) == 1), so a
		// second same-label subject in the retained set falls straight through
		// to ambiguity. A duplicate label hidden entirely behind the
		// truncation boundary remains the residual risk, and it is
		// unresolvable by label under any rule -- a caller who means that
		// subject must name it by canonical ID (SubjectHint), which takes the
		// truncation-immune ExactHint path instead.
		//
		// Confidence == 1 is required alongside the mechanism so this can only
		// ever read the override's own value: CorroboratedConfidence returns a
		// base of 1 unchanged precisely so being found a second way never
		// costs an exact match its certainty.
		exactIndex := make([]int, 0, len(commitIndex))
		for _, index := range commitIndex {
			if candidates[index].Confidence == 1 && HasMechanism(candidates[index].MatchMechanisms, contextfabric.MatchExact) {
				exactIndex = append(exactIndex, index)
			}
		}
		// identityIndex (CHAOS-3884): the RAW, unconditional-on-uniqueness
		// eligible-kind alias/provider-key population -- isAliasIdentityEligibleKind,
		// Confidence==1 (only ever true via NodeCandidate's identityTrusted
		// bump, which itself requires FromKeyedIdentityLookup), MatchAlias
		// or MatchProviderKey. Deliberately NOT pre-filtered by
		// identityCollision here (a v3 mistake, corrected): filtering
		// membership by uniqueness made a duplicate-claimant tie
		// UNDETECTABLE by len(identityIndex) (two colliding candidates would
		// each independently fail their own uniqueness check and never
		// enter the slice, reading as 0 rather than 2) -- exactly the class
		// of bug len(exactIndex) itself avoids by staying a RAW count.
		// Uniqueness is a SEPARATE check (identityCollision), applied at
		// the point of commit below, not baked into membership.
		identityIndex := make([]int, 0, len(commitIndex))
		for _, index := range commitIndex {
			candidate := candidates[index]
			if candidate.Confidence != 1 || !isAliasIdentityEligibleKind(candidate.Subject.Kind) {
				continue
			}
			if HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) || HasMechanism(candidate.MatchMechanisms, contextfabric.MatchProviderKey) {
				identityIndex = append(identityIndex, index)
			}
		}
		if len(commitIndex) > 0 {
			identityTrustGateBlocked = identityTrustUnproven(candidates[commitIndex[0]], aliasIdentityComplete)
		}
		// Reuses tiedStatisticalTopUnderTruncation's own tie test with
		// searchTruncated forced TRUE, so the trace flag and the refusal
		// rule can never disagree about what "tied" means -- one definition,
		// two readers.
		tiedStatisticalTop = tiedStatisticalTopUnderTruncation(candidates, commitIndex, true)
		switch {
		// CHAOS-3917: exact alias != canonical identity -- an exact-label
		// match alone must never suffice to commit when the identical
		// literal term is ALSO claimed, by a DIFFERENT canonical subject,
		// via the alias or provider-key identity class (case 45's own
		// shape: a project's exact label and two repositories' aliases, all
		// on the same term). identityCrossClassRivalClaimant is the new,
		// unified claimant-uniqueness proof (chaos3917_identity_unification.go)
		// -- a candidate with no rival recorded (the overwhelming common
		// case, and every pre-existing exactIndex test) is completely
		// unaffected, byte-identical to before this ticket.
		case len(exactIndex) == 1 && !identityCrossClassRivalClaimant(SubjectKey(candidates[exactIndex[0]].Subject), identity, identityTerms):
			committedIndex[exactIndex[0]] = true
			candidates[exactIndex[0]].State = contextfabric.ResolutionCommitted
			resolution.Committed = []contextfabric.SubjectRef{candidates[exactIndex[0]].Subject}
			commitGate = "exact_index"
			// CHAOS-4085 (sol@xhigh change 2): STATISTICAL, deliberately,
			// even though this tier stamps MatchExact and Confidence==1.
			// This is LABEL equality, not identity: this branch's own doc
			// comment above concedes the residual risk it cannot close ("a
			// duplicate label hidden entirely behind the truncation
			// boundary"), and the uniqueness it does check (len(exactIndex)
			// == 1) is uniqueness within the RETAINED set, not within the
			// corpus. A caller who means a specific subject under that
			// hazard is told to name it by canonical id -- which is
			// CommitBasisCallerCanonicalID, a different, genuinely proven
			// basis. Nothing about the exact-label tier's own commit
			// behavior changes here; only its standing before CHAOS-4085's
			// affirmation gate does.
			bases.Record(candidates[exactIndex[0]].Subject, contextfabric.CommitBasisStatistical)
		// CHAOS-3884: the identity fast path. Sits AFTER exactIndex
		// (Finding 1's precedence: a candidate's own canonical label is a
		// stronger identity claim than a derived alias/provider-key handle,
		// so an exact-label winner is never second-guessed by an alias
		// tie), BEFORE searchTruncated (the SAME "term equality against the
		// subject's own identity data survives ordinary search truncation"
		// argument CHAOS-3810 already established for exactIndex, extended
		// here because aliasIdentityComplete -- unlike searchTruncated --
		// is a COMPLETE, keyed guarantee, not a ranked/truncatable one).
		// identityCollision (MEDIUM-B/C) is the uniqueness check membership
		// itself no longer performs. identityCrossClassRivalClaimant
		// (CHAOS-3917) is this same fast path's own LABEL-class rival check
		// -- Finding 1's precedence note above is now mutual: an exact-label
		// winner no longer second-guesses an alias tie, and an alias winner
		// no longer second-guesses an exact-label tie either.
		case aliasIdentityComplete && len(identityIndex) == 1 &&
			!identityCollision(SubjectKey(candidates[identityIndex[0]].Subject), identity, identityTerms) &&
			!identityCrossClassRivalClaimant(SubjectKey(candidates[identityIndex[0]].Subject), identity, identityTerms):
			committedIndex[identityIndex[0]] = true
			candidates[identityIndex[0]].State = contextfabric.ResolutionCommitted
			resolution.Committed = []contextfabric.SubjectRef{candidates[identityIndex[0]].Subject}
			commitGate = "identity_fast_path"
			// CHAOS-4085: the ONE branch that earns
			// CommitBasisAuthoritativeIdentity, and it earns it from THIS
			// case's own guard expression rather than from anything visible
			// on the candidate -- aliasIdentityComplete (the identity
			// universe was enumerated completely, so uniqueness is proven
			// rather than an artifact of a truncated read), len(identityIndex)
			// == 1 and !identityCollision (unique within the class), and
			// !identityCrossClassRivalClaimant (unique across classes,
			// CHAOS-3917). Existence and authorization come for free: the
			// candidate is here because a keyed read of this org's graph
			// returned it and NodeCandidate's authorization filter kept it.
			// Drop ANY of those conjuncts and this same candidate would
			// fall through to lone_floor/top_of_two and be recorded
			// statistical below -- which is exactly the distinction the
			// mechanism set alone cannot express.
			bases.Record(candidates[identityIndex[0]].Subject, contextfabric.CommitBasisAuthoritativeIdentity)
		case searchTruncated:
			// Codex round-3 review of D11/AC-3778-0: truncation is a
			// property of the RESOLUTION, not of any one candidate's
			// score, and it must be checked BEFORE any confidence
			// threshold, not after -- a candidate reaching this branch at
			// all (as opposed to the hard State==Committed fast path
			// above, reachable only via ExactHint's keyed,
			// truncation-immune lookup) can never be trusted to auto-commit
			// when the search that produced it (or any sibling search in
			// this same resolution) may have had a genuinely competing
			// candidate dropped before it ever reached this function. This
			// closes two escape paths a per-candidate confidence cap alone
			// left open: (a) NodeCandidate's exact label/name match
			// override forces Confidence to 1.0 regardless of the
			// backend's own (possibly truncation-capped) Relevance, and
			// (b) the candidatesBySubject merge in ResolveSubjects keeps
			// whichever of two same-subject entries has the HIGHER
			// confidence, so an untruncated call's full-strength entry for
			// a subject can silently overwrite a truncated call's
			// deliberately-demoted one for that SAME subject -- neither
			// case is visible from a single candidate's own Confidence
			// value, which is exactly why this has to be an independent,
			// resolution-wide signal instead.
			ambiguous = true
		// identityTrustUnproven applied here too (codex xhigh review finding,
		// 2026-08-17, team-lead-ratified "close it now"): identityCollision
		// alone cannot see a claimant an INCOMPLETE identity-universe read
		// never returned, so a truncated-read identity-trust candidate could
		// otherwise clear LoneFloor on an unproven uniqueness claim -- see
		// identityTrustUnproven's own doc comment (chaos3884_identity.go)
		// for the full gap this closes. A candidate this ticket never
		// touches (no identity mechanism, or confidence==1 via exact-label
		// match instead) is entirely unaffected, mirroring identityCollision's
		// own untouched-candidate guarantee immediately to its left.
		// identityCrossClassRivalClaimant (CHAOS-3917) applied here too --
		// the SAME unified claimant-uniqueness proof exactIndex/identityIndex
		// now require, applied uniformly to LoneFloor so a label/alias rival
		// pair cannot slip through this gate merely because neither
		// individually reached its own dedicated fast path.
		case len(commitIndex) == 1 && gateValid && !isVectorOnlyCandidate(candidates[commitIndex[0]].MatchMechanisms) && !identityCollision(SubjectKey(candidates[commitIndex[0]].Subject), identity, identityTerms) && !identityCrossClassRivalClaimant(SubjectKey(candidates[commitIndex[0]].Subject), identity, identityTerms) && !identityTrustUnproven(candidates[commitIndex[0]], aliasIdentityComplete) && candidates[commitIndex[0]].Confidence >= gate.LoneFloor:
			committedIndex[commitIndex[0]] = true
			candidates[commitIndex[0]].State = contextfabric.ResolutionCommitted
			resolution.Committed = []contextfabric.SubjectRef{candidates[commitIndex[0]].Subject}
			commitGate = "lone_floor"
			// CHAOS-4085: statistical. A confidence floor is a score
			// comparison against a retrieved population, including for a
			// Confidence==1 alias candidate that reached here BECAUSE
			// aliasIdentityComplete was false -- the identity fast path
			// above declined it for want of a completeness proof, so this
			// gate must not launder it back into one.
			bases.Record(candidates[commitIndex[0]].Subject, contextfabric.CommitBasisStatistical)
		case len(commitIndex) >= 2 && gateValid:
			top, second := candidates[commitIndex[0]], candidates[commitIndex[1]]
			// CHAOS-3884 spot-check item 1: identityCollision applied here
			// too, not just the fast path above. Without this, a colliding
			// identity candidate's confidence=1 bump (earned via an
			// UNPROVEN-unique claim) manufactures a 1.0-vs-(whatever
			// second scores) gap that can trivially clear TopFloor/TopGap
			// on its own -- an existence signal (is this claim unique)
			// laundered through a STRENGTH gate that was never designed to
			// arbitrate identity ambiguity. A candidate with no identity
			// match terms at all (identityTerms[key] empty) is entirely
			// unaffected -- identityCollision returns false for it
			// unconditionally, so this never suppresses a legitimate
			// ordinary lexical/vector top-of-two commit.
			//
			// identityTrustUnproven applied here too (codex xhigh review
			// finding, 2026-08-17, team-lead-ratified "close it now"): the
			// SAME truncated-read gap the comment above closes for
			// identityCollision applies to TopFloor's 1.0-vs-second gap
			// independently of it -- see identityTrustUnproven's own doc
			// comment (chaos3884_identity.go).
			// identityCrossClassRivalClaimant (CHAOS-3917) applied here too, same
			// rationale as LoneFloor above.
			if gap := top.Confidence - second.Confidence; !isVectorOnlyCandidate(top.MatchMechanisms) && !identityCollision(SubjectKey(top.Subject), identity, identityTerms) && !identityCrossClassRivalClaimant(SubjectKey(top.Subject), identity, identityTerms) && !identityTrustUnproven(top, aliasIdentityComplete) && top.Confidence >= gate.TopFloor && gap >= gate.TopGap {
				committedIndex[commitIndex[0]] = true
				candidates[commitIndex[0]].State = contextfabric.ResolutionCommitted
				resolution.Committed = []contextfabric.SubjectRef{candidates[commitIndex[0]].Subject}
				commitGate = "top_of_two"
				// CHAOS-4085: statistical -- a gap between two scores.
				bases.Record(candidates[commitIndex[0]].Subject, contextfabric.CommitBasisStatistical)
			} else {
				ambiguous = true
			}
		default:
			ambiguous = true
		}
		// CHAOS-3829: the additive commit-path carve-out, checked ONLY as
		// a RESCUE once every gate above has already run to completion
		// and decided ambiguous with nothing committed -- every branch
		// above (the exact-label override, searchTruncated, the
		// lone-candidate gate, the top-of-two gate) is untouched, in both
		// code and behavior, by what follows. See vectorMarginCommit's
		// doc comment for the full precondition set this reads
		// (corroboration + a measurable, sufficiently large vector-arm
		// top-1/top-2 similarity margin) and why it may fire even when
		// searchTruncated is the reason ambiguous is true.
		//
		// codex r1 F1 (accepted, narrowed; SUPERSEDED in shape, not in
		// substance, by codex r5 K1+K2 below): the LOWER half of the
		// envelope. At an effective per-call returned-row bound >=2, the
		// merged cross-call top-2 argument is conservative -- any candidate
		// this resolution never even SAW (beyond every individual Search
		// call's own returned set, as opposed to F0's "returned but
		// NodeCandidate-rejected" case, which vectorArmSimilarity already
		// covers) has similarity <= that call's own Kth-ranked
		// (least-similar) returned row, which is in turn <= the merged,
		// cross-call vectorArmSimilarity's own second entry -- so a truly
		// UNSEEN candidate can never be closer than the competitor this
		// function already found. At a bound of 1, a Search call returns AT
		// MOST one row per term, so there is no such bound at all: an unseen
		// second-place candidate could have any similarity whatsoever, and
		// the "competitor" this function finds (if any, from a DIFFERENT
		// term's own single result) carries no guarantee of being the TRUE
		// nearest one. Fail closed rather than trust a margin with no
		// completeness bound behind it.
		//
		// codex r5 K2 (accepted, P1): F1's ORIGINAL test read max, i.e.
		// request.Options.MaxSubjectCandidates -- the NOMINAL limit a caller
		// asked for, not the row count a Search call can actually return.
		// falkorgraph's own per-call cap (ACR_CONTEXT_FABRIC_FALKOR_MAX_RESULTS,
		// a.config.MaxResults) independently clamps every fulltext/vector
		// call to min(requested limit, that cap) BELOW resolve.go -- a
		// deployment with cap=1 and a request max>=2 passed F1's own test
		// (max>=2) while every Search call this resolution actually made
		// returned at most ONE row, silently breaking the "true #2 is
		// returned at any bound>=2" proof F1's own comment above relies on.
		// effectiveSearchLimit is resolve.go's ResolveSubjects computing that
		// SAME clamp itself (mirroring fulltextSearchNodesForResolution/
		// vectorSearchNodesWithOverFetch's own "if limit<=0 || limit>cap {
		// limit=cap}" idiom) and handing the REAL bound in here, so this
		// function never has to trust the nominal request value again.
		//
		// codex r5 K1 (accepted, P1): the UPPER half of the envelope, a
		// DIFFERENT hazard from F1/K2's lower half -- this one is about
		// CORROBORATION width, not the vector-arm margin's own completeness.
		// CalibrateMarginFromReport's oracle measured corroboration (whether
		// a subject's vector-arm top-1 was ALSO found by the lexical arm,
		// vectorArmCorroborated below) at fulltextSearchNodesForResolution's
		// own report-pinned TopK (MarginCalibrationOptions.TargetTopK=20,
		// tau_calibration.go's F7 pin) -- a subject sitting at lexical rank
		// 21-50 was OUTSIDE that measurement and excluded from the eligible
		// (wrong-top1 or correct-top1) population M was computed from. At
		// runtime, hybridSearchNodes passes the SAME limit to its lexical
		// call as its vector call (vector.go), so a deployment whose
		// effective per-call bound exceeds 20 can corroborate a wrong top-1
		// at that wider rank that calibration never saw -- an UNMEASURED
		// population, the identical class of hazard F7/H2/H3 already guard
		// on the calibration side, now closed on the RUNTIME side too.
		// calibratedTopK carries RetrievalPolicy.CalibratedTopK (pinned 20
		// for this identity, retrieval_policy.go) through the SAME
		// EmbedderFromEnv/attachEmbedder/ResolveDeps seam M itself already
		// uses, gated together with M (both zero/disabled for any identity
		// without a calibrated entry, or whenever the floor-override guard
		// -- G3/H1(a) -- disables M).
		//
		// UNIFIED (codex r5's own instruction): K1 and K2 are ONE envelope
		// condition, not two independent ones -- effectiveSearchLimit must
		// sit in [2, calibratedTopK] inclusive. This also SUBSUMES F1's
		// original max>=2 test structurally: effectiveSearchLimit is always
		// <= max (it can only ever be the SAME value or a narrower clamp of
		// it), so effectiveSearchLimit>=2 already implies max>=2 -- there is
		// nothing left for a separate max>=2 check to add.
		//
		// codex r2 G2 (accepted, ratified-invariant violation): a
		// duplicate exact-label collision (len(exactIndex) >= 2) must NOT
		// reach the rescue, regardless of what caused ambiguous=true above
		// (searchTruncated, or the top-of-two test on two tied
		// Confidence==1 exact matches -- both leave ambiguous=true here).
		// CHAOS-3810's own doc comment on the exact-label override rules
		// this irreducibly ambiguous by design: two subjects share the
		// IDENTICAL label, and only the caller (via a canonical SubjectHint,
		// the truncation-immune ExactHint path) can say which one was
		// meant. A vector-similarity margin between two SAME-LABELED
		// subjects answers a different question entirely -- "which is
		// closer in embedding space" -- and says NOTHING about which one
		// the caller's literal, exact-matching term actually referred to;
		// picking the higher-margin one would silently override a
		// deliberate, ratified design decision with an unrelated signal.
		//
		// codex r4 J2 (accepted, narrowed): the ERR arm of the reviewed
		// claim was impossible (ResolveSubjects returns immediately on any
		// per-term Search error, before this function is ever reached --
		// no partial-map-after-error state exists), but the DEGRADED arm
		// is real: a successful call whose vector arm degraded (e.g. an
		// embedder failure that still falls back to a lexical-only result,
		// ResolveDeps.Search's own degraded return) leaves
		// vectorArmSimilarity missing that term's competitors, the SAME
		// incompleteness class F0/F1 already guard against for a
		// truncated/too-small-K search -- except here the calibration
		// harness (oracle_live_test.go's live driver) HARD-FAILS on any
		// degradation rather than measuring it, so a degraded resolution
		// was NEVER part of the population M was calibrated against, at
		// any margin. retrievalDegraded gates the rescue accordingly --
		// conservative scope, deliberately: ANY retrieval degradation
		// anywhere in this resolution (not just on the term that would
		// have produced the missing competitor) disables the carve-out,
		// which is the cleanest statement of "calibration measured only
		// CLEAN resolutions" available without threading per-term
		// degradation state through the merge. The EXISTING confidence
		// gates above (the lone-candidate gate, the top-of-two gate, the
		// exact-label override) are entirely untouched by this -- they already had
		// their own, independent relationship to degradation before
		// CHAOS-3829 and this ticket does not change it.
		//
		// codex r7 M1 (accepted, SECURITY class -- AGENTS.md scope
		// invariant): a scoped principal (principal.RepositoryScopes) or a
		// scope-narrowed request (RequestedScope.RepositorySlugs/ProjectIDs/
		// TeamIDs) can make this rescue an EXISTENCE ORACLE for a subject
		// the caller is not authorized to see. F0's side-map is deliberately
		// PRE-AUTHORIZATION (mergeSearchResults records vectorArmSimilarity
		// BEFORE NodeCandidate's own accept/reject decision) -- that is
		// correct for measuring the margin honestly, but it means a subject
		// outside the caller's scope can still act as COMPETITOR here: a
		// hidden close hit lowers the visible winner's margin, and margin <
		// threshold flips the resolution from committed to ambiguous
		// (clarification) -- OBSERVABLE, scope-dependent behavior that
		// leaks "something you cannot see is close by", exactly the kind of
		// inference AGENTS.md's scope invariant forbids.
		//
		// THREE candidate fixes were considered and rejected, each trading
		// this hazard for a different one -- stated here so none of the
		// three re-cycles as "obviously simpler":
		//   - COUNT the out-of-scope competitor's margin impact but strip
		//     its IDENTITY from any caller-visible output: still an
		//     existence oracle -- ambiguous-vs-committed is itself the leak,
		//     independent of what identifying detail accompanies it.
		//   - FILTER the side-map to only scope-authorized hits before
		//     vectorMarginCommit ever runs: this is F0's own inflation
		//     hazard AGAIN, just re-derived per request instead of globally
		//     -- a margin computed against a narrower, filtered population
		//     is inflated exactly on the cases where a genuinely close (but
		//     filtered) competitor exists, the SAME failure F0 already
		//     fixed for the unscoped case.
		//   - QUERY-FILTER the underlying Search calls themselves to the
		//     caller's scope (so the side-map is honestly narrower because
		//     the SEARCH was narrower, not because of a later filter step):
		//     this changes the geometry M was calibrated against -- the
		//     oracle harness measured ORG-WIDE, unscoped retrieval; a
		//     scoped principal's WITHIN-SCOPE similarity distribution is an
		//     uncalibrated population M was never validated against, and
		//     could be tighter or looser in either direction.
		//
		// RESOLUTION: unscopedVisibility makes the rescue a CONSTANT-OFF for
		// every scoped principal/request, not merely narrower. Rescue-off
		// has NO observable dependence on hidden subjects at all -- a
		// scoped caller sees "ambiguous" (or whatever the EXISTING gates
		// above already decided) regardless of what does or does not exist
		// outside their scope, so there is nothing left to infer. The
		// unscoped population this leaves eligible is EXACTLY the
		// org-wide, no-scope geometry CalibrateMarginFromReport's oracle
		// measured (the harness runs with no principal/request scope
		// narrowing at all) -- so this is not a narrower-but-still-uncalibrated
		// carve-out, it is the SAME calibrated population with the
		// uncalibrated (scoped) one now excluded entirely.
		//
		// NO REMAINING LEAK PATH: F0's side-map itself is UNCHANGED (still
		// pre-authorization, still built the same way) -- it is only ever
		// CONSUMED by this rescue, and the rescue is now constant-off
		// whenever that consumption could be scope-observable. Nothing
		// downstream of this function reads vectorArmSimilarity directly
		// (see ResolveSubjects' own doc comment on the map's scope), so
		// closing the ONE consumer closes the entire path.
		//
		// codex r8 O1 (CRITICAL, accepted -- caller computes
		// unscopedVisibility via scopesUnrestricted, resolve.go): the
		// ORIGINAL "unscoped" test (an EMPTY RepositoryScopes list) was
		// UNREACHABLE for any real authenticated credential -- every
		// production credential is issued with at least one scope, and an
		// org-wide one is issued as the wildcard ["*"], never []. The
		// caller now also treats a wildcard-scoped principal as
		// unrestricted (ScopeMatch's own definition: "*" matches
		// unconditionally, before the node's own attribute is even
		// consulted), so the rescue is REACHABLE by the credential shape
		// production actually issues, closing what would otherwise have
		// been a permanently-dead code path.
		// gateValid (CHAOS-3857 sol review F1) is a further, independent
		// conjunct: the rescue is an ALTERNATE commit path for the exact
		// ambiguity an invalid gate must leave fully unresolved (commit
		// nothing), so it must be disabled by the SAME invalidity that
		// disables the confidence-threshold cases above, not just left to
		// fire because those cases individually declined to commit.
		// CHAOS-4085 (sol@xhigh change 3, team-lead ratified 2026-08-22):
		// tiedStatisticalTopUnderTruncation is a NEW, independent conjunct
		// that removes one demonstrated-unsafe population from this rescue
		// entirely -- a TRUNCATED search whose top two commit-eligible
		// candidates carry the SAME confidence.
		//
		// The v9 trial (tag 20260822T091538Z) produced BOTH outcomes from
		// exactly this shape, in the same run, against the same corpus: a
		// never-commit control committed a wrong subject out of a three-way
		// tie at 0.76375, and a different case committed the RIGHT subject
		// out of a three-way tie at 0.755. The two resolutions are
		// indistinguishable at this layer -- same tie structure, same
		// mechanisms, same truncation, same rescue. That is the finding:
		// when a relevance ranking has already declared "these are equally
		// good" AND the search admits it may not have returned the true
		// competitor, an embedding-similarity margin is arbitrating a
		// question the evidence does not answer. Under DP9 (zero wrong
		// commits) the correct sibling is the recall price, not a licence
		// to keep the class.
		//
		// Deliberately NOT rehabilitated downstream. CHAOS-4085's
		// post-synthesis affirmation gate reads MODEL OUTPUT, and model
		// output is CORRELATED with the same lexical/embedding proximity
		// that produced the tie in the first place -- a synthesis handed a
		// wrong-but-similar subject can and does write plausible supporting
		// prose about it. Affirmation may therefore only ever VETO a
		// commit, never restore one this gate refused; that is why this
		// conjunct lives here, before the commit, rather than as another
		// clause in the reducer.
		//
		// Narrow by construction: an UNTRUNCATED search reaching a tie is
		// untouched (the population was complete, so the tie is real
		// information), and a truncated search with a strictly-separated
		// top is untouched (the ranking did discriminate). Only the
		// conjunction is refused.
		if ambiguous && gateValid && vectorMarginCommitThreshold > 0 && len(exactIndex) < 2 && !retrievalDegraded &&
			calibratedTopK > 0 && effectiveSearchLimit >= 2 && effectiveSearchLimit <= calibratedTopK &&
			unscopedVisibility && !tiedStatisticalTopUnderTruncation(candidates, commitIndex, searchTruncated) {
			// CHAOS-3884 spot-check MEDIUM-C/item 1: identityCollision is
			// checked on the RESCUE'S OWN chosen candidate, not via
			// len(identityIndex) (eligibility-scoped, invisible to a
			// non-eligible-kind collision -- e.g. two teams, or a team and
			// a repository, colliding on the same alias would leave
			// identityIndex empty since neither/only-one is eligible, yet
			// the repository candidate the rescue is about to pick could
			// still be the disputed claimant). Mirrors the SAME G2
			// rationale len(exactIndex)<2 already applies to the exact-label
			// class, extended to the alias class: a duplicate identity
			// match is irreducibly ambiguous, and an unrelated
			// embedding-proximity signal must not arbitrate it. A
			// candidate with no identity match terms at all is unaffected
			// (identityCollision returns false), so this never touches the
			// rescue's pre-existing, unrelated population.
			//
			// Deliberately NOT also gated on !identityTrustUnproven(...,
			// aliasIdentityComplete) the way LoneFloor/TopFloor are
			// (reviewer-3884 design review, 2026-08-17, confirmed correct,
			// not an oversight): identityCollision applies here because a
			// KNOWN rival makes embedding proximity the wrong arbiter (the
			// SAME G2 rationale the comment above already states) --
			// completeness does not, because vectorMarginCommit picks its
			// candidate on raw vector-arm similarity alone, never on
			// Confidence, so a bump-derived 1.0 never feeds its margin in
			// the first place, and the rescue's own ratified geometry
			// already tolerates an incomplete population (it may fire even
			// when searchTruncated -- itself a form of incompleteness -- is
			// the reason ambiguous is true). Adding the conjunct here would
			// narrow a separately-ratified path on a premise it never
			// rested on.
			// identityCrossClassRivalClaimant (CHAOS-3917) applied here too, same
			// rationale identityCollision's own comment just above already gives.
			if index, ok := vectorMarginCommit(candidates, commitIndex, vectorArmSimilarity, vectorMarginCommitThreshold); ok && !identityCollision(SubjectKey(candidates[index].Subject), identity, identityTerms) && !identityCrossClassRivalClaimant(SubjectKey(candidates[index].Subject), identity, identityTerms) {
				committedIndex[index] = true
				candidates[index].State = contextfabric.ResolutionCommitted
				resolution.Committed = []contextfabric.SubjectRef{candidates[index].Subject}
				ambiguous = false
				commitGate = "vector_margin_rescue"
				// CHAOS-4085: statistical -- an embedding-similarity margin
				// is the definitive score comparison.
				bases.Record(candidates[index].Subject, contextfabric.CommitBasisStatistical)
			}
		}
		// CHAOS-3896 Slice C (design brief v6 §1.4, R5's amendment): the
		// evidence_census commit path -- a FURTHER alternate commit path for
		// the SAME ambiguity, fired only when the confidence-threshold gates
		// above AND the CHAOS-3829 rescue above have both already run to
		// completion and left `ambiguous` true. evidenceCensusAttestedKey=""
		// (every existing caller) makes this entire block dead, at zero
		// cost, exactly like every other optional rescue precondition this
		// function already gates on.
		//
		// gateValid: the SAME conjunct lone_floor/top_of_two/vector_margin_rescue
		// already require -- an invalid gate must disable every commit path,
		// this one included (resolution.go's own precedent, extended
		// verbatim per the brief's own text: "gateValid joins the conjunct
		// exactly as lone_floor/top_of_two and the rescue already require").
		//
		// len(exactIndex) < 2: the SAME G2 rationale vector_margin_rescue's
		// own comment states in full -- a duplicate exact-label collision is
		// irreducibly ambiguous by CHAOS-3810's own design, and a census
		// witness answers a different question ("does source data attest a
		// satisfier") than which of two identically-labeled subjects a
		// caller's literal term meant; it must not arbitrate that collision.
		//
		// isVectorOnlyCandidate: AC-3778-3's pin, RETAINED per the brief's
		// own text ("non-contradiction never commits a vector-only base
		// (pin retained)") -- the census witness corroborates whatever
		// mechanism the attested candidate already carries, but a
		// vector-only candidate must stay excluded from BOTH confidence
		// gates AND this rescue, by mechanism identity, exactly like the
		// two gates above.
		//
		// identityCollision: unchanged from every other commit path above --
		// a known rival claimant on the SAME (key class, term) makes even a
		// positive keyed witness the wrong arbiter of WHICH claimant the
		// caller meant. identityCrossClassRivalClaimant (CHAOS-3917, codex
		// xhigh review finding, confirmed) applied here too: without it,
		// evidence_census was an unguarded SIXTH fast path -- a candidate
		// the first pass correctly left ambiguous because a cross-class
		// rival (e.g. an exact-label match's term also claimed by a
		// colliding alias) is visible in this SAME identity/identityTerms
		// side channel would otherwise commit anyway once the census round
		// attested it, since identity/identityTerms are reused unchanged
		// across the re-entry (resolve.go's second
		// ResolveFromMergedCandidatesWithGate call passes the identical
		// maps the first call already populated) and identityCollision
		// alone -- exactly like the other five call sites before this
		// fix -- cannot see a cross-class rival.
		//
		// evidenceStrength(...) >= gate.LoneFloor: "the ratified
		// corroborated-band arithmetic computed locally over the raw base
		// plus one attested source witness (0.50 -> 0.755 >= LoneFloor); no
		// wire mechanism, no curve change, no candidate mutation, raw-bases
		// re-entry, MaxMatchMechanisms frozen at 6 by assertion" (brief
		// §1.4) -- see evidenceStrength's own doc comment
		// (chaos3896_slice_c_evidence_census.go) for the exact formula.
		// Reads rawBase[...], NOT candidates[index].Confidence (codex xhigh
		// review finding, confirmed -- see rawBase's own doc comment above):
		// the latter is this SAME call's phase-2.5 OUTPUT, already
		// corroborated for any candidate carrying 2+ real mechanisms, and
		// feeding that back into evidenceStrength's own corroboration
		// formula would double-apply it.
		if ambiguous && gateValid && evidenceCensusAttestedKey != "" && len(exactIndex) < 2 {
			if index, ok := indexBySubjectKey(candidates, evidenceCensusAttestedKey); ok &&
				!isVectorOnlyCandidate(candidates[index].MatchMechanisms) &&
				!identityCollision(evidenceCensusAttestedKey, identity, identityTerms) &&
				!identityCrossClassRivalClaimant(evidenceCensusAttestedKey, identity, identityTerms) &&
				evidenceStrength(rawBase[evidenceCensusAttestedKey]) >= gate.LoneFloor {
				committedIndex[index] = true
				candidates[index].State = contextfabric.ResolutionCommitted
				resolution.Committed = []contextfabric.SubjectRef{candidates[index].Subject}
				ambiguous = false
				commitGate = "evidence_census"
				// CHAOS-4085: statistical. A census witness lifts a raw
				// base over LoneFloor through the corroborated-band
				// arithmetic (evidenceStrength) -- it attests that SOME
				// source satisfies the question, which is a strength
				// signal, not a proof that this candidate is the subject
				// the caller named.
				bases.Record(candidates[index].Subject, contextfabric.CommitBasisStatistical)
			}
		}
	}
	if ambiguous {
		for index := range candidates {
			candidates[index].State = contextfabric.ResolutionAmbiguous
		}
	}

	// Phase 4: truncation last. parentKeys is the set of subject keys that
	// are themselves the resolved canonical parent of at least one
	// observation candidate present in this set -- prioritized just below
	// the committed subject(s) so a document's answer-bearing parent is
	// never crowded out of a tight budget by the document's own higher raw
	// relevance (see the shared-parent probe this fixes).
	parentKeys := make(map[string]bool)
	for _, candidate := range candidates {
		if !IsObservationSubjectKind(candidate.Subject.Kind) {
			continue
		}
		if parentKey, ok := observationParentKey[SubjectKey(candidate.Subject)]; ok {
			parentKeys[parentKey] = true
		}
	}
	tier := func(index int) int {
		switch {
		case committedIndex[index]:
			return 0
		case parentKeys[SubjectKey(candidates[index].Subject)]:
			return 1
		default:
			return 2
		}
	}
	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return tier(order[i]) < tier(order[j]) })
	ordered := make([]contextfabric.SubjectCandidate, len(candidates))
	for i, index := range order {
		ordered[i] = candidates[index]
	}
	if max > 0 && len(ordered) > max {
		ordered = ordered[:max]
	}
	resolution.Candidates = ordered
	// Codex round-4 finding 1: the clarification prompt must be built from
	// the RETAINED (post-truncation) candidate set, not the full set --
	// naming a subject in the prompt that Phase 4 truncation already
	// dropped from resolution.Candidates would offer the caller a choice
	// absent from the machine-readable result they would resolve it
	// against.
	if ambiguous && allowClarification {
		resolution.ClarificationPrompt = ClarificationPrompt(ordered)
	}
	if tracer != nil {
		// ONE unified decision event per resolution (not per commit-gate
		// branch): "do NOT gold-plate" -- the branch that fired is already
		// fully explained by the corroboration-stage events above (base vs
		// final confidence per candidate) plus this outcome, without
		// needing five near-identical trace call sites.
		switch {
		case len(resolution.Committed) == 1:
			committedKey := SubjectKey(resolution.Committed[0])
			winningMechanism := ""
			for index := range candidates {
				if SubjectKey(candidates[index].Subject) != committedKey {
					continue
				}
				if len(candidates[index].MatchMechanisms) > 0 {
					winningMechanism = string(candidates[index].MatchMechanisms[0])
				}
				break
			}
			// Whether the identity gate specifically was WHY this
			// committed (as opposed to an ordinary exact/lexical/vector
			// commit) is answered by correlating this event's Subject with
			// the SAME subject's own "identity_gate" event (emitted from
			// NodeCandidate, the real gate inputs) -- not reconstructed
			// here (team-lead ruling, 2026-08-17, guardrail 6).
			tracer.Trace(ResolutionTraceEvent{
				RequestID: requestID, Stage: "decision", Subject: resolution.Committed[0],
				Outcome: "committed", WinningMechanism: winningMechanism, CommitGate: commitGate,
				AliasLookupComplete: aliasIdentityComplete, IdentityTrustGateBlocked: identityTrustGateBlocked,
				SearchTruncated: searchTruncated,
				// CHAOS-4085: read back from the SAME set the engine
				// consumes, never re-derived here -- a trace that disagreed
				// with the basis the gate actually used would be worse than
				// no trace at all.
				CommitBasis:        string(bases.For(resolution.Committed[0])),
				TiedStatisticalTop: tiedStatisticalTop,
			})
		case len(resolution.Committed) == 0 && ambiguous:
			tracer.Trace(ResolutionTraceEvent{
				RequestID: requestID, Stage: "decision", Outcome: "ambiguous",
				AliasLookupComplete: aliasIdentityComplete, IdentityTrustGateBlocked: identityTrustGateBlocked,
				SearchTruncated: searchTruncated,
				// CHAOS-4085: an ambiguous outcome carrying
				// TiedStatisticalTop && SearchTruncated && CommitGate=="" IS
				// the tied-rescue refusal, countable directly from trace.
				TiedStatisticalTop: tiedStatisticalTop,
			})
		case len(resolution.Committed) == 0:
			tracer.Trace(ResolutionTraceEvent{
				RequestID: requestID, Stage: "decision", Outcome: "no_commit",
				AliasLookupComplete: aliasIdentityComplete, IdentityTrustGateBlocked: identityTrustGateBlocked,
				SearchTruncated: searchTruncated, TiedStatisticalTop: tiedStatisticalTop,
			})
		}
	}
	return resolution, bases
}

// tiedStatisticalTopUnderTruncation reports the ONE population CHAOS-4085
// removes from the CHAOS-3829 vector-margin rescue: a resolution whose
// search TRUNCATED and whose top two commit-eligible candidates hold the
// SAME confidence, below the exact/identity 1.0 band.
//
// Both conjuncts are load-bearing, and neither alone is:
//
//   - searchTruncated alone is already tolerated by the rescue on purpose
//     (see the rescue's own comment: it may fire even when truncation is
//     why ambiguous is true), because a margin computed over a bounded,
//     calibrated top-K carries its own completeness envelope.
//   - a tie alone, on an UNTRUNCATED search, is genuine information: the
//     population was complete and the ranking honestly could not separate
//     two real candidates.
//
// Together they are not. A tie says the lexical/relevance signal
// discriminated nothing; truncation says the set it failed to discriminate
// over may not even contain the right answer. Breaking that with an
// embedding margin answers "which of these is closest in embedding space",
// which is a different question from "which subject did the caller mean" --
// the same category error CHAOS-3810's exact-label duplicate rule and
// CHAOS-3884's identityCollision rule already refuse to let a proximity
// signal arbitrate.
//
// candidates is confidence-sorted descending and commitIndex preserves that
// order, so commitIndex[0]/[1] are the top two ELIGIBLE candidates (an
// observation blocked by an unresolved canonical parent is already absent).
// Fewer than two eligible candidates cannot be tied.
//
// The <1 test excludes the exact/identity 1.0 band: two candidates both at
// 1.0 are a duplicate-identity collision, which len(exactIndex) < 2 and
// identityCollision already refuse for reasons of their own, and folding
// them in here would only obscure which rule did the refusing.
//
// Float equality is used deliberately, with no tolerance. These confidences
// are not independently-derived measurements being compared for
// approximate agreement: they are the SAME arithmetic (CorroboratedConfidence
// over a shared band constant) producing bit-identical results for
// candidates that scored alike, which is precisely the tie this refuses. A
// tolerance would additionally capture near-ties, a strictly larger
// population than the one the trial demonstrated unsafe, and CHAOS-4085
// does not claim evidence about it.
func tiedStatisticalTopUnderTruncation(candidates []contextfabric.SubjectCandidate, commitIndex []int, searchTruncated bool) bool {
	if !searchTruncated || len(commitIndex) < 2 {
		return false
	}
	top, second := candidates[commitIndex[0]], candidates[commitIndex[1]]
	return top.Confidence < 1 && top.Confidence == second.Confidence
}

// vectorMarginCommit implements CHAOS-3829's ratified commit-path carve-out
// (chris-ratified 2026-08-15/16, geometry finalized 2026-08-16 dropping the
// original vectorSearchComplete conjunct; codex round-1 findings F0/F4
// adjudicated 2026-08-16 -- see ResolveFromMergedCandidates' own doc
// comment for the call site). candidates/commitIndex are the SAME values
// ResolveFromMergedCandidates' own gates just finished computing --
// commitIndex already excludes a blocked observation, so TOP (below) can
// never be a subject the existing eligibility rules already ruled out.
//
// TOP is the highest-vector-similarity entry, among commitIndex candidates
// that have a vectorArmSimilarity value at all (a subject with NO
// vector-mechanism finding is simply absent from that map and can never be
// top). COMPETITOR is the highest-vector-similarity entry ANYWHERE ELSE in
// vectorArmSimilarity -- deliberately NOT restricted to commitIndex (codex
// r1 F0): vectorArmSimilarity is populated from every raw ANN result
// BEFORE NodeCandidate's authorization/acceptance decision runs (see
// mergeSearchResults), so a genuinely closer competitor that NodeCandidate
// rejected (or that never became a commit-eligible candidate for any other
// reason) still counts as a competitor here. If that competitor's
// similarity exceeds top's, the resulting margin is NEGATIVE, which can
// never clear a positive threshold -- an automatic, structural fail-closed,
// not a special case this function has to detect.
//
// Fires ONLY when ALL of:
//
//  1. TOP exists (>=1 commitIndex candidate carries a vectorArmSimilarity
//     value) AND COMPETITOR exists (>=1 OTHER entry exists anywhere in
//     vectorArmSimilarity) -- a margin needs a competitor to measure a gap
//     against; neither existing alone is a margin, and treating either
//     absence as an infinite or zero margin would be fabricating a value
//     this function never measured (mirrors CalibrateMarginFromReport's
//     identical eligibility rule, tau_calibration.go);
//  2. TOP is CORROBORATED under the NARROWED, MEASURED pairing (codex r1
//     F4): its merged MatchMechanisms must include BOTH MatchVector AND
//     MatchLexical specifically -- see vectorArmCorroborated's own doc
//     comment for why this replaces the broader
//     DistinctMechanismCount>=2-of-anything test CorroboratedConfidence
//     itself uses for the EXISTING lone/top-of-two gates (untouched: this
//     function never calls CorroboratedConfidence or reads Confidence at
//     all);
//  3. the margin (TOP's similarity minus COMPETITOR's) is >= threshold (M,
//     a per-embedder-identity calibrated constant the caller already
//     validated is > 0 before calling -- see falkorgraph/retrieval_policy.go's
//     RetrievalPolicy.VectorMarginCommitThreshold).
//
// Returns ok=false on any missing input above; the caller's existing
// ambiguous verdict then stands entirely unchanged.
//
// SOUND UNDER TRUNCATION: vectorArmSimilarity's values are RAW, unclamped
// similarities (see CandidateNode.VectorSimilarity's doc comment) --
// computed identically whether or not the search call that found a given
// candidate itself truncated. A k-NN result is distance-ordered, so
// truncation can only ever drop candidates BEYOND the returned set; it
// cannot reorder or misrepresent the gap between two candidates that WERE
// returned. This is precisely why this function reads vectorArmSimilarity,
// never a candidate's own Confidence/Relevance (which vector.go correctly,
// but bluntly, clamps to a shared floor for EVERY candidate once THAT
// call's OWN search truncated -- exactly the differentiation this margin
// needs, and exactly why almost every eligible case in the CHAOS-3829
// calibration measurement had searchTruncated=true).
func vectorMarginCommit(candidates []contextfabric.SubjectCandidate, commitIndex []int, vectorArmSimilarity map[string]float64, threshold float64) (int, bool) {
	type ranked struct {
		key        string
		similarity float64
	}
	entries := make([]ranked, 0, len(vectorArmSimilarity))
	for key, similarity := range vectorArmSimilarity {
		entries = append(entries, ranked{key: key, similarity: similarity})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].similarity == entries[j].similarity {
			return entries[i].key < entries[j].key
		}
		return entries[i].similarity > entries[j].similarity
	})

	eligibleByKey := make(map[string]int, len(commitIndex))
	for _, index := range commitIndex {
		eligibleByKey[SubjectKey(candidates[index].Subject)] = index
	}

	// TOP: the highest-similarity entry that is ALSO a commit-eligible
	// candidate -- may skip past one or more higher-similarity, non-eligible
	// entries, which is exactly what makes those entries available as the
	// competitor below.
	topEntry := -1
	for i, e := range entries {
		if _, ok := eligibleByKey[e.key]; ok {
			topEntry = i
			break
		}
	}
	if topEntry == -1 {
		return 0, false
	}
	top := entries[topEntry]
	topIndex := eligibleByKey[top.key]

	// COMPETITOR: the highest-similarity entry OTHER than top, from the
	// FULL side map -- see the function doc comment (F0) for why this is
	// not restricted to commitIndex.
	competitorEntry := -1
	for i := range entries {
		if i == topEntry {
			continue
		}
		competitorEntry = i
		break
	}
	if competitorEntry == -1 {
		return 0, false
	}
	competitor := entries[competitorEntry]

	if !vectorArmCorroborated(candidates[topIndex].MatchMechanisms) {
		return 0, false
	}
	if margin := top.similarity - competitor.similarity; margin < threshold {
		return 0, false
	}
	return topIndex, true
}

// vectorArmCorroborated is CHAOS-3829 codex r1 F4's NARROWED corroboration
// check for the commit-path carve-out specifically: the MEASURED pairing
// CalibrateMarginFromReport actually calibrated M against is "the oracle's
// vector arm AND its lexical arm (fulltextSearchNodesForResolution) both
// independently proposed this subject" -- i.e. MatchVector AND MatchLexical
// SPECIFICALLY, not "any two distinct recognized mechanisms" the way
// CorroboratedConfidence's own, broader DistinctMechanismCount>=2 test
// reads for the EXISTING lone/top-of-two gates (left entirely unchanged --
// this function is called ONLY from vectorMarginCommit). A vector+traversal
// or vector+exact pairing was never part of the calibrated population and
// must not enable this specific carve-out; MatchLexical is the mechanism
// falkorgraph's hybridSearchNodes stamps on every fulltextSearchNodesForResolution
// result (vector.go, immediately after that call), the SAME production
// function the oracle harness calls directly for its own lexical-arm
// measurement (measureOneTermVectorArm, oracle_live_test.go) -- see
// TestHybridSearchNodes_StampsMatchLexicalOnFulltextResults for the pinning
// test keeping that mechanism choice honest.
func vectorArmCorroborated(mechanisms []contextfabric.MatchMechanism) bool {
	return HasMechanism(mechanisms, contextfabric.MatchVector) && HasMechanism(mechanisms, contextfabric.MatchLexical)
}

// AnyCallerSourced reports whether any resolved candidate came from a
// caller-explicit hint (see callerSourced tracking in the adapter's
// ResolveSubjects). Ported unchanged from zepgraph.anyCallerSourced.
func AnyCallerSourced(candidatesBySubject map[string]contextfabric.SubjectCandidate, callerSourced map[string]bool) bool {
	for key := range candidatesBySubject {
		if callerSourced[key] {
			return true
		}
	}
	return false
}

// FinalizeExactResolution implements N4's two-class truncation: when the
// resolved exact-hint candidates exceed max, caller-explicit hints are
// retained first (all of them, up to the bound); receipt-derived hints fill
// only the remaining room, never displacing a caller-explicit one. Order is
// otherwise deterministic (subject key) within each class. Ported unchanged
// from zepgraph.finalizeExactResolution.
func FinalizeExactResolution(candidatesBySubject map[string]contextfabric.SubjectCandidate, callerSourced map[string]bool, max int) contextfabric.SubjectResolution {
	candidates := make([]contextfabric.SubjectCandidate, 0, len(candidatesBySubject))
	for _, candidate := range candidatesBySubject {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		iCaller, jCaller := callerSourced[SubjectKey(candidates[i].Subject)], callerSourced[SubjectKey(candidates[j].Subject)]
		if iCaller != jCaller {
			return iCaller
		}
		return SubjectKey(candidates[i].Subject) < SubjectKey(candidates[j].Subject)
	})
	if max > 0 && len(candidates) > max {
		candidates = candidates[:max]
	}
	committed := make([]contextfabric.SubjectRef, 0, len(candidates))
	for _, candidate := range candidates {
		committed = append(committed, candidate.Subject)
	}
	return contextfabric.SubjectResolution{Candidates: candidates, Committed: committed}
}

// FinalizeExactResolutionWithBasis is FinalizeExactResolution plus the
// CommitBasisSet for what it committed -- recorded PER CLASS, not
// uniformly.
//
// This is the caller-hint SHORT CIRCUIT (resolve.go's AnyCallerSourced
// branch), a second commit exit that never reaches
// ResolveFromMergedCandidatesWithGateAndBasis at all. It fires when at
// least ONE caller-explicit hint resolved -- and it then commits every
// retained candidate, including RECEIPT-DERIVED ones that merely rode
// along in the same request.
//
// The two classes do NOT carry the same proof, and this function is built
// around that distinction already (see its own two-class truncation rule
// above, and resolve.go's short-circuit comment: a receipt-only resolution
// deliberately does NOT short-circuit, "so a conversational follow-up
// naming a different subject than the one a prior receipt bound can still
// be found and compete on its own terms"):
//
//   - callerSourced -- a canonical id the caller stated in THIS request
//     (RequestedScope.SubjectHints with any source but
//     prior_subject_receipt). The caller is asserting identity now, and
//     the hint loop re-read it from the graph by keyed lookup and
//     re-authorized it. CommitBasisCallerCanonicalID.
//   - everything else retained here -- a subject derived from a PRIOR
//     result's receipt. It was re-read and re-authorized identically, but
//     the identity it names was chosen in an earlier turn and may itself
//     have been an engine-proposed, statistically-ranked candidate. Under
//     DP9 that is not a proof, and stamping it proven would launder a
//     prior statistical guess into an exemption one request later.
//     CommitBasisStatistical -- it commits exactly as before, it simply
//     has to be affirmed like any other scored subject.
//
// Codex xhigh review round 3, HIGH: the previous version stamped every
// retained subject CommitBasisCallerCanonicalID, which collapsed exactly
// the distinction this function's own truncation rule exists to preserve.
func FinalizeExactResolutionWithBasis(candidatesBySubject map[string]contextfabric.SubjectCandidate, callerSourced map[string]bool, max int) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet) {
	resolution := FinalizeExactResolution(candidatesBySubject, callerSourced, max)
	bases := make(contextfabric.CommitBasisSet, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		if callerSourced[SubjectKey(subject)] {
			bases.Record(subject, contextfabric.CommitBasisCallerCanonicalID)
			continue
		}
		bases.Record(subject, contextfabric.CommitBasisStatistical)
	}
	return resolution, bases
}

// ClarificationPrompt builds the caller-facing ambiguity prompt from the
// (post-truncation) candidate set. Ported unchanged from
// zepgraph.clarificationPrompt.
func ClarificationPrompt(candidates []contextfabric.SubjectCandidate) string {
	max := 3
	if len(candidates) < max {
		max = len(candidates)
	}
	labels := make([]string, 0, max)
	for _, candidate := range candidates {
		labels = append(labels, candidate.Subject.Label)
		if len(labels) == 3 {
			break
		}
	}
	return "Which subject did you mean: " + strings.Join(labels, ", ") + "?"
}
