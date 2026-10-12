package v1

import "testing"

func TestContextFabricDisplayNumber(t *testing.T) {
	t.Parallel()
	for in, want := range map[float64]string{0.993773746751932: "0.99", 0.31907399881: "0.32", 0.98: "0.98", 3: "3", 0: "0", -0.456: "-0.46"} {
		if got := ContextFabricDisplayNumber(in); got != want {
			t.Errorf("ContextFabricDisplayNumber(%v) = %q, want %q", in, got, want)
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
