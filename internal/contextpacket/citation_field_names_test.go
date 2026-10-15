package contextpacket

import "testing"

// The field names are derived from the catalog statements; this pins the
// derivation, so a catalog change that adds or drops a name= literal in a
// citation expression must update this table too.
func TestCitationFieldNamesAreTheNameLiteralsOfEachCatalogCitation(t *testing.T) {
	want := map[string]string{
		"ai_review_outcomes.v1":             `\b(review_outcome_id)=`,
		"ai_workflow_artifacts.v1":          `\b(artifact_id)=`,
		"ai_workflow_runs.v1":               `\b(run_id)=`,
		"ci_pipeline_runs.v1":               `\b(run_id)=`,
		"deployment_incident_provenance.v1": `\b(edge_id)=`,
		"deployments.v1":                    `\b(deployment_id|release_ref|release_ref_confidence|status)=`,
		"file_complexity.v1":                `\b(complexity)=`,
		"file_hotspots.v1":                  `\b(churn|complexity)=`,
		"incidents.v1":                      `\b(severity|status)=`,
		"pull_request_reviews.v1":           `\b(review_id)=`,
		"work_graph.v1":                     `\b(edge_id)=`,
		"work_item_dependencies.v1":         `\b(dependency)=`,
		"work_items.v1":                     `\b(work_item_id)=`,
	}
	if len(citationFieldNames) != len(want) {
		t.Errorf("sources with citation field names = %d, want %d", len(citationFieldNames), len(want))
	}
	for source, pattern := range want {
		got, ok := citationFieldNames[source]
		if !ok || got.String() != pattern {
			t.Errorf("%s field names = %v, want %s", source, got, pattern)
		}
	}
	for _, source := range []string{"git_commits.v1", "pull_requests.v1", "git_commit_files.v1"} {
		if _, ok := citationFieldNames[source]; ok {
			t.Errorf("%s writes no name= literal and must keep every word of its citation", source)
		}
	}
}
