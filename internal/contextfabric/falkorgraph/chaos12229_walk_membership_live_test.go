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
