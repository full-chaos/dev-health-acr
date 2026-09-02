package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Y3 (S7c): the budget assertion must run on the FINAL document, after label
// composition, with the EFFECTIVE budget passed explicitly -- at EVERY fresh
// result exit from Investigate, not only the decisive one.
//
// Two independent defects are pinned here, and they fail on DIFFERENT axes:
//
//   - the decisive path IS measured (fitAssembledResult), but measured BEFORE
//     the plan re-stamp and before applyCoverageDisplayLabels. Those writes add
//     BYTES and no items, so the decisive-path red is on the BYTE axis.
//   - the other four fresh-result exits (unresolved.go's terminalResult,
//     window.go's windowVetoResult and windowConfirmationRequiredResult, and
//     structure.go's structureVetoResult) are never measured AT ALL. They can
//     therefore overrun on the ITEM axis, which is the cheaper axis to reach:
//     SubjectResolution.Candidates is a charged term the contract permits up to
//     50, against a service ACR_MAX_ITEMS whose rig value is 30.
//
// The four unmeasured exits were each swept twice before: CHAOS-4413 added the
// Completeness stamp to all five, CHAOS-4690 added the display-label stamp to
// all five. Neither added the budget measurement. The enumeration existed; the
// budget stage was simply never added to it.

// manyAmbiguousCandidates returns a resolution carrying count ambiguous
// candidates -- the shape the subjectless terminal exists to serve, since its
// whole purpose is handing the caller the candidate set it could not choose
// between. Receipt ids are unique because
// ContextFabricSubjectResolution.validate enforces uniqueness.
func manyAmbiguousCandidates(count int, prompt string) SubjectResolution {
	candidates := make([]SubjectCandidate, 0, count)
	for i := 0; i < count; i++ {
		candidates = append(candidates, SubjectCandidate{
			ReceiptID:      fmt.Sprintf("receipt_ambiguous_%04d", i),
			Subject:        SubjectRef{Kind: SubjectProject, CanonicalID: fmt.Sprintf("project_ask_dev_%04d", i), Label: fmt.Sprintf("Ask Dev (%04d)", i)},
			State:          ResolutionAmbiguous,
			MatchReasons:   []string{"Label matched more than one authorized project."},
			Confidence:     0.5,
			EvidenceRefIDs: []string{},
		})
	}
	return SubjectResolution{
		Candidates:          candidates,
		Committed:           []SubjectRef{},
		ClarificationPrompt: prompt,
	}
}

// TestSubjectlessTerminalIsNeverMeasuredAgainstTheItemBudget is Y3's RED #2.
//
// RED on origin/main: Investigate returns a subjectless terminal whose CHARGED
// item count exceeds the engine's own configured MaxItems, with no error, no
// budget measurement, no plan-narrowing event and no narrower continuation. The
// route then 413s it (context_fabric_routes.go's CompleteUsageWithBudget gate),
// so the caller gets the CHAOS-4754 failure mode with none of the diagnosis
// that path at least emits.
//
// GREEN on the branch: the assertion at this exit refuses with
// ErrAnswerExceedsBudget, carrying the measurement.
func TestSubjectlessTerminalIsNeverMeasuredAgainstTheItemBudget(t *testing.T) {
	t.Parallel()

	const maxItems = 30
	const candidates = 50 // the contract's own ceiling; validate_context_fabric_result.go:54

	graphCtx := emptyGraphContext()
	graphCtx.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
	graph := &acceptanceGraphReader{
		resolution: manyAmbiguousCandidates(candidates, "Which subject did you mean?"),
		context:    graphCtx,
	}
	engine := buildTerminalEngineWithBudget(t, graph, newMapResultStore(), maxItems)

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		// GREEN side: the assertion fired. Verify it is the budget refusal and
		// that it names the overrun, rather than any other failure.
		var refusal AnswerBudgetRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("Investigate() error = %v, want an AnswerBudgetRefusal from the post-label assertion", err)
		}
		if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunItems {
			t.Fatalf("refusal.Overrun = %q, want %q", refusal.Overrun, contractsv1.ContextFabricBudgetOverrunItems)
		}
		return
	}

	// RED side: no error. Prove the served document is over the budget the
	// engine was configured with, and print the per-collection split so the
	// dominant term is checked rather than assumed.
	counts := contractsv1.CountContextFabricResultItems(result)
	t.Logf("status=%q item split: candidates=%d drivers=%d facts=%d cohort_members=%d remaining_work=%d readiness_gaps=%d conflicts=%d paths=%d budgeted=%d",
		result.Status, counts.Candidates, counts.Drivers, counts.ClaimedFacts, counts.CohortMembers,
		counts.RemainingWork, counts.ReadinessGaps, counts.Conflicts, counts.Paths, counts.Budgeted())

	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required (sanity check: the subjectless terminal is the exit under test)", result.Status)
	}
	if counts.Budgeted() <= maxItems {
		t.Fatalf("budgeted items = %d, want > %d -- the probe did not reach an over-budget shape, so this test proves nothing about the defect", counts.Budgeted(), maxItems)
	}
	t.Fatalf("the subjectless terminal exit served %d budgeted items against a configured MaxItems of %d with NO error and NO budget measurement: the route will 413 this document with no engine-side diagnosis", counts.Budgeted(), maxItems)
}

