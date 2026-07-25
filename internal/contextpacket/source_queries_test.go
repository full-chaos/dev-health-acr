package contextpacket_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestDeploymentsSourceQuery_usesTruthfulCitationWhenReleaseReferenceIsEmpty(t *testing.T) {
	// Given
	var deploymentQuery contextpacket.SourceQuery
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "deployments.v1" {
			deploymentQuery = query
			break
		}
	}

	// When
	const expectedCitation = "if(d.release_ref != '', concat('release_ref=', d.release_ref), concat('deployment_id=', d.deployment_id)) citation"
	citationIsTruthy := strings.Contains(deploymentQuery.Statement, expectedCitation)
	citationUsesNullableStatus := strings.Contains(deploymentQuery.Statement, "ifNull(d.status, '') citation")

	// Then
	if !citationIsTruthy || citationUsesNullableStatus {
		t.Fatal("deployments.v1 must cite its release reference or deployment identity, never nullable status")
	}
}

func TestGitCommitsSourceQuery_usesCommitHashWhenMessageExceedsEvidenceBounds(t *testing.T) {
	// Given
	var commitQuery contextpacket.SourceQuery
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "git_commits.v1" {
			commitQuery = query
			break
		}
	}

	// When
	const label = "if(lengthUTF8(ifNull(c.message, '')) BETWEEN 1 AND 1000, ifNull(c.message, ''), concat('commit ', c.hash)) display_label"
	const citation = "if(lengthUTF8(ifNull(c.message, '')) BETWEEN 1 AND 2000, ifNull(c.message, ''), concat('commit ', c.hash)) citation"
	usesBoundedCommitMessage := strings.Contains(commitQuery.Statement, label) && strings.Contains(commitQuery.Statement, citation)

	// Then
	if !usesBoundedCommitMessage {
		t.Fatal("git_commits.v1 must use the real commit hash when a message violates evidence bounds")
	}
}

func TestFileHotspotsSourceQuery_usesDirectFactsFromLatestReplacementRun(t *testing.T) {
	// Given
	var hotspotQuery contextpacket.SourceQuery
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "file_hotspots.v1" {
			hotspotQuery = query
			break
		}
	}

	// When
	const directFactConfidence = "1.0 confidence, concat('churn=', toString(churn_loc_30d), ', complexity=', toString(cyclomatic_total)) citation"
	usesDirectFactConfidence := strings.Contains(hotspotQuery.Statement, directFactConfidence)

	// Then
	if !usesDirectFactConfidence {
		t.Fatal("file_hotspots.v1 must use direct-fact confidence and measures from the selected replacement run")
	}
}

