package entitlements_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// CHAOS-12730: a provider outage is never cached. The first lookup during the
// outage fails closed; the next lookup after recovery succeeds with no restart.
func TestClientHasEntitlement_recoversAfterProviderOutageWithoutRestart(t *testing.T) {
	var down atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`))
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server, entitlements.Config{})

	down.Store(true)
	if entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime"); err == nil || entitled {
		t.Fatalf("during the outage HasEntitlement() = %t, %v; want false and an error", entitled, err)
	}
	down.Store(false)
	if entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime"); err != nil || !entitled {
		t.Fatalf("after recovery HasEntitlement() = %t, %v; want true, nil", entitled, err)
	}
}
