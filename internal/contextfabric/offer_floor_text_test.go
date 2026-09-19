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
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		material:   StructureOfferMaterial{SubjectFloor: OfferFloorOutcome{Refused: true, SearchedKinds: []string{"team"}}},
		context:    emptyGraphContext(),
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
	want := noMatchLimitationSubjectNotFound([]string{"Phantom", "Squad"}, []string{"team"})
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

// TestTheOfferFloorTextsArePinned: the marker names what was not found and the
// sentence tells the caller what to do next.
func TestTheOfferFloorTextsArePinned(t *testing.T) {
	t.Parallel()
	if !strings.HasPrefix(noMatchLimitationOfferFloorEmptied, "No subject matching the name in the question was found, so nothing was offered") {
		t.Fatalf("marker text = %q", noMatchLimitationOfferFloorEmptied)
	}
	got := noMatchLimitationSubjectNotFound([]string{"Phantom"}, []string{"team"})
	want := `No subject matching "Phantom" was found among the kinds searched (team), so nothing was offered and no canonical facts were read. Candidates that matched only part of the name were not offered. Name the subject you mean, or rephrase the question so it names one.`
	if got != want {
		t.Fatalf("sentence = %q", got)
	}
	blank := noMatchLimitationSubjectNotFound([]string{"", ""}, []string{"team"})
	if !strings.HasPrefix(blank, "No subject matching the subject named in the question was found") {
		t.Fatalf("blank terms sentence = %q", blank)
	}
	if kindsSearchedText(nil) != "none" || kindsSearchedText([]string{"a", "b"}) != "a, b" {
		t.Fatal("kindsSearchedText")
	}
}
