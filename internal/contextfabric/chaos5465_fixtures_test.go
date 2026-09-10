package contextfabric

// Shared fixtures and harness for the CHAOS-5465 pins.
//
// R4-5 (design review): the previous version of continuationPrior built a
// carrier that does NOT pass InvestigationResult.Validate -- its answer plan
// carried no family source, so every pin that called it a "valid servable
// carrier" was asserting against a result the product would refuse. The
// fixture now VALIDATES ITSELF at construction, so an invalid carrier cannot
// be built and no pin can quietly rest on one.

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	continuationReceiptID = "winr_5465continuationaaaa"
	continuationPriorID   = "result_5465_turn_one_0001"
	continuationOlderID   = "result_5465_turn_zero_001"
)

// forcedFamilyInterpreter proposes a stated family with a stated group kind.
//
// It exists because interpreterFunc's adapter always reports
// unclassified/none, which is the ONE condition under which the pre-existing
// carry applied -- so every arm built on it would exercise the old path and
// none would exercise this one.
type forcedFamilyInterpreter struct {
	family    QuestionFamily
	groupKind SubjectKind
	err       error
}

func (f forcedFamilyInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	if f.err != nil {
		return InterpretedQuestion{}, QuestionFamilyOutcome{}, f.err
	}
	return InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: "status",
		TimeContext:       TimeContext{Axis: TemporalCurrent},
	}, QuestionFamilyOutcome{
		Family:             f.family,
		Source:             QuestionFamilySourceModel,
		WinningSampleIndex: 0,
		WinningSample:      FamilySample{ModelFamily: f.family, GroupKind: f.groupKind},
		Version:            QuestionFamilyTableVersion,
	}, nil
}

// continuationPrior builds a turn one that classified `family` and offers a
// real window receipt for turn two to redeem.
func continuationPrior(t testing.TB, resultID, question string, family QuestionFamily, groupKind SubjectKind) InvestigationResult {
	t.Helper()
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	prior := validInvestigationResult()
	prior.ResultID = resultID
	prior.Question = question
	prior.ConfirmedStructure = nil
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        family,
		FamilySource:  QuestionFamilySourceModel,
		GroupKind:     groupKind,
		FamilyVersion: QuestionFamilyTableVersion,
		Budget:        contractsv1.ContextFabricAnswerPlanBudget{NarrowingBasis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical},
	}
	prior.WindowClarification = &WindowClarification{Options: []WindowOption{{
		ReceiptID:  continuationReceiptID,
		OptionID:   "opt_90d",
		Label:      "the last 90 days",
		RelativeID: RelativeWindowTrailing90D,
		Start:      &frozenStart,
		End:        &frozenEnd,
	}}}
	// R4-5: THE FIXTURE VALIDATES ITSELF. A carrier a pin calls "valid" must
	// be one the product would accept; the previous version was not, and five
	// pins rested on it without noticing.
	//
	// IT FAILS THE TEST, IT DOES NOT PANIC, and the difference is not style.
	// A panic here aborts the whole package binary, so a build that produced an
	// invalid carrier reported a crash with no named failing test -- the tests
	// after it never ran and nothing attributed the loss. A hosted mutation arm
	// measured exactly that: 659 of 1554 tests executed and the arm came back
	// as a harness error rather than a kill. `testing.TB` costs one parameter
	// and turns the same guard into a failure with a name on it.
	if err := prior.Validate(); err != nil {
		t.Fatalf("continuationPrior built an invalid carrier: %v", err)
	}
	return prior
}

// continuationRequest is the window-only turn two: identical question bytes,
// exactly one window receipt, and no other prior-result reference. That is the
// archived shape -- 38 of 38 turn-two requests measured.
func continuationRequest(question string) InvestigationRequest {
	request := validInvestigationRequest()
	request.Question = question
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: continuationPriorID, ReceiptID: continuationReceiptID}}
	return request
}

type continuationHarness struct {
	engine    *Engine
	telemetry *recordingTelemetry
	store     *staticResultStore
}

func newContinuationHarness(t *testing.T, store *staticResultStore, interpreter QuestionInterpreter) continuationHarness {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	telemetry := &recordingTelemetry{}
	fresh := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			bases:      provenCommitBases(project),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreter,
		Results:     store,
		Telemetry:   telemetry,
	})
	return continuationHarness{engine: engine, telemetry: telemetry, store: store}
}

func (h continuationHarness) investigate(t *testing.T, request InvestigationRequest) InvestigationResult {
	t.Helper()
	result, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// soleDecision asserts EXACTLY ONE continuation event and returns it.
//
// Exactly one, never "at least one": a surplus would double-count every rate
// computed off this line, and a test that counts only what it expects cannot
// detect a surplus.
func (h continuationHarness) soleDecision(t *testing.T) windowContinuationDecision {
	t.Helper()
	if len(h.telemetry.windowContinuationDecisions) != 1 {
		t.Fatalf("got %d window-continuation decisions, want exactly 1 -- this is the axis's only denominator, so a surplus corrupts every rate computed from it; records: %#v",
			len(h.telemetry.windowContinuationDecisions), h.telemetry.windowContinuationDecisions)
	}
	return h.telemetry.windowContinuationDecisions[0]
}
