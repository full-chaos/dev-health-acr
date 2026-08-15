package embedcache

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestConfigFromEnvDefaultsDisabled(t *testing.T) {
	cfg, err := ConfigFromEnv(lookupFrom(nil))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("cache must default OFF when unset")
	}
	if cfg.MaxEntries != DefaultMaxEntries {
		t.Fatalf("MaxEntries = %d, want default %d", cfg.MaxEntries, DefaultMaxEntries)
	}
}

func TestConfigFromEnvHonorsEnabledAndSize(t *testing.T) {
	cfg, err := ConfigFromEnv(lookupFrom(map[string]string{
		EnvEnabled:    "true",
		EnvMaxEntries: "10",
	}))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("cache must be enabled when the env var is true")
	}
	if cfg.MaxEntries != 10 {
		t.Fatalf("MaxEntries = %d, want 10", cfg.MaxEntries)
	}
}

func TestConfigFromEnvRejectsOutOfBoundsSize(t *testing.T) {
	if _, err := ConfigFromEnv(lookupFrom(map[string]string{
		EnvEnabled:    "true",
		EnvMaxEntries: "0",
	})); err == nil {
		t.Fatal("expected an error for a zero cache size")
	}
	if _, err := ConfigFromEnv(lookupFrom(map[string]string{
		EnvEnabled:    "true",
		EnvMaxEntries: "not-a-number",
	})); err == nil {
		t.Fatal("expected an error for a non-integer cache size")
	}
}

// TestDisabledConfigIgnoresGarbageSize is codex round 1 finding 1: a
// disabled cache must be bulletproof-inert. An unrelated, stale, or
// malformed EnvMaxEntries must never fail startup for a deployment that
// never opted into the cache.
func TestDisabledConfigIgnoresGarbageSize(t *testing.T) {
	cases := map[string]string{
		"unset":            "",
		"zero":             "0",
		"negative":         "-5",
		"non-integer":      "banana",
		"way out of range": "999999999",
	}
	for name, size := range cases {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{EnvEnabled: "false"}
			if size != "" {
				values[EnvMaxEntries] = size
			}
			cfg, err := ConfigFromEnv(lookupFrom(values))
			if err != nil {
				t.Fatalf("ConfigFromEnv with EnvEnabled=false must never error on EnvMaxEntries=%q, got: %v", size, err)
			}
			if cfg.Enabled {
				t.Fatal("cache must stay disabled")
			}
		})
	}
	// Same proof again with EnvEnabled entirely unset (the real default
	// startup shape), not just explicitly "false".
	cfg, err := ConfigFromEnv(lookupFrom(map[string]string{EnvMaxEntries: "not-a-number"}))
	if err != nil {
		t.Fatalf("ConfigFromEnv with EnvEnabled unset must never error on a garbage EnvMaxEntries, got: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("cache must default disabled")
	}
}

func TestWrapIsNoopWhenDisabledOrNil(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}

	if got := Wrap(inner, Config{Enabled: false, MaxEntries: DefaultMaxEntries}); got != contextfabric.Embedder(inner) {
		t.Fatal("Wrap must return the embedder unchanged when disabled")
	}
	if got := Wrap(nil, Config{Enabled: true, MaxEntries: DefaultMaxEntries}); got != nil {
		t.Fatal("Wrap must return nil unchanged even when enabled")
	}
}

func TestWrapReturnsCacheWhenEnabled(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	wrapped := Wrap(inner, Config{Enabled: true, MaxEntries: DefaultMaxEntries})
	if _, ok := wrapped.(*Cache); !ok {
		t.Fatalf("Wrap did not return a *Cache when enabled, got %T", wrapped)
	}
	// Sanity: the wrapped embedder still functions and still caches.
	ctx := context.Background()
	if _, err := wrapped.Embed(ctx, []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := wrapped.Embed(ctx, []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if inner.callCount() != 1 {
		t.Fatalf("inner embedder called %d times, want 1", inner.callCount())
	}
}
