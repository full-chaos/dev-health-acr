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
	if got := ContextFabricDisplayString("rationale", "Compounding Risk score is 0.993773746751932 (severity: high)."); got != "Compounding Risk score is 0.99 (severity: high)." {
		t.Errorf("rationale = %q", got)
	}
	if got := ContextFabricDisplayString("rule_basis_unavailable_reason", "rationale_not_in_rule_sentence_form"); got != "rationale not in rule sentence form" {
		t.Errorf("reason = %q", got)
	}
	if got := ContextFabricDisplayString("rationale", "Window 2026-10-01 to 2026-10-08 had 12 items."); got != "Window 2026-10-01 to 2026-10-08 had 12 items." {
		t.Errorf("untouched rationale = %q", got)
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
	if got := ContextFabricDisplayString("attribution_basis", "unlisted_token"); got != "unlisted_token" {
		t.Errorf("unlisted basis = %q", got)
	}
}
