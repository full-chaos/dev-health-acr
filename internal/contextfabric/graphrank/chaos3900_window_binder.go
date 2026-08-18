package graphrank

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-3900 W0 (design brief v5.2, §1.2(d)/D2 flow diagram): the
// deterministic, engine-side temporal-expression binder, SHADOW/PROPOSAL
// ONLY per chris's final-stamp descope ruling. A guards-passing span
// PROPOSES a RelativeID for the inferred-default path (overriding the
// class-table pick, §1.2's "a guards-passing binder span's RelativeID
// overrides the class table's pick") -- it NEVER mints question_stated
// authority (three successive absence proofs for an entity-collision guard
// failed; side-channel prediction of what resolution could bind is unsound
// as a class, per chris's ruling; see chaos3900-window-flow.md diagram D2).
// This file implements ONLY the proposal half: BindWindowSpans, the
// multi-span refusal, and the structural role check. There is no
// collision guard in W0 (or in any v1 slice) -- none is needed, because
// nothing here ever reaches decisive authority; a colliding span costs at
// most one disclosed, gated inferred default (§1.2(d)'s own closing
// argument).
//
// Corpus-safety discipline (same as chaos3899_handle_grammar.go's
// BoundHandle): BoundWindowSpan below deliberately carries ONLY byte
// offsets into the question, never the matched substring itself -- so
// there is no span text field to accidentally let leak into a trace or
// report in the first place, stricter than BoundHandle needs to be (that
// type's Value is a real identifier that legitimately needs to reach a
// census predicate; a temporal span never does).

// BoundWindowSpan is one grammar-bound temporal span: the CLOSED registry
// entry that matched, the RelativeID it maps to, and its exact source span
// (offsets only -- see this file's own doc comment).
type BoundWindowSpan struct {
	Grammar    string // the registry entry's own fixed name -- safe to trace
	RelativeID contextfabric.RelativeWindowID
	SpanStart  int
	SpanEnd    int
}

type windowGrammarEntry struct {
	name       string
	relativeID contextfabric.RelativeWindowID
	pattern    *regexp.Regexp
}

