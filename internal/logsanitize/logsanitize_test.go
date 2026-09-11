package logsanitize

import (
	"strings"
	"testing"
)

// TestSanitizeLogAttrDomain pins the moved implementation directly (CHAOS-5558):
// the input-domain table {clean, CRLF, ANSI/control, over-length, empty,
// unicode} the CHAOS-5544 line established, executed here so the moved code
// is proven independent of internal/contextfabric's own re-export test.
func TestSanitizeLogAttrDomain(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"clean", "req_abc123", "req_abc123"},
		{"crlf", "evil\nFAKE_LOG_LINE=injected\r\n", "evil?FAKE_LOG_LINE=injected??"},
		{"ansi_escape", "\x1b[31mred\x1b[0m", "?[31mred?[0m"},
		{"control_byte", "a\x00b", "a?b"},
		{"empty", "", ""},
		{"over_length", strings.Repeat("a", 300), strings.Repeat("a", 256)},
		{"unicode", "réq_ünïcödé", "r?q_?n?c?d?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeLogAttr(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeLogAttr(%q) = %q, want %q", tc.input, got, tc.want)
			}
			for _, r := range got {
				if r < 0x20 || r > 0x7e {
					t.Fatalf("SanitizeLogAttr(%q) produced a non-printable-ASCII rune %q", tc.input, r)
				}
			}
			if strings.ContainsAny(got, "\n\r") {
				t.Fatalf("SanitizeLogAttr(%q) = %q still carries a line break", tc.input, got)
			}
		})
	}
}

func TestSanitizeLogStrings(t *testing.T) {
	got := SanitizeLogStrings([]string{"clean", "evil\nline"})
	want := []string{"clean", "evil?line"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SanitizeLogStrings = %#v, want %#v", got, want)
	}
	if SanitizeLogStrings(nil) != nil {
		t.Fatalf("SanitizeLogStrings(nil) must return nil, not an empty slice")
	}
	original := []string{"evil\nline"}
	_ = SanitizeLogStrings(original)
	if original[0] != "evil\nline" {
		t.Fatalf("SanitizeLogStrings mutated the caller's backing array: %#v", original)
	}
}
