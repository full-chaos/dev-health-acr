package hosted

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

// CHAOS-12461 spend guard. ACR_CONTEXT_FABRIC_MODEL_BASE_URL alone does not keep a question
// off an organization's own model: the per-organization source (ops, the default) answers
// first. Pointing acr-api at the scripted model oracle is only safe when the source is off
// and the service model is open. These tests run the real wiring (buildOrgModelConfigResolver,
// wrapWithOrgModelRuntimeResolver, newContextFabricModelRuntime) with an organization that
// HAS its own model configuration and a credential, loopback only, no real key anywhere.

const (
	orgBYOBaseURL = "https://byo-model.example.invalid/v1/"
	orgBYOKey     = "sk-org-own-key-not-real"
)

func oracleStandIn(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"context-fabric-oracle","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func oracleEnv(baseURL string, extra map[string]string) func(string) (string, bool) {
	values := map[string]string{
		"ACR_CONTEXT_FABRIC_MODEL_PROVIDER":                "oracle",
		"ACR_CONTEXT_FABRIC_MODEL":                         "context-fabric-oracle",
		"ACR_CONTEXT_FABRIC_MODEL_BASE_URL":                baseURL,
		"ACR_CONTEXT_FABRIC_MODEL_ALLOW_INSECURE_BASE_URL": "true",
		"ACR_CONTEXT_FABRIC_MODEL_API_KEY":                 "dummy-not-a-key",
		"ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS":            "1",
	}
	for k, v := range extra {
		values[k] = v
	}
	return envLookup(values)
}

func orgWithOwnModel() (entitlementWithLLMConfig, *fakeOrgLLMSource) {
	source := &fakeOrgLLMSource{found: true, config: entitlements.OrgLLMConfig{
		Provider: "openai", Model: "gpt-org-own", BaseURL: orgBYOBaseURL, APIKey: llm.NewSecret(orgBYOKey), Version: "v-org-1",
	}}
	return entitlementWithLLMConfig{fakeOrgLLMSource: source}, source
}

// With the source off and the service model open, an organization that has its own model
// configuration still resolves to the oracle: its configuration is never read.
func TestOrgWithItsOwnModelResolvesToTheOracleWhenTheSourceIsOffAndTheServiceModelIsOpen(t *testing.T) {
	oracle, hits := oracleStandIn(t)
	lookup := oracleEnv(oracle.URL+"/v1/", map[string]string{EnvOrgLLMConfigSource: "off"})
	entitlement, source := orgWithOwnModel()

	deployment, err := newContextFabricModelRuntime(context.Background(), lookup, nil, discardLogger())
	if err != nil || deployment == nil {
		t.Fatalf("deployment runtime = %v, %v", deployment, err)
	}
	orgConfigs, err := buildOrgModelConfigResolver(entitlement, lookup, discardLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	if orgConfigs != nil {
		t.Fatalf("source=off built a per-organization source: %T", orgConfigs)
	}
	wired, resolver, err := wrapWithOrgModelRuntimeResolver(deployment, orgConfigs, lookup, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if resolver != nil || wired != deployment {
		t.Fatalf("the wired runtime is not the deployment runtime: %T", wired)
	}
	// A real call, as an investigation makes it: it reaches the oracle stand-in.
	_, _, _ = wired.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_with_own_model"}, contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678", Question: "q?",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20},
		Consumer:    contextfabric.ConsumerInfo{Name: "t", Version: "v1", Surface: "t"},
	})
	if hits.Load() == 0 {
		t.Fatal("the call did not reach the oracle")
	}
	if source.calls != 0 {
		t.Fatalf("the organization's own model configuration was read %d times", source.calls)
	}
}

// RED CONTROL. Without the two settings the organization's own configuration wins over the
// environment base URL: this is the hazard the docs name. The test asserts the hazard is
// real, so a change that quietly made the environment win (or the source default off) shows.
func TestWithoutTheSettingsAnOrgWithItsOwnModelBeatsTheEnvironmentBaseURL(t *testing.T) {
	oracle, hits := oracleStandIn(t)
	for name, tc := range map[string]struct {
		extra map[string]string
		open  bool
	}{
		"source unset, service model closed": {nil, false},
		"source unset, service model open":   {nil, true},
		"source ops, service model open":     {map[string]string{EnvOrgLLMConfigSource: "ops"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			lookup := oracleEnv(oracle.URL+"/v1/", tc.extra)
			entitlement, source := orgWithOwnModel()
			orgConfigs, err := buildOrgModelConfigResolver(entitlement, lookup, discardLogger(), tc.open)
			if err != nil || orgConfigs == nil {
				t.Fatalf("per-organization source = %v, %v, want the ops source", orgConfigs, err)
			}
			resolved, ok, err := orgConfigs.ResolveOrgModelConfig(context.Background(), "org_with_own_model")
			if err != nil || !ok {
				t.Fatalf("resolve = %v, %v", ok, err)
			}
			if resolved.BaseURL != orgBYOBaseURL || resolved.Credential.Reveal() != orgBYOKey {
				t.Fatalf("resolved = %q, want the organization's own base URL and credential", resolved.BaseURL)
			}
			if strings.HasPrefix(resolved.BaseURL, oracle.URL) {
				t.Fatal("the organization resolved to the oracle without the settings")
			}
			if source.calls == 0 || hits.Load() != 0 {
				t.Fatalf("source calls = %d oracle hits = %d", source.calls, hits.Load())
			}
		})
	}
}

// source=off alone, with the service model closed, is refused at start: no silent half-setting.
func TestSourceOffWithoutTheServiceModelOpenRefusesToStart(t *testing.T) {
	entitlement, _ := orgWithOwnModel()
	lookup := oracleEnv("http://127.0.0.1:1/v1/", map[string]string{EnvOrgLLMConfigSource: "off"})
	if got, err := buildOrgModelConfigResolver(entitlement, lookup, discardLogger(), false); err == nil || got != nil {
		t.Fatalf("source=off with the service model closed = %v, %v, want a start-up error", got, err)
	}
}
