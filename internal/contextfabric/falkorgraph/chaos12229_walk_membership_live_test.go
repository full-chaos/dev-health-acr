package falkorgraph

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12229, on a real FalkorDB: a walk reads a work item as a projection
// map of walkNodeProperties, and that map carries the stated project
// membership, so a requested project narrowing decides walked work items too.
func TestLiveAWalkReadsAWorkItemsStatedProjectMembership(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	assertWalkReadsStatedMembership(t, ctx, adapter)
}

func assertWalkReadsStatedMembership(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	orgID := "live-12229-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	observed := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	workItem := func(id string, identities *[]string) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject:       contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:" + id, Label: id},
			Authorization: scope, ProjectIdentities: identities,
			EvidenceRefIDs: []string{"evidence_12229_" + id}, ObservedAt: observed, SourceVersion: "v1",
		}
	}
	member, other := []string{"project.v2:linear:proj-1"}, []string{"project.v2:linear:proj-2"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_12229_walk_0001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities:      []contextfabric.EntityProjection{workItem("LIN-MEMBER", &member), workItem("LIN-OTHER", &other), workItem("LIN-UNSTATED", nil)},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind}) RETURN %s", labelSubject, propOrgID, propKind, walkProjection("n", walkNodeProperties))
	rows, err := adapter.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": string(contractsv1.ContextFabricSubjectWorkItem)}, true)
	if err != nil {
		t.Fatalf("walk projection read error = %v", err)
	}
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}, ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: member, Complete: true}}
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/api"}}
	got := map[string][2]bool{}
	for _, r := range rows {
		n := walkNode(r["n"])
		if n == nil {
			t.Fatalf("a walk row holds no node: %#v", r)
		}
		attributes := toCandidateNode(n).Attributes
		got[propStringValue(n.Properties[propLabel])] = [2]bool{graphrank.AuthorizedAttributes(principal, narrowed, attributes), graphrank.ProjectNarrowingUnproven(narrowed, attributes)}
	}
	want := map[string][2]bool{"LIN-MEMBER": {true, false}, "LIN-OTHER": {false, true}, "LIN-UNSTATED": {true, true}}
	for label, admittedUnproven := range want {
		if got[label][0] != admittedUnproven[0] || (admittedUnproven[0] && got[label][1] != admittedUnproven[1]) {
			t.Errorf("%s: admitted, unproven = %v, want %v", label, got[label], admittedUnproven)
		}
	}
}

// Review r1 P2, on a real FalkorDB: the repository work-item walk reports, in
// TreeWorkItemWalk, that a requested project narrowing admitted its linked
// issues only on their "*" project lists (no issue states a membership here),
// and reports nothing without a narrowing.
func TestLiveTheRepositoryWalkReportsAnUnverifiedProjectNarrowing(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	assertRepositoryWalkReportsUnverifiedNarrowing(t, ctx, adapter)
}

func assertRepositoryWalkReportsUnverifiedNarrowing(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	orgID := "live-12229-tree-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	entities, relationships := newLiveMemberFixture(orgID, time.Now().UTC(), nil, liveMemberLinks())
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	principal := storage.Principal{OrgID: orgID}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: memberAnchorID, Label: memberAnchorSlug}
	for name, tc := range map[string]struct {
		scope contextfabric.RequestedScope
		want  bool
	}{
		"no narrowing": {contextfabric.RequestedScope{}, false},
		"a narrowing no issue proves": {contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"},
			ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true}}, true},
	} {
		walk, err := adapter.TreeWorkItemMembers(ctx, principal, binding, tc.scope, anchor, 25)
		if err != nil {
			t.Fatalf("%s: TreeWorkItemMembers() error = %v", name, err)
		}
		if len(walk.Members) == 0 {
			t.Fatalf("%s: premise: the walk admitted no member", name)
		}
		if walk.ProjectNarrowingUnverified != tc.want {
			t.Fatalf("%s: ProjectNarrowingUnverified = %v, want %v", name, walk.ProjectNarrowingUnverified, tc.want)
		}
	}
}
