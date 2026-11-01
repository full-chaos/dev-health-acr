package config

import (
	"strings"
	"testing"
)

func credentialEnv(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	values := completeRuntimeEnvironment()
	for k, v := range extra {
		if v == "-" {
			delete(values, k)
			continue
		}
		values[k] = v
	}
	return values
}

var workloadClientEnv = map[string]string{
	"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "https://auth-service.example.test",
	"ACR_AUTH_WORKLOAD_ISSUER":       "https://auth.example.test",
	"ACR_AUTH_WORKLOAD_SUBJECT_FILE": "/var/run/secrets/dev-health/auth-workload/token",
}

func withWorkloadClient(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range workloadClientEnv {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestEntitlementCredentialDefaultsToTheServiceToken(t *testing.T) {
	cfg, err := load(mapLookup(credentialEnv(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EntitlementCredential(); got != EntitlementCredentialServiceToken {
		t.Fatalf("credential = %q, want %q", got, EntitlementCredentialServiceToken)
	}
}

func TestEntitlementCredentialSelectsWorkloadAndKeepsAMountedTokenFile(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"token file still set (rollback mount)": withWorkloadClient(map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload"}),
		"token file unset":                      withWorkloadClient(map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload", "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "-"}),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := load(mapLookup(credentialEnv(t, extra)))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.EntitlementCredential() != EntitlementCredentialWorkload || cfg.EntitlementMode() != EntitlementModeRemote {
				t.Fatalf("credential = %q, mode = %q", cfg.EntitlementCredential(), cfg.EntitlementMode())
			}
		})
	}
}

func TestEntitlementCredentialRefusals(t *testing.T) {
	tests := map[string]struct {
		extra map[string]string
		want  string
	}{
		"unknown value": {
			extra: map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "svc_acr"},
			want:  "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL",
		},
		"workload without the auth workload client": {
			extra: map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload"},
			want:  "ACR_AUTH_WORKLOAD_",
		},
		"workload without the entitlement URL": {
			extra: withWorkloadClient(map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload", "ACR_DEV_HEALTH_ENTITLEMENT_URL": "-", "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "-"}),
			want:  "ACR_DEV_HEALTH_ENTITLEMENT_URL",
		},
		"service_token with a URL and no token file": {
			extra: map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "service_token", "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "-"},
			want:  "must be configured together",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := load(mapLookup(credentialEnv(t, test.extra)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load() error = %v, want it to name %q", err, test.want)
			}
			if strings.Contains(err.Error(), "svc_acr") {
				t.Fatalf("the error echoes the configured value: %v", err)
			}
		})
	}
}
