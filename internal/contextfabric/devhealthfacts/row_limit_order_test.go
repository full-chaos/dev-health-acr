package devhealthfacts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rowLimitWithoutOrder lists the limited statements that need no top-level
// ORDER BY, keyed by file and the start of the argument: a single aggregate
// row, and an argument the guard cannot read because it is a variable (its
// statement is checked at its own definition).
var rowLimitWithoutOrder = map[string]string{
	"investment.go|investmentWatermarkStatement":        "one aggregate row, nothing to cut",
	"investment_org_mix.go|statement":                   "one aggregate row over the whole organization, nothing to cut",
	"investment_window_span.go|investmentSpanStatement": "one aggregate row, nothing to cut",
	"workitems.go|statement":                            "built above with ORDER BY p.id; the variable cannot be read here",
}

func matchingParen(text string, open int) int {
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// TestEveryRowLimitedStatementOrdersItsRows requires a top-level ORDER BY on
// every statement that is cut by withRowLimit or withRowProbeLimit: a LIMIT
// over an unordered set changes WHICH rows are served, which no later sort
// can repair.
func TestEveryRowLimitedStatementOrdersItsRows(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, marker := range []string{"withRowLimit(", "withRowProbeLimit("} {
			for from := 0; ; {
				at := strings.Index(text[from:], marker)
				if at < 0 {
					break
				}
				at += from
				from = at + len(marker)
				lineStart := strings.LastIndex(text[:at], "\n") + 1
				lineEnd := at + strings.Index(text[at:], "\n")
				line := strings.TrimSpace(text[lineStart:lineEnd])
				if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "func ") {
					continue
				}
				closing := matchingParen(text, at+len(marker)-1)
				if closing < 0 {
					t.Fatalf("%s: unbalanced %s", name, marker)
				}
				body := text[at+len(marker) : closing]
				key := name + "|" + strings.TrimSpace(body)
				if reason, ok := rowLimitWithoutOrder[key]; ok {
					_ = reason
					checked++
					continue
				}
				lastOrder := strings.LastIndex(body, "ORDER BY")
				lastClose := strings.LastIndex(body, "\n)")
				if lastOrder < 0 || lastOrder < lastClose {
					short := body
					if len(short) > 90 {
						short = short[:90]
					}
					t.Errorf("%s: a row-limited statement has no top-level ORDER BY: %q", name, short)
				}
				checked++
			}
		}
	}
	if checked < 30 {
		t.Fatalf("only %d row-limited statements were found, the guard is not reading the source", checked)
	}
}

// TestEveryServedThemeShareIsRounded requires every line that writes a theme
// share field (theme_*, theme_quality_bugfix, prior_theme_*) to go through the
// declared rounding, whichever read (repository, team, project) it is on: a
// share written without it moves in its last digits with the aggregation
// order and changes the client input digest.
func TestEveryServedThemeShareIsRounded(t *testing.T) {
	checked := 0
	for _, name := range []string{"investment.go", "investment_repo_mix.go", "investment_project_rollup_mix.go", "investment_project_mix_phased.go", "investment_project_native_mix.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for number, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if !strings.Contains(line, "] = contextfabric.NumberFactValue(") {
				continue
			}
			if !strings.Contains(line, "FactFieldTheme(") && !strings.Contains(line, "FactFieldThemeQualityBugfix") && !strings.Contains(line, "FactFieldPriorTheme(") {
				continue
			}
			checked++
			if !strings.Contains(line, "roundMixEffort(") {
				t.Errorf("%s:%d writes a theme share without the declared rounding: %s", name, number+1, trimmed)
			}
		}
	}
	if checked < 8 {
		t.Fatalf("only %d theme share writes were found, the guard is not reading the source", checked)
	}
}
