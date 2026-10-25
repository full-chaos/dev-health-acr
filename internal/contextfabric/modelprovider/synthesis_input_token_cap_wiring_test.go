package modelprovider

import "testing"

// The synthesis input token cap reaches the primary runtime and the fallback
// runtime alike: both send the same synthesis prompt.
func TestRuntimeConfigsCarryTheSynthesisInputTokenCap(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "test-model", FallbackModel: "test-fallback", MaxSynthesisInputTokens: 40000}

	if got := runtimeConfigWithPhrasing(cfg, cfg.Model, nil).MaxSynthesisInputTokens; got != 40000 {
		t.Fatalf("primary MaxSynthesisInputTokens = %d, want 40000", got)
	}
	if got := fallbackRuntimeConfig(cfg).MaxSynthesisInputTokens; got != 40000 {
		t.Fatalf("fallback MaxSynthesisInputTokens = %d, want 40000", got)
	}
}
