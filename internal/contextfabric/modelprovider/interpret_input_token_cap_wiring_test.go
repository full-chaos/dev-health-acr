package modelprovider

import "testing"

// The interpret input token cap reaches the primary runtime and the fallback
// runtime alike: both send the same interpret prompt.
func TestRuntimeConfigsCarryTheInterpretInputTokenCap(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "test-model", FallbackModel: "test-fallback", MaxInterpretInputTokens: 40000}

	if got := runtimeConfigWithPhrasing(cfg, cfg.Model, nil).MaxInterpretInputTokens; got != 40000 {
		t.Fatalf("primary MaxInterpretInputTokens = %d, want 40000", got)
	}
	if got := fallbackRuntimeConfig(cfg).MaxInterpretInputTokens; got != 40000 {
		t.Fatalf("fallback MaxInterpretInputTokens = %d, want 40000", got)
	}
}
