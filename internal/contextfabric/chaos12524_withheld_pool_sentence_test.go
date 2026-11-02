package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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

// The clarification sentence a withheld pool carries must be true of every pool
// that reaches the arm (codex r1 on #1922, executed): the offer floor withholds a
// lexical candidate too, so the sentence names no match mechanism, and a window
// option can be the only offer, so it does not promise that a subject is chosen
// from the options. It names no subject and no term: every candidate was withheld.
func TestTheWithheldPoolClarificationSentenceSaysWhatHappened(t *testing.T) {
	t.Parallel()
	for _, claim := range []string{"semantic", "similarity", "One authorized subject", "matched", "chosen from the offered options"} {
		if strings.Contains(clarificationRequiredLimitationWithheldPool, claim) {
			t.Fatalf("sentence %q claims %q, which is false for a lexical pool or a window-only offer", clarificationRequiredLimitationWithheldPool, claim)
		}
	}
	if !strings.Contains(clarificationRequiredLimitationWithheldPool, "offered option") {
		t.Fatalf("sentence %q does not point at the offers the clarification exists for", clarificationRequiredLimitationWithheldPool)
	}
	if len(clarificationRequiredLimitationWithheldPool) > len(clarificationRequiredLimitationOne) {
		t.Fatalf("sentence is %d bytes, longer than the %d-byte one it restates on stored rows: a near-limit row could cross the byte cap", len(clarificationRequiredLimitationWithheldPool), len(clarificationRequiredLimitationOne))
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

// A row stored before this change carries the old sentence on a clarification
// with no candidate, and a row with an offer to redeem is answerable, so neither
// repair runs on it (codex r1 on #1922, executed: served unchanged). The served
// copy is restated in place; a row with a candidate keeps its sentence.
func TestAStoredWithheldPoolClarificationIsRestatedOnRead(t *testing.T) {
	t.Parallel()
	stored := InvestigationResult{
		Status:              InvestigationClarificationRequired,
		SubjectResolution:   SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, ClarificationPrompt: OfferPoolEmptiedClarificationPrompt},
		Limitations:         []string{"A retrieval mechanism was unavailable.", clarificationRequiredLimitationOne},
		DeterministicAnswer: "Clarification is required before this question can be answered.",
		StructureNeeds:      &contractsv1.ContextFabricStructureNeeds{WindowOptions: []contractsv1.ContextFabricWindowOption{{}}},
	}
	if !resultOffersRedeemable(stored) {
		t.Fatal("fixture: the row must carry an offer to redeem, or the offer-less repair answers for it")
	}
	RepairStoredClarification(&stored, nil, SemanticStateReadAvailable)
	if len(stored.Limitations) != 2 || stored.Limitations[1] != clarificationRequiredLimitationWithheldPool {
		t.Fatalf("limitations = %#v, want the old sentence restated in place", stored.Limitations)
	}
	withCandidate := InvestigationResult{
		Status:            InvestigationClarificationRequired,
		SubjectResolution: SubjectResolution{Candidates: []SubjectCandidate{{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "A"}}}, Committed: []SubjectRef{}},
		Limitations:       []string{clarificationRequiredLimitationOne},
	}
	RepairStoredClarification(&withCandidate, nil, SemanticStateReadAvailable)
	if withCandidate.Limitations[0] != clarificationRequiredLimitationOne {
		t.Fatalf("limitations = %#v: a row with a candidate keeps the one-subject sentence", withCandidate.Limitations)
	}
}

// The restatement edits the served copy only: a store that hands back its own
// slice must keep the row it stored.
func TestTheRestatementLeavesTheStoredSliceAlone(t *testing.T) {
	t.Parallel()
	limitations := []string{clarificationRequiredLimitationOne}
	served := InvestigationResult{
		Status:            InvestigationClarificationRequired,
		SubjectResolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		Limitations:       limitations,
		StructureNeeds:    &contractsv1.ContextFabricStructureNeeds{WindowOptions: []contractsv1.ContextFabricWindowOption{{}}},
	}
	RepairStoredClarification(&served, nil, SemanticStateReadAvailable)
	if served.Limitations[0] != clarificationRequiredLimitationWithheldPool {
		t.Fatalf("served = %#v, want the restated sentence", served.Limitations)
	}
	if limitations[0] != clarificationRequiredLimitationOne {
		t.Fatalf("the stored slice was edited: %#v", limitations)
	}
}