// buildTerminalEngineWithBudget mirrors unresolved_test.go's buildTerminalEngine
// exactly, adding the SERVICE item ceiling. Kept separate rather than widening
// that helper, so no existing terminal-path test changes behaviour.
func buildTerminalEngineWithBudget(t *testing.T, graph GraphReader, results InvestigationResultStore, maxItems int) *Engine {
	t.Helper()
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: SynthesisDraft{}, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- an investigation with no committed subject must never reach the canonical fact read", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a terminal clarification/no_match result must be composed without a model call")
			return InvestigationResult{}, nil
		}),
		Results: results,
	}, EngineOptions{
		ServiceVersion: "terminal-budget-test",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_terminal0001" },
		MaxItems:       maxItems,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// cohortOf returns count minimal, unranked cohort members. Unranked
// deliberately: ContextFabricCohort.validate imposes the dense 1..N
// AttentionRank sequence only on members whose RankingComputed is true, so an
// unranked cohort is the smallest legal shape that still charges its members to
// the item budget.
func cohortOf(count int) *Cohort {
	members := make([]CohortMember, 0, count)
	for i := 0; i < count; i++ {
		members = append(members, CohortMember{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: fmt.Sprintf("project_member_%04d", i), Label: fmt.Sprintf("Member %04d", i)},
			Rank:    i + 1,
		})
	}
	return &Cohort{Kind: SubjectProject, Members: members}
}

// REFUTED HYPOTHESIS, recorded rather than deleted (executed 2026-09-02).
//
// A draft of this file also probed "ambiguous candidates PLUS a discovered
// cohort", on the theory that the subjectless terminal composed at
// engine.go:1555 -- which runs after DiscoverContext and carries
// `Cohort: graphContext.Cohort` verbatim (unresolved.go:333) -- would charge
// candidates and cohort members TOGETHER, putting the overrun inside ask-dev's
// own shipped budget (max_subject_candidates 10 + max_cohort_members 50).
//
// It does not. With a cohort present the engine does NOT take the subjectless
// terminal at all: the cohort's members become the investigation's subjects and
// it proceeds to the canonical fact read (proven by the probe tripping
// buildTerminalEngine's own ReadFacts guard, which fails the test if the fact
// read is ever reached on a no-committed-subject path). A cohort makes the
// investigation non-subjectless by construction.
//
// So the subjectless terminal charges CANDIDATES ONLY, and the reachability
// statement for this defect is narrower than the draft claimed:
//
//   - ask-dev requests max_subject_candidates 10 (ask-dev origin/main
//     src/lib/acr/client.ts:245)          -> NOT reachable
//   - MCP defaults to 20 (investigate_question.go:37)  -> NOT reachable
//   - a direct API caller may request up to the contract maximum of 50
//     (validate_context_fabric_request.go bounds it 1..50)  -> REACHABLE
//
// The defect is therefore CONTRACT-reachable and executed below, but not
// reachable at any shipped consumer's budget today. That is the same honesty
// CHAOS-4785 was filed with, and it is stated here rather than left for a
// reviewer to discover.

