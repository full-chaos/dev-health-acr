// Command acr-trial-responder-api is the OpenAI-API-backed sibling of
// scripts/trial/run-responder-codex.sh (CHAOS-3884 arm-5): it answers the
// SAME file-exchange envelope contract -- internal/runtime/hosted's
// fileExchangeRuntime (file_exchange_runtime_test.go's ENVELOPE CONTRACT
// comment) -- but via a direct OpenAI API call instead of a `codex exec`
// subscription subprocess.
//
// CHAOS-4313 (chris ruling, 2026-08-26 05:30 PDT): all trial responder runs
// move to the OpenAI API. Trial volume is decreasing, and the CPU load
// `codex exec` puts on the host now costs more than the API spend. This
// supersedes the "harnesses not API keys" standing rule for the measurement
// responder ONLY -- codex reviews are unchanged, and run-responder-codex.sh
// is retained for replaying historical runs
// (ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex).
//
// Usage: acr-trial-responder-api <exchange-dir> [poll-seconds]
//
// Contract (identical to run-responder-codex.sh): watches <exchange-dir>/
// requests for new request files and answers each by writing the SAME
// filename to <exchange-dir>/responses. Exits once <exchange-dir>/DONE
// exists and every published request has a matching response file.
//
// Auth: OPENAI_API_KEY must already be set in THIS PROCESS's environment --
// never read from argv, never logged, never rendered. scripts/trial/
// run-responder-api.sh resolves it via trial_secret OPENAI_API_KEY
// (ops/.env, the SAME source internal/contextfabric/embedprovider already
// uses for embeddings) and execs this binary with it set; this binary never
// falls back to any other credential source.
//
// Model: ACR_TEST_TRIAL_RESPONDER_MODEL (default "gpt-5.6-luna").
//
// Reasoning effort: ACR_TEST_TRIAL_RESPONDER_EFFORT (no default -- unset
// means the request's own ReasoningEffort field is never sent, so the
// provider's own default applies, unchanged from every run before this
// knob existed).
//
// Hygiene: every request/response body carries real corpus text (system
// prompt, user payload) -- this program NEVER writes that to stdout/
// stderr. Per-request operational logs (status, timing, failure class,
// byte/violation COUNTS -- never prompt/output content itself, matching
// the file-exchange runtime's own sol-review-F11 precedent of a byte
// length standing in for content) go to
// <exchange-dir>/_responder_logs/<request-file>.log, mirroring
// run-responder-codex.sh's own discipline.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

const (
	defaultModel               = "gpt-5.6-luna"
	defaultPollInterval        = 2 * time.Second
	defaultRequestTimeout      = 3 * time.Minute
	defaultMaxTransportRetries = 5
	// defaultMaxAnswerAttempts (codex xhigh review round 1, High, confirmed)
	// bounds how many times run()'s poll loop will retry a single request
	// before giving up on it and writing a terminal
	// classRetriesExhausted response. Without this, a request stuck on a
	// persistently transient failure (e.g. a rate limit that never
	// clears) left `pending` permanently nonzero, so run()'s own
	// `DONE && pending == 0` exit condition never fired -- the launcher's
	// EXIT trap `wait "$responder_pid"` (run-two-turn.sh) then blocks
	// forever, even after the go test itself has long since timed out on
	// this exact request via its own exchange() deadline. Each attempt
	// can itself take up to defaultRequestTimeout under sustained 429/5xx
	// (the SDK's own WithMaxRetries backoff runs inside that budget), so
	// 10 is a real ceiling (worst case ~tens of minutes), not a token
	// gesture -- long enough to ride out a genuine transient outage, short
	// enough that this responder always eventually exits on its own.
	defaultMaxAnswerAttempts = 10
)

