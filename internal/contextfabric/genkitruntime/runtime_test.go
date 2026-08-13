package genkitruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// erroringFallbackRuntime is a fallback ModelRuntime whose own call also
// fails, always returning err alongside its own already-classified receipt
// -- exactly what a real fallback genkitruntime.Runtime returns on its own
// terminal failure (receipt built, Outcome set via receiptOutcomeForError,
// paired with the classified error). It exists to prove CHAOS-3770 F4: when
// BOTH the primary and the fallback fail, the primary's receipt must not
// claim a successful fallback, and the caller must see the fallback's own
// (final leg) classification.
type erroringFallbackRuntime struct {
	err     error
	receipt contextfabric.ModelExecutionReceipt
}

func (f erroringFallbackRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, f.receipt, f.err
}

func (f erroringFallbackRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, f.receipt, f.err
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

// TestRuntimeRecordsTheFinalLegsFailureWhenInterpretationFallbackAlsoFails is
// the CHAOS-3770 F4 probe: when the primary fails and the configured
// fallback ALSO fails, the receipt must not claim FallbackUsed/"fallback"
// (that outcome means the fallback produced usable output, which it did
// not), and the caller must see the fallback's own -- the final leg's --
// classification, not the primary's. Getting this wrong means a caller
// reading the primary's classification (e.g. a transient rate limit) could
// treat a case as retryable when the fallback's own failure was actually
// terminal, or vice versa.
func TestRuntimeRecordsTheFinalLegsFailureWhenInterpretationFallbackAlsoFails(t *testing.T) {
	t.Parallel()
	fallbackErr := fmt.Errorf("%w: fallback provider down", contextfabric.ErrModelUnavailable)
	fallbackReceipt := validReceipt(contextfabric.ModelOperationInterpret)
	fallbackReceipt.Outcome = "unavailable"
	runtime := mustRuntime(t, &generatorStub{
		interpretErr: core.NewError(core.RESOURCE_EXHAUSTED, "primary quota exhausted"),
	}, Config{
		MaxAttempts: 1,
		Fallback:    erroringFallbackRuntime{err: fallbackErr, receipt: fallbackReceipt},
	})

	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())

	if !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("InterpretQuestion() error = %v, want the FALLBACK leg's own classification (ErrModelUnavailable)", err)
	}
	if errors.Is(err, contextfabric.ErrModelRateLimited) {
		t.Fatalf("InterpretQuestion() error = %v, must not carry the PRIMARY leg's classification once the fallback also ran and failed", err)
	}
	if receipt.FallbackUsed {
		t.Fatalf("receipt.FallbackUsed = true, want false: the fallback call never produced usable output, so the receipt must not claim fallback use")
	}
	if receipt.Outcome != "unavailable" {
		t.Fatalf("receipt.Outcome = %q, want the fallback leg's own outcome %q, not \"fallback\" (which means the fallback succeeded)", receipt.Outcome, "unavailable")
	}
}

// TestRuntimeRecordsTheFinalLegsFailureWhenSynthesisFallbackAlsoFails is the
// SynthesizeAnswer counterpart -- see
// TestRuntimeRecordsTheFinalLegsFailureWhenInterpretationFallbackAlsoFails.
func TestRuntimeRecordsTheFinalLegsFailureWhenSynthesisFallbackAlsoFails(t *testing.T) {
	t.Parallel()
	fallbackErr := fmt.Errorf("%w: fallback output failed validation", contextfabric.ErrModelOutput)
	fallbackReceipt := validReceipt(contextfabric.ModelOperationSynthesize)
	fallbackReceipt.Outcome = "invalid_output"
	runtime := mustRuntime(t, &generatorStub{
		synthesisErr: core.NewError(core.RESOURCE_EXHAUSTED, "primary quota exhausted"),
	}, Config{
		MaxAttempts: 1,
		Fallback:    erroringFallbackRuntime{err: fallbackErr, receipt: fallbackReceipt},
	})

	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())

	if !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("SynthesizeAnswer() error = %v, want the FALLBACK leg's own classification (ErrModelOutput)", err)
	}
	if errors.Is(err, contextfabric.ErrModelRateLimited) {
		t.Fatalf("SynthesizeAnswer() error = %v, must not carry the PRIMARY leg's classification once the fallback also ran and failed", err)
	}
	if receipt.FallbackUsed {
		t.Fatalf("receipt.FallbackUsed = true, want false: the fallback call never produced usable output, so the receipt must not claim fallback use")
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt.Outcome = %q, want the fallback leg's own outcome %q, not \"fallback\" (which means the fallback succeeded)", receipt.Outcome, "invalid_output")
	}
}

