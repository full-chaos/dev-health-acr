package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func TestClientHasEntitlement_reads_platform_model_fallback_from_the_same_cached_response(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		fallback bool
	}{
		{name: "granted", body: `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true,"platform_model_fallback":true}`, fallback: true},
		{name: "not granted", body: `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true,"platform_model_fallback":false}`},
		{name: "absent from an older ops build", body: `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fetches atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fetches.Add(1)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			client := newClient(t, server, entitlements.Config{})

			fallback, fallbackErr := client.HasEntitlement(context.Background(), "org-1", "platform_model_fallback")
			serviceModel, serviceErr := client.HasEntitlement(context.Background(), "org-1", "acr_service_model")
			runtime, runtimeErr := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

			if fallbackErr != nil || serviceErr != nil || runtimeErr != nil || fallback != tc.fallback || !serviceModel || !runtime {
				t.Fatalf("platform_model_fallback = %t, %v; acr_service_model = %t, %v; agent_context_runtime = %t, %v; want %t, true, true", fallback, fallbackErr, serviceModel, serviceErr, runtime, runtimeErr, tc.fallback)
			}
			if got := fetches.Load(); got != 1 {
				t.Fatalf("entitlement fetches = %d, want 1 for all three answers", got)
			}
		})
	}
}

func TestClientHasEntitlement_fails_closed_for_a_malformed_platform_model_fallback_field(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true,"platform_model_fallback":null}`,
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true,"platform_model_fallback":"true"}`,
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true,"platform_model_fallback":true,"platform_model_fallback":true}`,
	} {
		server := newTLSServer(t, http.StatusOK, body)
		client := newClient(t, server, entitlements.Config{})
		for _, entitlement := range []string{"platform_model_fallback", "acr_service_model", "agent_context_runtime"} {
			if entitled, err := client.HasEntitlement(context.Background(), "org-1", entitlement); err == nil || entitled {
				t.Fatalf("HasEntitlement(%s) on %s = %t, %v; want false and an error", entitlement, body, entitled, err)
			}
		}
	}
}

// ops marks the 404 of a saved configuration behind a closed byo_llm gate with
// a reason header beside X-Org-Llm-Config: none. That answer is never "no
// BYO" (the platform model would answer) and never "unusable" (the turn would
// be admitted on a model that cannot run); it is its own error, cached like
// any other ops answer.
func TestOrgLLMConfigGateClosedIsItsOwnErrorAndCached(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Org-Llm-Config", "none")
		w.Header().Set("X-Org-Llm-Config-Reason", "byo_llm_gate_closed")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found"}`))
	}))
	t.Cleanup(server.Close)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
		if found || !errors.Is(err, entitlements.ErrLLMConfigGateClosed) || errors.Is(err, entitlements.ErrLLMConfigUnusable) {
			t.Fatalf("read %d: found = %v err = %v, want a gate-closed error that is not unusable", i, found, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ops calls = %d, want 1 (the gate-closed answer is cached)", got)
	}
}

func TestOrgLLMConfigPlainNoConfigStaysNoBYO(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Org-Llm-Config", "none")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found"}`))
	}))
	t.Cleanup(server.Close)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.OrgLLMConfig(context.Background(), llmOrg); found || err != nil {
		t.Fatalf("found = %v err = %v, want no BYO and no error", found, err)
	}
}

// ops marks the 404 for an org whose saved BYO keys are for other purposes
// only (none resolves for investigations). It is neither "no BYO" nor
// "unusable", and it is cached like any other ops answer.
func TestOrgLLMConfigNoInvestigateConfigIsItsOwnErrorAndCached(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Org-Llm-Config", "none")
		w.Header().Set("X-Org-Llm-Config-Reason", "byo_no_investigate_config")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found"}`))
	}))
	t.Cleanup(server.Close)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
		if found || !errors.Is(err, entitlements.ErrLLMConfigNoInvestigateConfig) || errors.Is(err, entitlements.ErrLLMConfigUnusable) || errors.Is(err, entitlements.ErrLLMConfigGateClosed) {
			t.Fatalf("read %d: found = %v err = %v, want its own error, not unusable and not gate closed", i, found, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ops calls = %d, want 1 (the answer is cached)", got)
	}
}

// A 404 is "no BYO" only when ops marks it so. A bare 404 (a stale ops build, a
// proxy that drops the header) or one with a reason this build does not know is
// an unknown answer: it fails closed and is never read as no configuration.
func TestOrgLLMConfigUnmarkedOrUnknownNotFoundFailsClosed(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"bare 404":                       {},
		"reason without the marker":      {"X-Org-Llm-Config-Reason": "byo_llm_gate_closed"},
		"marker with an unknown reason":  {"X-Org-Llm-Config": "none", "X-Org-Llm-Config-Reason": "some_future_reason"},
		"a marker value other than none": {"X-Org-Llm-Config": "saved"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"Not found"}`))
			}))
			t.Cleanup(server.Close)
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := client.OrgLLMConfig(context.Background(), llmOrg); found || err == nil {
				t.Fatalf("found = %v err = %v, want an error and no config", found, err)
			}
		})
	}
}
