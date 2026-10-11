package entitlements_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func TestClientHasEntitlement_reads_service_model_from_the_same_cached_response(t *testing.T) {
	for _, tc := range []struct {
		name         string
		body         string
		runtime      bool
		serviceModel bool
	}{
		{name: "granted", body: `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true}`, runtime: true, serviceModel: true},
		{name: "not granted", body: `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":false}`, runtime: true},
		{name: "absent", body: `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`, runtime: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fetches atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fetches.Add(1)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			client := newClient(t, server, entitlements.Config{})

			serviceModel, serviceErr := client.HasEntitlement(context.Background(), "org-1", "acr_service_model")
			runtime, runtimeErr := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

			if serviceErr != nil || runtimeErr != nil || serviceModel != tc.serviceModel || runtime != tc.runtime {
				t.Fatalf("acr_service_model = %t, %v; agent_context_runtime = %t, %v; want %t, %t", serviceModel, serviceErr, runtime, runtimeErr, tc.serviceModel, tc.runtime)
			}
			if got := fetches.Load(); got != 1 {
				t.Fatalf("entitlement fetches = %d, want 1 for both answers", got)
			}
		})
	}
}

func TestClientHasEntitlement_fails_closed_for_a_malformed_service_model_field(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":null}`,
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":"true"}`,
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true,"acr_service_model":true}`,
	} {
		server := newTLSServer(t, http.StatusOK, body)
		client := newClient(t, server, entitlements.Config{})
		for _, entitlement := range []string{"acr_service_model", "agent_context_runtime"} {
			if entitled, err := client.HasEntitlement(context.Background(), "org-1", entitlement); err == nil || entitled {
				t.Fatalf("HasEntitlement(%s) on %s = %t, %v; want false and an error", entitlement, body, entitled, err)
			}
		}
	}
}

func TestClientHasEntitlement_rejects_an_unknown_entitlement_name(t *testing.T) {
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"acr_service_model":true}`)
	client := newClient(t, server, entitlements.Config{})
	if entitled, err := client.HasEntitlement(context.Background(), "org-1", "byo_llm"); err == nil || entitled {
		t.Fatalf("HasEntitlement(byo_llm) = %t, %v; want false and an error", entitled, err)
	}
}
