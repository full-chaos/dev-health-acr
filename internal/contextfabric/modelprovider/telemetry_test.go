package modelprovider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
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
