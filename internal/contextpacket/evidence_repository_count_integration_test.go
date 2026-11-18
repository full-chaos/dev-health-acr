package contextpacket_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A reader of an organization with more than 64 repositories resolves a
// catalog evidence handle the same as a reader of a small organization, on a
// real ClickHouse. The handle's repository is the last one in slug order, so
// a capped repository listing never reaches it.

func countRepoID(index int) string { return fmt.Sprintf("30000000-0000-4000-8000-%012d", index) }

func countRepoSlug(index int) string { return fmt.Sprintf("acme/r%04d", index) }

func seedCountOrganization(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, repositories int, runID string) {
	t.Helper()
	now := "'2026-09-01 12:00:00.000'"
	values := make([]string, 0, repositories)
	for index := range repositories {
		values = append(values, fmt.Sprintf(`('%s', '%s', %s, %s, '%s', 'github')`, countRepoID(index), countRepoSlug(index), now, now, orgID))
	}
	scopeExec(t, ctx, direct, `INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES `+strings.Join(values, ", "))
	if runID != "" {
		scopeExec(t, ctx, direct, fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES ('%s', '%s', 'success', %s, %s, '%s')`, countRepoID(repositories-1), runID, now, now, orgID))
	}
}

func countTargetLocator(t *testing.T, ctx context.Context, rows *contextpacket.CatalogClickHouseRows, orgID string, repositories int) string {
	t.Helper()
	references, err := rows.ResolveEvidenceReference(ctx, orgID, contractsv1.ResolvedScope{RepoID: countRepoID(repositories - 1), RepoSlug: countRepoSlug(repositories - 1)}, "ci_pipeline_runs.v1", "")
	if err != nil || len(references) != 1 {
		t.Fatalf("org %s target locator: references = %d, error = %v", orgID, len(references), err)
	}
	return references[0].Evidence.EvidenceRefID
}

func TestEvidenceHandleResolvesAboveSixtyFourRepositories(t *testing.T) {
	ctx := context.Background()
	query, direct := startScopeClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	rows := contextpacket.NewCatalogClickHouseRows(query)
	codec := fixtureEvidenceCodec(t)
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	allSlugs := func(repositories int) []string {
		slugs := make([]string, 0, repositories)
		for index := range repositories {
			slugs = append(slugs, countRepoSlug(index))
		}
		return slugs
	}
	cells := []struct {
		name         string
		repositories int
		scopes       func(int) []string
	}{
		{"64 repositories org-wide", 64, func(int) []string { return []string{"*"} }},
		{"65 repositories org-wide", 65, func(int) []string { return []string{"*"} }},
		{"256 repositories org-wide", 256, func(int) []string { return []string{"*"} }},
		{"65 repositories named grant list", 65, allSlugs},
		{"256 repositories named grant list", 256, allSlugs},
		{"256 repositories owner wildcard", 256, func(int) []string { return []string{"acme/*"} }},
	}
	for index, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			orgID := fmt.Sprintf("40000000-0000-4000-8000-%012d", index)
			runID := fmt.Sprintf("run-count-%d", index)
			seedCountOrganization(t, ctx, direct, orgID, cell.repositories, runID)
			handle, err := codec.Encode(orgID, countRepoID(cell.repositories-1), "ci_pipeline_runs.v1", countTargetLocator(t, ctx, rows, orgID, cell.repositories), false)
			if err != nil {
				t.Fatalf("encode handle: %v", err)
			}
			expanded, err := store.ResolveEvidence(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: cell.scopes(cell.repositories)}, handle)
			if err != nil {
				t.Fatalf("resolve evidence for %d repositories: %v", cell.repositories, err)
			}
			if expanded.Structured["pipeline_run_id"] != runID || expanded.Evidence.EvidenceRefID != handle {
				t.Fatalf("expanded = %#v", expanded)
			}
		})
	}
}

func TestEvidenceHandleAboveSixtyFourRepositoriesNeverResolvesAnotherOrganizationsRow(t *testing.T) {
	ctx := context.Background()
	query, direct := startScopeClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	const repositories = 256
	const orgA = "50000000-0000-4000-8000-00000000000a"
	const orgB = "50000000-0000-4000-8000-00000000000b"
	// Both organizations hold the same repository ids and slugs; only B
	// holds the run.
	seedCountOrganization(t, ctx, direct, orgA, repositories, "")
	seedCountOrganization(t, ctx, direct, orgB, repositories, "run-FOREIGN")
	rows := contextpacket.NewCatalogClickHouseRows(query)
	codec := fixtureEvidenceCodec(t)
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatalf("create evidence store: %v", err)
	}
	locatorB := countTargetLocator(t, ctx, rows, orgB, repositories)
	handleB, err := codec.Encode(orgB, countRepoID(repositories-1), "ci_pipeline_runs.v1", locatorB, false)
	if err != nil {
		t.Fatalf("encode org B handle: %v", err)
	}
	// Sanity: the org B handle resolves for an org B reader.
	if _, err := store.ResolveEvidence(ctx, storage.Principal{OrgID: orgB, RepositoryScopes: []string{"*"}}, handleB); err != nil {
		t.Fatalf("org B reader resolving its own handle: %v", err)
	}
	handleA, err := codec.Encode(orgA, countRepoID(repositories-1), "ci_pipeline_runs.v1", locatorB, false)
	if err != nil {
		t.Fatalf("encode org A handle: %v", err)
	}
	for name, handle := range map[string]string{"org B handle": handleB, "org A handle over org B locator": handleA} {
		expanded, err := store.ResolveEvidence(ctx, storage.Principal{OrgID: orgA, RepositoryScopes: []string{"*"}}, handle)
		if !errors.Is(err, storage.ErrNotFound) || err.Error() != storage.ErrNotFound.Error() {
			t.Fatalf("org A reader, %s: expanded = %#v, error = %v, want generic not found", name, expanded, err)
		}
	}
}
