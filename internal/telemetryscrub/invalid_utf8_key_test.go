package telemetryscrub

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func recordRaw(build func(*slog.Logger)) string {
	var buf bytes.Buffer
	build(slog.New(Handler(slog.NewJSONHandler(&buf, nil))))
	return buf.String()
}

// An invalid UTF-8 byte is dropped by compact(), like a foreign letter, so it must not let a key fold onto a benign key.
func TestInvalidUTF8ByteInKeyNeverMatchesBenign(t *testing.T) {
	cases := []struct {
		key   string
		value any
		leak  string
	}{
		{"secret\xffary", "probe-value", "probe-value"},
		{"p\xffass", 7, ":7"},
		{"\xffoutput_tokens", "probe-value", "probe-value"},
		{"output_tokens\xff", "probe-value", "probe-value"},
		{"pass\xff", 7, ":7"},
		{"p\xc3ass", 7, ":7"},
		{"p\xe2\x80ass", 7, ":7"},
	}
	for _, c := range cases {
		out := recordRaw(func(l *slog.Logger) { l.Info("m", c.key, c.value) })
		if strings.Contains(out, c.leak) {
			t.Errorf("key %q kept its value: %s", c.key, out)
		}
		if !strings.Contains(out, Redacted) {
			t.Errorf("key %q not redacted: %s", c.key, out)
		}
		if k := BenignKind(c.key); k != "" {
			t.Errorf("BenignKind(%q) = %q, want none", c.key, k)
		}
		if e := newKeyEntry(c.key); e.kind != "" {
			t.Errorf("newKeyEntry(%q).kind = %q, want none", c.key, e.kind)
		}
	}
}

// A valid U+FFFD rune is a symbol, not an invalid byte: it folds like any other non-letter rune.
func TestValidReplacementRuneInKeyStaysFolded(t *testing.T) {
	for _, k := range []string{"output\uFFFD_tokens", "secret\uFFFDary"} {
		out := recordRaw(func(l *slog.Logger) { l.Info("m", k, 7) })
		if !strings.Contains(out, ":7") {
			t.Errorf("benign key %q lost its value: %s", k, out)
		}
	}
	if BenignKind("pass\uFFFD") != "int" {
		t.Errorf("BenignKind(%q) = %q, want int", "pass\uFFFD", BenignKind("pass\uFFFD"))
	}
}
