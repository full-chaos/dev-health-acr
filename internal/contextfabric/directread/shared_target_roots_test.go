package directread

import (
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A work item two requested teams reach counts for both when the expander
// names both: one fact, both teams in reached_from, both rows served, and the
// read is complete.
func TestDirectReadCountsATargetForEveryRequestedTeamThatReachesIt(t *testing.T) {
	shared := labelled(workA, "WI-A")
	expander := &scriptedExpander{expand: func(request contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets:     []contextfabric.SubjectRef{shared},
			TargetRoot:  map[string]contextfabric.SubjectRef{contextfabric.FactSubjectKey(shared): teamT},
			TargetRoots: map[string][]contextfabric.SubjectRef{contextfabric.FactSubjectKey(shared): {teamU, teamT}},
			Counts:      contextfabric.FactScopeExpansionCounts{CandidateCount: 1},
		}
	}}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam))
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{
		{Kind: "team", CanonicalID: teamT.CanonicalID}, {Kind: "team", CanonicalID: teamU.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fact := servedOn(response, "blockers", shared)
	want := []ReachedFrom{{Kind: "team", CanonicalID: teamT.CanonicalID, Basis: "direct"}, {Kind: "team", CanonicalID: teamU.CanonicalID, Basis: "direct"}}
	if fact == nil || !reflect.DeepEqual(fact.ReachedFrom, want) || len(response.Facts) != 1 {
		t.Fatalf("facts %s, want one fact reached from both teams", mustJSON(t, response.Facts))
	}
	for _, team := range []contextfabric.SubjectRef{teamT, teamU} {
		if row := coverageOf(t, response, "blockers", team); row.Outcome != OutcomeFactServed || strings.Contains(row.Reason, "counted under only one") {
			t.Fatalf("row %+v, want fact_served with no lower-bound note", row)
		}
	}
	if response.Status != StatusComplete {
		t.Fatalf("status %q, want complete", response.Status)
	}
}

// Two requested teams with their own targets, every root named: neither row
// is a lower bound.
func TestDirectReadServesEveryShareCompleteWhenTheExpanderNamesEveryRoot(t *testing.T) {
	a, b := labelled(workA, "WI-A"), labelled(workB, "WI-B")
	expander := &scriptedExpander{expand: func(contextfabric.FactScopeExpansionRequest) contextfabric.FactScopeExpansionResult {
		return contextfabric.FactScopeExpansionResult{
			Targets:     []contextfabric.SubjectRef{a, b},
			TargetRoots: map[string][]contextfabric.SubjectRef{contextfabric.FactSubjectKey(a): {teamT}, contextfabric.FactSubjectKey(b): {teamU}},
			Counts:      contextfabric.FactScopeExpansionCounts{CandidateCount: 2},
		}
	}}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, perSubjectProvider(contextfabric.FactBlockers, contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectTeam))
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"blockers"}, Subjects: []RequestSubject{
		{Kind: "team", CanonicalID: teamT.CanonicalID}, {Kind: "team", CanonicalID: teamU.CanonicalID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range []contextfabric.SubjectRef{teamT, teamU} {
		if row := coverageOf(t, response, "blockers", team); row.Outcome != OutcomeFactServed || !strings.Contains(row.Reason, "scope expansion reached 1 work items") || strings.Contains(row.Reason, "counted under only one") {
			t.Fatalf("row %+v", row)
		}
	}
	if response.Status != StatusComplete {
		t.Fatalf("status %q, want complete", response.Status)
	}
}
