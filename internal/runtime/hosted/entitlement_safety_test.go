package hosted

import (
	"strings"
	"testing"
)

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
