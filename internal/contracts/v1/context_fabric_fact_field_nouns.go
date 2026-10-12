package v1

import (
	"math"
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

// ContextFabricDisplayNumber renders a number for prose: a whole value as an
// integer, any other value to two decimals.
func ContextFabricDisplayNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// ContextFabricDisplayString renders a string fact value for prose: a known
// attribution basis token reads as its phrase; any other value stays as is.
func ContextFabricDisplayString(field, value string) string {
	if field == "attribution_basis" {
		if phrase, ok := contextFabricAttributionBasisPhrases[value]; ok {
			return phrase
		}
	}
	return value
}
