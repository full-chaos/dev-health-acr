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

func TestWithoutVocabularyLabelsShedsTheMapOnly(t *testing.T) {
	projection := Project(contractsv1.ContextFabricInvestigationResult{
		ResultID: "result_one", VocabularyLabels: map[string]string{"answer_status:partial": "Partial answer"},
	}, DefaultBudget)
	shed := WithoutVocabularyLabels(projection)
	if shed.VocabularyLabels != nil || shed.ResultID != "result_one" {
		t.Fatalf("shed = %+v", shed)
	}
}