// stripPostMeasurementLabels returns a copy of result with exactly the fields
// applyCoverageDisplayLabels writes cleared, so a caller can measure the
// document as it stood WHEN fitAssembledResult measured it. Deliberately
// mirrors chaos4690_coverage_display_labels.go's own write set, field for
// field: if that composer grows a field, this helper stops matching it and the
// delta it computes silently shrinks -- so the assertion below requires a
// MINIMUM delta rather than merely a positive one.
func stripPostMeasurementLabels(result InvestigationResult) InvestigationResult {
	stripped := result
	stripped.EvidenceRefLabels = nil
	sources := append([]SourceObservation(nil), result.Coverage.Sources...)
	for i := range sources {
		sources[i].Label = ""
		sources[i].StateLabel = ""
	}
	details := append([]CoverageDetail(nil), result.Coverage.Details...)
	for i := range details {
		details[i].Label = ""
	}
	stripped.Coverage.Sources = sources
	stripped.Coverage.Details = details
	return stripped
}

// TestDecisivePathMeasuresBeforeLabelCompositionNotAfter is Y3's RED #1, on the
// BYTE axis -- the axis the decisive-path defect actually lives on.
//
// engine.go's order is: fitAssembledResult measures (:1859), the plan is
// re-stamped (:1863), applyCoverageDisplayLabels writes source labels, detail
// labels and the whole EvidenceRefLabels map (:1892), and only then does
// Validate run (:1895). Every one of those writes adds BYTES and changes NO
// item counts, so the document the engine measured is smaller than the document
// the route serializes and serves.
//
// The budget is CALIBRATED from the run itself rather than hardcoded: the test
// measures the served document, measures it again with exactly the post-
// measurement label fields stripped, and picks a budget strictly between the
// two. A hardcoded byte figure would rot the first time any unrelated field
// changed, and would not prove the relation this test is about.
func TestDecisivePathMeasuresBeforeLabelCompositionNotAfter(t *testing.T) {
	t.Parallel()

	build := func(maxBytes int64) (InvestigationResult, error) {
		project := acceptanceProject()
		graph := &acceptanceGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context:    bootstrapGraphContext(project),
		}
		raw := "readiness: canonical fact capability timed out"
		refs := []string{"evidence_status_0001", "evidence_status_0002", "evidence_status_0003", "evidence_status_0004"}
		facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{{Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")}, EvidenceRefIDs: refs, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"}},
				Coverage: Coverage{
					Sources: []SourceObservation{
						{Source: "canonical_fact:status", State: SourceAvailable},
						{Source: "canonical_fact:readiness", State: SourceUnavailable, Reason: "canonical fact capability timed out"},
					},
					Partial:         true,
					DegradedReasons: []string{raw},
					Details: []CoverageDetail{detailFixture(contractsv1.ContextFabricCoverageDetailFactReadFailed, "canonical_fact:readiness", true, raw, func(d *CoverageDetail) {
						d.FactKind = FactReadiness
						d.SourceState = SourceUnavailable
					})},
				},
				Version: "ops-v1",
			}, nil
		})
		draft := SynthesisDraft{
			Status: InvestigationPartial, DirectJudgment: "Ask Dev status is in progress; readiness could not be evaluated.",
			CurrentState: "Readiness data is unavailable.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
			RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
			Limitations:    []string{"Readiness evaluation was unavailable for this investigation."},
			EvidenceRefIDs: refs, ClaimedFacts: []ClaimedFact{},
			DeterministicAnswer: "placeholder", Warnings: []string{},
		}
		engine := buildAcceptanceEngineWithByteBudget(t, graph, facts, draft, maxBytes)
		return engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	}

	// Pass 1: unbounded, to calibrate. A zero budget axis means "unbounded on
	// that axis" (ContextFabricResponseBudget's own doc comment).
	served, err := build(0)
	if err != nil {
		t.Fatalf("calibration run: Investigate() error = %v", err)
	}
	post, err := contractsv1.MeasureContextFabricResponse(served)
	if err != nil {
		t.Fatalf("measure served: %v", err)
	}
	pre, err := contractsv1.MeasureContextFabricResponse(stripPostMeasurementLabels(served))
	if err != nil {
		t.Fatalf("measure stripped: %v", err)
	}
	delta := post.Bytes - pre.Bytes
	t.Logf("pre-label %d bytes, post-label %d bytes, label composition adds %d bytes; items unchanged (%d -> %d)",
		pre.Bytes, post.Bytes, delta, pre.Items.Budgeted(), post.Items.Budgeted())

	if delta < 8 {
		t.Fatalf("label composition added only %d bytes: this probe cannot straddle the two measurements, so it would prove nothing", delta)
	}
	if pre.Items.Budgeted() != post.Items.Budgeted() {
		t.Fatalf("item counts changed across label composition (%d -> %d): this test asserts the BYTE-axis relation and its premise no longer holds", pre.Items.Budgeted(), post.Items.Budgeted())
	}

	// Pass 2: a budget strictly between what the engine measures and what the
	// route serves.
	budget := pre.Bytes + delta/2
	if budget <= pre.Bytes || budget >= post.Bytes {
		t.Fatalf("calibrated budget %d does not lie strictly between %d and %d", budget, pre.Bytes, post.Bytes)
	}

	result, err := build(budget)
	if err != nil {
		// GREEN side.
		var refusal AnswerBudgetRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("Investigate() error = %v, want an AnswerBudgetRefusal from the post-label assertion", err)
		}
		if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
			t.Fatalf("refusal.Overrun = %q, want %q", refusal.Overrun, contractsv1.ContextFabricBudgetOverrunBytes)
		}
		return
	}

	final, measureErr := contractsv1.MeasureContextFabricResponse(result)
	if measureErr != nil {
		t.Fatalf("measure final: %v", measureErr)
	}
	if final.Bytes <= budget {
		t.Fatalf("served document is %d bytes against a %d-byte budget: it fits, so this run does not exercise the defect", final.Bytes, budget)
	}
	t.Fatalf("the engine accepted a fit at a %d-byte budget and then served %d bytes: it measured the document BEFORE the plan re-stamp and applyCoverageDisplayLabels, and the route will refuse what it actually serves", budget, final.Bytes)
}

