package falkorgraph

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/redis/go-redis/v9"
)

// chaos4298LiveBatch builds the smallest ProjectionBatch that passes
// contractsv1.ContextFabricProjectionBatch.Validate() (one Entity; the other
// four arrays must be non-nil but may be empty) -- writeWatermark runs
// unconditionally at the end of ApplyProjectionBatch regardless of what the
// batch actually projects, so this is enough to drive repeated real writes.
// cursor is threaded through so a caller can vary NextCursor (and therefore
// projectionWatermark's own hash) independently of proving generation
// advances on an UNCHANGED hash too.
func chaos4298LiveBatch(orgID, batchID, source, cursor, nextCursor string) contextfabric.ProjectionBatch {
	observed := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_chaos4298", Label: "CHAOS-4298"}
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: batchID, OrgID: orgID, Source: source,
		SourceVersion: "v1", Cursor: cursor, NextCursor: nextCursor, GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject:        subject,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_chaos4298_0001"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

// TestLiveWatermarkGenerationAdvancesOnEveryWriteEvenWithIdenticalHash is
// CHAOS-4298's own end-to-end ABA-closure proof against a real FalkorDB
// server: three IDENTICAL batches (same BatchID/OrgID/Source/SourceVersion/
// Cursor/NextCursor) each produce the SAME projectionWatermark hash --
// exactly the shape codex R2's flagged hazard needs (a write landing that
// leaves backend_watermark's own value unchanged, indistinguishable from
// "no write happened" under the old value-only comparison) -- yet
// chaos4155WatermarkSnapshot's own generation reading still advances by
// exactly 1 on every write, because writeWatermark's
// `coalesce(w.generation, 0) + 1` runs unconditionally, never gated on
// whether backend_watermark's value actually changed.
func TestLiveWatermarkGenerationAdvancesOnEveryWriteEvenWithIdenticalHash(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-org-4298-generation-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	batch := chaos4298LiveBatch(orgID, "batch_chaos4298_00000001", "chaos4298-source", "cursor-1", "cursor-2")

	var hashes []string
	for i := 0; i < 3; i++ {
		receipt, err := adapter.ApplyProjectionBatch(ctx, batch)
		if err != nil {
			t.Fatalf("ApplyProjectionBatch call %d error = %v", i+1, err)
		}
		hashes = append(hashes, receipt.BackendWatermark)
	}
	if hashes[0] == "" || hashes[0] != hashes[1] || hashes[1] != hashes[2] {
		t.Fatalf("BackendWatermark across 3 identical batches = %v, want all 3 non-empty and equal -- precondition for this test to actually exercise the ABA shape", hashes)
	}

	key, err := adapter.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		t.Fatalf("resolveReadKey error = %v", err)
	}
	snapshot, err := adapter.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		t.Fatalf("chaos4155WatermarkSnapshot error = %v", err)
	}
	if got := snapshot[batch.Source].Generation; got != 3 {
		t.Fatalf("generation after 3 identical-hash writes = %d, want 3 -- generation must advance on EVERY write regardless of whether backend_watermark's own value changed", got)
	}
	if snapshot[batch.Source].Epoch == "" {
		t.Fatalf("epoch is empty after 3 writes, want a non-empty per-node nonce")
	}
}

