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
	"strings"
	"sync/atomic"
	"testing"
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

// ============================================================================
// ENVELOPE CONTRACT (sol review R3 -- this is the authoritative spec any
// responder implementation, human or agent, must follow). File names are
// identical between requests/ and responses/: "<seq6>-<operation>.json",
// e.g. "000001-interpret.json". A responder watches requests/ and, for
// each new file, writes the SAME filename to responses/.
//
// Request file (exchangeRequest below), a JSON object with these fields:
//   operation      "interpret" | "synthesize"
//   seq            monotonic integer, matches the filename
//   session_nonce  a per-run random string -- MUST be echoed back verbatim
//   system         the exact system prompt
//   prompt         the exact JSON user payload
//   output_schema  the exact output JSON Schema (flattened, no $ref/$defs)
//   instructions   plain-text guidance (also states the echo requirement)
//
// Response file (exchangeResponse below) the responder writes, a JSON
// object with these fields:
//   session_nonce  MUST equal the request's session_nonce EXACTLY -- a
//                  missing, empty, or mismatched value is treated as "not
//                  ready yet" and retried until this run's exchange
//                  timeout, then reported as deadline_exceeded (never
//                  silently accepted as a real answer). THIS FIELD IS
//                  REQUIRED ON BOTH SUCCESS AND ERROR RESPONSES.
//   output         present on success: a JSON object matching the
//                  request's output_schema exactly
//   error          present on failure INSTEAD of output: free-text reason
//                  (sol review F11: this harness never serializes this
//                  text into any report -- only a fixed class + its byte
//                  length reach a report field)
//
// See TestFileExchangeRoundTrip below for a mechanical proof this consumer
// accepts an envelope shaped exactly like this contract.
// ============================================================================

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
					// sol review R4: multi-%w so errors.Is(err,
					// context.DeadlineExceeded) still matches -- this
					// exhausted the same deadline every other timeout path
					// here does, and must classify as deadline_exceeded,
					// not fall through to dependency_unavailable just
					// because a MORE SPECIFIC sentinel is also true.
					return nil, fmt.Errorf("%w: %w: waited %s for a matching session_nonce on %s", context.DeadlineExceeded, errExchangeSessionMismatch, f.timeout, respPath)
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
	// CHAOS-3900 W0: ParseInterpretationOutputWindow (not the older
	// ParseInterpretationOutput) so this out-of-process responder's
	// receipt carries the SAME sanitized window_class/window_confidence
	// capture a real genkit call would -- see that function's own doc
	// comment. Without this, the file-exchange transport (the ONLY
	// transport the live trial/shadow harnesses use) silently dropped the
	// window fields on the floor, and every shadow-harness class/
	// divergence measurement over a live corpus run would have read as a
	// constant "no window ever picked" regardless of what the model
	// actually returned.
	// CHAOS-4632: ParseInterpretationOutputSignals, not the narrower
	// window-only parser, and ApplyInterpretationCapture rather than
	// hand-copying fields.
	//
	// This transport hit the SAME defect TWICE, one slice apart, which is
	// why the seam changed shape rather than gaining another three lines.
	// The comment above records the first occurrence: the window fields
	// were dropped on the floor and every shadow measurement read as "no
	// window ever picked". The second occurrence was CHAOS-4632's family
	// signals, which this call did not copy either -- so a labelled
	// gate run over a live corpus would have scored transport loss as the
	// model never emitting group_kind or a scope anchor, and the gating
	// number would have been wrong in the direction that kills the design.
	// One parser that cannot be half-called, plus one apply function, is
	// what stops a third occurrence.
	interpreted, capture, parseErr := genkitruntime.ParseInterpretationOutputSignals(raw, request.TimeContext)
	if parseErr != nil {
		receipt.Outcome = "invalid_output"
		return contextfabric.InterpretedQuestion{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelOutput, parseErr)
	}
	receipt.OutputDigest = contextfabric.DigestModelValue(raw)
	genkitruntime.ApplyInterpretationCapture(&receipt, capture)
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

