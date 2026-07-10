package contextpacket_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestSourceCatalog_joins_repositories_for_raw_evidence_tables(t *testing.T) {
	for _, id := range []string{"git_commits.v1", "git_commit_files.v1", "pull_requests.v1", "pull_request_reviews.v1", "ci_pipeline_runs.v1", "deployments.v1", "incidents.v1"} {
		query := catalogQuery(t, id)
		for _, predicate := range []string{"INNER JOIN repos FINAL AS repo", "repo.org_id = {org_id:String}", "repo.id = {repo_id:UUID}"} {
			if !strings.Contains(query.Statement, predicate) {
				t.Fatalf("%s missing %q", id, predicate)
			}
		}
	}
}

func TestSourceCatalog_compares_uuid_organizations_as_strings(t *testing.T) {
	for _, id := range []string{"ai_workflow_runs.v1", "ai_workflow_artifacts.v1", "ai_review_outcomes.v1", "deployment_incident_provenance.v1"} {
		statement := catalogQuery(t, id).Statement
		if !strings.Contains(statement, "toString(org_id) = {org_id:String}") || strings.Contains(statement, "toUUID(") {
			t.Fatalf("%s does not safely compare UUID org scope: %s", id, statement)
		}
	}
}

func TestSourceCatalog_exports_standard_evidence_columns(t *testing.T) {
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		for _, alias := range []string{" evidence_ref_id", " system", " entity_type", " entity_id", " display_label", " safe_uri", " provenance", " confidence", " citation", " observed_at"} {
			if strings.Count(query.Statement, alias) < 2 {
				t.Fatalf("%s missing projection alias %q: %s", query.ID, alias, query.Statement)
			}
		}
	}
}

func TestSourceCatalog_skips_commit_sources_without_commit_scope(t *testing.T) {
	plan := contextpacket.ReadPlan{OrgID: "org", RepoID: "00000000-0000-0000-0000-000000000001", RepoSlug: "owner/repo", Branch: "main"}
	executor := &catalogRecorder{}

	result, err := contextpacket.ExecuteCatalog(context.Background(), executor, plan)

	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	if executor.queried["git_commits.v1"] || executor.queried["git_commit_files.v1"] {
		t.Fatalf("commit scoped queries executed without a commit: %#v", executor.queried)
	}
	for _, source := range []string{"git_commits.v1", "git_commit_files.v1"} {
		if !containsUnavailable(result.Unavailable, source, "commit_scope_not_requested") {
			t.Fatalf("%s was not disclosed as skipped: %#v", source, result.Unavailable)
		}
	}
}

type catalogRecorder struct{ queried map[string]bool }

func (r *catalogRecorder) QueryEvidence(_ context.Context, query contextpacket.SourceQuery, _ []contextpacket.ClickHouseBinding) ([]contractsv1.EvidenceRef, error) {
	if r.queried == nil {
		r.queried = map[string]bool{}
	}
	r.queried[query.ID] = true
	return nil, nil
}

func catalogQuery(t *testing.T, id string) contextpacket.SourceQuery {
	t.Helper()
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == id {
			return query
		}
	}
	t.Fatalf("catalog query %s missing", id)
	return contextpacket.SourceQuery{}
}
