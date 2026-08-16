package graphrank

import (
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// ResolveFromMergedCandidates implements the class fix for Codex round-3
// findings "2" and "3": both were the same defect -- truncation ran BEFORE
// the semantic decision phases (parent-aware eligibility, commit priority),
// so whichever candidates truncation happened to keep could silently
// exclude the one the decision phase would have picked. This function runs
// in explicit phases with truncation LAST:
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
// true.
func ResolveFromMergedCandidates(candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, max int, allowClarification bool, searchTruncated bool, vectorArmSimilarity map[string]float64, vectorMarginCommitThreshold float64) contextfabric.SubjectResolution {
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
	for index := range candidates {
		candidates[index].Confidence = CorroboratedConfidence(candidates[index].MatchMechanisms, candidates[index].Confidence)
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
		return resolution
	}

	// Phase 3: commit decision over the FULL, untruncated ranked set.
	committedIndex := make(map[int]bool)
	for index, candidate := range candidates {
		if candidate.State == contextfabric.ResolutionCommitted && candidate.Confidence == 1 {
			committedIndex[index] = true
			resolution.Committed = append(resolution.Committed, candidate.Subject)
		}
	}
	ambiguous := false
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
		switch {
		case len(exactIndex) == 1:
			committedIndex[exactIndex[0]] = true
			candidates[exactIndex[0]].State = contextfabric.ResolutionCommitted
			resolution.Committed = []contextfabric.SubjectRef{candidates[exactIndex[0]].Subject}
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
		case len(commitIndex) == 1 && candidates[commitIndex[0]].Confidence >= 0.72:
			committedIndex[commitIndex[0]] = true
			candidates[commitIndex[0]].State = contextfabric.ResolutionCommitted
			resolution.Committed = []contextfabric.SubjectRef{candidates[commitIndex[0]].Subject}
		case len(commitIndex) >= 2:
			top, second := candidates[commitIndex[0]], candidates[commitIndex[1]]
			if gap := top.Confidence - second.Confidence; top.Confidence >= 0.88 && gap >= 0.12 {
				committedIndex[commitIndex[0]] = true
				candidates[commitIndex[0]].State = contextfabric.ResolutionCommitted
				resolution.Committed = []contextfabric.SubjectRef{candidates[commitIndex[0]].Subject}
			} else {
				ambiguous = true
			}
		default:
			ambiguous = true
		}
		// CHAOS-3829: the additive commit-path carve-out, checked ONLY as
		// a RESCUE once every gate above has already run to completion
		// and decided ambiguous with nothing committed -- every branch
		// above (the exact-label override, searchTruncated, the lone
		// 0.72 gate, the top-of-two 0.88/0.12 gate) is untouched, in both
		// code and behavior, by what follows. See vectorMarginCommit's
		// doc comment for the full precondition set this reads
		// (corroboration + a measurable, sufficiently large vector-arm
		// top-1/top-2 similarity margin) and why it may fire even when
		// searchTruncated is the reason ambiguous is true.
		//
		// codex r1 F1 (accepted, narrowed): max>=2 is REQUIRED. At
		// max>=2, the merged cross-call top-2 argument is conservative --
		// any candidate this resolution never even SAW (beyond every
		// individual Search call's own returned set, as opposed to F0's
		// "returned but NodeCandidate-rejected" case, which
		// vectorArmSimilarity already covers) has similarity <= that
		// call's own Kth-ranked (least-similar) returned row, which is in
		// turn <= the merged, cross-call vectorArmSimilarity's own second
		// entry -- so a truly UNSEEN candidate can never be closer than
		// the competitor this function already found. At max==1, a Search
		// call returns AT MOST one row per term, so there is no such
		// bound at all: an unseen second-place candidate could have any
		// similarity whatsoever, and the "competitor" this function finds
		// (if any, from a DIFFERENT term's own single result) carries no
		// guarantee of being the TRUE nearest one. Fail closed rather than
		// trust a margin with no completeness bound behind it.
		if ambiguous && vectorMarginCommitThreshold > 0 && max >= 2 {
			if index, ok := vectorMarginCommit(candidates, commitIndex, vectorArmSimilarity, vectorMarginCommitThreshold); ok {
				committedIndex[index] = true
				candidates[index].State = contextfabric.ResolutionCommitted
				resolution.Committed = []contextfabric.SubjectRef{candidates[index].Subject}
				ambiguous = false
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
	return resolution
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