// buildAcceptanceEngineWithByteBudget mirrors buildAcceptanceEngineWithTelemetry
// with the SERVICE byte ceiling set, so the engine's effectiveResponseBudget has
// a byte axis to measure against.
func buildAcceptanceEngineWithByteBudget(t *testing.T, graph GraphReader, facts CanonicalFactReader, draft SynthesisDraft, maxBytes int64) *Engine {
	t.Helper()
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: draft, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
	}, EngineOptions{
		ServiceVersion:     "acceptance-test",
		Now:                func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID:        func() string { return "result_acceptance01" },
		MaxSerializedBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestEveryFreshResultExitAssertsTheBudget is the per-exit coverage proof.
//
// Team-lead's ruling was that the guard lands on ALL FIVE exits so the claim
// "the final document is measured" is true as stated rather than true of one
// path. This test EXECUTES each exit and asserts the assertion actually fired
// there, naming that exit -- rather than sweeping the source for the constants,
// which would be an allowlist of the sites the author thought of. (That is the
// failure mode a prior lane hit four times on this same repository: every
// version of its gate was closed over the shapes its author had enumerated.)
//
// It asserts on the FIT outcome deliberately. The two over-budget reds above
// prove the refusal; this proves the measurement HAPPENS at every exit, which
// is the property that was actually missing -- four of these five exits emitted
// nothing at all.
func TestEveryFreshResultExitAssertsTheBudget(t *testing.T) {
	t.Parallel()

	// A budget large enough that every fixture fits, but NON-ZERO: a zero on
	// both axes means "unbounded", and assertFitsBudget deliberately returns
	// early there, so a zero budget would make this test vacuous.
	const maxItems = 500

	seen := map[BudgetAssertStage]bool{}

	t.Run("subjectless_terminal", func(t *testing.T) {
		telemetry := &recordingTelemetry{}
		graphCtx := emptyGraphContext()
		graphCtx.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
		graph := &acceptanceGraphReader{resolution: manyAmbiguousCandidates(2, "Which subject did you mean?"), context: graphCtx}
		engine := buildTerminalEngineWithBudgetAndTelemetry(t, graph, newMapResultStore(), maxItems, telemetry)
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow()); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		assertStageRecorded(t, telemetry, BudgetAssertSubjectlessTerminal)
		seen[BudgetAssertSubjectlessTerminal] = true
	})

	t.Run("decisive", func(t *testing.T) {
		telemetry := &recordingTelemetry{}
		project := acceptanceProject()
		graph := &acceptanceGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context:    bootstrapGraphContext(project),
		}
		facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts:    []CanonicalFact{{Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")}, EvidenceRefIDs: []string{"evidence_status_0001"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"}},
				Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:status", State: SourceAvailable}}},
				Version:  "ops-v1",
			}, nil
		})
		draft := SynthesisDraft{
			Status: InvestigationComplete, DirectJudgment: "Ask Dev status is in progress.",
			CurrentState: "In progress.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
			RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
			Limitations: []string{}, EvidenceRefIDs: []string{"evidence_status_0001"}, ClaimedFacts: []ClaimedFact{},
			DeterministicAnswer: "placeholder", Warnings: []string{},
		}
		engine := buildAcceptanceEngineWithItemBudgetAndTelemetry(t, graph, facts, draft, maxItems, telemetry)
		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow()); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		assertStageRecorded(t, telemetry, BudgetAssertDecisive)
		seen[BudgetAssertDecisive] = true
	})

	// The three remaining exits are driven through the same request shapes the
	// display-label sweep uses for them, so this test and that one stay in
	// agreement about WHICH request reaches WHICH exit.
	for _, tc := range []struct {
		name    string
		stage   BudgetAssertStage
		prepare func(*InvestigationRequest)
		status  InvestigationStatus
	}{
		{"window_veto", BudgetAssertWindowVeto, func(r *InvestigationRequest) {
			r.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}
		}, InvestigationNoMatch},
		{"structure_veto", BudgetAssertStructureVeto, func(r *InvestigationRequest) {
			r.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "ancr_confirm0001"}}
		}, InvestigationNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telemetry := &recordingTelemetry{}
			engine := buildVetoEngineWithBudget(t, telemetry, maxItems)
			request := validInvestigationRequest()
			tc.prepare(&request)
			result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != tc.status {
				t.Fatalf("Status = %q, want %q (sanity check: the intended exit was not taken, so this subtest proves nothing)", result.Status, tc.status)
			}
			assertStageRecorded(t, telemetry, tc.stage)
			seen[tc.stage] = true
		})
	}

	t.Run("window_confirmation_required", func(t *testing.T) {
		telemetry := &recordingTelemetry{}
		interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
		graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
		engine := buildWindowGateEngineWithBudget(t, interpreter, graph, newMapResultStore(), maxItems, telemetry)
		request := validInvestigationRequest()
		request.Consumer.Surface = "mcp"
		request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}
		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.Status != InvestigationClarificationRequired {
			t.Fatalf("Status = %q, want clarification_required (sanity check on the gate path taken)", result.Status)
		}
		assertStageRecorded(t, telemetry, BudgetAssertWindowConfirmationRequired)
		seen[BudgetAssertWindowConfirmationRequired] = true
	})

	t.Run("reuse", func(t *testing.T) {
		telemetry := &recordingTelemetry{}
		project, candidate := reusableCandidate()
		engine := buildReuseEngineWithBudget(t, project, candidate, maxItems, telemetry)
		result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if !result.Reused {
			t.Fatal("premise: this subtest must be a reuse hit, or it proves nothing about the reuse exit")
		}
		assertStageRecorded(t, telemetry, BudgetAssertReuse)
		seen[BudgetAssertReuse] = true
	})

	// Coverage over the VOCABULARY, not over a hand-written list: adding a
	// sixth exit without a subtest here fails at this assertion rather than
	// passing silently. BudgetAssertStageVocabulary is a sized array, so a new
	// member also breaks the build of anything ranging over it with a fixed
	// expectation.
	for _, stage := range BudgetAssertStageVocabulary() {
		if !seen[stage] {
			t.Errorf("no subtest drove the %q exit: every fresh-result exit must be proven to assert, or the guard is an allowlist again", stage)
		}
	}
}

