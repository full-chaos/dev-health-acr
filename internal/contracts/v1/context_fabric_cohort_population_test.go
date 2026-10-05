package v1

import "testing"

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
