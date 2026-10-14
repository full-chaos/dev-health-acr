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

// real_normalizer_deployment_rows_tiered.json holds the deploymentRow values the
// ops provider sync's own normalizers emit after migration 126 (dev-health-ops
// f850e8f974: normalizeGitHubDeployment + deploymentLifecycleFromStatuses +
// deploymentLatestStatus + chooseDeploymentPullRequest, normalizeGitLabDeployment
// + resolveGitLabDeploymentMergeRequest), dumped from a throwaway test over
// API-shaped payloads: a GitHub exact merge sha match, a merged pull request
// that is not the merge commit, a first-listed unmerged pull request, no link,
// and a GitLab merged merge request with and without a link.
//
//go:embed testdata/real_normalizer_deployment_rows_tiered.json
var realNormalizerTieredRows []byte

type tieredDeploymentRow struct {
	realDeploymentRow
	PullRequestTier string `json:"pull_request_tier"`
}

func TestRealNormalizerTiersReachTheCauseRoleOnlyWhenTheTierIsStrongOnRealClickHouse(t *testing.T) {
	var rows []tieredDeploymentRow
	if err := json.Unmarshal(realNormalizerTieredRows, &rows); err != nil || len(rows) != 6 {
		t.Fatalf("fixture rows: %d, %v", len(rows), err)
	}
	for _, shape := range []struct {
		name         string
		withColumn   bool
		wantCause    []string
		wantRelation []string
		wantAbsent   bool
	}{
		{"table with the tier column", true, []string{"pull_request:41", "pull_request:42", "pull_request:7"}, []string{"pull_request:43"}, false},
		{"table before the migration", false, nil, []string{"pull_request:41", "pull_request:42", "pull_request:43", "pull_request:7"}, true},
	} {
		t.Run(shape.name, func(t *testing.T) {
			contextpacket.ResetStatementShapeProbesForTest()
			ctx := context.Background()
			query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, nil)
			for _, statement := range devhealthschema.DDL("repos", "deployments", "git_pull_requests") {
				if err := direct.Exec(ctx, statement); err != nil {
					t.Fatalf("create table: %v\n%s", err, statement)
				}
			}
			if !shape.withColumn {
				if err := direct.Exec(ctx, `ALTER TABLE deployments DROP COLUMN pull_request_tier`); err != nil {
					t.Fatalf("drop the tier column: %v", err)
				}
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
				columns, values := `repo_id, deployment_id, status, environment, started_at, finished_at, deployed_at, merged_at, pull_request_number, release_ref, release_ref_confidence, last_synced, org_id`,
					[]any{row.RepoID, row.DeploymentID, row.Status, row.Environment, instant(row.StartedAt), instant(row.FinishedAt), instant(row.DeployedAt), instant(row.MergedAt), row.PullRequestNumber, row.ReleaseRef, row.ReleaseRefConfidence, *instant(&row.LastSynced), row.OrgID}
				marks := "?,?,?,?,?,?,?,?,?,?,?,?,?"
				if shape.withColumn {
					columns, marks, values = columns+", pull_request_tier", marks+",?", append(values, row.PullRequestTier)
				}
				if err := direct.Exec(ctx, `INSERT INTO deployments (`+columns+`) VALUES (`+marks+`)`, values...); err != nil {
					t.Fatalf("seed deployment %s: %v", row.DeploymentID, err)
				}
			}
			for number, title := range map[uint32]string{41: "One-page checkout", 42: "Regression: delivery slots", 43: "Unmerged candidate", 7: "Hotfix: gift cards"} {
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
			request := contractsv1.ContextPacketRequest{SchemaVersion: contractsv1.ContextPacketRequestSchema, RequestID: "req-real-tier", Goal: "checkout regression", Repository: contractsv1.RepositoryRef{Slug: "acme/checkout"}, Scope: contractsv1.RequestedScope{Branch: "main"},
				Options: contractsv1.PacketOptions{MaxItems: 30, MaxOutputTokens: 6000, MaxSerializedBytes: 65536}, Client: contractsv1.ClientInfo{Name: "test", Version: "1.0"}}
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
			for _, key := range shape.wantCause {
				if served[key] != contractsv1.CategoryCause {
					t.Errorf("%s served as %q, want cause (served: %v)", key, served[key], served)
				}
			}
			for _, key := range shape.wantRelation {
				if served[key] != contractsv1.CategoryEvidence {
					t.Errorf("%s served as %q, want evidence/relation (served: %v)", key, served[key], served)
				}
			}
			if got := slices.Contains(packet.Warnings, "deployments_pull_request_tier_column_absent"); got != shape.wantAbsent {
				t.Errorf("column-absent note = %v, want %v (warnings %v)", got, shape.wantAbsent, packet.Warnings)
			}
		})
	}
	contextpacket.ResetStatementShapeProbesForTest()
}
