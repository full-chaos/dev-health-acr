package devhealthfacts

import (
	"reflect"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Every kind served as a team rollup declares the obligations its subject
// level declares, minus the ones a count over owned repositories cannot
// establish.
func TestTeamRollupKindsDeclareDerivedObligations(t *testing.T) {
	want := map[contextfabric.FactKind][]contextfabric.AnswerObligation{
		contextfabric.FactBlockers:     {contextfabric.ObligationRemainingWork},
		contextfabric.FactPullRequests: {contextfabric.ObligationPrincipalDrivers},
		contextfabric.FactIncidents:    {contextfabric.ObligationPrincipalDrivers},
		contextfabric.FactDeployments:  {contextfabric.ObligationPrincipalDrivers},
	}
	for kind := range teamRollupObligationKinds {
		expected, ok := want[kind]
		if !ok {
			t.Fatalf("team rollup kind %q has no expected row", kind)
		}
		got := append([]contextfabric.AnswerObligation(nil), factKindObligations(kind)[contextfabric.SubjectTeam]...)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("%s team obligations = %v, want %v", kind, got, expected)
		}
	}
	if len(teamRollupObligationKinds) != len(want) {
		t.Errorf("team rollup kinds = %d, expected rows = %d", len(teamRollupObligationKinds), len(want))
	}
}
