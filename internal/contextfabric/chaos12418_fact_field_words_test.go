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

// The map holds only the fields in this result, never the table: one field of
// the table is one key, so a claim costs what it names.
func TestTheMapHoldsOnlyTheFieldsOfThisResult(t *testing.T) {
	words := serveWords(t, InvestigationResult{ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{Field: "completion_ratio"}}})
	if len(words) != 1 {
		t.Fatalf("one field served %d words: %v", len(words), words)
	}
}

// Near the bound a field word sheds with every word that is not a status word,
// so the status tier is what it was before these words.
func TestTheStatusTierHoldsNoFactFieldWord(t *testing.T) {
	bare := correctedFixture()
	bare.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{claimedFactWithTables()}
	labeled, tier := ServeVocabularyLabels(bare, true, 0)
	if tier != contractsv1.ContextFabricVocabularyLabelsFull {
		t.Fatalf("no bound: tier %q", tier)
	}
	served, tier := ServeVocabularyLabels(bare, true, encodedSize(t, labeled)-1)
	if tier != contractsv1.ContextFabricVocabularyLabelsStatusOnly {
		t.Fatalf("one byte short of the full map: tier %q", tier)
	}
	for key := range served.VocabularyLabels {
		if strings.HasPrefix(key, "fact_field:") {
			t.Errorf("the status tier holds %q", key)
		}
	}
}

// A derivation's field names what the value measures, in free text, and is not
// looked up on a fact (ContextFabricClaimedFact.DerivedFrom). A name that happens
// to match a declaration is still not that declaration, so it takes the generic
// path.
func TestADerivedClaimTakesTheGenericPathEvenWhenItsFieldIsADeclaredName(t *testing.T) {
	result := InvestigationResult{ClaimedFacts: []contractsv1.ContextFabricClaimedFact{{
		Field:       "completion_ratio",
		DerivedFrom: []contractsv1.ContextFabricDerivationInput{{FactID: "fact_one", Field: "completed_count"}},
	}}}
	if got := serveWords(t, result); len(got) != 0 {
		t.Fatalf("a derived field served %v", got)
	}
}
