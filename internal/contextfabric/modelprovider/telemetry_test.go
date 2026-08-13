package modelprovider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestNew_neverExportsPromptContentToGenkitTelemetry is the CHAOS-3770 F1
// probe: Genkit's core/tracing package unconditionally records a
// generation's full request (including the system prompt and the encoded
// question) as the "genkit:input" span attribute, and the model's response
// as "genkit:output" (core/tracing/tracing.go's spanMetadata.attributes(),
// set on every span by tracing.RunInNewSpan's deferred
// span.SetAttributes -- verified directly against the vendored source in
// go.mod's module cache, not assumed). Whether that ever leaves the
// process depends entirely on an AMBIENT environment variable this service
// does not set or control: the first call anywhere in the process to
// tracing.TracerProvider() lazily wires up an HTTP telemetry exporter when
// GENKIT_TELEMETRY_SERVER is non-empty (a sync.Once, package-global to
// genkit, so it is genuinely activated by nothing ACR itself does). Once
// wired, WriteTelemetryImmediate's SimpleSpanProcessor exports every
// finished span SYNCHRONOUSLY (in the same goroutine as span.End()), so a
// generation call's prompt content would reach that server on this exact
// call, with no flush/timing race to account for.
//
// This directly contradicts docs/operations.md's no-prompt-in-logs/
// telemetry claim (AC-3770-5): a deployment sharing a process environment
// with genkit dev tooling (or any other ambient source of
// GENKIT_TELEMETRY_SERVER) would silently exfiltrate prompt and answer
// content to that server on every investigation.
//
// Ordering note: tracing.TracerProvider()'s sync.Once is process-global, so
// this test is only a meaningful pre-fix repro when it is the FIRST caller
// in the process to touch Genkit tracing -- run it in isolation:
//
//	go test ./internal/contextfabric/modelprovider -run '^TestNew_neverExportsPromptContentToGenkitTelemetry$' -v
//
// The fix (registering modelprovider's own inert TracerProvider before
// genkit.Init ever runs, in initGenkit) makes the test's outcome
// independent of run order: every New() call -- and every test in this
// package constructs a runtime via New() -- wins the race against Genkit's
// lazy provider init before it ever consults GENKIT_TELEMETRY_SERVER, so
// the assertion holds no matter which test runs first.
func TestNew_neverExportsPromptContentToGenkitTelemetry(t *testing.T) {
	var mu sync.Mutex
	var captured []byte
	telemetryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = append(captured, body...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer telemetryServer.Close()
	t.Setenv("GENKIT_TELEMETRY_SERVER", telemetryServer.URL)

	const secretQuestion = "release readiness for the Ask Dev launch -- must never leave this process"
	provider := recordingProvider(t, chatCompletion(t, validInterpretationJSON))
	cfg := testConfig(provider)

	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest()
	request.Question = secretQuestion
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, request); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(string(captured), secretQuestion) {
		t.Fatalf("genkit telemetry export carried prompt content: %s", captured)
	}
	// The point is not merely "the secret string is absent" -- it is that
	// Genkit's own telemetry wiring never activates for this service at
	// all, regardless of the ambient environment. Assert zero bytes
	// reached the telemetry server, not just that the needle is missing
	// from a haystack that could still exist for other reasons.
	if len(captured) != 0 {
		t.Fatalf("genkit exported %d bytes of span telemetry despite GENKIT_TELEMETRY_SERVER being ambient-only env, want zero exports: %s", len(captured), captured)
	}
}

