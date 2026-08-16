package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// vectorBandCeiling mirrors falkorgraph.vectorRelevanceCeiling. It is
// duplicated here rather than imported because graphrank must not depend on a
// backend adapter -- but the RELATIONSHIP between the two constants is the
// whole of AC-3778-3, so it is asserted explicitly below.
const vectorBandCeiling = 0.70

// loneCommitGate and topOfTwoGate are the shipped thresholds in
// ResolveFromMergedCandidates. CHAOS-3778 does not change them; the
// corroborated band is positioned AGAINST them, so a test that pins the band
// must pin what it is positioned against.
const (
	loneCommitGate = 0.72
	topOfTwoGate   = 0.88
)

func corroborationCandidate(id string, confidence float64, mechanisms ...contextfabric.MatchMechanism) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		ReceiptID: "receipt_" + id + "_padding",
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectProject, CanonicalID: id, Label: id,
		},
		State:           contextfabric.ResolutionProposed,
		MatchReasons:    []string{"probe"},
		Confidence:      confidence,
		MatchMechanisms: mechanisms,
	}
}

func resolveOne(candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}
	return ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0)
}

// AC-3778-3: "A vector hit alone never commits a subject." This holds by
// ARITHMETIC, not by a rule -- the vector band's ceiling is strictly below the
// lone-candidate gate, so there is no vector-only confidence that can reach it.
func TestAC_3778_3_VectorOnlyCandidateCannotReachTheLoneCommitGate(t *testing.T) {
	if vectorBandCeiling >= loneCommitGate {
		t.Fatalf("vector band ceiling %v must stay strictly below the lone-candidate gate %v", vectorBandCeiling, loneCommitGate)
	}
	// The strongest possible vector-only candidate, unopposed.
	resolution := resolveOne(corroborationCandidate("auth", vectorBandCeiling, contextfabric.MatchVector))
	if len(resolution.Committed) != 0 {
		t.Fatalf("a vector-only candidate at the band ceiling must not commit, got %v", resolution.Committed)
	}
	if resolution.Candidates[0].Confidence != vectorBandCeiling {
		t.Fatalf("a single-mechanism candidate must keep its own band confidence, got %v", resolution.Candidates[0].Confidence)
	}
}

// AC-3778-2: the lift. A subject that vector search proposed directly AND
// traversal proposed independently reaches the corroborated band and commits
// when unopposed. This is the path an ambiguous question actually takes.
func TestAC_3778_2_CorroboratedPairReachesTheLoneCommitGate(t *testing.T) {
	candidate := corroborationCandidate("auth", 0.62, contextfabric.MatchVector, contextfabric.MatchTraversalParent)
	resolution := resolveOne(candidate)
	if len(resolution.Committed) != 1 {
		t.Fatalf("a corroborated, unopposed candidate must commit, got %v (confidence %v)", resolution.Committed, resolution.Candidates[0].Confidence)
	}
	if got := resolution.Candidates[0].Confidence; got < loneCommitGate || got > CorroboratedCeiling {
		t.Fatalf("corroborated confidence %v must land inside [%v, %v]", got, loneCommitGate, CorroboratedCeiling)
	}
}

// AC-3778-3 again, from the other side: corroboration must not turn genuine
// ambiguity into a guess. Two corroborated candidates cannot clear the
// top-of-two gate, because the band ceiling is strictly below it.
func TestAC_3778_3_TwoCorroboratedCandidatesStillClarify(t *testing.T) {
	if CorroboratedCeiling >= topOfTwoGate {
		t.Fatalf("corroborated ceiling %v must stay strictly below the top-of-two gate %v", CorroboratedCeiling, topOfTwoGate)
	}
	resolution := resolveOne(
		corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical, contextfabric.MatchTraversalParent),
		corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical),
	)
	if len(resolution.Committed) != 0 {
		t.Fatalf("two corroborated candidates must fall to clarification, got %v", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("a clarification prompt must be offered when two corroborated candidates compete")
	}
}