func assertStageRecorded(t *testing.T, telemetry *recordingTelemetry, want BudgetAssertStage) {
	t.Helper()
	if len(telemetry.budgetAssertions) == 0 {
		t.Fatalf("no budget assertion recorded at the %q exit: the assertion did not run there", want)
	}
	for _, event := range telemetry.budgetAssertions {
		if event.Stage != want {
			continue
		}
		if !ValidBudgetAssertStage(event.Stage) {
			t.Fatalf("recorded stage %q is not a vocabulary member", event.Stage)
		}
		if !event.Fits {
			t.Fatalf("%q asserted an overrun on a fixture sized to fit: %+v", want, event)
		}
		if event.MeasuredBytesPostLabel <= 0 {
			t.Fatalf("%q recorded %d post-label bytes, want a real measurement", want, event.MeasuredBytesPostLabel)
		}
		return
	}
	t.Fatalf("recorded assertions %+v carry no %q stage", telemetry.budgetAssertions, want)
}

// --- engine builders: each mirrors the existing harness for its exit, adding
// only an EngineTelemetry and a NON-ZERO item ceiling, so no existing test
// changes behaviour.

func buildTerminalEngineWithBudgetAndTelemetry(t *testing.T, graph GraphReader, results InvestigationResultStore, maxItems int, telemetry EngineTelemetry) *Engine {
	t.Helper()
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: SynthesisDraft{}, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- a no-committed-subject investigation must never reach the canonical fact read", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a terminal result must be composed without a model call")
			return InvestigationResult{}, nil
		}),
		Results:   results,
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "terminal-budget-test",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_terminal0001" },
		MaxItems:       maxItems,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func buildAcceptanceEngineWithItemBudgetAndTelemetry(t *testing.T, graph GraphReader, facts CanonicalFactReader, draft SynthesisDraft, maxItems int, telemetry EngineTelemetry) *Engine {
	t.Helper()
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: draft, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
		Telemetry:   telemetry,
	}, EngineOptions{
		ServiceVersion: "acceptance-test",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_acceptance01" },
		MaxItems:       maxItems,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func buildVetoEngineWithBudget(t *testing.T, telemetry EngineTelemetry, maxItems int) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Graph: bindingOnlyGraphReader{t: t},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached on a veto request")
			return InterpretedQuestion{}, nil
		}),
		Facts: failingFactReader{t: t},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached on a veto request")
			return InvestigationResult{}, nil
		}),
		Results: &staticResultStore{results: map[string]InvestigationResult{}},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a veto request")
			return InvestigationResult{}, false, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:    func() string { return "result_fresh_00001" },
		MaxItems:       maxItems,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func buildWindowGateEngineWithBudget(t *testing.T, interpreter QuestionInterpreter, graph *acceptanceGraphReader, results InvestigationResultStore, maxItems int, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- a gated request must never reach the canonical fact read", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a gated request must never reach synthesis")
			return InvestigationResult{}, nil
		}),
		Results:   results,
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "window-gate-test",
		Now:            func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_windowgate01" },
		MaxItems:       maxItems,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// buildReuseEngineWithBudget wires an engine whose reuse gate always hits with