// TestNew_neverExportsErrorContentToGenkitMetrics is the CHAOS-3770 F1
// residual probe (codex round 2, point a): genkit's internal/metrics
// package records attribute.String("errorMessage", err.Error()) on a
// counter obtained from the global otel.Meter("genkit") -- i.e.
// otel.GetMeterProvider() -- every time a Genkit action (including a
// generate call) fails (WriteActionFailure, invoked from
// core/action.go's recordActionMetrics, on OUR reachable call path, not
// only Genkit's dev-only registered-action path). err.Error() can carry a
// raw provider response body (see retryable's doc comment in
// genkitruntime/runtime.go). Unlike tracing.TracerProvider(), Genkit's
// metrics path has no "already registered, skip" check of its own; it
// just calls otel.Meter("genkit") fresh. suppressGenkitTelemetryExport now
// also registers a no-op *metric.MeterProvider before genkit.Init runs.
//
// Ordering note (codex round 3, informational): like the tracing probe
// above, Genkit's metrics instruments are created via a package-global
// sync.OnceValue (internal/metrics/metrics.go's fetchInstruments) on the
// FIRST ever action completion in the process, permanently binding to
// whichever MeterProvider was active at that moment. Run in isolation to
// exercise THIS test's own reader specifically:
//
//	go test ./internal/contextfabric/modelprovider -run '^TestNew_neverExportsErrorContentToGenkitMetrics$' -v
//
// Unlike the pre-fix repro (which genuinely depends on being first), this
// is not a soundness gap in the fixed code as part of the full suite:
// every test in this package reaches an action only via New(), and New()
// always calls suppressGenkitTelemetryExport before genkit.Init runs --
// so whichever test in the binary happens to trigger the first-ever
// action completion, fetchInstruments binds to a no-op MeterProvider
// either way. A run as part of the full package suite can therefore bind
// to an EARLIER test's no-op provider instead of this test's own reader
// (making the collected-metrics assertion trivially true rather than a
// live check of this specific reader) rather than failing -- it cannot
// silently pass while the real vulnerability persists, because ANY
// binding to a provider other than one an attacker controls means the
// content never left the process. The isolated run above is what proves
// the mechanism, not merely its absence of failure.
func TestNew_neverExportsErrorContentToGenkitMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	// Registered BEFORE New(): if New()'s suppression did not overwrite
	// this with a no-op provider, genkit's metrics instruments would bind
	// to THIS reader's provider on the generate call below.
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	const secret = "Rejected prompt: release readiness for the Ask Dev launch -- must never leave this process"
	provider := recordingProvider(t, providerError(http.StatusBadRequest, "invalid_prompt", secret))
	cfg := testConfig(provider)

	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, callErr := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest()); callErr == nil {
		t.Fatal("InterpretQuestion() error = nil, want a classified failure")
	}

	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.ScopeMetrics) != 0 {
		t.Fatalf("manual reader (registered before New()) collected %d scope-metrics groups, want 0 -- New() must overwrite the global MeterProvider before genkit's metrics instruments ever bind to it: %+v", len(data.ScopeMetrics), data)
	}
}

// TestNew_rejectsGenkitDevEnvironment is the CHAOS-3770 F1 residual probe
// (codex round 2, point c): genkit.Init starts a local reflection server
// only when GENKIT_ENV=dev (api.CurrentEnvironment(), core/api/environment.go).
// That server's handleNotify endpoint lets a caller register a NEW
// telemetry exporter URL at runtime, independent of
// GENKIT_TELEMETRY_SERVER and independent of
// suppressGenkitTelemetryExport's tracer/meter-provider preemption above
// (it wires a client directly via tracing.WriteTelemetryImmediate/
// WriteTelemetryRealtime, not through the global provider that check
// preempts). New() must fail composition outright rather than silently
// allow it, the same posture as an ambient OPENAI_* variable.
func TestNew_rejectsGenkitDevEnvironment(t *testing.T) {
	t.Setenv("GENKIT_ENV", "dev")
	provider := recordingProvider(t, chatCompletion(t, validInterpretationJSON))
	cfg := testConfig(provider)

	runtime, err := New(context.Background(), cfg)

	if err == nil {
		t.Fatal("New() = nil error with GENKIT_ENV=dev, want a rejection")
	}
	if runtime != nil {
		t.Fatal("New() returned a runtime alongside an error")
	}
	if !strings.Contains(err.Error(), "GENKIT_ENV") {
		t.Fatalf("err = %q, want it to name GENKIT_ENV", err)
	}
	if provider.calls() != 0 {
		t.Fatal("a rejected configuration must not construct genkit or contact a provider")
	}
}