// TestRuntimeRecordsTheFinalLegsFailureWhenInterpretationIsSemanticallyInvalidAndFallbackAlsoFails
// is the CHAOS-3770 F4 residual probe: the SAME receipt-corruption/wrong-
// classification bug as TestRuntimeRecordsTheFinalLegsFailureWhenInterpretationFallbackAlsoFails,
// but triggered via the OTHER fallback branch -- the primary's output was
// parseable but SEMANTICALLY invalid (fails toDomain/Validate, not a
// generation error), and the fallback that's then tried also fails. The
// caller must still see the fallback's own (final) classification and
// receipt.Outcome, not the primary's stale "invalid_output"/ErrModelOutput.
func TestRuntimeRecordsTheFinalLegsFailureWhenInterpretationIsSemanticallyInvalidAndFallbackAlsoFails(t *testing.T) {
	t.Parallel()
	invalidOutput := validInterpretationOutput()
	invalidOutput.Shape = "registered_plan_only" // not a member of the closed shape vocabulary
	fallbackErr := fmt.Errorf("%w: fallback provider down", contextfabric.ErrModelUnavailable)
	fallbackReceipt := validReceipt(contextfabric.ModelOperationInterpret)
	fallbackReceipt.Outcome = "unavailable"
	runtime := mustRuntime(t, &generatorStub{interpretation: invalidOutput}, Config{
		MaxAttempts: 1,
		Fallback:    erroringFallbackRuntime{err: fallbackErr, receipt: fallbackReceipt},
	})

	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())

	if !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("InterpretQuestion() error = %v, want the FALLBACK leg's own classification (ErrModelUnavailable)", err)
	}
	if errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("InterpretQuestion() error = %v, must not carry the PRIMARY leg's stale invalid_output/ErrModelOutput classification once the fallback also ran and failed", err)
	}
	if receipt.FallbackUsed {
		t.Fatalf("receipt.FallbackUsed = true, want false: the fallback call never produced usable output, so the receipt must not claim fallback use")
	}
	if receipt.Outcome != "unavailable" {
		t.Fatalf("receipt.Outcome = %q, want the fallback leg's own outcome %q, not the primary's stale \"invalid_output\"", receipt.Outcome, "unavailable")
	}
}