// TestLiveWatermarkGenerationSelfHealsFromAPreCHAOS4298Node proves the
// coalesce-based self-heal claim in writeWatermark's own doc comment: a
// watermark node planted WITHOUT a generation OR epoch property (simulating
// a node written by the pre-CHAOS-4298 Cypher, before either field existed)
// reads generation=1 (coalesce(null, 0) + 1) and epoch=chaos4298SentinelEpoch
// (coalesce(null, sentinel) -- the ON MATCH branch, since the node already
// exists) after its very first post-deploy write -- no backfill migration
// needed -- and generation increments normally after that while epoch stays
// fixed at the sentinel (matching any other already-epoched node) until the
// next purge.
func TestLiveWatermarkGenerationSelfHealsFromAPreCHAOS4298Node(t *testing.T) {
	ctx := context.Background()
	adapter, addr := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-org-4298-selfheal-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	source := "chaos4298-selfheal-source"
	key := graphKey(adapter.config.GraphPrefix, orgID)

	// ensureOrgGraph normally runs inside ApplyProjectionBatch before any
	// write -- called directly here so the raw plant below has a graph key
	// to target before this test's own first ApplyProjectionBatch call.
	if err := adapter.ensureOrgGraph(ctx, key); err != nil {
		t.Fatalf("ensureOrgGraph error = %v", err)
	}
	raw := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = raw.Close() })
	plant := fmt.Sprintf(
		"CREATE (w:%s {%s:'%s', source:'%s', cursor:'cursor-0', backend_watermark:'pre-4298-watermark'})",
		labelWatermark, propOrgID, orgID, source,
	)
	if err := raw.Do(ctx, "GRAPH.QUERY", key, plant).Err(); err != nil {
		t.Fatalf("plant pre-CHAOS-4298 watermark node via raw GRAPH.QUERY error = %v", err)
	}

	snapshotBefore, err := adapter.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err == nil {
		t.Fatalf("chaos4155WatermarkSnapshot before any post-deploy write = %+v, want a malformed-row error -- the planted node has no generation property yet", snapshotBefore)
	}

	batch := chaos4298LiveBatch(orgID, "batch_chaos4298_selfheal_001", source, "cursor-0", "cursor-1")
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch (self-heal write) error = %v", err)
	}
	snapshot, err := adapter.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		t.Fatalf("chaos4155WatermarkSnapshot after self-heal write error = %v", err)
	}
	if got := snapshot[source].Generation; got != 1 {
		t.Fatalf("generation after the first post-deploy write to a pre-CHAOS-4298 node = %d, want 1 (coalesce(null, 0) + 1)", got)
	}
	if got := snapshot[source].Epoch; got != chaos4298SentinelEpoch {
		t.Fatalf("epoch after the first post-deploy write to a pre-epoch node = %q, want the fixed sentinel %q (ON MATCH's coalesce(w.epoch, sentinel), not a fresh per-write nonce)", got, chaos4298SentinelEpoch)
	}

	if _, err := adapter.ApplyProjectionBatch(ctx, chaos4298LiveBatch(orgID, "batch_chaos4298_selfheal_002", source, "cursor-1", "cursor-2")); err != nil {
		t.Fatalf("ApplyProjectionBatch (second write) error = %v", err)
	}
	snapshot, err = adapter.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		t.Fatalf("chaos4155WatermarkSnapshot after second write error = %v", err)
	}
	if got := snapshot[source].Generation; got != 2 {
		t.Fatalf("generation after the second post-deploy write = %d, want 2 -- must increment normally once self-healed", got)
	}
	if got := snapshot[source].Epoch; got != chaos4298SentinelEpoch {
		t.Fatalf("epoch after the second post-deploy write = %q, want it to stay fixed at the sentinel %q -- epoch must never change once a node has one, only a purge should be able to move it", got, chaos4298SentinelEpoch)
	}
}

// TestLiveWatermarkEpochDetectsPurgeAndRebuildLandingOnSameGeneration is
// the team-lead-ordered follow-up proof (2026-08-26) that generation ALONE
// is insufficient across a graph purge: PurgeOrganization deletes the
// whole graph, so a rebuilt (org, source) watermark node's generation
// self-heals back to 1 -- the SAME value a census's first read would have
// seen before the purge, on the very common case of a source touched
// exactly once both before and after. Only epoch (a fresh per-creation
// nonce, reassigned because the rebuild takes writeWatermark's ON CREATE
// branch again) tells the two apart.
func TestLiveWatermarkEpochDetectsPurgeAndRebuildLandingOnSameGeneration(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-org-4298-purge-epoch-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	source := "chaos4298-purge-source"

	batch := chaos4298LiveBatch(orgID, "batch_chaos4298_purge_001", source, "cursor-0", "cursor-1")
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch (pre-purge write) error = %v", err)
	}
	key, err := adapter.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		t.Fatalf("resolveReadKey (pre-purge) error = %v", err)
	}
	before, err := adapter.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		t.Fatalf("chaos4155WatermarkSnapshot (pre-purge) error = %v", err)
	}
	if before[source].Generation != 1 {
		t.Fatalf("precondition: generation before purge = %d, want 1 (first-ever write)", before[source].Generation)
	}

	if err := adapter.PurgeOrganization(ctx, orgID); err != nil {
		t.Fatalf("PurgeOrganization error = %v", err)
	}
	// Rebuild: the SAME source, written again -- a fresh node (MERGE finds
	// nothing after the purge), so generation self-heals back to 1, exactly
	// matching the pre-purge reading on its own.
	if _, err := adapter.ApplyProjectionBatch(ctx, chaos4298LiveBatch(orgID, "batch_chaos4298_purge_002", source, "cursor-0", "cursor-1")); err != nil {
		t.Fatalf("ApplyProjectionBatch (post-purge rebuild) error = %v", err)
	}
	key, err = adapter.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		t.Fatalf("resolveReadKey (post-purge) error = %v", err)
	}
	after, err := adapter.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		t.Fatalf("chaos4155WatermarkSnapshot (post-purge) error = %v", err)
	}
	if after[source].Generation != 1 {
		t.Fatalf("precondition: generation after purge+rebuild = %d, want 1 (fresh node, same self-heal) -- if this isn't 1, the test isn't exercising the same-generation collision it exists to prove", after[source].Generation)
	}
	if after[source].Epoch == before[source].Epoch {
		t.Fatalf("epoch unchanged across a purge+rebuild (%q both times) -- want a DIFFERENT epoch, since the rebuilt node is a genuinely new node", after[source].Epoch)
	}

	if watermarkSnapshotsEqual(before, after) {
		t.Fatalf("watermarkSnapshotsEqual(before, after) = true, want false -- a census straddling this purge+rebuild must see drift, not a coincidentally-stable generation")
	}
}
