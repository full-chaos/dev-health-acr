package devhealthsource_test

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// admittedByPastRange mirrors the graph temporal rule for a range read: an edge
// is read when it starts at or before the window end and has not ended by the
// window start.
func admittedByPastRange(edge contractsv1.ContextFabricRelationshipProjection, start, end time.Time) bool {
	return (edge.ValidFrom == nil || !edge.ValidFrom.After(end)) && (edge.ValidTo == nil || edge.ValidTo.After(start))
}

func TestOwnershipEdgesAreNotTimeSlicedByTheSyncStamp(t *testing.T) {
	t.Parallel()
	synced := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	t.Run("project to team", func(t *testing.T) {
		t.Parallel()
		client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership FINAL", rows: [][]any{
			projectTeamRow("project-x", "team-y", "manual", synced, 1, time.Time{}, synced),
		}}}}
		edge := relationshipByID(t, teamsProjectsBatch(t, client), devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "project-x", "team-y", "manual"))
		if !admittedByPastRange(edge, start, end) {
			t.Fatalf("open ownership synced %v is excluded by the past window %v..%v (valid_from %v valid_to %v)", synced, start, end, edge.ValidFrom, edge.ValidTo)
		}
	})
	t.Run("project to team ended before the window", func(t *testing.T) {
		t.Parallel()
		ended := start.Add(-24 * time.Hour)
		client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership FINAL", rows: [][]any{
			projectTeamRow("project-x", "team-y", "manual", synced, 0, ended, synced),
		}}}}
		edge := relationshipByID(t, teamsProjectsBatch(t, client), devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "project-x", "team-y", "manual"))
		if admittedByPastRange(edge, start, end) {
			t.Fatalf("ownership ended %v is admitted by the window %v..%v", ended, start, end)
		}
	})
	t.Run("repository to team", func(t *testing.T) {
		t.Parallel()
		fixture := openRepositoryTeam(repoGitHubID, "full-chaos/dev-health-ops", "gh:ops-team", "github", "native", synced)
		batch := repositoryTeamBatch(t, repositoryTeamClient(fixture), nil)
		edge := relationshipByID(t, batch, devhealthsource.RepositoryTeamRelationshipIDForTest(repoGitHubID, "gh:ops-team", "github", "native"))
		if !admittedByPastRange(edge, start, end) {
			t.Fatalf("open ownership synced %v is excluded by the past window %v..%v (valid_from %v valid_to %v)", synced, start, end, edge.ValidFrom, edge.ValidTo)
		}
	})
}
