package devhealthsource_test

import (
	"fmt"
	"strings"
)

// outsideQuotes reports, for every byte of text, whether it lies outside a
// quoted literal or identifier. A quote opens at ', " or ` and closes at the
// same character; inside a single-quoted literal a doubled quote and a
// backslash escape stay inside it. The quote characters themselves count as
// inside.
func outsideQuotes(text string) []bool {
	outside := make([]bool, len(text))
	var open byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if open == 0 {
			if c == '\'' || c == '"' || c == '`' {
				open = c
				continue
			}
			outside[i] = true
			continue
		}
		switch {
		case c == '\\' && open == '\'' && i+1 < len(text):
			i++
		case c == open && i+1 < len(text) && text[i+1] == open:
			i++
		case c == open:
			open = 0
		}
	}
	return outside
}

// splitDeclaredType separates a declared type from its trailing DEFAULT or
// MATERIALIZED clause. A clause word inside a quoted label or literal is not
// the clause.
func splitDeclaredType(declared string) (base, kind, expression string) {
	outside := outsideQuotes(declared)
	for i := range declared {
		if !outside[i] {
			continue
		}
		for _, marker := range []string{" MATERIALIZED ", " DEFAULT "} {
			if strings.HasPrefix(declared[i:], marker) {
				return declared[:i], strings.TrimSpace(marker), declared[i+len(marker):]
			}
		}
	}
	return declared, "", ""
}

// normalizeDefaultExpression is the form two default expressions are compared
// in: spaces outside quoted literals are dropped, spaces inside them count.
func normalizeDefaultExpression(expression string) string {
	outside := outsideQuotes(expression)
	var b strings.Builder
	for i := 0; i < len(expression); i++ {
		if expression[i] == ' ' && outside[i] {
			continue
		}
		b.WriteByte(expression[i])
	}
	return b.String()
}

// defaultClauseDrift compares a declared DEFAULT or MATERIALIZED clause with
// the live column's default_kind and default_expression. It returns "" when
// they agree and otherwise a sentence naming the drift.
func defaultClauseDrift(declaredKind, declaredExpression, liveKind, liveExpression string) string {
	switch {
	case declaredKind == "MATERIALIZED":
		if liveKind != declaredKind || normalizeDefaultExpression(liveExpression) != normalizeDefaultExpression(declaredExpression) {
			return fmt.Sprintf("is %s %q but devhealthschema declares %s %q", liveKind, liveExpression, declaredKind, declaredExpression)
		}
	case liveKind == "MATERIALIZED":
		// The reverse drift: acr DDL DEFAULTs are test seed and are never
		// compared, but a live MATERIALIZED column is the production contract
		// and a declaration that does not say so is stale.
		declared := declaredKind
		if declared == "" {
			declared = "a plain column"
		}
		return fmt.Sprintf("is MATERIALIZED %q but devhealthschema declares %s", liveExpression, declared)
	}
	return ""
}
