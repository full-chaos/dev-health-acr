package contextfabric

import (
	"context"
	"strings"
	"testing"
)

// TestTheFloorEmptiedTerminalReachesTheResultAsTheSubjectNotFoundSentence
// drives Investigate: the marker limitation never leaks, and the result
// carries the sentence naming the terms asked for and the kinds searched.
func TestTheFloorEmptiedTerminalReachesTheResultAsTheSubjectNotFoundSentence(t *testing.T) {
	t.Parallel()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
			ClarificationPrompt: OfferFloorEmptiedClarificationPrompt([]string{"team"}),
		},
		context: emptyGraphContext(),
	}
	interpretation := bootstrapInterpretation()
	interpretation.SubjectTerms = []string{"Phantom", "Squad"}
	engine := buildWindowGateEngine(t, &countingInterpreter{interpretation: interpretation}, graph, newMapResultStore())
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("status = %q, want no_match", result.Status)
	}
	want := noMatchLimitationSubjectNotFound([]string{"Phantom", "Squad"}, "team")
	found := false
	for _, limitation := range result.Limitations {
		if limitation == noMatchLimitationOfferFloorEmptied {
			t.Fatalf("the internal marker limitation leaked into the result: %q", result.Limitations)
		}
		if limitation == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("limitations = %q, want the subject-not-found sentence %q", result.Limitations, want)
	}
	if !strings.Contains(want, `"Phantom Squad"`) || !strings.Contains(want, "among the kinds searched (team)") {
		t.Fatalf("sentence = %q", want)
	}
}

// TestTheOfferFloorTextsArePinned: the prompt carries part of the question's
// name away, the marker names what was not found, and the sentence tells the
// caller what to do next.
func TestTheOfferFloorTextsArePinned(t *testing.T) {
	t.Parallel()
	if got := OfferFloorEmptiedClarificationPrompt([]string{"a", "b"}); got !=
		"No subject matching the name in the question was found; candidates that matched only part of it were not offered. Kinds searched: a, b." {
		t.Fatalf("prompt = %q", got)
	}
	if got := OfferFloorEmptiedClarificationPrompt(nil); !strings.HasSuffix(got, "Kinds searched: none.") {
		t.Fatalf("empty prompt = %q", got)
	}
	if !strings.HasPrefix(noMatchLimitationOfferFloorEmptied, "No subject matching the name in the question was found, so nothing was offered") {
		t.Fatalf("marker text = %q", noMatchLimitationOfferFloorEmptied)
	}
	got := noMatchLimitationSubjectNotFound([]string{"Phantom"}, "team")
	want := `No subject matching "Phantom" was found among the kinds searched (team), so nothing was offered and no canonical facts were read. Candidates that matched only part of the name were not offered. Name the subject you mean, or rephrase the question so it names one.`
	if got != want {
		t.Fatalf("sentence = %q", got)
	}
	blank := noMatchLimitationSubjectNotFound([]string{"", ""}, "team")
	if !strings.HasPrefix(blank, "No subject matching the subject named in the question was found") {
		t.Fatalf("blank terms sentence = %q", blank)
	}
}
