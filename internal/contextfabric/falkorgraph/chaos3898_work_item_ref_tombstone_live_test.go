package falkorgraph

// White-box (package falkorgraph, not falkorgraph_test): this file needs
// nodeByKindID/graphKey, the same reason chaos3785_round3_live_test.go and
// codex_round1_live_test.go already live here.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// TestLiveWorkItemRefTombstoneOnlyDeletesTheStubOnceOrphaned is CHAOS-3898
// §1.5's ONE genuinely new tombstone mechanism, proven against a real
// FalkorDB: a work_item_ref stub node referenced by TWO edges (two
// different source work items both depending on the same unresolved raw
// target -- a real, reachable shape: two dependency rows, or a
// dependency row re-synced before and after ANOTHER row already
// referenced the same raw target) must survive a tombstone that only
// retires ONE of those edges, and must be deleted only once BOTH are
// retired -- applyTombstone's "relationship"/"edge" case unconditionally
// deletes the named edge, but the "work_item_ref" case must conditionally
// refuse to delete the node while any edge still remains on it.
func TestLiveWorkItemRefTombstoneOnlyDeletesTheStubOnceOrphaned(t *testing.T) {
	adapter, _ := newCodexRoundLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-work-item-ref-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	key := graphKey(adapter.config.GraphPrefix, orgID)

	source1, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(source1): %v", err)
	}
	source2, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-102"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(source2): %v", err)
	}
	refID, refOmitted := identity.DeriveWorkItemRef("ghpr:owner/repo#7", nil)
	if refOmitted {
		t.Fatal("identity.DeriveWorkItemRef unexpectedly omitted")
	}
	refSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItemRef, CanonicalID: refID, Label: "ghpr:owner/repo#7"}
	source1Subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: source1, Label: "WIDGET-101"}
	source2Subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: source2, Label: "WIDGET-102"}
	relationshipID1 := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, source1, refID, "blocks")
	relationshipID2 := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, source2, refID, "blocks")

	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	authz := contextfabric.AuthorizationScope{RepositorySlugs: []string{"example-org/widget-service"}}
	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_workitemref_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			{Subject: source1Subject, Authorization: authz, EvidenceRefIDs: []string{"evidence_source1"}, ObservedAt: observed, SourceVersion: "v1"},
			{Subject: source2Subject, Authorization: authz, EvidenceRefIDs: []string{"evidence_source2"}, ObservedAt: observed, SourceVersion: "v1"},
			{Subject: refSubject, Authorization: authz, EvidenceRefIDs: []string{"evidence_ref"}, ObservedAt: observed, SourceVersion: "v1"},
		},
		Relationships: []contextfabric.RelationshipProjection{
			{
				RelationshipID: relationshipID1, Type: "BLOCKS", From: source1Subject, To: refSubject,
				Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
				Authorization: authz, EvidenceRefIDs: []string{"evidence_edge1"}, ObservedAt: observed, SourceVersion: "v1",
			},
			{
				RelationshipID: relationshipID2, Type: "BLOCKS", From: source2Subject, To: refSubject,
				Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
				Authorization: authz, EvidenceRefIDs: []string{"evidence_edge2"}, ObservedAt: observed, SourceVersion: "v1",
			},
		},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity/edge ApplyProjectionBatch() error = %v", err)
	}

	stub, err := adapter.nodeByKindID(ctx, key, orgID, "work_item_ref", refID, temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() before healing error = %v", err)
	}
	if stub == nil {
		t.Fatal("work_item_ref stub not found after the entity/edge batch -- test setup invalid")
	}

	// Heal edge1 only: tombstone relationship1 AND the node. The node
	// must SURVIVE -- edge2 still references it.
	healEdge1 := entityBatch
	healEdge1.BatchID = "batch_workitemref_00000002"
	healEdge1.Cursor, healEdge1.NextCursor = "cursor-1", "cursor-2"
	healEdge1.Entities, healEdge1.Relationships = []contextfabric.EntityProjection{}, []contextfabric.RelationshipProjection{}
	healEdge1.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: "relationship", CanonicalID: relationshipID1, Reason: "target resolved", EffectiveAt: observed.Add(time.Minute), SourceVersion: "v1"},
		{Kind: "work_item_ref", CanonicalID: refID, Reason: "target resolved, healing if orphaned", EffectiveAt: observed.Add(time.Minute), SourceVersion: "v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, healEdge1); err != nil {
		t.Fatalf("heal-edge1 ApplyProjectionBatch() error = %v", err)
	}

	stub, err = adapter.nodeByKindID(ctx, key, orgID, "work_item_ref", refID, temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() after healing edge1 error = %v", err)
	}
	if stub == nil {
		t.Fatal("work_item_ref stub was deleted while edge2 still referenced it -- the conditional orphan check did not hold")
	}
	remaining, err := adapter.edgesOfNode(ctx, key, orgID, subjectUUID("work_item_ref", refID), temporalFilter{})
	if err != nil {
		t.Fatalf("edgesOfNode() error = %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("edges remaining on the stub = %d, want exactly 1 (edge2)", len(remaining))
	}

	// Heal edge2: tombstone relationship2 AND the node. The node is now
	// truly orphaned and must be deleted.
	healEdge2 := healEdge1
	healEdge2.BatchID = "batch_workitemref_00000003"
	healEdge2.Cursor, healEdge2.NextCursor = "cursor-2", "cursor-3"
	healEdge2.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: "relationship", CanonicalID: relationshipID2, Reason: "target resolved", EffectiveAt: observed.Add(2 * time.Minute), SourceVersion: "v1"},
		{Kind: "work_item_ref", CanonicalID: refID, Reason: "target resolved, healing if orphaned", EffectiveAt: observed.Add(2 * time.Minute), SourceVersion: "v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, healEdge2); err != nil {
		t.Fatalf("heal-edge2 ApplyProjectionBatch() error = %v", err)
	}

	stub, err = adapter.nodeByKindID(ctx, key, orgID, "work_item_ref", refID, temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() after healing edge2 error = %v", err)
	}
	if stub != nil {
		t.Fatal("work_item_ref stub survived after its last referencing edge was healed -- the orphan cleanup never fired")
	}
}

// TestLiveWorkItemRefTombstoneIsIdempotentAgainstAnUnmintedStub proves the
// SAME tombstone shape is a safe no-op when the ref-form was never
// actually minted -- devhealthsource emits it unconditionally on every
// resolved row (design brief §1.5), so this MUST NOT error against a key
// that was never written.
func TestLiveWorkItemRefTombstoneIsIdempotentAgainstAnUnmintedStub(t *testing.T) {
	adapter, _ := newCodexRoundLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-work-item-ref-noop-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	refID, refOmitted := identity.DeriveWorkItemRef("never-minted", nil)
	if refOmitted {
		t.Fatal("identity.DeriveWorkItemRef unexpectedly omitted")
	}
	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_workitemref_noop_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{
			{Kind: "work_item_ref", CanonicalID: refID, Reason: "never minted", EffectiveAt: observed, SourceVersion: "v1"},
		},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() with a tombstone for a never-minted stub error = %v, want a safe no-op", err)
	}
}