// RED -> GREEN, as required by the orchestrator's ruling: this test proves the
// merge-rule change is load-bearing. It runs the SAME corroboration case
// through the pre-CHAOS-3778 "keep the higher confidence, discard the loser"
// merge and through MergeCandidates, and asserts they disagree -- the legacy
// rule cannot commit, because it threw away the very mechanism that made the
// candidate corroborated.
func TestMaxOnlyMergeFailsTheCorroborationCase(t *testing.T) {
	// The same subject, found twice: once by vector search directly, once by
	// walking from a matched document to its canonical parent.
	fromVector := corroborationCandidate("auth", 0.62, contextfabric.MatchVector)
	fromTraversal := corroborationCandidate("auth", 0.43, contextfabric.MatchTraversalParent)

	// legacyMaxOnlyMerge is the exact rule this change replaced.
	legacyMaxOnlyMerge := func(current, incoming contextfabric.SubjectCandidate) contextfabric.SubjectCandidate {
		if incoming.Confidence > current.Confidence {
			return incoming
		}
		return current
	}

	legacy := resolveOne(legacyMaxOnlyMerge(fromVector, fromTraversal))
	if len(legacy.Committed) != 0 {
		t.Fatal("RED case is not red: the legacy max-only merge was expected to fail to commit")
	}
	if count := DistinctMechanismCount(legacy.Candidates[0].MatchMechanisms); count != 1 {
		t.Fatalf("the legacy merge must retain only the winner's single mechanism, got %d", count)
	}

	merged := resolveOne(MergeCandidates(fromVector, fromTraversal))
	if len(merged.Committed) != 1 {
		t.Fatalf("GREEN case failed: MergeCandidates must let the corroborated subject commit, got %v (confidence %v)",
			merged.Committed, merged.Candidates[0].Confidence)
	}
	if count := DistinctMechanismCount(merged.Candidates[0].MatchMechanisms); count != 2 {
		t.Fatalf("MergeCandidates must retain both mechanisms, got %d", count)
	}
}

// Order-soundness, extended across the corroboration arm (the AC-3778-0
// property this change must not break): confidence is monotone non-decreasing
// in BOTH the base confidence and the mechanism count.
func TestCorroborationIsMonotoneInBaseAndInMechanismCount(t *testing.T) {
	pair := []contextfabric.MatchMechanism{contextfabric.MatchVector, contextfabric.MatchLexical}
	previous := -1.0
	for base := 0.0; base <= 0.99; base += 0.01 {
		got := CorroboratedConfidence(pair, base)
		if got < previous {
			t.Fatalf("confidence fell from %v to %v as base rose to %v", previous, got, base)
		}
		previous = got
	}

	growing := []contextfabric.MatchMechanism{
		contextfabric.MatchExact, contextfabric.MatchAlias, contextfabric.MatchProviderKey,
		contextfabric.MatchLexical, contextfabric.MatchVector, contextfabric.MatchTraversalParent,
	}
	previous = -1.0
	for count := 2; count <= len(growing); count++ {
		got := CorroboratedConfidence(growing[:count], 0.6)
		if got < previous {
			t.Fatalf("confidence fell from %v to %v as the mechanism count rose to %d", previous, got, count)
		}
		previous = got
	}
}

// The invariant is that CORROBORATION never lifts a candidate to or past the
// top-of-two gate. It is deliberately NOT "the output is always below the
// gate": a base that is already at or above it passes through untouched by the
// never-demote guard, because demoting an independently strong candidate would
// be a worse bug than the one this band exists to prevent.
//
// Stated exactly: the output never exceeds max(base, CorroboratedCeiling). So
// anything at or above the gate was already there on the strength of the
// candidate's own base, and corroboration contributed nothing to it.
//
// In practice no such base exists on the shipped paths -- the single-mechanism
// bands cap at 0.75 (lexical) and 0.70 (vector), traversal-exact reaches 0.85,
// and an exact hint at 1.0 returns early -- but the property is asserted over
// the whole domain so a future band change cannot quietly violate it.
func TestCorroborationNeverLiftsACandidateToTheTopOfTwoGate(t *testing.T) {
	all := []contextfabric.MatchMechanism{
		contextfabric.MatchExact, contextfabric.MatchAlias, contextfabric.MatchProviderKey,
		contextfabric.MatchLexical, contextfabric.MatchVector, contextfabric.MatchTraversalParent,
	}
	for count := 2; count <= len(all); count++ {
		for base := 0.0; base < 1.0; base += 0.005 {
			got := CorroboratedConfidence(all[:count], base)
			ceiling := CorroboratedCeiling
			if base > ceiling {
				ceiling = base
			}
			if got > ceiling {
				t.Fatalf("corroborated confidence %v (base %v, %d mechanisms) exceeded max(base, ceiling) %v", got, base, count, ceiling)
			}
			if got >= topOfTwoGate && base < topOfTwoGate {
				t.Fatalf("corroboration lifted base %v (%d mechanisms) to %v, at or past the top-of-two gate", base, count, got)
			}
			// Codex round-1 review note (b): for a base above the ceiling the
			// never-demote guard must pass it through EXACTLY, not merely
			// bound it. Asserting only the bound would let a future change
			// quietly reshape a high base (say, damping it toward the
			// ceiling) while still satisfying the inequality.
			if base > CorroboratedCeiling && got != base {
				t.Fatalf("a base above the ceiling must pass through unchanged: base %v (%d mechanisms) became %v", base, count, got)
			}
			if got < CorroboratedFloor && got != base {
				t.Fatalf("corroborated confidence %v (base %v, %d mechanisms) fell below the band floor", got, base, count)
			}
		}
	}
}