// candidate, plus a NON-ZERO item ceiling so the reuse assertion has a budget to
// measure against.
func buildReuseEngineWithBudget(t *testing.T, project SubjectRef, candidate InvestigationResult, maxItems int, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached on a reuse hit")
			return InterpretedQuestion{}, nil
		}),
		Facts: failingFactReader{t: t},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached on a reuse hit")
			return InvestigationResult{}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:    func() string { return "result_fresh_00001" },
		MaxItems:       maxItems,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestReuseHitIsRevalidatedAgainstTheCurrentBudget is the promise-of-record
// clause, executed.
//
// chris's ruled promise says in terms: "Reuse and stored reads are RE-VALIDATED
// against the current budget and refuse if they no longer fit." The reuse key
// does not include response budgets, so a row stored under a generous budget is
// served unchanged after the budget is tightened -- and the ROUTE then refuses
// it, with no engine measurement, no narrowing event and no continuation. That
// is the CHAOS-4754 failure mode reached through a path no assembly strategy can
// repair, because a stored row carries no facts or drivers to re-plan from.
//
// RED on origin/main: the stored row is served with no assertion at all.
// GREEN: the reuse serve refuses with a measured AnswerBudgetRefusal.
//
// This is the INTERIM behaviour only. Whether such a hit should instead miss
// (budget-keyed reuse) or re-investigate is floor paper C2, open and ticketed.
func TestReuseHitIsRevalidatedAgainstTheCurrentBudget(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()

	// The budget is squeezed on the BYTE axis and the stored row is left
	// EXACTLY as the fixture builds it. That is deliberate, and two earlier
	// attempts are why: inflating the row with cohort members, and then with
	// resolution candidates, both made the reuse gate MISS -- the recheck
	// re-authorizes the row's contents, so an invented shape stops being a
	// reuse hit and the test silently stops being about reuse at all (observed
	// twice). Squeezing the ceiling instead of the document keeps the hit real.
	stored, err := contractsv1.MeasureContextFabricResponse(candidate)
	if err != nil {
		t.Fatalf("measure stored row: %v", err)
	}
	maxBytes := stored.Bytes / 2
	if maxBytes < 1 || maxBytes >= stored.Bytes {
		t.Fatalf("premise: stored row is %d bytes, computed ceiling %d -- cannot straddle", stored.Bytes, maxBytes)
	}

	telemetry := &recordingTelemetry{}
	engine := buildReuseEngineWithByteBudget(t, project, candidate, maxBytes, telemetry)

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err == nil {
		t.Fatalf("reuse served a stored row of %d bytes against a %d-byte ceiling with no refusal (Reused=%v): the route will refuse it with no engine-side diagnosis", stored.Bytes, maxBytes, result.Reused)
	}
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Investigate() error = %v, want an AnswerBudgetRefusal from the reuse re-validation", err)
	}
	if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("refusal.Overrun = %q, want %q", refusal.Overrun, contractsv1.ContextFabricBudgetOverrunBytes)
	}
	if len(telemetry.budgetAssertions) == 0 || telemetry.budgetAssertions[0].Stage != BudgetAssertReuse {
		t.Fatalf("budget assertions = %+v, want one recorded at the reuse exit", telemetry.budgetAssertions)
	}
}

