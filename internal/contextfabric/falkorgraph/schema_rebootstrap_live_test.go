package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestLivePurgeFromAnotherClientIsHealedOnANonEmptyGraph is the live half of
// TestAPurgeFromAnotherProcessIsHealedByTheNextTick, which a fake cannot
// settle: the serve process bootstraps, ANOTHER client deletes the graph key
// (the rebuild step run by exec), the serve process keeps writing and so
// re-creates the key WITHOUT its schema (nodes present, no indexes, no
// constraints), and the next tick must create the index and the unique
// constraints on that non-empty graph and land the embed batch. Whether
// FalkorDB accepts that DDL on an existing graph is exactly what only a real
// server answers.
func TestLivePurgeFromAnotherClientIsHealedOnANonEmptyGraph(t *testing.T) {
	ctx := context.Background()
	embedder := &axisEmbedder{vectors: map[string][]float32{}}
	serve := startVectorLiveAdapter(t, embedder, 0.55)

	const nodes = 40
	orgID := "live-rebootstrap-" + time.Now().UTC().Format("20060102T150405.000000000")
	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_reb_00000001", OrgID: orgID,
		Source: "rebootstrap-live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2",
		GeneratedAt: observed,
		Entities:    []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	for i := 0; i < nodes; i++ {
		batch.Entities = append(batch.Entities, contextfabric.EntityProjection{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: fmt.Sprintf("p%02d", i), Label: fmt.Sprintf("Rebootstrap Service %02d", i),
			},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_vector_1234"}, ObservedAt: observed, SourceVersion: "v1",
		})
	}

	if _, err := serve.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("first batch (bootstraps the schema): %v", err)
	}
	key, err := serve.resolveWriteKey(ctx, orgID)
	if err != nil {
		t.Fatalf("resolveWriteKey: %v", err)
	}
	if !bootstrapCached(serve, key) {
		t.Fatal("premise: serve must have cached its bootstrap")
	}

	other, err := NewWithEmbedder(Config{
		Addr: serve.config.Addr, GraphPrefix: serve.config.GraphPrefix, RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 2, AllowInsecure: true, TLS: false,
	}, EmbedderOptions{Embedder: embedder, SimilarityFloor: 0.55})
	if err != nil {
		t.Fatalf("second client: %v", err)
	}
	if err := liveSDKAPI(t, other).deleteGraph(ctx, key); err != nil {
		t.Fatalf("purge from the second client: %v", err)
	}

	// Serve writes on: the nodes land in a graph key with no schema, and the
	// vector probe finds no index.
	if _, err := serve.ApplyProjectionBatch(ctx, batch); !errors.Is(err, errVectorIndexNotReady) {
		t.Fatalf("batch after the purge = %v, want errVectorIndexNotReady", err)
	}
	if bootstrapCached(serve, key) {
		t.Fatal("the absent index must drop the cached bootstrap")
	}
	rows, err := serve.api.query(ctx, key, fmt.Sprintf("MATCH (n:%s) RETURN count(n) AS n", labelSubject), nil, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("count nodes: %v", err)
	}
	if count, _ := intFromCount(rows[0]["n"]); count != nodes {
		t.Fatalf("premise: %d nodes on the schema-less graph, want %d", count, nodes)
	}
	if indexes, err := serve.api.indexes(ctx, key); err != nil || len(indexes) != 0 {
		t.Fatalf("premise: indexes = %d (%v) on the purged graph, want none", len(indexes), err)
	}

	if _, err := serve.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("the next tick must bootstrap the non-empty graph and land the batch: %v", err)
	}
	if _, state, err := serve.vectorIndexDimension(ctx, key); err != nil || state != vectorIndexKnown {
		t.Fatalf("vector index after the heal: state %v err %v, want known", state, err)
	}
	constraints, err := serve.api.constraints(ctx, key)
	if err != nil || len(constraints) == 0 {
		t.Fatalf("constraints after the heal = %d (%v), want the unique constraints back", len(constraints), err)
	}
	rows, err = serve.api.query(ctx, key, fmt.Sprintf("MATCH (n:%s) WHERE n.%s IS NOT NULL RETURN count(n) AS n", labelSubject, propEmbedding), nil, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("count embeddings: %v", err)
	}
	if count, _ := intFromCount(rows[0]["n"]); count != nodes {
		t.Fatalf("embeddings after the heal = %d, want %d", count, nodes)
	}
}
