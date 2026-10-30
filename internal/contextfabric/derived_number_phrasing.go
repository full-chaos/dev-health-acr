package contextfabric

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Derived-number phrasing (CHAOS-11600). An answer states some numbers it did
// not observe: a judged score, a theme share a model's categorization produced,
// and a value the synthesizer derived from other facts. AGENTS.md and the North
// Star contract ask every such statement to read as one -- appears, leans or
// suggests -- and never as an observation ("the score is 0.73").
//
// MeasureDerivedNumberPhrasing counts, in the prose a reader sees, the sentences
// that state a derived number, and how many of them state it with none of those
// words. It changes nothing: it is the per-answer measure the engine logs on
// every saved result and the corpus harness (cmd/acr-answer-phrasing) reads off
// stored answers. Evidence rules constrain claims; they never refuse the answer
// (North Star check 4), so this is a measurement, not a gate.

// derivedNumberFields is the closed set of claimed-fact fields whose number is
// derived, keyed by field name. A field here is derived on every fact kind that
// carries it.
var derivedNumberFields = map[string]string{
	// The compounding risk score is a judged value (decision K7), under each
	// name a fact carries it by.
	"compounding_risk":            derivedBasisScore,
	"latest_compounding_risk":     derivedBasisScore,
	"rule_basis_compounding_risk": derivedBasisScore,
	// An investment theme share is the roll-up of a model's categorization of
	// each work unit.
	"theme_feature_delivery":       derivedBasisModelCategorized,
	"theme_operational":            derivedBasisModelCategorized,
	"theme_maintenance":            derivedBasisModelCategorized,
	"theme_quality":                derivedBasisModelCategorized,
	"theme_risk":                   derivedBasisModelCategorized,
	"theme_quality_bugfix":         derivedBasisModelCategorized,
	"prior_theme_feature_delivery": derivedBasisModelCategorized,
	"prior_theme_operational":      derivedBasisModelCategorized,
	"prior_theme_maintenance":      derivedBasisModelCategorized,
	"prior_theme_quality":          derivedBasisModelCategorized,
	"prior_theme_risk":             derivedBasisModelCategorized,
}

const (
	derivedBasisScore            = "score"
	derivedBasisModelCategorized = "model_categorized"
	derivedBasisDerivation       = "derivation"
)

// DerivedNumberField reports whether a claimed fact's field states a derived
// number on every fact that carries it, and why: "score" or "model_categorized".
// A claim with derived_from is derived whatever its field.
func DerivedNumberField(field string) (string, bool) {
	basis, ok := derivedNumberFields[field]
	return basis, ok
}

// hedgeWord matches the words that state a derived number as one.
var hedgeWord = regexp.MustCompile(`(?i)\b(appears?|appeared|appearing|leans?|leaned|leaning|suggests?|suggested|suggesting)\b`)

// numberToken matches a number as prose writes it: an optional sign (a minus
// at the start or after a space or an opening bracket, never the hyphen of a
// date), digits with optional thousands separators and decimals, and an
// optional percent sign. The number is the first group.
var numberToken = regexp.MustCompile(`(?:^|[\s(\[])(-?\d[\d,]*(?:\.\d+)?%?)|(\d[\d,]*(?:\.\d+)?%?)`)

// DerivedNumberPhrasing is one answer's measure.
type DerivedNumberPhrasing struct {
	// DerivedValues is how many claimed facts state a derived number.
	DerivedValues int
	// Sentences is how many reader-facing sentences state one of them.
	Sentences int
	// Asserted is how many of those carry no appears, leans or suggests.
	Asserted int
}

// phrasingClaim is one derived value as prose may write it: its number forms,
// and the words that name what it measures.
type phrasingClaim struct {
	forms map[string]struct{}
	words *regexp.Regexp
}

