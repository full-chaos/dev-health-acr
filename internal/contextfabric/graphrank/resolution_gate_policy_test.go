package graphrank

import (
	"math"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// This file's tests are additive-only (CHAOS-3857 gate-threshold sweep
// parameterization): they exercise CommitGatePolicy/DefaultCommitGatePolicy/
// Validate and ResolveFromMergedCandidatesWithGate, the new surfaces
// ResolveFromMergedCandidates now delegates to. No existing test in this
// package is modified -- corroboration_test.go's loneCommitGate/topOfTwoGate
// constants and every call to ResolveFromMergedCandidates there are
// untouched and still green.

// resolveWithGate is this file's own thin helper, mirroring corroboration_test.go's
// resolveOne but taking an explicit gate -- kept local to this file (not
// added to corroboration_test.go, which stays unmodified per the sol
// review's hard requirement).
func resolveWithGate(gate CommitGatePolicy, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, c := range candidates {
		bySubject[SubjectKey(c.Subject)] = c
	}
	return ResolveFromMergedCandidatesWithGate(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, gate, nil, nil, false, nil, "")
}

// TestDefaultCommitGatePolicyMatchesCalibratedProductionValues pins the
// three calibrated thresholds this whole parameterization must never
// silently drift from. corroboration_test.go's own loneCommitGate (0.72)/
// topOfTwoGate (0.88) constants independently pin two of the same three
// numbers from the OUTSIDE (a candidate confidence test), so a
// DefaultCommitGatePolicy() value drifting from either would ALSO break
// those pre-existing, unmodified tests -- this test pins the third
// (TopGap, 0.12) directly, since no existing test isolates it.
func TestDefaultCommitGatePolicyMatchesCalibratedProductionValues(t *testing.T) {
	got := DefaultCommitGatePolicy()
	want := CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.88, TopGap: 0.12}
	if got != want {
		t.Fatalf("DefaultCommitGatePolicy() = %+v, want %+v", got, want)
	}
}

// TestShippedTopOfTwoGapIsIndependentlyPinnedAt012 is sol review F3's
// independent literal pin: unlike loneCommitGate/topOfTwoGate in
// corroboration_test.go (which duplicate 0.72/0.88 as hand-written
// literals to position OTHER assertions against them), nothing in this
// package independently re-derives the shipped TopGap outside
// DefaultCommitGatePolicy() itself -- so a test built FROM
// DefaultCommitGatePolicy().TopGap would be circular, proving nothing
// about whether 0.12 is really what ships. This test hardcodes 0.12
// directly (never reads DefaultCommitGatePolicy()) and proves it via real
// candidate confidences: a gap of exactly 0.12 commits, a gap of
// 0.119999... (one float64 ULP under) does not.
func TestShippedTopOfTwoGapIsIndependentlyPinnedAt012(t *testing.T) {
	const independentTopGapLiteral = 0.12
	top := corroborationCandidate("top", 0.90)
	atGap := resolveWithGate(DefaultCommitGatePolicy(), top, corroborationCandidate("second-at-gap", 0.90-independentTopGapLiteral))
	if len(atGap.Committed) != 1 {
		t.Fatalf("gap == 0.12 (independently hardcoded): Committed = %v, want exactly one commit", atGap.Committed)
	}
	belowGap := resolveWithGate(DefaultCommitGatePolicy(), top, corroborationCandidate("second-below-gap", 0.90-independentTopGapLiteral+0.001))
	if len(belowGap.Committed) != 0 {
		t.Fatalf("gap == 0.119 (just under 0.12): Committed = %v, want none", belowGap.Committed)
	}
}

