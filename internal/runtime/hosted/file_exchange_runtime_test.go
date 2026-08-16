package hosted_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fileExchangeRuntime is CHAOS-3742 arm 4/5's diagnostic generative
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
// agent, or a codex-exec subprocess for arm 5) answers.
//
// Explicitly diagnostic: purpose is to locate the bottleneck (model
// quality at interpretation vs. the retrieval/gates pipeline itself), not
// to propose this as a deployable arm. Latency/cost are NOT comparable to
// a real provider (an interactive responder, no API pricing); receipts
// record token usage as unavailable (zero ModelUsage) with a distinct
// Provider/Model label so they are never confused with a real billable
// call when read back from acr.context_fabric_model_execution_receipts.
//
// SINGLE-ATTEMPT BY DESIGN (sol review F7): unlike the real genkitruntime
// (r.withRetry), this transport never retries a failed exchange -- a
// timeout or a responder error surfaces as one classified failure per
// call. Retry parity with the real runtime's transient-failure
// classification was judged not cheaply reusable without duplicating
// withRetry's genkit-coupled retry predicate; documented here rather than
// implemented, per the explicitly offered lighter option.
type fileExchangeRuntime struct {
	dir     string
	model   string
	timeout time.Duration
	poll    time.Duration
	nonce   string
	seq     atomic.Int64
}

func newFileExchangeRuntime(dir, model string, timeout time.Duration) (*fileExchangeRuntime, error) {
	for _, sub := range []string{"requests", "responses"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create exchange dir %s: %w", sub, err)
		}
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate exchange session nonce: %w", err)
	}
	return &fileExchangeRuntime{dir: dir, model: model, timeout: timeout, poll: 500 * time.Millisecond, nonce: hex.EncodeToString(nonceBytes)}, nil
}

