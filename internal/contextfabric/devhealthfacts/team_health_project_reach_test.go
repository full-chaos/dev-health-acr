package devhealthfacts_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// A team that owns a project and no repository reaches repositories through
// the project's linked pull requests; it is never served "owns no repository".
func TestTeamHealthReachingRepositoriesThroughAProjectIsNotNamedAsOwningNothing(t *testing.T) {
	t.Parallel()
	client := projectReachFake()
	client.tables = append(client.tables, fakeTable{match: "FROM compounding_risk_daily", rows: nil})
	result := readTeamHealth(t, client)
	if result.State == contextfabric.SourceNotApplicable {
		t.Fatalf("state = %q, want the ordinary no-data state for a team that reaches a repository", result.State)
	}
	if strings.Contains(result.Reason, "owns no repository") || strings.Contains(result.Reason, "own no repository") {
		t.Fatalf("reason = %q, a team that reaches a repository is not named as owning nothing", result.Reason)
	}
}

func TestTeamDeficienciesReachingRepositoriesThroughAProjectIsNotNamedAsOwningNothing(t *testing.T) {
	t.Parallel()
	result := readTeamKind(t, projectReachFake(), contextfabric.FactOperationalDeficiencies)
	if result.State == contextfabric.SourceNotApplicable || strings.Contains(result.Reason, "own no repository") || strings.Contains(result.Reason, "owns no repository") {
		t.Fatalf("state = %q reason = %q, a team that reaches a repository is not named as owning nothing", result.State, result.Reason)
	}
}
