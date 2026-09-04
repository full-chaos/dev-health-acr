package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// resolveWithBasis is the CHAOS-4085 counterpart to resolveOne: it returns
// BOTH the resolution and the CommitBasisSet recorded at the commit site,
// which is the whole point of these tests -- the basis is not derivable
// from the resolution, so nothing but the second return value can prove it.
func resolveWithBasis(searchTruncated bool, vectorArmSimilarity map[string]float64, threshold float64, aliasIdentityComplete bool, identity identityClaimants, terms identityMatchTerms, candidates ...contextfabric.SubjectCandidate) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet) {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}
	return ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, searchTruncated,
		vectorArmSimilarity, threshold, false, 10, 20, true,
		DefaultCommitGatePolicy(), identity, terms, aliasIdentityComplete, nil, "", "", false, false, nil)
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
	resolution, bases, _ := resolveWithBasis(true /* searchTruncated */, tiedTopSimilarities(), 0.25, false, nil, nil, tiedTopCandidates()...)

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
	resolution, bases, _ := resolveWithBasis(false /* searchTruncated */, tiedTopSimilarities(), 0.25, false, nil, nil, tiedTopCandidates()...)

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

	resolution, bases, _ := resolveWithBasis(true /* searchTruncated */, similarities, 0.25, false, nil, nil, top, second)

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

	complete, completeBases, _ := resolveWithBasis(false, nil, 0, true /* aliasIdentityComplete */, nil, nil, repo)
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
	incomplete, incompleteBases, _ := resolveWithBasis(false, nil, 0, false /* aliasIdentityComplete */, nil, nil, repo)
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
	scored, scoredBases, _ := resolveWithBasis(false, nil, 0, false, nil, nil, lexical)
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

	resolution, bases, _ := resolveWithBasis(false, nil, 0, false, nil, nil, labelled)

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

	resolution, bases, _ := resolveWithBasis(true /* even under truncation */, nil, 0, false, nil, nil, hinted)

	if len(resolution.Committed) != 1 {
		t.Fatalf("a caller-hinted subject commits regardless of truncation, got %v", resolution.Committed)
	}
	if basis := bases.For(resolution.Committed[0]); basis != contextfabric.CommitBasisCallerCanonicalID {
		t.Fatalf("basis = %q, want %q", basis, contextfabric.CommitBasisCallerCanonicalID)
	}
}

// TestChaos4085_ExactHintShortCircuitRecordsBasisPerClass covers the SECOND
// commit exit -- resolve.go's caller-hint short circuit, which never
// reaches the gate function at all -- and specifically the MIXED request
// codex xhigh review round 3's HIGH names.
//
// The short circuit fires when at least ONE caller-explicit hint resolved,
// and then commits every retained candidate, including receipt-derived ones
// that merely rode along. Those two classes do not carry the same proof: a
// receipt names an identity chosen in an EARLIER turn, which may itself
// have been an engine-proposed, statistically-ranked candidate. Stamping it
// proven would launder a prior statistical guess into an exemption one
// request later.
func TestChaos4085_ExactHintShortCircuitRecordsBasisPerClass(t *testing.T) {
	explicit := corroborationCandidate("hint_explicit", 1, contextfabric.MatchExact)
	fromReceipt := corroborationCandidate("hint_receipt", 1, contextfabric.MatchExact)
	bySubject := map[string]contextfabric.SubjectCandidate{
		SubjectKey(explicit.Subject):    explicit,
		SubjectKey(fromReceipt.Subject): fromReceipt,
	}
	// Exactly resolve.go's own bookkeeping: only a hint whose Source is NOT
	// prior_subject_receipt is marked caller-sourced.
	callerSourced := map[string]bool{SubjectKey(explicit.Subject): true}

	resolution, bases, digests := FinalizeExactResolutionWithBasis(bySubject, callerSourced, 10)

	if len(resolution.Committed) != 2 {
		t.Fatalf("both hinted subjects still commit, got %v", resolution.Committed)
	}
	if basis := bases.For(explicit.Subject); basis != contextfabric.CommitBasisCallerCanonicalID {
		t.Fatalf("a canonical id the caller stated in THIS request: basis = %q, want %q", basis, contextfabric.CommitBasisCallerCanonicalID)
	}
	if basis := bases.For(fromReceipt.Subject); basis != contextfabric.CommitBasisStatistical {
		t.Fatalf("a receipt-derived rider must not be exempted: basis = %q, want %q", basis, contextfabric.CommitBasisStatistical)
	}
	// CHAOS-4087: the wire-safe digest must agree with the internal basis
	// at the SAME call site -- IdentityProven derived from each subject's
	// own basis, both stamped with the caller-hint short-circuit's own
	// commit gate name.
	if d := digests.For(explicit.Subject); d.CommitGate != "caller_hint_short_circuit" || !d.IdentityProven {
		t.Fatalf("explicit hint digest = %+v, want CommitGate=caller_hint_short_circuit, IdentityProven=true", d)
	}
	if d := digests.For(fromReceipt.Subject); d.CommitGate != "caller_hint_short_circuit" || d.IdentityProven {
		t.Fatalf("receipt-derived digest = %+v, want CommitGate=caller_hint_short_circuit, IdentityProven=false", d)
	}
}

