package sidecar

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file isolates the redactRemoteURLForError test suite (credential
// redaction, byte-budget truncation, and UTF-8 boundary safety for the
// sanitized remote URL echoed into error text) from the parseRemoteURL
// shape-validation suite in workspace_remote_test.go, which owns the
// shared parseRemoteURL fixtures this package's other remote tests use.

func TestRedactRemoteURLForError_StripsUserinfo(t *testing.T) {
	const canary = "S3cr3t-Canary-Token"
	redacted := redactRemoteURLForError("https://user:" + canary + "@github.com/owner/repo.git")
	if strings.Contains(redacted, canary) {
		t.Fatalf("redactRemoteURLForError leaked the credential: %q", redacted)
	}
	if !strings.Contains(redacted, "github.com/owner/repo.git") {
		t.Fatalf("redactRemoteURLForError should preserve the non-credential shape, got %q", redacted)
	}
}

func TestRedactRemoteURLForError_BoundsLength(t *testing.T) {
	huge := "https://github.com/" + strings.Repeat("a", 10_000) + "/repo.git"
	redacted := redactRemoteURLForError(huge)
	if len(redacted) > maxSanitizedRemoteEcho {
		t.Fatalf("redactRemoteURLForError did not bound length (including suffix): %d bytes", len(redacted))
	}
}

// TestRedactRemoteURLForError_HandlesSchemeLessAndMalformedShapes proves
// redactRemoteURLForError's credential redaction does not depend on raw
// being a URL net/url.Parse can successfully round-trip: it must strip
// leading userinfo from scheme-less SCP-like shorthand (url.Parse returns
// an outright error on this shape) and from shapes url.Parse "succeeds"
// on but misparses (treating a bogus userinfo-looking prefix as a URL
// scheme, leaving u.User unpopulated), while leaving non-credential
// shapes — including an '@' that appears in a path segment rather than in
// leading userinfo position — untouched.
func TestRedactRemoteURLForError_HandlesSchemeLessAndMalformedShapes(t *testing.T) {
	const canary = "S3cr3t-Canary-Token"
	cases := []struct {
		name         string
		raw          string
		wantContains string
	}{
		{"scheme-less scp-like, bare username", canary + "@host:owner/repo", "host:owner/repo"},
		{"scheme-less scp-like, user and password", "user:" + canary + "@host:owner/repo", "host:owner/repo"},
		{"double leading '@' still fully redacted", "a:" + canary + "@b@host:owner/repo", "host:owner/repo"},
		{"ssh scheme with password", "ssh://user:" + canary + "@host/owner/repo", "host/owner/repo"},
		{"'@' in path position is not credentials", "https://host/owner/repo@" + canary, "host/owner/repo@" + canary},
		{"no '@' at all is unchanged", "https://host/owner/repo.git", "https://host/owner/repo.git"},
		{"local path is unchanged", "/absolute/local/path.git", "/absolute/local/path.git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redacted := redactRemoteURLForError(tc.raw)
			if tc.name != "'@' in path position is not credentials" && strings.Contains(redacted, canary) {
				t.Fatalf("redactRemoteURLForError(%q) leaked the credential canary: %q", tc.raw, redacted)
			}
			if !strings.Contains(redacted, tc.wantContains) {
				t.Fatalf("redactRemoteURLForError(%q) = %q, want it to contain %q", tc.raw, redacted, tc.wantContains)
			}
		})
	}
}

// TestRedactRemoteURLForError_StripsControlCharsAlongsideCredential proves
// the two defenses compose: a rejected remote carrying both a control
// character and a credential must have neither echoed back.
func TestRedactRemoteURLForError_StripsControlCharsAlongsideCredential(t *testing.T) {
	const canary = "S3cr3t-Canary-Token"
	redacted := redactRemoteURLForError(canary + "@ho\x01st:owner/repo")
	if strings.Contains(redacted, canary) {
		t.Fatalf("leaked the credential canary: %q", redacted)
	}
	if strings.ContainsRune(redacted, '\x01') {
		t.Fatalf("leaked a control character: %q", redacted)
	}
}

// TestRedactRemoteURLForError_ExactBudgetIsNotTruncated proves a string
// exactly at maxSanitizedRemoteEcho passes through unchanged: no suffix is
// appended when the input already fits the total bound.
func TestRedactRemoteURLForError_ExactBudgetIsNotTruncated(t *testing.T) {
	// No '@' before the first '/', so credential redaction is a no-op and
	// the raw byte count is exactly what redactRemoteURLForError sees.
	raw := "https://host/" + strings.Repeat("a", maxSanitizedRemoteEcho-len("https://host/"))
	if len(raw) != maxSanitizedRemoteEcho {
		t.Fatalf("test setup bug: raw is %d bytes, want exactly %d", len(raw), maxSanitizedRemoteEcho)
	}
	got := redactRemoteURLForError(raw)
	if got != raw {
		t.Fatalf("exact-budget input was altered: got %q, want unchanged %q", got, raw)
	}
}

