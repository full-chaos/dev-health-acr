package contextfabric

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type budgetTurn struct {
	result     InvestigationResult
	collected  *contractsv1.ContextFabricSynthesisInput
	totalBytes int
	err        error
}

func runBudgetTurn(t *testing.T, teams, noteBytes, budget int) budgetTurn {
	t.Helper()
	members := make([]CohortMember, 0, teams)
	facts := make([]CanonicalFact, 0, teams)
	for i := range teams {
		team := SubjectRef{Kind: SubjectTeam, CanonicalID: fmt.Sprintf("team:T%03d", i), Label: fmt.Sprintf("Team %03d", i)}
		members = append(members, CohortMember{Subject: team, Rank: i + 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}})
		facts = append(facts, CanonicalFact{Kind: FactHealth, Subject: team, Fields: map[string]FactValue{
			"severity": StringFactValue("high"), "note": StringFactValue(strings.Repeat("x", noteBytes)),
		}})
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: &Cohort{Kind: SubjectTeam, Rationale: "kind census match", Members: members}, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts:    facts,
				Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: RuntimeAnswerSynthesizer{
			Runtime: &clientRuntime{},
			Options: RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
			ClientSynthesis: &ClientSynthesisAssembly{
				PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: hex.EncodeToString(sha256Sum("system")),
				Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode,
			},
		},
		Results: &resultStoreStub{}, Telemetry: &recordingTelemetry{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_94480001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_94480001"
	request.Question = "which teams are struggling?"
	request.SynthesisMode = SynthesisModeClient
	request.Options.MaxSerializedBytes = budget
	ctx, collected := WithSynthesisInputCollector(context.Background())
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org-1"}, request)
	turn := budgetTurn{result: result, collected: collected(), err: err}
	if err == nil && turn.collected != nil {
		body, marshalErr := json.Marshal(contractsv1.ContextFabricInvestigationResponse{ContextFabricInvestigationResult: result, SynthesisInput: turn.collected})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		turn.totalBytes = len(body)
	}
	return turn
}

func boundedDisclosure(result InvestigationResult) (declared, served int) {
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisInputBounded {
			declared += *detail.Declared
			served += *detail.Served
		}
	}
	return declared, served
}

func factsInInput(t *testing.T, bundle *contractsv1.ContextFabricSynthesisInput) int {
	t.Helper()
	var input struct {
		Facts []json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(bundle.Input, &input); err != nil {
		t.Fatal(err)
	}
	return len(input.Facts)
}

// A client synthesis turn serves the answer and the synthesis input within the
// caller's byte budget, and says how many facts the cut left out.
func TestClientSynthesisInputIsCutToTheCallerBudget(t *testing.T) {
	t.Parallel()
	const teams, budget = 400, 60000
	uncut := runBudgetTurn(t, teams, 1500, 1<<20)
	if uncut.err != nil || uncut.collected == nil || uncut.collected.Bounded || uncut.totalBytes <= budget {
		t.Fatalf("unbudgeted turn: err = %v total = %d, want a body over %d", uncut.err, uncut.totalBytes, budget)
	}
	turn := runBudgetTurn(t, teams, 1500, budget)
	if turn.err != nil || turn.collected == nil {
		t.Fatalf("Investigate() error = %v bundle = %v", turn.err, turn.collected)
	}
	if turn.totalBytes > budget {
		t.Fatalf("served body = %d bytes, want at most the %d byte budget", turn.totalBytes, budget)
	}
	if !turn.collected.Bounded || !slices.Contains(turn.result.Limitations, contractsv1.ContextFabricSynthesisInputBoundedLimitation) {
		t.Fatalf("bounded = %v limitations = %v, want the cut disclosed", turn.collected.Bounded, turn.result.Limitations)
	}
	given := factsInInput(t, turn.collected)
	declared, served := boundedDisclosure(turn.result)
	read := factsInInput(t, uncut.collected)
	if declared != read || served != given || given >= read || given == 0 {
		t.Fatalf("disclosed %d of %d facts, input carries %d of %d: want the disclosure to state the cut exactly", served, declared, given, read)
	}
}

// A budget that holds the whole input leaves it as it was.
func TestClientSynthesisInputUnderTheBudgetShareIsUnchanged(t *testing.T) {
	t.Parallel()
	free := runBudgetTurn(t, 6, 200, 1<<20)
	budgeted := runBudgetTurn(t, 6, 200, 300000)
	if free.err != nil || budgeted.err != nil || free.collected == nil || budgeted.collected == nil {
		t.Fatalf("errors = %v, %v", free.err, budgeted.err)
	}
	if string(free.collected.Input) != string(budgeted.collected.Input) || free.collected.InputSHA256 != budgeted.collected.InputSHA256 || free.collected.Bounded {
		t.Fatalf("a budget that holds the input changed it: bounded = %v", free.collected.Bounded)
	}
}

// Facts that cannot be reduced to the caller's share end in the budget
// refusal, never an over-budget input.
func TestClientSynthesisCoreOverTheBudgetShareIsABudgetRefusal(t *testing.T) {
	t.Parallel()
	turn := runBudgetTurn(t, 4, 6000, 8192)
	var refusal AnswerBudgetRefusal
	if !errors.As(turn.err, &refusal) || !errors.Is(turn.err, ErrAnswerExceedsBudget) || refusal.MaxSerializedBytes != int64(clientSynthesisBodyShare(8192)) || refusal.MeasuredBytes <= refusal.MaxSerializedBytes || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("Investigate() error = %v, want the byte budget refusal naming the input share of 8192 with measured above it", turn.err)
	}
	if turn.collected != nil {
		t.Fatal("a synthesis input was delivered with the refusal")
	}
}
