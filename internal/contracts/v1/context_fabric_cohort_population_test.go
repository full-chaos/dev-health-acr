package v1

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestCohortValidateEnforcesThePopulationInvariants(t *testing.T) {
	t.Parallel()
	base := func() ContextFabricCohort { return rankedCohortFixture(ContextFabricCohortScoreMeaning("")) }
	valid := base()
	valid.Members[0].RankingComputed = false
	valid.Members[0].AttentionRank = 0
	valid.Members[0].DataCompleteness, valid.Members[0].Outcome, valid.Members[0].MissingSignals = "", "", nil
	if err := valid.Validate(); err != nil {
		t.Fatalf("the unmutated cohort is invalid: %v", err)
	}
	for name, mutate := range map[string]func(*ContextFabricCohort){
		"negative":                         func(c *ContextFabricCohort) { c.Population = -1 },
		"below the listed members":         func(c *ContextFabricCohort) { c.Population = 1; c.Members = append(c.Members, c.Members[0]) },
		"a lower bound with no population": func(c *ContextFabricCohort) { c.PopulationLowerBound = true },
	} {
		c := valid
		c.Members = append([]ContextFabricCohortMember(nil), valid.Members...)
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: a malformed population was accepted", name)
		}
	}
}

func TestProjectedCohortRejectsAPopulationBelowTheTotal(t *testing.T) {
	t.Parallel()
	c := ContextFabricProjectedCohort{
		Kind: ContextFabricSubjectWorkItem, Total: 20, Population: 14, Rationale: "walk",
		Members: []ContextFabricProjectedCohortMember{{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_item:a", Label: "A"}, Rank: 1, InclusionReasons: []string{"linked"}}},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("a population below the canonical total was accepted")
	}
	c.Population = 20
	if err := c.Validate(); err != nil {
		t.Fatalf("a population equal to the total was rejected: %v", err)
	}
}

func TestTheProjectionSchemaRejectsALowerBoundWithNoPopulation(t *testing.T) {
	t.Parallel()
	fixture := loadFixture[map[string]any](t, "context_fabric_answer_projection.v1.json")
	cohort, ok := fixture["cohort"].(map[string]any)
	if !ok {
		t.Fatal("the projection fixture has no cohort")
	}
	check := func(population any) error {
		cohort["population_lower_bound"] = true
		delete(cohort, "population")
		if population != nil {
			cohort["population"] = population
		}
		encoded, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		return contractcheck.ValidateSerialized("", "context_fabric_answer_projection.v1.schema.json", encoded)
	}
	if err := check(nil); err == nil {
		t.Fatal("a lower bound with no population is schema-valid")
	}
	if err := check(float64(5)); err != nil {
		t.Fatalf("a lower bound with a population was rejected: %v", err)
	}
}
