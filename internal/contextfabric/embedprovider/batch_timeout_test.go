package embedprovider

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestBatchCallsUseTheDedicatedBatchTimeoutNotTheReadSideTimeout pins
// CHAOS-3828: the write/projection path issues one request for up to
// MaxBatch texts, and before this fix that request was bounded by the SAME
// Config.Timeout sized for a single read-path query -- so a 64-text batch
// against anything but a very fast warm local model timed out on every
// call, and the projection failure path cleared every target's vector as a
// result (indistinguishable from a genuine embedder outage).
//
// The server here always takes 150ms to respond. Timeout is set well below
// that (50ms, read-path budget) and BatchTimeout well above it (1s,
// write-path budget). A single-text call must still fail closed under the
// short read-side Timeout (proving the two knobs are genuinely
// independent, not one collapsing into the other); a multi-text call must
// SUCCEED under the longer BatchTimeout.
func TestBatchCallsUseTheDedicatedBatchTimeoutNotTheReadSideTimeout(t *testing.T) {
	t.Parallel()
	server := embeddingsServer(t, func(inputs []string) (int, any) {
		time.Sleep(150 * time.Millisecond)
		items := make([]map[string]any, 0, len(inputs))
		for i := range inputs {
			items = append(items, embeddingItem(i, float64(i)))
		}
		return http.StatusOK, okResponse(items...)
	})
	cfg := testConfig(server.URL)
	cfg.Timeout = 50 * time.Millisecond
	cfg.BatchTimeout = time.Second
	cfg.MaxBatch = DefaultMaxBatch
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("single-text (read-path shape) still bounded by the short Timeout", func(t *testing.T) {
		t.Parallel()
		if _, err := embedder.Embed(context.Background(), []string{"one query"}); err == nil {
			t.Fatal("a single-text call slower than Timeout must fail closed -- if it succeeded, BatchTimeout leaked into the read path")
		}
	})

	t.Run("multi-text (write-path shape) succeeds under the longer BatchTimeout", func(t *testing.T) {
		t.Parallel()
		vectors, err := embedder.Embed(context.Background(), []string{"doc one", "doc two", "doc three"})
		if err != nil {
			t.Fatalf("a batch slower than the read-side Timeout but faster than BatchTimeout must succeed, got: %v", err)
		}
		if len(vectors) != 3 {
			t.Fatalf("got %d vectors, want 3", len(vectors))
		}
	})
}

// TestConfigFromEnvReadsTheBatchTimeout pins that ACR_CONTEXT_FABRIC_EMBED_BATCH_TIMEOUT
// is read independently of ACR_CONTEXT_FABRIC_EMBED_TIMEOUT, with its own
// default when unset.
func TestConfigFromEnvReadsTheBatchTimeout(t *testing.T) {
	t.Parallel()
	baseEnv := map[string]string{
		EnvBaseURL:           "https://embed.example/v1/",
		EnvProvider:          "test-provider",
		EnvModel:             "test-model",
		EnvDimension:         "768",
		EnvAllowNoCredential: "true",
	}

	t.Run("defaults when unset", func(t *testing.T) {
		t.Parallel()
		cfg, err := ConfigFromEnv(lookupOf(baseEnv))
		if err != nil {
			t.Fatalf("ConfigFromEnv: %v", err)
		}
		if cfg.BatchTimeout != DefaultBatchTimeout {
			t.Fatalf("cfg.BatchTimeout = %v, want default %v", cfg.BatchTimeout, DefaultBatchTimeout)
		}
		if cfg.Timeout != DefaultTimeout {
			t.Fatalf("cfg.Timeout = %v, want default %v", cfg.Timeout, DefaultTimeout)
		}
	})

	t.Run("overridden independently of Timeout", func(t *testing.T) {
		t.Parallel()
		env := make(map[string]string, len(baseEnv)+1)
		for k, v := range baseEnv {
			env[k] = v
		}
		env[EnvBatchTimeout] = "10s"
		cfg, err := ConfigFromEnv(lookupOf(env))
		if err != nil {
			t.Fatalf("ConfigFromEnv: %v", err)
		}
		if cfg.BatchTimeout != 10*time.Second {
			t.Fatalf("cfg.BatchTimeout = %v, want 10s", cfg.BatchTimeout)
		}
		if cfg.Timeout != DefaultTimeout {
			t.Fatalf("cfg.Timeout = %v, want the unaffected default %v", cfg.Timeout, DefaultTimeout)
		}
	})

	t.Run("garbage value is a loud error, not a silent fallback", func(t *testing.T) {
		t.Parallel()
		env := make(map[string]string, len(baseEnv)+1)
		for k, v := range baseEnv {
			env[k] = v
		}
		env[EnvBatchTimeout] = "not-a-duration"
		if _, err := ConfigFromEnv(lookupOf(env)); err == nil {
			t.Fatal("a garbage batch timeout must fail ConfigFromEnv, not silently fall back to the default")
		}
	})
}
