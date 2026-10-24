package devhealthsource

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The ownership statements are split so the edge counts read the producers' own
// row sources; the rendered producer statements must not change by one byte.
func TestOwnershipProducerStatementsAreByteIdenticalToTheGoldens(t *testing.T) {
	t.Parallel()
	for name, got := range map[string]string{
		"repository_teams_group":     repositoryTeamsGroupedSQL(),
		"repository_teams_paged":     repositoryTeamsStatement(cursorState{After: "k"}),
		"project_teams_group_legacy": projectTeamsGroupSQL(false),
		"project_teams_group_ingest": projectTeamsGroupSQL(true),
		"work_item_teams_empty":      workItemTeamsStatement(cursorState{}),
		"work_item_teams_after":      workItemTeamsStatement(cursorState{After: "k"}),
	} {
		want, err := os.ReadFile("testdata/" + name + ".golden.sql")
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: the producer statement drifted from the golden captured before the row source was extracted", name)
		}
	}
}

func TestOwnershipEdgeCountsReadTheProducersOwnRowSourceAndKeepTheirOmissions(t *testing.T) {
	t.Parallel()
	for _, ingest := range []bool{false, true} {
		project := projectOwnershipEdgeCountSQL(ingest)
		for _, want := range []string{
			projectTeamsFromGroupSQL(ingest),
			"HAVING NOT (" + projectTeamsSuppressedSQL + ")",
			derivedIDFitsSQL(identity.KindProject, "g_provider", "g_project_id"),
		} {
			if !strings.Contains(project, want) {
				t.Errorf("ingest=%v: project statement lacks %q", ingest, want)
			}
		}
	}
	repository := repositoryOwnershipEdgeCountSQL()
	for _, want := range []string{repositoryTeamsFromGroupSQL(), "HAVING o.repo_key != ''"} {
		if !strings.Contains(repository, want) {
			t.Errorf("repository statement lacks %q", want)
		}
	}
	workItem := workItemOwnershipEdgeCountSQL()
	for _, want := range []string{
		workItemTeamsFromGroupSQL(),
		"HAVING countIf(" + workItemTeamsQualifies + ") > 0",
		derivedIDFitsSQL(identity.KindWorkItem, "g_repo_id", "g_work_item_id"),
		"uniqExact(g_repo_id, g_work_item_id, g_team_id)",
	} {
		if !strings.Contains(workItem, want) {
			t.Errorf("work item statement lacks %q", want)
		}
	}
	for _, statement := range []string{project0(), repository, workItem} {
		for _, banned := range []string{"LIMIT {row_limit", "{since_us", "ORDER BY"} {
			if strings.Contains(statement, banned) {
				t.Errorf("a count has no page or cursor (%q)", banned)
			}
		}
	}
}

func project0() string { return projectOwnershipEdgeCountSQL(false) }

func TestOwnedByTeamEdgeCountsReportEveryArmAndRefuseAnUnknownOne(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{
		"AS g_repo_key":   {{"repository", uint64(7)}},
		"AS g_project_id": {{"project", uint64(3)}},
	}}
	source, err := NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"repository": 7, "project": 3, "work_item": 0}
	for arm, n := range want {
		key := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipOwnedByTeam, Arm: arm}
		if got, ok := counts[key]; !ok || got != n {
			t.Errorf("%s = %d (present %v), want %d", arm, got, ok, n)
		}
	}
	bad := &edgeCountClient{rows: map[string][][]any{"AS g_repo_key": {{"mystery", uint64(1)}}}}
	other, _ := NewTeamsProjectsSource(bad, true)
	if _, err := other.ProjectionSourceEdgeCounts(context.Background(), "org-a"); err == nil || !strings.Contains(err.Error(), `unknown arm "mystery"`) {
		t.Fatalf("an unknown arm is an error, got %v", err)
	}
	if deferred := source.DeferredEdgeRelations(); len(deferred) != 0 {
		t.Fatalf("the teams and projects source defers nothing, got %v", deferred)
	}
}
