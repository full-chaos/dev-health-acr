package config

import "testing"

// acr-api and acr-mcp's edge gate must resolve the same failure limits from
// the same environment: the edge gate reads them through
// AuthGateLimitsFromEnvironment, acr-api through load. CHAOS-7201 r2: a second
// hardcoded default at the edge diverged from the ACR_REQUESTS_PER_MINUTE
// fallback.
func TestAuthGateLimitsAgreeWithWhatAcrAPILoads(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"nothing set":                    {},
		"only requests per minute":       {"ACR_REQUESTS_PER_MINUTE": "5"},
		"explicit failures win":          {"ACR_REQUESTS_PER_MINUTE": "5", "ACR_AUTH_FAILURES_PER_WINDOW": "9"},
		"requests per minute above 20":   {"ACR_REQUESTS_PER_MINUTE": "300"},
		"auth window beats shared":       {"ACR_LIMIT_WINDOW": "2m", "ACR_AUTH_LIMIT_WINDOW": "30s"},
		"shared window fallback":         {"ACR_LIMIT_WINDOW": "2m"},
		"tracked keys and in flight set": {"ACR_AUTH_MAX_TRACKED_KEYS": "77", "ACR_AUTH_MAX_IN_FLIGHT": "5"},
	} {
		t.Run(name, func(t *testing.T) {
			env["ACR_LOCAL_COMPOSITION_READY"] = "true"
			cfg, err := load(mapLookup(env))
			if err != nil {
				t.Fatal(err)
			}
			got, err := AuthGateLimitsFromEnvironment(mapLookup(env))
			if err != nil {
				t.Fatal(err)
			}
			want := AuthGateLimits{
				FailureLimit: cfg.RequestControls.AuthFailures, Window: cfg.RequestControls.Auth.Window,
				MaxTrackedKeys: cfg.RequestControls.AuthTrackedKeys, MaxInFlight: cfg.RequestControls.AuthMaxInFlight,
			}
			if got != want {
				t.Fatalf("edge gate limits %+v, acr-api limits %+v", got, want)
			}
		})
	}
	// The shared default itself: 20 failures, one minute.
	got, err := AuthGateLimitsFromEnvironment(mapLookup(nil))
	if err != nil || got.FailureLimit != 20 || got.Window.String() != "1m0s" {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	if got, _ := AuthGateLimitsFromEnvironment(mapLookup(map[string]string{"ACR_REQUESTS_PER_MINUTE": "5"})); got.FailureLimit != 5 {
		t.Fatalf("ACR_REQUESTS_PER_MINUTE=5 gave FailureLimit %d, want 5", got.FailureLimit)
	}
}

func TestAuthGateLimitsRefuseBadValues(t *testing.T) {
	for name, value := range map[string]string{
		"ACR_AUTH_FAILURES_PER_WINDOW": "0", "ACR_AUTH_MAX_TRACKED_KEYS": "-1", "ACR_AUTH_MAX_IN_FLIGHT": "abc",
		"ACR_AUTH_LIMIT_WINDOW": "0s", "ACR_LIMIT_WINDOW": "-5s", "ACR_REQUESTS_PER_MINUTE": "x",
	} {
		if _, err := AuthGateLimitsFromEnvironment(mapLookup(map[string]string{name: value})); err == nil {
			t.Errorf("%s=%q accepted", name, value)
		}
	}
}
