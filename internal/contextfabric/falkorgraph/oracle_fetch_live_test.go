package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestLiveFetchEmbedderFenceCorpusScopesToOrgAndIdentity is the real-server
// proof for oracle.go's corpus fetch: real projected vectors, real Cypher
// decode, real identity-fence exclusion -- against an ephemeral testcontainers
// FalkorDB, following this package's established live-test convention
// (vector_live_test.go), not the ACR_TEST_* externally-supplied-graph
// convention oracle_live_test.go uses for the withheld ambiguity corpus.
func TestLiveFetchEmbedderFenceCorpusScopesToOrgAndIdentity(t *testing.T) {
	authVector := []float32{1, 0, 0, 0}
	nearAuthVector := []float32{0.9701425, 0.24253562, 0, 0} // cosine 0.9701 with authVector
	billingVector := []float32{0, 1, 0, 0}                   // orthogonal to authVector

	embedder := &axisEmbedder{vectors: map[string][]float32{}}
	adapter := startVectorLiveAdapter(t, embedder, 0.55)

	orgID := "live-oracle-" + time.Now().UTC().Format("20060102T150405.000000000")
	observed := time.Now().UTC()

	entities := []struct {
		id, label string
		vector    []float32
	}{
		{"auth", "Authentication Service", authVector},
		{"session", "Session and Login Handling", nearAuthVector},
		{"billing", "Billing Reconciliation", billingVector},
	}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_oracle_00000001", OrgID: orgID,
		Source: "oracle-live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2",
		GeneratedAt: observed,
		Entities:    []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	for _, entity := range entities {
		projection := contextfabric.EntityProjection{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: entity.id, Label: entity.label,
			},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_oracle_1234"}, ObservedAt: observed, SourceVersion: "v1",
		}
		embedder.vectors[entitySearchText(projection)] = entity.vector
		batch.Entities = append(batch.Entities, projection)
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("ApplyProjectionBatch(): %v", err)
	}

	key := graphKey(adapter.config.GraphPrefix, orgID)
	ctx := context.Background()

	// Stale-identity write, done out of band via raw Cypher (the batch API
	// always stamps the CURRENTLY configured embedder's identity, so this is
	// the only way to construct the AC-3778-7 fence-failing shape this test
	// needs): "billing" now carries a vector under a DIFFERENT identity.
	// fetchEmbedderFenceCorpus must exclude it even though node.embedding is
	// still present, exactly like verifyStoredEmbedderIdentity's own
	// predicate.
	staleCypher := "MATCH (n:" + labelSubject + " {" + propOrgID + ":$org, " + propCanonicalID + ":$id}) SET n." + propEmbedderIdentity + " = $identity"
	if _, err := adapter.api.query(ctx, key, staleCypher, map[string]interface{}{"org": orgID, "id": "billing", "identity": "other-provider/other-model"}, false); err != nil {
		t.Fatalf("stamp stale identity: %v", err)
	}

	corpus, err := adapter.fetchEmbedderFenceCorpus(ctx, key, orgID)
	if err != nil {
		t.Fatalf("fetchEmbedderFenceCorpus(): %v", err)
	}
	if len(corpus) != 2 {
		t.Fatalf("expected 2 fence-passing vectors (billing excluded by stale identity), got %d: %+v", len(corpus), corpus)
	}
	byID := map[string]oracleVector{}
	for _, v := range corpus {
		byID[v.CanonicalID] = v
	}
	if _, ok := byID["billing"]; ok {
		t.Fatal("billing carries a stale embedder_identity and must be excluded from the fence-passing corpus")
	}
	auth, ok := byID["auth"]
	if !ok {
		t.Fatal("auth is missing from the fence-passing corpus")
	}
	session, ok := byID["session"]
	if !ok {
		t.Fatal("session is missing from the fence-passing corpus")
	}
	if len(auth.Vector) != 4 || len(session.Vector) != 4 {
		t.Fatalf("decoded vectors have the wrong width: auth=%v session=%v", auth.Vector, session.Vector)
	}

	// Cross-check against the SAME real server's own ANN ordering
	// (vectorSearchNodes, proven live in vector_live_test.go): brute force
	// over the fence-passing corpus must rank "auth" (self) above "session"
	// (near) for an "auth" query, exactly like the live ANN path does.
	ranked := bruteForceRank(float64Vector(authVector), corpus)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked entries, got %d", len(ranked))
	}
	if ranked[0].CanonicalID != "auth" || ranked[1].CanonicalID != "session" {
		t.Fatalf("oracle ranking = %+v, want [auth, session]", ranked)
	}
	if ranked[0].Similarity <= ranked[1].Similarity {
		t.Fatalf("exact match did not score above the near neighbor: %+v", ranked)
	}
	// True cosine, verified against the real (unnormalized-write-path)
	// stored vector: the exact match must be (numerically) a perfect 1.0.
	if diff := ranked[0].Similarity - 1.0; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("exact-match true cosine = %v, want 1.0", ranked[0].Similarity)
	}
}
