package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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

// --- codex round-2 P2: Unicode-aware phrase boundaries, not ASCII \b ---

// TestExpandWithLexicon_UnicodeWordsDoNotFalselyExpand is the regression
// proof: Go's regexp \b is ASCII-only (RE2's \w is exactly [0-9A-Za-z_]),
// so a naive `\bpr\b` reads the transition from ASCII 'r' to a non-ASCII
// letter as a word boundary -- "prévision" (French) and "orgánico"
// (Spanish) must NOT expand, even though they contain "pr"/"org" as a
// byte-level prefix, because "é"/"á" are genuine word runes
// (isFulltextWordRune's unicode.IsLetter test) with no boundary before
// them. MUTATION CHECK: reverting hasWholeWordPhrase to a `\b`-based regexp
// makes both of these expand.
func TestExpandWithLexicon_UnicodeWordsDoNotFalselyExpand(t *testing.T) {
	cases := []string{
		"la prévision du projet",         // French "forecast" -- starts with "pr"
		"desarrollo orgánico del equipo", // Spanish "organic" -- starts with "org"
	}
	for _, text := range cases {
		if got := expandWithLexicon(text); got != text {
			t.Fatalf("expandWithLexicon(%q) = %q, want unchanged -- a Unicode word must not be split at a non-ASCII rune", text, got)
		}
	}
}

// TestExpandWithLexicon_AsciiWordsStillExpand is
// UnicodeWordsDoNotFalselyExpand's positive companion: genuine short-hand
// usage in ordinary (ASCII) text must keep expanding exactly as before --
// the Unicode-aware boundary check must not become a blanket "never expand
// near non-ASCII" over-correction, since it never sees non-ASCII input in
// these cases at all.
func TestExpandWithLexicon_AsciiWordsStillExpand(t *testing.T) {
	cases := map[string]string{
		"pr 42":                "pull request",
		"the org's repos":      "organization",
		"who owns this ticket": "issue",
	}
	for text, wantSynonym := range cases {
		got := expandWithLexicon(text)
		if got == text {
			t.Fatalf("expandWithLexicon(%q) did not expand, want it to widen with %q", text, wantSynonym)
		}
		if !strings.Contains(strings.ToLower(got), wantSynonym) {
			t.Fatalf("expandWithLexicon(%q) = %q, want it to contain %q", text, got, wantSynonym)
		}
	}
}

// TestHasWholeWordPhrase_BoundaryIsRuneAware unit-tests the boundary
// primitive directly, independent of the lexicon table: a phrase match
// immediately followed or preceded by any unicode letter/digit (ASCII or
// not) is not a whole-word match; immediately followed/preceded by
// punctuation, whitespace, or nothing (start/end of string) is.
func TestHasWholeWordPhrase_BoundaryIsRuneAware(t *testing.T) {
	cases := []struct {
		text, phrase string
		want         bool
	}{
		{"prévision", "pr", false},  // non-ASCII letter immediately after -- not a boundary
		{"pr é vision", "pr", true}, // space after "pr" -- genuine boundary
		{"the pr.", "pr", true},     // punctuation after -- genuine boundary
		{"orgánico", "org", false},  // non-ASCII letter immediately after
		{"the org", "org", true},    // end of string after -- genuine boundary
		{"reorg", "org", false},     // ASCII letter immediately before -- not a boundary
		{"pr", "pr", true},          // exact, whole string
	}
	for _, tc := range cases {
		if got := hasWholeWordPhrase(strings.ToLower(tc.text), strings.ToLower(tc.phrase)); got != tc.want {
			t.Errorf("hasWholeWordPhrase(%q, %q) = %v, want %v", tc.text, tc.phrase, got, tc.want)
		}
	}
}

// --- codex round-4 P1 (fix A): kind-scoping + the field-label collision class guard ---

// TestCompileLexicon_PanicsOnUnscopedLabelCollision is the LAYER 2 class
// guard's own proof, against a TEST-ONLY lexicon instance (never the
// shipped domainLexiconGroups): an unscoped phrase equal to a real
// search-text field-label word ("team", fieldLabelTeam) must panic at
// compile/init time, before it could ever reach a live query and produce
// wrong-kind lexical hits.
func TestCompileLexicon_PanicsOnUnscopedLabelCollision(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("compileLexicon() did not panic on an unscoped phrase colliding with a field-label word")
		}
	}()
	compileLexicon([]domainLexiconGroup{{phrases: []string{"team"}}})
}