// TestCommitGateGeometry is sol review F3's table-driven sweep across
// EVERY commit-decision shape this gate governs, including the EXACT
// boundary values that die silently if `>=` in resolution.go ever becomes
// `>`. For every case run "at default", it also asserts the
// byte-identical-behavior proof (F3: compare the FULL SubjectResolution,
// not just Committed) that ResolveFromMergedCandidates ==
// ResolveFromMergedCandidatesWithGate(..., DefaultCommitGatePolicy()) --
// superseding the narrower, Committed-only proof an earlier round of this
// file had.
func TestCommitGateGeometry(t *testing.T) {
	type tc struct {
		name          string
		candidates    []contextfabric.SubjectCandidate
		gate          CommitGatePolicy
		useDefault    bool // when true, also proves wrapper equality against this case
		wantCommitted int  // 0 or 1 -- every case here commits at most one subject
	}
	cases := []tc{
		{
			name:          "lone candidate well above LoneFloor commits",
			candidates:    []contextfabric.SubjectCandidate{corroborationCandidate("solo", 0.90)},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 1,
		},
		{
			name:          "lone candidate EXACTLY AT LoneFloor commits (>=, not >)",
			candidates:    []contextfabric.SubjectCandidate{corroborationCandidate("solo-at-floor", 0.72)},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 1,
		},
		{
			name:          "lone candidate just below LoneFloor does not commit",
			candidates:    []contextfabric.SubjectCandidate{corroborationCandidate("solo-below-floor", 0.71)},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 0,
		},
		{
			name: "top-of-two commits when both floor and gap clear",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.95), corroborationCandidate("second", 0.70),
			},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 1,
		},
		{
			name: "top-of-two rejected by floor despite a huge gap",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.85), corroborationCandidate("second", 0.10),
			},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 0,
		},
		{
			name: "top-of-two rejected by gap despite clearing the floor",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.95), corroborationCandidate("second", 0.90),
			},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 0,
		},
		{
			name: "top-of-two commits at EXACT floor and EXACT gap boundaries (>=, not >)",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.88), corroborationCandidate("second", 0.76),
			},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 1,
		},
		{
			name: "top-of-two rejected one ULP below the floor boundary",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", math.Nextafter(0.88, 0)), corroborationCandidate("second", 0.10),
			},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 0,
		},
		{
			name: "top-of-two rejected one ULP below the gap boundary",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.90), corroborationCandidate("second", math.Nextafter(0.78, 1)),
			},
			gate:          DefaultCommitGatePolicy(),
			useDefault:    true,
			wantCommitted: 0,
		},
		// Override cases: an explicit gate reaches and changes the
		// decision relative to what DefaultCommitGatePolicy() would do on
		// the SAME candidates (no wrapper-equality check -- these are not
		// "at default").
		{
			name:          "lowered LoneFloor commits a candidate the default would leave ambiguous",
			candidates:    []contextfabric.SubjectCandidate{corroborationCandidate("solo", 0.65)},
			gate:          CommitGatePolicy{LoneFloor: 0.60, TopFloor: 0.88, TopGap: 0.12},
			wantCommitted: 1,
		},
		{
			name: "widened TopGap commits a pair the default would leave ambiguous",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.90), corroborationCandidate("second", 0.80),
			},
			gate:          CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.88, TopGap: 0.05},
			wantCommitted: 1,
		},
		{
			name: "raised TopFloor rejects a pair the default would commit",
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.90), corroborationCandidate("second", 0.70),
			},
			gate:          CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.95, TopGap: 0.12},
			wantCommitted: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			viaGate := resolveWithGate(c.gate, c.candidates...)
			if len(viaGate.Committed) != c.wantCommitted {
				t.Fatalf("Committed = %v, want %d commit(s)", viaGate.Committed, c.wantCommitted)
			}
			if !c.useDefault {
				return
			}
			bySubject := make(map[string]contextfabric.SubjectCandidate, len(c.candidates))
			for _, cand := range c.candidates {
				bySubject[SubjectKey(cand.Subject)] = cand
			}
			viaDefaultWrapper := ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true)
			if !reflect.DeepEqual(viaDefaultWrapper, viaGate) {
				t.Fatalf("ResolveFromMergedCandidates() != ResolveFromMergedCandidatesWithGate(..., DefaultCommitGatePolicy()):\n  wrapper: %+v\n  gate:    %+v", viaDefaultWrapper, viaGate)
			}
		})
	}
}

