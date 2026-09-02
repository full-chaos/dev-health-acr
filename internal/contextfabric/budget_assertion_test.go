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