// Corroboration is evidence FOR a candidate. It must never cost one
// confidence. This is reachable, not theoretical: an exact label match reached
// through traversal carries base 0.85 (1.0 * the 0.85 one-hop discount) with
// two mechanisms, whose corroborated value is lower.
func TestCorroborationNeverDemotesAStrongerSingleMechanismCandidate(t *testing.T) {
	mechanisms := []contextfabric.MatchMechanism{contextfabric.MatchExact, contextfabric.MatchTraversalParent}
	const base = 0.85
	got := CorroboratedConfidence(mechanisms, base)
	if got < base {
		t.Fatalf("corroboration demoted a candidate from %v to %v", base, got)
	}
	if got != base {
		t.Fatalf("this case is expected to fall through to the base guard, got %v", got)
	}
}

// An exact match is already maximal. Being found a second way must never
// demote it into the corroborated band.
func TestExactMatchIsNeverDemotedByCorroboration(t *testing.T) {
	mechanisms := []contextfabric.MatchMechanism{contextfabric.MatchExact, contextfabric.MatchLexical, contextfabric.MatchVector}
	if got := CorroboratedConfidence(mechanisms, 1); got != 1 {
		t.Fatalf("an exact match must keep confidence 1, got %v", got)
	}
}

// An unrecognized mechanism must be DROPPED, never counted. Otherwise a typo
// would be enough to corroborate a candidate over the commit gate.
func TestUnknownMechanismIsDroppedAndNeverCorroborates(t *testing.T) {
	mechanisms := []contextfabric.MatchMechanism{contextfabric.MatchVector, contextfabric.MatchMechanism("semantic")}
	if count := DistinctMechanismCount(mechanisms); count != 1 {
		t.Fatalf("an unrecognized mechanism must not be counted, got %d distinct", count)
	}
	if got := CorroboratedConfidence(mechanisms, 0.62); got != 0.62 {
		t.Fatalf("an unrecognized mechanism must not corroborate; confidence changed to %v", got)
	}
}

// A merged mechanism set must serialize in one canonical order regardless of
// the order the mechanisms were discovered in -- an InvestigationResult is
// immutable and answer reuse keys on it (CHAOS-3782).
func TestMergedMechanismOrderIsCanonical(t *testing.T) {
	forward := MergeMechanisms(
		[]contextfabric.MatchMechanism{contextfabric.MatchVector},
		[]contextfabric.MatchMechanism{contextfabric.MatchExact, contextfabric.MatchLexical},
	)
	reverse := MergeMechanisms(
		[]contextfabric.MatchMechanism{contextfabric.MatchLexical, contextfabric.MatchExact},
		[]contextfabric.MatchMechanism{contextfabric.MatchVector},
	)
	if len(forward) != len(reverse) {
		t.Fatalf("merged sets differ in size: %v vs %v", forward, reverse)
	}
	for i := range forward {
		if forward[i] != reverse[i] {
			t.Fatalf("merged mechanism order is not canonical: %v vs %v", forward, reverse)
		}
	}
	if forward[0] != contextfabric.MatchExact {
		t.Fatalf("canonical order must lead with the strongest mechanism, got %v", forward)
	}
}