// MeasureDerivedNumberPhrasing measures one answer. A sentence states a derived
// number when it carries one of the answer's derived claimed-fact values as the
// prose writes a number (the value verbatim, rounded to two decimals, or, for a
// share between 0 and 1, as a whole percent; with its sign, an ASCII or a
// Unicode minus) AND that number is attached to a word that names what the
// value measures (a word of its field, such as "risk" for a compounding risk
// score or "quality" for the quality theme share; "investment", "mix" or
// "theme" for any theme share): the word stands before the number with no
// other number between them, or after it in the same clause. An unrelated
// number, equal or not, is not counted. Dates and clock times are not numbers
// here. The sentence is qualified when appears, leans or suggests stands in
// that attachment or in the number's own clause; a hedge about something else
// in the sentence does not qualify the number.
//
// current_state is a list the service composes under a heading ("Derived
// values, which suggest rather than observe: ..."): the heading qualifies every
// item, so a list sentence is qualified by a hedge anywhere in it. The cohort
// ranking narration (cohortDriverJudgmentSummary) states only derived values
// (a signal's weight and value, its contribution, the attention score), so a
// driver summary in that shape is a derived-number sentence by construction.
//
// The prose read is everything a reader is given to read: the lead
// (direct_judgment and deterministic_answer), current_state,
// strongest_pressures, each driver's title, summary and qualification, each
// finding's summary, limitations and warnings.
func MeasureDerivedNumberPhrasing(result InvestigationResult) DerivedNumberPhrasing {
	var claims []phrasingClaim
	var measure DerivedNumberPhrasing
	for _, claim := range result.ClaimedFacts {
		if !derivedClaimOf(claim) {
			continue
		}
		var forms []string
		switch number, integer := claim.Value.Number, claim.Value.Integer; {
		case number != nil && !math.IsNaN(*number) && !math.IsInf(*number, 0):
			forms = numberForms(*number)
		case integer != nil:
			forms = []string{strconv.FormatInt(*integer, 10)}
		default:
			continue
		}
		measure.DerivedValues++
		set := make(map[string]struct{}, len(forms))
		for _, form := range forms {
			set[form] = struct{}{}
		}
		claims = append(claims, phrasingClaim{forms: set, words: measureWords(claim.Field)})
	}
	for _, sentence := range readerSentences(result) {
		var states, asserted bool
		if sentence.narration {
			states, asserted = true, !hedgeWord.MatchString(sentence.text)
		} else {
			states, asserted = measureSentence(sentence.text, claims, sentence.list)
		}
		if !states {
			continue
		}
		measure.Sentences++
		if asserted {
			measure.Asserted++
		}
	}
	return measure
}

// derivedClaimOf reports whether a claimed fact states a derived number: its
// field is in the closed set, or it carries derived_from.
func derivedClaimOf(claim ClaimedFact) bool {
	if len(claim.DerivedFrom) > 0 {
		return true
	}
	_, derived := DerivedNumberField(claim.Field)
	return derived
}

// splitDerivedClaims splits claims into observations and derived values, each
// in its original order. The service's own current_state lists them apart.
func splitDerivedClaims(claims []ClaimedFact) (observed, derived []ClaimedFact) {
	for _, claim := range claims {
		if derivedClaimOf(claim) {
			derived = append(derived, claim)
			continue
		}
		observed = append(observed, claim)
	}
	return observed, derived
}

// derivedValuesClause lists derived values in current_state as what they are.
func derivedValuesClause(sentences []string) string {
	return "Derived values, which suggest rather than observe: " + strings.Join(sentences, "; ") + "."
}

// measureWordsSkipped are field-name parts that say when or how a value was
// read, not what it measures.
var measureWordsSkipped = map[string]bool{"latest": true, "rule": true, "basis": true, "prior": true, "theme": true}

