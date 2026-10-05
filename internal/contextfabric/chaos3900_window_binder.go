package contextfabric

import (
	"regexp"
	"strings"
	"unicode"
)

// CHAOS-3900 temporal-expression binder (design brief v5.2, §1.2(d)/D2 flow
// diagram): PROPOSAL ONLY per chris's final-stamp descope ruling, in W0 and
// still in W1. A guards-passing span PROPOSES a RelativeID for the
// inferred-default path (overriding the class-table pick, §1.2's "a
// guards-passing binder span's RelativeID overrides the class table's
// pick") -- it NEVER mints question_stated authority (three successive
// absence proofs for an entity-collision guard failed; side-channel
// prediction of what resolution could bind is unsound as a class, per
// chris's ruling; see chaos3900-window-flow.md diagram D2). This file
// implements ONLY the proposal half: BindWindowSpans, the multi-span
// refusal, and the structural role check. There is no collision guard in
// this slice -- none is needed, because a binder proposal never reaches
// decisive authority on its own; a colliding span costs at most one
// disclosed, gated inferred default (§1.2(d)'s own closing argument).
//
// W0 shipped this in package graphrank, shadow-only, beside CHAOS-3896/
// 3899's own grammar-registry discipline (chaos3899_handle_grammar.go). W1's
// canonicalizeEvidenceWindow (window.go) calls ProposeWindowFromSpans as a
// real pre-tryReuse engine step, which package graphrank's existing
// "graphrank imports contextfabric" direction would turn into an import
// cycle -- so this file moved into package contextfabric unchanged in
// behavior.
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
	RelativeID RelativeWindowID
	SpanStart  int
	SpanEnd    int
}

