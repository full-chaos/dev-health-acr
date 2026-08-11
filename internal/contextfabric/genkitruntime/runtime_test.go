package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type generatorStub struct {
	interpretation interpretationOutput
	synthesis      synthesisOutput
	interpretErr   error
	synthesisErr   error
	requests       []generationRequest
	wait           bool
}

func (g *generatorStub) Interpret(ctx context.Context, request generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	g.requests = append(g.requests, request)
	if g.wait {
		<-ctx.Done()
		return interpretationOutput{}, contextfabric.ModelUsage{}, ctx.Err()
	}
	return g.interpretation, contextfabric.ModelUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}, g.interpretErr
}

func (g *generatorStub) Synthesize(ctx context.Context, request generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	g.requests = append(g.requests, request)
	if g.wait {
		<-ctx.Done()
		return synthesisOutput{}, contextfabric.ModelUsage{}, ctx.Err()
	}
	return g.synthesis, contextfabric.ModelUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}, g.synthesisErr
}

type fallbackRuntime struct {
	interpreted contextfabric.InterpretedQuestion
	draft       contextfabric.SynthesisDraft
}

func (f fallbackRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return f.interpreted, validReceipt(contextfabric.ModelOperationInterpret), nil
}

func (f fallbackRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return f.draft, validReceipt(contextfabric.ModelOperationSynthesize), nil
}

