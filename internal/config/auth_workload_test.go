package config

import (
	"strings"
	"testing"
)

func authWorkloadEnv(extra map[string]string) lookupEnv {
	env := map[string]string{
		"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "https://auth-service.example.test",
		"ACR_AUTH_WORKLOAD_ISSUER":       "https://auth.example.test",
		"ACR_AUTH_WORKLOAD_SUBJECT_FILE": "/var/run/secrets/dev-health/auth-workload/token",
	}
	for k, v := range extra {
		if v == "-" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return mapLookup(env)
}

func TestAuthWorkloadUnsetIsOff(t *testing.T) {
	got, err := LoadAuthWorkload(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got != (AuthWorkloadConfig{}) || got.Enabled() {
		t.Fatalf("unset must be the zero, disabled config: %+v", got)
	}
	cfg, err := load(dataQueryEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthWorkload.Enabled() {
		t.Fatal("load: unset env must leave the auth workload client off")
	}
}

func TestAuthWorkloadParsesEveryEnvName(t *testing.T) {
	got, err := LoadAuthWorkload(authWorkloadEnv(map[string]string{"ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP": "true", "ACR_AUTH_WORKLOAD_EXCHANGE_URL": " http://auth-service.dev-health.svc:8080 "}))
	if err != nil {
		t.Fatal(err)
	}
	want := AuthWorkloadConfig{
		ExchangeURL:    "http://auth-service.dev-health.svc:8080",
		AllowPlainHTTP: true,
		Issuer:         "https://auth.example.test",
		SubjectFile:    "/var/run/secrets/dev-health/auth-workload/token",
	}
	if got != want || !got.Enabled() {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	boot, err := LoadAuthWorkload(authWorkloadEnv(map[string]string{"ACR_AUTH_WORKLOAD_SUBJECT_FILE": "-", "ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE": "/run/secrets/acr_workload_bootstrap"}))
	if err != nil {
		t.Fatal(err)
	}
	if boot.BootstrapFile != "/run/secrets/acr_workload_bootstrap" || boot.SubjectFile != "" {
		t.Fatalf("bootstrap file not read: %+v", boot)
	}
}

func TestAuthWorkloadLoadCarriesTheConfig(t *testing.T) {
	env := map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true"}
	for _, k := range []string{"ACR_AUTH_WORKLOAD_EXCHANGE_URL", "ACR_AUTH_WORKLOAD_ISSUER", "ACR_AUTH_WORKLOAD_SUBJECT_FILE"} {
		v, _ := authWorkloadEnv(nil)(k)
		env[k] = v
	}
	cfg, err := load(mapLookup(env))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AuthWorkload.Enabled() || cfg.AuthWorkload.Issuer != "https://auth.example.test" {
		t.Fatalf("load did not carry the auth workload config: %+v", cfg.AuthWorkload)
	}
	delete(env, "ACR_AUTH_WORKLOAD_ISSUER")
	if _, err := load(mapLookup(env)); err == nil || !strings.Contains(err.Error(), "ACR_AUTH_WORKLOAD_ISSUER") {
		t.Fatalf("load must refuse a partial auth workload config naming the variable: %v", err)
	}
}

func TestAuthWorkloadRefusesIncompleteOrContradictoryInput(t *testing.T) {
	cases := map[string]struct {
		extra map[string]string
		want  string
	}{
		"no exchange url":            {map[string]string{"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "-"}, "ACR_AUTH_WORKLOAD_EXCHANGE_URL"},
		"blank exchange url":         {map[string]string{"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "  "}, "ACR_AUTH_WORKLOAD_EXCHANGE_URL"},
		"no issuer":                  {map[string]string{"ACR_AUTH_WORKLOAD_ISSUER": "-"}, "ACR_AUTH_WORKLOAD_ISSUER"},
		"blank issuer":               {map[string]string{"ACR_AUTH_WORKLOAD_ISSUER": " "}, "ACR_AUTH_WORKLOAD_ISSUER"},
		"no subject":                 {map[string]string{"ACR_AUTH_WORKLOAD_SUBJECT_FILE": "-"}, "ACR_AUTH_WORKLOAD_SUBJECT_FILE"},
		"both subjects":              {map[string]string{"ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE": "/run/secrets/b"}, "ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE"},
		"bad plain http switch":      {map[string]string{"ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP": "sometimes"}, "ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP"},
		"only the plain http switch": {map[string]string{"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "-", "ACR_AUTH_WORKLOAD_ISSUER": "-", "ACR_AUTH_WORKLOAD_SUBJECT_FILE": "-", "ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP": "true"}, "ACR_AUTH_WORKLOAD_EXCHANGE_URL"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadAuthWorkload(authWorkloadEnv(tc.extra))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "example.test") || strings.Contains(err.Error(), "/var/run/secrets") {
				t.Fatalf("the error must not echo a configured value: %v", err)
			}
		})
	}
}

func TestAuthWorkloadValidateRefusesAPartialConfig(t *testing.T) {
	cfg := Config{AuthWorkload: AuthWorkloadConfig{ExchangeURL: "https://auth-service.example.test"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ACR_AUTH_WORKLOAD_ISSUER") {
		t.Fatalf("Validate must refuse a partial auth workload config: %v", err)
	}
}
