package contextfabric

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12517. A past answer states the time it speaks for with no help from the
// prose: ACR's own historical example, through the real decoder and serve step.
func TestAHistoricalAnswerServesItsEffectiveTime(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repopath.Root(t), "contracts/examples/v1/context_fabric_investigation_result_historical.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Temporal == nil || result.Temporal.CoverageComplete {
		t.Fatalf("the example must carry an incomplete temporal label: %+v", result.Temporal)
	}
	served, _ := ServeVocabularyLabels(result, true, 0)
	want := "This answer speaks for 2026-02-28, not the 2026-03-01 you asked about, and not every source covers that time."
	if got := served.VocabularyLabels["temporal:note"]; got != want {
		t.Fatalf("temporal:note = %q, want %q", got, want)
	}
}

// A current answer has no temporal label, so it has no line.
func TestACurrentAnswerHasNoTemporalNote(t *testing.T) {
	var result InvestigationResult
	if got := serveWords(t, result); len(got) != 0 {
		t.Fatalf("a current answer served %v", got)
	}
}

// Near the bound the sentence stays: with no word a past answer reads as current.
func TestTheTemporalNoteSurvivesTheStatusTier(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repopath.Root(t), "contracts/examples/v1/context_fabric_investigation_result_historical.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bare contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &bare); err != nil {
		t.Fatal(err)
	}
	labeled, _ := ServeVocabularyLabels(bare, true, 0)
	served, tier := ServeVocabularyLabels(bare, true, encodedSize(t, labeled)-1)
	if tier != contractsv1.ContextFabricVocabularyLabelsStatusOnly {
		t.Fatalf("one byte short of the full map: tier %q", tier)
	}
	if served.VocabularyLabels["temporal:note"] == "" {
		t.Fatalf("the status tier holds %v", served.VocabularyLabels)
	}
}
