package hosted_test

// CHAOS-4386 answer-rate follow-up (team-lead scope-add ruling 2026-08-28
// 04:30 PDT): the v39/v40 report carried no per-case terminal answer
// record -- the answer-rate analysis had to proxy from arm x member
// structure rows. Pure-logic RED/GREEN tests, no live corpus -- runs
// unconditionally under `make verify`.

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func chaos4386ResultWithStatus(status contractsv1.ContextFabricInvestigationStatus) contractsv1.ContextFabricInvestigationResult {
	return contractsv1.ContextFabricInvestigationResult{Status: status}
}

// TestChaos4386TerminalReason pins chaos4386TerminalReason's exact
// per-status disclosure-CHANNEL mapping to a CLOSED vocabulary -- codex
// review round 1 (P2, confirmed): the raw engine/model text must never
// reach this field (corpus-safe, closed-vocabulary telemetry), only which
// channel fired.
func TestChaos4386TerminalReason(t *testing.T) {
	t.Run("complete has nothing to disclose", func(t *testing.T) {
		if got := chaos4386TerminalReason(chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationComplete)); got != "" {
			t.Errorf("chaos4386TerminalReason(complete) = %q, want empty", got)
		}
	})
	t.Run("clarification_required with SubjectResolution.ClarificationPrompt (the engine's own ordinary disclosure channel) classifies as clarification_reason_disclosed, never the raw text", func(t *testing.T) {
		// codex review round 4/confirmation pass (P2, confirmed): this is
		// the field the REAL ambiguous-candidate path populates
		// (internal/contextfabric/unresolved.go's composeSubjectlessTerminal)
		// -- Interpretation.ClarificationReason legitimately stays empty
		// on this, the common, path.
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationClarificationRequired)
		result.SubjectResolution.ClarificationPrompt = "which project did you mean -- this raw text must never appear in the classification"
		if got := chaos4386TerminalReason(result); got != "clarification_reason_disclosed" {
			t.Errorf("chaos4386TerminalReason = %q, want %q (a closed class, not the raw text)", got, "clarification_reason_disclosed")
		}
	})
	t.Run("clarification_required with Interpretation.ClarificationReason (the secondary, model-interpretation channel) classifies as clarification_reason_disclosed, never the raw text", func(t *testing.T) {
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationClarificationRequired)
		result.Interpretation.ClarificationReason = "ambiguous subject -- this raw text must never appear in the classification"
		if got := chaos4386TerminalReason(result); got != "clarification_reason_disclosed" {
			t.Errorf("chaos4386TerminalReason = %q, want %q (a closed class, not the raw text)", got, "clarification_reason_disclosed")
		}
	})
	t.Run("clarification_required with no reason classifies as undisclosed", func(t *testing.T) {
		if got := chaos4386TerminalReason(chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationClarificationRequired)); got != "undisclosed" {
			t.Errorf("chaos4386TerminalReason = %q, want %q", got, "undisclosed")
		}
	})
	t.Run("degraded with a DegradedReasons entry classifies as degraded_reason_disclosed, never the raw text", func(t *testing.T) {
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationDegraded)
		result.Coverage.DegradedReasons = []string{"source_unavailable: this raw text must never appear in the classification", "second_reason"}
		if got := chaos4386TerminalReason(result); got != "degraded_reason_disclosed" {
			t.Errorf("chaos4386TerminalReason = %q, want %q (a closed class, not the raw text)", got, "degraded_reason_disclosed")
		}
	})
	t.Run("no_match with a Limitations entry classifies as limitation_disclosed, never the raw text", func(t *testing.T) {
		// codex confirmation pass 3 (P2, confirmed): this is where a
		// NORMAL production no_match (empty subject pool, window/
		// structure veto) puts its explanation -- DegradedReasons/
		// Warnings both stay empty on that path.
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationNoMatch)
		result.Limitations = []string{"no candidate matched the requested subject -- this raw text must never appear in the classification"}
		if got := chaos4386TerminalReason(result); got != "limitation_disclosed" {
			t.Errorf("chaos4386TerminalReason = %q, want %q (a closed class, not the raw text)", got, "limitation_disclosed")
		}
	})
	t.Run("no_match falls back to warning_disclosed when no degraded reason or limitation, never the raw text", func(t *testing.T) {
		result := chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationNoMatch)
		result.Warnings = []string{"no candidate matched -- this raw text must never appear in the classification"}
		if got := chaos4386TerminalReason(result); got != "warning_disclosed" {
			t.Errorf("chaos4386TerminalReason = %q, want %q (a closed class, not the raw text)", got, "warning_disclosed")
		}
	})
	t.Run("non-complete with neither channel populated classifies as undisclosed, not fabricated", func(t *testing.T) {
		if got := chaos4386TerminalReason(chaos4386ResultWithStatus(contractsv1.ContextFabricInvestigationPartial)); got != "undisclosed" {
			t.Errorf("chaos4386TerminalReason = %q, want %q", got, "undisclosed")
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

// TestChaos4386PositiveArmNeverAttemptedRow is the codex review rounds 1+2
// regression for the two-turn side.
func TestChaos4386PositiveArmNeverAttemptedRow(t *testing.T) {
	tc := trialCase{Question: "fixture question, never real corpus text", ExpectKind: "project", ExpectID: "project.v2:fixture:chaos4386neverattempted"}

	t.Run("turn1 error (nil result): terminal fields stay at the zero value", func(t *testing.T) {
		// Round 1 (P1, confirmed): an oracle-eligible case whose positive
		// arm never even got attempted must still produce a row -- or the
		// case would be entirely invisible to
		// chaos4386TwoTurnAnswerRate's denominator.
		row := chaos4386PositiveArmNeverAttemptedRow(42, "expected_kind", tc, nil, "turn 1 investigate error: rate_limited", nil)

		if row.Index != 42 || row.Member != "expected_kind" {
			t.Errorf("Index/Member = %d/%q, want 42/%q", row.Index, row.Member, "expected_kind")
		}
		if row.Arm != string(twoTurnArmPositive) {
			t.Errorf("Arm = %q, want %q", row.Arm, twoTurnArmPositive)
		}
		if row.ExpectedID != tc.ExpectID {
			t.Errorf("ExpectedID = %q, want %q -- chaos4386TwoTurnAnswerRate's own denominator gate reads this field", row.ExpectedID, tc.ExpectID)
		}
		if row.ArmInvalidReason == "" {
			t.Error("ArmInvalidReason is empty, want a reason recorded")
		}
		if row.TerminalStatus != "" || row.ClaimedFactsCount != 0 {
			t.Errorf("TerminalStatus/ClaimedFactsCount = %q/%d, want empty/0 -- no real result was ever produced to measure", row.TerminalStatus, row.ClaimedFactsCount)
		}
		// codex review confirmation pass 2 (P2, confirmed): this row must
		// not silently count as coverage in
		// twoTurnCaseIndicesFromResults' own derivation -- turn 1 itself
		// failed, nothing ran.
		if !row.PositiveArmNeverAttempted {
			t.Error("PositiveArmNeverAttempted is false, want true for a turn-1-error row")
		}
		if got := twoTurnCaseIndicesFromResults([]twoTurnCaseResult{row}); len(got) != 0 {
			t.Errorf("twoTurnCaseIndicesFromResults = %v, want empty -- a case whose positive arm never even ran must not read as covered", got)
		}

		// End-to-end: exactly the shape codex's own round-1 finding
		// described -- one case answers, one case's positive arm never
		// even ran. The rate must read 0.5, never 1.0 (which is what
		// scanning ONLY rows that actually reached a real Investigate()
		// call would report).
		results := []twoTurnCaseResult{
			twoTurnAnswerRateRow("positive", "project.v2:answered", "complete", 1),
			row,
		}
		got := chaos4386TwoTurnAnswerRate(results)
		if want := 0.5; got != want {
			t.Errorf("chaos4386TwoTurnAnswerRate = %v, want %v -- the never-attempted row must count as an unanswered denominator entry, not disappear", got, want)
		}
	})

	t.Run("disclosure absent (turn1 IS the real terminal result): a case resolved directly on turn 1 counts as answered", func(t *testing.T) {
		// Round 2 (P1, confirmed): the disclosure-absent branch's turn1
		// can ALREADY be a complete, fact-bearing answer (no confirmation
		// needed, hence no disclosure) -- the original always-zero
		// terminal fields silently miscounted that case as unanswered.
		subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: tc.ExpectID}
		turn1 := contractsv1.ContextFabricInvestigationResult{
			Status: contractsv1.ContextFabricInvestigationComplete,
			ClaimedFacts: []contractsv1.ContextFabricClaimedFact{
				{ClaimID: "c1", Kind: contractsv1.ContextFabricFactInvestment, Subject: subject},
			},
		}
		row := chaos4386PositiveArmNeverAttemptedRow(43, "expected_kind", tc, &turn1, "turn 1 produced no structure/window disclosure -- positive arm never attempted", nil)

		if row.TerminalStatus != "complete" || row.ClaimedFactsCount != 1 {
			t.Errorf("TerminalStatus/ClaimedFactsCount = %q/%d, want %q/1 -- turn1 IS this row's real terminal result", row.TerminalStatus, row.ClaimedFactsCount, "complete")
		}
		if row.ArmInvalidReason == "" {
			t.Error("ArmInvalidReason is empty, want a reason recorded (the positive arm still never literally ran)")
		}
		// codex review round 3 (P2, confirmed): a row with real terminal
		// data claiming 0 bytes is internally inconsistent -- turn1's own
		// size must be measured too.
		wantBytes, wantTokens := chaos4386MeasureResult(turn1)
		if row.ResultBytes != wantBytes || row.EstTokens != wantTokens {
			t.Errorf("ResultBytes/EstTokens = %d/%d, want %d/%d -- turn1's own real terminal result must be measured, not left at 0", row.ResultBytes, row.EstTokens, wantBytes, wantTokens)
		}
		if row.ResultBytes == 0 {
			t.Fatal("fixture measured 0 bytes -- fixture cannot distinguish the bug from the fix")
		}
		// codex review confirmation pass 2 (P2, confirmed): unlike the
		// turn-1-error row, THIS row genuinely reached and ran turn 1, so
		// it must count as real coverage.
		if row.PositiveArmNeverAttempted {
			t.Error("PositiveArmNeverAttempted is true, want false -- turn 1 genuinely ran for this case")
		}
		if got := twoTurnCaseIndicesFromResults([]twoTurnCaseResult{row}); len(got) != 1 || got[0] != 43 {
			t.Errorf("twoTurnCaseIndicesFromResults = %v, want [43] -- a case that genuinely reached turn 1 must count as covered", got)
		}

		results := []twoTurnCaseResult{row}
		got := chaos4386TwoTurnAnswerRate(results)
		if want := 1.0; got != want {
			t.Errorf("chaos4386TwoTurnAnswerRate = %v, want %v -- a case resolved directly on turn 1 must count as answered, not silently understate the rate", got, want)
		}
	})
}

// nTurnAnswerRateRow mirrors twoTurnAnswerRateRow's own shape for
// nTurnCaseResult.
func nTurnAnswerRateRow(armInvalidReason, terminalStatus string, claimedFacts int) nTurnCaseResult {
	return nTurnCaseResult{ArmInvalidReason: armInvalidReason, TerminalStatus: terminalStatus, ClaimedFactsCount: claimedFacts}
}

// TestChaos4386NTurnAnswerRate pins chaos4386NTurnAnswerRate's exact
// numerator/denominator: only nTurnArmInvalidNoOracleEntry means genuine
// ORACLE ineligibility and is excluded from the denominator (own doc
// comment) -- every OTHER ArmInvalidReason (a genuine investigate
// failure on an oracle-eligible case) must still count, as unanswered.
func TestChaos4386NTurnAnswerRate(t *testing.T) {
	t.Run("no eligible rows is 0, not NaN", func(t *testing.T) {
		if got := chaos4386NTurnAnswerRate(nil); got != 0 {
			t.Errorf("chaos4386NTurnAnswerRate(nil) = %v, want 0", got)
		}
	})
	t.Run("no-oracle-entry cases are excluded from the denominator", func(t *testing.T) {
		results := []nTurnCaseResult{
			nTurnAnswerRateRow(nTurnArmInvalidNoOracleEntry, "", 0),
		}
		if got := chaos4386NTurnAnswerRate(results); got != 0 {
			t.Errorf("chaos4386NTurnAnswerRate = %v, want 0 (the only row is genuinely oracle-ineligible)", got)
		}
	})
	t.Run("a genuine investigate-error case counts as an unanswered denominator entry, codex review round 1 P1 confirmed", func(t *testing.T) {
		results := []nTurnCaseResult{
			nTurnAnswerRateRow("", "complete", 1),
			nTurnAnswerRateRow("turn 1 investigate error: rate_limited", "", 0),
		}
		got := chaos4386NTurnAnswerRate(results)
		if want := 0.5; got != want {
			t.Errorf("chaos4386NTurnAnswerRate = %v, want %v -- a plain ArmInvalidReason!=\"\" exclusion would drop this row entirely and report 1.0 instead", got, want)
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

// TestChaos4386RunTwoTurnPositiveArmOfferMissStampsTerminalFieldsFromTurn1
// is the codex review confirmation pass 4 (P2, confirmed) regression:
// runTwoTurnPositiveArm's own OfferMiss early return (turn 1 disclosed
// something, just not an offer matching THIS member) must stamp terminal
// fields from turn1 -- the SAME "turn1 is the real terminal result"
// treatment the disclosure-absent branch already gets -- rather than
// leaving them at zero despite a real result existing to measure.
func TestChaos4386RunTwoTurnPositiveArmOfferMissStampsTerminalFieldsFromTurn1(t *testing.T) {
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project.v2:fixture:chaos4386offermiss"}
	turn1 := contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_chaos4386_offermiss_t1", Status: contractsv1.ContextFabricInvestigationDegraded,
		Coverage: contractsv1.ContextFabricCoverage{DegradedReasons: []string{"source_unavailable"}},
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{
			{ClaimID: "c1", Kind: contractsv1.ContextFabricFactInvestment, Subject: subject},
		},
		// StructureNeeds is nil -- selectOracleOffer(member="expected_kind")
		// returns found=false unconditionally, driving the OfferMiss path
		// this test exists to exercise, without needing a live investigator.
	}
	wantBytes, wantTokens := chaos4386MeasureResult(turn1)
	if wantBytes == 0 {
		t.Fatal("fixture turn1 measured 0 bytes -- fixture cannot distinguish the bug from the fix")
	}

	tc := trialCase{Question: "fixture question, never real corpus text", ExpectKind: "project", ExpectID: subject.CanonicalID}
	entry := twoTurnOracleEntry{Index: 44, Member: string(contractsv1.ContextFabricStructureNeedExpectedKind), PositiveKind: "project"}
	res := runTwoTurnPositiveArm(t, context.Background(), nil, storage.Principal{}, 44, tc, entry, turn1, 0, nil, "")

	if !res.OfferMiss {
		t.Fatal("OfferMiss is false -- fixture did not reach the offer-miss path this test exists to exercise")
	}
	if res.TerminalStatus != "degraded" || res.ClaimedFactsCount != 1 {
		t.Errorf("TerminalStatus/ClaimedFactsCount = %q/%d, want %q/1 -- turn1 IS this row's real terminal result", res.TerminalStatus, res.ClaimedFactsCount, "degraded")
	}
	if res.TerminalReason != "degraded_reason_disclosed" {
		t.Errorf("TerminalReason = %q, want %q", res.TerminalReason, "degraded_reason_disclosed")
	}
	if res.ResultBytes != wantBytes || res.EstTokens != wantTokens {
		t.Errorf("ResultBytes/EstTokens = %d/%d, want %d/%d -- turn1's own real terminal result must be measured, not left at 0", res.ResultBytes, res.EstTokens, wantBytes, wantTokens)
	}
}