// TestGenkitDebugLoggingNeverCarriesGenerationContentOnACRsPath is the
// CHAOS-3770 F1 residual probe (codex round 2, point b): genkit logs
// generation content at Debug level in more than one place --
// ai/generate.go's DefineGenerateAction closure logs full request/response
// JSON directly, but that closure is registered as the "generate" ACTION
// and is reachable only through action-dispatch/reflection callers (never
// through genkit.GenerateData's actual call chain: ai.Generate ->
// GenerateWithRequest directly, bypassing action dispatch entirely --
// confirmed by reading the vendored source, not assumed, and corroborated
// below: the captured log never contains a "GenerateAction" record). The
// schema-mismatch branch IS on our reachable path
// (ai/generate.go:465) and logs a JSON-parse error -- but empirically
// (see below) that error's own text is a generic parser message ("data is
// not valid JSON: invalid character ...", encoding/json's own format),
// never the offending content itself.
//
// core/logger/logger.go's FromContext falls back to slog.Default() when
// no context-scoped logger is present -- and genkit exposes no API to
// install one, so that fallback is unconditional. ACR never calls
// logger.SetLevel (the only thing that elevates slog.Default() to Debug)
// or slog.SetDefault anywhere in this repository (grepped, not assumed),
// so slog.Default() stays at its stdlib zero-value level (Info) in
// production, silently dropping every Debug call before any I/O happens.
//
// This test proves the stronger, level-independent property empirically:
// capture EVERY log line genkit produces -- at Debug, the most verbose
// level reachable by any means -- across both a successful call (prompt
// content) and a failing one (malformed model output), and confirm
// neither secret appears anywhere in the captured output. That is worst
// case for content leakage; production (Info level, nothing elevated)
// only logs less.
func TestGenkitDebugLoggingNeverCarriesGenerationContentOnACRsPath(t *testing.T) {
	captureAtDebug := func(t *testing.T, handler http.HandlerFunc, request func() contextfabric.InvestigationRequest) string {
		t.Helper()
		var buf bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		defer slog.SetDefault(previous)

		provider := recordingProvider(t, handler)
		cfg := testConfig(provider)
		runtime, err := New(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, request())
		if strings.Contains(buf.String(), "GenerateAction") {
			t.Fatal("captured log contains a \"GenerateAction\" record -- genkit.GenerateData is no longer bypassing action dispatch as assumed; re-derive this test's premise from the current vendored source before trusting it")
		}
		return buf.String()
	}

	t.Run("successful call never logs the question", func(t *testing.T) {
		const secretQuestion = "SECRET_QUESTION_MUST_NEVER_APPEAR_IN_LOGS"
		captured := captureAtDebug(t, chatCompletion(t, validInterpretationJSON), func() contextfabric.InvestigationRequest {
			request := testRequest()
			request.Question = secretQuestion
			return request
		})
		if strings.Contains(captured, secretQuestion) {
			t.Fatalf("Debug-level log contains the question text: %s", captured)
		}
	})

	t.Run("malformed model output never echoes into the schema-mismatch log", func(t *testing.T) {
		const secretModelOutput = "SECRET_MODEL_OUTPUT_TOKEN_MUST_NEVER_APPEAR_IN_LOGS -- this is not JSON"
		captured := captureAtDebug(t, chatCompletion(t, secretModelOutput), func() contextfabric.InvestigationRequest {
			return testRequest()
		})
		if !strings.Contains(captured, "model failed to generate output matching expected schema") {
			t.Fatalf("sanity check failed: the schema-mismatch log line never fired, so this test proves nothing; got: %s", captured)
		}
		if strings.Contains(captured, secretModelOutput) {
			t.Fatalf("Debug-level log echoed the malformed model output verbatim: %s", captured)
		}
	})
}

