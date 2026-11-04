package v1

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CHAOS-12726: a team is forecast under several work scopes at once, and an
// answer may state only some of them. The sentence says which scopes the
// forecast covers and how many the team has, so one scope is never read as the
// team's. It states no source choice: it only discloses what was used.
//
// EVERY WORD IS SERVED OR FIXED: the team label and the scope name are quoted
// (strconv.Quote); the counts are numbers. ScopeCountAtLeast words a floor.

// ContextFabricForecastScopeLimitation composes the sentence for a team whose
// forecast covers covered of total work scopes. scope names the covered scope
// and is used only when covered is 1. atLeast is true when total is a floor.
// A name longer than ContextFabricForecastScopeNameMaxRunes is cut and marked
// with an ellipsis, so the sentence always fits the limitation bound however
// the quoting escapes it. It returns "" only when the sentence would not be
// true: covered < 1, covered >= total, an empty or untrimmed team label.
func ContextFabricForecastScopeLimitation(team, scope string, covered, total int64, atLeast bool) string {
	if team == "" || strings.TrimSpace(team) != team || strings.TrimSpace(scope) != scope ||
		covered < 1 || covered >= total {
		return ""
	}
	team, scope = clampForecastScopeName(team), clampForecastScopeName(scope)
	var sentence strings.Builder
	sentence.WriteString("This forecast for team ")
	sentence.WriteString(strconv.Quote(team))
	sentence.WriteString(" covers ")
	if covered == 1 && scope != "" {
		sentence.WriteString("work scope " + strconv.Quote(scope) + " only: 1")
	} else {
		sentence.WriteString(strconv.FormatInt(covered, 10))
	}
	if atLeast {
		sentence.WriteString(" of at least ")
	} else {
		sentence.WriteString(" of the team's ")
	}
	sentence.WriteString(strconv.FormatInt(total, 10))
	sentence.WriteString(" work scopes. Left out of this answer: ")
	if atLeast {
		sentence.WriteString("at least ")
	}
	left := total - covered
	sentence.WriteString(strconv.FormatInt(left, 10))
	if left == 1 {
		sentence.WriteString(" work scope.")
	} else {
		sentence.WriteString(" work scopes.")
	}
	if utf8.RuneCountInString(sentence.String()) > ContextFabricLimitationMaxLength {
		return ""
	}
	return sentence.String()
}

// ContextFabricForecastScopeNameMaxRunes bounds each name the sentence quotes.
// Quoting escapes a control character to six runes, so two names of this length
// and the fixed words stay well inside ContextFabricLimitationMaxLength.
const ContextFabricForecastScopeNameMaxRunes = 100

func clampForecastScopeName(name string) string {
	runes := []rune(name)
	if len(runes) <= ContextFabricForecastScopeNameMaxRunes {
		return name
	}
	return string(runes[:ContextFabricForecastScopeNameMaxRunes-1]) + "…"
}

var forecastScopeLimitationPattern = regexp.MustCompile(`^This forecast for team "(?:[^"\\]|\\.)*" covers (?:work scope "(?:[^"\\]|\\.)*" only: 1|[1-9]\d{0,8}) of (?:the team's|at least) [1-9]\d{0,8} work scopes\. Left out of this answer: (?:at least )?[1-9]\d{0,8} work scopes?\.$`)

// IsContextFabricForecastScopeLimitation reports whether a limitation is one
// ContextFabricForecastScopeLimitation composed; it matches the whole sentence.
func IsContextFabricForecastScopeLimitation(limitation string) bool {
	return utf8.RuneCountInString(limitation) <= ContextFabricLimitationMaxLength && forecastScopeLimitationPattern.MatchString(limitation)
}
