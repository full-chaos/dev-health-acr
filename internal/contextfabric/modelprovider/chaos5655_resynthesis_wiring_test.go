package modelprovider

import "testing"

// TestRuntimeConfigWithPhrasingPropagatesResynthesisAttempts mirrors
// TestRuntimeConfigPropagatesLogger's shape for CHAOS-5655's own knob:
// runtimeConfigWithPhrasing (the PRIMARY runtime's own builder, see
// newPrimaryRuntime) must copy MaxSynthesisResynthesisAttempts from Config.
func TestRuntimeConfigWithPhrasingPropagatesResynthesisAttempts(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "test-model", MaxSynthesisResynthesisAttempts: 3}

	if got := runtimeConfigWithPhrasing(nil, cfg, cfg.Model, nil); got.MaxSynthesisResynthesisAttempts != 3 {
		t.Fatalf("runtimeConfigWithPhrasing().MaxSynthesisResynthesisAttempts = %d, want 3", got.MaxSynthesisResynthesisAttempts)
	}
}

// TestRuntimeConfigLeavesResynthesisAttemptsUnsetForTheFallbackLeg is the
// load-bearing negative half: the FALLBACK runtime is built through the
// plain runtimeConfig (newPrimaryRuntime's fallbackRuntime construction),
// which must NOT carry this knob regardless of what the primary's Config
// requested -- the ratified ordering ("primary re-samples first, fallback
// runs once after the bound is exhausted") depends on the fallback leg
// staying a single draw. genkitruntime.New then defaults the zero value to
// 1, matching today's pre-CHAOS-5655 behavior exactly.
func TestRuntimeConfigLeavesResynthesisAttemptsUnsetForTheFallbackLeg(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "test-model", MaxSynthesisResynthesisAttempts: 3}

	if got := runtimeConfig(nil, cfg, cfg.Model, nil); got.MaxSynthesisResynthesisAttempts != 0 {
		t.Fatalf("runtimeConfig().MaxSynthesisResynthesisAttempts = %d, want 0 (unset) -- the fallback leg must never inherit the primary's resynthesis bound", got.MaxSynthesisResynthesisAttempts)
	}
}

// TestRuntimeConfigWithPhrasingLeavesResynthesisAttemptsZeroWhenConfigDoesNotSetIt
// is the plain negative case, mirroring
// TestRuntimeConfigLeavesLoggerNilWhenConfigDoesNotSetIt: a caller that
// never sets the field (every pre-CHAOS-5655 caller) gets zero through,
// which genkitruntime.New defaults to 1.
func TestRuntimeConfigWithPhrasingLeavesResynthesisAttemptsZeroWhenConfigDoesNotSetIt(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "test-model"}
	if got := runtimeConfigWithPhrasing(nil, cfg, cfg.Model, nil); got.MaxSynthesisResynthesisAttempts != 0 {
		t.Fatalf("runtimeConfigWithPhrasing().MaxSynthesisResynthesisAttempts = %d, want 0", got.MaxSynthesisResynthesisAttempts)
	}
}
