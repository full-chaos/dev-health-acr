package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// resolveWithBasis is the CHAOS-4085 counterpart to resolveOne: it returns
// BOTH the resolution and the CommitBasisSet recorded at the commit site,
// which is the whole point of these tests -- the basis is not derivable
// from the resolution, so nothing but the second return value can prove it.
func resolveWithBasis(searchTruncated bool, vectorArmSimilarity map[string]float64, threshold float64, aliasIdentityComplete bool, identity identityClaimants, terms identityMatchTerms, candidates ...contextfabric.SubjectCandidate) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet) {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}
	return ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, searchTruncated,
		vectorArmSimilarity, threshold, false, 10, 20, true,
		DefaultCommitGatePolicy(), identity, terms, aliasIdentityComplete, nil, "", "")
}

// tiedTopCandidates reproduces the v9 trial's case-61 RESOLUTION shape: a
// three-way tie at one confidence, every candidate corroborated
// lexical+vector, none of them anywhere near TopFloor. The identical shape
// (same tie arity, same mechanisms, same band) produced a WRONG commit on a
// never-commit control and a CORRECT commit on a different case in the same
// run -- which is the finding that made this class unsalvageable at
// resolution time.
func tiedTopCandidates() []contextfabric.SubjectCandidate {
	return []contextfabric.SubjectCandidate{
		corroborationCandidate("tied_a", 0.50, contextfabric.MatchLexical, contextfabric.MatchVector),
		corroborationCandidate("tied_b", 0.50, contextfabric.MatchLexical, contextfabric.MatchVector),
		corroborationCandidate("tied_c", 0.50, contextfabric.MatchLexical, contextfabric.MatchVector),
	}
}

// tiedTopSimilarities gives tied_a a decisive vector-arm margin, so the
// rescue WOULD fire on this input if nothing refused it. Without this the
// tests below would pass for the wrong reason (no margin, hence no rescue).
func tiedTopSimilarities() map[string]float64 {
	return map[string]float64{
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "tied_a"}): 0.95,
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "tied_b"}): 0.40,
		SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "tied_c"}): 0.38,
	}
}

// TestChaos4085_TiedTopUnderTruncationRefusesTheVectorMarginRescue is the
// resolution-side regression pin for the v9 trial's wrong commit. A tied
// statistical top plus a truncated search must commit NOTHING, however
// decisive the vector-arm margin looks.
func TestChaos4085_TiedTopUnderTruncationRefusesTheVectorMarginRescue(t *testing.T) {
	resolution, bases := resolveWithBasis(true /* searchTruncated */, tiedTopSimilarities(), 0.25, false, nil, nil, tiedTopCandidates()...)

	if len(resolution.Committed) != 0 {
		t.Fatalf("a tied statistical top under a truncated search must commit nothing, got %v", resolution.Committed)
	}
	if len(bases) != 0 {
		t.Fatalf("nothing committed, so no basis may be recorded, got %v", bases)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.State == contextfabric.ResolutionCommitted {
			t.Fatalf("candidate %s left in committed state with an empty Committed list", candidate.Subject.CanonicalID)
		}
	}
}

// TestChaos4085_TiedTopWithoutTruncationStillRescues is the NARROWNESS half
// of the pin above, and it is what proves this ticket removed one specific
// population rather than switching the CHAOS-3829 rescue off. An
// untruncated search that reaches a tie saw a COMPLETE population, so the
// tie is real information and the ratified carve-out still applies.
func TestChaos4085_TiedTopWithoutTruncationStillRescues(t *testing.T) {
	resolution, bases := resolveWithBasis(false /* searchTruncated */, tiedTopSimilarities(), 0.25, false, nil, nil, tiedTopCandidates()...)

	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "tied_a" {
		t.Fatalf("an untruncated tie must still rescue the decisive-margin candidate, got %v", resolution.Committed)
	}
	if basis := bases.For(resolution.Committed[0]); basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("a vector-margin rescue is a score comparison; basis = %q, want %q", basis, contextfabric.CommitBasisStatistical)
	}
}

// TestChaos4085_SeparatedTopUnderTruncationStillRescues is the other
// narrowness half: truncation ALONE was already tolerated by the rescue on
// purpose (see its own doc comment), so a truncated search whose ranking
// genuinely discriminated must be unaffected. Only the CONJUNCTION is
// refused.
func TestChaos4085_SeparatedTopUnderTruncationStillRescues(t *testing.T) {
	top := corroborationCandidate("sep_top", 0.60, contextfabric.MatchLexical, contextfabric.MatchVector)
	second := corroborationCandidate("sep_second", 0.50, contextfabric.MatchLexical, contextfabric.MatchVector)
	similarities := map[string]float64{
		SubjectKey(top.Subject):    0.95,
		SubjectKey(second.Subject): 0.40,
	}

	resolution, bases := resolveWithBasis(true /* searchTruncated */, similarities, 0.25, false, nil, nil, top, second)

	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "sep_top" {
		t.Fatalf("a truncated search with a strictly separated top must still rescue, got %v", resolution.Committed)
	}
	if basis := bases.For(resolution.Committed[0]); basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("basis = %q, want %q", basis, contextfabric.CommitBasisStatistical)
	}
}

