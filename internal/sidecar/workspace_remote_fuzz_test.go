package sidecar

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzRedactRemoteURLForError is a property test asserting, for every
// input: (1) output is valid UTF-8, (2) output never exceeds the strict
// maxSanitizedRemoteEcho byte bound (which includes the truncation suffix),
// and (3) output contains no control character. The seed corpus covers the
// deterministic cases in workspace_remote_test.go plus the invalid-UTF-8
// shapes exercised by FuzzSanitizeMessage in api_errors_fuzz_test.go (lone
// lead bytes, an encoded surrogate half, and a codepoint above U+10FFFF),
// since redactRemoteURLForError's control-character pass relies on the same
// Go for-range-over-string substitution of U+FFFD for invalid bytes.
func FuzzRedactRemoteURLForError(f *testing.F) {
	f.Add("https://github.com/owner/repo.git")
	f.Add("https://user:pass@github.com/owner/repo.git")
	f.Add("a:secret@b@host:owner/repo")
	f.Add(strings.Repeat("a", 185) + "é" + strings.Repeat("b", 50))
	f.Add("x" + strings.Repeat("\U0001F44D", 100))
	f.Add("https://host/" + strings.Repeat("a", 10_000))
	f.Add("\xff\x80")
	f.Add(strings.Repeat("\xc3", 300))
	f.Add("\xed\xa0\x80")     // encoded UTF-16 surrogate half, always invalid in UTF-8
	f.Add("\xf4\x90\x80\x80") // codepoint above U+10FFFF, always invalid in UTF-8
	f.Add("host\x01with\x02control\x03chars@owner/repo")
	f.Add("")

	f.Fuzz(func(t *testing.T, raw string) {
		got := redactRemoteURLForError(raw)

		if !utf8.ValidString(got) {
			t.Fatalf("redactRemoteURLForError(%q) produced invalid UTF-8: %q", raw, got)
		}
		if len(got) > maxSanitizedRemoteEcho {
			t.Fatalf("redactRemoteURLForError(%q) exceeded the %d-byte budget: %d bytes: %q", raw, maxSanitizedRemoteEcho, len(got), got)
		}
		for _, r := range got {
			if unicode.IsControl(r) {
				t.Fatalf("redactRemoteURLForError(%q) leaked a control character: %q", raw, got)
			}
		}
	})
}
