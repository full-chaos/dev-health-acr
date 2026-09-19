package contextfabric

import (
	"strings"
	"testing"
)

// TestTheFloorEmptiedTerminalIsANoMatchWhateverElseIsRedeemable: a pool whose
// every candidate was withheld for matching too weakly is a no_match for a
// caller that can clarify, whether or not some unrelated channel (a window
// option) could be redeemed -- a window answer cannot identify the subject.
func TestTheFloorEmptiedTerminalIsANoMatchWhateverElseIsRedeemable(t *testing.T) {
	t.Parallel()
	for _, redeemable := range []bool{false, true} {
		resolution := SubjectResolution{
			Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
			ClarificationPrompt: OfferFloorEmptiedClarificationPrompt([]string{"ci_pipeline_run", "team"}),
		}
		request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
		status, limitation := resolveTerminalStatus(request, &resolution, nil, redeemable, declaredKindDecision{}, SubjectSubstitutionNotEvaluated)
		if status != InvestigationNoMatch || limitation != noMatchLimitationOfferFloorEmptied {
			t.Fatalf("redeemable=%v: status=%q limitation=%q", redeemable, status, limitation)
		}
	}
}

// TestTheFloorEmptiedPromptRoundTripsItsKinds: the prompt is the typed
// carrier, so the kinds it names come back out of it exactly.
func TestTheFloorEmptiedPromptRoundTripsItsKinds(t *testing.T) {
	t.Parallel()
	for _, kinds := range [][]string{nil, {"team"}, {"ci_pipeline_run", "team"}} {
		got, ok := offerFloorEmptiedKinds(OfferFloorEmptiedClarificationPrompt(kinds))
		if !ok {
			t.Fatalf("kinds %v: prompt not recognised", kinds)
		}
		want := "none"
		if len(kinds) > 0 {
			want = strings.Join(kinds, ", ")
		}
		if got != want {
			t.Fatalf("kinds %v: got %q, want %q", kinds, got, want)
		}
	}
	if _, ok := offerFloorEmptiedKinds(OfferPoolEmptiedClarificationPrompt); ok {
		t.Fatal("the vector-only emptied prompt was read as the floor-emptied one")
	}
}

// TestTheSubjectNotFoundSentenceNamesWhatWasSearched: the named subject and
// the kinds searched are in the sentence, and it says nothing was offered.
func TestTheSubjectNotFoundSentenceNamesWhatWasSearched(t *testing.T) {
	t.Parallel()
	got := noMatchLimitationSubjectNotFound([]string{"Phantom", "Squad"}, "ci_pipeline_run, team")
	for _, want := range []string{`"Phantom Squad"`, "ci_pipeline_run, team", "nothing was offered"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sentence %q lacks %q", got, want)
		}
	}
	if fallback := noMatchLimitationSubjectNotFound(nil, "team"); !strings.Contains(fallback, "the subject named in the question") {
		t.Fatalf("fallback sentence = %q", fallback)
	}
}