// TestCommitGatePolicyValidate is a direct unit table for Validate()
// itself, independent of any resolution behavior -- every rejection
// reason sol review F1 named, checked in isolation.
func TestCommitGatePolicyValidate(t *testing.T) {
	valid := DefaultCommitGatePolicy()
	if err := valid.Validate(); err != nil {
		t.Fatalf("DefaultCommitGatePolicy().Validate() = %v, want nil", err)
	}
	cases := []struct {
		name string
		gate CommitGatePolicy
	}{
		{"zero value", CommitGatePolicy{}},
		{"LoneFloor zero, others default", CommitGatePolicy{LoneFloor: 0, TopFloor: 0.88, TopGap: 0.12}},
		{"TopFloor zero, others default", CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0, TopGap: 0.12}},
		{"TopGap zero, others default", CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.88, TopGap: 0}},
		{"LoneFloor negative", CommitGatePolicy{LoneFloor: -0.1, TopFloor: 0.88, TopGap: 0.12}},
		{"LoneFloor above 1", CommitGatePolicy{LoneFloor: 1.1, TopFloor: 0.88, TopGap: 0.12}},
		{"TopFloor above 1", CommitGatePolicy{LoneFloor: 0.72, TopFloor: 1.5, TopGap: 0.12}},
		{"TopGap above 1", CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.88, TopGap: 1.5}},
		{"LoneFloor NaN", CommitGatePolicy{LoneFloor: math.NaN(), TopFloor: 0.88, TopGap: 0.12}},
		{"TopFloor +Inf", CommitGatePolicy{LoneFloor: 0.72, TopFloor: math.Inf(1), TopGap: 0.12}},
		{"TopGap -Inf", CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.88, TopGap: math.Inf(-1)}},
		{"TopGap equals TopFloor", CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.88, TopGap: 0.88}},
		{"TopGap exceeds TopFloor", CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.5, TopGap: 0.6}},
		{"LoneFloor exceeds TopFloor", CommitGatePolicy{LoneFloor: 0.95, TopFloor: 0.88, TopGap: 0.12}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.gate.Validate(); err == nil {
				t.Fatalf("%+v.Validate() = nil, want a rejection", c.gate)
			}
		})
	}
}

// TestResolveFromMergedCandidatesWithGateCommitsNothingForAnInvalidPolicy
// is sol review F1's exact scenario, tested DIRECTLY at the evaluator
// layer (not just the env boundary -- falkorgraph's
// TestEmbedderFromEnv_CrossFieldInvalidCommitGateErrorsLoudlyNamingBothFields
// covers that layer; this one proves the evaluator ITSELF fails closed
// even when handed an invalid policy through some OTHER path that never
// went through EmbedderFromEnv's validation at all -- exactly the gap sol
// found: "the exported evaluator has NO guard -- safety rests on one call
// site").
//
// Both candidate sets used here would UNAMBIGUOUSLY commit under
// DefaultCommitGatePolicy() (a lone candidate at 0.99, and a top-of-two
// pair at 0.99/0.10) -- proving the invalid policy suppresses a commit
// that would otherwise be a slam dunk, not merely one that was already
// borderline.
func TestResolveFromMergedCandidatesWithGateCommitsNothingForAnInvalidPolicy(t *testing.T) {
	cases := []struct {
		name       string
		gate       CommitGatePolicy
		candidates []contextfabric.SubjectCandidate
	}{
		{
			name:       "partial zero: LoneFloor=0 alone (TopFloor/TopGap at calibrated defaults)",
			gate:       CommitGatePolicy{LoneFloor: 0, TopFloor: 0.88, TopGap: 0.12},
			candidates: []contextfabric.SubjectCandidate{corroborationCandidate("solo", 0.99)},
		},
		{
			name: "partial zero: TopFloor=0 and TopGap=0 (LoneFloor at calibrated default)",
			gate: CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0, TopGap: 0},
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.99), corroborationCandidate("second", 0.10),
			},
		},
		{
			name:       "cross-field invalid: LoneFloor > TopFloor",
			gate:       CommitGatePolicy{LoneFloor: 0.95, TopFloor: 0.88, TopGap: 0.12},
			candidates: []contextfabric.SubjectCandidate{corroborationCandidate("solo", 0.99)},
		},
		{
			name: "cross-field invalid: TopGap >= TopFloor",
			gate: CommitGatePolicy{LoneFloor: 0.72, TopFloor: 0.5, TopGap: 0.6},
			candidates: []contextfabric.SubjectCandidate{
				corroborationCandidate("top", 0.99), corroborationCandidate("second", 0.10),
			},
		},
		{
			name:       "NaN field",
			gate:       CommitGatePolicy{LoneFloor: math.NaN(), TopFloor: 0.88, TopGap: 0.12},
			candidates: []contextfabric.SubjectCandidate{corroborationCandidate("solo", 0.99)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.gate.Validate(); err == nil {
				t.Fatalf("test setup bug: %+v.Validate() = nil, this case must use an invalid policy", c.gate)
			}
			resolution := resolveWithGate(c.gate, c.candidates...)
			if len(resolution.Committed) != 0 {
				t.Fatalf("invalid gate %+v: Committed = %v, want NONE (fail closed) even though these candidates would unambiguously commit under DefaultCommitGatePolicy()", c.gate, resolution.Committed)
			}
		})
	}
}

