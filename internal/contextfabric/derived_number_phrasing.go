package contextfabric

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/observability"
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

// numberToken matches a number as prose writes it: digits with optional
// thousands separators and decimals, and an optional percent sign.
var numberToken = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?%?`)

// DerivedNumberPhrasing is one answer's measure.
type DerivedNumberPhrasing struct {
	// DerivedValues is how many claimed facts state a derived number.
	DerivedValues int
	// Sentences is how many reader-facing sentences state one of them.
	Sentences int
	// Asserted is how many of those carry no appears, leans or suggests.
	Asserted int
}

// MeasureDerivedNumberPhrasing measures one answer. A sentence states a derived
// number when it carries one of the answer's derived claimed-fact values as the
// prose writes a number: the value verbatim, rounded to two decimals, or, for a
// share between 0 and 1, as a whole percent. The prose read is everything a
// reader is given to read: the lead (direct_judgment and deterministic_answer),
// strongest_pressures, each driver's title, summary and qualification, each
// finding's summary, limitations and warnings. current_state is a label: value
// list the service composes, not phrasing, and is not read.
func MeasureDerivedNumberPhrasing(result InvestigationResult) DerivedNumberPhrasing {
	forms := map[string]struct{}{}
	var measure DerivedNumberPhrasing
	for _, claim := range result.ClaimedFacts {
		_, derived := DerivedNumberField(claim.Field)
		if len(claim.DerivedFrom) > 0 {
			derived = true
		}
		if !derived {
			continue
		}
		var claimForms []string
		switch number, integer := claim.Value.Number, claim.Value.Integer; {
		case number != nil && !math.IsNaN(*number) && !math.IsInf(*number, 0):
			claimForms = numberForms(*number)
		case integer != nil:
			claimForms = []string{strconv.FormatInt(*integer, 10)}
		default:
			continue
		}
		measure.DerivedValues++
		for _, form := range claimForms {
			forms[form] = struct{}{}
		}
	}
	if len(forms) == 0 {
		return measure
	}
	for _, sentence := range readerSentences(result) {
		if !statesAnyForm(sentence, forms) {
			continue
		}
		measure.Sentences++
		if !hedgeWord.MatchString(sentence) {
			measure.Asserted++
		}
	}
	return measure
}

// numberForms is every spelling the synthesis prompt allows prose to give a
// fractional number in: verbatim, two decimals, and a whole percent for a share.
// A bare whole number is left out: a score or share of exactly 0 or 1 would
// otherwise match every count of 0 or 1 in the prose.
func numberForms(value float64) []string {
	forms := []string{strconv.FormatFloat(value, 'f', 2, 64)}
	if verbatim := strconv.FormatFloat(value, 'f', -1, 64); strings.Contains(verbatim, ".") {
		forms = append(forms, verbatim)
	}
	if value >= 0 && value <= 1 {
		forms = append(forms, strconv.FormatFloat(math.Round(value*100), 'f', 0, 64)+"%")
	}
	return forms
}

func statesAnyForm(sentence string, forms map[string]struct{}) bool {
	for _, token := range numberToken.FindAllString(sentence, -1) {
		if _, ok := forms[strings.ReplaceAll(token, ",", "")]; ok {
			return true
		}
	}
	return false
}

func readerSentences(result InvestigationResult) []string {
	texts := []string{result.DirectJudgment, result.DeterministicAnswer}
	texts = append(texts, result.StrongestPressures...)
	for _, driver := range result.Drivers {
		texts = append(texts, driver.Title, driver.Summary, driver.Qualification)
	}
	for _, findings := range [][]Finding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			texts = append(texts, finding.Summary)
		}
	}
	texts = append(texts, result.Limitations...)
	texts = append(texts, result.Warnings...)
	var sentences []string
	for _, text := range texts {
		sentences = append(sentences, splitSentences(text)...)
	}
	return sentences
}

// splitSentences ends a sentence at '.', '!', '?' or ';' followed by white
// space or the end of the text, and at a line break. A decimal point is
// followed by a digit, so it never ends a sentence.
func splitSentences(text string) []string {
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
		switch r {
		case '\n':
			flush(i + 1)
		case '.', '!', '?', ';':
			if i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' || runes[i+1] == '\t' {
				flush(i + 1)
			}
		}
	}
	flush(len(runes))
	return out
}

// recordDerivedNumberPhrasing logs one saved answer's measure at Info. Content
// safe: the result id and three counts, never a sentence.
func recordDerivedNumberPhrasing(ctx context.Context, principal storage.Principal, result InvestigationResult) {
	measure := MeasureDerivedNumberPhrasing(result)
	requestID, _ := observability.RequestIDFromContext(ctx)
	slog.Default().InfoContext(ctx, "context fabric derived number phrasing",
		"org_id", SanitizeLogAttr(principal.OrgID), "result_id", SanitizeLogAttr(result.ResultID),
		"request_id", SanitizeLogAttr(string(requestID)),
		"synthesis_source", SanitizeLogAttr(string(result.Versions.SynthesisSource)),
		"cf_derived_values", measure.DerivedValues, "cf_derived_number_sentences", measure.Sentences,
		"cf_derived_number_asserted", measure.Asserted)
}
