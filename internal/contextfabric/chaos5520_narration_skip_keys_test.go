package contextfabric

import (
	"reflect"
	"sort"
	"testing"
)

// TestEveryDeclaredDriverSkipReasonIsProducedAndNothingElseIs drives the real
// narration composer into each way it counts a skipped driver and requires
// the keys it wrote to equal the closed key set the line's declaration
// publishes. A reason added to the composer without the declaration (or the
// reverse) fails here.
func TestEveryDeclaredDriverSkipReasonIsProducedAndNothingElseIs(t *testing.T) {
	t.Parallel()
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}, availableCoverage())

	_, _, uncitable := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})

	bogus := rankTestMember("B")
	bogus.EvidenceRefIDs = []string{"evidence_team_b_roster"}
	bogusCohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{bogus}}
	bogusRanked, _, bogusCitations := RankCohort(bogusCohort, []CanonicalFact{healthFact("B", "high")}, availableCoverage())
	bogusRanked.Members[0].Drivers = append(bogusRanked.Members[0].Drivers, CohortMemberDriver{Signal: "not_a_ranking_signal", Weight: 99, WeightContributed: 99})
	_, _, unknownSignal := narrateCohortDriverJudgments(bogusRanked, nil, 0, bogusCitations, ItemAllocation{})

	seen := map[string]bool{}
	for _, event := range []CohortDriverNarrationEvent{uncitable, unknownSignal} {
		for key := range event.DriversSkipped {
			seen[key] = true
		}
	}
	var got []string
	for key := range seen {
		got = append(got, key)
	}
	want := CohortDriverNarrationLineVocabulary("drivers_skipped")
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the composer wrote skip reasons %v, the declaration publishes %v", got, want)
	}
}