// TestRedactRemoteURLForError_OneByteOverBudgetTruncatesWithinBound proves
// a single byte past maxSanitizedRemoteEcho triggers truncation, and the
// truncated result (prefix + suffix) never exceeds maxSanitizedRemoteEcho.
func TestRedactRemoteURLForError_OneByteOverBudgetTruncatesWithinBound(t *testing.T) {
	raw := "https://host/" + strings.Repeat("a", maxSanitizedRemoteEcho-len("https://host/")+1)
	got := redactRemoteURLForError(raw)
	if len(got) > maxSanitizedRemoteEcho {
		t.Fatalf("exceeded byte budget: %d bytes: %q", len(got), got)
	}
	if !strings.HasSuffix(got, remoteEchoTruncationSuffix) {
		t.Fatalf("expected truncation suffix, got %q", got)
	}
	want := raw[:remoteEchoTruncationBudget] + remoteEchoTruncationSuffix
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRedactRemoteURLForError_NeverSplitsATwoByteRuneAtBoundary reproduces
// the exact bug class fixed alongside sanitizeMessage's truncateUTF8
// adoption in api_errors.go: a raw byte slice at the budget boundary can
// bisect a multi-byte UTF-8 rune. remoteEchoTruncationBudget is 186
// (200 - len("...(truncated)")); this raw is built so byte index 186 is
// the continuation byte of a 2-byte rune ('é' = 0xC3 0xA9) that starts at
// byte 185.
func TestRedactRemoteURLForError_NeverSplitsATwoByteRuneAtBoundary(t *testing.T) {
	prefix := strings.Repeat("a", 185)
	raw := prefix + "é" + strings.Repeat("b", 50)
	if len(raw) <= maxSanitizedRemoteEcho {
		t.Fatalf("test setup bug: raw is only %d bytes, want > %d", len(raw), maxSanitizedRemoteEcho)
	}
	got := redactRemoteURLForError(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("produced invalid UTF-8: %q", got)
	}
	if len(got) > maxSanitizedRemoteEcho {
		t.Fatalf("exceeded byte budget: %d bytes: %q", len(got), got)
	}
	// The straddling rune is dropped whole, not split: the 185-byte ASCII
	// prefix survives and 'é' does not appear at all.
	want := prefix + remoteEchoTruncationSuffix
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRedactRemoteURLForError_NeverSplitsAFourByteRuneAtBoundary is the
// four-byte-rune counterpart: a run of 👍 (U+1F44D, 4 bytes each) after a
// 1-byte ASCII prefix puts budget byte 186 at offset 1 of the 47th emoji
// (a continuation byte), so that whole emoji must be dropped rather than
// split.
func TestRedactRemoteURLForError_NeverSplitsAFourByteRuneAtBoundary(t *testing.T) {
	raw := "x" + strings.Repeat("👍", 100)
	if len(raw) <= maxSanitizedRemoteEcho {
		t.Fatalf("test setup bug: raw is only %d bytes, want > %d", len(raw), maxSanitizedRemoteEcho)
	}
	got := redactRemoteURLForError(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("produced invalid UTF-8: %q", got)
	}
	if len(got) > maxSanitizedRemoteEcho {
		t.Fatalf("exceeded byte budget: %d bytes: %q", len(got), got)
	}
	want := "x" + strings.Repeat("👍", 46) + remoteEchoTruncationSuffix
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRedactRemoteURLForError_NormalizesInvalidUTF8Input proves invalid
// byte sequences in raw (which can never originate from a well-formed Git
// remote but must still be handled safely, since raw is untrusted input on
// the rejection path) never survive into the output: ranging over an
// invalid UTF-8 string in Go substitutes U+FFFD per bad byte, so the
// control-character-stripping pass always emits valid UTF-8 before
// truncateUTF8 ever runs, exactly mirroring sanitizeMessage's guarantee in
// api_errors.go.
func TestRedactRemoteURLForError_NormalizesInvalidUTF8Input(t *testing.T) {
	raw := "https://host/owner/repo\xff\x80" + strings.Repeat("\xc3", 400)
	got := redactRemoteURLForError(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("produced invalid UTF-8: %q", got)
	}
	if len(got) > maxSanitizedRemoteEcho {
		t.Fatalf("exceeded byte budget: %d bytes: %q", len(got), got)
	}
}

// TestRedactRemoteURLForError_CredentialSurvivesTruncationWithMultipleAtSigns
// combines two defenses under load: a credential canary behind multiple
// '@' characters (the shape leadingUserinfoPattern's greedy match must
// consume through the *last* '@', not the first) in a raw string long
// enough to also force truncation. Both the redaction and the strict byte
// bound must hold simultaneously.
func TestRedactRemoteURLForError_CredentialSurvivesTruncationWithMultipleAtSigns(t *testing.T) {
	const canary = "S3cr3t-Canary-Token-Do-Not-Leak"
	raw := "a:" + canary + "@b@host:owner/" + strings.Repeat("r", 300)
	got := redactRemoteURLForError(raw)
	if strings.Contains(got, canary) {
		t.Fatalf("leaked the credential canary: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("produced invalid UTF-8: %q", got)
	}
	if len(got) > maxSanitizedRemoteEcho {
		t.Fatalf("exceeded byte budget: %d bytes: %q", len(got), got)
	}
	if !strings.HasSuffix(got, remoteEchoTruncationSuffix) {
		t.Fatalf("expected truncation suffix, got %q", got)
	}
}

// TestRedactRemoteURLForError_NeverLeaksBytesBeyondTheBudget is a canary
// test: a distinctive marker planted past the byte budget must never
// appear in the sanitized output, and the output must never grow past the
// budget to make room for it.
func TestRedactRemoteURLForError_NeverLeaksBytesBeyondTheBudget(t *testing.T) {
	const canary = "CANARY-PAST-THE-BUDGET-MARKER"
	raw := "https://host/" + strings.Repeat("a", maxSanitizedRemoteEcho*2) + canary
	got := redactRemoteURLForError(raw)
	if strings.Contains(got, canary) {
		t.Fatalf("leaked bytes beyond the truncation budget: %q", got)
	}
	if len(got) > maxSanitizedRemoteEcho {
		t.Fatalf("exceeded byte budget: %d bytes: %q", len(got), got)
	}
}
