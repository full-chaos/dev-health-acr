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
	"strings"
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

// continuationCarrierBudgetBytes is the effective response byte budget a turn
// one asked through the harness engine recorded on its plan: the harness
// engine sets no service ceiling, so it is the request's own
// max_serialized_bytes. A carrier recording any other budget is a turn two
// that changed an answer-shaping option.
func continuationCarrierBudgetBytes() int64 {
	return int64(validInvestigationRequest().Options.MaxSerializedBytes)
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
		Budget: contractsv1.ContextFabricAnswerPlanBudget{
			MaxSerializedBytes: continuationCarrierBudgetBytes(),
			NarrowingBasis:     contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
		},
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

// carrierRequestIdentity is the request identity TURN ONE stamped on its
// snapshot. Turn one asked the same question through the same harness request,
// so it is that request's identity with nothing dropped -- a fixture carrier
// whose identity is absent would read as "the caller changed something", which
// is a fixture defect, not the cell under test. A pin that WANTS a changed
// identity mutates the continuing request, never this.
func carrierRequestIdentity(question string) SemanticRequestIdentity {
	base := validInvestigationRequest()
	base.Question = question
	return SemanticRequestIdentityOf(base, "")
}

// framelessCarrierState is the persisted snapshot turn one saved when its
// interpreter proposed no frame -- the shape every frameless fixture
// interpreter in these pins produces. It carries the plan's family, group axis
// and narrowing basis, no frame, no roles and no declarations.
func framelessCarrierState(prior InvestigationResult) *PersistedSemanticState {
	plan := prior.AnswerPlan
	// A fixture plan that names only its family stands for a turn whose
	// family came from the model under the table in force.
	source, version := plan.FamilySource, plan.FamilyVersion
	if source == "" {
		source = QuestionFamilySourceModel
	}
	// The SNAPSHOT records the table in force at capture; a plan stamp a cell
	// has deliberately corrupted (blank, whitespace) is the plan gate's input,
	// not a value the capture would have written.
	if strings.TrimSpace(version) != version || version == "" {
		version = QuestionFamilyTableVersion
	}
	return BuildSemanticState(SemanticStateInput{
		Outcome: QuestionFamilyOutcome{
			Family: plan.Family, Source: source,
			Gate: FrameGate{Outcome: FrameGateNotEvaluated},
		},
		// Turn one read the SAME question through the same harness
		// interpreter, so its interpretation shape is the harness's.
		EmittedShape:    ShapeOpen,
		GroupKind:       plan.GroupKind,
		NarrowingBasis:  plan.Budget.NarrowingBasis,
		FamilyVersion:   version,
		RequestIdentity: carrierRequestIdentity(prior.Question),
	})
}

// framedCarrierState is the snapshot a turn one that DID propose a validated
// frame saved: a grouped frame on the plan's own axis (members of `member`), or
// a discovered-kind frame of `member` when the plan groups nothing.
func framedCarrierState(t testing.TB, prior InvestigationResult, member SubjectKind) *PersistedSemanticState {
	t.Helper()
	plan := prior.AnswerPlan
	expression := SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: member}}
	if plan.GroupKind != "" {
		expression = SubjectExpression{Kind: SubjectExpressionGroupedMembers, Grouped: &GroupedSetExpression{GroupKind: plan.GroupKind, MemberKind: member}}
	}
	result := ValidateFrame(QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: expression, Temporal: TemporalIntentCurrent}, nil, ShapeOpen)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: carrier frame invalid (%v)", result.Failure.Invariant)
	}
	gate := DecideFrameGate(result, true)
	if gate.Refuses() {
		t.Fatalf("fixture defect: carrier frame refused by its gate (%s)", gate.Observable())
	}
	frame := result.Frame
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: plan.Family, Source: QuestionFamilySourceModel, Frame: &frame, Gate: gate},
		EmittedShape:    ShapeOpen,
		GroupKind:       plan.GroupKind,
		NarrowingBasis:  plan.Budget.NarrowingBasis,
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: carrierRequestIdentity(prior.Question),
	})
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("fixture defect: framed carrier snapshot does not validate: %v", err)
	}
	return state
}

// withFramedCarrier registers a framed snapshot for prior in store.
func withFramedCarrier(t testing.TB, store *staticResultStore, prior InvestigationResult, member SubjectKind) *staticResultStore {
	t.Helper()
	if store.states == nil {
		store.states = map[string]*PersistedSemanticState{}
	}
	store.states[prior.ResultID] = framedCarrierState(t, prior, member)
	return store
}

// withCarrierStates gives every stored result that has a carriable plan the
// snapshot its turn would have saved, unless the result already has one. A
// pin about a LEGACY carrier (no snapshot) simply does not call it.
func withCarrierStates(t testing.TB, store *staticResultStore) *staticResultStore {
	t.Helper()
	if store.states == nil {
		store.states = map[string]*PersistedSemanticState{}
	}
	for id, result := range store.results {
		if _, ok := store.states[id]; ok || carriablePlan(result) == nil {
			continue
		}
		state := framelessCarrierState(result)
		if _, err := EncodeSemanticState(state); err != nil {
			t.Fatalf("fixture defect: the carrier snapshot for %q does not validate: %v", id, err)
		}
		store.states[id] = state
	}
	return store
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
	// Every carrier a production turn saves carries its snapshot now; a pin
	// about a legacy carrier opts out with store.noCarrierStates.
	if !store.noCarrierStates {
		withCarrierStates(t, store)
	}
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