// TestResolveFromMergedCandidatesWithGateHonorsLoweredLoneFloor is the core
// acceptance test for the whole parameterization: a single candidate at
// confidence 0.65 sits BELOW the calibrated LoneFloor (0.72) --
// ResolveFromMergedCandidates (the default, unchanged path) must leave it
// ambiguous, exactly as it always has -- but
// ResolveFromMergedCandidatesWithGate with an EXPLICIT, lowered LoneFloor
// (0.60) must commit it. Proves the new gate parameter actually reaches and
// changes the decision, not merely that it compiles. (Also covered as one
// row of TestCommitGateGeometry above; kept standalone for its narrower,
// more readable failure message.)
func TestResolveFromMergedCandidatesWithGateHonorsLoweredLoneFloor(t *testing.T) {
	candidate := corroborationCandidate("solo", 0.65)
	atDefault := resolveWithGate(DefaultCommitGatePolicy(), candidate)
	if len(atDefault.Committed) != 0 {
		t.Fatalf("at the calibrated default, Committed = %v, want none (0.65 < 0.72)", atDefault.Committed)
	}

	lowered := DefaultCommitGatePolicy()
	lowered.LoneFloor = 0.60
	atLoweredFloor := resolveWithGate(lowered, candidate)
	if len(atLoweredFloor.Committed) != 1 || atLoweredFloor.Committed[0] != candidate.Subject {
		t.Fatalf("with LoneFloor=0.60, Committed = %v, want [%+v]", atLoweredFloor.Committed, candidate.Subject)
	}
}

// --- AC-3778-3 structural guard (CHAOS-3857) ---
//
// CHAOS-3857 swept the commit-gate thresholds and chris ratified
// LoneFloor=0.68, then REJECTED that value during ship verification on two
// counter-examples (see DefaultCommitGatePolicy's own doc comment for the
// full record): the vector-only arithmetic gap this guard closes, and a
// separate lexical wrong-commit on live infrastructure (see
// TestLexicalThreeOfFourTokenOverlapStaysAmbiguousAtTheDefaultGate below).
// LoneFloor stayed 0.72 -- the guard below is therefore PROVABLY INERT
// under DefaultCommitGatePolicy() today (vectorRelevanceCeiling=0.70 sits
// strictly below both 0.72 and 0.88, so arithmetic alone already excludes a
// vector-only candidate from either gate). It ships anyway, deliberately:
// it is the difference between "excluded by mechanism identity" and
// "excluded by today's specific threshold values", and it is what makes any
// FUTURE env-override that lowers LoneFloor below 0.70 (falkorgraph's
// ACR_CONTEXT_FABRIC_COMMIT_LONE_FLOOR) safe by construction instead of by
// someone remembering to re-check this arithmetic by hand. The tests below
// prove it is load-bearing under such an override, not dead code.
//
// Mutation-verified (recorded here since Go has no first-class mutation
// harness in this repo): temporarily forcing isVectorOnlyCandidate to
// always return false, at an overridden LoneFloor below 0.70, makes a
// vector-only candidate auto-commit that must not -- confirmed via
// TestVectorOnlyGuardBlocksLoneCommitRegardlessOfConfidence and
// TestAC_3778_3_VectorOnlyCandidateCannotReachTheLoneCommitGate
// (corroboration_test.go) both failing under that mutation.

