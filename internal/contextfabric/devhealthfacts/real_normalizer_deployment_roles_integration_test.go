package devhealthfacts_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// real_normalizer_deployment_rows.json holds the deploymentRow values the ops
// provider sync's own normalizers emit (dev-health-ops internal/providersync
// at bc9d9bbfa6: normalizeGitHubDeployment + deploymentLifecycleFromStatuses +
// deploymentLatestStatus + chooseDeploymentPullRequest, and
// normalizeGitLabDeployment + resolveGitLabDeploymentMergeRequest), dumped
// from a throwaway test over GitHub and GitLab API-shaped payloads. They are
// not hand-authored: release_ref is the deployment id at confidence 0.3 (no
// deployed commit sha is stored); pull_request_number is the stored tie.
//
//go:embed testdata/real_normalizer_deployment_rows.json
var realNormalizerDeploymentRows []byte

type realDeploymentRow struct {
	RepoID               string  `json:"repo_id"`
	DeploymentID         string  `json:"deployment_id"`
	Status               *string `json:"status"`
	Environment          *string `json:"environment"`
	StartedAt            *string `json:"started_at"`
	FinishedAt           *string `json:"finished_at"`
	DeployedAt           *string `json:"deployed_at"`
	MergedAt             *string `json:"merged_at"`
	PullRequestNumber    *uint32 `json:"pull_request_number"`
	ReleaseRef           string  `json:"release_ref"`
	ReleaseRefConfidence float64 `json:"release_ref_confidence"`
	OrgID                string  `json:"org_id"`
	LastSynced           string  `json:"last_synced"`
}

type rowsStore struct {
	scope    contractsv1.ResolvedScope
	evidence []contractsv1.EvidenceRef
}

func (s rowsStore) ResolveScope(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	return s.scope, nil
}

func (s rowsStore) ContextForTask(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return storage.EvidenceBundle{ResolvedScope: s.scope, Evidence: s.evidence, QueryVersion: contextpacket.QueryVersionV1}, nil
}

func (s rowsStore) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
}

// A failed deployment row as the real GitHub and GitLab normalizers store it
// is served as pressure and its stored pull request as a relation (the match
// tier is not stored, so never a cause), through the real catalog statements
// on ClickHouse; the two stored links are counted as demoted.
func TestRealNormalizerDeploymentRowsServeFailedDeploymentsAsPressureAndTheirPullRequestAsRelation(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, nil)
	for _, statement := range devhealthschema.DDL("repos", "deployments", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	var rows []realDeploymentRow
	if err := json.Unmarshal(realNormalizerDeploymentRows, &rows); err != nil || len(rows) != 4 {
		t.Fatalf("fixture rows: %d, %v", len(rows), err)
	}
	instant := func(value *string) *time.Time {
		if value == nil {
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			t.Fatalf("fixture time %q: %v", *value, err)
		}
		return &parsed
	}
	org, repoID := rows[0].OrgID, rows[0].RepoID
	synced := instant(&rows[0].LastSynced)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoID, org, "acme/checkout", "github", *synced); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	for _, row := range rows {
		if err := direct.Exec(ctx, `INSERT INTO deployments (repo_id, deployment_id, status, environment, started_at, finished_at, deployed_at, merged_at, pull_request_number, release_ref, release_ref_confidence, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			row.RepoID, row.DeploymentID, row.Status, row.Environment, instant(row.StartedAt), instant(row.FinishedAt), instant(row.DeployedAt), instant(row.MergedAt), row.PullRequestNumber, row.ReleaseRef, row.ReleaseRefConfidence, *instant(&row.LastSynced), row.OrgID); err != nil {
			t.Fatalf("seed deployment %s: %v", row.DeploymentID, err)
		}
	}
	titles := map[uint32]string{41: "One-page checkout", 42: "Regression: delivery slots show past times", 6: "Saved payment methods", 7: "Hotfix: gift cards double-applied"}
	for number, title := range titles {
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoID, org, number, title, "merged", *synced, *synced, "feature", ""); err != nil {
			t.Fatalf("seed pull request %d: %v", number, err)
		}
	}

	plan := contextpacket.ReadPlan{OrgID: org, RepoID: repoID, RepoSlug: "acme/checkout"}
	catalog, err := contextpacket.ExecuteCatalog(ctx, contextpacket.NewClickHouseSourceExecutor(query), plan)
	if err != nil {
		t.Fatalf("execute catalog: %v", err)
	}
	request := contractsv1.ContextPacketRequest{SchemaVersion: contractsv1.ContextPacketRequestSchema, RequestID: "req-real-normalizer", Goal: "checkout regression", Repository: contractsv1.RepositoryRef{Slug: "acme/checkout"}, Scope: contractsv1.RequestedScope{Branch: "main"},
		Options: contractsv1.PacketOptions{MaxItems: 20, MaxOutputTokens: 4000, MaxSerializedBytes: 65536}, Client: contractsv1.ClientInfo{Name: "test", Version: "1.0"}}
	store := rowsStore{scope: contractsv1.ResolvedScope{RepoID: repoID, RepoSlug: "acme/checkout", Resolution: contractsv1.ScopeBranchFiltered}, evidence: catalog.Evidence}
	packet, err := contextpacket.NewAssembler(store, contextpacket.Options{Now: func() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0"}).
		Assemble(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"acme/checkout"}}, request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	served := map[string]contractsv1.PacketCategory{}
	for _, item := range packet.Items {
		for _, related := range item.RelatedEntities {
			served[related.Type+":"+related.ID] = item.Category
		}
	}
	want := map[string]contractsv1.PacketCategory{
		"deployment:9001": contractsv1.CategoryState, "deployment:9002": contractsv1.CategoryPressure,
		"deployment:7001": contractsv1.CategoryState, "deployment:7002": contractsv1.CategoryPressure,
		"pull_request:41": contractsv1.CategoryEvidence, "pull_request:42": contractsv1.CategoryEvidence,
		"pull_request:6": contractsv1.CategoryEvidence, "pull_request:7": contractsv1.CategoryEvidence,
	}
	if !slices.Contains(packet.Warnings, "cause_links_demoted:2:link_tier_not_stored") {
		t.Errorf("the two stored pull request links are not counted as demoted: %v", packet.Warnings)
	}
	for key, category := range want {
		if served[key] != category {
			t.Errorf("%s served as %q, want %q (served: %v)", key, served[key], category, served)
		}
	}
}
