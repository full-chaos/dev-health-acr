package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestEvidenceStrength pins design brief v6 §1.4's own worked example
// ("0.50 -> 0.755 >= LoneFloor") and the surrounding guarantees
// CorroboratedConfidence's formula already carries: never below base
// ("corroboration is evidence FOR a candidate; it must never cost one
// confidence" -- mechanism.go), and base>=1 returns 1 unchanged.
func TestEvidenceStrength(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base float64
		want float64
	}{
		{"brief's own worked example", 0.50, 0.755},
		{"zero base still reaches CorroboratedFloor", 0, CorroboratedFloor},
		{"already-exact base returns 1 unchanged", 1, 1},
		{"above-1 base clamps to 1", 1.5, 1},
		{"a base already above CorroboratedCeiling is never demoted", 0.95, 0.95},
		{"negative base clamps to 0 first", -1, CorroboratedFloor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := evidenceStrength(tc.base); got != tc.want {
				t.Fatalf("evidenceStrength(%v) = %v, want %v", tc.base, got, tc.want)
			}
		})
	}
}

// TestEvidenceStrengthMatchesCorroboratedConfidenceAtDistinctTwo pins the
// EXACT equivalence evidenceStrength's own doc comment claims: it is
// CorroboratedConfidence(mechanisms, base) for any two-distinct-mechanism
// set, evaluated without ever needing a real mechanism pair.
func TestEvidenceStrengthMatchesCorroboratedConfidenceAtDistinctTwo(t *testing.T) {
	t.Parallel()
	twoMechanisms := []contextfabric.MatchMechanism{contextfabric.MatchLexical, contextfabric.MatchVector}
	for _, base := range []float64{0, 0.1, 0.3, 0.5, 0.7, 0.9} {
		want := CorroboratedConfidence(twoMechanisms, base)
		if got := evidenceStrength(base); got != want {
			t.Fatalf("evidenceStrength(%v) = %v, want CorroboratedConfidence(2 mechanisms, %v) = %v", base, got, base, want)
		}
	}
}

func TestIndexBySubjectKey(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:1"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:2"}},
	}
	if index, ok := indexBySubjectKey(candidates, SubjectKey(candidates[1].Subject)); !ok || index != 1 {
		t.Fatalf("indexBySubjectKey(second) = (%d, %v), want (1, true)", index, ok)
	}
	if _, ok := indexBySubjectKey(candidates, "nonexistent"); ok {
		t.Fatal("indexBySubjectKey(unknown key) = ok, want not-found")
	}
	if _, ok := indexBySubjectKey(nil, "anything"); ok {
		t.Fatal("indexBySubjectKey(nil candidates) = ok, want not-found")
	}
}

// TestAttestedSatisfier exercises attestedSatisfier's own structural
// re-derivation of RunShadowEvidenceRound's would-commit predicate (brief
// §1.4: "censusComplete && |satisfiers| == 1 names one source row S") --
// it must not trust Attestation.Outcome alone.
func TestAttestedSatisfier(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		attestation Attestation
		wantOK      bool
		wantKind    contextfabric.SubjectKind
		wantID      string
	}{
		{
			name: "decisive single bridged satisfier",
			attestation: Attestation{
				Outcome: ShadowWouldCommit, UnscopedVisibility: true,
				Kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532"}},
			},
			wantOK: true, wantKind: contextfabric.SubjectPullRequest, wantID: "pull_request:repo-1:532",
		},
		{
			// codex xhigh review finding (LOW, defense-in-depth): even an
			// otherwise-decisive attestation must not qualify without
			// UnscopedVisibility -- no live bypass exists today
			// (RunShadowEvidenceRound already refuses to run the census at
			// all for a scoped caller), but this function is the ONE gate
			// deciding whether an Attestation drives a real commit, so it
			// asserts the invariant itself.
			name: "UnscopedVisibility=false refuses an otherwise-decisive attestation",
			attestation: Attestation{
				Outcome: ShadowWouldCommit, UnscopedVisibility: false,
				Kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532"}},
			},
			wantOK: false,
		},
		{
			name:        "would_clarify never qualifies",
			attestation: Attestation{Outcome: ShadowWouldClarify},
			wantOK:      false,
		},
		{
			name:        "would_no_match never qualifies",
			attestation: Attestation{Outcome: ShadowWouldNoMatch},
			wantOK:      false,
		},
		{
			name: "unbridged satisfier (empty SatisfierCanonicalID) never qualifies",
			attestation: Attestation{
				Outcome: ShadowWouldCommit,
				Kinds:   []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: ""}},
			},
			wantOK: false,
		},
		{
			name: "closure mismatch never qualifies",
			attestation: Attestation{
				Outcome: ShadowWouldCommit,
				Kinds:   []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532", ClosureMismatch: true}},
			},
			wantOK: false,
		},
		{
			name: "incomplete kind never qualifies",
			attestation: Attestation{
				Outcome: ShadowWouldCommit,
				Kinds:   []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: false, Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532"}},
			},
			wantOK: false,
		},
		{
			name: "a second multi-satisfier kind poisons the whole attestation",
			attestation: Attestation{
				Outcome: ShadowWouldCommit,
				Kinds: []KindAttestation{
					{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532"},
					{Kind: contextfabric.SubjectWorkItem, Complete: true, Count: 3},
				},
			},
			wantOK: false,
		},
		{
			name:        "zero value",
			attestation: Attestation{},
			wantOK:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind, id, ok := attestedSatisfier(tc.attestation)
			if ok != tc.wantOK {
				t.Fatalf("attestedSatisfier() ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if kind != tc.wantKind || id != tc.wantID {
				t.Fatalf("attestedSatisfier() = (%q, %q), want (%q, %q)", kind, id, tc.wantKind, tc.wantID)
			}
		})
	}
}

