package contextpacket_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestSourceCatalog_joins_repositories_for_raw_evidence_tables(t *testing.T) {
	for _, id := range []string{"git_commits.v1", "git_commit_files.v1", "pull_requests.v1", "pull_request_reviews.v1", "ci_pipeline_runs.v1", "deployments.v1"} {
		query := catalogQuery(t, id)
		for _, predicate := range []string{"INNER JOIN repos AS repo FINAL", "repo.org_id = {org_id:String}", "repo.id = {repo_id:UUID}"} {
			if !strings.Contains(query.Statement, predicate) {
				t.Fatalf("%s missing %q", id, predicate)
			}
		}
	}
}

func TestSourceCatalog_readsCanonicalOperationalIncidents(t *testing.T) {
	// Given
	query := catalogQuery(t, "incidents.v1")

	// Then
	for _, projection := range []string{
		"FROM operational_incidents AS i FINAL",
		"INNER JOIN operational_service_repository_mappings AS m FINAL ON i.org_id = m.org_id AND i.service_id = m.service_id",
		"i.org_id = {org_id:String}",
		"m.org_id = {org_id:String}",
		"m.repo_id = {repo_id:UUID}",
		"m.is_active = 1",
		"m.valid_from <= coalesce({as_of:Nullable(DateTime64(3, 'UTC'))}, now64(6))",
		"(m.valid_to IS NULL OR m.valid_to > coalesce({as_of:Nullable(DateTime64(3, 'UTC'))}, now64(6)))",
		"i.is_deleted = 0",
		"i.id entity_id",
		"i.title display_label",
		"ifNull(i.source_url, '') safe_uri",
		"coalesce(i.started_at, i.source_event_at, i.observed_at) observed_at",
	} {
		if !strings.Contains(query.Statement, projection) {
			t.Fatalf("incidents.v1 missing canonical operational projection %q: %s", projection, query.Statement)
		}
	}
	if strings.Contains(query.Statement, "FROM incidents AS i") || strings.Contains(query.Statement, "i.repo_id") {
		t.Fatalf("incidents.v1 uses retired incident storage or unavailable repository column: %s", query.Statement)
	}
	if !strings.Contains(query.Statement, "LIMIT 1 BY m.repo_id, i.id") {
		t.Fatalf("incidents.v1 does not deduplicate mapped incidents per repository: %s", query.Statement)
	}
	if !strings.Contains(query.Statement, "greatest(0, least(1, coalesce(m.relationship_confidence, i.relationship_confidence, 1.0))) confidence") {
		t.Fatalf("incidents.v1 does not clamp mapping confidence: %s", query.Statement)
	}
	if !strings.Contains(query.Statement, "ORDER BY m.repo_id, i.id, m.relationship_confidence DESC, i.last_synced DESC, m.id ASC") {
		t.Fatalf("incidents.v1 does not deterministically prefer the highest-confidence mapping: %s", query.Statement)
	}
}

