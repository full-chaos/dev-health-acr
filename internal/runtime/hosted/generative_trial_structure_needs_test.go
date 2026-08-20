package hosted_test

import "testing"

// CHAOS-3927 P1 post-merge measurement: pins tallyOutcome's own
// StructureNeedsCoverage aggregation (structureNeedsCoverage's own doc
// comment, generative_trial_live_test.go) with synthetic caseOutcome
// values -- no live infra, no corpus, no model call needed to verify the
// counting logic itself.
//
// Four cases exercise every branch the aggregation must get right:
//   - stalled + disclosed:      counts toward BOTH total_stalled and disclosed_on_stalled.
//   - stalled + NOT disclosed:  counts toward total_stalled only -- the
//     coverage gap this measurement exists to catch.
//   - committed (not stalled):  excluded from the tally entirely, even
//     though StructureNeedsDisclosed happens to be false here too (a
//     committed case correctly never discloses StructureNeeds).
//   - error (Status == ""):     excluded -- no real, validated result was
//     ever reached, so there is nothing to measure disclosure against.
func TestTallyOutcome_StructureNeedsCoverage(t *testing.T) {
	report := &trialReport{}

	stalledDisclosed := caseOutcome{
		Index: 0, Outcome: "no_commit", Stage: "no_match", Status: "no_match",
		CommittedCount: 0, StructureNeedsDisclosed: true,
	}
	stalledUndisclosed := caseOutcome{
		Index: 1, Outcome: "no_commit", Stage: "clarification_required", Status: "clarification_required",
		CommittedCount: 0, StructureNeedsDisclosed: false,
	}
	committedNotStalled := caseOutcome{
		Index: 2, Outcome: "correct", Stage: "usable_answer", Status: "complete",
		CommittedCount: 1, StructureNeedsDisclosed: false,
	}
	errored := caseOutcome{
		Index: 3, Outcome: "error:interpretation_rejected", Stage: "interpretation_rejected", Status: "",
		CommittedCount: 0, StructureNeedsDisclosed: false,
	}

	for _, outcome := range []caseOutcome{stalledDisclosed, stalledUndisclosed, committedNotStalled, errored} {
		tallyOutcome(report, outcome)
	}

	if report.StructureNeedsCoverage == nil {
		t.Fatal("report.StructureNeedsCoverage = nil, want a populated summary after at least one stalled case")
	}
	if got, want := report.StructureNeedsCoverage.TotalStalled, 2; got != want {
		t.Errorf("TotalStalled = %d, want %d (the two stalled cases; committed and error cases excluded)", got, want)
	}
	if got, want := report.StructureNeedsCoverage.DisclosedOnStalled, 1; got != want {
		t.Errorf("DisclosedOnStalled = %d, want %d (only the disclosed stalled case)", got, want)
	}
}

// TestTallyOutcome_StructureNeedsCoverage_NoStalledCasesStaysNil is the
// additive-optional twin: a run with zero stalled cases must leave
// StructureNeedsCoverage nil (never a zero-value struct), matching
// ClickHouseUsageSummary's own "absence means not measured" convention
// this type's doc comment cites.
func TestTallyOutcome_StructureNeedsCoverage_NoStalledCasesStaysNil(t *testing.T) {
	report := &trialReport{}
	committedNotStalled := caseOutcome{
		Index: 0, Outcome: "correct", Stage: "usable_answer", Status: "complete",
		CommittedCount: 1, StructureNeedsDisclosed: false,
	}
	tallyOutcome(report, committedNotStalled)

	if report.StructureNeedsCoverage != nil {
		t.Fatalf("report.StructureNeedsCoverage = %+v, want nil (no stalled case was tallied)", report.StructureNeedsCoverage)
	}
}