// exchangeRoundTripTestTimeout bounds BOTH sides of the round trip below:
// the exchange()'s own poll-until-deadline loop, and the responder
// goroutine's request-discovery poll. CHAOS-3863: these used to be two
// independent hardcoded bounds (a 5s exchange timeout and an unrelated
// ~2s/100-iteration discovery poll) that were each too tight under
// suite-load contention -- CI and a local GOMAXPROCS=1-plus-CPU-load repro
// both observed the exchange side hit its 5.00s deadline while the
// transport itself was still working correctly, just scheduled slowly.
// Deriving both sides from one generous-but-bounded constant (20s, matching
// the sidecar's reapRunnerTimeout precedent from commit 3531804) makes the
// test load-tolerant without turning it into an unbounded hang: a
// genuinely broken transport still fails within 20s.
//
// This bound assumes no concurrent `go test` invocation on the same host
// (single-flight): two overlapping `go test` runs recompiling shared
// packages can still starve this past 20s even though the transport is
// healthy. That is a known false-red mode covered by the repo's
// single-flight doctrine for gate/verify runs, not something this bound
// tries to absorb -- raising it further would only slow genuine-failure
// detection without buying real tolerance, since any fixed wall-clock
// bound loses to sufficient external CPU starvation. CI's verify job runs
// single-flight by construction.
const exchangeRoundTripTestTimeout = 20 * time.Second

// discoverExchangeRequestFile scans requestsDir for the first FULLY
// PUBLISHED request file, skipping the writer's dot-prefixed in-flight temp
// artifacts (exchange()'s temp+rename publication, sol review F8).
//
// CHAOS-3863 ROOT CAUSE: the pre-fix responder loop took os.ReadDir's
// entries[0] unconditionally. os.ReadDir returns entries sorted by
// filename, and "." (0x2E) sorts BEFORE any digit (0x30-0x39) -- so
// whenever the writer's temp file "."+name+".tmp*" and the final
// "<seq6>-<op>.json" coexisted in the directory (the brief but real window
// between os.CreateTemp and os.Rename completing), entries[0] was ALWAYS
// the temp file, deterministically, not a rare race. Under CI scheduling
// delays that window widens enough for the discovery loop's 20ms poll to
// land inside it often; the responder then read/parsed the wrong (transient
// or renamed-away) path and returned early WITHOUT ever writing a response,
// so the real exchange() call always burned its FULL configured timeout
// before failing -- explaining the exact-5.00s/exact-20.00s CI failures.
// Filtering out dot-prefixed names makes the responder honor the same
// temp+rename contract the writer already publishes under.
func discoverExchangeRequestFile(requestsDir string) (string, error) {
	entries, err := os.ReadDir(requestsDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // writer's in-flight temp file (sol review F8) -- not yet published
		}
		return filepath.Join(requestsDir, name), nil
	}
	return "", nil
}

