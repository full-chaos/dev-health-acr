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

// TestLiveCanonicalEntityWriteClearsAStaleValidityWindow is CHAOS-3785
// codex round-3 finding R3-1: subjectMergeAttrs only ADDED
// valid_from/valid_to when non-nil, so a canonical entity with no validity
// window of its own could not clear a stale window an earlier write had
// left behind. `SET n += $map` only touches keys the map contains, so
// omitting those keys left the stale window in place forever.
//
// CHAOS-3781 round-1 F3 changed how a stale window can ARRIVE, not
// whether it must be cleared. This test originally seeded one through a
// referenced STUB (an episode's own StartedAt/EndedAt landing on its
// attachment subject before that subject's canonical entity existed).
// Stubs no longer write any validity window at all -- see
// TestLiveReferencedStubsCarryNoValidityWindowAtAll below, which now
// guards that route directly -- so the stale window here is seeded the
// way one can still legitimately exist: an earlier OWNED write that DID
// carry a window, followed by a re-projection that does not.
//
// That path is not hypothetical during the v3 -> v4 rollout: a graph
// projected before F3 holds stub-seeded windows, and the canonical write
// clearing them unconditionally is what heals those organizations without
// a second migration.
func TestLiveCanonicalEntityWriteClearsAStaleValidityWindow(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-r31-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	target := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_r31_target", Label: "R3-1 target"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	staleFrom := observed
	staleTo := observed.Add(time.Hour)

	// An OWNED entity write carrying a real window lands first.
	windowedBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_r31_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: target, Authorization: scope, EvidenceRefIDs: []string{"evidence_r31_entity"},
			ObservedAt: observed, ValidFrom: &staleFrom, ValidTo: &staleTo, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, windowedBatch); err != nil {
		t.Fatalf("windowed ApplyProjectionBatch() error = %v", err)
	}

	// Confirm the window really landed before healing -- otherwise a false
	// pass below would prove nothing.
	before, err := adapter.nodeByKindID(ctx, key, orgID, string(target.Kind), target.CanonicalID, temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() before healing error = %v", err)
	}
	if before == nil {
		t.Fatal("expected target's node to exist after the windowed write")
	}
	if _, ok := before.Properties[propValidFrom]; !ok {
		t.Fatalf("expected the windowed write to set %s, properties = %+v", propValidFrom, before.Properties)
	}

	// A LATER batch re-projects the same canonical entity with NO validity
	// window (ValidFrom/ValidTo nil -- the ordinary shape for a producer
	// whose source row has no interval).
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

// TestLiveReferencedStubsCarryNoValidityWindowAtAll is the CHAOS-3781
// round-1 F3 half of the invariant above, kept in this file so a reader
// arriving from R3-1 sees immediately why that test no longer seeds
// through a stub.
//
// An episode's StartedAt/EndedAt is the EPISODE's interval. Writing it
// onto the attachment subject claimed the subject was valid only while
// that episode ran -- so a historical read excluded a live work item
// everywhere outside an unrelated hour, and the next referencing record
// overwrote the window again, making the result depend on projection
// order.
func TestLiveReferencedStubsCarryNoValidityWindowAtAll(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-f3-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	target := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_f3_target", Label: "F3 target"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}

	episodeBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_f3_0001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Episodes: []contextfabric.EpisodeProjection{{
			// CHAOS-3901: EpisodeID is the FULL canonical id, not a bare
			// row id -- the same contract devhealthsource/episodes.go's
			// episodeCandidate already follows (it stamps EpisodeID with
			// its own "episode:"+id, never a bare value). Before the
			// CHAOS-3901 fix, falkorgraph's projectEpisode re-prefixed
			// EpisodeID a second time, so a bare id here happened to land
			// on the same doubled string projectEpisode produced for a
			// real (already-prefixed) production EpisodeID -- masking the
			// mismatch this test exists to catch, rather than exercising
			// it.
			EpisodeID: "episode:episode_f3_000000001", Subject: target, Goal: "Investigate", Outcome: "resolved", Summary: "did the thing",
			Authorization: scope, EvidenceRefIDs: []string{"evidence_f3_episode"},
			StartedAt: observed, EndedAt: observed.Add(time.Hour), SourceVersion: "v1",
		}},
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, episodeBatch); err != nil {
		t.Fatalf("episode ApplyProjectionBatch() error = %v", err)
	}

	stub, err := adapter.nodeByKindID(ctx, key, orgID, string(target.Kind), target.CanonicalID, temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() error = %v", err)
	}
	if stub == nil {
		t.Fatal("expected the episode write to create the attachment subject's stub node")
	}
	for _, propKey := range []string{propValidFrom, propValidFromNs, propValidTo, propValidToNs} {
		if value, ok := stub.Properties[propKey]; ok {
			t.Fatalf("a referenced stub carried %s = %v; the episode's own interval is not its subject's: %+v", propKey, value, stub.Properties)
		}
	}

	// The EPISODE node itself still carries the window -- the fix removes
	// it from the stub, not from the record that genuinely has one.
	// CHAOS-3901: single-prefixed, matching the EpisodeID above and
	// projectEpisode's fixed owned-node id (no second "episode:" applied).
	episodeNode, err := adapter.nodeByKindID(ctx, key, orgID, string(contextfabric.SubjectEpisode), "episode:episode_f3_000000001", temporalFilter{})
	if err != nil {
		t.Fatalf("nodeByKindID() episode error = %v", err)
	}
	if episodeNode == nil {
		t.Fatal("expected the episode node to exist")
	}
	if _, ok := episodeNode.Properties[propValidFrom]; !ok {
		t.Fatalf("the episode node lost its own validity window: %+v", episodeNode.Properties)
	}
}
