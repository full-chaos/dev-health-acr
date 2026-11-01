package config

import (
	"strings"
	"testing"
)

func TestAuthProjectionUnsetIsOffAndNeedsNothing(t *testing.T) {
	cfg, err := load(dataQueryEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthProjection.Enabled() || cfg.AuthProjection != (AuthProjectionConfig{}) {
		t.Fatalf("unset must be the zero, disabled config: %+v", cfg.AuthProjection)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthProjectionURLNeedsTheWorkloadClient(t *testing.T) {
	_, err := load(dataQueryEnv(map[string]string{"ACR_AUTH_PROJECTION_URL": "https://secret-host.example.test"}))
	if err == nil || !strings.Contains(err.Error(), "ACR_AUTH_PROJECTION_URL") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-host") {
		t.Fatalf("the error echoes the value: %v", err)
	}
}

func TestAuthProjectionURLIsValidatedLikeTheExchangeURL(t *testing.T) {
	workload := map[string]string{
		"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "https://auth-service.example.test",
		"ACR_AUTH_WORKLOAD_ISSUER":       "https://auth.example.test",
		"ACR_AUTH_WORKLOAD_SUBJECT_FILE": "/var/run/secrets/dev-health/auth-workload/token",
	}
	with := func(url string, extra map[string]string) map[string]string {
		env := map[string]string{"ACR_AUTH_PROJECTION_URL": url}
		for k, v := range workload {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	good := []map[string]string{
		with("https://auth-service.example.test", nil),
		with("https://auth-service.example.test:8443/", nil),
		with("http://auth-service.internal:8080", map[string]string{"ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP": "true"}),
	}
	for _, env := range good {
		cfg, err := load(dataQueryEnv(env))
		if err != nil || !cfg.AuthProjection.Enabled() {
			t.Errorf("%v: %v", env["ACR_AUTH_PROJECTION_URL"], err)
		}
	}
	bad := []map[string]string{
		with("http://auth-service.internal:8080", nil),
		with("https://auth-service.example.test/internal/v1/acr/credential-events", nil),
		with("https://auth-service.example.test?x=1", nil),
		with("auth-service", nil),
		with("ftp://auth-service.example.test", nil),
		with("https://user:pw@auth-service.example.test", nil),
	}
	for _, env := range bad {
		if _, err := load(dataQueryEnv(env)); err == nil {
			t.Errorf("%q was accepted", env["ACR_AUTH_PROJECTION_URL"])
		} else if strings.Contains(err.Error(), "pw@") {
			t.Errorf("the error echoes the value: %v", err)
		}
	}
	if _, err := load(dataQueryEnv(with("  ", nil))); err != nil {
		t.Errorf("a blank value is unset: %v", err)
	}
}