func TestSDKGeneratorUsesGenkitStructuredOutputAndUsage(t *testing.T) {
	ctx := context.Background()
	g := genkit.Init(ctx)
	genkit.DefineModel(g, "test/context-fabric", &ai.ModelOptions{
		Label: "Context Fabric test model",
		Supports: &ai.ModelSupports{
			Constrained: ai.ConstrainedSupportAll,
			SystemRole:  true,
			Output:      []string{"json"},
		},
	}, func(_ context.Context, request *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		if request.Output == nil || request.Output.Format != ai.OutputFormatJSON || len(request.Messages) < 2 {
			t.Fatalf("model request = %#v", request)
		}
		encoded, err := json.Marshal(validInterpretationOutput())
		if err != nil {
			t.Fatal(err)
		}
		return &ai.ModelResponse{
			Message:      ai.NewModelTextMessage(string(encoded)),
			FinishReason: ai.FinishReasonStop,
			Usage:        &ai.GenerationUsage{InputTokens: 17, OutputTokens: 9, TotalTokens: 26},
		}, nil
	})
	runtime, err := New(Config{
		Genkit: g, Provider: "test", Model: "test/context-fabric", ModelVersion: "test-v1",
		Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	interpreted, receipt, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if interpreted.Shape != contextfabric.ShapeOpen || receipt.Usage.TotalTokens != 26 || receipt.Usage.InputTokens != 17 || receipt.Usage.OutputTokens != 9 {
		t.Fatalf("interpreted = %#v receipt = %#v", interpreted, receipt)
	}
}

func TestRuntimeInterpretsOpenQuestionWithoutQuestionRegistry(t *testing.T) {
	t.Parallel()
	stub := &generatorStub{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, stub, Config{})
	request := validRequest()
	request.Question = "Everything says it is nearly done, so what is actually preventing this from being useful?"

	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if interpreted.Shape != contextfabric.ShapeOpen || interpreted.RequestedJudgment != "actual_status_and_current_drivers" {
		t.Fatalf("interpreted = %#v", interpreted)
	}
	if receipt.Operation != contextfabric.ModelOperationInterpret || receipt.Outcome != "success" || len(receipt.InputDigest) != 64 || len(receipt.OutputDigest) != 64 {
		t.Fatalf("receipt = %#v", receipt)
	}
	if len(stub.requests) != 1 || strings.Contains(stub.requests[0].System, "supported questions") {
		t.Fatalf("requests = %#v", stub.requests)
	}
}

func TestRuntimeRejectsInvalidStructuredInterpretation(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.Shape = "registered_plan_only"
	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil || !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestRuntimeRejectsSynthesisThatInventsEvidence(t *testing.T) {
	t.Parallel()
	output := validSynthesisOutput()
	output.EvidenceRefIDs = []string{"evidence_not_in_input"}
	runtime := mustRuntime(t, &generatorStub{synthesis: output}, Config{})
	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil || !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("SynthesizeAnswer() error = %v", err)
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestRuntimeUsesBoundedFallbackWhenModelUnavailable(t *testing.T) {
	t.Parallel()
	fallbackQuestion := validInterpretedQuestion()
	fallbackDraft := validDraft()
	runtime := mustRuntime(t, &generatorStub{interpretErr: errors.New("503 unavailable")}, Config{
		MaxAttempts: 1,
		Fallback:    fallbackRuntime{interpreted: fallbackQuestion, draft: fallbackDraft},
	})
	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if interpreted.RequestedJudgment != fallbackQuestion.RequestedJudgment || !receipt.FallbackUsed || receipt.Outcome != "fallback" {
		t.Fatalf("interpreted = %#v receipt = %#v", interpreted, receipt)
	}
}

func TestRuntimeBoundsModelDeadline(t *testing.T) {
	stub := &generatorStub{wait: true}
	runtime := mustRuntime(t, stub, Config{Timeout: time.Second, MaxAttempts: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
}

func TestReceiptContainsDigestsNotRawQuestion(t *testing.T) {
	t.Parallel()
	runtime := mustRuntime(t, &generatorStub{interpretation: validInterpretationOutput()}, Config{})
	request := validRequest()
	request.Question = "SECRET QUESTION TEXT MUST NOT ENTER THE RECEIPT"
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	formatted := receipt.Provider + receipt.Model + receipt.InputDigest + receipt.OutputDigest + receipt.Outcome
	if strings.Contains(formatted, request.Question) {
		t.Fatalf("receipt leaked question: %#v", receipt)
	}
}

func mustRuntime(t *testing.T, generator generator, override Config) *Runtime {
	t.Helper()
	config := Config{
		Provider: "test-provider", Model: "test/model", ModelVersion: "test-model-v1",
		InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
		SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Second,
		MaxAttempts: 1, MaxInputBytes: 128 << 10,
	}
	if override.Timeout != 0 {
		config.Timeout = override.Timeout
	}
	if override.MaxAttempts != 0 {
		config.MaxAttempts = override.MaxAttempts
	}
	if override.Fallback != nil {
		config.Fallback = override.Fallback
	}
	runtime, err := newWithGenerator(config, generator)
	if err != nil {
		t.Fatalf("newWithGenerator() error = %v", err)
	}
	fixed := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return fixed }
	return runtime
}

func validRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request_12345678", Question: "What is driving Ask Dev?",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
}

func validInterpretationOutput() interpretationOutput {
	return interpretationOutput{
		Shape: "open", RequestedJudgment: "actual_status_and_current_drivers",
		SubjectTerms: []string{"Ask Dev"}, TimeContext: outputTimeContext{Axis: "current"},
		FactRequirements: []factRequirementOutput{{Kind: "status"}, {Kind: "readiness"}, {Kind: "blockers"}},
	}
}

func validInterpretedQuestion() contextfabric.InterpretedQuestion {
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "fallback_open_investigation",
		TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
}

func validSynthesisInput() contextfabric.SynthesisInput {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	path := contextfabric.RelationshipPath{
		PathID: "path_12345678", Nodes: []contextfabric.SubjectRef{project, {Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_1", Label: "Release acceptance"}},
		Edges: []contextfabric.RelationshipEdge{{
			Type: "REQUIRES", From: project, To: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_1", Label: "Release acceptance"},
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			EvidenceRefIDs: []string{"evidence_release_1234"},
		}},
		WhyRelevant: "The open work blocks release.", EvidenceRefIDs: []string{"evidence_release_1234"},
	}
	return contextfabric.SynthesisInput{
		Request: validRequest(), Interpretation: validInterpretedQuestion(),
		Graph: contextfabric.GraphContext{
			Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
			Paths:      []contextfabric.RelationshipPath{path}, EvidenceRefIDs: []string{"evidence_release_1234"},
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		},
		Facts: contextfabric.CanonicalFactBundle{
			Facts: []contextfabric.CanonicalFact{{
				Kind: contextfabric.FactReadiness, Subject: project,
				Fields:         map[string]contextfabric.FactValue{"release_ready": contextfabric.BooleanFactValue(false)},
				EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: contextfabric.SourceAvailable,
			}},
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1",
		},
	}
}

func validSynthesisOutput() synthesisOutput {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return synthesisOutput{
		Status: "complete", DirectJudgment: "Ask Dev is not release-ready.",
		CurrentState:       "Tracked completion and release readiness diverge.",
		StrongestPressures: []string{"Release acceptance remains open."},
		Drivers: []contextfabric.DriverJudgment{{
			DriverID: "driver_12345678", Standing: contextfabric.DriverPrincipal,
			Category: "release_readiness", Title: "Release acceptance remains open",
			Summary: "Required acceptance has not completed.", AffectedSubjects: []contextfabric.SubjectRef{project},
			PathIDs: []string{"path_12345678"}, EvidenceRefIDs: []string{"evidence_release_1234"},
			Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicInferred,
			Confidence: 0.9, Current: true,
		}},
		RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{"evidence_release_1234"},
		DeterministicAnswer: "Ask Dev is not release-ready because release acceptance remains open.", Warnings: []string{},
	}
}

func validDraft() contextfabric.SynthesisDraft {
	output := validSynthesisOutput()
	draft, _ := output.toDomain()
	return draft
}

func validReceipt(operation contextfabric.ModelOperation) contextfabric.ModelExecutionReceipt {
	now := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	return contextfabric.ModelExecutionReceipt{
		Operation: operation, Provider: "fallback", Model: "deterministic", ModelVersion: "v1",
		PromptVersion: "fallback-v1", SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1",
		StartedAt: now, CompletedAt: now, Attempts: 1,
		InputDigest: strings.Repeat("a", 64), OutputDigest: strings.Repeat("b", 64), Outcome: "success",
	}
}

var _ generator = (*generatorStub)(nil)
var _ contextfabric.ModelRuntime = fallbackRuntime{}
