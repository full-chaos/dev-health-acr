package falkorgraph

// CHAOS-3827: a caller-typed question whose punctuation stands ALONE between
// spaces (the trailing "?" of `... "Horizontal scaling readiness"?`, once the
// quote it was glued to is stripped as a separator) used to survive
// tokenizeForFulltext as a term of its own. fulltextSearchNodes joins terms
// with RediSearch's OR operator, so that term became a bare `|?` element in
// the query string -- a RediSearch SYNTAX ERROR, not a zero-match term, which
// failed the whole search and surfaced as "context fabric graph dependency
// error during search context graph", killing subject resolution for that
// question.
//
// Live-verified against the dev graph (read-only db.idx.fulltext.queryNodes
// probes, FalkorDB at 127.0.0.1:16379):
//
//	'What|is|the|status|of|Horizontal|scaling|readiness|?' -> Syntax error at offset 50
//	'What|is|the|status|of|Horizontal|scaling|readiness'   -> 915 nodes
//	'What|is|the|status|of|Horizontal|scaling|readiness?'  -> 915 nodes
//
// The last line is the measurement-preservation constraint these tests pin
// alongside the fix: a term with punctuation GLUED to it ("readiness?") is
// accepted by RediSearch and matches exactly what the bare word matches, so
// the fix must not rewrite it -- only terms that are punctuation and nothing
// else are dropped. Every bare punctuation rune probed (`?`, `~`, `{`, `.`,
// `_`, `#`, `&`, `/`, `+`, `=`, `!`, `,`, backtick, `<`, `>`, `^`) errors the
// same way in a `readiness|X` join, so the rule is a rune CLASS ("no letter
// and no digit"), never a hand-picked metacharacter list -- the same choice
// isFulltextWordRune already documents.
//
// Leading-glued metacharacters are the second live-broken class this file
// pins (probed the same way): `{foo`, `}foo`, `[foo`, `]foo`, `;foo`, `$foo`
// are syntax errors even with a real word attached, and `~foo` is worse than
// an error -- it silently becomes a FUZZY match (35987 nodes against the dev
// graph, versus 47 for the bare word). Trimming the leading run of
// non-letter/digit runes is the same rune-class rule applied to the front of
// a term, and it leaves trailing-glued punctuation ("readiness?") untouched.

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// chaos3827Question is the live-reproduced question from the ticket: the
// closing quote is a tokenizeForFulltext separator, so the "?" that followed
// it is left standing alone.
const chaos3827Question = `What is the status of "Horizontal scaling readiness"?`