// windowGrammarRegistry is the CLOSED slice-1 grammar (design brief
// §1.2/§8: "Slice-1 grammar registry: relative trailing expressions mapped
// to registry RelativeIDs"). Every pattern anchors on \b (Go RE2
// word-boundary) on both sides, giving the identical maximal-munch,
// word-boundary discipline handleGrammarRegistry's own doc comment
// explains (chaos3899_handle_grammar.go) -- a property of the regex
// engine, not application logic.
//
// Deliberately narrower than the design brief's full description ("plus
// explicit month/date-range shapes"): W0 is a measurement slice whose own
// premise is that this reach is small (0/50 corpus questions carry any
// temporal expression at all, per the brief's measured basis) -- the three
// relative-trailing phrases are the slice-1 registry; explicit date-range
// shapes are a registry addition for a later slice if the measured
// residual ever justifies it, exactly the ci_run_id pattern's own
// "minimal, conservative slice-1 grammar... widening it is a registry
// addition, not a redesign" precedent.
var windowGrammarRegistry = []windowGrammarEntry{
	{name: "trailing_month", relativeID: contextfabric.RelativeWindowTrailing30D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+month\b`)},
	{name: "trailing_quarter", relativeID: contextfabric.RelativeWindowTrailing90D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+quarter\b`)},
	{name: "trailing_year", relativeID: contextfabric.RelativeWindowTrailing365D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+year\b`)},
}

// BindWindowSpans applies the closed grammar to question (verbatim
// request.Question) and returns every bound span. The registry's three
// patterns target disjoint token shapes (month/quarter/year are mutually
// exclusive words), so no overlap-dedup is needed, mirroring BindHandles.
func BindWindowSpans(question string) []BoundWindowSpan {
	var bound []BoundWindowSpan
	for _, entry := range windowGrammarRegistry {
		for _, loc := range entry.pattern.FindAllStringIndex(question, -1) {
			bound = append(bound, BoundWindowSpan{Grammar: entry.name, RelativeID: entry.relativeID, SpanStart: loc[0], SpanEnd: loc[1]})
		}
	}
	return bound
}

// IsMultiWindowSpan reports whether bound names a multi-span shape (design
// brief §1.2(d)'s adopted multi_handle discipline applied to time): TWO OR
// MORE grammar-bound spans in one question refuses proposal authority
// entirely, never silently picks one.
func IsMultiWindowSpan(bound []BoundWindowSpan) bool {
	return len(bound) >= 2
}

// windowRolePrepositions is the closed, structural preposition set a
// single bound span may be preceded by to earn proposal authority (design
// brief §1.2(d)'s "grammatical-role check": "a closed preposition set
// (within/over/in/during/since/for/past the/last/the last)"). "past"/
// "last" themselves are already consumed by the grammar match (they lead
// every windowGrammarRegistry pattern), so this set covers the
// PRECEDING-preposition half of that closed list; the clause-initial/
// clause-final adverbial-position half is a separate structural check,
// hasWindowRole below.
var windowRolePrepositions = []string{"within", "over", "in", "during", "since", "for"}

// hasWindowRole is the STRUCTURAL role check (design brief §1.2(d)): a
// span earns proposal authority only when it occupies a temporal
// grammatical role checkable deterministically from token context alone --
// preceded by a closed preposition, or standing in clause-initial/
// clause-final adverbial position. Deliberately NOT a semantic check: a
// span in a temporal role that is ALSO an entity name (e.g. a project
// literally named "Last Year") passes this exactly as the brief's own B2
// history records (v4's overclaim, corrected in round-4/v5.2) -- W0 carries
// no collision guard because nothing here ever reaches decisive authority
// (see this file's package doc comment).
func hasWindowRole(question string, span BoundWindowSpan) bool {
	before := question[:span.SpanStart]
	if strings.TrimSpace(before) == "" {
		// Clause-initial: nothing but whitespace precedes the span.
		return true
	}
	lowerBefore := strings.ToLower(strings.TrimRightFunc(before, unicode.IsSpace))
	// Tolerate ONE intervening closed article ("the"/"a"/"an") between the
	// preposition and the span itself ("within THE last month", "over A
	// past year" -- ungrammatical but harmless to also accept): the
	// article carries no temporal-role information of its own, so
	// stripping it keeps the check STRUCTURAL (a second fixed closed word
	// set), never semantic.
	for _, article := range windowRoleArticles {
		if stripped, ok := trimSuffixWord(lowerBefore, article); ok {
			lowerBefore = stripped
			break
		}
	}
	if lowerBefore == "" {
		// After stripping a bare leading article ("The last month has been
		// rough" -- before="The "), nothing else precedes the span: this
		// IS clause-initial, re-testing the condition the top-of-function
		// check already applies to a truly empty `before`. Without this,
		// "The last month..." fails both the preposition check (nothing
		// left to match) and the clause-final check (text still follows
		// the span) and was wrongly refused.
		return true
	}
	for _, prep := range windowRolePrepositions {
		if _, ok := trimSuffixWord(lowerBefore, prep); ok {
			return true
		}
	}
	after := question[span.SpanEnd:]
	trailing := strings.TrimFunc(after, func(r rune) bool {
		return unicode.IsSpace(r) || r == '.' || r == '?' || r == ',' || r == '!'
	})
	if trailing == "" {
		// Clause-final: only trailing whitespace/terminal punctuation
		// follows the span.
		return true
	}
	return false
}

// windowRoleArticles is the closed set hasWindowRole tolerates between a
// preposition and the span it governs -- see that function's own comment.
var windowRoleArticles = []string{"the", "a", "an"}

// trimSuffixWord reports whether s ends with word as a whole word
// (word-boundary on both sides -- preceded by a non-word byte or
// start-of-string, per isWindowWordByte) and, if so, returns s with that
// trailing word and any whitespace immediately before it removed.
func trimSuffixWord(s, word string) (string, bool) {
	if !strings.HasSuffix(s, word) {
		return s, false
	}
	boundaryIdx := len(s) - len(word)
	if boundaryIdx != 0 && isWindowWordByte(s[boundaryIdx-1]) {
		return s, false
	}
	return strings.TrimRightFunc(s[:boundaryIdx], unicode.IsSpace), true
}

func isWindowWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// WindowBindReason is the closed, counted shadow-vocabulary for the
// binder's own outcome (design brief §1.2(d) / D2 flow diagram): every
// value here is a `log/slog`-safe enum, never derived text.
type WindowBindReason string

const (
	// WindowBindNoSpan: the grammar bound nothing at all.
	WindowBindNoSpan WindowBindReason = "no_span"
	// WindowBindSpanUnbound: exactly one span bound but failed the role
	// check (counted `temporal_span_unbound`, role-fail sub-reason).
	WindowBindSpanUnbound WindowBindReason = "temporal_span_unbound"
	// WindowBindSpanAmbiguous: two or more spans bound (counted
	// `temporal_span_ambiguous`).
	WindowBindSpanAmbiguous WindowBindReason = "temporal_span_ambiguous"
	// WindowBindRoutedInferred: exactly one span bound and passed the role
	// check -- routes to the inferred-default path as a PROPOSAL (counted
	// `binder_span_routed_inferred`), never question_stated.
	WindowBindRoutedInferred WindowBindReason = "binder_span_routed_inferred"
)

// WindowBindOutcome is the SHADOW-ONLY binder verdict for one question.
type WindowBindOutcome struct {
	Reason     WindowBindReason
	RelativeID contextfabric.RelativeWindowID // set only when Reason == WindowBindRoutedInferred
	SpansBound int
}

// ProposeWindowFromSpans runs the whole W0 binder pipeline over question
// (verbatim request.Question, pre-reuse, pre-model, engine-side -- per the
// design brief's own D2 flow diagram): grammar match, multi-span refusal,
// then the structural role check on the single surviving span. The
// returned RelativeID (when Reason == WindowBindRoutedInferred) is a
// PROPOSAL for the inferred-default path only -- see this file's own
// package doc comment for why it can never be more than that in W0/v1.
func ProposeWindowFromSpans(question string) WindowBindOutcome {
	bound := BindWindowSpans(question)
	switch {
	case len(bound) == 0:
		return WindowBindOutcome{Reason: WindowBindNoSpan}
	case IsMultiWindowSpan(bound):
		return WindowBindOutcome{Reason: WindowBindSpanAmbiguous, SpansBound: len(bound)}
	}
	span := bound[0]
	if !hasWindowRole(question, span) {
		return WindowBindOutcome{Reason: WindowBindSpanUnbound, SpansBound: 1}
	}
	return WindowBindOutcome{Reason: WindowBindRoutedInferred, RelativeID: span.RelativeID, SpansBound: 1}
}