// exchangeRequest is the self-contained file a responder reads: the full
// prompt exactly as the real API would receive it, plus the exact output
// contract, plus instructions. No corpus text beyond what production
// itself would send to a model for this operation -- the same information
// boundary a real model call already crosses.
//
// SessionNonce (sol review F6): a fresh random value generated once per
// fileExchangeRuntime (per test process), echoed back required in the
// response. Session dirs are already timestamped-fresh (S3), but the
// nonce is a second, independent guard against ever accepting a leftover
// response file from a DIFFERENT run that happens to reuse a sequence
// number in the same dir (e.g. two runs pointed at the same dir by
// operator error).
type exchangeRequest struct {
	Operation    string          `json:"operation"` // "interpret" | "synthesize"
	Seq          int64           `json:"seq"`
	SessionNonce string          `json:"session_nonce"`
	System       string          `json:"system"`
	Prompt       string          `json:"prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Instructions string          `json:"instructions"`
}

type exchangeResponse struct {
	SessionNonce string          `json:"session_nonce"`
	Output       json.RawMessage `json:"output"`
	Error        string          `json:"error,omitempty"`
}

// errExchangeResponderReported classifies a responder-signaled failure
// (the exchangeResponse.Error field was non-empty). Its own error TEXT
// never carries the responder's content -- see the sol review F11 comment
// at its use site.
var errExchangeResponderReported = errors.New("file-exchange responder reported an error")

// errExchangeSessionMismatch classifies a response file whose echoed
// session_nonce does not match this run's -- almost certainly a stale
// leftover from a different session sharing this directory (sol review
// F6); treated as "not ready yet" (see exchange's poll loop) rather than
// a hard failure, since the correct file may still arrive before the
// deadline.
var errExchangeSessionMismatch = errors.New("file-exchange response session nonce mismatch")

func (f *fileExchangeRuntime) exchange(ctx context.Context, operation, system, prompt string, schema []byte) (json.RawMessage, error) {
	seq := f.seq.Add(1)
	name := fmt.Sprintf("%06d-%s.json", seq, operation)
	reqPath := filepath.Join(f.dir, "requests", name)
	respPath := filepath.Join(f.dir, "responses", name)

	req := exchangeRequest{
		Operation: operation, Seq: seq, SessionNonce: f.nonce, System: system, Prompt: prompt, OutputSchema: schema,
		Instructions: "Produce a single JSON object as the response file's `output` field, matching `output_schema` exactly (every required field present, enum values exactly as listed). Base your answer ONLY on `system` and `prompt` -- do not invent facts not present in them. Echo `session_nonce` back UNCHANGED in your response. Write the response as {\"session_nonce\": <the same value>, \"output\": <the JSON object>} to the response file path you were given for this request.",
	}
	body, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal exchange request: %w", err)
	}
	// Request publication via temp+rename (sol review F8, symmetric with
	// the response-side torn-read tolerance below): a responder watching
	// requests/ with its own poll loop could otherwise observe a
	// partially-written request file. Same-directory temp file so the
	// rename is atomic on the same filesystem.
	tmp, err := os.CreateTemp(filepath.Join(f.dir, "requests"), "."+name+".tmp*")
	if err != nil {
		return nil, fmt.Errorf("create temp exchange request: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("write temp exchange request: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("close temp exchange request: %w", err)
	}
	if err := os.Rename(tmpPath, reqPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("publish exchange request: %w", err)
	}

	deadline := time.Now().Add(f.timeout)
	ticker := time.NewTicker(f.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Preserve context.Canceled/DeadlineExceeded identity (sol
			// review F7): ctx.Err() unwrapped, not renamed into a bespoke
			// message, so the classifier's errors.Is(err,
			// context.DeadlineExceeded)/context.Canceled checks still see
			// it after InterpretQuestion/SynthesizeAnswer wrap it below.
			return nil, ctx.Err()
		case <-ticker.C:
			raw, err := os.ReadFile(respPath)
			if err != nil {
				if os.IsNotExist(err) {
					if time.Now().After(deadline) {
						return nil, fmt.Errorf("%w: waited %s for %s", context.DeadlineExceeded, f.timeout, respPath)
					}
					continue
				}
				return nil, fmt.Errorf("read exchange response: %w", err)
			}
			var resp exchangeResponse
			if unmarshalErr := json.Unmarshal(raw, &resp); unmarshalErr != nil {
				// Torn-read protection (fable-review finding): a responder
				// that writes the response file in place (not
				// temp-file-then-rename) can be observed mid-write by our
				// poll tick. A parse failure here is treated as "not ready
				// yet" and retried until the deadline, not a hard failure
				// -- the alternative (failing on the first malformed read)
				// would misclassify a normal write-in-progress race as a
				// responder error.
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("%w: response file never parsed cleanly after %s (last error: %v)", context.DeadlineExceeded, f.timeout, unmarshalErr)
				}
				continue
			}
			if resp.SessionNonce != f.nonce {
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("%w: waited %s for a matching session_nonce on %s", errExchangeSessionMismatch, f.timeout, respPath)
				}
				continue
			}
			if resp.Error != "" {
				// sol review F11: resp.Error is RESPONDER-SUPPLIED text (an
				// out-of-process agent, not production's sanitized provider
				// error path -- SanitizeProviderErrorBody never runs on
				// this transport). It must never be serialized into a Go
				// error message that could reach a report field: only a
				// fixed class name and a byte length, never the content.
				return nil, fmt.Errorf("%w (%d bytes)", errExchangeResponderReported, len(resp.Error))
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
		// %w (not %v, sol review F7): preserves context.DeadlineExceeded/
		// Canceled identity when that is the underlying cause, alongside
		// ErrModelUnavailable -- Go's multi-%w support means
		// errors.Is(err, context.DeadlineExceeded) still matches through
		// this wrap.
		return contextfabric.InterpretedQuestion{}, receipt, fmt.Errorf("%w: %w", contextfabric.ErrModelUnavailable, exchangeErr)
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
		return contextfabric.SynthesisDraft{}, receipt, fmt.Errorf("%w: %w", contextfabric.ErrModelUnavailable, exchangeErr)
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
