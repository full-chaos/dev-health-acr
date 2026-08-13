package modelprovider

import (
	"bytes"
	"context"
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
// Ordering note: like the tracing probe above, Genkit's metrics
// instruments are created via a package-global sync.OnceValue
// (internal/metrics/metrics.go's fetchInstruments) on the FIRST ever
// action completion in the process, permanently binding to whichever
// MeterProvider was active at that moment -- run in isolation:
//
//	go test ./internal/contextfabric/modelprovider -run '^TestNew_neverExportsErrorContentToGenkitMetrics$' -v
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
