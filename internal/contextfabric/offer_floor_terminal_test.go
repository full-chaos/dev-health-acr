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

// TestTheSubjectNotFoundSentenceNamesTheRequestedKindAndTheClosestMatches: the
// named subject, the requested kind and the closest-match kinds are in the
// sentence, and it says nothing was offered.
func TestTheSubjectNotFoundSentenceNamesTheRequestedKindAndTheClosestMatches(t *testing.T) {
	t.Parallel()
	got := noMatchLimitationSubjectNotFound([]string{"Phantom", "Squad"}, SubjectProject, []string{"ci_pipeline_run", "team"})
	for _, want := range []string{`No project matched "Phantom Squad"; the closest matches were CI pipeline runs, teams.`, "Nothing was offered"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sentence %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "searched") {
		t.Fatalf("sentence %q claims a search scope", got)
	}
	if got := noMatchLimitationSubjectNotFound([]string{"Phantom"}, "", []string{"team"}); !strings.HasPrefix(got, `No subject matched "Phantom"; the closest matches were teams.`) {
		t.Fatalf("no requested kind: %q", got)
	}
	if got := noMatchLimitationSubjectNotFound([]string{"Phantom"}, SubjectProject, nil); !strings.HasPrefix(got, `No project matched "Phantom". Nothing was offered`) {
		t.Fatalf("no near misses: %q", got)
	}
	if got := noMatchLimitationSubjectNotFound([]string{"Phantom"}, "not_a_kind", nil); !strings.HasPrefix(got, `No subject matched "Phantom".`) {
		t.Fatalf("unrecognized kind: %q", got)
	}
	if fallback := noMatchLimitationSubjectNotFound(nil, "", nil); !strings.HasPrefix(fallback, "No subject matched the name in the question.") {
		t.Fatalf("fallback sentence = %q", fallback)
	}
}

// TestTheRequestedKindComesFromTheWinningReadingElseTheSingleExpectedKind.
func TestTheRequestedKindComesFromTheWinningReadingElseTheSingleExpectedKind(t *testing.T) {
	t.Parallel()
	var outcome QuestionFamilyOutcome
	outcome.WinningSample.RequestedKind = SubjectProject
	if got := noMatchRequestedKind(InvestigationRequest{ExpectedKinds: []SubjectKind{SubjectTeam}}, outcome); got != SubjectProject {
		t.Fatalf("winning reading: %q", got)
	}
	if got := noMatchRequestedKind(InvestigationRequest{ExpectedKinds: []SubjectKind{SubjectTeam}}, QuestionFamilyOutcome{}); got != SubjectTeam {
		t.Fatalf("single expected kind: %q", got)
	}
	if got := noMatchRequestedKind(InvestigationRequest{ExpectedKinds: []SubjectKind{SubjectTeam, SubjectProject}}, QuestionFamilyOutcome{}); got != "" {
		t.Fatalf("two expected kinds: %q", got)
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