type windowGrammarEntry struct {
	name       string
	relativeID RelativeWindowID
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
	{name: "trailing_month", relativeID: RelativeWindowTrailing30D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+month\b`)},
	{name: "trailing_quarter", relativeID: RelativeWindowTrailing90D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+quarter\b`)},
	{name: "trailing_year", relativeID: RelativeWindowTrailing365D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+year\b`)},
	// CHAOS-6557: numeric trailing phrases, closed to the widths that ARE
	// registry RelativeIDs. Prod acceptance (req lane-mcp-accept-d49b78d5)
	// asked "over the last 30 days"; with no entry here the class table's
	// trailing_90d default was offered instead of the stated 30 days. Any
	// other width ("last 14 days", "last 2 months") still binds nothing --
	// never the nearest registry width. Disjoint from the three entries
	// above: those require the unit word directly after last/past.
	{name: "trailing_30_days", relativeID: RelativeWindowTrailing30D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+(?:30|thirty)\s+days\b`)},
	{name: "trailing_90_days", relativeID: RelativeWindowTrailing90D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+(?:90|ninety)\s+days\b`)},
	{name: "trailing_3_months", relativeID: RelativeWindowTrailing90D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+(?:3|three)\s+months\b`)},
	{name: "trailing_365_days", relativeID: RelativeWindowTrailing365D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+365\s+days\b`)},
	{name: "trailing_12_months", relativeID: RelativeWindowTrailing365D, pattern: regexp.MustCompile(`(?i)\b(?:last|past)\s+(?:12|twelve)\s+months\b`)},
}

// BindWindowSpans applies the closed grammar to question (verbatim
// request.Question) and returns every bound span. The registry's patterns
// target disjoint token shapes (a bare month/quarter/year word, or one
// fixed number+unit pair per entry), so no overlap-dedup is needed,
// mirroring BindHandles.
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
	if strippedPrecedingPreposition(lowerBefore) {
		return true
	}
	after := question[span.SpanEnd:]
	// isReuseTerminalPunctuation (answer_reuse.go), NOT a hand-rolled set --
	// codex review finding (W1 round 4): QuestionHash's own CanonicalizeQuestion
	// strips this EXACT closed set (including ';'/':', which a hand-rolled
	// subset here previously omitted) before hashing, so two questions
	// differing ONLY in which of these trailing marks they end with
	// already hash IDENTICALLY. A binder role-check using a NARROWER set
	// let those two hash-identical questions reach genuinely DIFFERENT
	// binder outcomes (one clause-final-passes, the other doesn't) --
	// which, for the inferred-default path (no reuse-key fragment of its
	// own, guarded only by the single deployment-wide WindowInferenceVersion),
	// meant a reuse hit could silently serve the OTHER phrasing's inferred
	// window. Sharing the identical closed set with QuestionHash is what
	// makes "same hash" and "same binder outcome" actually agree.
	trailing := strings.TrimFunc(after, func(r rune) bool {
		return unicode.IsSpace(r) || isReuseTerminalPunctuation(r)
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
	RelativeID RelativeWindowID // set only when Reason == WindowBindRoutedInferred
	// Grammar (CHAOS-6557) is the registry entry's own fixed name, set
	// only when Reason == WindowBindRoutedInferred -- safe to trace (never
	// question text), so the binder telemetry line can name WHICH phrase
	// shape proposed the window.
	Grammar    string
	SpansBound int
	// Trailing (CHAOS-6560) is set only when Reason == WindowBindRoutedInferred
	// and the span states a TRAILING window ("in the last month", "past
	// quarter", "last 30 days"): the caller's own words fix the bounds, so the
	// engine may commit it. A bare "last month/quarter/year" (no preposition)
	// names the previous CALENDAR period, which this closed grammar does not
	// bound as a trailing window: the Calendar field carries it instead.
	Trailing bool
	// PointInTime (CHAOS-6560) is set when the span is the object of an
	// explicit point-in-time construction ("as of the end of last month"): the
	// caller asked about a state at an instant, not a period of activity, so
	// the span is never an evidence window.
	PointInTime bool
	// Calendar is set when the question names the previous
	// CALENDAR month, quarter or year with a bare "last month|quarter|year"
	// that states no trailing window. It is the binder's own deterministic
	// reading of the phrase: the engine derives the bounds from its own clock,
	// so the window never depends on what a sampled interpreter said. Reason is
	// untouched (a non-trailing span stays a proposal off the MCP surface).
	Calendar CalendarPeriod
}

// CalendarPeriod is the closed vocabulary of previous-calendar-period phrases.
type CalendarPeriod string

const (
	CalendarPeriodNone    CalendarPeriod = ""
	CalendarPeriodMonth   CalendarPeriod = "month"
	CalendarPeriodQuarter CalendarPeriod = "quarter"
	CalendarPeriodYear    CalendarPeriod = "year"
)

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
	roleOK := hasWindowRole(question, span)
	calendar := calendarPeriodOfSpan(question, span, roleOK)
	if !roleOK {
		return WindowBindOutcome{Reason: WindowBindSpanUnbound, SpansBound: 1, Calendar: calendar}
	}
	return WindowBindOutcome{Reason: WindowBindRoutedInferred, RelativeID: span.RelativeID, Grammar: span.Grammar, SpansBound: 1, Trailing: spanStatesTrailingWindow(question, span), PointInTime: spanIsPointInTime(question, span), Calendar: calendar}
}

// strippedPrecedingPreposition reports whether lowerBefore (lowercased text
// before a span, article already stripped) ends in a closed temporal
// preposition.
func strippedPrecedingPreposition(lowerBefore string) bool {
	for _, prep := range windowRolePrepositions {
		if _, ok := trimSuffixWord(lowerBefore, prep); ok {
			return true
		}
	}
	return false
}

// spanStatesTrailingWindow (CHAOS-6557) reports whether a bound span states a
// TRAILING window rather than naming a calendar period. Every grammar entry
// except the bare "last month|quarter|year" shapes is trailing by its own
// words ("past month", "last 30 days"); "last month|quarter|year" is trailing
// only in the article-led forms "in/over/within/during/for the last month" and
// after "since" -- "for last month", "over last month" and a bare "carried the
// most work last month" name the previous CALENDAR period.
func spanStatesTrailingWindow(question string, span BoundWindowSpan) bool {
	switch span.Grammar {
	case "trailing_month", "trailing_quarter", "trailing_year":
	default:
		return true
	}
	if !strings.HasPrefix(strings.ToLower(question[span.SpanStart:span.SpanEnd]), "last") {
		return true
	}
	lowerBefore := strings.ToLower(strings.TrimRightFunc(question[:span.SpanStart], unicode.IsSpace))
	if _, ok := trimSuffixWord(lowerBefore, "since"); ok {
		return true
	}
	stripped, hadArticle := lowerBefore, false
	for _, article := range windowRoleArticles {
		if s, ok := trimSuffixWord(lowerBefore, article); ok {
			stripped, hadArticle = s, true
			break
		}
	}
	return hadArticle && strippedPrecedingPreposition(stripped)
}

// windowPointInTimeLeads are the closed word groups that, immediately before a
// span (articles aside), make it the object of an explicit point in time.
var windowPointInTimeLeads = []string{"as of", "as at", "end of", "start of", "beginning of", "close of"}

// spanIsPointInTime reports whether the words just before span form a closed
// point-in-time construction ("as of the end of last month").
func spanIsPointInTime(question string, span BoundWindowSpan) bool {
	lowerBefore := strings.ToLower(strings.TrimRightFunc(question[:span.SpanStart], unicode.IsSpace))
	for {
		stripped := lowerBefore
		for _, article := range windowRoleArticles {
			if s, ok := trimSuffixWord(stripped, article); ok {
				stripped = s
				break
			}
		}
		if stripped == lowerBefore {
			break
		}
		lowerBefore = stripped
	}
	for _, lead := range windowPointInTimeLeads {
		if _, ok := trimSuffixWord(lowerBefore, lead); ok {
			return true
		}
	}
	return false
}

// calendarPeriodOfSpan () reports which previous CALENDAR period a
// bound span names: only a bare "last month|quarter|year" that is neither a
// trailing form (spanStatesTrailingWindow) nor the object of a point-in-time
// construction. A span that passed the role check qualifies. One that failed it
// qualifies only when the clause position is the ordinary "...last month and
// why?" tail (a coordinating conjunction directly after the span). Either way
// a determiner or possessive directly before the span refuses it: "the Last Year
// and Beyond project" is a name, and "the last month" is not a calendar period.
func calendarPeriodOfSpan(question string, span BoundWindowSpan, roleOK bool) CalendarPeriod {
	var period CalendarPeriod
	switch span.Grammar {
	case "trailing_month":
		period = CalendarPeriodMonth
	case "trailing_quarter":
		period = CalendarPeriodQuarter
	case "trailing_year":
		period = CalendarPeriodYear
	default:
		return CalendarPeriodNone
	}
	if spanStatesTrailingWindow(question, span) || spanIsPointInTime(question, span) {
		return CalendarPeriodNone
	}
	if spanPrecededByDeterminer(question, span) {
		return CalendarPeriodNone
	}
	if roleOK || spanFollowedByConjunction(question, span) {
		return period
	}
	return CalendarPeriodNone
}

var windowConjunctions = []string{"and", "but"}

// spanFollowedByConjunction reports whether the first word after span is a
// closed coordinating conjunction.
func spanFollowedByConjunction(question string, span BoundWindowSpan) bool {
	after := strings.ToLower(strings.TrimLeftFunc(question[span.SpanEnd:], func(r rune) bool { return unicode.IsSpace(r) || r == ',' }))
	for _, conjunction := range windowConjunctions {
		if strings.HasPrefix(after, conjunction) && (len(after) == len(conjunction) || !isWindowWordByte(after[len(conjunction)])) {
			return true
		}
	}
	return false
}

var windowDeterminers = []string{"the", "a", "an", "this", "that", "our", "their", "my", "your", "his", "her", "its"}

// spanPrecededByDeterminer reports whether the word directly before span is a
// closed determiner or possessive.
func spanPrecededByDeterminer(question string, span BoundWindowSpan) bool {
	lowerBefore := strings.ToLower(strings.TrimRightFunc(question[:span.SpanStart], unicode.IsSpace))
	for _, determiner := range windowDeterminers {
		if _, ok := trimSuffixWord(lowerBefore, determiner); ok {
			return true
		}
	}
	return false
}
