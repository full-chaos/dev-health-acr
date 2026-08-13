package falkorgraph

// White-box (package falkorgraph, not falkorgraph_test): these probes read
// raw node properties via nodeByKindID, which adapter_live_invariants_test.go's
// black-box suite cannot reach. Mirrors codex_round1_live_test.go's own
// reason for existing outside that package.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestLiveCanonicalEntityWriteClearsAStaleValidityWindowAStubSeeded is
// CHAOS-3785 codex round-3 finding R3-1: subjectMergeAttrs only added
// valid_from/valid_to keys when non-nil, so a canonical entity with no
// validity window of its own (the ordinary case for every devhealthsource
// producer -- none of them ever set EntityProjection.ValidFrom/ValidTo)
// could not clear a stale window a REFERENCED write had already seeded --
// e.g. an episode's own StartedAt/EndedAt, seeded into its attachment
// subject's node via referencedSubjectStubMergeCypher's ON CREATE before the
// canonical entity for that subject ever arrived. `SET n += $map` only
// touches keys the map contains; omitting valid_from/valid_to entirely on
// the canonical write left the stub's stale window in place forever.
func TestLiveCanonicalEntityWriteClearsAStaleValidityWindowAStubSeeded(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-r31-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	target := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_r31_target", Label: "R3-1 target"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}

	// A REFERENCED write (an episode attaching to target) lands FIRST,
	// seeding target's stub node with a real valid_from/valid_to window
	// (episode.StartedAt/EndedAt) via ON CREATE.
	episodeBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r31_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Episodes: []contextfabric.EpisodeProjection{{
			EpisodeID: "episode_r31_00000001", Subject: target, Goal: "Investigate", Outcome: "resolved", Summary: "did the thing",
			Authorization: scope, EvidenceRefIDs: []string{"evidence_r31_episode"},
			StartedAt: observed, EndedAt: observed.Add(time.Hour), SourceVersion: "v1",
		}},
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, episodeBatch); err != nil {
		t.Fatalf("episode ApplyProjectionBatch() error = %v", err)
	}

	// Confirm the stub actually seeded a validity window before healing --
	// otherwise a false pass below would prove nothing.
	before, err := adapter.nodeByKindID(ctx, key, orgID, string(target.Kind), target.CanonicalID, temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() before healing error = %v", err)
	}
	if before == nil {
		t.Fatal("expected target's stub node to exist after the episode write")
	}
	if _, ok := before.Properties[propValidFrom]; !ok {
		t.Fatalf("expected the episode stub to seed %s, properties = %+v", propValidFrom, before.Properties)
	}

	// A LATER batch carries target's own CANONICAL entity, with no validity
	// window of its own (ValidFrom/ValidTo nil, the ordinary devhealthsource
	// shape).
	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r31_2", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed.Add(2 * time.Hour),
		Entities: []contextfabric.EntityProjection{{
			Subject: target, Authorization: scope, EvidenceRefIDs: []string{"evidence_r31_entity"}, ObservedAt: observed.Add(2 * time.Hour), SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}

	after, err := adapter.nodeByKindID(ctx, key, orgID, string(target.Kind), target.CanonicalID, temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() after healing error = %v", err)
	}
	if after == nil {
		t.Fatal("expected target's node to still exist after the canonical write")
	}
	for _, propKey := range []string{propValidFrom, propValidFromNs, propValidTo, propValidToNs} {
		if value, ok := after.Properties[propKey]; ok {
			t.Fatalf("expected the canonical entity write to clear stale %s, still present as %v: %+v", propKey, value, after.Properties)
		}
	}
}
