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

// CHAOS-7261 on a real ClickHouse, through the path source_evidence takes:
// an ev2 handle carries only the SHA-256 of its statement's locator
// (evidence_ref_id); ResolveEvidence re-runs the statement and needs exactly
// one row whose locator matches. A locator that two rows of one repository
// share therefore (a) refuses while both exist and (b) serves the OTHER row
// once the row the handle was minted for is gone ("collider-left").
//
// The two colliders are the smallest data the statements' own keys allow:
//   - deployment_incident_provenance.v1 cited edge_id alone; the table key is
//     (org_id, deployment_id, incident_id, source), so two rows of one edge_id
//     differing in source both survive FINAL.
//   - work_item_dependencies.v1 joined src:tgt:key with a bare ':'; work item
//     ids hold ':' (jira:PROJ-1), so (src "a:b", tgt "c") and (src "a", tgt
//     "b:c") are two rows with one locator.

const (
	injRepoID = "30000000-0000-4000-8000-000000000003"
	injRepo   = "acme/inj"
	injOrg    = "10000000-0000-4000-8000-0000000000aa"
)

func injectiveStore(t *testing.T, ctx context.Context) (*contextpacket.ClickHouseEvidenceStore, *contextpacket.CatalogClickHouseRows, clickhousedriver.Conn, *contextpacket.EvidenceIDCodec) {
	t.Helper()
	query, direct := startScopeClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	now := "'2026-09-01 12:00:00.000'"
	scopeExec(t, ctx, direct, fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, injRepoID, injRepo, now, now, injOrg))
	rows := contextpacket.NewCatalogClickHouseRows(query)
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	return store, rows, direct, codec
}

