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

// The team entry follows the subject-level declarations: change a subject
// entry and the derived team list changes with it, so a hard-coded team list
// cannot satisfy this row.
func TestTeamRollupObligationsFollowTheSubjectDeclarations(t *testing.T) {
	declared := map[contextfabric.SubjectKind][]contextfabric.AnswerObligation{
		contextfabric.SubjectRepository: {contextfabric.ObligationState, contextfabric.ObligationTrendSeries, contextfabric.ObligationReadiness},
		contextfabric.SubjectIncident:   {contextfabric.ObligationPeriodDelta, contextfabric.ObligationTrendSeries},
		contextfabric.SubjectWorkItem:   {contextfabric.ObligationRemainingWork},
	}
	got := deriveTeamRollupObligations(declared)
	want := []contextfabric.AnswerObligation{contextfabric.ObligationPeriodDelta, contextfabric.ObligationTrendSeries, contextfabric.ObligationRemainingWork}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derived = %v, want %v (union in subject-kind order, state and readiness dropped, no duplicates)", got, want)
	}
	if got := deriveTeamRollupObligations(nil); len(got) != 0 {
		t.Fatalf("no subject declarations derived %v", got)
	}
}
