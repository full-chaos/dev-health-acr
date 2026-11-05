package synthesisprompt_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

// CHAOS-13009: the latest day the compared teams share feeds the offer the
// server composes when it refuses the quadrant. The model was shown the field
// key and wrote it into its own limitation ("a common live day of 2026-10-07").
// The server's disclosures are the server's to word: the model never sees the field.
func TestTheDayAnOfferIsComposedFromNeverReachesTheModel(t *testing.T) {
	t.Parallel()
	facts := []contextfabric.CanonicalFact{{
		Kind:    contextfabric.FactLandscape,
		Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:gh:ops-team", Label: "Ops Team"},
		Fields: map[string]contextfabric.FactValue{
			"as_of_day":                          contextfabric.StringFactValue("2026-10-07"),
			"churn_loc_30d":                      contextfabric.IntegerFactValue(441951),
			contextfabric.FactFieldCommonLiveDay: contextfabric.StringFactValue("2026-10-07"),
		},
	}}
	shown := synthesisprompt.ModelFacingFacts(facts)
	if _, leaked := shown[0].Fields[contextfabric.FactFieldCommonLiveDay]; leaked {
		t.Fatalf("the model is shown %v", shown[0].Fields)
	}
	for _, kept := range []string{"as_of_day", "churn_loc_30d"} {
		if _, ok := shown[0].Fields[kept]; !ok {
			t.Errorf("the model lost %q", kept)
		}
	}
	if _, still := facts[0].Fields[contextfabric.FactFieldCommonLiveDay]; !still {
		t.Fatal("the canonical fact lost the field: the engine reads it for the offer")
	}
}
