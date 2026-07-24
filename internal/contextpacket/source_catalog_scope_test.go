package contextpacket

import (
	"context"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type catalogScopeExecutor struct {
	evidenceBySource map[string][]contractsv1.EvidenceRef
	queried          []string
}

func (e *catalogScopeExecutor) QueryEvidence(_ context.Context, query SourceQuery, _ []ClickHouseBinding) ([]contractsv1.EvidenceRef, error) {
	e.queried = append(e.queried, query.ID)
	return e.evidenceBySource[query.ID], nil
}

func TestExecuteCatalog_labels_repository_wide_evidence_when_branch_is_requested(t *testing.T) {
	// Given
	executor := &catalogScopeExecutor{evidenceBySource: map[string][]contractsv1.EvidenceRef{
		"deployments.v1": {{Source: contractsv1.EvidenceSource{DisplayLabel: "production deployment"}, ObservedAt: time.Now().UTC()}},
	}}
	plan := ReadPlan{Branch: "fix/acr-scope-gating"}

	// When
	result, err := ExecuteCatalog(context.Background(), executor, plan)

	// Then
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	if !slices.Contains(executor.queried, "deployments.v1") {
		t.Fatal("deployments.v1 was not queried for a branch-scoped request")
	}
	if result.Evidence[0].Source.DisplayLabel != "production deployment (repository-wide)" {
		t.Fatalf("display label = %q, want repository-wide label", result.Evidence[0].Source.DisplayLabel)
	}
	if result.Evidence[0].Metadata["scope_breadth"] != "repository-wide" {
		t.Fatalf("scope breadth = %#v, want repository-wide", result.Evidence[0].Metadata["scope_breadth"])
	}
	foundDeploymentWatermark := false
	for _, watermark := range result.Watermarks {
		if watermark.Source == "deployments.v1" && watermark.Status == "fresh" {
			foundDeploymentWatermark = true
			break
		}
	}
	if !foundDeploymentWatermark {
		t.Fatalf("watermarks = %#v, want deployments.v1 watermark", result.Watermarks)
	}
}

func TestExecuteCatalog_queries_repo_wide_commit_sources_when_commit_is_not_requested(t *testing.T) {
	// Given
	executor := &catalogScopeExecutor{evidenceBySource: map[string][]contractsv1.EvidenceRef{
		"git_commits.v1":      {{ObservedAt: time.Now().UTC()}},
		"git_commit_files.v1": {{ObservedAt: time.Now().UTC()}},
	}}

	// When
	_, err := ExecuteCatalog(context.Background(), executor, ReadPlan{})

	// Then
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	for _, source := range []string{"git_commits.v1", "git_commit_files.v1"} {
		if !slices.Contains(executor.queried, source) {
			t.Fatalf("%s was not queried without a commit scope", source)
		}
	}
}

func TestCatalogScopeUnavailableReason_keeps_unverified_commit_source_gated_when_commit_is_not_requested(t *testing.T) {
	// Given
	query := SourceQuery{ID: "commit_requires_sha.v1", Scope: EvidenceScopeCommit}

	// When
	reason := catalogScopeUnavailableReason(query, ReadPlan{})

	// Then
	if reason != "commit_scope_not_requested" {
		t.Fatalf("reason = %q, want commit_scope_not_requested", reason)
	}
}