// TestChaos4085_TiedTopAtTheExactIdentityBandIsLeftToItsOwnRules pins the
// <1 conjunct: two candidates both at Confidence 1 are a duplicate-identity
// collision, which len(exactIndex) < 2 and identityCollision already refuse
// for reasons of their own. tiedStatisticalTopUnderTruncation must not
// claim that population, so the refusal stays attributable to one rule.
func TestChaos4085_TiedTopAtTheExactIdentityBandIsLeftToItsOwnRules(t *testing.T) {
	if tiedStatisticalTopUnderTruncation([]contextfabric.SubjectCandidate{
		corroborationCandidate("one", 1, contextfabric.MatchExact),
		corroborationCandidate("two", 1, contextfabric.MatchExact),
	}, []int{0, 1}, true) {
		t.Fatal("a tie at the 1.0 identity band is not this rule's population")
	}
}

// TestChaos4085_IdentityFastPathIsTheOnlyAuthoritativeBasis is sol@xhigh
// change 2's central pin: the SAME candidate -- same mechanism set, same
// Confidence of 1 -- is AUTHORITATIVE when the identity universe was
// completely enumerated and merely STATISTICAL when it was not. No
// consumer could tell these apart from the resolution alone, which is
// exactly why the basis is recorded at the commit site.
func TestChaos4085_IdentityFastPathIsTheOnlyAuthoritativeBasis(t *testing.T) {
	repo := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_repo_padding",
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectRepository, CanonicalID: "repository:alpha", Label: "alpha",
		},
		State:           contextfabric.ResolutionProposed,
		MatchReasons:    []string{"alias"},
		Confidence:      1,
		MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchAlias},
	}

	complete, completeBases := resolveWithBasis(false, nil, 0, true /* aliasIdentityComplete */, nil, nil, repo)
	if len(complete.Committed) != 1 {
		t.Fatalf("a complete, unrivalled keyed identity must commit, got %v", complete.Committed)
	}
	if basis := completeBases.For(complete.Committed[0]); basis != contextfabric.CommitBasisAuthoritativeIdentity {
		t.Fatalf("complete enumeration: basis = %q, want %q", basis, contextfabric.CommitBasisAuthoritativeIdentity)
	}

	// The SAME candidate, with only the completeness proof withdrawn, does
	// not commit at all: CHAOS-3884's identityTrustUnproven independently
	// blocks it at lone_floor. That pre-existing behavior is what this
	// assertion pins -- the important property for CHAOS-4085 is that
	// nothing anywhere records CommitBasisAuthoritativeIdentity for it.
	incomplete, incompleteBases := resolveWithBasis(false, nil, 0, false /* aliasIdentityComplete */, nil, nil, repo)
	if len(incomplete.Committed) != 0 {
		t.Fatalf("an identity claim over an incompletely-read universe must not commit, got %v", incomplete.Committed)
	}
	if len(incompleteBases) != 0 {
		t.Fatalf("nothing committed, so no basis may be recorded, got %v", incompleteBases)
	}

	// A non-identity candidate clearing the lone-candidate floor is the
	// contrasting STATISTICAL commit: same single-candidate pool, same
	// gate, but selected by a score rather than by a proven identity.
	lexical := corroborationCandidate("lexical_lone", 0.80, contextfabric.MatchLexical)
	scored, scoredBases := resolveWithBasis(false, nil, 0, false, nil, nil, lexical)
	if len(scored.Committed) != 1 {
		t.Fatalf("a lone candidate above LoneFloor must commit, got %v", scored.Committed)
	}
	if basis := scoredBases.For(scored.Committed[0]); basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("a confidence floor is a score comparison: basis = %q, want %q", basis, contextfabric.CommitBasisStatistical)
	}
}

// TestChaos4085_ExactLabelTierIsStatistical pins the other half of change
// 2: MatchExact at Confidence 1 is not, on its own, an identity proof --
// resolution.go's own exactIndex doc comment concedes the duplicate-label-
// behind-the-truncation-boundary hazard it cannot close.
func TestChaos4085_ExactLabelTierIsStatistical(t *testing.T) {
	labelled := corroborationCandidate("exact_label", 1, contextfabric.MatchExact, contextfabric.MatchLexical)

	resolution, bases := resolveWithBasis(false, nil, 0, false, nil, nil, labelled)

	if len(resolution.Committed) != 1 {
		t.Fatalf("the exact-label tier must still commit exactly as before, got %v", resolution.Committed)
	}
	if basis := bases.For(resolution.Committed[0]); basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("exact LABEL equality is not an identity proof: basis = %q, want %q", basis, contextfabric.CommitBasisStatistical)
	}
}