// TestRuntimeRecordsTheFinalLegsFailureWhenSynthesisIsSemanticallyInvalidAndFallbackAlsoFails
// is the SynthesizeAnswer counterpart -- see
// TestRuntimeRecordsTheFinalLegsFailureWhenInterpretationIsSemanticallyInvalidAndFallbackAlsoFails.
func TestRuntimeRecordsTheFinalLegsFailureWhenSynthesisIsSemanticallyInvalidAndFallbackAlsoFails(t *testing.T) {
	t.Parallel()
	invalidOutput := validSynthesisOutput()
	invalidOutput.EvidenceRefIDs = []string{"evidence_not_in_input"} // fails ValidateAgainst grounding
	fallbackErr := fmt.Errorf("%w: fallback provider down", contextfabric.ErrModelRateLimited)
	fallbackReceipt := validReceipt(contextfabric.ModelOperationSynthesize)
	fallbackReceipt.Outcome = "rate_limited"
	runtime := mustRuntime(t, &generatorStub{synthesis: invalidOutput}, Config{
		MaxAttempts: 1,
		Fallback:    erroringFallbackRuntime{err: fallbackErr, receipt: fallbackReceipt},
	})

	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())

	if !errors.Is(err, contextfabric.ErrModelRateLimited) {
		t.Fatalf("SynthesizeAnswer() error = %v, want the FALLBACK leg's own classification (ErrModelRateLimited)", err)
	}
	if errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("SynthesizeAnswer() error = %v, must not carry the PRIMARY leg's stale invalid_output/ErrModelOutput classification once the fallback also ran and failed", err)
	}
	if receipt.FallbackUsed {
		t.Fatalf("receipt.FallbackUsed = true, want false: the fallback call never produced usable output, so the receipt must not claim fallback use")
	}
	if receipt.Outcome != "rate_limited" {
		t.Fatalf("receipt.Outcome = %q, want the fallback leg's own outcome %q, not the primary's stale \"invalid_output\"", receipt.Outcome, "rate_limited")
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

// TestClassifyModelErrorAnchorsStatusExtractionToACRsOwnSanitizedToken is a
// CHAOS-3770 follow-up probe. Found because a rebased PR's CI landed on an
// ephemeral test port containing the digits "429" -- the flake WAS the
// bug, not a race or test-state contamination: classifyModelError's
// fallback did strings.Contains(lower, "429") against the WHOLE
// unstructured error text, and since apierror.Error.Error() embeds the
// full request URL verbatim, ANY substring in that URL (a test's
// ephemeral port, or in production a BYO endpoint's own hostname, port,
// or path segment) containing those three digits produced a false
// rate-limit classification for a completely unrelated status. Confirmed
// directly: feeding classifyModelError the literal string this shape
// produces for a genuine 401 on port 55429 returned ErrModelRateLimited.
//
// classifyModelError now extracts the status code ONLY from the fixed,
// ACR-controlled token sanitizeProviderErrorBody embeds in every
// sanitized response ("provider response redacted by ACR (status <code>
// <text>)") -- text no real provider ever supplies and no incidental URL
// component can produce, so it cannot collide with anything else in the
// error string.
func TestClassifyModelErrorAnchorsStatusExtractionToACRsOwnSanitizedToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		wantIs error
	}{
		{
			// The exact literal shape observed in CI: a 401 (unrelated to
			// rate limiting) whose request URL happens to use an
			// ephemeral port containing "429".
			name:   "port_number_containing_429_digits_is_not_a_rate_limit_signal",
			err:    errors.New(`failed to create completion: POST "http://127.0.0.1:55429/v1/chat/completions": 401 Unauthorized {"message":"provider response redacted by ACR (status 401 Unauthorized)","type":"acr_sanitized_error","param":null,"code":null}`),
			wantIs: contextfabric.ErrModelUnavailable,
		},
		{
			name:   "genuinely_sanitized_429_still_classifies_as_rate_limited",
			err:    errors.New(`failed to create completion: POST "http://127.0.0.1:12345/v1/chat/completions": 429 Too Many Requests {"message":"provider response redacted by ACR (status 429 Too Many Requests)","type":"acr_sanitized_error","param":null,"code":null}`),
			wantIs: contextfabric.ErrModelRateLimited,
		},
		{
			// Adversarial: a port number that ALSO happens to contain
			// "429" as a substring (4290 contains "429"), on a genuinely
			// unrelated 503 -- proves the anchor, not mere avoidance of
			// the exact 55429 case above.
			name:   "port_number_4290_does_not_leak_into_a_503_classification",
			err:    errors.New(`failed to create completion: POST "http://127.0.0.1:4290/v1/chat/completions": 503 Service Unavailable {"message":"provider response redacted by ACR (status 503 Service Unavailable)","type":"acr_sanitized_error","param":null,"code":null}`),
			wantIs: contextfabric.ErrModelUnavailable,
		},
		{
			// Adversarial: the request URL itself embeds the FULL literal
			// sanitized-token text with a fake "429" (a BYO endpoint path
			// segment could do this), preceding the REAL sanitized token
			// the SDK appends for the actual response body. The anchor
			// must resolve to the LAST match -- the genuine sanitized
			// token always comes after the URL in the SDK's error format
			// -- not the first.
			name:   "url_embedded_fake_sanitized_token_does_not_override_the_real_trailing_one",
			err:    errors.New(`failed to create completion: POST "http://127.0.0.1:12345/v1/provider response redacted by ACR (status 429 Too Many Requests)/chat/completions": 503 Service Unavailable {"message":"provider response redacted by ACR (status 503 Service Unavailable)","type":"acr_sanitized_error","param":null,"code":null}`),
			wantIs: contextfabric.ErrModelUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyModelError(tc.err)
			if !errors.Is(got, tc.wantIs) {
				t.Fatalf("classifyModelError(%v) = %v, want errors.Is match for %v", tc.err, got, tc.wantIs)
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
		{"internal_not_retryable_even_with_transient_wording", core.NewError(core.INTERNAL, "unexpected panic: connection reset while parsing"), false},
		{"context_canceled_not_retryable", context.Canceled, false},
		// CHAOS-3770 F2: an UNSTRUCTURED error (not *core.GenkitError -- what
		// the real OpenAI SDK returns, since compat_oai/generate.go wraps the
		// SDK's own error with plain fmt.Errorf rather than producing a
		// GenkitError) must never be retried by sniffing its message for a
		// transient-sounding substring. The OpenAI SDK's error Error() method
		// embeds the raw provider response body verbatim
		// (openai-go/internal/apierror.Error.Error()), so a NON-transient
		// validation failure whose body happens to quote a word like
		// "timeout" or contain "502" would otherwise be retried with the
		// identical payload, violating the no-retry-same-input rule
		// (operations.md). These two cases replace the old
		// "plain_502_retryable=true" expectation, which encoded exactly that
		// defect.
		{"plain_502_not_retryable_unstructured", errors.New("upstream returned 502"), false},
		{"unstructured_error_embedding_transient_word_not_retried", errors.New(`400 Bad Request: {"error":{"message":"Rejected prompt: request timeout budget of 30s exceeded for this input","type":"invalid_request_error","code":"invalid_prompt"}}`), false},
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

// TestRuntimeDoesNotRetryAnUnstructuredNonTransientErrorWithTheSamePayload is
// the CHAOS-3770 F2 end-to-end probe: it drives the retry loop itself
// (rather than calling retryable() directly) to prove that an unstructured,
// non-transient generation failure whose message happens to contain a
// transient-sounding word is called exactly once, never resubmitted with the
// same encoded payload.
func TestRuntimeDoesNotRetryAnUnstructuredNonTransientErrorWithTheSamePayload(t *testing.T) {
	t.Parallel()
	stub := &generatorStub{
		interpretErr: errors.New(`400 Bad Request: {"error":{"message":"Rejected prompt: request timeout budget of 30s exceeded for this input","type":"invalid_request_error"}}`),
	}
	runtime := mustRuntime(t, stub, Config{MaxAttempts: 3})

	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())

	if err == nil {
		t.Fatal("InterpretQuestion() error = nil, want the classified generation failure")
	}
	if len(stub.requests) != 1 {
		t.Fatalf("generator was called %d times, want exactly 1 -- a non-transient unstructured error must not be retried with the same payload", len(stub.requests))
	}
	if receipt.Attempts != 1 {
		t.Fatalf("receipt.Attempts = %d, want 1", receipt.Attempts)
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
