package answerprojection

import (
	"maps"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12416: the projection carries the result's own map, so web's read path
// over a byte option and MCP both get the words, and an absent map stays absent.
func TestProjectCarriesTheMap(t *testing.T) {
	result := contractsv1.ContextFabricInvestigationResult{
		VocabularyLabels: map[string]string{"answer_status:partial": "Partial answer", "status_correction:note": "A sentence."},
	}
	projection := Project(result, DefaultBudget)
	if !maps.Equal(projection.VocabularyLabels, result.VocabularyLabels) {
		t.Fatalf("projection carries %v, want %v", projection.VocabularyLabels, result.VocabularyLabels)
	}
	projection.VocabularyLabels["answer_status:partial"] = "changed"
	if result.VocabularyLabels["answer_status:partial"] != "Partial answer" {
		t.Fatal("the projection shares the result's map")
	}
}

func TestProjectKeepsAnAbsentMapAbsent(t *testing.T) {
	if got := Project(contractsv1.ContextFabricInvestigationResult{}, DefaultBudget).VocabularyLabels; got != nil {
		t.Fatalf("an absent map projected as %v: an old pin refuses the key", got)
	}
}

func TestShedVocabularyLabelsNarrowsByTier(t *testing.T) {
	labels := map[string]string{"answer_status:partial": "Partial answer", "status_correction:note": "A sentence.", "subject_kind:team": "team"}
	status := map[string]string{"answer_status:partial": "Partial answer", "status_correction:note": "A sentence."}
	for name, row := range map[string]struct {
		tier contractsv1.ContextFabricVocabularyLabelsTier
		want map[string]string
	}{
		"full":        {contractsv1.ContextFabricVocabularyLabelsFull, labels},
		"status only": {contractsv1.ContextFabricVocabularyLabelsStatusOnly, status},
		"none":        {contractsv1.ContextFabricVocabularyLabelsNone, nil},
	} {
		t.Run(name, func(t *testing.T) {
			shed := ShedVocabularyLabels(contractsv1.ContextFabricAnswerProjection{ResultID: "result_one", VocabularyLabels: labels}, row.tier)
			if !maps.Equal(shed.VocabularyLabels, row.want) || shed.ResultID != "result_one" {
				t.Fatalf("shed = %+v, want map %v", shed, row.want)
			}
		})
	}
}

// A complete status over complete completeness composes "Full answer" on the
// canonical document. The projection cuts it and re-derives its state, so the
// same word on the projection would claim what that document contradicts.
func TestAProjectionWordsItsStatusByItsOwnCompleteness(t *testing.T) {
	result := contractsv1.ContextFabricInvestigationResult{Status: contractsv1.ContextFabricInvestigationComplete}
	result.Completeness.State = contractsv1.ContextFabricAnswerCompletenessComplete
	result.Completeness.Outcomes = []contractsv1.ContextFabricPlanRequirementOutcomeRow{{Requirement: "state/subject/team", Outcome: contractsv1.ContextFabricRequirementSatisfied}}
	result.VocabularyLabels = map[string]string{"answer_status:complete": "Full answer", "another_vocabulary:complete": "a word of its own"}

	whole := Project(result, DefaultBudget)
	if got := whole.VocabularyLabels["answer_status:complete"]; got != "Full answer" || whole.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("an uncut projection reads %q at state %q", got, whole.Completeness.State)
	}

	MarkFullResultOmitted(&whole)
	if whole.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatal("omitting the full result left the state complete: this proves nothing")
	}
	if got := whole.VocabularyLabels["answer_status:complete"]; got != "Answer, not fully covered" {
		t.Fatalf("a projection with the full result left out reads %q", got)
	}
	if result.VocabularyLabels["answer_status:complete"] != "Full answer" {
		t.Fatal("the restatement wrote through to the canonical map")
	}
	if got := whole.VocabularyLabels["another_vocabulary:complete"]; got != "a word of its own" {
		t.Fatalf("the restatement reached another vocabulary: %q", got)
	}

	cut := result
	cut.Limitations = []string{}
	cut.Drivers = []contractsv1.ContextFabricDriverJudgment{{DriverID: "driver_one", Standing: contractsv1.ContextFabricDriverPrincipal}, {DriverID: "driver_two", Standing: contractsv1.ContextFabricDriverPrincipal}}
	projected := Project(cut, Budget{MaxDrivers: 1})
	if projected.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("a cut left the state complete: this proves nothing: %+v", projected.ProjectionBudget)
	}
	if got := projected.VocabularyLabels["answer_status:complete"]; got != "Answer, not fully covered" {
		t.Fatalf("a cut projection reads %q", got)
	}
}

// The note quotes the status word, so it reads the same completeness.
func TestAProjectionRestatesTheNoteWithItsStatusWord(t *testing.T) {
	correction := contractsv1.ContextFabricStatusCorrection{
		ReplacedStatus: contractsv1.ContextFabricInvestigationComplete, Direction: contractsv1.ContextFabricStatusCorrectionCompleteToPartial,
		DecidingRequirement: "health/subject/team", DecidingOutcome: contractsv1.ContextFabricRequirementNarrowed,
	}
	result := contractsv1.ContextFabricInvestigationResult{Status: contractsv1.ContextFabricInvestigationPartial}
	result.Completeness.StatusCorrection = &correction
	result.VocabularyLabels = map[string]string{"answer_status:partial": "Partial answer", "status_correction:note": "A word from another state."}
	projection := Project(result, DefaultBudget)
	MarkFullResultOmitted(&projection)
	want, ok := contractsv1.ContextFabricStatusCorrectionNote(correction, projection.Status, projection.Completeness.State)
	if !ok || projection.VocabularyLabels["status_correction:note"] != want {
		t.Fatalf("note = %q, want %q", projection.VocabularyLabels["status_correction:note"], want)
	}
	// A map that was shed of its note does not grow one back.
	delete(result.VocabularyLabels, "status_correction:note")
	bare := Project(result, DefaultBudget)
	if _, held := bare.VocabularyLabels["status_correction:note"]; held {
		t.Fatal("the restatement added a word the map did not hold")
	}
}
