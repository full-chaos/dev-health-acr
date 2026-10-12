package v1

import "testing"

// TestMemberStatusNoMatchRecognizerMatchesOnlyTheGeneratedSentences: the
// zero-match sentence of each kind a status filter applies to is a
// service-authored limitation; a kind with no status filter has none, and a
// changed word is not recognised.
func TestMemberStatusNoMatchRecognizerMatchesOnlyTheGeneratedSentences(t *testing.T) {
	for _, kind := range []string{"pull_request", "project", "incident"} {
		sentence := ContextFabricMemberStatusNoMatchLimitation(kind)
		if sentence == "" || !IsContextFabricMemberStatusLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("%s zero-match sentence %q is not recognised", kind, sentence)
		}
	}
	if got := ContextFabricMemberStatusNoMatchLimitation("team"); got != "" {
		t.Fatalf("team zero-match sentence = %q, want none: a team has no status filter", got)
	}
	for _, lookalike := range []string{
		"No pull requests the read reached hold the status asked for now; that is a count of matches, not a statement about health.",
		"No teams the read reached hold the state asked for now; that is a count of matches, not a statement about health.",
		"No pull requests the read reached hold the state asked for now; that is a statement about health.",
	} {
		if IsContextFabricMemberStatusLimitation(lookalike) {
			t.Fatalf("lookalike %q is recognised", lookalike)
		}
	}
}