// TestIsVectorOnlyCandidate pins the mechanism-identity rule directly,
// independent of any resolution-level side effect.
func TestIsVectorOnlyCandidate(t *testing.T) {
	cases := []struct {
		name           string
		mechanisms     []contextfabric.MatchMechanism
		wantVectorOnly bool
	}{
		{"vector alone", []contextfabric.MatchMechanism{contextfabric.MatchVector}, true},
		{"nil mechanisms", nil, false},
		{"empty mechanisms", []contextfabric.MatchMechanism{}, false},
		{"lexical alone", []contextfabric.MatchMechanism{contextfabric.MatchLexical}, false},
		{"vector + lexical (the CHAOS-3829 rescue's own corroboration pairing)", []contextfabric.MatchMechanism{contextfabric.MatchVector, contextfabric.MatchLexical}, false},
		{"vector + traversal", []contextfabric.MatchMechanism{contextfabric.MatchVector, contextfabric.MatchTraversalParent}, false},
		{"vector + exact (an exact match originally surfaced by the vector adapter)", []contextfabric.MatchMechanism{contextfabric.MatchVector, contextfabric.MatchExact}, false},
		{"unrecognized mechanism alongside vector is dropped, leaving vector-only", []contextfabric.MatchMechanism{contextfabric.MatchVector, contextfabric.MatchMechanism("semantic")}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isVectorOnlyCandidate(c.mechanisms); got != c.wantVectorOnly {
				t.Fatalf("isVectorOnlyCandidate(%v) = %v, want %v", c.mechanisms, got, c.wantVectorOnly)
			}
		})
	}
}

// TestVectorOnlyGuardBlocksLoneCommitRegardlessOfConfidence proves the guard
// is a MECHANISM-IDENTITY rule, not a confidence-band one: it holds even at
// a confidence far above anything falkorgraph's real vector adapter could
// produce (capped at vectorRelevanceCeiling=0.70), so it survives a future
// adapter change that widened the band, or an env-override that lowered
// LoneFloor below 0.70 -- exactly the scenario that makes this guard
// non-dead-code despite being inert under DefaultCommitGatePolicy() today.
func TestVectorOnlyGuardBlocksLoneCommitRegardlessOfConfidence(t *testing.T) {
	candidate := corroborationCandidate("solo", 0.99, contextfabric.MatchVector)
	resolution := resolveWithGate(DefaultCommitGatePolicy(), candidate)
	if len(resolution.Committed) != 0 {
		t.Fatalf("a vector-only candidate at confidence 0.99 must not auto-commit alone, got %v", resolution.Committed)
	}
	if resolution.Candidates[0].Confidence != 0.99 {
		t.Fatalf("the guard must not alter the candidate's own confidence, got %v", resolution.Candidates[0].Confidence)
	}
}

// TestVectorOnlyGuardBlocksTopOfTwoCommit is the same proof for the OTHER
// gate: a vector-only top candidate must not win a top-of-two commit
// either, even clearing both TopFloor and TopGap by a wide margin. Without
// this case the guard would only be proven for one of the two commit paths
// AC-3778-3 was ratified against.
func TestVectorOnlyGuardBlocksTopOfTwoCommit(t *testing.T) {
	top := corroborationCandidate("top", 0.99, contextfabric.MatchVector)
	second := corroborationCandidate("second", 0.10, contextfabric.MatchLexical)
	resolution := resolveWithGate(DefaultCommitGatePolicy(), top, second)
	if len(resolution.Committed) != 0 {
		t.Fatalf("a vector-only top-of-two candidate must not commit even with floor and gap both cleared, got %v", resolution.Committed)
	}
}

