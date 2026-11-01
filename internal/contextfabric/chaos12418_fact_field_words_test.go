package contextfabric

import (
	"maps"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12418. A claimed fact reaches the screen with no help from the prose:
// the map holds the word of its field and of its table's columns.

func claimedFactWithTables() contractsv1.ContextFabricClaimedFact {
	var value contractsv1.ContextFabricScalarValue
	return contractsv1.ContextFabricClaimedFact{
		Kind: contractsv1.ContextFabricFactFlow, Field: "completion_ratio",
		Rows:           []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]contractsv1.ContextFabricScalarValue{"team_name": value}}, {Fields: map[string]contractsv1.ContextFabricScalarValue{"cycle_time_p50_hours": value}}},
		TimeSeriesRows: []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]contractsv1.ContextFabricScalarValue{"items_completed": value}}},
	}
}

func TestAClaimedFactServesTheWordsOfItsFieldAndItsColumns(t *testing.T) {
	result := InvestigationResult{ClaimedFacts: []contractsv1.ContextFabricClaimedFact{claimedFactWithTables()}}
	want := map[string]string{
		"fact_kind:flow":                  "flow",
		"fact_field:completion_ratio":     "Completion ratio",
		"fact_field:team_name":            "Team name",
		"fact_field:cycle_time_p50_hours": "Median cycle time (hours)",
		"fact_field:items_completed":      "Items completed",
	}
	if got := serveWords(t, result); !maps.Equal(got, want) {
		t.Fatalf("map = %v, want %v", got, want)
	}
}

// A derived claim names what it measures in free text, and a column can be new
// to this build: neither has a row, so neither has a key, and the raw string is
// never served as a label.
func TestAFieldWithNoDeclarationTakesTheGenericPath(t *testing.T) {
	var value contractsv1.ContextFabricScalarValue
	result := InvestigationResult{ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{
		Kind: contractsv1.ContextFabricFactFlow, Field: "mean of the two rates",
		Rows: []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]contractsv1.ContextFabricScalarValue{"a_future_column": value}}},
	}}}
	for key := range serveWords(t, result) {
		if strings.HasPrefix(key, "fact_field:") {
			t.Errorf("a field with no declaration has key %q", key)
		}
	}
}

// The fact field words go first, so a field word never costs a chip its word,
// and the tier states it.
func TestABoundShedsTheFactFieldWordsBeforeAnyOtherWord(t *testing.T) {
	bare := correctedFixture()
	bare.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{claimedFactWithTables()}
	labeled, tier := ServeVocabularyLabels(bare, true, 0)
	if tier != contractsv1.ContextFabricVocabularyLabelsFull {
		t.Fatalf("no bound: tier %q", tier)
	}
	without := contractsv1.ContextFabricVocabularyLabelsWithoutFactFields(labeled.VocabularyLabels)
	if len(without) == 0 || len(without) >= len(labeled.VocabularyLabels) {
		t.Fatalf("the fixture must hold field words and other words: %v", labeled.VocabularyLabels)
	}

	served, tier := ServeVocabularyLabels(bare, true, encodedSize(t, labeled)-1)
	if tier != contractsv1.ContextFabricVocabularyLabelsNoFactFields || !maps.Equal(served.VocabularyLabels, without) {
		t.Fatalf("one byte short of the full map: tier %q map %v", tier, served.VocabularyLabels)
	}

	served, tier = ServeVocabularyLabels(bare, true, encodedSize(t, served)-1)
	status := contractsv1.ContextFabricStatusVocabularyLabels(labeled.VocabularyLabels)
	if tier != contractsv1.ContextFabricVocabularyLabelsStatusOnly || !maps.Equal(served.VocabularyLabels, status) {
		t.Fatalf("one byte short of the other words: tier %q map %v", tier, served.VocabularyLabels)
	}
}

// A map of field words only has no word left to keep, so an empty map does not
// stand for the tier: the shed reports none.
func TestAMapOfFieldWordsOnlyHasNoFactFieldsTier(t *testing.T) {
	bare := InvestigationResult{ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{Field: "completion_ratio"}}}
	labeled, _ := ServeVocabularyLabels(bare, true, 0)
	if len(labeled.VocabularyLabels) != 1 {
		t.Fatalf("the fixture must hold one field word: %v", labeled.VocabularyLabels)
	}
	served, tier := ServeVocabularyLabels(bare, true, encodedSize(t, labeled)-1)
	if tier != contractsv1.ContextFabricVocabularyLabelsNone || served.VocabularyLabels != nil {
		t.Fatalf("tier %q map %v, want none and no map", tier, served.VocabularyLabels)
	}
}
