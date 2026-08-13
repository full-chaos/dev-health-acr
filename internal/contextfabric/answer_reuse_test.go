package contextfabric

import "testing"

// TestAC_3782_5_WhitespaceCaseTrailingPunctuationInsensitive binds AC-3782-5's
// first half literally: two questions that differ only in whitespace,
// letter case, or trailing punctuation produce the same canonical hash.
func TestAC_3782_5_WhitespaceCaseTrailingPunctuationInsensitive(t *testing.T) {
	base := "is the auth migration on track"
	variants := []string{
		"Is the auth migration on track",
		"IS THE AUTH MIGRATION ON TRACK",
		"is the auth migration on track?",
		"is the auth migration on track ?",
		"is the auth migration on track!?",
		"  is   the  auth migration   on track  ",
		"\tis the auth migration on track\n",
		"is the auth migration on track.",
	}
	want := QuestionHash(base)
	for _, variant := range variants {
		if got := QuestionHash(variant); got != want {
			t.Errorf("QuestionHash(%q) = %q, want %q (same as base %q)", variant, got, want, base)
		}
	}
}

// TestAC_3782_5_AnyWordDifferenceChangesHash binds AC-3782-5's second half:
// two questions that differ in any word do not produce the same hash.
func TestAC_3782_5_AnyWordDifferenceChangesHash(t *testing.T) {
	base := QuestionHash("is the auth migration on track")
	differing := []string{
		"is the auth migration off track",
		"is the auth migration on schedule",
		"was the auth migration on track",
		"is the payments migration on track",
		"is the auth migration on track today", // an added word is a word difference
	}
	for _, question := range differing {
		if got := QuestionHash(question); got == base {
			t.Errorf("QuestionHash(%q) unexpectedly matched the base question's hash", question)
		}
	}
}

func TestCanonicalizeQuestion_fixedPointOnRepeatedTrailingPunctuation(t *testing.T) {
	got := CanonicalizeQuestion("done ? ! . ")
	want := "done"
	if got != want {
		t.Errorf("CanonicalizeQuestion = %q, want %q", got, want)
	}
}

func TestCanonicalizeQuestion_preservesInternalPunctuationAndWordBoundaries(t *testing.T) {
	// Internal punctuation and hyphenation must NOT be stripped -- only
	// trailing punctuation is. "self-review" and "self review" are
	// different words for reuse purposes.
	if CanonicalizeQuestion("what's blocking self-review?") == CanonicalizeQuestion("what's blocking self review?") {
		t.Error("internal hyphenation must not be normalized away")
	}
	if got, want := CanonicalizeQuestion("what's blocking release?"), "what's blocking release"; got != want {
		t.Errorf("CanonicalizeQuestion = %q, want %q", got, want)
	}
}

// TestAC_3782_5_OnlyClosedTerminalPunctuationSetIsStripped is the Codex
// round-1 F7 regression: unicode.IsPunct also matches '#', '@', '&', '%',
// '*', '_', and '-', so the prior implementation silently stripped them
// too and merged materially different questions ("is this C#?" and "is
// this C" canonicalized identically). Only [. ? ! , ; :] may be treated
// as trailing punctuation; every other character, even at the very end
// of the string, must survive untouched.
func TestAC_3782_5_OnlyClosedTerminalPunctuationSetIsStripped(t *testing.T) {
	cases := []struct{ in, want string }{
		{"is this c#", "is this c#"},
		{"is this c#?", "is this c#"}, // trailing '?' stripped, '#' preserved
		{"ping @support", "ping @support"},
		{"ping @support.", "ping @support"}, // trailing '.' stripped, '@' preserved
		{"discount is 10%", "discount is 10%"},
		{"what changed in v2*", "what changed in v2*"},
		{"see appendix a_1", "see appendix a_1"},
		{"status: blocked-", "status: blocked-"},
	}
	for _, tc := range cases {
		if got := CanonicalizeQuestion(tc.in); got != tc.want {
			t.Errorf("CanonicalizeQuestion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// The closed set itself still strips and still merges across it.
	closedSetVariants := []string{"done", "done.", "done?", "done!", "done,", "done;", "done:"}
	want := QuestionHash(closedSetVariants[0])
	for _, variant := range closedSetVariants[1:] {
		if got := QuestionHash(variant); got != want {
			t.Errorf("QuestionHash(%q) = %q, want %q (same as %q)", variant, got, want, closedSetVariants[0])
		}
	}
}

func TestQuestionHash_isSHA256HexOfCanonicalForm(t *testing.T) {
	got := QuestionHash("Hello?")
	if len(got) != 64 {
		t.Fatalf("QuestionHash length = %d, want 64 (hex-encoded SHA-256)", len(got))
	}
	for _, r := range got {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("QuestionHash %q is not lowercase hex", got)
		}
	}
}
