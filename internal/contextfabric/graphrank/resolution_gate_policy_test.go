package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// This file's tests are additive-only (CHAOS-3857 gate-threshold sweep
// parameterization): they exercise CommitGatePolicy/DefaultCommitGatePolicy
// and ResolveFromMergedCandidatesWithGate, the new surfaces
// ResolveFromMergedCandidates now delegates to. No existing test in this
// package is modified -- corroboration_test.go's loneCommitGate/topOfTwoGate
// constants and every call to ResolveFromMergedCandidates there are
// untouched and still green, which is itself the byte-identical-behavior
// proof: ResolveFromMergedCandidates's own logic did not change, only where
// its three threshold literals now live.

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

// TestResolveFromMergedCandidatesIsResolveFromMergedCandidatesWithGateAtDefault
// is the direct, mechanical byte-identical-behavior proof: for an arbitrary
// candidate set, ResolveFromMergedCandidates's result must equal
// ResolveFromMergedCandidatesWithGate's result when the gate argument is
// DefaultCommitGatePolicy() -- exactly what ResolveFromMergedCandidates's
// new one-line body does. If a future edit ever lets the two diverge (e.g.
// someone "optimizes" the thin wrapper into a partial copy), this fails.
func TestResolveFromMergedCandidatesIsResolveFromMergedCandidatesWithGateAtDefault(t *testing.T) {
	bySubject := map[string]contextfabric.SubjectCandidate{}
	for _, c := range []contextfabric.SubjectCandidate{
		corroborationCandidate("alpha", 0.75, contextfabric.MatchLexical),
		corroborationCandidate("beta", 0.60, contextfabric.MatchLexical),
	} {
		bySubject[SubjectKey(c.Subject)] = c
	}
	viaDefault := ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true)
	viaExplicitGate := ResolveFromMergedCandidatesWithGate(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy())
	if len(viaDefault.Committed) != len(viaExplicitGate.Committed) {
		t.Fatalf("Committed length differs: default=%v explicitGate=%v", viaDefault.Committed, viaExplicitGate.Committed)
	}
	for i := range viaDefault.Committed {
		if viaDefault.Committed[i] != viaExplicitGate.Committed[i] {
			t.Fatalf("Committed[%d] differs: default=%+v explicitGate=%+v", i, viaDefault.Committed[i], viaExplicitGate.Committed[i])
		}
	}
}

// TestResolveFromMergedCandidatesWithGateHonorsLoweredLoneFloor is the core
// acceptance test for the whole parameterization: a single candidate at
// confidence 0.65 sits BELOW the calibrated LoneFloor (0.72) --
// ResolveFromMergedCandidates (the default, unchanged path) must leave it
// ambiguous, exactly as it always has -- but
// ResolveFromMergedCandidatesWithGate with an EXPLICIT, lowered LoneFloor
// (0.60) must commit it. Proves the new gate parameter actually reaches and
// changes the decision, not merely that it compiles.
func TestResolveFromMergedCandidatesWithGateHonorsLoweredLoneFloor(t *testing.T) {
	candidate := corroborationCandidate("solo", 0.65)
	bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(candidate.Subject): candidate}

	atDefault := ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true)
	if len(atDefault.Committed) != 0 {
		t.Fatalf("at the calibrated default, Committed = %v, want none (0.65 < 0.72)", atDefault.Committed)
	}

	lowered := DefaultCommitGatePolicy()
	lowered.LoneFloor = 0.60
	atLoweredFloor := ResolveFromMergedCandidatesWithGate(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, lowered)
	if len(atLoweredFloor.Committed) != 1 || atLoweredFloor.Committed[0] != candidate.Subject {
		t.Fatalf("with LoneFloor=0.60, Committed = %v, want [%+v]", atLoweredFloor.Committed, candidate.Subject)
	}
}

// TestResolveFromMergedCandidatesWithGateHonorsWidenedTopGap is the
// top-of-two counterpart: two candidates whose gap (0.10) clears the
// calibrated TopGap (0.12) is FALSE at the default -- ambiguous -- but a
// widened-permissive override (TopGap: 0.05) commits the top candidate.
func TestResolveFromMergedCandidatesWithGateHonorsWidenedTopGap(t *testing.T) {
	bySubject := map[string]contextfabric.SubjectCandidate{}
	for _, c := range []contextfabric.SubjectCandidate{
		corroborationCandidate("top", 0.90),
		corroborationCandidate("second", 0.80),
	} {
		bySubject[SubjectKey(c.Subject)] = c
	}
	atDefault := ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true)
	if len(atDefault.Committed) != 0 {
		t.Fatalf("at the calibrated default, Committed = %v, want none (gap 0.10 < 0.12)", atDefault.Committed)
	}

	widened := DefaultCommitGatePolicy()
	widened.TopGap = 0.05
	atWidenedGap := ResolveFromMergedCandidatesWithGate(bySubject, map[string]string{}, map[string]bool{}, 10, true, false, nil, 0, false, 10, 20, true, widened)
	if len(atWidenedGap.Committed) != 1 {
		t.Fatalf("with TopGap=0.05, Committed = %v, want exactly one commit (gap 0.10 >= 0.05, top >= TopFloor)", atWidenedGap.Committed)
	}
}
