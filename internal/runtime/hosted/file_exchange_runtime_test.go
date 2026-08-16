package hosted_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fileExchangeRuntime is CHAOS-3742 arm 4's diagnostic generative
// transport: it implements contextfabric.ModelRuntime by writing a
// self-contained request file (the EXACT system prompt, user payload, and
// output JSON Schema genkitruntime.Runtime would send/expect -- via
// genkitruntime's exported exchange-support helpers, never reimplemented)
// and polling for a response file, instead of calling a model API. Wired
// through hosted.Options.ModelRuntimeOverride, so everything downstream --
// RuntimeQuestionInterpreter/RuntimeAnswerSynthesizer's own
// Validate/ValidateAgainst + classification, receipt recording via the
// SAME ModelReceiptSink, the engine, the graph, the canonical facts -- is
// the identical production pipeline. Only the generative call itself is
// swapped for a file exchange an out-of-process responder (a separate
// agent) answers.
//
// Explicitly diagnostic: purpose is to locate the bottleneck (model
// quality at interpretation vs. the retrieval/gates pipeline itself), not
// to propose this as a deployable arm. Latency/cost are NOT comparable to
// a real provider (an interactive human/agent responder, no API pricing);
// receipts record token usage as unavailable (zero ModelUsage) with a
// distinct Provider/Model label so they are never confused with a real
// billable call when read back from acr.context_fabric_model_execution_receipts.
type fileExchangeRuntime struct {
	dir     string
	model   string
	timeout time.Duration
	poll    time.Duration
	seq     atomic.Int64
}

func newFileExchangeRuntime(dir, model string, timeout time.Duration) (*fileExchangeRuntime, error) {
	for _, sub := range []string{"requests", "responses"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create exchange dir %s: %w", sub, err)
		}
	}
	return &fileExchangeRuntime{dir: dir, model: model, timeout: timeout, poll: 500 * time.Millisecond}, nil
}

// exchangeRequest is the self-contained file a responder reads: the full
// prompt exactly as the real API would receive it, plus the exact output
// contract, plus instructions. No corpus text beyond what production
// itself would send to a model for this operation -- the same information
// boundary a real model call already crosses.
type exchangeRequest struct {
	Operation    string          `json:"operation"` // "interpret" | "synthesize"
	Seq          int64           `json:"seq"`
	System       string          `json:"system"`
	Prompt       string          `json:"prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Instructions string          `json:"instructions"`
}

type exchangeResponse struct {
	Output json.RawMessage `json:"output"`
	Error  string          `json:"error,omitempty"`
}

func (f *fileExchangeRuntime) exchange(ctx context.Context, operation, system, prompt string, schema []byte) (json.RawMessage, error) {
	seq := f.seq.Add(1)
	reqPath := filepath.Join(f.dir, "requests", fmt.Sprintf("%06d-%s.json", seq, operation))
	respPath := filepath.Join(f.dir, "responses", fmt.Sprintf("%06d-%s.json", seq, operation))

	req := exchangeRequest{
		Operation: operation, Seq: seq, System: system, Prompt: prompt, OutputSchema: schema,
		Instructions: "Produce a single JSON object as the response file's `output` field, matching `output_schema` exactly (every required field present, enum values exactly as listed). Base your answer ONLY on `system` and `prompt` -- do not invent facts not present in them. Write the response as {\"output\": <the JSON object>} to the response file path you were given for this request.",
	}
	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal exchange request: %w", err)
	}
	if err := os.WriteFile(reqPath, body, 0o644); err != nil {
		return nil, fmt.Errorf("write exchange request: %w", err)
	}

	deadline := time.Now().Add(f.timeout)
	ticker := time.NewTicker(f.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			raw, err := os.ReadFile(respPath)
			if err != nil {
				if os.IsNotExist(err) {
					if time.Now().After(deadline) {
						return nil, fmt.Errorf("file-exchange timed out after %s waiting for %s", f.timeout, respPath)
					}
					continue
				}
				return nil, fmt.Errorf("read exchange response: %w", err)
			}
			var resp exchangeResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return nil, fmt.Errorf("parse exchange response %s: %w", respPath, err)
			}
			if resp.Error != "" {
				return nil, fmt.Errorf("exchange responder reported an error: %s", resp.Error)
			}
			return resp.Output, nil
		}
	}
}

func (f *fileExchangeRuntime) baseReceipt(operation contextfabric.ModelOperation, started, completed time.Time, prompt string) contextfabric.ModelExecutionReceipt {
	return contextfabric.ModelExecutionReceipt{
		Operation: operation, Provider: "file-exchange", Model: f.model, ModelVersion: "n/a",
		PromptVersion: "chaos-3742-trial-arm4", SchemaVersion: "v1", EvaluatorVersion: "chaos-3742-trial-arm4",
		StartedAt: started, CompletedAt: completed, Attempts: 1,
		InputDigest: contextfabric.DigestModelValue([]byte(prompt)),
		// Usage intentionally left zero: no per-token API accounting exists
		// for an interactive out-of-process responder. Provider/Model above
		// distinguish these rows from a real billable call on read-back.
	}
}

func (f *fileExchangeRuntime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	prompt, err := genkitruntime.BuildInterpretationPrompt(request, genkitruntime.DefaultExchangeMaxInputBytes)
	if err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, err
	}
	schema, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, err
	}
	started := time.Now().UTC()
	raw, exchangeErr := f.exchange(ctx, "interpret", genkitruntime.InterpretationSystemPrompt(), prompt, schema)
	completed := time.Now().UTC()
	receipt := f.baseReceipt(contextfabric.ModelOperationInterpret, started, completed, prompt)
	if exchangeErr != nil {
		receipt.Outcome = "unavailable"
		return contextfabric.InterpretedQuestion{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelUnavailable, exchangeErr)
	}
	interpreted, parseErr := genkitruntime.ParseInterpretationOutput(raw, request.TimeContext)
	if parseErr != nil {
		receipt.Outcome = "invalid_output"
		return contextfabric.InterpretedQuestion{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelOutput, parseErr)
	}
	receipt.OutputDigest = contextfabric.DigestModelValue(raw)
	// "pending_validation": RuntimeQuestionInterpreter.Interpret runs its
	// own Validate()+classification next, exactly as it does for the real
	// genkit runtime, and upgrades this to "success" itself.
	receipt.Outcome = "pending_validation"
	return interpreted, receipt, nil
}

func (f *fileExchangeRuntime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	prompt, err := genkitruntime.BuildSynthesisPrompt(input, genkitruntime.DefaultExchangeMaxInputBytes)
	if err != nil {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, err
	}
	schema, err := genkitruntime.SynthesisOutputSchema()
	if err != nil {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, err
	}
	started := time.Now().UTC()
	raw, exchangeErr := f.exchange(ctx, "synthesize", genkitruntime.SynthesisSystemPrompt(), prompt, schema)
	completed := time.Now().UTC()
	receipt := f.baseReceipt(contextfabric.ModelOperationSynthesize, started, completed, prompt)
	if exchangeErr != nil {
		receipt.Outcome = "unavailable"
		return contextfabric.SynthesisDraft{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelUnavailable, exchangeErr)
	}
	draft, parseErr := genkitruntime.ParseSynthesisOutput(raw)
	if parseErr != nil {
		receipt.Outcome = "invalid_output"
		return contextfabric.SynthesisDraft{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelOutput, parseErr)
	}
	receipt.OutputDigest = contextfabric.DigestModelValue(raw)
	receipt.Outcome = "pending_validation"
	return draft, receipt, nil
}