func TestSourceCatalog_places_alias_before_FINAL(t *testing.T) {
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if strings.Contains(query.Statement, "FINAL AS") {
			t.Fatalf("%s uses invalid FINAL alias ordering: %s", query.ID, query.Statement)
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

func TestSourceCatalog_uses_exact_reviewed_provenance_mapping_per_source(t *testing.T) {
	tests := []struct {
		name            string
		sourceID        string
		expectedMapping string
	}{
		{
			name:            "AI workflow artifact aliases and native evidence",
			sourceID:        "ai_workflow_artifacts.v1",
			expectedMapping: "multiIf(source = 'pr_body', 'explicit_text', source = 'branch_name', 'heuristic', source = 'native', 'native', '') provenance",
		},
		{
			name:            "AI review native evidence",
			sourceID:        "ai_review_outcomes.v1",
			expectedMapping: "multiIf(source = 'native', 'native', '') provenance",
		},
		{
			name:            "deployment incident native and inferred evidence",
			sourceID:        "deployment_incident_provenance.v1",
			expectedMapping: "multiIf(source = 'native', 'native', source = 'heuristic', 'heuristic', '') provenance",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			statement := catalogQuery(t, tt.sourceID).Statement

			// Then
			if strings.Count(statement, "multiIf(source =") != 1 || !strings.Contains(statement, tt.expectedMapping) {
				t.Fatalf("%s does not use exact reviewed provenance mapping %q: %s", tt.sourceID, tt.expectedMapping, statement)
			}
		})
	}
}

func TestSourceCatalog_leaves_unknown_raw_provenance_invalid(t *testing.T) {
	for _, sourceID := range []string{"ai_workflow_artifacts.v1", "ai_review_outcomes.v1", "deployment_incident_provenance.v1"} {
		t.Run(sourceID, func(t *testing.T) {
			// When
			statement := catalogQuery(t, sourceID).Statement

			// Then
			if !strings.Contains(statement, ", '') provenance") {
				t.Fatalf("%s does not end its provenance mapping with an invalid fallback: %s", sourceID, statement)
			}
			if strings.Contains(statement, "source provenance") {
				t.Fatalf("%s still projects untrusted raw provenance: %s", sourceID, statement)
			}
		})
	}
}

func TestSourceCatalog_incidents_does_not_launder_unknown_provenance_as_native(t *testing.T) {
	// When
	statement := catalogQuery(t, "incidents.v1").Statement

	// Then
	expectedMapping := "multiIf(m.relationship_provenance = 'bounded_service_repository_heuristic', 'heuristic', m.relationship_provenance IN ('native', 'native_repository_context', 'admin_configuration', 'pagerduty_service_metadata', 'compass_service_catalog'), 'native', m.relationship_provenance IN ('explicit_text', 'heuristic', 'derived'), m.relationship_provenance, '') provenance"
	if strings.Count(statement, "multiIf(m.relationship_provenance") != 1 || !strings.Contains(statement, expectedMapping) {
		t.Fatalf("incidents.v1 does not use exact fail-invalid relationship provenance mapping %q: %s", expectedMapping, statement)
	}
	if strings.Contains(statement, "isNull(m.relationship_provenance)") || strings.Contains(statement, "m.relationship_provenance = '', 'native'") {
		t.Fatalf("incidents.v1 still launders absent relationship provenance as native: %s", statement)
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

func TestSourceCatalog_normalizes_confidence_to_float64(t *testing.T) {
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if !strings.Contains(query.Statement, "toFloat64(confidence) confidence") {
			t.Fatalf("%s does not normalize confidence to Float64: %s", query.ID, query.Statement)
		}
	}
}

// Commit-scoped sources are no longer skipped wholesale on a repo-wide request.
// They may run without a commit ONLY when the statement provably tolerates an
// empty commit_sha. This asserts that safety invariant rather than the old
// blanket gate, so a future commit-scoped source cannot be made repo-wide
// readable without the predicate that makes it safe.
func TestSourceCatalog_runs_commit_sources_without_commit_only_when_sql_allows(t *testing.T) {
	plan := contextpacket.ReadPlan{OrgID: "org", RepoID: "00000000-0000-0000-0000-000000000001", RepoSlug: "owner/repo", Branch: "main"}
	executor := &catalogRecorder{}

	result, err := contextpacket.ExecuteCatalog(context.Background(), executor, plan)

	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	commitSources := 0
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.Scope != contextpacket.EvidenceScopeCommit {
			continue
		}
		commitSources++
		if executor.queried[query.ID] {
			if !strings.Contains(query.Statement, "{commit_sha:String} = ''") {
				t.Fatalf("%s ran without a commit but its statement has no empty-commit predicate: %s", query.ID, query.Statement)
			}
			continue
		}
		if !containsUnavailable(result.Unavailable, query.ID, "commit_scope_not_requested") {
			t.Fatalf("%s was gated but not disclosed accurately: %#v", query.ID, result.Unavailable)
		}
	}
	if commitSources == 0 {
		t.Fatal("no commit-scoped sources in catalog; this test would assert nothing")
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
