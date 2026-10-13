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

// ContextFabricStringFieldClass says how a declared string fact field may
// appear in a sentence a reader sees.
type ContextFabricStringFieldClass string

const (
	// StringFieldText is prose-safe text (a name, title, id, timestamp or
	// plain word): shown as is.
	StringFieldText ContextFabricStringFieldClass = "text"
	// StringFieldToken is an enumerated or snake_case token: shown as words.
	StringFieldToken ContextFabricStringFieldClass = "token"
	// StringFieldNumericInString carries a number inside the string: its long
	// decimals show at display precision.
	StringFieldNumericInString ContextFabricStringFieldClass = "numeric_in_string"
	// StringFieldNeverInProse is an opaque handle or a text that names wire
	// fields: it stays in structured fields and is left out of sentences.
	StringFieldNeverInProse ContextFabricStringFieldClass = "never_in_prose"
)

var contextFabricStringFieldClasses = func() map[string]ContextFabricStringFieldClass {
	classes := map[string]ContextFabricStringFieldClass{}
	for _, name := range []string{
		"as_of_day", "blocked_by_work_item_id", "blocked_by_work_item_ref", "completed_at", "computed_at", "day",
		"environment", "id", "last_failure_occurred_at", "last_sync_at", "latest_as_of_day", "latest_day",
		"latest_computed_at", "latest_severity", "map_name", "name", "organization_id", "provider", "repository_id",
		"repository_name", "required_child_work_item_id", "required_child_work_item_ref", "rule_basis_severity",
		"rule_computed_at", "rule_id", "rule_version", "scope", "scope_id", "scope_name", "severity", "severity_as_of",
		"status_since", "success_criterion", "team_id", "team_name", "title", "unit_from", "unit_to", "window_end",
		"window_start", "work_scope_id", "work_unit_id",
		"as_of", "latest_sprint_end", "next_sprint_end", "state_source", "target_date", "target_date_provider",
	} {
		classes[name] = StringFieldText
	}
	for _, name := range []string{
		"archived_items", "attribution", "attribution_basis", "basis", "blocker_status_basis", "change_failure_link_tier",
		"change_failure_rate_state", "last_failure_stage", "daily_flow_omitted_reason", "incident_attribution_basis", "investment_mix_source",
		"latest_score_unavailable_reason", "latest_severity_unavailable_reason", "latest_unavailable_reason", "link_tier",
		"member_kind", "mix_source", "oldest_blocked_age_basis", "oldest_blocked_age_unavailable_reason", "outcome", "window", "p50_basis", "p50_unavailable_reason", "population_window", "pull_request_link_basis", "reach_basis",
		"relationship_type", "repository_link_basis", "repository_tables_basis", "rollup_basis",
		"rule_basis_unavailable_reason", "severity_basis", "severity_unavailable_reason", "signal", "source", "state",
		"status", "status_basis", "status_provenance", "status_since_basis", "story_points_completed_unavailable_reason",
		"bug_completed_ratio_unavailable_reason", "theme", "unit_attribution_basis", "unit_kind", "unit_mix_source",
		"window_basis", "window_mode",
		"commitment_status", "items_unavailable_reason", "items_unreadable_reason", "iteration_basis", "project_state", "sprint_kind",
	} {
		classes[name] = StringFieldToken
	}
	classes["rationale"] = StringFieldNumericInString
	for _, name := range []string{"next_cursor", "repository_breakdown_limitation", "unit_unresolved_refs", "unit_weight", "units_limitation"} {
		classes[name] = StringFieldNeverInProse
	}
	return classes
}()

// ContextFabricStringFieldClassOf returns the class of a declared string fact
// field or table column; ok is false for a field no table row classifies.
func ContextFabricStringFieldClassOf(field string) (ContextFabricStringFieldClass, bool) {
	class, ok := contextFabricStringFieldClasses[field]
	return class, ok
}

// ContextFabricDisplayString renders a string fact value for prose. ok is
// false when the field must stay out of sentences.
func ContextFabricDisplayString(field, value string) (text string, ok bool) {
	class, _ := ContextFabricStringFieldClassOf(field)
	switch class {
	case StringFieldNeverInProse:
		return "", false
	case StringFieldNumericInString:
		return contextFabricLongDecimal.ReplaceAllStringFunc(value, func(match string) string {
			number, err := strconv.ParseFloat(match, 64)
			if err != nil {
				return match
			}
			return ContextFabricDisplayNumber(number)
		}), true
	case StringFieldToken:
		if phrase, known := contextFabricAttributionBasisPhrases[value]; known && field == "attribution_basis" {
			return phrase, true
		}
		return strings.ReplaceAll(value, "_", " "), true
	}
	return value, true
}

var contextFabricLongDecimal = regexp.MustCompile(`\b\d+\.\d+\b`)

var contextFabricCohortSignalNouns = map[string]string{
	"investment_mix":                    "investment mix",
	"health.compounding_risk":           "health risk",
	"operational_deficiencies.severity": "operational deficiencies",
	"readiness.coverage_gap":            "readiness gap",
	"workload.forecast_pressure":        "workload pressure",
}

// ContextFabricCohortSignalNoun is the display noun of a cohort ranking
// signal; ok is false for a signal outside the vocabulary.
func ContextFabricCohortSignalNoun(signal string) (string, bool) {
	noun, ok := contextFabricCohortSignalNouns[signal]
	return noun, ok
}
