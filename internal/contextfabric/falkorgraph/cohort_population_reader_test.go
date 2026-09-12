package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The population has to SURVIVE THE BOUNDARY, not merely be computed.
//
// graphrank counts it and this adapter is the only thing that carries it out
// to the engine. A counter that is correct inside DiscoveredCohort and never
// assigned onto the returned GraphContext is indistinguishable, from every
// consumer's side, from not counting at all -- and the graphrank tests cannot
// see that, because they call the pure function directly. This drives the
// real adapter and reads the boundary.
func TestDiscoverContextCarriesTheCohortPopulationPastTheRenderCap(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			// THREE authorized teams against a cap of ONE. The pool must
			// exceed the cap or the two numbers agree and this proves
			// nothing.
			rows := make([]row, 0, 3)
			for _, id := range []string{"team_platform", "team_payments", "team_search"} {
				r := fakeSubjectNodeRow("team", id, id)
				r["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
				r["n"].(*node).Properties["authorization_teams"] = []string{id}
				rows = append(rows, r)
			}
			return rows, nil
		default:
			t.Fatalf("unexpected query for a subjectless cohort request: %s", cypher)
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	request := contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "which teams are struggling",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 1, MaxRelationshipPaths: 10,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "teams_under_pressure",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		Frame:      discoveredTeamCohortFrame(),
	}

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("Cohort = %#v, want exactly one member -- the render cap must still bound what the answer carries", result.Cohort)
	}
	if result.CohortPopulation != 3 {
		t.Fatalf("CohortPopulation = %d, want 3 -- the count crossed the boundary as the cap's value, or not at all", result.CohortPopulation)
	}
}
