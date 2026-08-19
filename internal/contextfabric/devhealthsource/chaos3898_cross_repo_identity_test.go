package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// TestClickHouseProjectionSourceDoesNotCollideAcrossRepos is the real,
// call-site-level acceptance proof for design brief D-1..D-4 (CHAOS-3898
// §0): two different repos' rows sharing the SAME bare source-natural-key
// value (run_id / review_id / deployment_id / work_item_id) must no longer
// derive the same canonical id, now that D-1..D-4's producers (tables.go's
// queryCIRuns/queryPullRequestReviews/queryDeployments/queryWorkItems)
// derive through identity.Derive with repo_id as a real segment.
//
// internal/contextfabric/identity/chaos3898_known_defects_test.go used to
// carry TestKnownDefect_D1..D4 as RED-documented pins for this same
// invariant, each comparing a frozen, no-longer-live "today" helper against
// itself -- literally `todayCIPipelineRunID("run-1") ==
// todayCIPipelineRunID("run-1")`, the same call made twice, which can only
// ever be true and so could never usefully flip to green. That file's own
// doc comment names the correct S2 action once its cited call sites are
// rewired: "update this file's 'today' helper to match, or delete the
// now-obsolete pin." All four call sites it cited ARE rewired as of this
// slice, so those four tests are deleted (not merely un-skipped) in this
// same change -- this test is their replacement, and it is the one that
// actually exercises the real call sites end to end through the fake
// ClickHouse client, the same way every other regression test in this
// package does.
func TestClickHouseProjectionSourceDoesNotCollideAcrossRepos(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	for i, table := range tables {
		switch table.match {
		case "FROM work_items AS w":
			tables[i] = fakeTable{match: table.match, rows: [][]any{
				{"WIDGET-SHARED", "repo-1", "example-org/widget-service", "Title A", "open", "", at, at, uint8(0), zeroTime, "", "", "", []string{}},
				{"WIDGET-SHARED", "repo-2", "example-org/other-service", "Title B", "open", "", at, at, uint8(0), zeroTime, "", "", "", []string{}},
			}}
		case "FROM deployments AS d":
			tables[i] = fakeTable{match: table.match, rows: [][]any{
				{"repo-1", "example-org/widget-service", "deploy-shared", "success", "production", at, uint8(1), at, uint8(0), zeroTime, "v1"},
				{"repo-2", "example-org/other-service", "deploy-shared", "success", "production", at, uint8(1), at, uint8(0), zeroTime, "v1"},
			}}
		}
	}
	tables = append(tables,
		fakeTable{match: "FROM git_pull_request_reviews AS r", rows: [][]any{
			{"review-shared", "repo-1", uint32(1042), "approved", at, "example-org/widget-service", at, uint8(0), zeroTime, "PR A"},
			{"review-shared", "repo-2", uint32(1042), "approved", at, "example-org/other-service", at, uint8(0), zeroTime, "PR B"},
		}},
		fakeTable{match: "FROM ci_pipeline_runs AS c", rows: [][]any{
			{"run-shared", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, "pipeline-a"},
			{"run-shared", "repo-2", "main", "success", "example-org/other-service", at, at, uint8(1), at, "pipeline-b"},
		}},
	)
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}

	// D-1..D-4: each kind's two same-natural-key rows, from repo-1 and
	// repo-2, must produce two DISTINCT canonical ids -- collapsing to one
	// (the pre-fix defect) would mean one repo's row silently overwrote
	// the other's payload and authorization, the exact collision D-1..D-4
	// name.
	want := []string{
		"work_item.v2:repo-1:WIDGET-SHARED", "work_item.v2:repo-2:WIDGET-SHARED",
		"deployment.v2:repo-1:deploy-shared", "deployment.v2:repo-2:deploy-shared",
		"pull_request_review.v2:repo-1:1042:review-shared", "pull_request_review.v2:repo-2:1042:review-shared",
		"ci_pipeline_run.v2:repo-1:run-shared", "ci_pipeline_run.v2:repo-2:run-shared",
	}
	found := map[string]bool{}
	for _, entity := range batch.Entities {
		found[entity.Subject.CanonicalID] = true
	}
	for _, id := range want {
		if !found[id] {
			t.Fatalf("canonical id %q missing from batch -- two repos' rows collided into one id; batch entities = %+v", id, batch.Entities)
		}
	}
}

// TestTeamsProjectsSourceDoesNotCollideAcrossProviders is D-5's call-site
// acceptance proof, the same shape as
// TestClickHouseProjectionSourceDoesNotCollideAcrossRepos above but for
// teams_projects.go's queryProjects: two different providers' projects
// rows sharing the same bare projects.id must no longer derive the same
// canonical id now that D-5 derives through identity.Derive with provider
// as a real segment.
func TestTeamsProjectsSourceDoesNotCollideAcrossProviders(t *testing.T) {
	t.Parallel()
	updatedAt := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			projectRow("P-SHARED", "GitHub Widget", "", "github", "backlog", "", 1, updatedAt),
			projectRow("P-SHARED", "GitLab Widget", "", "gitlab", "backlog", "", 1, updatedAt.Add(-time.Minute)),
		}},
	}}
	batch := teamsProjectsBatch(t, client)
	want := []string{"project.v2:github:P-SHARED", "project.v2:gitlab:P-SHARED"}
	found := map[string]bool{}
	for _, entity := range batch.Entities {
		found[entity.Subject.CanonicalID] = true
	}
	for _, id := range want {
		if !found[id] {
			t.Fatalf("canonical id %q missing from batch -- github's and gitlab's projects collided into one id; batch entities = %+v", id, batch.Entities)
		}
	}
}