// exchangeRequest/exchangeResponse mirror file_exchange_runtime_test.go's
// own types field-for-field -- this package cannot import an unexported
// _test.go type from another package, so the JSON envelope contract (this
// file's own doc comment) is the shared authority both sides implement
// against, exactly as run-responder-codex.sh already does from bash.
type exchangeRequest struct {
	Operation    string          `json:"operation"`
	Seq          int64           `json:"seq"`
	SessionNonce string          `json:"session_nonce"`
	System       string          `json:"system"`
	Prompt       string          `json:"prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Instructions string          `json:"instructions"`
}

type exchangeResponse struct {
	SessionNonce string          `json:"session_nonce"`
	Output       json.RawMessage `json:"output,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// Closed-vocabulary failure classes (CHAOS-4313 scope: "retries/backoff on
// 429 with a closed-vocabulary failure class surfaced through the harness,
// never by catting logs"). Only these fixed strings ever reach
// exchangeResponse.Error -- never a provider error body or corpus text.
const (
	classRateLimited    = "rate_limited"
	classServerError    = "server_error"
	classAuthError      = "auth_error"
	classInvalidRequest = "invalid_request"
	classNetworkError   = "network_error"
	classInvalidOutput  = "invalid_output"
	classModelRefused   = "model_refused"
	classCanceled       = "context_canceled"
	// classRetriesExhausted (defaultMaxAnswerAttempts's own doc comment):
	// distinct from every other class above so a reader of a report's
	// provenance can tell "gave up after N attempts" apart from "failed
	// once, terminally" at a glance.
	classRetriesExhausted = "retries_exhausted"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: acr-trial-responder-api <exchange-dir> [poll-seconds]")
		os.Exit(1)
	}
	exdir := os.Args[1]
	poll := defaultPollInterval
	if len(os.Args) > 2 {
		secs, err := strconv.Atoi(os.Args[2])
		if err != nil || secs <= 0 {
			fmt.Fprintf(os.Stderr, "acr-trial-responder-api: invalid poll-seconds %q\n", os.Args[2])
			os.Exit(1)
		}
		poll = time.Duration(secs) * time.Second
	}

	// Fail closed, defense in depth (CHAOS-4313 acceptance: "launcher with
	// TRANSPORT=api and no key fails closed with a named error before any
	// request is published"). scripts/trial/run-responder-api.sh already
	// checks this before exec'ing this binary; this repeats the check so
	// the binary is correct standalone too, not correct-with-tribal-
	// knowledge (AGENTS.md's own standing phrase for exactly this class of
	// gap).
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "acr-trial-responder-api: OPENAI_API_KEY is not set in this process's environment -- run-responder-api.sh resolves it from trial_secret OPENAI_API_KEY before exec'ing this binary; refusing to run without it")
		os.Exit(1)
	}
	model := strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_RESPONDER_MODEL"))
	if model == "" {
		model = defaultModel
	}
	// effort (CHAOS-4313 follow-up, chris/team-lead 2026-08-26 10:36 PDT):
	// ACR_TEST_TRIAL_RESPONDER_EFFORT passes through to the OpenAI
	// ChatCompletionNewParams.ReasoningEffort field verbatim (see
	// answerOne). Deliberately has NO default here, unlike model above --
	// leaving it empty means the field is never set on the request at all
	// (shared.ReasoningEffort's zero value, `omitzero`-tagged), so the
	// provider's own default applies, exactly the same as every case-57 run
	// before this knob existed. See resolveResponderEffort's own doc
	// comment for why the value is still validated against a bounded
	// character set despite not being restricted to a fixed enum.
	effort, err := resolveResponderEffort(os.Getenv("ACR_TEST_TRIAL_RESPONDER_EFFORT"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "acr-trial-responder-api: %v\n", err)
		os.Exit(1)
	}

	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(defaultMaxTransportRetries),
		// Shared with internal/contextfabric/embedprovider and
		// modelprovider (the ONLY other packages in this repo that build
		// an OpenAI-compatible transport) -- see that function's own doc
		// comment for why a non-2xx provider response body must never
		// reach an error string this process could log or persist.
		option.WithMiddleware(modelprovider.SanitizeProviderErrorBody),
	}
	if base := strings.TrimSpace(os.Getenv("ACR_TEST_TRIAL_RESPONDER_API_BASE_URL")); base != "" {
		// Test-only hook (same shape as common.sh's ACR_TRIAL_KIAC_DSN_BIN
		// precedent): points this binary at a fake HTTP server so the
		// contract test (main_test.go) never makes a live network call.
		// No production launcher sets this.
		opts = append(opts, option.WithBaseURL(base))
	}
	client := openai.NewClient(opts...)

	r := &responder{client: client, model: model, effort: effort, exchangeDir: exdir, poll: poll, requestTimeout: defaultRequestTimeout, attempts: map[string]int{}, loggedStrictNormalization: map[string]bool{}}
	if err := r.run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "acr-trial-responder-api: %v\n", err)
		os.Exit(1)
	}
}

