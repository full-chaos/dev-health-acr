package contextpacket_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestCatalogClickHouseRows_requires_unique_exact_locator_match(t *testing.T) {
	digest := sha256.Sum256([]byte("acr:v1:ci:opaque-reference"))
	lookupHash := hex.EncodeToString(digest[:])
	branchDigest := sha256.Sum256([]byte("main"))
	asOf := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	var statement string
	var bindings []contextpacket.ClickHouseBinding
	rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 1}, statement: &statement, bindings: &bindings})
	references, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, contextpacket.EvidenceReferenceLookup{QueryID: "ci_pipeline_runs.v1", LookupHash: lookupHash, BranchHash: hex.EncodeToString(branchDigest[:]), AsOf: &asOf})
	if err != nil || len(references) != 1 {
		t.Fatalf("references = %d, error = %v, want one exact match", len(references), err)
	}
	boundHash, ok := bindingValue[string](bindings, "evidence_lookup_hash")
	if !strings.Contains(statement, "lower(hex(SHA256(concat(") || !strings.Contains(statement, "= {evidence_lookup_hash:String} LIMIT 2") || !ok || boundHash != lookupHash {
		t.Fatalf("statement = %q, locator binding = %q, present = %t", statement, boundHash, ok)
	}
	expectedBindings := []string{"org_id", "repo_id", "repo_slug", "branch", "branch_hash", "commit_sha", "task_ref", "files", "as_of", "time_window_days", "evidence_lookup_hash"}
	if len(bindings) != len(expectedBindings) {
		t.Fatalf("bindings = %#v", bindings)
	}
	for index, name := range expectedBindings {
		if bindings[index].Name != name {
			t.Fatalf("binding %d = %q, want %q", index, bindings[index].Name, name)
		}
	}
	if bindings[4].Value != hex.EncodeToString(branchDigest[:]) || bindings[8].Value != asOf {
		t.Fatalf("scope bindings = %#v", bindings)
	}
	rows = contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 2}})
	if _, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, contextpacket.EvidenceReferenceLookup{QueryID: "ci_pipeline_runs.v1", LookupHash: lookupHash}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ambiguous exact match error = %v, want generic not found", err)
	}
}

func TestCatalogClickHouseRows_preserves_legacy_locator_saturation_guard(t *testing.T) {
	rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 501}})
	if _, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, contextpacket.EvidenceReferenceLookup{QueryID: "ci_pipeline_runs.v1"}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("legacy saturation error = %v, want generic not found", err)
	}
}

func TestCatalogClickHouseRows_ignores_branch_digest_for_repository_wide_replay(t *testing.T) {
	var statement string
	rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 1}, statement: &statement})
	_, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, contextpacket.EvidenceReferenceLookup{QueryID: "work_items.v1", LookupHash: strings.Repeat("a", 64), BranchHash: strings.Repeat("b", 64), RepositoryWide: true})
	if err != nil {
		t.Fatalf("resolve repository-wide replay: %v", err)
	}
	if strings.Contains(statement, "branch_hash") || strings.Contains(statement, "branch_sha256") {
		t.Fatalf("repository-wide replay was branch-filtered: %s", statement)
	}
}

func TestCatalogClickHouseRows_separates_raw_branch_catalog_from_digest_replay(t *testing.T) {
	tests := []struct {
		queryID       string
		catalogBranch string
		replayBranch  string
	}{
		{"repository_freshness.v1", "ref = {branch:String}", "ref_sha256 = {branch_hash:String}"},
		{"pull_requests.v1", "p.head_branch = {branch:String}", "p.head_branch_sha256 = {branch_hash:String}"},
		{"pull_request_reviews.v1", "p.head_branch = {branch:String}", "p.head_branch_sha256 = {branch_hash:String}"},
		{"ci_pipeline_runs.v1", "c.branch = {branch:String}", "c.branch_sha256 = {branch_hash:String}"},
		{"file_complexity.v1", "ref = {branch:String}", "ref_sha256 = {branch_hash:String}"},
	}
	for _, test := range tests {
		t.Run(test.queryID, func(t *testing.T) {
			var catalogStatement string
			for _, query := range contextpacket.SourceQueryCatalogV1 {
				if query.ID == test.queryID {
					catalogStatement = query.Statement
					break
				}
			}
			if !strings.Contains(catalogStatement, test.catalogBranch) || strings.Contains(catalogStatement, "branch_hash") || strings.Contains(catalogStatement, "SHA256(") {
				t.Fatalf("catalog query mixes raw and digest branch lookup: %s", catalogStatement)
			}

			var replayStatement string
			rows := contextpacket.NewCatalogClickHouseRows(locatorQueryClient{rows: &locatorQueryRows{count: 1}, statement: &replayStatement})
			_, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-server-derived", RepoSlug: "example-org/widget-service"}, contextpacket.EvidenceReferenceLookup{QueryID: test.queryID, LookupHash: strings.Repeat("a", 64), BranchHash: strings.Repeat("b", 64)})
			if err != nil {
				t.Fatalf("resolve digest replay: %v", err)
			}
			if !strings.Contains(replayStatement, test.replayBranch) || strings.Contains(replayStatement, " = {branch:String}") || strings.Contains(replayStatement, "SHA256(ref)") || strings.Contains(replayStatement, "force_optimize_projection_name") {
				t.Fatalf("replay query is not digest-indexed and independently bounded: %s", replayStatement)
			}
		})
	}
}
