package panelharness

import "testing"

func selection(identity, applied string) PanelistSelection {
	return PanelistSelection{CanonicalModelIdentity: identity, AppliedValue: applied}
}

func TestBuildMemberRun_CompleteRequiresEveryConfiguredPanelist(t *testing.T) {
	tests := []struct {
		name              string
		expectedPanelists int
		selections        []PanelistSelection
		wantComplete      bool
	}{
		{"all three answered", 3, []PanelistSelection{selection("a", "x"), selection("b", "x"), selection("c", "y")}, true},
		{"one panelist missing (errored/timed out)", 3, []PanelistSelection{selection("a", "x"), selection("b", "x")}, false},
		{"none answered", 3, nil, false},
		{"zero configured panelists is never complete", 0, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := BuildMemberRun("expected_kind", tc.expectedPanelists, tc.selections)
			if run.Complete != tc.wantComplete {
				t.Errorf("Complete = %v, want %v", run.Complete, tc.wantComplete)
			}
		})
	}
}

func TestBuildMemberRun_DistinctIdentitiesRejectsDuplicateModel(t *testing.T) {
	tests := []struct {
		name         string
		selections   []PanelistSelection
		wantDistinct bool
	}{
		{"three distinct models", []PanelistSelection{selection("sol", "x"), selection("luna", "x"), selection("opus", "y")}, true},
		{"same model counted twice", []PanelistSelection{selection("sol", "x"), selection("sol", "y")}, false},
		{"empty is vacuously distinct", nil, true},
		{"single panelist is vacuously distinct", []PanelistSelection{selection("sol", "x")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := BuildMemberRun("expected_kind", len(tc.selections), tc.selections)
			if run.DistinctIdentities != tc.wantDistinct {
				t.Errorf("DistinctIdentities = %v, want %v", run.DistinctIdentities, tc.wantDistinct)
			}
		})
	}
}

func TestBuildMemberRun_ConsensusMajorityAndAgreementBits(t *testing.T) {
	selections := []PanelistSelection{selection("sol", "pull_request"), selection("luna", "pull_request"), selection("opus", "work_item")}
	run := BuildMemberRun("expected_kind", 3, selections)

	if got, want := run.Consensus.ValueCounts["pull_request"], 2; got != want {
		t.Errorf("value_counts[pull_request] = %d, want %d", got, want)
	}
	if got, want := run.Consensus.ValueCounts["work_item"], 1; got != want {
		t.Errorf("value_counts[work_item] = %d, want %d", got, want)
	}
	if run.Consensus.MajorityValue != "pull_request" {
		t.Errorf("MajorityValue = %q, want %q", run.Consensus.MajorityValue, "pull_request")
	}
	wantBits := []bool{true, true, false}
	if len(run.Consensus.AgreementBits) != len(wantBits) {
		t.Fatalf("AgreementBits length = %d, want %d", len(run.Consensus.AgreementBits), len(wantBits))
	}
	for i, want := range wantBits {
		if run.Consensus.AgreementBits[i] != want {
			t.Errorf("AgreementBits[%d] = %v, want %v", i, run.Consensus.AgreementBits[i], want)
		}
	}
}

// TestBuildMemberRun_TiedMajorityIsDeterministic proves the tie-break rule
// (lexicographically smaller value wins) is stable and does not depend on
// panelist arrival/slice order -- a retry that completed panelists in a
// different order must still produce the identical MajorityValue from the
// same underlying votes.
func TestBuildMemberRun_TiedMajorityIsDeterministic(t *testing.T) {
	orderA := []PanelistSelection{selection("sol", "work_item"), selection("luna", "pull_request")}
	orderB := []PanelistSelection{selection("luna", "pull_request"), selection("sol", "work_item")}

	runA := BuildMemberRun("expected_kind", 2, orderA)
	runB := BuildMemberRun("expected_kind", 2, orderB)

	if runA.Consensus.MajorityValue != runB.Consensus.MajorityValue {
		t.Fatalf("majority value depends on arrival order: %q vs %q", runA.Consensus.MajorityValue, runB.Consensus.MajorityValue)
	}
	if runA.Consensus.MajorityValue != "pull_request" {
		t.Errorf("MajorityValue = %q, want the lexicographically smaller tied value %q", runA.Consensus.MajorityValue, "pull_request")
	}
}

func TestBuildMemberRun_EmptySelectionsProduceEmptyConsensus(t *testing.T) {
	run := BuildMemberRun("expected_kind", 0, nil)
	if run.Consensus.MajorityValue != "" {
		t.Errorf("MajorityValue = %q, want empty for no selections", run.Consensus.MajorityValue)
	}
	if len(run.Consensus.AgreementBits) != 0 {
		t.Errorf("AgreementBits = %v, want empty", run.Consensus.AgreementBits)
	}
	if run.Consensus.ValueCounts == nil {
		t.Error("ValueCounts must be a non-nil (possibly empty) map for stable JSON encoding as {} rather than null")
	}
}