// A single-mechanism candidate must be returned completely unchanged, so every
// pre-CHAOS-3778 resolution behaves exactly as it did before.
func TestSingleMechanismConfidenceIsUnchanged(t *testing.T) {
	for _, base := range []float64{0, 0.5, 0.62, 0.70, 0.75, 0.85} {
		if got := CorroboratedConfidence([]contextfabric.MatchMechanism{contextfabric.MatchLexical}, base); got != base {
			t.Fatalf("single-mechanism base %v changed to %v", base, got)
		}
		if got := CorroboratedConfidence(nil, base); got != base {
			t.Fatalf("no-mechanism base %v changed to %v", base, got)
		}
	}
}

// Codex round-1 F1, at the RESOLUTION level: this is what the adapter-side bug
// actually cost. A strong, unopposed lexical candidate must still commit when
// a sibling vector search found nothing -- truncation authority belongs to a
// search that had something to truncate.
func TestF1_AStrongLexicalCommitSurvivesAVectorSearchThatFoundNothing(t *testing.T) {
	// A lexical hit at the band ceiling, alone, clears the 0.72 gate.
	strong := corroborationCandidate("auth", 0.75, contextfabric.MatchLexical)

	// The shipped behavior: the empty vector search reports no truncation, so
	// the lexical commit stands.
	notTruncated := ResolveFromMergedCandidates(
		map[string]contextfabric.SubjectCandidate{SubjectKey(strong.Subject): strong},
		map[string]string{}, map[string]bool{}, 10, true, false, nil, 0,
	)
	if len(notTruncated.Committed) != 1 {
		t.Fatalf("a strong unopposed lexical candidate must commit, got %v", notTruncated.Committed)
	}

	// The bug, demonstrated: had the empty vector search claimed truncation,
	// the same candidate would have been forced to ambiguous. This asserts the
	// COST of the defect, so the fix cannot be reverted without a red test.
	truncated := ResolveFromMergedCandidates(
		map[string]contextfabric.SubjectCandidate{SubjectKey(strong.Subject): strong},
		map[string]string{}, map[string]bool{}, 10, true, true, nil, 0,
	)
	if len(truncated.Committed) != 0 {
		t.Fatal("this test no longer demonstrates the cost it exists to pin: " +
			"searchTruncated must still short-circuit the commit decision")
	}
}

// Codex round-1 F4, per the orchestrator's ruling: the degradation marker is
// REQUEST-SCOPED and travels out of ResolveSubjects on the resolution, so the
// engine can fold it into the answer. It must not be conflated with
// truncation, which has entirely different consequences.
func TestF4_ResolveSubjectsReportsRetrievalDegradationOnTheResolution(t *testing.T) {
	backend := &fakeGraphBackend{
		searchResults:  map[string][]CandidateNode{},
		searchDegraded: true,
	}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(),
		contextfabric.InterpretedQuestion{SubjectTerms: []string{"the auth work"}}, backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if !resolution.RetrievalDegraded {
		t.Fatal("a Search that reported a missing mechanism must surface on the resolution")
	}
}

// Degradation and truncation are independent signals with different
// consequences: truncation blocks auto-commit, degradation does not (it only
// tells the reader the candidate set may be narrower). Conflating them would
// either block commits that should stand or hide missing mechanisms.
func TestF4_DegradationDoesNotBlockAnAutoCommitTheWayTruncationDoes(t *testing.T) {
	strong := corroborationCandidate("auth", 0.75, contextfabric.MatchLexical)
	bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(strong.Subject): strong}

	// Degradation is NOT an input to ResolveFromMergedCandidates at all --
	// only truncation is -- so a degraded-but-untruncated resolution still
	// commits.
	resolution := ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0)
	if len(resolution.Committed) != 1 {
		t.Fatalf("degradation must not block an unopposed strong commit, got %v", resolution.Committed)
	}
}

// A healthy search must never mark the answer degraded.
func TestF4_HealthySearchReportsNoDegradation(t *testing.T) {
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{}}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(),
		contextfabric.InterpretedQuestion{SubjectTerms: []string{"anything"}}, backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects: %v", err)
	}
	if resolution.RetrievalDegraded {
		t.Fatal("a healthy search must not mark the resolution degraded")
	}
}