// TestVectorOnlyGuardDoesNotBlockACorroboratedVectorCandidate proves the
// guard's scope from the OTHER side: a candidate whose mechanisms include
// MatchVector ALONGSIDE something else is never "vector-only" and must
// commit exactly as it did before the guard existed -- this is a
// SINGLE-mechanism rule, not a "touched vector at any point" rule.
func TestVectorOnlyGuardDoesNotBlockACorroboratedVectorCandidate(t *testing.T) {
	candidate := corroborationCandidate("auth", 0.62, contextfabric.MatchVector, contextfabric.MatchTraversalParent)
	resolution := resolveWithGate(DefaultCommitGatePolicy(), candidate)
	if len(resolution.Committed) != 1 {
		t.Fatalf("a corroborated vector+traversal candidate must still auto-commit, got %v (confidence %v)", resolution.Committed, resolution.Candidates[0].Confidence)
	}
}

// TestVectorOnlyGuardDoesNotReachTheChaos3829RescuePath proves the guard's
// scope boundary the other direction: the CHAOS-3829 vector-margin rescue
// (a SEPARATE, independently ratified commit path, deliberately untouched
// by this guard -- see isVectorOnlyCandidate's own doc comment) can still
// commit a vector+lexical corroborated candidate exactly as before.
// vectorArmCorroborated's own MatchVector-AND-MatchLexical requirement
// means a candidate the rescue commits was never vector-only to begin
// with, so there is nothing here the guard could have blocked.
func TestVectorOnlyGuardDoesNotReachTheChaos3829RescuePath(t *testing.T) {
	auth := corroborationCandidate("auth", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	bySubject := map[string]contextfabric.SubjectCandidate{
		SubjectKey(auth.Subject):  auth,
		SubjectKey(authz.Subject): authz,
	}
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.50,
	}
	resolution := ResolveFromMergedCandidatesWithGate(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, similarities, 0.25, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "")
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("the CHAOS-3829 rescue must still commit the higher-margin corroborated candidate, got %v", resolution.Committed)
	}
}

// TestLexicalThreeOfFourTokenOverlapStaysAmbiguousAtTheDefaultGate pins,
// fast and at the unit level, the counter-example that (alongside the
// vector-only arithmetic gap above) got LoneFloor=0.68 rejected during
// CHAOS-3857 ship verification: TestLiveRelationshipProjectionNeverDowngradesAnEndpointsOwnAuthorization
// (falkorgraph, real FalkorDB via testcontainers) searched for a work item
// named "Repo-less work item" and got a DIFFERENT subject, "Repo-backed
// work item", back as a false-positive lexical match -- 3 of their 4
// tokens overlap ("Repo-", "work", "item"; only "less"/"backed" differ),
// which fulltextRelevanceFloor/span normalizes to exactly 0.50+0.25*0.75 =
// 0.6875. At LoneFloor=0.72 that stays ambiguous, as asserted here; at the
// rejected LoneFloor=0.68 it auto-committed the wrong subject (confirmed by
// toggling the constant and rerunning the live test both ways during ship
// verification). This is a lexical-only candidate -- the vector-only guard
// above does not and should not touch it; lexical-alone auto-commit is
// intentional design (see falkorgraph/queries.go's fulltextRelevanceCeiling
// doc comment). Keeping this case pinned at the FAST unit level means a
// future LoneFloor change surfaces this specific counter-example without
// requiring the slow, Docker-backed live suite to run.
func TestLexicalThreeOfFourTokenOverlapStaysAmbiguousAtTheDefaultGate(t *testing.T) {
	const threeOfFourTokenOverlapConfidence = 0.50 + 0.25*0.75 // 0.6875
	candidate := corroborationCandidate("repo_backed_work_item", threeOfFourTokenOverlapConfidence, contextfabric.MatchLexical)
	resolution := resolveWithGate(DefaultCommitGatePolicy(), candidate)
	if len(resolution.Committed) != 0 {
		t.Fatalf("a 3-of-4 lexical token overlap (confidence %v) must not auto-commit at the default gate, got %v", threeOfFourTokenOverlapConfidence, resolution.Committed)
	}
}
