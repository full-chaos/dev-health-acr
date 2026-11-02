package contextfabric

import (
	"strings"
	"testing"
)

// CHAOS-12524: a clarification over a withheld pool has no candidate on the
// turn, so a sentence that says one subject matched and can be confirmed is
// false. Rows H2 (an explicit handle the question does not name), M1 (two
// handles) and Y1 (an observed-time axis) all reached this arm with 0
// candidates and the exclusion prompt, and kept their handle option, so the
// clarification stands and only its sentence changes.
func TestAWithheldPoolClarificationClaimsNoMatchedSubject(t *testing.T) {
	t.Parallel()
	for _, frame := range []struct {
		name  string
		frame *QuestionFrame
	}{
		{"no frame (two handles, no round)", nil},
		{"a named frame with no kind (observed-time axis)", heldFrame_(comparisonFrame(comparisonNamedOperand("platform", SubjectTeam)))},
	} {
		t.Run(frame.name, func(t *testing.T) {
			t.Parallel()
			resolution := heldResolution(OfferPoolEmptiedClarificationPrompt, 0)
			request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
			status, limitation := resolveTerminalStatus(request, &resolution, frame.frame, true, declaredKindDecision{}, SubjectSubstitutionNotEvaluated, OfferFloorOutcome{})
			if status != InvestigationClarificationRequired {
				t.Fatalf("status = %q, want the clarification: the turn still has an offer to redeem", status)
			}
			if strings.Contains(limitation, "One authorized subject matched") || limitation == clarificationRequiredLimitationOne {
				t.Fatalf("limitation = %q: no candidate is on the turn, so no subject matched that could be confirmed", limitation)
			}
			if limitation != clarificationRequiredLimitationWithheldPool {
				t.Fatalf("limitation = %q, want the withheld-pool clarification sentence", limitation)
			}
		})
	}
}

// The clarification sentence a withheld pool now carries says what happened to
// the pool, as its terminal sibling does, and points at the offers. It names no
// subject and no term: every candidate was withheld.
func TestTheWithheldPoolClarificationSentenceSaysWhatHappened(t *testing.T) {
	t.Parallel()
	if !strings.HasPrefix(clarificationRequiredLimitationWithheldPool, "Retrieval matched one or more subjects only by semantic similarity") {
		t.Fatalf("sentence %q does not say the pool was withheld", clarificationRequiredLimitationWithheldPool)
	}
	if !strings.Contains(clarificationRequiredLimitationWithheldPool, "offered options") {
		t.Fatalf("sentence %q does not point at the offers the clarification exists for", clarificationRequiredLimitationWithheldPool)
	}
	if clarificationRequiredLimitationWithheldPool == noMatchLimitationOfferPoolEmptied {
		t.Fatal("the clarification and the terminal must stay distinct: two arms with one status are told apart by their prose")
	}
}

// A stored row written before this change carries the old sentence on the
// withheld-pool arm. Both repairs must still recognise the new one, so a row
// written after it repairs the same way.
func TestTheRepairsRecogniseTheWithheldPoolClarification(t *testing.T) {
	t.Parallel()
	stored := InvestigationResult{
		Status:              InvestigationClarificationRequired,
		SubjectResolution:   withheldPoolResolution(),
		Limitations:         []string{clarificationRequiredLimitationWithheldPool},
		DeterministicAnswer: "Clarification is required before this question can be answered.",
	}
	if !RepairLegacyUnanswerableClarification(&stored) {
		t.Fatal("the offer-less repair did not fire on a withheld-pool clarification")
	}
	for _, limitation := range stored.Limitations {
		if limitation == clarificationRequiredLimitationWithheldPool {
			t.Fatal("the repaired row still carries the clarification sentence")
		}
	}
}

// The declared-kind repair replaces the clarification sentence in place. It
// must replace the withheld-pool sentence too, or a repaired row would carry a
// clarification sentence beside a no_match status.
func TestTheDeclaredKindRepairReplacesTheWithheldPoolClarification(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	served := storedClarification(storedCIRunCandidates()...)
	served.Limitations = []string{"A retrieval mechanism was unavailable.", clarificationRequiredLimitationWithheldPool}
	served.SubjectResolution.ClarificationPrompt = OfferPoolEmptiedClarificationPrompt
	got := RepairStoredClarification(&served, named, SemanticStateReadAvailable)
	if got.Determination != StoredAnswerabilityUnanswerable || !got.Repaired {
		t.Fatalf("determination/repaired = %q/%v, want unanswerable/true", got.Determination, got.Repaired)
	}
	if len(served.Limitations) != 2 || served.Limitations[1] != declaredKindTerminalLimitation {
		t.Fatalf("served limitations = %#v, want the withheld-pool sentence replaced in place", served.Limitations)
	}
}
