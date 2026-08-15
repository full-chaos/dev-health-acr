package falkorgraph

import (
	"strings"
	"testing"
)

// TestExpandWithLexicon_NoMatchIsByteIdentical is the single-most
// load-bearing property in this file: for any text no lexicon group
// matches, expandWithLexicon MUST return the exact same string. Both
// queries.go's fulltextSearchNodes and vector.go's vectorQueryText branch
// on `expanded != text` to keep the RediSearch query string and the
// embedcache key byte-identical to before this ticket for the overwhelming
// majority of terms that never touch the lexicon.
func TestExpandWithLexicon_NoMatchIsByteIdentical(t *testing.T) {
	cases := []string{
		"", "   ", "Ask Dev", "horizontal scaling readiness", "throughput trend",
		"principal engineer", "preview mode", "orgy", // substrings of lexicon phrases that must NOT match
	}
	for _, text := range cases {
		if got := expandWithLexicon(text); got != text {
			t.Fatalf("expandWithLexicon(%q) = %q, want unchanged (no lexicon phrase should match)", text, got)
		}
	}
}

// TestExpandWithLexicon_WholeWordBoundary proves the closed vocabulary
// matches whole words/phrases only, never a substring inside an unrelated
// word -- "pr" must not fire inside "principal", "org" must not fire
// inside "organism".
func TestExpandWithLexicon_WholeWordBoundary(t *testing.T) {
	cases := []string{"principal engineer", "organism detection", "preview mode", "orgy"}
	for _, text := range cases {
		if got := expandWithLexicon(text); got != text {
			t.Fatalf("expandWithLexicon(%q) = %q, want unchanged -- a lexicon phrase matched inside an unrelated word", text, got)
		}
	}
}

// TestExpandWithLexicon_AddsOtherGroupMembersOnce proves a genuine match
// widens the text with every OTHER phrase in the matched group, and does
// not re-add the phrase that was already matched.
func TestExpandWithLexicon_AddsOtherGroupMembersOnce(t *testing.T) {
	const text = "who approved the PR"
	got := expandWithLexicon(text)
	if got == text {
		t.Fatalf("expandWithLexicon(%q) did not widen a text containing the lexicon phrase \"PR\"", text)
	}
	if !strings.Contains(strings.ToLower(got), "pull request") {
		t.Fatalf("expandWithLexicon(%q) = %q, want it to contain synonym %q", text, got, "pull request")
	}
	added := strings.TrimPrefix(got, text)
	if hasWholeWord(added, "pr") {
		t.Fatalf("expandWithLexicon(%q) = %q, want the matched phrase \"PR\" itself NOT re-added", text, got)
	}
}

// TestExpandWithLexicon_MultiWordPhraseMatches proves a multi-word lexicon
// phrase ("pull request") is itself a valid match trigger, not just the
// short-hand side of a group.
func TestExpandWithLexicon_MultiWordPhraseMatches(t *testing.T) {
	const text = "status of the pull request"
	got := expandWithLexicon(text)
	if !strings.Contains(strings.ToLower(got), "pr") || !hasWholeWord(got, "pr") {
		t.Fatalf("expandWithLexicon(%q) = %q, want \"pr\" added", text, got)
	}
}

// TestExpandWithLexicon_MultipleGroupsCompose proves a text matching
// several DIFFERENT lexicon groups gets widened by all of them, not just
// the first.
func TestExpandWithLexicon_MultipleGroupsCompose(t *testing.T) {
	got := strings.ToLower(expandWithLexicon("which repo owns this ticket"))
	if !strings.Contains(got, "repository") {
		t.Fatalf("expandWithLexicon() = %q, want \"repository\" added for the \"repo\" match", got)
	}
	if !strings.Contains(got, "issue") || !strings.Contains(got, "work item") {
		t.Fatalf("expandWithLexicon() = %q, want \"issue\"/\"work item\" added for the \"ticket\" match", got)
	}
}

// TestExpandWithLexicon_Deterministic proves repeated calls on the same
// input produce the exact same output -- required for the embedcache (T11)
// key and for a reproducible RediSearch query string.
func TestExpandWithLexicon_Deterministic(t *testing.T) {
	const text = "the CI run for this repo failed"
	first := expandWithLexicon(text)
	for i := 0; i < 5; i++ {
		if got := expandWithLexicon(text); got != first {
			t.Fatalf("expandWithLexicon(%q) = %q on call %d, want %q (deterministic)", text, got, i, first)
		}
	}
}

// hasWholeWord reports whether word appears as one of text's own
// fulltextWords tokens (case-insensitive, whole-word) -- the same splitter
// production confidence scoring uses, so this test helper checks presence
// the same way the read path itself would.
func hasWholeWord(text, word string) bool {
	word = strings.ToLower(word)
	for _, f := range fulltextWords(text) {
		if f == word {
			return true
		}
	}
	return false
}