// buildReuseEngineWithByteBudget is buildReuseEngineWithBudget with the ceiling
// on the BYTE axis instead of the item axis.
func buildReuseEngineWithByteBudget(t *testing.T, project SubjectRef, candidate InvestigationResult, maxBytes int64, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("interpreter should not be reached on a reuse hit")
			return InterpretedQuestion{}, nil
		}),
		Facts: failingFactReader{t: t},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached on a reuse hit")
			return InvestigationResult{}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion:     "acr-test",
		Now:                func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:        func() string { return "result_fresh_00001" },
		MaxSerializedBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// TestPersistedRowCarriesTheAnswerPlan is the stored-vs-served divergence,
// pinned. It is a defect independent of budgets and was found by the same
// enumeration.
//
// BEFORE this change, every stamped exit did: compose -> validate -> SAVE ->
// return, and the CALLER then did `stampAnswerPlan(result, plan)`. None of those
// exits sets AnswerPlan itself (verified: zero writes to it in unresolved.go,
// window.go and structure.go). So the persisted row had NO plan while the served
// response did -- stored and served disagreed about what the answer was, which
// is the divergence the budget measurement was moved into contracts/v1 to
// prevent on the other axis.
//
// AFTER: finalizeServed stamps BEFORE the caller's Save runs, so the row that is
// stored is the row that is served.
//
// RED on origin/main: the saved row's AnswerPlan is nil.
func TestPersistedRowCarriesTheAnswerPlan(t *testing.T) {
	t.Parallel()

	store := newMapResultStore()
	graphCtx := emptyGraphContext()
	graphCtx.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
	graph := &acceptanceGraphReader{
		resolution: manyAmbiguousCandidates(2, "Which subject did you mean?"),
		context:    graphCtx,
	}
	engine := buildTerminalEngineWithBudget(t, graph, store, 500)

	served, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if served.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required (sanity check: the persisting exit under test was not reached)", served.Status)
	}
	if served.AnswerPlan == nil {
		t.Fatal("premise: the SERVED result carries no plan, so this test cannot show a divergence")
	}

	stored, ok := store.byOrg[acceptancePrincipal().OrgID][served.ResultID]
	if !ok {
		t.Fatalf("no row persisted for result %q: this exit is supposed to persist, so the test proves nothing", served.ResultID)
	}
	if stored.AnswerPlan == nil {
		t.Fatal("the PERSISTED row carries no AnswerPlan while the SERVED response does: stored and served disagree, and a later read of that row returns an answer missing the plan its original caller saw")
	}
	if stored.AnswerPlan.Family != served.AnswerPlan.Family {
		t.Fatalf("stored plan family %q != served %q", stored.AnswerPlan.Family, served.AnswerPlan.Family)
	}
}

