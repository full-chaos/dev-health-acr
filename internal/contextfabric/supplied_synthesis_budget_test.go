package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func newBudgetedWriteBackTurn(t *testing.T, draft SynthesisDraft, members int, options EngineOptions) *writeBackTurn {
	t.Helper()
	parse := &draftParserStub{draft: draft}
	telemetry := &recordingTelemetry{}
	store := &resultStoreStub{}
	cohortSubjects := make([]CohortMember, 0, members)
	for _, member := range budgetStageCohort(members).Members {
		member.Subject.Kind = SubjectTeam
		cohortSubjects = append(cohortSubjects, member)
	}
	synthesizer := writeBackTurnSynthesizer(parse, telemetry)
	synthesizer.ClientSynthesis.Encode = cohortAwareEncode
	engine, err := NewEngine(EngineDependencies{
		Interpreter: suppliedGateInterpreter{interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
				TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}},
			}, nil
		})},
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: &Cohort{Kind: SubjectTeam, Rationale: "budget fixture", Members: cohortSubjects, Complete: true},
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts:    []CanonicalFact{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizer,
		Results:     store, Telemetry: telemetry,
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return &writeBackTurn{engine: engine, parse: parse, store: store, telemetry: telemetry}
}

// cohortAwareEncode puts the cohort in the encoded input, so a narrowed cohort
// is a different input with another digest.
func cohortAwareEncode(orgID string, input SynthesisInput, maxBytes int) ([]byte, error) {
	members := []string{}
	if input.Graph.Cohort != nil {
		for _, member := range input.Graph.Cohort.Members {
			members = append(members, member.Subject.CanonicalID)
		}
	}
	encoded, err := json.Marshal(map[string]any{"org_id": orgID, "question": input.Request.Question, "members": members})
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxBytes {
		return nil, &ModelInputOverflow{Bytes: len(encoded), MaxBytes: maxBytes}
	}
	return encoded, nil
}

func writeBackBudgetOptions(maxBytes int64) EngineOptions {
	return EngineOptions{
		ServiceVersion: "acr-test", MaxSerializedBytes: maxBytes, SynthesisDeadlineReserve: time.Second,
		Now:         func() time.Time { return time.Unix(300, 0).UTC() },
		NewResultID: func() string { return "result_60000001" },
	}
}

func bulkyWriteBackDraft(limitations, bytes int) SynthesisDraft {
	draft := countedWriteBackDraft(InvestigationPartial)
	for index := 0; index < limitations; index++ {
		draft.Limitations = append(draft.Limitations, strings.Repeat(string(rune('a'+index)), bytes))
	}
	return draft
}

// both calls of the flow on one engine; it returns what call 1 saved and call 2's error as is.
func (w *writeBackTurn) twoCalls(t *testing.T) (first InvestigationResult, err error) {
	t.Helper()
	base := func() InvestigationRequest {
		request := validInvestigationRequestWithConfirmedWindow()
		request.RequestID = "request_60000001"
		request.Question = "which teams are struggling?"
		request.SynthesisMode = SynthesisModeClient
		request.SuppliedInterpretation = writeBackSuppliedInterpretation()
		return request
	}
	ctx, collected := WithSynthesisInputCollector(context.Background())
	if _, err := w.engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, base()); err != nil {
		t.Fatalf("call 1: Investigate() error = %v", err)
	}
	bundle := collected()
	if bundle == nil {
		t.Fatal("call 1 delivered no synthesis input")
	}
	first = w.store.saved
	request := base()
	request.SuppliedSynthesis = &contractsv1.ContextFabricSuppliedSynthesis{
		Output: json.RawMessage(`{}`), ModelOutputVersion: bundle.Contract.ModelOutputVersion, PromptVersion: bundle.Contract.PromptVersion,
		SystemSHA256: bundle.Contract.SystemSHA256, InputSHA256: bundle.InputSHA256,
	}
	ctx, _ = WithSynthesisInputCollector(context.Background())
	_, err = w.engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, request)
	return first, err
}

func TestSuppliedSynthesisOverBudgetIsRefusedNotResynthesized(t *testing.T) {
	t.Parallel()
	const members = 6
	draft := bulkyWriteBackDraft(8, 500)

	fits := newBudgetedWriteBackTurn(t, draft, members, writeBackBudgetOptions(1<<20))
	firstSaved, err := fits.twoCalls(t)
	if err != nil {
		t.Fatalf("control: a write-back inside the budget: error = %v, want it served", err)
	}
	if fits.store.saved.DeterministicAnswer == firstSaved.DeterministicAnswer || fits.store.saved.Versions.ModelIdentity == firstSaved.Versions.ModelIdentity {
		t.Fatal("control: the write-back's answer was not saved")
	}

	tight := newBudgetedWriteBackTurn(t, draft, members, writeBackBudgetOptions(5000))
	tightFirst, err := tight.twoCalls(t)
	var changed *SynthesisInputChanged
	if errors.As(err, &changed) {
		t.Fatalf("error = %v: the over-budget write-back re-synthesized on a narrowed input and asked for a new draft", err)
	}
	var refusal AnswerBudgetRefusal
	if !errors.Is(err, ErrAnswerExceedsBudget) || !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want the planned budget refusal", err)
	}
	if refusal.RetryAttempted {
		t.Fatalf("refusal = %+v, want no retry attempted", refusal)
	}
	if tight.parse.calls != 1 {
		t.Fatalf("parse calls = %d, want exactly 1", tight.parse.calls)
	}
	if !reflect.DeepEqual(tight.store.saved, tightFirst) {
		t.Fatal("a refused write-back saved a result")
	}
	if len(tight.telemetry.suppliedSynthesisDecisions) != 0 {
		t.Fatalf("supplied synthesis decisions = %+v, want none: no outcome of the closed vocabulary fits a budget refusal", tight.telemetry.suppliedSynthesisDecisions)
	}
	refusals := 0
	for _, event := range tight.telemetry.planNarrowings {
		if event.RefusalPlanned {
			refusals++
			if event.RetryAttempted || event.RetryDeclined != RetryDeclinedSuppliedSynthesis {
				t.Fatalf("refusal event = %+v, want a declined retry named supplied_synthesis", event)
			}
		}
	}
	if refusals != 1 {
		t.Fatalf("refusal events = %d, want 1", refusals)
	}
}