// validResponderEffort bounds ACR_TEST_TRIAL_RESPONDER_EFFORT's character
// set and length -- see resolveResponderEffort's own doc comment for why.
var validResponderEffort = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// resolveResponderEffort (codex xhigh review round 1, High, confirmed)
// validates ACR_TEST_TRIAL_RESPONDER_EFFORT's raw value, trimmed. Empty
// (unset, or whitespace-only) is always valid and returns "" -- see the
// effort local's own doc comment in main() for why that means "never set
// ReasoningEffort on the request" rather than an error.
//
// A non-empty value must match validResponderEffort above: shared.
// ReasoningEffort's own typed consts only enumerate "low"/"medium"/"high"
// (the real OpenAI API's current vocabulary), but the underlying Go type is
// a plain string, and this harness also targets internal reasoning-tier
// model names (e.g. an "xhigh" tier) not in that enum -- so this
// deliberately does NOT restrict the value to a fixed enum; an
// unrecognized-but-well-formed tier name is the provider's own 400 to
// raise, not this harness's job to pre-guess. What this DOES bound: this
// value is sent to the OpenAI API verbatim (answerOne), printed to this
// process's own stdout (run's own startup log line), and persisted into a
// provenance artifact (trialProvenance.ResponderEffort) -- three sinks that
// must never carry a secret, corpus text, or arbitrary-length garbage, and
// nothing upstream of this function validates the env var's shape before
// it reaches them. A bounded closed character set (letters, digits,
// underscore, hyphen; 1-32 characters) is generous enough for any real
// effort-tier label while catching exactly the failure modes that matter
// here: control characters, whitespace, and anything long enough to be a
// credential or prose fragment rather than a tier label. Fails closed
// (an error, not a silent empty-string fallback) so a malformed value is
// visible at startup rather than silently dropped.
func resolveResponderEffort(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if !validResponderEffort.MatchString(v) {
		return "", fmt.Errorf("ACR_TEST_TRIAL_RESPONDER_EFFORT must be 1-32 characters of [A-Za-z0-9_-] -- refusing to log or send an unbounded/unexpected value verbatim")
	}
	return v, nil
}

type responder struct {
	client openai.Client
	model  string
	// effort is ACR_TEST_TRIAL_RESPONDER_EFFORT, validated by
	// resolveResponderEffort -- "" means never set ReasoningEffort on the
	// request (provider default).
	effort         string
	exchangeDir    string
	poll           time.Duration
	requestTimeout time.Duration
	// attempts counts answerOne calls per request filename that ended in a
	// non-terminal (retry-next-tick) outcome -- see defaultMaxAnswerAttempts's
	// own doc comment. Mutated only from run()'s own single-threaded,
	// sequential for loop -- answerOne is always called synchronously,
	// never from a goroutine, so a plain map needs no locking.
	attempts map[string]int
	// loggedStrictNormalization tracks which req.Operation values have
	// already had their strict-schema rewrite notes logged (see
	// logStrictNormalizationOnce) -- every call for a given operation
	// normalizes the SAME schema to the SAME result, so logging the notes
	// on every single call would just repeat the same lines for the
	// life of the process; once per operation is enough to audit what
	// strict mode cost. Same single-threaded-loop reasoning as attempts
	// above -- no locking needed.
	loggedStrictNormalization map[string]bool
}

// run is the watch loop: identical shape to run-responder-codex.sh's own
// `while true; do ... done` body, and to the trial harness's own
// discoverExchangeRequestFile/exchange() naming for the same concepts, so a
// reader who already knows one responder recognizes the other immediately.
func (r *responder) run(ctx context.Context) error {
	reqDir := filepath.Join(r.exchangeDir, "requests")
	respDir := filepath.Join(r.exchangeDir, "responses")
	logDir := filepath.Join(r.exchangeDir, "_responder_logs")
	for _, d := range []string{reqDir, respDir, logDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	effortLabel := r.effort
	if effortLabel == "" {
		effortLabel = "unset(provider-default)"
	}
	fmt.Printf("responder: watching %s (poll=%s, model=%s, effort=%s, transport=api) -- OpenAI API, never a codex subprocess\n", r.exchangeDir, r.poll, r.model, effortLabel)

	for {
		// os.ReadDir returns entries sorted by name; harmless here (unlike
		// the consumer's own discoverExchangeRequestFile, this loop visits
		// every pending entry, not just the first), kept only for
		// deterministic log ordering across runs.
		entries, err := os.ReadDir(reqDir)
		if err != nil {
			return fmt.Errorf("list %s: %w", reqDir, err)
		}
		pending := 0
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue // writer's in-flight temp file (temp+rename publish) -- not yet published
			}
			reqPath := filepath.Join(reqDir, name)
			respPath := filepath.Join(respDir, name)
			if responseAnswersRequest(reqPath, respPath) {
				continue // already answered, and the response's own session_nonce proves it
			}
			pending++
			r.answerOne(ctx, reqPath, respPath, logDir)
		}
		_, doneErr := os.Stat(filepath.Join(r.exchangeDir, "DONE"))
		if doneErr == nil && pending == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.poll):
		}
	}
	fmt.Println("responder: DONE, every published request answered")
	return nil
}

