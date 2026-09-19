package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestTheFloorOutcomeIsANoMatchWhateverElseIsRedeemable: a pool whose every
// candidate was withheld for matching too weakly is a no_match, whether or not
// some unrelated channel (a window option) could be redeemed and whether or
// not the caller accepts a clarification -- a window answer cannot identify
// the subject.
func TestTheFloorOutcomeIsANoMatchWhateverElseIsRedeemable(t *testing.T) {
	t.Parallel()
	floor := OfferFloorOutcome{Refused: true, SearchedKinds: []string{"ci_pipeline_run", "team"}}
	for _, allow := range []bool{true, false} {
		for _, redeemable := range []bool{false, true} {
			resolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
			request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: allow}}
			status, limitation := resolveTerminalStatus(request, &resolution, nil, redeemable, declaredKindDecision{}, SubjectSubstitutionNotEvaluated, floor)
			if status != InvestigationNoMatch || limitation != noMatchLimitationOfferFloorEmptied {
				t.Fatalf("allow=%v redeemable=%v: status=%q limitation=%q", allow, redeemable, status, limitation)
			}
		}
	}
}

// TestPromptTextNeverStandsInForTheFloorOutcome: only the typed outcome
// selects the subject-not-found terminal; a prompt that happens to read like
// it does not.
func TestPromptTextNeverStandsInForTheFloorOutcome(t *testing.T) {
	t.Parallel()
	resolution := SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
		ClarificationPrompt: "No subject matching the name in the question was found; candidates that matched only part of it were not offered. Kinds searched: not-a-real-search-kind.",
	}
	request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
	_, limitation := resolveTerminalStatus(request, &resolution, nil, false, declaredKindDecision{}, SubjectSubstitutionNotEvaluated, OfferFloorOutcome{})
	if limitation == noMatchLimitationOfferFloorEmptied {
		t.Fatalf("prompt text was classified as the typed floor outcome: %q", limitation)
	}
}

// TestTheSubjectNotFoundSentenceNamesWhatWasSearched: the named subject and
// the kinds searched are in the sentence, and it says nothing was offered.
func TestTheSubjectNotFoundSentenceNamesWhatWasSearched(t *testing.T) {
	t.Parallel()
	got := noMatchLimitationSubjectNotFound([]string{"Phantom", "Squad"}, []string{"ci_pipeline_run", "team"})
	for _, want := range []string{`"Phantom Squad"`, "ci_pipeline_run, team", "nothing was offered"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sentence %q lacks %q", got, want)
		}
	}
	if fallback := noMatchLimitationSubjectNotFound(nil, nil); !strings.Contains(fallback, "the subject named in the question") || !strings.Contains(fallback, "(none)") {
		t.Fatalf("fallback sentence = %q", fallback)
	}
}

// TestAnOfferMergedInAfterTheFloorEndsTheFloorOutcome: the terminal acts on
// the floor outcome only while the material still has nothing to redeem.
func TestAnOfferMergedInAfterTheFloorEndsTheFloorOutcome(t *testing.T) {
	t.Parallel()
	floor := OfferFloorOutcome{Refused: true, SearchedKinds: []string{"team"}}
	if got := effectiveSubjectFloor(StructureOfferMaterial{SubjectFloor: floor}); !got.Refused || len(got.SearchedKinds) != 1 {
		t.Fatalf("empty material: %+v", got)
	}
	withKind := StructureOfferMaterial{SubjectFloor: floor, KindOptions: make([]contractsv1.ContextFabricKindOption, 1)}
	if got := effectiveSubjectFloor(withKind); got.Refused {
		t.Fatalf("material with an offer: %+v", got)
	}
}
