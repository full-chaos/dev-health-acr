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
func ResolveFromMergedCandidates(candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, max int, allowClarification bool, searchTruncated bool) contextfabric.SubjectResolution {
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
