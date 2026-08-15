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
	if _, err := ConfigFromEnv(lookupFrom(map[string]string{EnvMaxEntries: "0"})); err == nil {
		t.Fatal("expected an error for a zero cache size")
	}
	if _, err := ConfigFromEnv(lookupFrom(map[string]string{EnvMaxEntries: "not-a-number"})); err == nil {
		t.Fatal("expected an error for a non-integer cache size")
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
