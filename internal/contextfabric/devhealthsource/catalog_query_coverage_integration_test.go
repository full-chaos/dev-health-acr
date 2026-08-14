package devhealthsource_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// TestCatalogQueriesExecutableAgainstDeclaredSchema executes every
// contextpacket source query against a ClickHouse holding ONLY the tables
// devhealthschema declares, rendered by DDL(). A query that consumes a
// column (or table) the declaration omits fails here with the server's
// unknown-identifier error instead of inside the next fixture that trusts
// the declaration -- the failure mode CHAOS-3781's hosted-integration run
// hit when repos.ref was consumed by RepositoryScopeQueryV1 but absent
// from the declaration, and which a per-package parity guard cannot see
// because both its fixtures and its assertions render from the same
// declaration.
//
// Zero evidence is the expected result everywhere; only executability is
// under test. Queries run through the production executor
// (NewClickHouseSourceExecutor) with ReadPlan.Bindings(), so a query
// cannot pass here in a shape production never runs.
func TestCatalogQueriesExecutableAgainstDeclaredSchema(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create declared table: %v\n%s", err, statement)
		}
	}

	plan := contextpacket.ReadPlan{
		OrgID:          "10000000-0000-4000-8000-000000000001",
		RepoID:         "20000000-0000-4000-8000-000000000002",
		RepoSlug:       "acme/coverage-service",
		Branch:         "main",
		CommitSHA:      "0123456789abcdef0123456789abcdef01234567",
		TaskRef:        "TASK-1",
		TimeWindowDays: 30,
	}
	executor := contextpacket.NewClickHouseSourceExecutor(query)

	// Queries that reference tables devhealthschema does not declare AT ALL
	// (CHAOS-3815). This list is two-sided: a listed query must STILL fail
	// (its table still undeclared), so declaring a table without deleting
	// its entries here breaks the build -- the list cannot silently rot
	// into hiding queries that now work.
	undeclaredTableQueries := map[string]string{
		"git_commits.v1":           "git_commits",
		"git_commit_files.v1":      "git_commits, git_commit_stats",
		"work_graph.v1":            "work_graph_edges",
		"ai_workflow_runs.v1":      "ai_workflow_runs",
		"ai_workflow_artifacts.v1": "ai_workflow_artifact_edges",
		"ai_review_outcomes.v1":    "work_graph_pr_review_outcome_edges",
		"file_hotspots.v1":         "file_hotspot_daily",
		"file_complexity.v1":       "file_complexity_snapshots",
	}

	for _, source := range contextpacket.SourceQueryCatalogV1 {
		evidence, err := executor.QueryEvidence(ctx, source, plan.Bindings())
		if tables, undeclared := undeclaredTableQueries[source.ID]; undeclared {
			if err == nil {
				t.Errorf("catalog query %s now executes against the declared schema; declare it covered and delete its CHAOS-3815 skip entry (%s)", source.ID, tables)
			}
			continue
		}
		if err != nil {
			t.Errorf("catalog query %s does not execute against the declared schema: %v", source.ID, err)
			continue
		}
		if len(evidence) != 0 {
			t.Errorf("catalog query %s returned %d rows from empty declared tables", source.ID, len(evidence))
		}
	}

	scopeRows, err := query.Query(ctx, contextpacket.RepositoryScopeQueryV1, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: plan.OrgID}, {Name: "repo_slug", Value: plan.RepoSlug},
	})
	if err != nil {
		t.Fatalf("RepositoryScopeQueryV1 does not execute against the declared schema: %v", err)
	}
	defer func() {
		if err := scopeRows.Close(); err != nil {
			t.Error(err)
		}
	}()
	if scopeRows.Next() {
		t.Fatal("RepositoryScopeQueryV1 returned a row from empty declared tables")
	}
	if err := scopeRows.Err(); err != nil {
		t.Fatal(err)
	}
}
