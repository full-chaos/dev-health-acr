package hosted_test

// CHAOS-4386 answer-rate follow-up (team-lead scope-add ruling 2026-08-28
// 04:30 PDT): the v39/v40 report carried no per-case terminal answer
// record -- the answer-rate analysis had to proxy from arm x member
// structure rows. Pure-logic RED/GREEN tests, no live corpus -- runs
// unconditionally under `make verify`.

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func chaos4386ResultWithStatus(status contractsv1.ContextFabricInvestigationStatus) contractsv1.ContextFabricInvestigationResult {
	return contractsv1.ContextFabricInvestigationResult{Status: status}
}

// TestChaos4386TerminalReason pins chaos4386TerminalReason's exact
// per-status disclosure-channel mapping.
func TestChaos4386TerminalReason(t *testing.T) {
	t.Run("complete has nothing to disclose", func(t *testing.T) {
		if got := chaos4386TerminalReason(chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationComplete)); got != "" {
			t.Errorf("chaos4386TerminalReason(complete) = %q, want empty", got)
		}
	})
	t.Run("clarification_required reads Interpretation.ClarificationReason", func(t *testing.T) {
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationClarificationRequired)
		result.Interpretation.ClarificationReason = "ambiguous subject"
		if got := chaos4386TerminalReason(result); got != "ambiguous subject" {
			t.Errorf("chaos4386TerminalReason = %q, want %q", got, "ambiguous subject")
		}
	})
	t.Run("degraded reads Coverage.DegradedReasons first entry", func(t *testing.T) {
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationDegraded)
		result.Coverage.DegradedReasons = []string{"source_unavailable", "second_reason"}
		if got := chaos4386TerminalReason(result); got != "source_unavailable" {
			t.Errorf("chaos4386TerminalReason = %q, want %q", got, "source_unavailable")
		}
	})
	t.Run("no_match falls back to Warnings first entry when no degraded reason", func(t *testing.T) {
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationNoMatch)
		result.Warnings = []string{"no candidate matched"}
		if got := chaos4386TerminalReason(result); got != "no candidate matched" {
			t.Errorf("chaos4386TerminalReason = %q, want %q", got, "no candidate matched")
		}
	})
	t.Run("non-complete with neither channel populated is empty, not fabricated", func(t *testing.T) {
		if got := chaos4386TerminalReason(chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationPartial)); got != "" {
			t.Errorf("chaos4386TerminalReason = %q, want empty", got)
		}
	})
}

// TestChaos4386TerminalFields pins the claimed-facts/rows counts and
// confirms TerminalStatus is the real wire literal.
func TestChaos4386TerminalFields(t *testing.T) {
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project.v2:fixture:chaos4386answerrate"}
	result := contractsv1.ContextFabricInvestigationResult{
		Status: contractsv1.ContextFabricInvestigationComplete,
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{
			{ClaimID: "c1", Kind: contractsv1.ContextFabricFactInvestment, Subject: subject, Rows: make([]contractsv1.ContextFabricClaimedFactRow, 3)},
			{ClaimID: "c2", Kind: contractsv1.ContextFabricFactWorkload, Subject: subject, Rows: make([]contractsv1.ContextFabricClaimedFactRow, 2)},
			{ClaimID: "c3", Kind: contractsv1.ContextFabricFactReadiness, Subject: subject}, // no rows
		},
	}
	status, claimed, rows, reason := chaos4386TerminalFields(result)
	if status != "complete" {
		t.Errorf("terminalStatus = %q, want %q", status, "complete")
	}
	if claimed != 3 {
		t.Errorf("claimedFactsCount = %d, want 3 (literal len(ClaimedFacts), not the distinct-source CanonicalFactsCount)", claimed)
	}
	if rows != 5 {
		t.Errorf("rowsCount = %d, want 5 (3+2+0 across all claimed facts)", rows)
	}
	if reason != "" {
		t.Errorf("terminalReason = %q, want empty for a complete result", reason)
	}
}

// twoTurnAnswerRateRow is a tiny constructor keeping the table below
// readable -- only the fields chaos4386TwoTurnAnswerRate reads.
func twoTurnAnswerRateRow(arm, expectedID, terminalStatus string, claimedFacts int) twoTurnCaseResult {
	return twoTurnCaseResult{Arm: arm, ExpectedID: expectedID, TerminalStatus: terminalStatus, ClaimedFactsCount: claimedFacts}
}

