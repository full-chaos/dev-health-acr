package sidecar

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzSanitizeMessage locks the security invariants of sanitizeMessage
// against arbitrary, potentially invalid-UTF-8 input: the result must
// always be valid UTF-8, must never exceed maxSanitizedMessageLength bytes,
// and must never contain a control character. It also checks idempotency:
// sanitizing an already-sanitized message must not change it further. The
// seed corpus covers the exact-boundary, one-over, multi-byte, and
// invalid-input cases exercised individually in api_errors_test.go, plus a
// few additional malformed-UTF-8 shapes.
func FuzzSanitizeMessage(f *testing.F) {
	seeds := []string{
		"",
		"plain ascii message",
		strings.Repeat("a", maxSanitizedMessageLength),
		strings.Repeat("a", maxSanitizedMessageLength+1),
		strings.Repeat("a", maxSanitizedMessageLength-1) + "é",
		"a" + strings.Repeat("👍", 130),
		strings.Repeat("日", 250),
		"line one\nFAKE-LOG-LINE injected\x00tail",
		"before\xff\x80after",
		strings.Repeat("\xff", 1000),
		strings.Repeat("\xc3", 300), // lone lead bytes never followed by a valid continuation byte
		"\xed\xa0\x80",              // encoded UTF-16 surrogate half, invalid in UTF-8
		"\xf4\x90\x80\x80",          // codepoint above U+10FFFF, invalid in UTF-8
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got := sanitizeMessage(raw)

		if !utf8.ValidString(got) {
			t.Fatalf("sanitizeMessage produced invalid UTF-8 for input %q: got %q", raw, got)
		}
		if len(got) > maxSanitizedMessageLength {
			t.Fatalf("sanitizeMessage exceeded the byte budget for input %q: got %d bytes", raw, len(got))
		}
		for _, r := range got {
			if unicode.IsControl(r) {
				t.Fatalf("sanitizeMessage left a control character %q in output for input %q", r, raw)
			}
		}
		if again := sanitizeMessage(got); again != got {
			t.Fatalf("sanitizeMessage is not idempotent: sanitize(%q) = %q, sanitize(that) = %q", raw, got, again)
		}
	})
}
