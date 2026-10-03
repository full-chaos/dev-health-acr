package contextfabric

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A cohort turn that asks to write its own answer keeps the fixed answer text
// when the service narrates cohort drivers: the status sentence of a partial
// answer says coverage was unavailable, which is not why this answer is
// partial.
func TestClientSynthesisCohortTurnKeepsTheFixedAnswerAfterNarration(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"}
	cohort := &Cohort{
		Kind:      SubjectTeam,
		Rationale: "kind census match",
		Members: []CohortMember{
			{Subject: team, Rank: 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_team_roster"}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	runtime := &clientRuntime{}
	telemetry := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{
					{Kind: FactHealth, Subject: team, Fields: map[string]FactValue{"severity": StringFactValue("high")}},
					investmentFact("CHAOS", balancedThemes(), 0),
				},
				Coverage: Coverage{
					Sources:         []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}},
					DegradedReasons: []string{},
				},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: RuntimeAnswerSynthesizer{
			Runtime: runtime,
			Options: RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
			ClientSynthesis: &ClientSynthesisAssembly{
				PromptVersion: "p", ModelOutputVersion: "o", SystemSHA256: hex.EncodeToString(sha256Sum("system")),
				Rules: []string{"rule"}, MaxBytes: clientTestMaxBytes, Encode: clientTestEncode,
			},
		},
		Results: &resultStoreStub{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_45800002" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_45800002"
	request.Question = "which teams are struggling?"
	request.SynthesisMode = SynthesisModeClient
	ctx, collected := WithSynthesisInputCollector(context.Background())
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if runtime.synthCalls != 0 {
		t.Fatalf("synthesize calls = %d, want 0", runtime.synthCalls)
	}
	if len(telemetry.cohortDriverNarrations) != 1 || telemetry.cohortDriverNarrations[0].JudgmentsEmitted == 0 {
		t.Fatalf("cohortDriverNarrations = %#v, want one event with a judgment: the fixture did not exercise narration", telemetry.cohortDriverNarrations)
	}
	if len(result.Drivers) == 0 {
		t.Fatal("the cohort answer carries no service-narrated driver")
	}
	if telemetry.cohortDriverNarrations[0].AnswerNarrativeRecomposed {
		t.Error("the narration event says the answer narrative was recomposed")
	}
	for name, got := range map[string]string{"direct_judgment": result.DirectJudgment, "current_state": result.CurrentState, "deterministic_answer": result.DeterministicAnswer} {
		if got != contractsv1.ContextFabricClientSynthesisAnswer {
			t.Errorf("%s = %q, want the fixed client synthesis answer", name, got)
		}
	}
	if result.Status != InvestigationPartial || result.Versions.SynthesisSource != SynthesisSourceClient {
		t.Errorf("status = %q synthesis_source = %q, want partial and client", result.Status, result.Versions.SynthesisSource)
	}
	if collected() == nil {
		t.Error("no synthesis input was delivered")
	}
}