// responseAnswersRequest (codex xhigh review round 1, Medium, confirmed)
// reports whether respPath is a GENUINE answer to reqPath, not merely
// present. run()'s own pending-detection loop used to treat any existing
// file at respPath as "already answered" via a bare os.Stat -- a stale or
// hand-placed response file left over in a reused exchange directory (a
// contrived scenario every current launcher avoids by always minting a
// fresh timestamped dir, but nothing here enforces that) would then be
// silently accepted as done, even though the consuming exchange()'s own
// session_nonce check would reject it and keep waiting -- pending would
// read 0 here while the consumer is still genuinely stuck. Parses BOTH
// files and requires the response's session_nonce to match the request's
// own; any read/parse failure on either side is NOT a match (fail toward
// re-answering, never toward a false "done" -- answerOne's own torn-read
// tolerance already handles a request file caught mid-publish).
func responseAnswersRequest(reqPath, respPath string) bool {
	respRaw, err := os.ReadFile(respPath)
	if err != nil {
		return false
	}
	var resp exchangeResponse
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return false
	}
	// codex xhigh review round 2 (High, confirmed), narrowed in round 3
	// (Medium, confirmed -- the round-2 fix was too broad): retryOrExhaust's
	// own give-up write can carry an EMPTY SessionNonce specifically when a
	// read/parse failure on reqPath never got far enough to learn it (see
	// that function's own doc comment) -- an empty nonce would otherwise
	// fail the SessionNonce=="" guard below unconditionally, so this
	// function permanently reported "not answered" for a request this
	// responder had already given up on, and run()'s pending count would
	// never reach zero: every following poll tick re-called answerOne,
	// which failed the SAME read again, called retryOrExhaust again
	// (already past defaultMaxAnswerAttempts), rewriting the identical
	// exhausted response every tick, forever. This carve-out is scoped to
	// EXACTLY that empty-nonce case -- an exhausted response WITH a
	// non-empty nonce (the normal case: the request parsed fine, only the
	// model call itself kept failing) still goes through the ordinary
	// nonce-match check below, so a STALE exhausted response left over
	// from a different session (a contrived reused-directory scenario,
	// same as every other staleness case this function guards against)
	// is still correctly rejected rather than accepted regardless of nonce.
	if resp.Error == classRetriesExhausted && resp.SessionNonce == "" {
		return true
	}
	if resp.SessionNonce == "" {
		return false
	}
	reqRaw, err := os.ReadFile(reqPath)
	if err != nil {
		return false
	}
	var req exchangeRequest
	if err := json.Unmarshal(reqRaw, &req); err != nil || req.SessionNonce == "" {
		return false
	}
	return resp.SessionNonce == req.SessionNonce
}

