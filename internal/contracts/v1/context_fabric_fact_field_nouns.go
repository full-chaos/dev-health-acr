package v1

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var contextFabricFactFieldNounOverrides = map[string]string{
	"compounding_risk":  "compounding risk score",
	"coverage_gap":      "coverage gap score",
	"forecast_pressure": "forecast pressure score",
	"theme_quality":     "quality theme share",
	"attribution_basis": "attribution basis",
}

var contextFabricAttributionBasisPhrases = map[string]string{
	"pr_ref_share_partition":                          "share of pull request references",
	"pr_ref_share_partition_over_team_repo_ownership": "share of pull request references over the team's owned repositories",
	"pr_ref_share_partition_over_all_repositories":    "share of pull request references over all repositories",
}

// ContextFabricFactFieldNoun is the display noun for a fact field in served
// prose: the wire name never reaches a sentence a reader sees.
func ContextFabricFactFieldNoun(field string) string {
	if noun, ok := contextFabricFactFieldNounOverrides[field]; ok {
		return noun
	}
	return strings.ReplaceAll(field, "_", " ")
}

// ContextFabricDisplayNumberUnavailable is what prose says for a number that
// is not a finite value; the raw value stays in the structured fields.
const ContextFabricDisplayNumberUnavailable = "not available"

// ContextFabricDisplayNumber renders a number for prose: a whole value as an
// integer, any other value to two decimals. NaN and infinities never print as
// numbers, and a value that rounds to zero prints without a sign.
func ContextFabricDisplayNumber(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return ContextFabricDisplayNumberUnavailable
	}
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v+0, 'f', 0, 64)
	}
	text := strconv.FormatFloat(v, 'f', 2, 64)
	if text == "-0.00" {
		return "0.00"
	}
	return text
}

// ContextFabricDisplayString renders a string fact value for prose: a known
// attribution basis token reads as its phrase, a rationale shows its long
// decimals at display precision, a reason token reads as words; any other
// value stays as is.
func ContextFabricDisplayString(field, value string) string {
	switch {
	case field == "attribution_basis":
		if phrase, ok := contextFabricAttributionBasisPhrases[value]; ok {
			return phrase
		}
	case field == "rationale":
		return contextFabricLongDecimal.ReplaceAllStringFunc(value, func(match string) string {
			number, err := strconv.ParseFloat(match, 64)
			if err != nil {
				return match
			}
			return ContextFabricDisplayNumber(number)
		})
	case strings.HasSuffix(field, "_reason"):
		return strings.ReplaceAll(value, "_", " ")
	}
	return value
}

var contextFabricLongDecimal = regexp.MustCompile(`\b\d+\.\d{3,}\b`)