// TestChaos4085_BasisIsEmptyWhenNothingCommits pins the absence case: an
// ambiguous resolution records no basis at all, so a stale entry can never
// be read back for a subject nothing committed.
func TestChaos4085_BasisIsEmptyWhenNothingCommits(t *testing.T) {
	_, bases, _ := resolveWithBasis(true, nil, 0, false, nil, nil,
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
	viaBasis, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
		tiedTopSimilarities(), 0.25, false, 10, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, nil, "", "", false, false, nil)

	if len(viaWrapper.Committed) != len(viaBasis.Committed) {
		t.Fatalf("wrapper committed %v, implementation committed %v", viaWrapper.Committed, viaBasis.Committed)
	}
	for i := range viaWrapper.Committed {
		if viaWrapper.Committed[i] != viaBasis.Committed[i] {
			t.Fatalf("wrapper/implementation disagree at %d: %v vs %v", i, viaWrapper.Committed[i], viaBasis.Committed[i])
		}
	}
}

// TestChaos4085_DecisionTraceCarriesBasisAndTieFlags is the observability
// pin for the team-lead addition (2026-08-22): the CHAOS-4085
// investigation could not attribute the wrong commit from trace at all --
// establishing what happened needed captured model-exchange transcripts,
// because nothing said which class of proof the commit stood on.
//
// A decision-stage event must now answer that on its own: gate, basis,
// mechanism, tie, truncation.
func TestChaos4085_DecisionTraceCarriesBasisAndTieFlags(t *testing.T) {
	t.Run("committed carries its basis", func(t *testing.T) {
		tracer := &recordingTracer{}
		lone := corroborationCandidate("traced_lone", 0.80, contextfabric.MatchLexical)
		bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(lone.Subject): lone}
		ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-basis", "", false, false, nil)

		event, ok := tracer.decision()
		if !ok {
			t.Fatal("no decision-stage event emitted")
		}
		if event.Outcome != "committed" || event.CommitGate != "lone_floor" {
			t.Fatalf("event = %+v, want a lone_floor commit", event)
		}
		if event.CommitBasis != string(contextfabric.CommitBasisStatistical) {
			t.Fatalf("CommitBasis = %q, want %q", event.CommitBasis, contextfabric.CommitBasisStatistical)
		}
		if event.TiedStatisticalTop {
			t.Fatal("a single candidate cannot be tied")
		}
	})

	t.Run("the tied-rescue refusal is countable from trace", func(t *testing.T) {
		tracer := &recordingTracer{}
		candidates := tiedTopCandidates()
		bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
		for _, candidate := range candidates {
			bySubject[SubjectKey(candidate.Subject)] = candidate
		}
		ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, true, /* searchTruncated */
			tiedTopSimilarities(), 0.25, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-tied", "", false, false, nil)

		event, ok := tracer.decision()
		if !ok {
			t.Fatal("no decision-stage event emitted")
		}
		// This exact conjunction IS the refusal, with no downstream
		// reconstruction required.
		if !(event.Outcome == "ambiguous" && event.TiedStatisticalTop && event.SearchTruncated && event.CommitGate == "") {
			t.Fatalf("the tied-rescue refusal must be directly countable, got %+v", event)
		}
		if event.CommitBasis != "" {
			t.Fatalf("nothing committed, so no basis may be traced, got %q", event.CommitBasis)
		}
	})

	t.Run("a tie WITHOUT truncation stays distinguishable", func(t *testing.T) {
		tracer := &recordingTracer{}
		candidates := tiedTopCandidates()
		bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
		for _, candidate := range candidates {
			bySubject[SubjectKey(candidate.Subject)] = candidate
		}
		ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, /* searchTruncated */
			tiedTopSimilarities(), 0.25, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-untied", "", false, false, nil)

		event, ok := tracer.decision()
		if !ok {
			t.Fatal("no decision-stage event emitted")
		}
		// Tied, not truncated, and it still commits -- the population the
		// refusal deliberately does NOT claim. A single "refused" boolean
		// would have made this indistinguishable from the case above by
		// absence alone.
		if !event.TiedStatisticalTop || event.SearchTruncated || event.Outcome != "committed" {
			t.Fatalf("a tie without truncation must stay separable and still commit, got %+v", event)
		}
	})
}

// recordingTracer captures decision-stage events for the trace pins above.
type recordingTracer struct{ events []ResolutionTraceEvent }

func (r *recordingTracer) Trace(event ResolutionTraceEvent) { r.events = append(r.events, event) }

func (r *recordingTracer) decision() (ResolutionTraceEvent, bool) {
	for _, event := range r.events {
		if event.Stage == "decision" {
			return event, true
		}
	}
	return ResolutionTraceEvent{}, false
}
