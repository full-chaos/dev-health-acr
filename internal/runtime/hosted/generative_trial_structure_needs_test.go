package hosted_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

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
//   - committed (not stalled):  excluded from the tally entirely.
//   - error (Status == ""):     excluded -- no real, validated result was
//     ever reached, so there is nothing to measure disclosure against.
//
// Codex xhigh review finding 1 (chaos-structure-needs-coverage,
// confirmed): the two EXCLUDED cases both used to carry
// StructureNeedsDisclosed=false, so a mutant that incremented
// DisclosedOnStalled OUTSIDE the stalled gate (e.g. unconditionally on
// every case whose bool is true) would have passed this test unchanged --
// nothing here exercised what happens when an excluded case's bool is
// TRUE. Both excluded cases below are now deliberately set to true: the
// assertions only pass if disclosedOnStalled correctly ignores them.
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
	// Disclosed=true here is deliberate (see doc comment above): a
	// committed case reaching Investigate's decisive path never actually
	// produces this combination in production (composeStructureNeeds is
	// only ever called on the subjectless-terminal path), but the
	// AGGREGATION must still ignore it correctly regardless.
	committedNotStalled := caseOutcome{
		Index: 2, Outcome: "correct", Stage: "usable_answer", Status: "complete",
		CommittedCount: 1, StructureNeedsDisclosed: true,
	}
	// Disclosed=true here is equally deliberate: an "error:*" outcome
	// never reaches the point where result.StructureNeeds is read (see
	// runOneCase's own early returns), so this exact combination cannot
	// occur in production either -- same reasoning as above.
	errored := caseOutcome{
		Index: 3, Outcome: "error:interpretation_rejected", Stage: "interpretation_rejected", Status: "",
		CommittedCount: 0, StructureNeedsDisclosed: true,
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
		t.Errorf("DisclosedOnStalled = %d, want %d (only the genuinely stalled+disclosed case -- the two excluded cases' Disclosed=true must NOT leak in)", got, want)
	}
}

// fakeInvestigator implements contextfabric.Investigator with a single
// canned (result, error) pair -- CHAOS-3927 P1 post-merge measurement,
// codex xhigh review finding 2 (chaos-structure-needs-coverage,
// confirmed): the aggregation test above only ever exercised tallyOutcome
// directly on hand-built caseOutcome values, never runTrialCase itself --
// a regression that removed or narrowed
// `outcome.StructureNeedsDisclosed = result.StructureNeeds != nil` in
// runOneCase would have stayed green. This fake lets the tests below
// drive runTrialCase (the ACTUAL result-to-outcome wiring) with no live
// infra: no graph, no model call, no corpus.
type fakeInvestigator struct {
	result contextfabric.InvestigationResult
	err    error
}

func (f fakeInvestigator) Investigate(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	return f.result, f.err
}

// minimalValidStalledResult builds the smallest InvestigationResult that
// (a) passes Validate() and (b) commits no subject -- i.e. exactly the
// "stalled" shape runOneCase/tallyOutcome key off. structureNeeds is
// attached as-is (nil or a valid, minimal StructureNeeds), letting each
// test control disclosure directly. Question is synthetic placeholder
// text, never real corpus content -- this repo's own PII-withholding
// discipline applies to test fixtures too.
func minimalValidStalledResult(structureNeeds *contractsv1.ContextFabricStructureNeeds) contextfabric.InvestigationResult {
	result := contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1,
		ResultID:      "result_trial_fake0001",
		RequestID:     "request_trial000000",
		GeneratedAt:   time.Now().UTC(),
		Status:        contextfabric.InvestigationClarificationRequired,
		Question:      "synthetic test question, not corpus content",
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
		},
		SubjectResolution: contextfabric.SubjectResolution{
			Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{},
			ClarificationPrompt: "synthetic clarification prompt, not corpus content",
		},
		StrongestPressures: []string{},
		Drivers:            []contextfabric.DriverJudgment{},
		RemainingWork:      []contextfabric.Finding{},
		ReadinessGaps:      []contextfabric.Finding{},
		Paths:              []contextfabric.RelationshipPath{},
		Conflicts:          []contextfabric.Finding{},
		Limitations:        []string{"structure needs disclosed"},
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []contextfabric.ClaimedFact{},
		Coverage:           contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		Versions: contextfabric.VersionSet{
			ServiceVersion: "test-v1", ContractVersion: contextfabric.InvestigationResultSchemaV1, Backend: "test",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1",
		},
		DeterministicAnswer: "clarification is required before this question can be answered.",
		Warnings:            []string{},
		StructureNeeds:      structureNeeds,
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// minimalValidStructureNeeds is the smallest StructureNeeds block that
// passes Validate(): a single closed-vocabulary Missing entry, no offer
// lists required.
func minimalValidStructureNeeds() *contractsv1.ContextFabricStructureNeeds {
	return &contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle},
	}
}

