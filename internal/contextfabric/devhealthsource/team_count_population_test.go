package devhealthsource

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The graph total of teams (falkorgraph CountKindTotal) keeps the active-team
// predicate because this statement counts ACTIVE teams only.
func TestTeamSourceCountIsTheActiveTeamPopulation(t *testing.T) {
	t.Parallel()
	for _, st := range teamsProjectsKindCountStatements {
		if st.kind == contractsv1.ContextFabricSubjectTeam {
			if !strings.Contains(st.sql, devhealthschema.ActiveTeamPredicate("")) {
				t.Fatalf("the team count statement no longer counts active teams only: %s", st.sql)
			}
			return
		}
	}
	t.Fatal("no team count statement")
}
