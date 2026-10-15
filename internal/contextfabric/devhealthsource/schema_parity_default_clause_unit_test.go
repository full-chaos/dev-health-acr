package devhealthsource_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

func TestSplitDeclaredTypeIgnoresClauseWordsInsideQuotes(t *testing.T) {
	cases := []struct {
		name, declared, base, kind, expression string
	}{
		{"plain type", "UInt64", "UInt64", "", ""},
		{"default", "String DEFAULT ''", "String", "DEFAULT", "''"},
		{"materialized", "UInt128 MATERIALIZED toUInt128(x)", "UInt128", "MATERIALIZED", "toUInt128(x)"},
		{"clause word in an enum label, no clause", "Enum8('a MATERIALIZED b' = 1, 'c DEFAULT d' = 2)", "Enum8('a MATERIALIZED b' = 1, 'c DEFAULT d' = 2)", "", ""},
		{"clause word in an enum label, then a clause", "Enum8('x DEFAULT y' = 1) DEFAULT 'x DEFAULT y'", "Enum8('x DEFAULT y' = 1)", "DEFAULT", "'x DEFAULT y'"},
		{"clause word in a quoted expression", "String MATERIALIZED concat('a MATERIALIZED b', x)", "String", "MATERIALIZED", "concat('a MATERIALIZED b', x)"},
		{"doubled quote in a label", "Enum8('it''s DEFAULT' = 1) DEFAULT 'a'", "Enum8('it''s DEFAULT' = 1)", "DEFAULT", "'a'"},
		{"backslash quote in a label", `Enum8('it\'s DEFAULT' = 1) DEFAULT 'a'`, `Enum8('it\'s DEFAULT' = 1)`, "DEFAULT", "'a'"},
		{"clause word in a backtick identifier", "Nullable(String) MATERIALIZED `a DEFAULT b`", "Nullable(String)", "MATERIALIZED", "`a DEFAULT b`"},
	}
	for _, c := range cases {
		base, kind, expression := splitDeclaredType(c.declared)
		if base != c.base || kind != c.kind || expression != c.expression {
			t.Errorf("%s: splitDeclaredType(%q) = (%q, %q, %q), want (%q, %q, %q)", c.name, c.declared, base, kind, expression, c.base, c.kind, c.expression)
		}
	}
}

func TestDefaultExpressionNormalizationKeepsQuotedSpaces(t *testing.T) {
	if normalizeDefaultExpression("concat('a b', x)") == normalizeDefaultExpression("concat('ab', x)") {
		t.Error("a space inside a quoted literal was dropped: concat('a b') compares equal to concat('ab')")
	}
	if normalizeDefaultExpression("toUInt128( toUnixTimestamp64Micro( v ) )") != normalizeDefaultExpression("toUInt128(toUnixTimestamp64Micro(v))") {
		t.Error("spaces outside quotes still count")
	}
	if normalizeDefaultExpression(`concat('it''s a b', 'x y')`) == normalizeDefaultExpression(`concat('it''s ab', 'x y')`) {
		t.Error("a space after a doubled quote was dropped")
	}
	if normalizeDefaultExpression(`concat('it\'s a b')`) == normalizeDefaultExpression(`concat('it\'s ab')`) {
		t.Error("a space after a backslash quote was dropped")
	}
}

func TestDefaultClauseDriftNamesEveryKindOfDisagreement(t *testing.T) {
	cases := []struct {
		name                             string
		declaredKind, declaredExpression string
		liveKind, liveExpression         string
		wantDrift                        bool
	}{
		{"no clause either side", "", "", "", "", false},
		{"declared DEFAULT, live DEFAULT: test seed, not compared", "DEFAULT", "0", "DEFAULT", "1", false},
		{"declared DEFAULT, live nothing: test seed, not compared", "DEFAULT", "0", "", "", false},
		{"declared MATERIALIZED, live the same", "MATERIALIZED", "toUInt128(x)", "MATERIALIZED", "toUInt128(x)", false},
		{"declared MATERIALIZED, only spaces outside quotes differ", "MATERIALIZED", "toUInt128( x )", "MATERIALIZED", "toUInt128(x)", false},
		{"declared MATERIALIZED, expression differs", "MATERIALIZED", "toUInt128(x)", "MATERIALIZED", "toUInt128(y)", true},
		{"declared MATERIALIZED, a space inside a literal differs", "MATERIALIZED", "concat('a b', x)", "MATERIALIZED", "concat('ab', x)", true},
		{"declared MATERIALIZED, live DEFAULT", "MATERIALIZED", "toUInt128(x)", "DEFAULT", "toUInt128(x)", true},
		{"declared MATERIALIZED, live plain", "MATERIALIZED", "toUInt128(x)", "", "", true},
		{"declared plain, live MATERIALIZED", "", "", "MATERIALIZED", "toUInt128(x)", true},
		{"declared DEFAULT, live MATERIALIZED", "DEFAULT", "0", "MATERIALIZED", "toUInt128(x)", true},
	}
	for _, c := range cases {
		drift := defaultClauseDrift(c.declaredKind, c.declaredExpression, c.liveKind, c.liveExpression)
		if (drift != "") != c.wantDrift {
			t.Errorf("%s: drift = %q, want drift %v", c.name, drift, c.wantDrift)
		}
	}
}

// Every declared production type splits and joins back to itself, and carries a
// clause only when its text has one outside quotes.
func TestSplitDeclaredTypeRoundTripsEveryDeclaredColumn(t *testing.T) {
	seen := 0
	for table, columns := range devhealthschema.ProductionColumns {
		for _, column := range columns {
			seen++
			base, kind, expression := splitDeclaredType(column.Type)
			joined := base
			if kind != "" {
				joined = base + " " + kind + " " + expression
			}
			if joined != column.Type {
				t.Errorf("%s.%s: %q splits to (%q, %q, %q) and joins to %q", table, column.Name, column.Type, base, kind, expression, joined)
			}
			if kind != "" && kind != "DEFAULT" && kind != "MATERIALIZED" {
				t.Errorf("%s.%s: kind %q", table, column.Name, kind)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no declared column was checked")
	}
}
