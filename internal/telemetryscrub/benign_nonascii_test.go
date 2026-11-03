package telemetryscrub

import (
	"log/slog"
	"testing"
)

func TestNonASCIIKeysNeverMatchBenign(t *testing.T) {
	for _, k := range []string{"secretéary", "outputé_tokens"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, "leaky-value") })
		if got[k] != Redacted {
			t.Errorf("key %q kept value %v, want redacted", k, got[k])
		}
		if BenignKind(k) != "" {
			t.Errorf("BenignKind(%q) = %q", k, BenignKind(k))
		}
	}
	for _, k := range []string{"output_tokens", "output-tokens", "output.tokens", "output tokens", "output​_tokens", "OutputTokens", "secretary"} {
		got := record(t, func(l *slog.Logger) { l.Info("m", k, 7) })
		if got[k] != float64(7) {
			t.Errorf("benign key %q = %v, want kept", k, got[k])
		}
	}
	if BenignKind("pass") != "int" || BenignKind("Pass​") != "int" {
		t.Errorf("typed benign key lost")
	}
}