// mintFor returns the ev2 handle of the one row the statement returns now.
func mintFor(t *testing.T, ctx context.Context, rows *contextpacket.CatalogClickHouseRows, codec *contextpacket.EvidenceIDCodec, queryID string) string {
	t.Helper()
	scope := contractsv1.ResolvedScope{RepoID: injRepoID, RepoSlug: injRepo}
	references, err := rows.ResolveEvidenceReference(ctx, injOrg, scope, queryID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 {
		t.Fatalf("%s: want exactly 1 row to mint for, got %d", queryID, len(references))
	}
	handle, err := codec.Encode(injOrg, injRepoID, queryID, references[0].Evidence.EvidenceRefID, false)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func expandText(e contractsv1.ExpandedEvidence) string {
	return e.Evidence.Source.DisplayLabel + " | " + e.Evidence.Citation + " | " + e.Excerpt
}

func TestEv2HandleNeverServesAnotherRowUnderCollidingLocators(t *testing.T) {
	principal := storage.Principal{OrgID: injOrg, RepositoryScopes: []string{"*"}}
	now := "'2026-09-01 12:00:00.000'"
	cases := []struct {
		name, queryID string
		seedA, seedB  []string
		deleteA       string
		rowA, rowB    string
		entityA       string
		structuredKey string
	}{
		{
			name: "deployment_incident_provenance same edge_id different source", queryID: "deployment_incident_provenance.v1",
			seedA:   []string{fmt.Sprintf(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES ('edge-1', '%s', 'dep-1', 'inc-1', '%s', 1.0, 'native', 'ROW-A', %s, %s)`, injOrg, injRepoID, now, now)},
			seedB:   []string{fmt.Sprintf(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES ('edge-1', '%s', 'dep-1', 'inc-1', '%s', 0.5, 'heuristic', 'ROW-B', %s, %s)`, injOrg, injRepoID, now, now)},
			deleteA: fmt.Sprintf(`ALTER TABLE work_graph_deployment_incident_edges DELETE WHERE org_id = '%s' AND source = 'native' SETTINGS mutations_sync = 2`, injOrg),
			rowA:    "ROW-A", rowB: "ROW-B", entityA: "edge-1", structuredKey: "edge_id",
		},
		{
			name: "work_item_dependencies colon-shifted endpoints", queryID: "work_item_dependencies.v1",
			seedA: []string{
				fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, title, status, created_at, updated_at, last_synced, org_id) VALUES ('%s', 'a:b', 't', 's', %s, %s, %s, '%s')`, injRepoID, now, now, now, injOrg),
				fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES ('a:b', 'c', 'blocks', 'ROW-A', %s, '%s')`, now, injOrg),
			},
			seedB: []string{
				fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, title, status, created_at, updated_at, last_synced, org_id) VALUES ('%s', 'a', 't', 's', %s, %s, %s, '%s')`, injRepoID, now, now, now, injOrg),
				fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES ('a', 'b:c', 'blocks', 'ROW-B', %s, '%s')`, now, injOrg),
			},
			deleteA: fmt.Sprintf(`ALTER TABLE work_item_dependencies DELETE WHERE org_id = '%s' AND source_work_item_id = 'a:b' SETTINGS mutations_sync = 2`, injOrg),
			rowA:    "ROW-A", rowB: "ROW-B", entityA: "a:b:c:blocks:fwd", structuredKey: "dependency_id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, rows, direct, codec := injectiveStore(t, ctx)
			for _, s := range tc.seedA {
				scopeExec(t, ctx, direct, s)
			}
			handle := mintFor(t, ctx, rows, codec, tc.queryID)
			served, err := store.ResolveEvidence(ctx, principal, handle)
			if err != nil || !strings.Contains(expandText(served), tc.rowA) {
				t.Fatalf("handle minted for row A must serve row A while it is alone: err=%v text=%q", err, expandText(served))
			}
			// The locator grammar is not the entity identity: entity_id keeps
			// its raw, human-readable form (the packet validator caps it at
			// 1024 runes, which the escaped locator can exceed).
			if got := served.Evidence.Source.EntityID; got != tc.entityA {
				t.Errorf("EntityID = %q, want the raw %q", got, tc.entityA)
			}
			if got := served.Structured[tc.structuredKey]; got != tc.entityA {
				t.Errorf("structured_fields[%s] = %v, want %q", tc.structuredKey, got, tc.entityA)
			}
			for _, s := range tc.seedB {
				scopeExec(t, ctx, direct, s)
			}
			// Row B now sits beside A. A handle minted for A must serve A or
			// refuse; it must never serve B.
			if both, err := store.ResolveEvidence(ctx, principal, handle); err == nil && !strings.Contains(expandText(both), tc.rowA) {
				t.Errorf("with both rows present the handle for A served %q", expandText(both))
			}
			scopeExec(t, ctx, direct, tc.deleteA)
			// Row A is gone. Its handle must not now serve row B.
			after, err := store.ResolveEvidence(ctx, principal, handle)
			if err == nil {
				t.Fatalf("collider-left: the handle minted for deleted row A served %q", expandText(after))
			}
			if !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("want ErrNotFound for the deleted row's handle, got %v", err)
			}
		})
	}
}

// A dependency whose ids hold hundreds of ':' has a raw entity_id under the
// 1024-rune bound but an escaped locator (':' -> "%3A") far over it. The
// handle must still expand: only the locator is escaped, never the entity id.
func TestEv2HandleExpandsADependencyWhoseEscapedFormExceedsTheEntityIDBound(t *testing.T) {
	ctx := context.Background()
	store, rows, direct, codec := injectiveStore(t, ctx)
	now := "'2026-09-01 12:00:00.000'"
	source := strings.Repeat("x:", 340) + "x"
	scopeExec(t, ctx, direct, fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, title, status, created_at, updated_at, last_synced, org_id) VALUES ('%s', '%s', 't', 's', %s, %s, %s, '%s')`, injRepoID, source, now, now, now, injOrg))
	scopeExec(t, ctx, direct, fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES ('%s', 'c', 'blocks', 'LONG', %s, '%s')`, source, now, injOrg))
	handle := mintFor(t, ctx, rows, codec, "work_item_dependencies.v1")
	served, err := store.ResolveEvidence(ctx, storage.Principal{OrgID: injOrg, RepositoryScopes: []string{"*"}}, handle)
	if err != nil {
		t.Fatalf("a valid dependency with colon-heavy ids must expand: %v", err)
	}
	if want := source + ":c:blocks:fwd"; served.Evidence.Source.EntityID != want {
		t.Errorf("EntityID = %d runes, want the raw %d-rune form", len(served.Evidence.Source.EntityID), len(want))
	}
}
