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
		"large whole":             {1e20, "100000000000000000000.00"},
	} {
		if got := ContextFabricDisplayNumber(tc.in); got != tc.want {
			t.Errorf("%s: ContextFabricDisplayNumber(%v) = %q, want %q", name, tc.in, got, tc.want)
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
	if got := ContextFabricDisplayString("attribution_basis", "unlisted_token"); got != "unlisted_token" {
		t.Errorf("unlisted basis = %q", got)
	}
}
