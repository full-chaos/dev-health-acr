package genkitruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core"
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
	modelSupports := &ai.ModelSupports{
		Constrained: ai.ConstrainedSupportAll,
		SystemRole:  true,
		Multiturn:   true,
		Output:      []string{"json"},
	}
	genkit.DefineModel(g, "test/context-fabric", &ai.ModelOptions{
		Label:    "Context Fabric test model",
		Supports: modelSupports,
	}, func(_ context.Context, request *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		if request.Output == nil || request.Output.Format != ai.OutputFormatJSON {
			t.Fatalf("model request output format = %#v, want JSON", request.Output)
		}
		if len(request.Messages) != 2 {
			t.Fatalf("model request messages = %#v, want system + user", request.Messages)
		}
		system, user := request.Messages[0], request.Messages[1]
		if system.Role != ai.RoleSystem || !strings.Contains(system.Text(), "bounded interpretation layer") {
			t.Fatalf("system message = %#v", system)
		}
		// Genkit's custom-constrained mode injects the JSON output schema as
		// instructions on the system message rather than on request.Output.Schema
		// (that field is only populated for native provider-side constrained
		// decoding). Assert the schema actually reached the model this way.
		if !strings.Contains(system.Text(), "conform to the following schema") || !strings.Contains(system.Text(), "requested_judgment") {
			t.Fatalf("system message missing injected JSON schema: %#v", system)
		}
		if user.Role != ai.RoleUser || !strings.Contains(user.Text(), "What is driving Ask Dev?") {
			t.Fatalf("user message = %#v", user)
		}
		encoded, err := json.Marshal(validInterpretationOutput())
		if err != nil {
			t.Fatal(err)
		}
		return &ai.ModelResponse{
			Message:      &ai.Message{Role: ai.RoleModel, Content: []*ai.Part{ai.NewJSONPart(string(encoded))}},
			FinishReason: ai.FinishReasonStop,
			Usage:        &ai.GenerationUsage{InputTokens: 17, OutputTokens: 9, TotalTokens: 26},
		}, nil
	})
	// Model returns JSON that Genkit itself accepts (fact_requirements.kind
	// carries no jsonschema enum constraint, so Genkit's own schema
	// validation passes it through and parses a typed interpretationOutput
	// successfully). Only the ACR-owned semantic validator
	// (InterpretedQuestion.Validate -> FactRequirement.Validate ->
	// validFactKind) knows the canonical fact-kind registry and must reject
	// the invented kind after Genkit has already produced a typed value.
	genkit.DefineModel(g, "test/context-fabric-invented-fact-kind", &ai.ModelOptions{
		Label:    "Context Fabric test model (invented fact kind)",
		Supports: modelSupports,
	}, func(_ context.Context, request *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		invalid := validInterpretationOutput()
		invalid.FactRequirements = []factRequirementOutput{{Kind: "invented_fact_kind_not_in_registry"}}
		encoded, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		return &ai.ModelResponse{
			Message:      &ai.Message{Role: ai.RoleModel, Content: []*ai.Part{ai.NewJSONPart(string(encoded))}},
			FinishReason: ai.FinishReasonStop,
			Usage:        &ai.GenerationUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
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
	if receipt.Outcome != "success" || receipt.Provider != "test" || receipt.Model != "test/context-fabric" {
		t.Fatalf("receipt = %#v", receipt)
	}

	inventedFactKindRuntime, err := New(Config{
		Genkit: g, Provider: "test", Model: "test/context-fabric-invented-fact-kind", ModelVersion: "test-v1",
		Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, invalidReceipt, err := inventedFactKindRuntime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil || !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("InterpretQuestion() with invented fact kind error = %v, want ErrModelOutput", err)
	}
	if invalidReceipt.Outcome != "invalid_output" {
		t.Fatalf("invalidReceipt = %#v", invalidReceipt)
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

// echoingGenerator derives interpretationOutput.RequestedJudgment from a
// digest of the exact prompt it received, instead of a fixed value
// regardless of input. A fixed-response fake cannot distinguish "the
// runtime forwarded this call's exact input to the model" from "a hidden
// text branch intercepted this specific input and substituted a canned
// response" -- both produce the same observable output. Deriving the
// output from the input closes that gap: if production code ever
// short-circuited a particular question with a hardcoded response instead
// of actually calling the generator with it, the returned judgment would
// not match this call's own prompt digest.
type echoingGenerator struct {
	requests []generationRequest
}

func (g *echoingGenerator) Interpret(_ context.Context, request generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	g.requests = append(g.requests, request)
	output := validInterpretationOutput()
	output.RequestedJudgment = echoJudgment(request.Prompt)
	return output, contextfabric.ModelUsage{}, nil
}

func (g *echoingGenerator) Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	return synthesisOutput{}, contextfabric.ModelUsage{}, errors.New("echoingGenerator.Synthesize is unused")
}

func echoJudgment(prompt string) string {
	digest := sha256.Sum256([]byte(prompt))
	return "echo_" + hex.EncodeToString(digest[:8])
}

// TestRuntimeInterpretsBootstrapQuestionParaphrasesAndNovelCombinationsIdentically
// is the direct evidence for the CHAOS-3754 acceptance bar: the bootstrap
// project-status question, several held-out paraphrases never used to shape
// any production code path, and a novel compound question combining facts
// no single fixture in this repository combines, must all flow through the
// identical InterpretQuestion path. There is no per-case production
// branch to add: the runtime forwards whatever question text it receives
// unmodified to the generator and returns whatever structured
// interpretation the generator (model) decided, so "held out" here means
// exactly what it should -- these strings influence nothing but the test
// assertion, never a code path. Using echoingGenerator (Codex finding
// G9(b)) rather than a fixed-response stub means each case's output is
// independently verified to be a genuine function of that case's own
// generator call, not a shared response that would mask a hidden
// text-specific branch producing the same final shape by coincidence.
func TestRuntimeInterpretsBootstrapQuestionParaphrasesAndNovelCombinationsIdentically(t *testing.T) {
	t.Parallel()
	questions := []string{
		"What is the actual status of the Ask Dev project, and what are the current drivers?",
		"Where does Ask Dev actually stand right now, and what's pushing it in that direction?",
		"Can you tell me the real state of the Ask Dev effort and why it is there?",
		"Honestly, is Ask Dev actually on track, and what is behind that?",
		"Compare the release readiness of Ask Dev against the platform migration, factoring in open incidents from the last two weeks and who owns the remaining blockers.",
	}
	for _, question := range questions {
		t.Run(question, func(t *testing.T) {
			t.Parallel()
			gen := &echoingGenerator{}
			runtime := mustRuntime(t, gen, Config{})
			request := validRequest()
			request.Question = question

			interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("InterpretQuestion() error = %v", err)
			}
			if interpreted.Shape != contextfabric.ShapeOpen || receipt.Outcome != "success" {
				t.Fatalf("interpreted = %#v receipt = %#v", interpreted, receipt)
			}
			if len(gen.requests) != 1 || !strings.Contains(gen.requests[0].Prompt, question) {
				t.Fatalf("generator did not receive the exact question text unmodified: requests = %#v", gen.requests)
			}
			if want := echoJudgment(gen.requests[0].Prompt); interpreted.RequestedJudgment != want {
				t.Fatalf("interpreted.RequestedJudgment = %q, want %q (this call's own prompt echo, proving a genuine per-call round trip)", interpreted.RequestedJudgment, want)
			}
			if strings.Contains(gen.requests[0].System, "supported questions") || strings.Contains(gen.requests[0].System, "allowed question") {
				t.Fatalf("system prompt references a supported-question registry: %q", gen.requests[0].System)
			}
		})
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
			Category: "relationship", Title: "Release acceptance remains open",
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

func TestClassifyModelErrorDistinguishesRateLimitUnavailableAndInvalidOutput(t *testing.T) {
	t.Parallel()
	sensitive := "leaked secret prompt fragment"
	cases := []struct {
		name   string
		err    error
		wantIs error
	}{
		{"rate_limited", core.NewError(core.RESOURCE_EXHAUSTED, "quota exhausted: %s", sensitive), contextfabric.ErrModelRateLimited},
		{"unavailable_status", core.NewError(core.UNAVAILABLE, "backend down: %s", sensitive), contextfabric.ErrModelUnavailable},
		{"deadline_status", core.NewError(core.DEADLINE_EXCEEDED, "slow: %s", sensitive), contextfabric.ErrModelUnavailable},
		{"aborted_status", core.NewError(core.ABORTED, "aborted: %s", sensitive), contextfabric.ErrModelUnavailable},
		{"invalid_argument_status", core.NewError(core.INVALID_ARGUMENT, "bad request: %s", sensitive), contextfabric.ErrModelOutput},
		{"internal_schema_mismatch", core.NewError(core.INTERNAL, "model failed to generate output matching expected schema: %s", sensitive), contextfabric.ErrModelOutput},
		{"internal_generic", core.NewError(core.INTERNAL, "unexpected panic: %s", sensitive), contextfabric.ErrModelUnavailable},
		{"unmapped_status", core.NewError(core.NOT_FOUND, "route missing: %s", sensitive), contextfabric.ErrModelUnavailable},
		{"plain_rate_limit_string", errors.New("received 429 Too Many Requests"), contextfabric.ErrModelRateLimited},
		{"plain_opaque_string", errors.New("boom"), contextfabric.ErrModelUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyModelError(tc.err)
			if !errors.Is(got, tc.wantIs) {
				t.Fatalf("classifyModelError(%v) = %v, want errors.Is match for %v", tc.err, got, tc.wantIs)
			}
			if strings.Contains(got.Error(), sensitive) {
				t.Fatalf("classifyModelError leaked provider message content: %v", got)
			}
		})
	}
}

func TestClassifyModelErrorPreservesCancellationAndDeadline(t *testing.T) {
	t.Parallel()
	if got := classifyModelError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("classifyModelError(context.Canceled) = %v", got)
	}
	if got := classifyModelError(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("classifyModelError(context.DeadlineExceeded) = %v", got)
	}
}

func TestRetryableUsesStructuredGenkitStatusOverStringHeuristics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"resource_exhausted_retryable", core.NewError(core.RESOURCE_EXHAUSTED, "quota"), true},
		{"unavailable_retryable", core.NewError(core.UNAVAILABLE, "down"), true},
		{"deadline_exceeded_status_retryable", core.NewError(core.DEADLINE_EXCEEDED, "slow"), true},
		{"aborted_retryable", core.NewError(core.ABORTED, "aborted"), true},
		{"invalid_argument_not_retryable", core.NewError(core.INVALID_ARGUMENT, "bad schema: invented_kind value present in response"), false},
		{"context_canceled_not_retryable", context.Canceled, false},
		{"plain_502_retryable", errors.New("upstream returned 502"), true},
		{"plain_opaque_not_retryable", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := retryable(tc.err); got != tc.want {
				t.Fatalf("retryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRuntimeClassifiesRateLimitedGenerationError proves the classification
// reaches callers end to end: a rate-limited generation failure with no
// fallback configured surfaces as contextfabric.ErrModelRateLimited from
// InterpretQuestion, not the generic ErrModelUnavailable.
func TestRuntimeClassifiesRateLimitedGenerationError(t *testing.T) {
	t.Parallel()
	runtime := mustRuntime(t, &generatorStub{
		interpretErr: core.NewError(core.RESOURCE_EXHAUSTED, "quota exhausted for org_1"),
	}, Config{MaxAttempts: 1})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil || !errors.Is(err, contextfabric.ErrModelRateLimited) {
		t.Fatalf("InterpretQuestion() error = %v, want ErrModelRateLimited", err)
	}
	if errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("InterpretQuestion() error = %v, must not also match ErrModelUnavailable", err)
	}
	if receipt.Outcome != "rate_limited" || receipt.FallbackUsed {
		t.Fatalf("receipt = %#v", receipt)
	}
}

// TestNewWithGeneratorValidatesConfig proves server-owned configuration
// bounds are enforced at construction time (provider/model required and
// bounded, timeout/attempts/input-size clamped to the ranges ADR 0008
// documents) rather than discovered per call.
func TestNewWithGeneratorValidatesConfig(t *testing.T) {
	t.Parallel()
	base := func() Config {
		return Config{
			Provider: "test-provider", Model: "test/model", ModelVersion: "test-model-v1",
			Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
		}
	}
	cases := []struct {
		name    string
		mutate  func(Config) Config
		wantErr bool
	}{
		{"valid_baseline", func(c Config) Config { return c }, false},
		{"missing_provider", func(c Config) Config { c.Provider = ""; return c }, true},
		{"missing_model", func(c Config) Config { c.Model = ""; return c }, true},
		{"provider_too_long", func(c Config) Config { c.Provider = strings.Repeat("p", 257); return c }, true},
		{"timeout_too_short", func(c Config) Config { c.Timeout = 500 * time.Millisecond; return c }, true},
		{"timeout_too_long", func(c Config) Config { c.Timeout = 3 * time.Minute; return c }, true},
		{"timeout_zero_defaults", func(c Config) Config { c.Timeout = 0; return c }, false},
		{"max_attempts_too_high", func(c Config) Config { c.MaxAttempts = 4; return c }, true},
		{"max_attempts_zero_defaults", func(c Config) Config { c.MaxAttempts = 0; return c }, false},
		{"max_attempts_boundary_three_ok", func(c Config) Config { c.MaxAttempts = 3; return c }, false},
		{"max_input_bytes_too_small", func(c Config) Config { c.MaxInputBytes = 4 << 10; return c }, true},
		{"max_input_bytes_too_large", func(c Config) Config { c.MaxInputBytes = 2 << 20; return c }, true},
		{"max_input_bytes_zero_defaults", func(c Config) Config { c.MaxInputBytes = 0; return c }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := newWithGenerator(tc.mutate(base()), &generatorStub{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("newWithGenerator() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewWithGeneratorRejectsNilGenerator(t *testing.T) {
	t.Parallel()
	_, err := newWithGenerator(Config{Provider: "test", Model: "test/model"}, nil)
	if err == nil {
		t.Fatal("newWithGenerator(nil generator) error = nil, want error")
	}
}

func TestNewRejectsMissingGenkitInstance(t *testing.T) {
	t.Parallel()
	_, err := New(Config{Provider: "test", Model: "test/model"})
	if err == nil {
		t.Fatal("New() with nil Genkit error = nil, want error")
	}
}

func TestNewWithGeneratorDefaultsVersionsFromModel(t *testing.T) {
	t.Parallel()
	runtime, err := newWithGenerator(Config{Provider: "test-provider", Model: "test/model"}, &generatorStub{})
	if err != nil {
		t.Fatalf("newWithGenerator() error = %v", err)
	}
	if runtime.config.ModelVersion != "test/model" {
		t.Fatalf("ModelVersion default = %q, want model name", runtime.config.ModelVersion)
	}
	if runtime.config.InterpretationPromptVersion != defaultInterpretationPromptVersion ||
		runtime.config.SynthesisPromptVersion != defaultSynthesisPromptVersion ||
		runtime.config.SchemaVersion != defaultSchemaVersion ||
		runtime.config.EvaluatorVersion != defaultEvaluatorVersion {
		t.Fatalf("config defaults = %#v", runtime.config)
	}
}

var _ generator = (*generatorStub)(nil)
var _ contextfabric.ModelRuntime = fallbackRuntime{}