// measureWords matches a word that names what field measures: each part of the
// field name, and for a theme share also "investment", "mix" and "theme".
func measureWords(field string) *regexp.Regexp {
	var words []string
	for _, part := range strings.Split(field, "_") {
		if len(part) > 2 && !measureWordsSkipped[part] {
			words = append(words, regexp.QuoteMeta(part))
		}
	}
	if basis, _ := DerivedNumberField(field); basis == derivedBasisModelCategorized {
		words = append(words, "investment", "mix", "theme")
	}
	if len(words) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?i)\b(` + strings.Join(words, "|") + `)`)
}

// measureSentence reports whether sentence states one of claims, and whether
// any such statement is unqualified. A list sentence is qualified by a hedge
// anywhere in it.
func measureSentence(sentence string, claims []phrasingClaim, list bool) (states, asserted bool) {
	if len(claims) == 0 {
		return false, false
	}
	sentence = unicodeMinus.Replace(sentence)
	numbers := numberSpans(sentence)
	if len(numbers) == 0 {
		return false, false
	}
	hedges := hedgeWord.FindAllStringIndex(sentence, -1)
	breaks := clauseBreak.FindAllStringIndex(sentence, -1)
	for _, claim := range claims {
		var words [][]int
		if claim.words != nil {
			if words = claim.words.FindAllStringIndex(sentence, -1); len(words) == 0 {
				continue
			}
		}
		for i, number := range numbers {
			if _, ok := claim.forms[number.value]; !ok {
				continue
			}
			from, to, attached := attachment(words, numbers, i, breaks, len(sentence), claim.words == nil)
			if !attached {
				continue
			}
			states = true
			if list {
				asserted = asserted || len(hedges) == 0
				continue
			}
			clauseFrom, clauseTo := clauseAround(breaks, number.start, number.end, len(sentence))
			if !spansHedge(hedges, from, to) && !spansHedge(hedges, clauseFrom, clauseTo) {
				asserted = true
			}
		}
	}
	return states, asserted
}

// unicodeMinus writes the Unicode minus sign as the ASCII one.
var unicodeMinus = strings.NewReplacer("\u2212", "-")

// clauseBreak matches what ends a clause inside a sentence: a comma, a
// semicolon, a colon, a dash between spaces, or a coordinating "and", "but",
// "while" or "whereas".
var clauseBreak = regexp.MustCompile(`[,;:]|\s[\x{2013}\x{2014}-]\s|(?i:\s(?:and|but|while|whereas)\s)`)

// dateOrTime matches a date or a clock time, whose digits are not numbers a
// measure could state.
var dateOrTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:\d{2})?)?|\b\d{1,2}:\d{2}(?::\d{2})?\b`)

// numberSpan is one number in a sentence: its byte span and its value as a
// form (thousands separators removed).
type numberSpan struct {
	start, end int
	value      string
}

// numberSpans lists the numbers in sentence in order, dates and times left out.
func numberSpans(sentence string) []numberSpan {
	masked := []byte(sentence)
	for _, span := range dateOrTime.FindAllStringIndex(sentence, -1) {
		for i := span[0]; i < span[1]; i++ {
			masked[i] = ' '
		}
	}
	var out []numberSpan
	for _, match := range numberToken.FindAllStringSubmatchIndex(string(masked), -1) {
		group := 2
		if match[group] < 0 {
			group = 4
		}
		start, end := match[group], match[group+1]
		out = append(out, numberSpan{start: start, end: end, value: strings.ReplaceAll(sentence[start:end], ",", "")})
	}
	return out
}

// attachment reports whether the number numbers[i] is attached to one of the
// measure words: one stands before it with no other number between them, or
// after it in the same clause. It returns the span from the first attached
// word to the number (or from the number to the last attached word after it).
// A claim whose field names no usable word (unworded) attaches to every
// matching number.
func attachment(words [][]int, numbers []numberSpan, i int, breaks [][]int, length int, unworded bool) (from, to int, attached bool) {
	number := numbers[i]
	from, to = number.start, number.end
	if unworded {
		return from, to, true
	}
	previous := 0
	if i > 0 {
		previous = numbers[i-1].end
	}
	_, clauseEnd := clauseAround(breaks, number.start, number.end, length)
	next := clauseEnd
	if i+1 < len(numbers) && numbers[i+1].start < next {
		next = numbers[i+1].start
	}
	for _, word := range words {
		switch {
		case word[0] >= previous && word[1] <= number.start:
			attached = true
			from = min(from, word[0])
		case word[0] >= number.end && word[1] <= next:
			attached = true
			to = max(to, word[1])
		}
	}
	return from, to, attached
}

// clauseAround returns the clause that holds [start, end).
func clauseAround(breaks [][]int, start, end, length int) (from, to int) {
	from, to = 0, length
	for _, brk := range breaks {
		if brk[1] <= start {
			from = brk[1]
		}
		if brk[0] >= end && brk[0] < to {
			to = brk[0]
		}
	}
	return from, to
}

