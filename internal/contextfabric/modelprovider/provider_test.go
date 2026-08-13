package modelprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The tests in this file drive the REAL Genkit OpenAI-compatible plugin and
// the real OpenAI SDK transport against a local HTTP server that replays
// recorded provider responses. That is deliberate: the point of CHAOS-3770's
// taxonomy requirement is to prove that genkitruntime.classifyModelError maps
// what an actual provider returns on the wire -- an HTTP status and an
// OpenAI-shaped error body -- onto the ACR sentinels, which a fake
// generator substituted above the plugin cannot show.

type providerServer struct {
	baseURL string

	mu            sync.Mutex
	callCount     int
	authorization string
	lastPath      string
}

func (p *providerServer) record(request *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.callCount++
	p.authorization = request.Header.Get("Authorization")
	p.lastPath = request.URL.Path
}

func (p *providerServer) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.callCount
}

func (p *providerServer) lastAuthorization() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authorization
}

func recordingProvider(t *testing.T, handler http.HandlerFunc) *providerServer {
	t.Helper()
	provider := &providerServer{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		provider.record(request)
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	provider.baseURL = server.URL + "/v1/"
	return provider
}

// chatCompletion replays a 200 chat-completion whose assistant message
// carries content verbatim.
func chatCompletion(t *testing.T, content string) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"created": 1760000000,
			"model": "gpt-5-nano",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": %q}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 41, "completion_tokens": 17, "total_tokens": 58}
		}`, content)
	}
}

// providerError replays a recorded provider failure: the status plus the
// OpenAI-shaped error envelope every OpenAI-compatible server returns.
func providerError(status int, code, message string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		fmt.Fprintf(writer, `{"error": {"message": %q, "type": "invalid_request_error", "param": null, "code": %q}}`, message, code)
	}
}

const validInterpretationJSON = `{
	"shape": "open",
	"requested_judgment": "actual_status_and_current_drivers",
	"subject_terms": ["Ask Dev"],
	"time_context": {"axis": "current"},
	"fact_requirements": [{"kind": "status"}, {"kind": "blockers"}],
	"clarification_needed": false
}`

func testConfig(provider *providerServer) Config {
	return Config{
		Provider: DefaultProvider, BaseURL: provider.baseURL, Model: DefaultModel,
		APIKey: "sk-configured", Timeout: 10 * time.Second, MaxAttempts: 1,
		MaxTransportRetries: 0, AllowInsecureBaseURL: true,
	}
}

func testRequest() contextfabric.InvestigationRequest {
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

// callProvider builds the real runtime for cfg and runs one interpretation
// against it, returning the classified error (nil on success).
func callProvider(t *testing.T, cfg Config) error {
	t.Helper()
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New() = %v, want a constructed runtime", err)
	}
	if runtime == nil {
		t.Fatal("New() returned a nil runtime without an error")
	}
	_, _, callErr := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())
	return callErr
}

func TestNew_interpretsThroughTheOpenAICompatiblePlugin(t *testing.T) {
	// Given a provider that answers with a well-formed interpretation.
	provider := recordingProvider(t, chatCompletion(t, validInterpretationJSON))

	// When
	runtime, err := New(context.Background(), testConfig(provider))
	if err != nil {
		t.Fatal(err)
	}
	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())

	// Then
	if err != nil {
		t.Fatalf("InterpretQuestion() = %v, want a successful interpretation", err)
	}
	if interpreted.Shape != contextfabric.ShapeOpen || len(interpreted.FactRequirements) != 2 {
		t.Fatalf("interpreted = %#v, want the provider's own shape and fact requirements", interpreted)
	}
	if receipt.Outcome != "success" || receipt.Attempts != 1 {
		t.Fatalf("receipt outcome/attempts = %q/%d, want success/1", receipt.Outcome, receipt.Attempts)
	}
	// The receipt is the replay record, so it must carry the plain provider
	// and model ids -- not the namespaced Genkit action name used to resolve
	// the model.
	if receipt.Provider != DefaultProvider || receipt.Model != DefaultModel {
		t.Fatalf("receipt provider/model = %q/%q, want %q/%q", receipt.Provider, receipt.Model, DefaultProvider, DefaultModel)
	}
	if receipt.Usage.TotalTokens != 58 {
		t.Fatalf("receipt usage = %#v, want the provider's reported token usage", receipt.Usage)
	}
	if calls := provider.calls(); calls != 1 {
		t.Fatalf("provider calls = %d, want exactly 1", calls)
	}
}

func TestNew_resolvesAnyModelIDIncludingOnesTheSDKDoesNotEnumerate(t *testing.T) {
	// Given a credential-free BYO endpoint serving a model id no OpenAI
	// plugin model table contains -- the property BYO LLM depends on -- and
	// an ambient OpenAI credential in the process environment.
	t.Setenv("OPENAI_API_KEY", "sk-ambient-must-not-reach-a-byo-endpoint")
	provider := recordingProvider(t, chatCompletion(t, validInterpretationJSON))
	cfg := testConfig(provider)
	cfg.Provider, cfg.Model = "local-llama", "meta-llama/Llama-3.1-8B-Instruct"
	cfg.APIKey = ""

	// When
	if err := callProvider(t, cfg); err != nil {
		t.Fatalf("InterpretQuestion() = %v, want an unenumerated model id to resolve dynamically", err)
	}

	// Then
	if provider.calls() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1", provider.calls())
	}
	// A credential-free endpoint must receive no Authorization header at
	// all -- neither the ambient credential nor an empty bearer.
	if got := provider.lastAuthorization(); got != "" {
		t.Fatalf("authorization = %q, want no header for a credential-free BYO endpoint", got)
	}
}

func TestNew_classifiesRecordedProviderFailures(t *testing.T) {
	// Given the provider failures CHAOS-3770 calls out, replayed with the
	// status and body a real OpenAI-compatible server returns.
	cases := map[string]struct {
		handler http.HandlerFunc
		want    error
	}{
		"401 invalid credential": {
			handler: providerError(http.StatusUnauthorized, "invalid_api_key", "Incorrect API key provided: sk-conf***ured."),
			want:    contextfabric.ErrModelUnavailable,
		},
		"403 credential lacks model access": {
			handler: providerError(http.StatusForbidden, "model_not_found", "Project does not have access to model gpt-5-nano."),
			want:    contextfabric.ErrModelUnavailable,
		},
		"429 rate limited": {
			handler: providerError(http.StatusTooManyRequests, "rate_limit_exceeded", "Rate limit reached for gpt-5-nano."),
			want:    contextfabric.ErrModelRateLimited,
		},
		"429 quota exhausted": {
			handler: providerError(http.StatusTooManyRequests, "insufficient_quota", "You exceeded your current quota."),
			want:    contextfabric.ErrModelRateLimited,
		},
		"500 provider fault": {
			handler: providerError(http.StatusInternalServerError, "server_error", "The server had an error processing your request."),
			want:    contextfabric.ErrModelUnavailable,
		},
		"503 provider overloaded": {
			handler: providerError(http.StatusServiceUnavailable, "engine_overloaded", "The engine is currently overloaded."),
			want:    contextfabric.ErrModelUnavailable,
		},
		"output violates the response schema": {
			handler: chatCompletion(t, `{"shape": "an_invented_shape", "requested_judgment": "x", "time_context": {"axis": "current"}, "fact_requirements": [], "clarification_needed": false}`),
			want:    contextfabric.ErrModelOutput,
		},
		"output is not JSON at all": {
			handler: chatCompletion(t, "I'm sorry, I can't help with that."),
			want:    contextfabric.ErrModelOutput,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			provider := recordingProvider(t, testCase.handler)

			// When
			err := callProvider(t, testConfig(provider))

			// Then
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestNew_neverForwardsProviderResponseContentInTheClassifiedError(t *testing.T) {
	// Given a provider error whose body quotes request content -- the kind
	// of body a 400 from a strict gateway carries.
	const secret = "release readiness for the Ask Dev launch"
	provider := recordingProvider(t, providerError(http.StatusBadRequest, "invalid_prompt", "Rejected prompt: "+secret))

	// When
	err := callProvider(t, testConfig(provider))

	// Then the classified error carries only a class and a fixed message;
	// the provider's body must not travel into logs, receipts, or telemetry
	// built from it.
	if err == nil {
		t.Fatal("err = nil, want a classified failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), provider.baseURL) {
		t.Fatalf("classified error leaked provider response or endpoint detail: %q", err)
	}
}

func TestNew_fallsBackToTheStrongerModelWhenThePrimaryFails(t *testing.T) {
	// Given a provider that fails the first model and answers the second --
	// the gpt-5-nano primary / stronger fallback shape from CHAOS-3770.
	var mu sync.Mutex
	seen := []string{}
	provider := recordingProvider(t, func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		first := len(seen) == 0
		seen = append(seen, string(body))
		mu.Unlock()
		if first {
			providerError(http.StatusServiceUnavailable, "engine_overloaded", "overloaded")(writer, request)
			return
		}
		chatCompletion(t, validInterpretationJSON)(writer, request)
	})
	cfg := testConfig(provider)
	cfg.FallbackModel = "gpt-5.6-luna"

	// When
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())

	// Then
	if err != nil {
		t.Fatalf("InterpretQuestion() = %v, want the fallback model to answer", err)
	}
	if !receipt.FallbackUsed || receipt.Outcome != "fallback" {
		t.Fatalf("receipt = %#v, want a recorded fallback", receipt)
	}
	// The receipt keeps the PRIMARY model's identity: the fallback is an
	// attribute of the primary's execution, and receipt.FallbackUsed is what
	// tells replay that a second model produced the output.
	if receipt.Model != DefaultModel {
		t.Fatalf("receipt model = %q, want the primary %q", receipt.Model, DefaultModel)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("provider calls = %d, want the primary then the fallback", len(seen))
	}
	if !strings.Contains(seen[1], "gpt-5.6-luna") {
		t.Fatalf("second call did not target the fallback model: %s", seen[1])
	}
}

func TestNew_rejectsAnInvalidConfigurationWithoutContactingAProvider(t *testing.T) {
	// Given
	provider := recordingProvider(t, chatCompletion(t, validInterpretationJSON))
	cfg := testConfig(provider)
	cfg.AllowInsecureBaseURL = false

	// When
	runtime, err := New(context.Background(), cfg)

	// Then
	if err == nil {
		t.Fatal("New() = nil error for a plaintext base URL without the insecure opt-in")
	}
	if runtime != nil {
		t.Fatal("New() returned a runtime alongside an error")
	}
	if provider.calls() != 0 {
		t.Fatal("a rejected configuration still reached the provider")
	}
}

// TestNew_classifiesStatusCorrectlyDespiteEphemeralPortsContainingStatusCodeDigits
// is a follow-up to CHAOS-3770/#88. A rebased PR's CI hit a real,
// intermittent (roughly 1-in-15 to 1-in-80 under
// `go test -race -shuffle=on -count=1`, repeated) flake:
// TestNew_classifiesRecordedProviderFailures's 401/403 cases occasionally
// classified as rate-limited instead of unavailable. The flake WAS the
// bug, not test contamination or a race (confirmed: -race never reported
// anything, and the middleware always saw the correct status).
//
// Root cause: classifyModelError's fallback path did
// strings.Contains(lower, "429") against the WHOLE unstructured error
// text. The OpenAI SDK's apierror.Error.Error() embeds the full request
// URL verbatim, so whenever an httptest.Server happened to land on an
// ephemeral port whose digits contain "429" (e.g. 55429, or 4290), ANY
// unrelated status on that request -- a 401, a 403, a 500 -- was
// misclassified as rate-limited. In production the same hazard exists
// wherever a BYO endpoint's own hostname, port, or path happens to
// contain those digits. classifyModelError now anchors status extraction
// to the fixed, ACR-controlled sanitized-error token
// ("provider response redacted by ACR (status <code> <text>)") that
// sanitizeProviderErrorBody guarantees for every non-2xx response, which
// no incidental URL component can produce or collide with.
//
// This test recreates the churn pattern that originally surfaced the bug
// (rapid create/close of httptest.Server instances, each on whatever
// ephemeral port the OS just freed) as an ongoing regression signal for
// the whole transport-to-classification path, one that does not depend
// on `-shuffle=on` rolling the right port to have a chance of catching a
// reintroduction. Each iteration constructs a FRESH runtime against a
// FRESH server, explicitly closing the previous iteration's server first
// (recordingProvider's t.Cleanup-scoped close would otherwise defer every
// close to the end of this whole test function, defeating the churn this
// test exists to stress) so the OS is free to hand back the same
// ephemeral port immediately, and alternates success/failure shapes per
// iteration so a response misattributed to the wrong iteration is caught
// either way.
func TestNew_classifiesStatusCorrectlyDespiteEphemeralPortsContainingStatusCodeDigits(t *testing.T) {
	const iterations = 60
	for i := 0; i < iterations; i++ {
		var handler http.HandlerFunc
		wantUnavailable := i%2 == 1
		if wantUnavailable {
			handler = providerError(http.StatusUnauthorized, "invalid_api_key", fmt.Sprintf("iteration %d credential rejected", i))
		} else {
			handler = chatCompletion(t, validInterpretationJSON)
		}

		server := httptest.NewServer(handler)
		cfg := testConfig(&providerServer{baseURL: server.URL + "/v1/"})

		runtime, err := New(context.Background(), cfg)
		if err != nil {
			server.Close()
			t.Fatalf("iteration %d: New() error = %v", i, err)
		}
		_, _, callErr := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())
		server.Close()

		if wantUnavailable {
			if !errors.Is(callErr, contextfabric.ErrModelUnavailable) {
				t.Fatalf("iteration %d: InterpretQuestion() error = %v, want ErrModelUnavailable -- a response may have been misattributed from a different iteration's server", i, callErr)
			}
		} else if callErr != nil {
			t.Fatalf("iteration %d: InterpretQuestion() error = %v, want success -- a response may have been misattributed from a different iteration's server", i, callErr)
		}
	}
}