// TestCompileLexicon_KindScopedCollisionDoesNotPanic is the companion
// control: the SAME colliding word, kind-scoped, must NOT panic -- scoping
// is a valid, checked way to close the collision (queries.go restricts the
// expansion query to that one subject kind), not merely a suppression of
// the guard.
func TestCompileLexicon_KindScopedCollisionDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("compileLexicon() panicked on a KIND-SCOPED colliding phrase: %v", r)
		}
	}()
	compileLexicon([]domainLexiconGroup{{phrases: []string{"team"}, targetKind: contextfabric.SubjectTeam}})
}

// TestCompileLexicon_ShippedLexiconHasNoUnscopedLabelCollisions proves the
// REAL, shipped domainLexiconGroups passes its own guard -- package init
// already proves this (a panic there would fail every test in the
// package), but this pins it as an explicit, individually-runnable
// assertion rather than relying on that side effect alone.
func TestCompileLexicon_ShippedLexiconHasNoUnscopedLabelCollisions(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("the shipped domainLexiconGroups panicked: %v", r)
		}
	}()
	compileLexicon(domainLexiconGroups)
}

// TestFulltextSearchNodes_RepositoryTermDoesNotSurfaceOtherKindsViaTheRepoLabel
// is the round-4 P1 end-to-end regression proof: term "repository" widens
// via the kind-scoped {"repo","repository"} group. The fake distinguishes
// the query production actually sends: "repo" WITH a subject_kind=repository
// filter is the FIX's shape (only genuine repository-kind rows can match);
// "repo" with NO kind filter is the BUG's shape a real RediSearch server
// would ALSO match against any pull_request/deployment/ci_pipeline_run/
// pull_request_review row, since every one of those templates composes the
// structural "repo: <slug>" field label into its own search_text
// regardless of subject matter. If kind-scoping is ever lost, the fake's
// unscoped branch fires and the false-positive PR row appears --
// this IS the mutation check, built into the fixture.
func TestFulltextSearchNodes_RepositoryTermDoesNotSurfaceOtherKindsViaTheRepoLabel(t *testing.T) {
	repoRow := fulltextRow("repository", "repo_1", "full-chaos/dev-health-acr", "full-chaos/dev-health-acr", nil)
	prWithRepoLabelRow := fulltextRow("pull_request", "pr_1", "Fix login bug", "Fix login bug repo: full-chaos/dev-health-acr", nil)

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		kind, hasKind := params["kind"]
		switch {
		case q == "repository":
			// Base query: nothing in this fixture is literally named "repository".
			return nil, nil
		case q == "repo" && hasKind && kind == "repository":
			// The FIX's shape: kind-scoped. A real RediSearch server,
			// restricted to subject_kind='repository', can never see the
			// PR's row at all.
			return []row{repoRow}, nil
		case q == "repo" && !hasKind:
			// The BUG's shape: an unscoped "repo" query. A real RediSearch
			// server would match BOTH rows here -- the repository's own
			// slug text and the PR's structural "repo: " label.
			return []row{repoRow, prWithRepoLabelRow}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	candidates, _, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", "repository", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	repoUUID := subjectUUID("repository", "repo_1")
	prUUID := subjectUUID("pull_request", "pr_1")
	var sawRepo, sawPR bool
	for _, c := range candidates {
		if c.UUID == repoUUID {
			sawRepo = true
		}
		if c.UUID == prUUID {
			sawPR = true
		}
	}
	if sawPR {
		t.Fatalf("candidates = %#v, want the pull_request row ABSENT -- the \"repo\" expansion must be scoped to the repository kind, not match every kind's own \"repo: \" field label", candidates)
	}
	if !sawRepo {
		t.Fatalf("candidates = %#v, want the genuine repository subject present -- kind-scoped expansion must still find actual repositories", candidates)
	}
}
