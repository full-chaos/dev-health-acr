package entitlements_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func TestClientNew_clones_config_urls_before_bearer_request(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`)
	config := clientConfig(t, server, entitlements.Config{})
	client, err := entitlements.New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	config.BaseURL.Host = "attacker.invalid"
	config.BaseURL.Scheme = "http"

	// When
	entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if err != nil || !entitled {
		t.Fatalf("HasEntitlement() = %t, %v; want original trusted destination", entitled, err)
	}
}

func TestClientCheck_fails_closed_for_non_health_contract(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`)
	client := newClient(t, server, entitlements.Config{})

	// When
	err := client.Check(context.Background())

	// Then
	if err == nil {
		t.Fatal("Check() error = nil; want fail-closed contract rejection")
	}
}