// TestStampedExitMeasuresAfterTheStampNotBefore pins the EXACT defeat that made
// the first version of this guard NOT CLEAN, on the axis it happens on.
//
// The first version asserted at the tail of each exit while `stampAnswerPlan`
// ran in the CALLER, so the plan object was added to the document after it had
// been measured. This is the seventh time this program has been beaten by
// something writing after the measurement, and it is the one mutation that must
// stay red: swap the stamp and the assert inside finalizeServed and this test
// fails.
//
// Note the axis. The plan adds BYTES and no items, so an item-axis test cannot
// see this and the subjectless-terminal item test above would stay green through
// the mutation. The budget is calibrated from the run rather than hardcoded, for
// the same reason as the decisive-path test: a literal would rot on the first
// unrelated field change and would not prove the relation.
func TestStampedExitMeasuresAfterTheStampNotBefore(t *testing.T) {
	t.Parallel()

	build := func(maxBytes int64) (InvestigationResult, error) {
		graphCtx := emptyGraphContext()
		graphCtx.Coverage.Sources = []SourceObservation{{Source: "context-fabric:graph", State: SourceAvailable}}
		graph := &acceptanceGraphReader{
			resolution: manyAmbiguousCandidates(2, "Which subject did you mean?"),
			context:    graphCtx,
		}
		engine := buildTerminalEngineWithByteBudget(t, graph, newMapResultStore(), maxBytes)
		return engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	}

	served, err := build(0) // unbounded, to calibrate
	if err != nil {
		t.Fatalf("calibration run: Investigate() error = %v", err)
	}
	if served.AnswerPlan == nil {
		t.Fatal("premise: this exit did not carry a stamped plan, so there is no stamp for the assertion to run after")
	}
	post, err := contractsv1.MeasureContextFabricResponse(served)
	if err != nil {
		t.Fatalf("measure served: %v", err)
	}
	unstamped := served
	unstamped.AnswerPlan = nil
	pre, err := contractsv1.MeasureContextFabricResponse(unstamped)
	if err != nil {
		t.Fatalf("measure unstamped: %v", err)
	}
	delta := post.Bytes - pre.Bytes
	t.Logf("pre-stamp %d bytes, post-stamp %d bytes, the plan stamp adds %d bytes; items unchanged (%d -> %d)",
		pre.Bytes, post.Bytes, delta, pre.Items.Budgeted(), post.Items.Budgeted())
	if delta < 8 {
		t.Fatalf("the plan stamp added only %d bytes: this probe cannot straddle the two measurements", delta)
	}
	if pre.Items.Budgeted() != post.Items.Budgeted() {
		t.Fatalf("item counts changed across the stamp (%d -> %d): this test asserts the BYTE-axis relation and its premise no longer holds", pre.Items.Budgeted(), post.Items.Budgeted())
	}

	budget := pre.Bytes + delta/2
	if budget <= pre.Bytes || budget >= post.Bytes {
		t.Fatalf("calibrated budget %d does not lie strictly between %d and %d", budget, pre.Bytes, post.Bytes)
	}

	result, err := build(budget)
	if err != nil {
		var refusal AnswerBudgetRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("Investigate() error = %v, want an AnswerBudgetRefusal", err)
		}
		if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
			t.Fatalf("refusal.Overrun = %q, want %q", refusal.Overrun, contractsv1.ContextFabricBudgetOverrunBytes)
		}
		return // GREEN: measured after the stamp.
	}
	final, measureErr := contractsv1.MeasureContextFabricResponse(result)
	if measureErr != nil {
		t.Fatalf("measure final: %v", measureErr)
	}
	t.Fatalf("the exit accepted a fit at a %d-byte budget and served %d bytes: it measured BEFORE the plan stamp, which is exactly the defeat this guard was rebuilt to prevent", budget, final.Bytes)
}

// buildTerminalEngineWithByteBudget is buildTerminalEngineWithBudget with the
// ceiling on the BYTE axis.
func buildTerminalEngineWithByteBudget(t *testing.T, graph GraphReader, results InvestigationResultStore, maxBytes int64) *Engine {
	t.Helper()
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: SynthesisDraft{}, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatalf("ReadFacts called with %#v -- a no-committed-subject investigation must never reach the canonical fact read", request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize called -- a terminal result must be composed without a model call")
			return InvestigationResult{}, nil
		}),
		Results: results,
	}, EngineOptions{
		ServiceVersion:     "terminal-byte-budget-test",
		Now:                func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID:        func() string { return "result_terminal0001" },
		MaxSerializedBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}