func TestCatalogSourceQueries_useTruthfulIdentityFallbacksForOptionalText(t *testing.T) {
	// Given
	tests := []struct {
		sourceID string
		expected string
	}{
		{"repository_freshness.v1", "if(lengthUTF8(ifNull(repo, '')) BETWEEN 1 AND 1000, ifNull(repo, ''), concat('repository ', toString(id))) display_label"},
		{"work_items.v1", "if(lengthUTF8(ifNull(title, '')) BETWEEN 1 AND 1000, ifNull(title, ''), concat('work item ', work_item_id)) display_label"},
		{"work_item_dependencies.v1", "if(lengthUTF8(ifNull(d.relationship_type_raw, '')) BETWEEN 1 AND 2000, ifNull(d.relationship_type_raw, ''), concat('dependency=', d.source_work_item_id, ':', d.target_work_item_id)) citation"},
		{"git_commit_files.v1", "if(lengthUTF8(ifNull(c.file_path, '')) BETWEEN 1 AND 1000, ifNull(c.file_path, ''), concat('commit file ', c.commit_hash)) display_label"},
		{"pull_requests.v1", "if(lengthUTF8(ifNull(p.state, '')) BETWEEN 1 AND 2000, ifNull(p.state, ''), concat('PR #', toString(p.number))) citation"},
		{"pull_request_reviews.v1", "if(lengthUTF8(ifNull(r.state, '')) BETWEEN 1 AND 2000, ifNull(r.state, ''), concat('review_id=', r.review_id)) citation"},
		{"ci_pipeline_runs.v1", "if(lengthUTF8(ifNull(c.status, '')) BETWEEN 1 AND 2000, ifNull(c.status, ''), concat('run_id=', c.run_id)) citation"},
		{"work_graph.v1", "if(lengthUTF8(ifNull(evidence, '')) BETWEEN 1 AND 2000, ifNull(evidence, ''), concat('edge_id=', edge_id)) citation"},
		{"ai_workflow_runs.v1", "if(lengthUTF8(ifNull(status, '')) BETWEEN 1 AND 2000, ifNull(status, ''), concat('run_id=', run_id)) citation"},
		{"ai_workflow_artifacts.v1", "if(lengthUTF8(ifNull(evidence, '')) BETWEEN 1 AND 2000, ifNull(evidence, ''), concat('artifact_id=', artifact_id)) citation"},
		{"ai_review_outcomes.v1", "if(lengthUTF8(trimBoth(concat(ifNull(outcome, ''), ifNull(evidence, '')))) BETWEEN 1 AND 2000, concat(ifNull(outcome, ''), ': ', ifNull(evidence, '')), concat('review_outcome_id=', review_outcome_id)) citation"},
		{"deployments.v1", "if(lengthUTF8(ifNull(d.environment, '')) BETWEEN 1 AND 1000, concat(ifNull(d.environment, ''), ' deployment'), concat('deployment ', d.deployment_id)) display_label"},
		{"deployment_incident_provenance.v1", "if(lengthUTF8(ifNull(evidence, '')) BETWEEN 1 AND 2000, ifNull(evidence, ''), concat('edge_id=', edge_id)) citation"},
	}

	// When
	queries := make(map[string]string, len(contextpacket.SourceQueryCatalogV1))
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		queries[query.ID] = query.Statement
	}

	// Then
	for _, test := range tests {
		t.Run(test.sourceID, func(t *testing.T) {
			if !strings.Contains(queries[test.sourceID], test.expected) {
				t.Fatalf("%s must use a truthful identity fallback for required evidence text", test.sourceID)
			}
		})
	}
}

func TestWorkItemsSourceQuery_omitsUnsafeOptionalURI(t *testing.T) {
	// Given
	var workItemsQuery contextpacket.SourceQuery
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "work_items.v1" {
			workItemsQuery = query
			break
		}
	}

	// When
	const safeURI = "if(lengthUTF8(ifNull(url, '')) <= 2048 AND match(ifNull(url, ''), '^[A-Za-z][A-Za-z0-9+.-]*:'), ifNull(url, ''), '') safe_uri"
	preservesOnlyBoundedSchemeURI := strings.Contains(workItemsQuery.Statement, safeURI)

	// Then
	if !preservesOnlyBoundedSchemeURI {
		t.Fatal("work_items.v1 must omit optional URLs that cannot satisfy evidence URI bounds")
	}
}

func TestAIWorkflowRunsSourceQuery_usesRunIDForBlankCompositeLabel(t *testing.T) {
	// Given
	var workflowRunsQuery contextpacket.SourceQuery
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if query.ID == "ai_workflow_runs.v1" {
			workflowRunsQuery = query
			break
		}
	}

	// When
	const label = "if(lengthUTF8(trimBoth(concat(ifNull(provider, ''), ' ', ifNull(run_kind, ''), ' ', ifNull(status, '')))) BETWEEN 1 AND 1000, concat(ifNull(provider, ''), ' ', ifNull(run_kind, ''), ' ', ifNull(status, '')), concat('AI workflow ', run_id)) display_label"
	usesRunIDForBlankComposite := strings.Contains(workflowRunsQuery.Statement, label)

	// Then
	if !usesRunIDForBlankComposite {
		t.Fatal("ai_workflow_runs.v1 must use its real run ID when descriptive fields are blank")
	}
}