// hasLetterOrDigit reports whether term contains at least one Unicode letter
// or digit -- the exact property a RediSearch query element must have to be
// a term rather than a syntax error.
func hasLetterOrDigit(term string) bool {
	for _, r := range term {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func TestTokenizeForFulltextDropsBarePunctuationTerms(t *testing.T) {
	got := tokenizeForFulltext(chaos3827Question)
	for _, term := range got {
		if !hasLetterOrDigit(term) {
			t.Fatalf("tokenizeForFulltext(%q) produced bare-punctuation term %q -- OR-joined into the RediSearch query this is a syntax error, not a zero-match term (full: %#v)",
				chaos3827Question, term, got)
		}
	}
	want := []string{"What", "is", "the", "status", "of", "Horizontal", "scaling", "readiness"}
	if len(got) != len(want) {
		t.Fatalf("tokenizeForFulltext(%q) = %#v, want %#v", chaos3827Question, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokenizeForFulltext(%q)[%d] = %q, want %q (full: %#v)", chaos3827Question, i, got[i], want[i], got)
		}
	}
}

// TestTokenizeForFulltextKeepsGluedPunctuationAndUnicodeWords is the
// measurement-preservation half: the fix must change nothing about a term
// that already carries a letter or digit, so existing lexical behaviour (and
// every relevance number derived from a search's candidate set) is untouched.
func TestTokenizeForFulltextKeepsGluedPunctuationAndUnicodeWords(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{name: "trailing glued punctuation survives verbatim", text: "is readiness? yes", want: []string{"is", "readiness?", "yes"}},
		{name: "unicode letters are words", text: "café naïve 日本語", want: []string{"café", "naïve", "日本語"}},
		{name: "digit-only term survives", text: "sprint 42 50%", want: []string{"sprint", "42", "50"}},
		// The asymmetry is deliberate: the OPENING backtick leads a term and
		// is trimmed, the closing one trails a real word and is left exactly
		// where it was.
		{name: "backticked slug keeps its trailing punctuation", text: "repo `dev-health-acr` status", want: []string{"repo", "dev", "health", "acr`", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenizeForFulltext(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("tokenizeForFulltext(%q) = %#v, want %#v", tc.text, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("tokenizeForFulltext(%q)[%d] = %q, want %q (full: %#v)", tc.text, i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestTokenizeForFulltextTrimsLeadingMetacharacters pins the second
// live-broken class (see this file's header): a metacharacter glued to the
// FRONT of a real word either errors outright or silently changes the query's
// meaning, so the leading run of non-letter/digit runes is trimmed.
func TestTokenizeForFulltextTrimsLeadingMetacharacters(t *testing.T) {
	for _, text := range []string{"{foo", "}foo", "[foo", "]foo", ";foo", "$foo", "~foo", "#foo", "_foo", "...foo"} {
		got := tokenizeForFulltext(text)
		if len(got) != 1 || got[0] != "foo" {
			t.Fatalf("tokenizeForFulltext(%q) = %#v, want [\"foo\"] -- a leading metacharacter is a RediSearch syntax error or a silent fuzzy/prefix rewrite, never part of the word", text, got)
		}
	}
}

// chaos3827AlwaysBrokenRunes are the six runes live-probed as a RediSearch
// syntax error in EVERY position -- leading-glued, trailing-glued, and bare
// (`{foo`/`foo{`, `[foo`/`foo[`, `;foo`/`foo;`, `}`/`]`, and `~`, which
// trailing-glued errors and leading-glued silently becomes a fuzzy match).
// Because no working query can contain them anywhere, removing them is pure
// error recovery: it cannot change the result set -- and so cannot change
// the lexical relevance -- of any query that works today. That is exactly
// why they are stripped as separators while a general trailing trim is not:
// a trailing trim would rewrite "readiness?", which RediSearch ACCEPTS and
// which live returns the identical result set to the bare word.
const chaos3827AlwaysBrokenRunes = "{}[];~"

// chaos3827BracketQuestion is the residual live repro found while probing
// CHAOS-3827: with only the leading trim in place this tokenizes to
// `What|is|the|status|of|Q3]|readiness?`, which FalkorDB rejects outright
// (live: syntax error), so the bracketed question still killed the whole
// search. After the six runes join the separator set it tokenizes to
// `What|is|the|status|of|Q3|readiness?` -- live: 905 nodes.
const chaos3827BracketQuestion = `What is the status of [Q3] readiness?`

func TestTokenizeForFulltextStripsAlwaysBrokenRunes(t *testing.T) {
	t.Run("bracketed question keeps both real words", func(t *testing.T) {
		got := tokenizeForFulltext(chaos3827BracketQuestion)
		want := []string{"What", "is", "the", "status", "of", "Q3", "readiness?"}
		if len(got) != len(want) {
			t.Fatalf("tokenizeForFulltext(%q) = %#v, want %#v", chaos3827BracketQuestion, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("tokenizeForFulltext(%q)[%d] = %q, want %q (full: %#v)", chaos3827BracketQuestion, i, got[i], want[i], got)
			}
		}
	})
	t.Run("no term carries an always-broken rune in any position", func(t *testing.T) {
		texts := []string{
			chaos3827BracketQuestion,
			"deploy {config} now", "list [items] here", "a;b", "roughly ~5 days",
			"trailing brace foo} and bracket foo] and semicolon foo; and tilde foo~",
		}
		for _, text := range texts {
			for _, term := range tokenizeForFulltext(text) {
				if strings.ContainsAny(term, chaos3827AlwaysBrokenRunes) {
					t.Fatalf("tokenizeForFulltext(%q) produced term %q carrying one of %q -- live-verified as a RediSearch syntax error in every position, so it can only fail the whole query",
						text, term, chaos3827AlwaysBrokenRunes)
				}
			}
		}
	})
	t.Run("stripping a broken rune never swallows the word beside it", func(t *testing.T) {
		for _, text := range []string{"{foo}", "[foo]", ";foo;", "~foo~", "foo]"} {
			got := tokenizeForFulltext(text)
			if len(got) != 1 || got[0] != "foo" {
				t.Fatalf("tokenizeForFulltext(%q) = %#v, want [\"foo\"] -- the rune is unusable, the word beside it is not", text, got)
			}
		}
	})
}

func TestTokenizeForFulltextPurePunctuationYieldsNoTerms(t *testing.T) {
	for _, text := range []string{"?", "??? !!!", "-- ...", "|%@\"'*-():", " . , ; "} {
		if got := tokenizeForFulltext(text); len(got) != 0 {
			t.Fatalf("tokenizeForFulltext(%q) = %#v, want no terms", text, got)
		}
	}
}

// TestFulltextSearchQueryStringHasNoBarePunctuationElement asserts the
// property at the level that actually reaches RediSearch: every `|`-delimited
// element of the query string fulltextSearchNodes builds must be a real term.
func TestFulltextSearchQueryStringHasNoBarePunctuationElement(t *testing.T) {
	var captured string
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if q, ok := params["query"].(string); ok {
			captured = q
		}
		return []row{
			{"node": &node{Properties: map[string]interface{}{propKind: "work_item", propCanonicalID: "hs", propLabel: "Horizontal scaling readiness"}}, "score": 2.0},
		}, nil
	}}
	adapter := newFakeAdapter(t, fake)

	for _, question := range []string{chaos3827Question, chaos3827BracketQuestion} {
		captured = ""
		candidates, _, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", question, 10, temporalFilter{})
		if err != nil {
			t.Fatalf("fulltextSearchNodes(%q) error = %v", question, err)
		}
		if len(candidates) != 1 {
			t.Fatalf("fulltextSearchNodes(%q) returned %d candidates, want 1", question, len(candidates))
		}
		if captured == "" {
			t.Fatalf("fulltextSearchNodes(%q) issued no query carrying a $query parameter", question)
		}
		for _, element := range strings.Split(captured, "|") {
			if !hasLetterOrDigit(element) {
				t.Fatalf("fulltextSearchNodes() built query %q with element %q carrying no letter or digit -- RediSearch rejects that whole query as a syntax error", captured, element)
			}
			if first := []rune(element)[0]; !unicode.IsLetter(first) && !unicode.IsDigit(first) {
				t.Fatalf("fulltextSearchNodes() built query %q with element %q starting on a metacharacter -- live-verified as a RediSearch syntax error (`{foo`) or a silent fuzzy rewrite (`~foo`)", captured, element)
			}
			if strings.ContainsAny(element, chaos3827AlwaysBrokenRunes) {
				t.Fatalf("fulltextSearchNodes() built query %q with element %q carrying one of %q -- live-verified as a RediSearch syntax error in every position", captured, element, chaos3827AlwaysBrokenRunes)
			}
		}
	}
}

// TestFulltextSearchPurePunctuationQuestionResolvesToNoCandidates covers the
// empty-after-filter case: a question made only of punctuation must take
// fulltextSearchNodes' existing len(terms)==0 early return -- no candidates,
// no error, and no query sent at all -- rather than reaching RediSearch with
// a query string it would reject.
func TestFulltextSearchPurePunctuationQuestionResolvesToNoCandidates(t *testing.T) {
	queried := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		queried = true
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

	var candidates []graphrank.CandidateNode
	candidates, truncated, err := adapter.fulltextSearchNodes(context.Background(), "test-key", "org-1", `??? "..." !!`, 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v, want nil for an all-punctuation question", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("fulltextSearchNodes() = %#v, want no candidates", candidates)
	}
	if truncated {
		t.Fatalf("fulltextSearchNodes() reported truncated=true for a question that produced no terms")
	}
	if queried {
		t.Fatalf("fulltextSearchNodes() sent a query for a question that produced no terms")
	}
}