// TestChaos4386TwoTurnAnswerRate pins chaos4386TwoTurnAnswerRate's exact
// numerator/denominator: positive-arm rows with a real expected answer
// only; numerator additionally requires claimed_facts_count>=1.
func TestChaos4386TwoTurnAnswerRate(t *testing.T) {
	t.Run("no eligible rows is 0, not NaN", func(t *testing.T) {
		if got := chaos4386TwoTurnAnswerRate(nil); got != 0 {
			t.Errorf("chaos4386TwoTurnAnswerRate(nil) = %v, want 0", got)
		}
	})
	t.Run("non-positive arms and control rows (no expected ID) are excluded from the denominator", func(t *testing.T) {
		results := []twoTurnCaseResult{
			twoTurnAnswerRateRow("inferred_tier", "project.v2:x", "complete", 1),
			twoTurnAnswerRateRow("confirmed_wrong", "project.v2:x", "complete", 1),
			twoTurnAnswerRateRow("mutation", "project.v2:x", "complete", 1),
			twoTurnAnswerRateRow("positive", "", "complete", 1), // control case, no expected answer
		}
		if got := chaos4386TwoTurnAnswerRate(results); got != 0 {
			t.Errorf("chaos4386TwoTurnAnswerRate = %v, want 0 (no eligible positive-arm rows with a real expected answer)", got)
		}
	})
	t.Run("complete with claimed facts counts as answered; degraded and factless complete do not", func(t *testing.T) {
		results := []twoTurnCaseResult{
			twoTurnAnswerRateRow("positive", "project.v2:a", "complete", 1),
			twoTurnAnswerRateRow("positive", "project.v2:b", "degraded", 1),
			twoTurnAnswerRateRow("positive", "project.v2:c", "complete", 0),
			twoTurnAnswerRateRow("positive", "project.v2:d", "complete", 2),
		}
		got := chaos4386TwoTurnAnswerRate(results)
		want := 2.0 / 4.0
		if got != want {
			t.Errorf("chaos4386TwoTurnAnswerRate = %v, want %v (2 of 4 eligible rows are complete with >=1 claimed fact)", got, want)
		}
	})
}

// nTurnAnswerRateRow mirrors twoTurnAnswerRateRow's own shape for
// nTurnCaseResult.
func nTurnAnswerRateRow(armInvalidReason, terminalStatus string, claimedFacts int) nTurnCaseResult {
	return nTurnCaseResult{ArmInvalidReason: armInvalidReason, TerminalStatus: terminalStatus, ClaimedFactsCount: claimedFacts}
}

// TestChaos4386NTurnAnswerRate pins chaos4386NTurnAnswerRate's exact
// numerator/denominator: every case that actually ran (ArmInvalidReason
// empty) is denominator-eligible by this class's own seed-selection
// construction (own doc comment).
func TestChaos4386NTurnAnswerRate(t *testing.T) {
	t.Run("no eligible rows is 0, not NaN", func(t *testing.T) {
		if got := chaos4386NTurnAnswerRate(nil); got != 0 {
			t.Errorf("chaos4386NTurnAnswerRate(nil) = %v, want 0", got)
		}
	})
	t.Run("arm-invalid cases are excluded from the denominator", func(t *testing.T) {
		results := []nTurnCaseResult{
			nTurnAnswerRateRow("no subject_anchor oracle entry", "", 0),
		}
		if got := chaos4386NTurnAnswerRate(results); got != 0 {
			t.Errorf("chaos4386NTurnAnswerRate = %v, want 0 (the only row is arm-invalid)", got)
		}
	})
	t.Run("complete with claimed facts counts as answered; clarification_required does not", func(t *testing.T) {
		results := []nTurnCaseResult{
			nTurnAnswerRateRow("", "complete", 1),
			nTurnAnswerRateRow("", "clarification_required", 0),
		}
		got := chaos4386NTurnAnswerRate(results)
		want := 0.5
		if got != want {
			t.Errorf("chaos4386NTurnAnswerRate = %v, want %v", got, want)
		}
	})
}