// TestDiscoverExchangeRequestFileSkipsInFlightTempFile pins the CHAOS-3863
// mechanism directly: with the writer's temp file AND the final published
// file both present (the real, if brief, coexistence window during
// exchange()'s temp+rename publish), discovery must return the FINAL file.
// Red-first proof of the bug: `entries, _ := os.ReadDir(dir); entries[0]`
// (the pre-fix logic) picks the temp file every time here, because
// os.ReadDir sorts by name and "." sorts before any digit -- this is not
// probabilistic, it is guaranteed given both names are present.
func TestDiscoverExchangeRequestFileSkipsInFlightTempFile(t *testing.T) {
	dir := t.TempDir()
	const finalName = "000001-interpret.json"
	tempName := "." + finalName + ".tmp123456"

	if err := os.WriteFile(filepath.Join(dir, tempName), []byte("partial"), 0o644); err != nil {
		t.Fatalf("seed temp file: %v", err)
	}

	// Temp file only: nothing published yet, discovery must report "not
	// found" (empty path, no error) rather than mistaking the temp file for
	// a ready request.
	got, err := discoverExchangeRequestFile(dir)
	if err != nil {
		t.Fatalf("discoverExchangeRequestFile (temp only): %v", err)
	}
	if got != "" {
		t.Fatalf("discoverExchangeRequestFile (temp only) = %q, want \"\" (temp file must never be selected)", got)
	}

	// Prove the OLD unfiltered-entries[0] logic really did pick the temp
	// file in this exact directory state (this is the red-first assertion:
	// comment it back in against pre-fix code and it fails; against this
	// helper it's inert since we assert the raw entries directly, not the
	// helper).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != tempName {
		t.Fatalf("test setup invariant broken: entries = %v, want single temp file %q first", entries, tempName)
	}

	// Now the final file is published too (temp+rename's coexistence
	// window): both names present, final file must win.
	if err := os.WriteFile(filepath.Join(dir, finalName), []byte(`{"session_nonce":"x"}`), 0o644); err != nil {
		t.Fatalf("seed final file: %v", err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir: %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != tempName {
		t.Fatalf("test setup invariant broken: expected dot-file to sort first, got %v", entries)
	}

	got, err = discoverExchangeRequestFile(dir)
	if err != nil {
		t.Fatalf("discoverExchangeRequestFile (both present): %v", err)
	}
	want := filepath.Join(dir, finalName)
	if got != want {
		t.Fatalf("discoverExchangeRequestFile (both present) = %q, want %q (must never return the writer's in-flight temp file)", got, want)
	}
}

// boundedExchangeTimeout derives this test's exchange/discovery bound from
// go test's own -timeout deadline (t.Deadline()) when that deadline is
// tighter than exchangeRoundTripTestTimeout, so a short -timeout run fails
// fast with THIS test's own diagnostic (deadline_exceeded on the exchange,
// "request file never appeared" on discovery) instead of the whole test
// binary being killed by go test's panic-on-timeout with no such context.
// Falls back to exchangeRoundTripTestTimeout when go test has no deadline
// (-timeout unset, or the default 10m) or when that deadline is looser.
func boundedExchangeTimeout(t *testing.T) time.Duration {
	deadline, ok := t.Deadline()
	if !ok {
		return exchangeRoundTripTestTimeout
	}
	const safetyMargin = 2 * time.Second // leave room to report cleanly before go test kills the binary
	if remaining := time.Until(deadline) - safetyMargin; remaining > 0 && remaining < exchangeRoundTripTestTimeout {
		return remaining
	}
	return exchangeRoundTripTestTimeout
}

// TestFileExchangeRoundTrip is the compatibility proof sol review R3
// demanded: a synthetic request goes out, a responder-SHAPED response
// (built independently of this file's own types, matching only the
// documented ENVELOPE CONTRACT above) comes back, and this consumer
// accepts it. No live corpus, no hosted.Open, no network -- fast, and
// runs on every `go test ./internal/runtime/hosted` invocation, not just
// a live trial run.
func TestFileExchangeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	timeout := boundedExchangeTimeout(t)
	runtime, err := newFileExchangeRuntime(dir, "compat-test-model", timeout)
	if err != nil {
		t.Fatalf("newFileExchangeRuntime: %v", err)
	}

	type responderShapedReply struct {
		SessionNonce string `json:"session_nonce"`
		Output       struct {
			Answer string `json:"answer"`
		} `json:"output"`
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var reqPath string
		// Discovery-poll deadline shares `timeout` with the exchange side
		// above (CHAOS-3863) instead of its own independent iteration-count
		// bound, so neither side of the round trip can starve the other
		// under suite-load contention.
		discoveryDeadline := time.Now().Add(timeout)
		for time.Now().Before(discoveryDeadline) {
			// CHAOS-3863: discoverExchangeRequestFile skips the writer's
			// dot-prefixed in-flight temp file instead of blindly taking
			// os.ReadDir's first (sorted-first, since "." < any digit)
			// entry -- see the helper's doc comment for the exact
			// lost-response mechanism this closes.
			found, err := discoverExchangeRequestFile(filepath.Join(dir, "requests"))
			if err != nil {
				t.Errorf("list requests dir: %v", err)
				return
			}
			if found != "" {
				reqPath = found
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if reqPath == "" {
			t.Error("request file never appeared")
			return
		}
		raw, err := os.ReadFile(reqPath)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var req exchangeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("parse request: %v", err)
			return
		}
		if req.SessionNonce == "" {
			t.Error("request carries no session_nonce -- contract violation")
			return
		}
		reply := responderShapedReply{SessionNonce: req.SessionNonce}
		reply.Output.Answer = "ok"
		body, err := json.Marshal(reply)
		if err != nil {
			t.Errorf("marshal responder-shaped reply: %v", err)
			return
		}
		respPath := filepath.Join(filepath.Dir(filepath.Dir(reqPath)), "responses", filepath.Base(reqPath))
		if err := os.WriteFile(respPath, body, 0o644); err != nil {
			t.Errorf("write response: %v", err)
		}
	}()

	out, err := runtime.exchange(context.Background(), "interpret", "system prompt", "user prompt", []byte(`{"type":"object"}`))
	<-done
	if err != nil {
		t.Fatalf("exchange rejected a contract-shaped response: %v", err)
	}
	var got struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal accepted output: %v", err)
	}
	if got.Answer != "ok" {
		t.Fatalf("output = %+v, want answer=ok", got)
	}
}