// TestResolveFromMergedCandidatesWithGate_EvidenceCensus is a direct,
// resolution.go-level table over the evidence_census rescue's own conjuncts
// (design brief v6 §1.4/R5), mirroring resolution_gate_policy_test.go's own
// style -- cheaper and more precise than driving every guard through the
// full ResolveSubjects pipeline. Every case starts from an otherwise-stalled
// single candidate (searchTruncated=true, base confidence 0.50, no
// mechanism) that the ordinary gates already leave ambiguous, and varies
// exactly one evidence_census precondition at a time.
func TestResolveFromMergedCandidatesWithGate_EvidenceCensus(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532", Label: "PR #532"}
	key := SubjectKey(subject)
	baseCandidates := func() map[string]contextfabric.SubjectCandidate {
		return map[string]contextfabric.SubjectCandidate{
			key: {Subject: subject, State: contextfabric.ResolutionProposed, Confidence: 0.50, MatchReasons: []string{"stalled"}},
		}
	}

	t.Run("commits when every conjunct holds", func(t *testing.T) {
		t.Parallel()
		resolution := ResolveFromMergedCandidatesWithGate(baseCandidates(), map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", key)
		if len(resolution.Committed) != 1 || SubjectKey(resolution.Committed[0]) != key {
			t.Fatalf("resolution.Committed = %#v, want exactly %q committed", resolution.Committed, key)
		}
	})

	t.Run("empty evidenceCensusAttestedKey disables the rescue (byte-identical to before Slice C)", func(t *testing.T) {
		t.Parallel()
		resolution := ResolveFromMergedCandidatesWithGate(baseCandidates(), map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", "")
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %#v, want none (evidence_census must stay off for the default \"\" key)", resolution.Committed)
		}
	})

	t.Run("an invalid gate disables evidence_census exactly like every other commit path", func(t *testing.T) {
		t.Parallel()
		invalid := CommitGatePolicy{} // fails Validate(): LoneFloor==0
		resolution := ResolveFromMergedCandidatesWithGate(baseCandidates(), map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, invalid, nil, nil, false, nil, "", key)
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %#v, want none under an invalid gate", resolution.Committed)
		}
	})

	t.Run("a key naming no candidate in the pool never commits anything", func(t *testing.T) {
		t.Parallel()
		resolution := ResolveFromMergedCandidatesWithGate(baseCandidates(), map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", "pull_request\x00pull_request:repo-1:does-not-exist")
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %#v, want none for an unmatched attested key", resolution.Committed)
		}
	})

	t.Run("AC-3778-3 pin retained: a vector-only candidate never commits via evidence_census", func(t *testing.T) {
		t.Parallel()
		candidates := map[string]contextfabric.SubjectCandidate{
			key: {Subject: subject, State: contextfabric.ResolutionProposed, Confidence: 0.50, MatchReasons: []string{"stalled"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchVector}},
		}
		resolution := ResolveFromMergedCandidatesWithGate(candidates, map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", key)
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %#v, want none -- a vector-only base must never commit via evidence_census (AC-3778-3, pin retained)", resolution.Committed)
		}
	})

	// codex xhigh review finding (HIGH, confirmed and fixed): a candidate
	// that already carries 2 REAL mechanisms (independent of the census
	// witness) gets corroborated to CorroboratedConfidence(0.50) ~= 0.755
	// by phase 2.5 -- and can still reach this rescue if searchTruncated
	// short-circuits the switch BEFORE the lone_floor case ever inspects
	// that already-boosted confidence. evidenceStrength MUST be evaluated
	// against the TRUE raw base (0.50 -> 0.755), never against phase 2.5's
	// own output (0.755 -> ~0.773 if double-applied) -- this test picks a
	// LoneFloor strictly between the two (0.76) so the two computations
	// disagree on the OUTCOME, not just the number: the double-application
	// bug would have committed here; the fix must refuse.
	t.Run("evidence strength uses the RAW base, not phase 2.5's own corroborated output, for an already-multi-mechanism candidate", func(t *testing.T) {
		t.Parallel()
		candidates := map[string]contextfabric.SubjectCandidate{
			key: {
				Subject: subject, State: contextfabric.ResolutionProposed, Confidence: 0.50,
				MatchReasons:    []string{"stalled"},
				MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical, contextfabric.MatchVector},
			},
		}
		wantRawBase := 0.50
		if got := evidenceStrength(wantRawBase); got != 0.755 {
			t.Fatalf("sanity check: evidenceStrength(%v) = %v, want 0.755", wantRawBase, got)
		}
		phase25Output := CorroboratedConfidence(candidates[key].MatchMechanisms, wantRawBase)
		if phase25Output <= wantRawBase {
			t.Fatalf("sanity check: phase-2.5 output (%v) must exceed the raw base (%v) for this scenario to be meaningful", phase25Output, wantRawBase)
		}
		doubleApplied := evidenceStrength(phase25Output)
		gate := DefaultCommitGatePolicy()
		gate.LoneFloor = (0.755 + doubleApplied) / 2 // strictly between the two candidate answers
		if gate.LoneFloor <= 0.755 || gate.LoneFloor >= doubleApplied {
			t.Fatalf("test construction error: LoneFloor %v must sit strictly between 0.755 and %v", gate.LoneFloor, doubleApplied)
		}
		resolution := ResolveFromMergedCandidatesWithGate(candidates, map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, gate, nil, nil, false, nil, "", key)
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %#v, want NONE: the raw base (0.50 -> evidenceStrength 0.755) sits below LoneFloor (%v), so evidence_census must refuse -- a non-empty Committed here means evidenceStrength was evaluated against the already-corroborated phase-2.5 output instead of the raw base", resolution.Committed, gate.LoneFloor)
		}
	})

	t.Run("a lexical mechanism (not vector-only) is unaffected by the AC-3778-3 guard", func(t *testing.T) {
		t.Parallel()
		candidates := map[string]contextfabric.SubjectCandidate{
			key: {Subject: subject, State: contextfabric.ResolutionProposed, Confidence: 0.50, MatchReasons: []string{"stalled"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical}},
		}
		resolution := ResolveFromMergedCandidatesWithGate(candidates, map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", key)
		if len(resolution.Committed) != 1 {
			t.Fatalf("resolution.Committed = %#v, want exactly 1 committed for a lexical-mechanism candidate", resolution.Committed)
		}
	})

	t.Run("a duplicate exact-label collision (len(exactIndex)>=2) refuses evidence_census", func(t *testing.T) {
		t.Parallel()
		other := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:533", Label: "PR #532"}
		candidates := map[string]contextfabric.SubjectCandidate{
			key:               {Subject: subject, State: contextfabric.ResolutionProposed, Confidence: 1, MatchedTerms: []string{"PR #532"}, MatchReasons: []string{"exact"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchExact}},
			SubjectKey(other): {Subject: other, State: contextfabric.ResolutionProposed, Confidence: 1, MatchedTerms: []string{"PR #532"}, MatchReasons: []string{"exact"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchExact}},
		}
		resolution := ResolveFromMergedCandidatesWithGate(candidates, map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", key)
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %#v, want none -- two colliding exact matches must stay irreducibly ambiguous, not arbitrated by evidence_census", resolution.Committed)
		}
	})

	t.Run("a known identity collision refuses evidence_census", func(t *testing.T) {
		t.Parallel()
		claimants := identityClaimants{
			identityKeyClassAlias: {"acme": {key: true, "pull_request\x00other": true}},
		}
		terms := identityMatchTerms{key: {{class: identityKeyClassAlias, term: "acme"}}}
		resolution := ResolveFromMergedCandidatesWithGate(baseCandidates(), map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), claimants, terms, false, nil, "", key)
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %#v, want none under a known identity collision", resolution.Committed)
		}
	})

	t.Run("commitGate is recorded as evidence_census and traced", func(t *testing.T) {
		t.Parallel()
		tracer := &captureResolutionTracer{}
		resolution := ResolveFromMergedCandidatesWithGate(baseCandidates(), map[string]string{}, map[string]bool{}, 10, true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-evidence-census", key)
		if len(resolution.Committed) != 1 {
			t.Fatalf("resolution.Committed = %#v, want exactly 1", resolution.Committed)
		}
		events := tracer.eventsForStage("decision")
		if len(events) != 1 || events[0].CommitGate != "evidence_census" {
			t.Fatalf("decision events = %#v, want exactly 1 with CommitGate=evidence_census", events)
		}
		if events[0].Outcome != "committed" {
			t.Fatalf("decision event Outcome = %q, want %q", events[0].Outcome, "committed")
		}
	})
}