// spansHedge reports whether a hedge word lies inside [from, to).
func spansHedge(hedges [][]int, from, to int) bool {
	for _, hedge := range hedges {
		if hedge[0] >= from && hedge[1] <= to {
			return true
		}
	}
	return false
}

// numberForms is every spelling the synthesis prompt allows prose to give a
// number in: verbatim (a whole value too, such as a score of exactly 0), two
// decimals, and a whole percent for a share. A whole value cannot match an
// unrelated count such as "0 repositories": the number must be attached to a
// word that names what the value measures (measureSentence).
func numberForms(value float64) []string {
	forms := []string{strconv.FormatFloat(value, 'f', 2, 64), strconv.FormatFloat(value, 'f', -1, 64)}
	if value >= 0 && value <= 1 {
		forms = append(forms, strconv.FormatFloat(math.Round(value*100), 'f', 0, 64)+"%")
	}
	return forms
}

// readerSentence is one sentence a reader is given, with how it is measured.
type readerSentence struct {
	text string
	// list is a current_state sentence: a list under a heading.
	list bool
	// narration is a cohort ranking narration summary.
	narration bool
}

func readerSentences(result InvestigationResult) []readerSentence {
	var sentences []readerSentence
	add := func(text string) {
		for _, sentence := range splitSentences(text, true) {
			sentences = append(sentences, readerSentence{text: sentence})
		}
	}
	add(result.DirectJudgment)
	add(result.DeterministicAnswer)
	for _, text := range result.StrongestPressures {
		add(text)
	}
	for _, driver := range result.Drivers {
		add(driver.Title)
		if cohortDriverSummaryShape.MatchString(driver.Summary) {
			// One sentence by construction, whatever periods its labels hold.
			sentences = append(sentences, readerSentence{text: driver.Summary, narration: true})
		} else {
			add(driver.Summary)
		}
		add(driver.Qualification)
	}
	for _, findings := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			add(finding.Summary)
		}
	}
	for _, text := range result.Limitations {
		add(text)
	}
	for _, text := range result.Warnings {
		add(text)
	}
	// current_state is a list the service composes, "label: value; label:
	// value." per clause: a ';' there separates list items, not sentences, so
	// a clause's opening words cover every item in it.
	for _, sentence := range splitSentences(result.CurrentState, false) {
		sentences = append(sentences, readerSentence{text: sentence, list: true})
	}
	return sentences
}

// splitSentences ends a sentence at '.', '!' or '?' (and at ';' when
// semicolons is set) followed by white space or the end of the text, and at a
// line break. A decimal point is followed by a digit, so it never ends a
// sentence.
func splitSentences(text string, semicolons bool) []string {
	var out []string
	start := 0
	runes := []rune(text)
	flush := func(end int) {
		if sentence := strings.TrimSpace(string(runes[start:end])); sentence != "" {
			out = append(out, sentence)
		}
		start = end
	}
	for i, r := range runes {
		end := r == '.' || r == '!' || r == '?' || (semicolons && r == ';')
		switch {
		case r == '\n':
			flush(i + 1)
		case end && (i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' || runes[i+1] == '\t'):
			flush(i + 1)
		}
	}
	flush(len(runes))
	return out
}

// RecordDerivedNumberPhrasing logs one stored answer's measure at Info, on the
// engine's logger. Content safe: the result id and three counts, never a
// sentence. saveResult calls it only for an answer the store accepted, so the
// lines count stored answers.
func (t SlogEngineTelemetry) RecordDerivedNumberPhrasing(ctx context.Context, principal storage.Principal, result InvestigationResult, measure DerivedNumberPhrasing) {
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID), "result_id", SanitizeLogAttr(result.ResultID),
		"synthesis_source", SanitizeLogAttr(string(result.Versions.SynthesisSource)),
		"cf_derived_values", measure.DerivedValues, "cf_derived_number_sentences", measure.Sentences,
		"cf_derived_number_asserted", measure.Asserted,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric derived number phrasing", args...)
}