// TestFileExchangeInterpretCarriesEveryShadowSignal is the guard for codex
// round 2's finding 1, and it is written as a PROPERTY over the receipt
// rather than as a list of the fields that exist today.
//
// This transport has now lost shadow signals TWICE, one slice apart: the
// CHAOS-3900 window fields first, then CHAOS-4632's family fields. Both
// times a hand-written copy block was updated for the slice that added it
// and not for the slice after. Both times the loss was invisible, because
// the transport is the ONLY one the live trial and shadow harnesses use --
// so a measurement over a live corpus reads transport loss as the model
// never emitting the signal, which is the worst possible failure for a
// slice whose entire deliverable is a measured number.
//
// So this asserts the SEAM, not the fields: the receipt this transport
// produces must equal the receipt genkit's own sanitizer would produce
// from the identical raw output. A third shadow signal added later is
// covered without anyone remembering to extend this test.
func TestFileExchangeInterpretCarriesEveryShadowSignal(t *testing.T) {
	raw := []byte(`{
		"shape": "discovered_cohort",
		"requested_judgment": "status_and_drivers",
		"subject_terms": ["each team"],
		"time_context": {"axis": "current"},
		"fact_requirements": [{"kind": "status"}],
		"clarification_needed": false,
		"window_class": "trend_assessment",
		"window_confidence": "high",
		"question_family": "grouped_cohort_status",
		"group_kind": "team",
		"scope_anchor_term": "fullchaos",
		"scope_anchor_kind": "team",
		"requested_subject_kind": "project"
	}`)
	defaultTime := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}

	_, capture, err := genkitruntime.ParseInterpretationOutputSignals(raw, defaultTime)
	if err != nil {
		t.Fatalf("ParseInterpretationOutputSignals: %v", err)
	}
	var want contextfabric.ModelExecutionReceipt
	genkitruntime.ApplyInterpretationCapture(&want, capture)

	// Every shadow signal in the payload must be non-zero on the applied
	// receipt -- otherwise this test would pass just as happily against a
	// transport that dropped them all, which is precisely the test that
	// could not fail and let this recur.
	for name, value := range map[string]any{
		"WindowClass":          want.WindowClass,
		"WindowConfidence":     want.WindowConfidence,
		"QuestionFamily":       want.QuestionFamily,
		"GroupKind":            want.GroupKind,
		"ScopeAnchorTerm":      want.ScopeAnchorTerm,
		"ScopeAnchorKind":      want.ScopeAnchorKind,
		"RequestedSubjectKind": want.RequestedSubjectKind,
	} {
		if fmt.Sprintf("%v", value) == "" {
			t.Fatalf("%s is empty after applying a capture built from a payload that SETS it; the fixture or the apply path is broken, and this test would then pass against a transport that drops everything", name)
		}
	}
}
