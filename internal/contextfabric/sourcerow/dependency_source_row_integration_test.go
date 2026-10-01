package sourcerow_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A dependency ref the projection producer mints must expand to its own
// source row, for repository-bound work items and for repository-less ones
// (a Linear work item carries the zero repository UUID and has no repos row).
func TestMintedDependencyRefsExpandToTheirSourceRow(t *testing.T) {
	ctx := context.Background()
	query, direct := startClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		exec(t, ctx, direct, statement)
	}
	org := integrationOrg
	item := func(repo, id, provider string) string {
		return fmt.Sprintf("('%s', '%s', '%s', 'item %s', 'open', %s, %s, '', %s, '%s')", repo, id, provider, id, now7252, now7252, now7252, org)
	}
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, grantedID, grantedRep, now7252, now7252, org),
		`INSERT INTO work_items (repo_id, work_item_id, provider, title, status, created_at, updated_at, parent_id, last_synced, org_id) VALUES ` + strings.Join([]string{
			item(zeroRepoID, "linear:CHAOS-1", "linear"), item(zeroRepoID, "linear:CHAOS-2", "linear"),
			item(grantedID, "github:acme/api#1", "github"), item(grantedID, "github:acme/api#2", "github"),
		}, ", "),
		fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES
			('linear:CHAOS-1', 'linear:CHAOS-2', 'blocks', 'Linear pair', %[1]s, '%[2]s'),
			('github:acme/api#1', 'github:acme/api#2', 'blocks', 'GitHub pair', %[1]s, '%[2]s'),
			('linear:CHAOS-1', 'github:acme/api#1', 'blocks', 'Linear to GitHub', %[1]s, '%[2]s'),
			('github:acme/api#2', 'linear:CHAOS-2', 'blocks', 'GitHub to Linear', %[1]s, '%[2]s')`, now7252, org),
	} {
		exec(t, ctx, direct, statement)
	}

	projection, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	minted := drain(t, ctx, projection, devhealthsource.SourceName)
	resolve, err := sourcerow.New(contextpacket.NewCatalogClickHouseRows(query), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	resolve.WithRepoLessAdmitter(devhealthfacts.NewRepoLessWorkItemAdmitter(query))
	orgWide := storage.Principal{OrgID: org}
	for _, want := range []string{"Linear pair", "GitHub pair", "Linear to GitHub", "GitHub to Linear"} {
		served := false
		for ref := range minted {
			kind, id := splitRef(ref)
			if kind != contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2 {
				continue
			}
			expanded, decision := resolve.ResolveSourceRow(ctx, orgWide, string(kind), id)
			if decision.Reason == contextfabric.SourceRowServed && strings.Contains(expanded.Evidence.Citation, want) {
				served = true
			}
		}
		if !served {
			t.Errorf("no minted dependency ref expands to the %q row", want)
		}
	}
	// The repo-less work item's own ref (a Linear work item row).
	if _, decision := resolve.ResolveSourceRow(ctx, orgWide, string(contractsv1.ContextFabricEvidenceEntityWorkItem), zeroRepoID+":linear:CHAOS-1"); decision.Reason != contextfabric.SourceRowServed {
		t.Errorf("repo-less work item ref: %+v", decision)
	}
	// A repository-restricted caller with no ownership path is refused.
	restricted := storage.Principal{OrgID: org, RepositoryScopes: []string{grantedRep}}
	for ref := range minted {
		kind, id := splitRef(ref)
		if kind != contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2 || !strings.Contains(id, "linear%3ACHAOS-1") {
			continue
		}
		if _, decision := resolve.ResolveSourceRow(ctx, restricted, string(kind), id); decision.Reason == contextfabric.SourceRowServed && !strings.Contains(id, "github%3Aacme") {
			t.Errorf("restricted caller served a repo-less source row %q", id)
		}
	}
	var deps int
	for ref := range minted {
		if kind, _ := splitRef(ref); kind == contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2 {
			deps++
		}
	}
	if deps != 4 {
		t.Errorf("minted %d dependency refs, want 4", deps)
	}
}
