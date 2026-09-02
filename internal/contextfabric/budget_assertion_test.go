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