// TestRunTrialCase_StructureNeedsDisclosed_WiredFromResult drives
// runTrialCase (not just tallyOutcome) through a fake investigator,
// proving the actual `result.StructureNeeds != nil` -> outcome wiring
// codex xhigh review finding 2 found untested.
func TestRunTrialCase_StructureNeedsDisclosed_WiredFromResult(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_fake_trial", RepositoryScopes: []string{"*"}}
	tc := trialCase{Question: "synthetic test question, not corpus content", ExpectKind: "project", ExpectID: "project_x"}

	t.Run("non-nil StructureNeeds -> disclosed true", func(t *testing.T) {
		t.Parallel()
		inv := fakeInvestigator{result: minimalValidStalledResult(minimalValidStructureNeeds())}
		outcome := runTrialCase(context.Background(), t, inv, principal, 0, tc, 30*time.Second, nil)
		if outcome.Status == "" {
			t.Fatalf("outcome.Status = %q, want a real status -- the fake result failed Validate(): %+v", outcome.Status, outcome)
		}
		if outcome.CommittedCount != 0 {
			t.Fatalf("outcome.CommittedCount = %d, want 0 (fixture commits nothing)", outcome.CommittedCount)
		}
		if !outcome.StructureNeedsDisclosed {
			t.Error("outcome.StructureNeedsDisclosed = false, want true: the fake result carried a non-nil StructureNeeds")
		}
	})

	t.Run("nil StructureNeeds -> disclosed false", func(t *testing.T) {
		t.Parallel()
		inv := fakeInvestigator{result: minimalValidStalledResult(nil)}
		outcome := runTrialCase(context.Background(), t, inv, principal, 1, tc, 30*time.Second, nil)
		if outcome.Status == "" {
			t.Fatalf("outcome.Status = %q, want a real status -- the fake result failed Validate(): %+v", outcome.Status, outcome)
		}
		if outcome.StructureNeedsDisclosed {
			t.Error("outcome.StructureNeedsDisclosed = true, want false: the fake result carried a nil StructureNeeds")
		}
	})
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

// TestTallyReplayStructureNeedsCoverage pins
// tallyReplayStructureNeedsCoverage (chaos3884_replay_harness_test.go,
// CHAOS-3927 P1 post-merge invariance measurement) with the SAME
// mutation-resistant shape TestTallyOutcome_StructureNeedsCoverage above
// uses: the two EXCLUDED cases (an error and a committed case) both set
// wiredStructureNeedsDisclosed=true deliberately, so the assertions only
// pass if the stalled gate correctly ignores them.
func TestTallyReplayStructureNeedsCoverage(t *testing.T) {
	report := &replayReport{}

	// Stalled + disclosed: counts toward both.
	tallyReplayStructureNeedsCoverage(report, nil, 0, true)
	// Stalled + NOT disclosed: counts toward total_stalled only.
	tallyReplayStructureNeedsCoverage(report, nil, 0, false)
	// Committed (not stalled) -- disclosed=true is deliberate (see doc
	// comment above): must still be excluded.
	tallyReplayStructureNeedsCoverage(report, nil, 1, true)
	// Error (wiredErr != nil) -- disclosed=true is equally deliberate:
	// must still be excluded, same reasoning as the generative harness's
	// own error case.
	tallyReplayStructureNeedsCoverage(report, context.DeadlineExceeded, 0, true)

	if report.StructureNeedsCoverage == nil {
		t.Fatal("report.StructureNeedsCoverage = nil, want a populated summary after at least one stalled case")
	}
	if got, want := report.StructureNeedsCoverage.TotalStalled, 2; got != want {
		t.Errorf("TotalStalled = %d, want %d (the two stalled cases; committed and error cases excluded)", got, want)
	}
	if got, want := report.StructureNeedsCoverage.DisclosedOnStalled, 1; got != want {
		t.Errorf("DisclosedOnStalled = %d, want %d (only the genuinely stalled+disclosed case -- the two excluded cases' disclosed=true must NOT leak in)", got, want)
	}
}

// TestTallyReplayStructureNeedsCoverage_NoStalledCasesStaysNil is the
// additive-optional twin, mirroring
// TestTallyOutcome_StructureNeedsCoverage_NoStalledCasesStaysNil above.
func TestTallyReplayStructureNeedsCoverage_NoStalledCasesStaysNil(t *testing.T) {
	report := &replayReport{}
	tallyReplayStructureNeedsCoverage(report, nil, 1, false)

	if report.StructureNeedsCoverage != nil {
		t.Fatalf("report.StructureNeedsCoverage = %+v, want nil (no stalled case was tallied)", report.StructureNeedsCoverage)
	}
}