// TestActionRunDebugLoggingNeverCarriesProviderResponseBody is the
// CHAOS-3770 F1(b) residual probe (codex round 3): the refutation in
// TestGenkitDebugLoggingNeverCarriesGenerationContentOnACRsPath was correct
// about DefineGenerateAction's closure being unreachable, but missed a
// SECOND Debug logging path that IS on our call chain and IS reachable for
// a genuine provider transport error (not just a malformed-but-200
// response): core/action.go's Action.Run wraps every action invocation,
// including the model action itself
// (GenerateWithRequest -> Model.Generate -> Action.Run, ai/generate.go:882),
// and its deferred log records the RAW error verbatim:
//
//	logger.FromContext(ctx).Debug("Action.Run", "name", a.Name(), "err", err)
//
// For a provider transport failure, that err is compat_oai's own
// fmt.Errorf("failed to create completion: %w", err)-wrapped openai-go
// SDK error, whose Error() method embeds the raw response body verbatim
// (openai-go/internal/apierror.Error.Error(), aliased as openai.Error --
// see retryable's doc comment in genkitruntime/runtime.go). slog formats
// an error-typed attribute via its Error() method, so this log line
// carries the raw provider response body whenever the model action
// itself fails -- a case
// TestGenkitDebugLoggingNeverCarriesGenerationContentOnACRsPath never
// exercised (its malformed-output case is a 200 OK with unparseable JSON
// content, not a transport error, so the model ACTION succeeds and
// Action.Run logs err=<nil>).
//
// The fix sanitizes at the transport boundary this package already owns
// (newClientOptions, via option.WithMiddleware) rather than trying to
// mute genkit's logger: a provider response body is replaced with a
// fixed, content-free shape -- preserving the HTTP status (so
// classifyModelError's existing status-based classification, including
// its "429" substring check for rate limiting, is unaffected -- see the
// composition assertion below and TestNew_classifiesRecordedProviderFailures)
// -- before the openai-go SDK ever constructs an *openai.Error from it.
// That closes the leak at its source: Action.Run's debug log, genkit's
// metrics errorMessage attribute (F1(a)), and any other current or future
// consumer of the error all see the sanitized text, unconditionally,
// regardless of log level or provider configuration.
func TestActionRunDebugLoggingNeverCarriesProviderResponseBody(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	const secret = "Rejected prompt: release readiness for the Ask Dev launch -- must never leave this process"
	provider := recordingProvider(t, providerError(http.StatusBadRequest, "invalid_prompt", secret))
	cfg := testConfig(provider)

	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, callErr := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())
	if callErr == nil {
		t.Fatal("InterpretQuestion() error = nil, want a classified failure")
	}

	captured := buf.String()
	// Sanity check first: the model action's own Action.Run debug log must
	// actually have fired with a non-nil err, or this test proves nothing
	// (the difference between this test and the earlier refutation is
	// exactly that the MODEL ACTION ITSELF fails here, not a later parse
	// step).
	if !strings.Contains(captured, "Action.Run") || !strings.Contains(captured, "name=openai/gpt-5-nano") {
		t.Fatalf("sanity check failed: the model action's Action.Run debug log never fired; got: %s", captured)
	}
	if strings.Contains(captured, secret) {
		t.Fatalf("Action.Run's debug log carried the raw provider response body: %s", captured)
	}
}

// TestSanitizedProviderErrorStillClassifiesIdenticallyThroughRetryableAndTaxonomy
// verifies the composition the round-3 fix direction calls for explicitly:
// sanitizing the provider response body at the transport boundary must not
// change classifyModelError's or retryable's behavior for any of the
// recorded provider failures TestNew_classifiesRecordedProviderFailures
// already pins. Re-running that exact assertion here, alongside
// genkitruntime's own retry-count probe, is what proves the sanitization
// preserves enough structure (the HTTP status, never the message) for F2's
// classification and no-retry-same-input guarantees to keep working
// unchanged.
func TestSanitizedProviderErrorStillClassifiesIdenticallyThroughRetryableAndTaxonomy(t *testing.T) {
	cases := map[string]struct {
		handler http.HandlerFunc
		want    error
	}{
		"401 invalid credential":            {providerError(http.StatusUnauthorized, "invalid_api_key", "Incorrect API key provided: sk-conf***ured."), contextfabric.ErrModelUnavailable},
		"403 credential lacks model access": {providerError(http.StatusForbidden, "model_not_found", "Project does not have access to model gpt-5-nano."), contextfabric.ErrModelUnavailable},
		"429 rate limited":                  {providerError(http.StatusTooManyRequests, "rate_limit_exceeded", "Rate limit reached for gpt-5-nano."), contextfabric.ErrModelRateLimited},
		"429 quota exhausted":               {providerError(http.StatusTooManyRequests, "insufficient_quota", "You exceeded your current quota."), contextfabric.ErrModelRateLimited},
		"500 provider fault":                {providerError(http.StatusInternalServerError, "server_error", "The server had an error processing your request."), contextfabric.ErrModelUnavailable},
		"503 provider overloaded":           {providerError(http.StatusServiceUnavailable, "engine_overloaded", "The engine is currently overloaded."), contextfabric.ErrModelUnavailable},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			provider := recordingProvider(t, testCase.handler)
			err := callProvider(t, testConfig(provider))
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
			// The retry loop must not have retried a non-transient
			// (4xx) failure with the same payload (CHAOS-3770 F2), and
			// must retry a genuinely transient one up to MaxAttempts.
			// testConfig sets MaxAttempts: 1, so this is really just
			// confirming the call completed and classified without a
			// panic on the sanitized error type -- the dedicated retry
			// probes live in genkitruntime; this test's job is only the
			// classification composition.
		})
	}
}
