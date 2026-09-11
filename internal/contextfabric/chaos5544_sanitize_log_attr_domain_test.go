package contextfabric

import (
	"strings"
	"testing"
)

// TestSanitizeLogAttrFullInputDomainTable is the CHAOS-5544 one-pass
// INPUT-DOMAIN table for SanitizeLogAttr, the one recognized barrier every
// request-derived log attribute at all four call sites (telemetry.go's
// requestIDLogAttrs, and genkitruntime's logInterpretDecision /
// logSynthesizeDecision / logPhraseDecision) now routes through.
// SanitizeLogAttr takes a single string; Go's type system already rules out
// "absent"/"null"/"wrong scalar type" cells for that parameter (a string
// cannot be nil, and there is no second, differently-typed overload to
// exercise), so the value-shaped axis below is the whole domain: the zero
// value (empty), every out-of-vocabulary byte/rune class CWE-117 and this
// ticket's own doc comment name, the length-cap boundary on both sides, a
// duplicated occurrence of the SAME forgery shape (proving the replace is
// global, not first-match-only), and the canonical already-clean shape.
// Every cell executes in this ONE pass, not scattered per-shape hand tests.
func TestSanitizeLogAttrFullInputDomainTable(t *testing.T) {
	t.Parallel()

	rep256 := strings.Repeat("a", 256)
	rep255 := strings.Repeat("a", 255)
	rep257 := strings.Repeat("a", 257)

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		// canonical: the exact shape observability.WithRequestID ever stores.
		{"canonical", "req_0123456789abcdef0123456789abcdef", "req_0123456789abcdef0123456789abcdef"},
		// zero value / "absent" collapses to empty for a string parameter.
		{"empty_zero_value", "", ""},
		// out-of-vocabulary bytes, CWE-117's two forgery characters isolated.
		{"cr_only", "req_a\rb", "req_a?b"},
		{"lf_only", "req_a\nb", "req_a?b"},
		{"crlf_combined", "req_a\r\nFORGED", "req_a??FORGED"},
		{"nul", "req_a\x00b", "req_a?b"},
		{"tab", "req_a\tb", "req_a?b"},
		{"ansi_escape", "req_a\x1b[31mb", "req_a?[31mb"},
		{"unicode_line_separator_u2028", "req_a b", "req_a?b"},
		{"unicode_paragraph_separator_u2029", "req_a b", "req_a?b"},
		{"unicode_non_ascii", "req_aéb", "req_a?b"},
		// duplicate: the SAME forgery shape repeated three times must all be
		// replaced -- proves the replacer is global, not first-match-only.
		{"duplicate_crlf", "req_a\r\nb\r\nc\r\nd", "req_a??b??c??d"},
		// boundary / boundary±1 on the 256-rune cap.
		{"boundary_255_under_cap", rep255, rep255},
		{"boundary_256_at_cap", rep256, rep256},
		{"boundary_257_over_cap_by_one", rep257, rep256},
		{"over_length_well_past_cap", strings.Repeat("a", 300), rep256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeLogAttr(tc.in); got != tc.want {
				t.Fatalf("SanitizeLogAttr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