// answerOne answers exactly one request file. On a TERMINAL failure (see
// classifyError) it writes an error response immediately, so the harness
// fails this case fast instead of burning its own exchange timeout. On a
// non-terminal (transient) failure it calls retryOrExhaust: ordinarily that
// writes nothing, and the outer run() loop retries the SAME request file on
// its next poll tick, mirroring run-responder-codex.sh's own
// swallow-and-retry-next-tick precedent (that script's `|| true` on every
// `codex exec` failure) -- but past defaultMaxAnswerAttempts it gives up and
// writes a terminal classRetriesExhausted response instead, so a
// persistently-failing request can never hold run()'s own exit condition
// (`DONE && pending == 0`) open forever (codex xhigh review round 1, High,
// confirmed).
func (r *responder) answerOne(ctx context.Context, reqPath, respPath, logDir string) {
	base := filepath.Base(reqPath)
	logf := requestLogger(logDir, base)

	raw, err := os.ReadFile(reqPath)
	if err != nil {
		logf("read request failed: %v", err)
		r.retryOrExhaust(respPath, base, "", logf)
		return
	}
	var req exchangeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		// Could be a torn read mid-write of the request's own temp+rename
		// publish (file_exchange_runtime_test.go's exchange(), sol review
		// F8) -- treat as transient, same as the consumer's own torn-read
		// tolerance on the response side.
		logf("parse request failed (retrying next tick): %v", err)
		r.retryOrExhaust(respPath, base, "", logf)
		return
	}

	var schemaAny any
	if err := json.Unmarshal(req.OutputSchema, &schemaAny); err != nil {
		logf("operation=%s seq=%d class=%s: request output_schema is not valid JSON: %v", req.Operation, req.Seq, classInvalidRequest, err)
		r.writeResponse(respPath, exchangeResponse{SessionNonce: req.SessionNonce, Error: classInvalidRequest})
		return
	}
	// Post-response validation (below, after the API call) uses THIS
	// ORIGINAL schema, never the strict-normalized one -- normalization is
	// allowed to WEAKEN semantic constraints (strictSchema's own doc
	// comment), so this stays the single authority for "is the answer
	// actually right."
	schemaLoader := gojsonschema.NewGoLoader(schemaAny)
	// strictSchema (team-lead ruling, 2026-08-26, following the case-57
	// kiac acceptance run): non-strict mode measured only 3/13 first-try
	// conformance on this operation's schema, with 2/13 exhausting every
	// retry outright -- "non-strict is unfit," ruled fix is to make
	// OpenAI's OWN grammar-constrained strict mode accept this schema.
	// See strictschema.go's own header for the full transform and why the
	// original schema (above) remains the post-response authority.
	strictSchema, strictNotes := normalizeForStrictSchema(schemaAny)
	r.logStrictNormalizationOnce(req.Operation, strictNotes, logf)

	// req.Instructions carries real behavioral guidance beyond formatting
	// ("base your answer ONLY on system and prompt -- do not invent facts
	// not present in them") -- codex xhigh review round 1 (High, confirmed):
	// an earlier draft decoded this field but never sent it to the model at
	// all, silently dropping that grounding instruction from every live
	// call. Sent as its own system-role message (not concatenated into
	// req.System) so the two stay visibly distinct in the request body --
	// the exact request-body shape a caller (main_test.go) can assert
	// against. The file-writing sentences in this text (written for
	// run-responder-codex.sh's own file-based responder) are harmless noise
	// to a model that has no file-write tool available here; the JSON
	// schema response_format below is this transport's own enforcement of
	// that same output contract.
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(req.System)}
	if req.Instructions != "" {
		messages = append(messages, openai.SystemMessage(req.Instructions))
	}
	messages = append(messages, openai.UserMessage(req.Prompt))

	callCtx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()
	started := time.Now()
	params := openai.ChatCompletionNewParams{
		Model:    r.model,
		Messages: messages,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   req.Operation,
					Schema: strictSchema,
					// Strict:true (team-lead ruling, 2026-08-26): strictSchema
					// is normalizeForStrictSchema's own OpenAI-strict-mode-
					// compatible rewrite of the original schema, not the raw
					// genkitruntime schema itself -- an earlier attempt to set
					// Strict:true against the RAW schema got an immediate 400
					// (confirmed live, reverted; the raw schema's own optional
					// properties are exactly what strict mode rejects). Grammar-
					// constrained decoding now enforces conformance at the
					// source instead of only detecting it after the fact.
					Strict: openai.Bool(true),
				},
			},
		},
	}
	if r.effort != "" {
		// omitzero on ChatCompletionNewParams.ReasoningEffort means an empty
		// r.effort already leaves this field genuinely unset (provider
		// default) without this guard -- written explicitly anyway so a
		// reader doesn't have to know that SDK detail to see the intent.
		params.ReasoningEffort = shared.ReasoningEffort(r.effort)
	}
	completion, callErr := r.client.Chat.Completions.New(callCtx, params)
	elapsed := time.Since(started)

	if callErr != nil {
		class, terminal := classifyError(callErr)
		logf("operation=%s seq=%d elapsed=%s class=%s terminal=%v", req.Operation, req.Seq, elapsed, class, terminal)
		if terminal {
			r.writeResponse(respPath, exchangeResponse{SessionNonce: req.SessionNonce, Error: class})
			return
		}
		r.retryOrExhaust(respPath, base, req.SessionNonce, logf)
		return
	}

	if len(completion.Choices) == 0 {
		logf("operation=%s seq=%d elapsed=%s class=%s: zero choices returned", req.Operation, req.Seq, elapsed, classInvalidOutput)
		r.writeResponse(respPath, exchangeResponse{SessionNonce: req.SessionNonce, Error: classInvalidOutput})
		return
	}
	msg := completion.Choices[0].Message
	if msg.Refusal != "" {
		// msg.Refusal's own text is model-generated, not provider-error
		// text, but it can still echo request content back -- never
		// logged or persisted here, same discipline as the request/prompt
		// bodies themselves.
		logf("operation=%s seq=%d elapsed=%s class=%s", req.Operation, req.Seq, elapsed, classModelRefused)
		r.writeResponse(respPath, exchangeResponse{SessionNonce: req.SessionNonce, Error: classModelRefused})
		return
	}
	content := strings.TrimSpace(msg.Content)
	// codex xhigh review round 1 (High, confirmed) + live acceptance-run
	// finding (CHAOS-4313 case 57, kiac): malformed or schema-non-conformant
	// content here is the MODEL's own sampled output, not a deterministic
	// decode failure -- unlike the SDK/transport-level errors classifyError
	// handles (which fail the exact same way every retry), a fresh sampling
	// attempt against the SAME request can plausibly produce compliant
	// output on a later try. response_format.strict is left unset (this
	// schema's own optional fields -- not every property in "required" --
	// make OpenAI's strict mode reject the request outright with a 400,
	// confirmed live before this comment was written; see the git history
	// on this line for that attempt and its revert), so nothing upstream
	// grammar-constrains the model at all -- both failure modes below are
	// therefore routed through retryOrExhaust (bounded, not immediately
	// terminal) instead of failing this request on the first miss.
	if !json.Valid([]byte(content)) {
		logf("operation=%s seq=%d elapsed=%s class=%s: model content is not valid JSON (%d bytes), retrying", req.Operation, req.Seq, elapsed, classInvalidOutput, len(content))
		r.retryOrExhaust(respPath, base, req.SessionNonce, logf)
		return
	}
	// json.Valid alone only proves the content parses as SOME JSON value,
	// not that it satisfies output_schema -- validated here against the
	// SAME schema the request carried, using gojsonschema (already a repo
	// dependency; internal/contextfabric/genkitruntime's own schema test
	// uses it identically). valErr (the validator call itself failing --
	// a malformed schema on OUR side, never the model's fault) stays
	// terminal; a schema VIOLATION in the model's own content goes through
	// retryOrExhaust like the malformed-JSON case immediately above, same
	// reasoning.
	// strictSchema (its own comment above) forces every property present
	// in every response, with an explicit JSON null standing in for "the
	// ORIGINAL schema treats this as absent" (an originally-optional
	// property, or one a compiled anyOf variant structurally excludes).
	// The ORIGINAL schema was never written expecting that: a property's
	// own value-schema (e.g. time_context.as_of's bare {"type":"string"})
	// accepts a MISSING key just fine but does NOT accept an explicit
	// null in its place -- validating the model's raw content as-is
	// against the original schema would therefore reject the EXACT
	// correct answer for every branch that omits a field, on every single
	// call. Stripping null-valued keys before validation restores the
	// equivalence: "explicitly null" (strict mode's only way to say
	// "omitted") is treated as "actually omitted" (what the original
	// schema was written against), the same convention
	// compileConditionalToAnyOf's own null-forcing relies on.
	validationContent, stripErr := stripNullFieldsJSON([]byte(content))
	if stripErr != nil {
		// Content already passed json.Valid above, so this should be
		// unreachable; fail closed (retry) rather than validate un-stripped
		// content and risk a false rejection.
		logf("operation=%s seq=%d elapsed=%s class=%s: could not strip null fields before validation: %v, retrying", req.Operation, req.Seq, elapsed, classInvalidOutput, stripErr)
		r.retryOrExhaust(respPath, base, req.SessionNonce, logf)
		return
	}
	result, valErr := gojsonschema.Validate(schemaLoader, gojsonschema.NewBytesLoader(validationContent))
	if valErr != nil {
		logf("operation=%s seq=%d elapsed=%s class=%s: schema validation itself failed: %v", req.Operation, req.Seq, elapsed, classInvalidOutput, valErr)
		r.writeResponse(respPath, exchangeResponse{SessionNonce: req.SessionNonce, Error: classInvalidOutput})
		return
	}
	if !result.Valid() {
		logf("operation=%s seq=%d elapsed=%s class=%s: model content does not satisfy output_schema (%d violation(s)), retrying", req.Operation, req.Seq, elapsed, classInvalidOutput, len(result.Errors()))
		r.retryOrExhaust(respPath, base, req.SessionNonce, logf)
		return
	}
	logf("operation=%s seq=%d elapsed=%s status=ok output_bytes=%d", req.Operation, req.Seq, elapsed, len(content))
	r.writeResponse(respPath, exchangeResponse{SessionNonce: req.SessionNonce, Output: json.RawMessage(content)})
}

