package config

import (
	"reflect"
	"strings"
	"testing"
)

// CHAOS-12086: the CORS allow list loads exact serialized origins and refuses
// every other shape, so the middleware's byte-for-byte match can only ever
// echo an origin an operator wrote on purpose.
func TestCORSAllowedOriginsLoadOnlyExactOrigins(t *testing.T) {
	values := completeRuntimeEnvironment()
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORSAllowedOrigins) != 0 {
		t.Fatalf("default allow list = %v, want empty (CORS answered for nobody)", cfg.CORSAllowedOrigins)
	}
	values[CORSAllowedOriginsEnv] = "https://www.fullchaos.dev, http://localhost:3000"
	cfg, err = load(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://www.fullchaos.dev", "http://localhost:3000"}; !reflect.DeepEqual(cfg.CORSAllowedOrigins, want) {
		t.Fatalf("allow list = %v, want %v", cfg.CORSAllowedOrigins, want)
	}
	for _, bad := range []string{
		"*",
		"https://*.fullchaos.dev",
		"https://www.fullchaos.dev/",
		"https://www.fullchaos.dev/ask-dev",
		"https://www.fullchaos.dev?x=1",
		"https://www.fullchaos.dev#x",
		"https://user@www.fullchaos.dev",
		"https://WWW.fullchaos.dev",
		"HTTPS://www.fullchaos.dev",
		"ftp://www.fullchaos.dev",
		"www.fullchaos.dev",
		"null",
	} {
		values[CORSAllowedOriginsEnv] = bad
		if _, err := load(mapLookup(values)); err == nil || !strings.Contains(err.Error(), CORSAllowedOriginsEnv) {
			t.Errorf("%q: load() error = %v, want an %s refusal", bad, err, CORSAllowedOriginsEnv)
		}
	}
}

// Production accepts https origins only; development keeps http for local
// stacks (kiac serves the web app on http://localhost).
func TestCORSAllowedOriginsAreHTTPSInProduction(t *testing.T) {
	if err := validCORSOrigin("production", "http://localhost"); err == nil {
		t.Fatal("production accepted an http origin")
	}
	if err := validCORSOrigin("production", "https://www.fullchaos.dev"); err != nil {
		t.Fatalf("production refused an https origin: %v", err)
	}
	for _, environment := range []string{"development", "test", "staging"} {
		if err := validCORSOrigin(environment, "http://localhost"); err != nil {
			t.Errorf("%s refused http://localhost: %v", environment, err)
		}
	}
}
