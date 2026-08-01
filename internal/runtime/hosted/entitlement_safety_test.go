package hosted

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

func TestNewEntitlement_local_provider_is_allow_all_and_offline(t *testing.T) {
	cfg := config.Config{Environment: "test"}

	provider, err := newEntitlement(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(localEntitlement); !ok {
		t.Fatalf("provider type = %T, want localEntitlement", provider)
	}
	if err := provider.Check(context.Background()); err != nil {
		t.Fatalf("local readiness = %v", err)
	}
	enabled, err := provider.HasEntitlement(context.Background(), "any-org", "agent_context_runtime")
	if err != nil || !enabled {
		t.Fatalf("local entitlement = %t, %v; want true, nil", enabled, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("local close = %v", err)
	}
	for _, request := range []struct{ orgID, entitlement string }{
		{orgID: "", entitlement: "agent_context_runtime"},
		{orgID: "any-org", entitlement: "different_entitlement"},
	} {
		enabled, err := provider.HasEntitlement(context.Background(), request.orgID, request.entitlement)
		if err == nil || enabled {
			t.Fatalf("local provider accepted unsupported request org=%q entitlement=%q", request.orgID, request.entitlement)
		}
	}
}

func TestNewEntitlement_does_not_expose_malformed_URL_values(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*string, *string)
	}{
		{name: "origin", configure: func(origin, _ *string) { *origin = "://sentinel-secret-origin" }},
		{name: "proxy", configure: func(_, proxy *string) { *proxy = "://sentinel-secret-proxy" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			cfg := testRuntimeConfig(t)
			origin := cfg.DevHealthEntitlementURL
			proxy := cfg.DevHealthEntitlementProxyURL
			test.configure(&origin, &proxy)
			cfg.DevHealthEntitlementURL = origin
			cfg.DevHealthEntitlementProxyURL = proxy

			// When
			_, err := newEntitlement(cfg)

			// Then
			if err == nil {
				t.Fatal("malformed entitlement URL was accepted")
			}
			if strings.Contains(err.Error(), "sentinel-secret") {
				t.Fatalf("entitlement error leaked configured URL: %v", err)
			}
		})
	}
}