// retryOrExhaust records one more non-terminal attempt at requestBase and,
// past defaultMaxAnswerAttempts, gives up: writes a terminal
// classRetriesExhausted response so this request stops holding run()'s own
// `pending` count open forever. See defaultMaxAnswerAttempts's own doc
// comment for why this exists. sessionNonce may be empty (a request read/
// parse failure never got far enough to know it) -- the written response
// then simply never satisfies the consumer's own nonce check and that call
// times out on schedule, exactly as if this responder had never run at all;
// never worse than not answering.
func (r *responder) retryOrExhaust(respPath, requestBase, sessionNonce string, logf func(string, ...any)) {
	r.attempts[requestBase]++
	if r.attempts[requestBase] < defaultMaxAnswerAttempts {
		return
	}
	logf("giving up after %d attempts: class=%s", r.attempts[requestBase], classRetriesExhausted)
	r.writeResponse(respPath, exchangeResponse{SessionNonce: sessionNonce, Error: classRetriesExhausted})
}

// logStrictNormalizationOnce logs notes's rewrite list the FIRST time this
// process normalizes operation's schema, and is a no-op on every later call
// for the same operation (see loggedStrictNormalization's own field
// comment). notes describes SCHEMA STRUCTURE only (property names/
// keywords from Go type reflection) -- never request/response content --
// so logging it is exempt from this file's own corpus-privacy discipline.
func (r *responder) logStrictNormalizationOnce(operation string, notes []string, logf func(string, ...any)) {
	if r.loggedStrictNormalization[operation] {
		return
	}
	r.loggedStrictNormalization[operation] = true
	logf("operation=%s strict-schema normalization (%d rewrite(s)):", operation, len(notes))
	for _, note := range notes {
		logf("  %s", note)
	}
}

