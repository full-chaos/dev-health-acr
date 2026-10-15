package contextpacket_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// One pull request is served by pull_requests.v1 and, as a projected work
// item ("ghpr:<repo>#N"), by work_items.v1. Both statements must carry the
// same subject key; a work item of another repository or a plain work item
// must not carry it.
func TestPullRequestSubjectKeyMatchesAcrossPullRequestAndWorkItemSources(t *testing.T) {
	ctx := context.Background()
	query, direct := startScopeClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	now := "'2026-09-01 12:00:00.000'"
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, injRepoID, injRepo, now, now, injOrg),
		fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) VALUES ('%s', 2, 'PR two', 'merged', %s, %s, '%s')`, injRepoID, now, now, injOrg),
		fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, title, status, created_at, updated_at, last_synced, org_id) VALUES ('%s', 'ghpr:%s#2', 'PR two', 'done', %s, %s, %s, '%s')`, injRepoID, injRepo, now, now, now, injOrg),
		fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, title, status, created_at, updated_at, last_synced, org_id) VALUES ('%s', 'ghpr:other/repo#2', 'other', 'done', %s, %s, %s, '%s')`, injRepoID, now, now, now, injOrg),
		fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, title, status, created_at, updated_at, last_synced, org_id) VALUES ('%s', 'jira:PROJ-2', 'plain', 'done', %s, %s, %s, '%s')`, injRepoID, now, now, now, injOrg),
	} {
		scopeExec(t, ctx, direct, statement)
	}
	plan := contextpacket.ReadPlan{OrgID: injOrg, RepoID: injRepoID, RepoSlug: injRepo}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	want := "pull_request:" + injRepoID + ":2"
	subjects := map[string]string{}
	for _, source := range contextpacket.SourceQueryCatalogV1 {
		if source.ID != "pull_requests.v1" && source.ID != "work_items.v1" {
			continue
		}
		refs, err := executor.QueryEvidence(ctx, source, plan.Bindings())
		if err != nil {
			t.Fatalf("%s: %v", source.ID, err)
		}
		for _, ref := range refs {
			subject, _ := ref.Metadata["subject_key"].(string)
			subjects[ref.Source.EntityType+"/"+ref.Source.EntityID] = subject
		}
	}
	expected := map[string]string{
		"pull_request/2":                   "pull_request:" + injRepoID + ":2",
		"work_item/ghpr:" + injRepo + "#2": want,
		"work_item/ghpr:other/repo#2":      "",
		"work_item/jira:PROJ-2":            "",
	}
	for key, subject := range expected {
		if got, ok := subjects[key]; !ok || got != subject {
			t.Errorf("subject_key[%s] = %q (present %v), want %q", key, got, ok, subject)
		}
	}
}