// TestChaos4085_PreCommittedCallerHintIsProven pins the caller-canonical-id
// basis on the arrival state resolve.go's SubjectHint branch produces: a
// candidate that is already State==Committed at Confidence 1 was named by
// canonical id, re-read by keyed lookup and re-authorized before it got
// here.
func TestChaos4085_PreCommittedCallerHintIsProven(t *testing.T) {
	hinted := corroborationCandidate("hinted", 1, contextfabric.MatchExact)
	hinted.State = contextfabric.ResolutionCommitted

	resolution, bases := resolveWithBasis(true /* even under truncation */, nil, 0, false, nil, nil, hinted)

	if len(resolution.Committed) != 1 {
		t.Fatalf("a caller-hinted subject commits regardless of truncation, got %v", resolution.Committed)
	}
	if basis := bases.For(resolution.Committed[0]); basis != contextfabric.CommitBasisCallerCanonicalID {
		t.Fatalf("basis = %q, want %q", basis, contextfabric.CommitBasisCallerCanonicalID)
	}
}

// TestChaos4085_FinalizeExactResolutionRecordsCallerCanonicalIDForEverySubject
// covers the SECOND commit exit -- resolve.go's caller-hint short circuit,
// which never reaches the gate function at all. An unrecorded basis there
// would be safe (unknown reads strict) but would refuse exactly the
// commits carrying the strongest proof in the system.
func TestChaos4085_FinalizeExactResolutionRecordsCallerCanonicalIDForEverySubject(t *testing.T) {
	first := corroborationCandidate("hint_one", 1, contextfabric.MatchExact)
	second := corroborationCandidate("hint_two", 1, contextfabric.MatchExact)
	bySubject := map[string]contextfabric.SubjectCandidate{
		SubjectKey(first.Subject):  first,
		SubjectKey(second.Subject): second,
	}
	callerSourced := map[string]bool{
		SubjectKey(first.Subject):  true,
		SubjectKey(second.Subject): true,
	}

	resolution, bases := FinalizeExactResolutionWithBasis(bySubject, callerSourced, 10)

	if len(resolution.Committed) != 2 {
		t.Fatalf("both hinted subjects must commit, got %v", resolution.Committed)
	}
	for _, subject := range resolution.Committed {
		if basis := bases.For(subject); basis != contextfabric.CommitBasisCallerCanonicalID {
			t.Fatalf("%s: basis = %q, want %q", subject.CanonicalID, basis, contextfabric.CommitBasisCallerCanonicalID)
		}
	}
}

// TestChaos4085_BasisIsEmptyWhenNothingCommits pins the absence case: an
// ambiguous resolution records no basis at all, so a stale entry can never
// be read back for a subject nothing committed.
func TestChaos4085_BasisIsEmptyWhenNothingCommits(t *testing.T) {
	_, bases := resolveWithBasis(true, nil, 0, false, nil, nil,
		corroborationCandidate("amb_a", 0.50, contextfabric.MatchLexical),
		corroborationCandidate("amb_b", 0.50, contextfabric.MatchLexical),
	)
	if len(bases) != 0 {
		t.Fatalf("an ambiguous resolution must record no basis, got %v", bases)
	}
}

// TestChaos4085_BasisDiscardingWrappersStayBehaviourallyIdentical pins the
// seam that keeps the ~30 pre-existing call sites of the old signature
// meaningful: the wrapper must return exactly what the basis-carrying
// implementation returns, or those tests would silently be exercising a
// different function from production.
func TestChaos4085_BasisDiscardingWrappersStayBehaviourallyIdentical(t *testing.T) {
	candidates := tiedTopCandidates()
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}

	viaWrapper := ResolveFromMergedCandidatesWithGate(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
		tiedTopSimilarities(), 0.25, false, 10, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, nil, "", "")
	viaBasis, _ := ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
		tiedTopSimilarities(), 0.25, false, 10, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, nil, "", "")

	if len(viaWrapper.Committed) != len(viaBasis.Committed) {
		t.Fatalf("wrapper committed %v, implementation committed %v", viaWrapper.Committed, viaBasis.Committed)
	}
	for i := range viaWrapper.Committed {
		if viaWrapper.Committed[i] != viaBasis.Committed[i] {
			t.Fatalf("wrapper/implementation disagree at %d: %v vs %v", i, viaWrapper.Committed[i], viaBasis.Committed[i])
		}
	}
}