// requestLogger returns a logf closure that appends timestamped lines to
// <logDir>/<requestBase>.log -- one file per request, exactly
// run-responder-codex.sh's own <LOG_DIR>/$base.log layout, so both
// responders' logs interleave the same way under _responder_logs/ when a
// historical codex run and a live api run are compared side by side.
// Content is metadata ONLY (status/class/timing/byte counts) -- never
// prompt or output text (this file's own header hygiene note).
func requestLogger(logDir, requestBase string) func(format string, args ...any) {
	logPath := filepath.Join(logDir, requestBase+".log")
	return func(format string, args ...any) {
		line := fmt.Sprintf("[%s] "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString(line)
	}
}

// writeResponse publishes resp via temp+rename in the SAME directory as
// respPath, mirroring the request writer's own publication discipline
// (file_exchange_runtime_test.go's exchange(), sol review F8) so the
// consuming go test's poll loop can never observe a partially-written
// response file. Best-effort: a failure here leaves the request unanswered,
// which surfaces as deadline_exceeded on the consumer side rather than a
// silently wrong response -- never worse than not answering at all.
func (r *responder) writeResponse(respPath string, resp exchangeResponse) {
	body, err := json.Marshal(resp)
	if err != nil {
		return
	}
	dir := filepath.Dir(respPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(respPath)+".tmp*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, respPath); err != nil {
		_ = os.Remove(tmpPath)
	}
}

// classifyError maps a Chat Completions call failure to a closed-vocabulary
// class (this file's own class* constants -- never the provider's raw
// text) and whether it is TERMINAL. TERMINAL failures (auth, malformed
// request, a canceled run) are written back immediately so the harness
// fails this case fast; everything else is left for run()'s own poll loop
// to retry, on the theory that a moment's wait may resolve it -- the SAME
// judgment run-responder-codex.sh's unconditional `|| true` already makes
// for every codex failure, just made explicit and per-class here.
//
// Mirrors embedprovider.IsTransientEmbedError's own *openai.Error
// status-code classification (internal/contextfabric/embedprovider/
// transient.go) rather than reimplementing it independently -- not reused
// directly because that function's name and doc comment are scoped to the
// embeddings call site specifically.
func classifyError(err error) (class string, terminal bool) {
	if errors.Is(err, context.Canceled) {
		return classCanceled, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// This call's own per-request timeout (r.requestTimeout), not the
		// exchange's overall deadline -- worth a fresh attempt.
		return classNetworkError, false
	}
	// codex xhigh review round 1 (High, confirmed): a 2xx response the SDK
	// cannot JSON-decode is NOT an *openai.Error (a response WAS received
	// here, unlike the transport-layer case at the bottom of this
	// function) -- it fell through to the generic "worth retrying" default
	// before this check existed, so a server that keeps returning
	// unparseable JSON for a given request shape would retry it FOREVER
	// (bounded only by defaultMaxAnswerAttempts, never converging).
	// embedprovider.IsTransientEmbedError classifies the identical shape
	// as PERSISTENT for exactly this reason -- mirrored here via the same
	// standard-library sentinel/type checks (*json.SyntaxError,
	// *json.UnmarshalTypeError, io.EOF for a fully empty 2xx body,
	// io.ErrUnexpectedEOF for a truncated one).
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return classInvalidOutput, true
	}
	// *openai.Error is checked BEFORE the apijson substring check below
	// (codex round-2 fix caught its own regression, confirmed live): that
	// type's own Error() method (internal/apierror) formats
	// `r.Request.Method`/`r.Response.StatusCode` unconditionally --
	// calling .Error() on one that is not fully populated from a real HTTP
	// round trip panics on a nil *http.Request. The SDK always populates
	// both fields for an error it constructs itself, so this never panics
	// on a genuine API error in production; ordering the type check first
	// means the substring check below (which DOES call err.Error()) is
	// only ever reached for an error that is provably NOT an *openai.Error
	// in the first place.
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusTooManyRequests:
			return classRateLimited, false
		case apiErr.StatusCode >= 500 && apiErr.StatusCode < 600:
			return classServerError, false
		// codex xhigh review round 1 (High, confirmed): 408 (Request
		// Timeout) is retry-shaped -- the server itself gave up waiting on
		// this ONE request, which says nothing about whether an identical
		// retry would fail the same way -- but the switch's own default
		// branch below previously classified it (like every other
		// unlisted status) as a terminal invalid_request.
		case apiErr.StatusCode == http.StatusRequestTimeout:
			return classNetworkError, false
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden:
			return classAuthError, true
		default:
			return classInvalidRequest, true
		}
	}
	// codex xhigh review round 2 (High, confirmed): the openai-go SDK's OWN
	// response-body decoder (internal/apijson) does not use the standard
	// library's *json.SyntaxError/*json.UnmarshalTypeError for a
	// well-formed-JSON-but-wrong-SHAPE 2xx body -- it returns a bare
	// fmt.Errorf("apijson: could not deserialize to ...") with no wrapped
	// type at all (confirmed by reading internal/apijson/decoder.go), so
	// the checks above never match it and it fell through to the generic
	// "worth retrying" default -- the SAME persistent-failure class those
	// checks exist to catch, from a different decoder. errors.As/errors.Is
	// cannot key on this (nothing to unwrap or type-assert); a substring
	// match on the SDK's own fixed "apijson:" prefix is the only signal
	// this error shape carries. strings.Contains, not HasPrefix, survives
	// any future %w wrapping added between this call and the SDK's
	// decoder (a wrapped error's own message text is always still a
	// substring of the wrapper's .Error()). Safe to call .Error() here
	// unconditionally: the *openai.Error case above already returned, so
	// err is provably some other, ordinary error type.
	if strings.Contains(err.Error(), "apijson:") {
		return classInvalidOutput, true
	}
	// No *openai.Error, no context error, no JSON decode error: a
	// transport-layer failure that never reached the server at all
	// (connection refused/reset, DNS, TLS handshake) -- the same
	// "network blip, default to worth retrying" judgment embedprovider's
	// own classifier makes for the identical
	// shape.
	return classNetworkError, false
}
