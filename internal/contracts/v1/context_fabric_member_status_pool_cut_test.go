package v1

import "testing"

// TestMemberStatusPoolCutRecognizerMatchesOnlyTheGeneratedSentences: each
// generated pool cut sentence is a service-authored limitation; a lookalike
// with a pool below the read, a kind with no status filter, or a changed word
// is not.
func TestMemberStatusPoolCutRecognizerMatchesOnlyTheGeneratedSentences(t *testing.T) {
	generated := []string{
		ContextFabricMemberStatusPoolCutLimitation("incident", 25, 0),
		ContextFabricMemberStatusPoolCutLimitation("pull_request", 26, 60),
		ContextFabricMemberStatusPoolCutLimitation("project", 0, 0),
		ContextFabricMemberStatusOutsideSetAtLeastLimitation("project", 2000),
	}
	for _, sentence := range generated {
		if sentence == "" || !IsContextFabricMemberStatusLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("generated sentence %q is not recognised", sentence)
		}
	}
	if got := ContextFabricMemberStatusPoolCutLimitation("team", 3, 0); got != "" {
		t.Fatalf("team pool cut = %q, want none: a team has no status filter", got)
	}
	for _, lookalike := range []string{
		"The state filter ran within the first 26 of 20 pull requests the read reached; pull requests past that bound were not checked.",
		"The status filter ran within the first 26 pull requests the read reached; pull requests past that bound were not checked.",
		"The state filter ran within the first 25 incidents the read reached; incidents past that bound were not checked.",
		"The status filter ran within the first 25 incidents the read reached; incidents past that bound were probably fine.",
		"At least 2000 projects have a status outside the known set and were not matched.",
		"At least 0 projects have a state outside the known set and were not matched.",
	} {
		if IsContextFabricMemberStatusLimitation(lookalike) {
			t.Fatalf("lookalike %q is recognised", lookalike)
		}
	}
}
