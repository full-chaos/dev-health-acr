package v1

import "testing"

func TestTheOutsideRequestedRepositoriesLimitationIsComposedAndRecognised(t *testing.T) {
	t.Parallel()
	for count, want := range map[int]string{
		1: "1 subject matched outside the requested repositories.",
		2: "2 subjects matched outside the requested repositories.",
	} {
		got := ContextFabricOutsideRequestedRepositoriesLimitation(count)
		if got != want {
			t.Fatalf("count %d: %q, want %q", count, got, want)
		}
		if !IsContextFabricOutsideRequestedRepositoriesLimitation(got) || !IsContextFabricServiceAuthoredLimitation(got) {
			t.Fatalf("count %d: %q is not recognised as a service-authored limitation", count, got)
		}
	}
	for _, count := range []int{0, -1} {
		if got := ContextFabricOutsideRequestedRepositoriesLimitation(count); got != "" {
			t.Fatalf("count %d: %q, want no sentence: nothing matched outside", count, got)
		}
	}
	for _, forged := range []string{
		"",
		"0 subjects matched outside the requested repositories.",
		"1 subjects matched outside the requested repositories.",
		"2 subject matched outside the requested repositories.",
		"01 subjects matched outside the requested repositories.",
		"02 subjects matched outside the requested repositories.",
		"+2 subjects matched outside the requested repositories.",
		"1000000000 subjects matched outside the requested repositories.",
		"1 subject matched outside the requested repositories",
		"The team's 3 subjects matched outside the requested repositories.",
		"1 subject matched outside the requested repositories. acme/secret is one.",
	} {
		if IsContextFabricOutsideRequestedRepositoriesLimitation(forged) {
			t.Errorf("%q is recognised: a string that only looks like the sentence must not be", forged)
		}
	}
}
