package v1

import (
	"math"
	"testing"
)

func TestContextFabricDisplayNumber(t *testing.T) {
	t.Parallel()
	for in, want := range map[float64]string{0.993773746751932: "0.99", 0.31907399881: "0.32", 0.98: "0.98", 3: "3", 0: "0", -0.456: "-0.46"} {
		if got := ContextFabricDisplayNumber(in); got != want {
			t.Errorf("ContextFabricDisplayNumber(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestContextFabricDisplayNumberNeverPrintsNonFiniteOrNegativeZero(t *testing.T) {
	t.Parallel()
	negZero := math.Copysign(0, -1)
	for name, tc := range map[string]struct {
		in   float64
		want string
	}{
		"nan":                     {math.NaN(), ContextFabricDisplayNumberUnavailable},
		"positive inf":            {math.Inf(1), ContextFabricDisplayNumberUnavailable},
		"negative inf":            {math.Inf(-1), ContextFabricDisplayNumberUnavailable},
		"negative zero":           {negZero, "0"},
		"rounds to negative zero": {-0.001, "0.00"},
		"large whole":             {1e20, "100000000000000000000"},
	} {
		if got := ContextFabricDisplayNumber(tc.in); got != tc.want {
			t.Errorf("%s: ContextFabricDisplayNumber(%v) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

func TestContextFabricDisplayStringKeepsStructuredTokensOutOfProse(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		field, in, want string
		shown           bool
	}{
		"rationale decimals":  {"rationale", "Compounding Risk score is 0.993773746751932 (severity: high).", "Compounding Risk score is 0.99 (severity: high).", true},
		"reason token":        {"rule_basis_unavailable_reason", "rationale_not_in_rule_sentence_form", "rationale not in rule sentence form", true},
		"untouched rationale": {"rationale", "Window 2026-10-01 to 2026-10-08 had 12 items.", "Window 2026-10-01 to 2026-10-08 had 12 items.", true},
		"basis phrase":        {"attribution_basis", "pr_ref_share_partition", "share of pull request references", true},
		"text as is":          {"title", "Carry_the signal", "Carry_the signal", true},
		"cursor never":        {"next_cursor", "abc", "", false},
		"unclassified as is":  {"zz_unclassified", "a_b", "a_b", true},
	} {
		got, shown := ContextFabricDisplayString(tc.field, tc.in)
		if got != tc.want || shown != tc.shown {
			t.Errorf("%s: ContextFabricDisplayString(%q, %q) = %q, %v; want %q, %v", name, tc.field, tc.in, got, shown, tc.want, tc.shown)
		}
	}
}

func TestContextFabricFactFieldNounHasNoUnderscoreAndKeepsOverrides(t *testing.T) {
	t.Parallel()
	for field := range contextFabricFactFieldNounOverrides {
		noun := ContextFabricFactFieldNoun(field)
		if noun == "" || noun == field {
			t.Errorf("noun for %q = %q", field, noun)
		}
	}
	if got := ContextFabricFactFieldNoun("organization_id"); got != "organization id" {
		t.Errorf("fallback noun = %q", got)
	}
}
