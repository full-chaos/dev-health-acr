package devhealthfacts

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A repo-less (zero repository UUID) work item is readable by the source-row
// resolver exactly when the scope expander admits it: organization grant,
// repository-restricted caller admitted through a project-ownership or
// native pull-request-link path, and repository-restricted caller denied.
// One fixture drives both.
func TestRepoLessWorkItemSourceRowsFollowTheScopeExpander(t *testing.T) {
	fixture := newAuthzPathsFixture(t)
	ctx := context.Background()
	resolver, err := sourcerow.New(contextpacket.NewCatalogClickHouseRows(fixture.query), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver.WithRepoLessAdmitter(NewRepoLessWorkItemAdmitter(fixture.query))

	projectP, _, err := identity.Derive(identity.KindProject, []string{"linear", authzPathsProjectP}, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectQ, _, err := identity.Derive(identity.KindProject, []string{"linear", authzPathsProjectQ}, nil)
	if err != nil {
		t.Fatal(err)
	}
	type item struct{ project, canonicalProject, id string }
	items := []item{
		{authzPathsProjectP, projectP, "linear:p-project"},
		{authzPathsProjectP, projectP, "linear:p-both"},
		{authzPathsProjectQ, projectQ, "linear:q-link"},
		{authzPathsProjectQ, projectQ, "linear:q-none"},
		{authzPathsProjectQ, projectQ, "linear:q-heuristic"},
		{authzPathsProjectQ, projectQ, "linear:q-explicit"},
	}
	callers := []struct {
		name      string
		principal storage.Principal
	}{
		{"organization grant", authzPathsPrincipal()},
		{"wildcard grant", authzPathsPrincipal("*")},
		{"restricted, granted repo owned by project's team", authzPathsPrincipal(authzPathsSlugG)},
		{"restricted, denied", authzPathsPrincipal("nobody/else")},
	}
	expander := NewScopeExpander(fixture.query)
	sawAdmit, sawDeny := map[string]bool{}, map[string]bool{}
	for _, caller := range callers {
		expanded := map[string]bool{}
		for _, project := range []struct{ id, canonical string }{{authzPathsProjectP, projectP}, {authzPathsProjectQ, projectQ}} {
			result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
				Principal:       caller.principal,
				RequirementKind: contextfabric.FactStatus,
				Origins:         []contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: project.canonical}},
				Policy:          "project_work_item_status_v1",
				TargetKind:      contextfabric.SubjectWorkItem,
				TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Limit:           50,
			})
			if err != nil {
				t.Fatalf("%s: expand %s: %v", caller.name, project.id, err)
			}
			for _, target := range result.Targets {
				expanded[target.Label] = true
			}
		}
		for _, it := range items {
			ref := fmt.Sprintf("%s:%s", zeroRepositoryID, it.id)
			_, decision := resolver.ResolveSourceRow(ctx, caller.principal, string(contractsv1.ContextFabricEvidenceEntityWorkItem), ref)
			served := decision.Reason == contextfabric.SourceRowServed
			if served != expanded[it.id] {
				t.Errorf("%s / %s: resolver served=%v (%+v), expander admitted=%v", caller.name, it.id, served, decision, expanded[it.id])
			}
			if served {
				sawAdmit[caller.name] = true
			} else {
				sawDeny[caller.name] = true
			}
		}
	}
	if !sawAdmit["organization grant"] || !sawAdmit["restricted, granted repo owned by project's team"] || !sawDeny["restricted, granted repo owned by project's team"] || !sawDeny["restricted, denied"] {
		t.Errorf("the fixture must exercise all three bases: admitted %v denied %v", sawAdmit, sawDeny)
	}
}
