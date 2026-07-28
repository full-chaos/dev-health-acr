package contextpacket_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type branchSnapshotEvidenceClient struct{ sawHashedBranch bool }

func (c *branchSnapshotEvidenceClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if statement == contextpacket.RepositoryScopeQueryV1 {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service", "feature"}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT toString(id), repo FROM repos FINAL WHERE") {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service"}}}, nil
	}
	if !strings.Contains(statement, "FROM file_complexity_snapshots") {
		return &rowScanner{}, nil
	}
	feature := []any{"acr:v1:complexity:internal/x.go", "dev_health", "file_complexity", "internal/x.go", "internal/x.go", "", "native", 1.0, "complexity=7", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)}
	main := []any{"acr:v1:complexity:internal/x.go", "dev_health", "file_complexity", "internal/x.go", "internal/x.go", "", "native", 1.0, "complexity=99", time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)}
	branch, _ := bindingValue[string](bindings, "branch")
	branchHash, _ := bindingValue[string](bindings, "branch_hash")
	lookupHash, hasLookup := bindingValue[string](bindings, "evidence_lookup_hash")
	featureDigest := sha256.Sum256([]byte("feature"))
	if branch == "feature" && branchHash == "" && !hasLookup {
		return &rowScanner{rows: [][]any{feature}}, nil
	}
	if branch == "" && branchHash == hex.EncodeToString(featureDigest[:]) && len(lookupHash) == 64 && strings.Contains(statement, "ref_sha256 = {branch_hash:String}") {
		c.sawHashedBranch = true
		return &rowScanner{rows: [][]any{feature}}, nil
	}
	if branch == "" && branchHash == "" {
		return &rowScanner{rows: [][]any{main}}, nil
	}
	return nil, fmt.Errorf("unexpected file-complexity bindings: %#v", bindings)
}

func TestClickHouseEvidenceStore_resolves_branch_snapshot_with_non_unique_locator(t *testing.T) {
	client := &branchSnapshotEvidenceClient{}
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(contextpacket.NewCatalogClickHouseRows(client), contextpacket.EvidenceStoreOptions{Codec: fixtureEvidenceCodec(t)})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	bundle, err := store.ContextForTask(context.Background(), fixturePrincipal(), fixtureRequest("req-branch-snapshot", "feature", ""))
	if err != nil {
		t.Fatalf("emit branch snapshot: %v", err)
	}
	var handle string
	var emittedLabel string
	for _, evidence := range bundle.Evidence {
		if evidence.SourceVersion == "file_complexity.v1" && evidence.Source.EntityID == "internal/x.go" {
			handle = evidence.EvidenceRefID
			emittedLabel = evidence.Source.DisplayLabel
			break
		}
	}
	if handle == "" {
		t.Fatal("branch snapshot evidence was not emitted")
	}
	expanded, err := store.ResolveEvidence(context.Background(), fixturePrincipal(), handle)
	if err != nil || !client.sawHashedBranch || expanded.Structured["file_path"] != "internal/x.go" || expanded.Evidence.Citation != "complexity=7" || expanded.Evidence.Source.DisplayLabel != emittedLabel {
		t.Fatalf("expanded branch snapshot = %#v, hashed branch = %t, error = %v", expanded, client.sawHashedBranch, err)
	}
}

type repositoryWideEvidenceClient struct{ sawCanonicalReplayLabel bool }

func (c *repositoryWideEvidenceClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if statement == contextpacket.RepositoryScopeQueryV1 {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service", "main"}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT toString(id), repo FROM repos FINAL WHERE") {
		return &rowScanner{rows: [][]any{{"00000000-0000-0000-0000-000000000001", "example-org/widget-service"}}}, nil
	}
	if !strings.Contains(statement, "FROM git_commits") {
		return &rowScanner{}, nil
	}
	row := []any{"acr:v1:commit:abc123", "dev_health", "commit", "abc123", "Fix release blocker", "", "native", 1.0, "Fix release blocker", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)}
	if _, replay := bindingValue[string](bindings, "evidence_lookup_hash"); replay {
		if strings.Contains(statement, "concat(display_label, ' (repository-wide)') display_label") {
			row[4] = "Fix release blocker (repository-wide)"
			c.sawCanonicalReplayLabel = true
		}
	}
	return &rowScanner{rows: [][]any{row}}, nil
}

func TestClickHouseEvidenceStore_replays_repository_wide_label_as_hashed(t *testing.T) {
	client := &repositoryWideEvidenceClient{}
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(contextpacket.NewCatalogClickHouseRows(client), contextpacket.EvidenceStoreOptions{Codec: fixtureEvidenceCodec(t)})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	bundle, err := store.ContextForTask(context.Background(), fixturePrincipal(), fixtureRequest("req-repository-wide-replay", "main", ""))
	if err != nil {
		t.Fatalf("emit repository-wide evidence: %v", err)
	}
	var emitted contractsv1.EvidenceRef
	for _, evidence := range bundle.Evidence {
		if evidence.SourceVersion == "git_commits.v1" {
			emitted = evidence
			break
		}
	}
	if emitted.EvidenceRefID == "" || emitted.Source.DisplayLabel != "Fix release blocker (repository-wide)" {
		t.Fatalf("emitted repository-wide evidence = %#v", emitted)
	}
	expanded, err := store.ResolveEvidence(context.Background(), fixturePrincipal(), emitted.EvidenceRefID)
	if err != nil || !client.sawCanonicalReplayLabel || expanded.Evidence.Source.DisplayLabel != emitted.Source.DisplayLabel {
		t.Fatalf("expanded repository-wide evidence = %#v, canonical replay label = %t, error = %v", expanded, client.sawCanonicalReplayLabel, err)
	}
}
