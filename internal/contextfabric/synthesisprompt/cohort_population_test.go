package synthesisprompt

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestClientPayloadCarriesTheCohortPopulation(t *testing.T) {
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T00", Label: "T00"}
	cohort := &contextfabric.Cohort{
		Kind: contextfabric.SubjectTeam, Rationale: "r", Truncated: true, Population: 13,
		Members: []contextfabric.CohortMember{{Subject: team, Rank: 1, InclusionReasons: []string{"matched"}}},
	}
	encoded, err := ClientPayload("org-1", contextfabric.SynthesisInput{Graph: contextfabric.GraphContext{Cohort: cohort}}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"population":13`) {
		t.Errorf("client payload does not carry the cohort population: %s", encoded)
	}
}
