package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

const vectorLiveTestImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

// axisEmbedder is a DETERMINISTIC stand-in for a real embedding model: each
// text is mapped to a fixed unit vector chosen by a keyword it contains.
//
// A real model is deliberately NOT used here. This test proves the ADAPTER's
// half of vector retrieval against a real FalkorDB server -- index creation,
// the distance-to-similarity conversion, the org predicate, the similarity
// floor, and the band -- none of which should depend on any particular
// model's similarity values. The embedder's own live behavior is covered
// separately, against a real server, in
// embedprovider/live_integration_test.go.
type axisEmbedder struct{ vectors map[string][]float32 }

func (a *axisEmbedder) Identity() contextfabric.EmbedderIdentity {
	return contextfabric.EmbedderIdentity{Provider: "axis", Model: "axis-probe", Dimension: 4}
}

func (a *axisEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vector, ok := a.vectors[text]
		if !ok {
			// An unknown text gets a vector orthogonal to every named axis,
			// so it is far from all of them -- the no-match control.
			vector = []float32{0, 0, 0, 1}
		}
		out[i] = vector
	}
	return out, nil
}

func startVectorLiveAdapter(t *testing.T, embedder contextfabric.Embedder, floor float64) *Adapter {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: vectorLiveTestImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start FalkorDB container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container.Host(): %v", err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatalf("container.MappedPort(): %v", err)
	}
	adapter, err := NewWithEmbedder(Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-live-vec", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	}, EmbedderOptions{Embedder: embedder, SimilarityFloor: floor})
	if err != nil {
		t.Fatalf("NewWithEmbedder(): %v", err)
	}
	return adapter
}

// The end-to-end live proof: projection writes vectors, the index is created
// and queryable, the DISTANCE the server returns is converted to a
// similarity, and the resulting confidences are order-correct on real server
// output rather than on hand-picked numbers.
func TestLiveVectorSearchNormalizesRealFalkorDistances(t *testing.T) {
	authVector := []float32{1, 0, 0, 0}
	nearAuthVector := []float32{0.9701425, 0.24253562, 0, 0} // cosine 0.9701 with authVector
	billingVector := []float32{0, 1, 0, 0}                   // cosine 0 with authVector

	embedder := &axisEmbedder{vectors: map[string][]float32{}}
	adapter := startVectorLiveAdapter(t, embedder, 0.55)

	orgID := "live-vector-" + time.Now().UTC().Format("20060102T150405.000000000")
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
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_vec_00000001", OrgID: orgID,
		Source: "vector-live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2",
		GeneratedAt: observed,
		// Every array must be non-nil to satisfy the v1 batch bounds.
		Entities: []contextfabric.EntityProjection{}, Relationships: []contextfabric.RelationshipProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	for _, entity := range entities {
		projection := contextfabric.EntityProjection{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: entity.id, Label: entity.label,
			},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_vector_1234"}, ObservedAt: observed, SourceVersion: "v1",
		}
		embedder.vectors[entitySearchText(projection)] = entity.vector
		batch.Entities = append(batch.Entities, projection)
	}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), batch); err != nil {
		t.Fatalf("ApplyProjectionBatch(): %v", err)
	}

	key := graphKey(adapter.config.GraphPrefix, orgID)
	candidates, truncated, err := adapter.vectorSearchNodes(context.Background(), key, orgID, authVector, 0.55, 10)
	if err != nil {
		t.Fatalf("vectorSearchNodes(): %v", err)
	}
	if truncated {
		t.Fatal("three nodes under a limit of ten must not report truncation")
	}
	// billing is orthogonal (cosine 0), so the similarity floor must have
	// dropped it -- AC-3778-4 against a real server.
	if len(candidates) != 2 {
		t.Fatalf("expected exactly the two above-floor neighbors, got %d: %#v", len(candidates), candidates)
	}
	byLabel := map[string]float64{}
	for _, candidate := range candidates {
		if candidate.Score != nil {
			t.Fatalf("a vector candidate must leave Score nil, got %v", *candidate.Score)
		}
		if candidate.Relevance == nil {
			t.Fatal("a vector candidate must declare Relevance")
		}
		if candidate.Mechanism != contextfabric.MatchVector {
			t.Fatalf("mechanism = %q, want vector", candidate.Mechanism)
		}
		byLabel[candidate.Name] = *candidate.Relevance
	}
	exact, ok := byLabel["Authentication Service"]
	if !ok {
		t.Fatalf("the exact vector match is missing: %#v", byLabel)
	}
	near, ok := byLabel["Session and Login Handling"]
	if !ok {
		t.Fatalf("the near vector match is missing: %#v", byLabel)
	}
	// THE POINT: on real server output, a NEARER neighbor scores HIGHER.
	// The raw score has the opposite order (the exact match's distance is 0).
	if exact <= near {
		t.Fatalf("confidence order inverted on live output: exact=%v near=%v", exact, near)
	}
	if exact > vectorRelevanceCeiling || near < vectorRelevanceFloor {
		t.Fatalf("live confidences escaped the [%v, %v] band: exact=%v near=%v",
			vectorRelevanceFloor, vectorRelevanceCeiling, exact, near)
	}
	// AC-3778-3, against a real server: no vector-only confidence commits.
	if exact >= 0.72 {
		t.Fatalf("a live vector-only candidate reached %v, at or past the lone-candidate gate", exact)
	}
	t.Logf("live vector confidences: exact=%v near=%v (band [%v, %v])",
		exact, near, vectorRelevanceFloor, vectorRelevanceCeiling)
}

// AC-3778-4's control, end to end: a question whose vector is orthogonal to
// every stored subject must return NOTHING, not the nearest k.
func TestLiveVectorSearchReturnsNoMatchForAnUnrelatedQuestion(t *testing.T) {
	embedder := &axisEmbedder{vectors: map[string][]float32{}}
	adapter := startVectorLiveAdapter(t, embedder, 0.55)

	orgID := "live-vector-nomatch-" + time.Now().UTC().Format("20060102T150405.000000000")
	projection := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectProject, CanonicalID: "auth", Label: "Authentication Service",
		},
		Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
		EvidenceRefIDs: []string{"evidence_vector_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
	}
	embedder.vectors[entitySearchText(projection)] = []float32{1, 0, 0, 0}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_vec_00000002", OrgID: orgID,
		Source: "vector-live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2",
		GeneratedAt:   projection.ObservedAt,
		Entities:      []contextfabric.EntityProjection{projection},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}); err != nil {
		t.Fatalf("ApplyProjectionBatch(): %v", err)
	}

	key := graphKey(adapter.config.GraphPrefix, orgID)
	// Orthogonal to the only stored subject: the index WILL return it as the
	// nearest neighbor, because a k-NN query always does. The floor is the
	// only thing standing between that and a confident wrong subject.
	candidates, _, err := adapter.vectorSearchNodes(context.Background(), key, orgID, []float32{0, 0, 0, 1}, 0.55, 10)
	if err != nil {
		t.Fatalf("vectorSearchNodes(): %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("an unrelated question must return no vector candidates, got %#v", candidates)
	}
}

// The org predicate is a POST-filter on the k-NN result, so it must be proven
// against a real server that another organization's subject can never surface.
func TestLiveVectorSearchNeverCrossesOrganizations(t *testing.T) {
	embedder := &axisEmbedder{vectors: map[string][]float32{}}
	adapter := startVectorLiveAdapter(t, embedder, 0.55)

	orgID := "live-vector-org-" + time.Now().UTC().Format("20060102T150405.000000000")
	projection := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectProject, CanonicalID: "auth", Label: "Authentication Service",
		},
		Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
		EvidenceRefIDs: []string{"evidence_vector_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
	}
	embedder.vectors[entitySearchText(projection)] = []float32{1, 0, 0, 0}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_vec_00000003", OrgID: orgID,
		Source: "vector-live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2",
		GeneratedAt:   projection.ObservedAt,
		Entities:      []contextfabric.EntityProjection{projection},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}); err != nil {
		t.Fatalf("ApplyProjectionBatch(): %v", err)
	}

	key := graphKey(adapter.config.GraphPrefix, orgID)
	// A perfect vector match, but asked for on behalf of a DIFFERENT org.
	candidates, _, err := adapter.vectorSearchNodes(context.Background(), key, "some-other-org", []float32{1, 0, 0, 0}, 0.55, 10)
	if err != nil {
		t.Fatalf("vectorSearchNodes(): %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("a foreign organization must never see a subject, got %#v", candidates)
	}
}

// FalkorDB's own hard dimension error is the second fail-closed layer beneath
// the adapter's fence. Proving it live keeps the fence's doc comment honest.
func TestLiveWrongDimensionQueryIsRejectedByTheServer(t *testing.T) {
	embedder := &axisEmbedder{vectors: map[string][]float32{}}
	adapter := startVectorLiveAdapter(t, embedder, 0.55)

	orgID := "live-vector-dim-" + time.Now().UTC().Format("20060102T150405.000000000")
	projection := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectProject, CanonicalID: "auth", Label: "Authentication Service",
		},
		Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
		EvidenceRefIDs: []string{"evidence_vector_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
	}
	embedder.vectors[entitySearchText(projection)] = []float32{1, 0, 0, 0}
	if _, err := adapter.ApplyProjectionBatch(context.Background(), contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_vec_00000004", OrgID: orgID,
		Source: "vector-live-test", SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2",
		GeneratedAt:   projection.ObservedAt,
		Entities:      []contextfabric.EntityProjection{projection},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}); err != nil {
		t.Fatalf("ApplyProjectionBatch(): %v", err)
	}

	key := graphKey(adapter.config.GraphPrefix, orgID)
	// Two values against a four-dimension index.
	_, _, err := adapter.vectorSearchNodes(context.Background(), key, orgID, []float32{1, 0}, 0.55, 10)
	if err == nil {
		t.Fatal("a wrong-width query must be rejected by the server, not silently answered")
	}
}

// The vector index must be discoverable with its dimension, which is what
// AC-3778-7's fence reads. If a server version stopped reporting it, the fence
// would silently degrade to "unknown" and this test says so.
func TestLiveVectorIndexReportsItsDimension(t *testing.T) {
	embedder := &axisEmbedder{vectors: map[string][]float32{}}
	adapter := startVectorLiveAdapter(t, embedder, 0.55)

	orgID := "live-vector-idx-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	if err := adapter.ensureOrgGraph(context.Background(), key); err != nil {
		t.Fatalf("ensureOrgGraph(): %v", err)
	}
	dimension, found, err := adapter.vectorIndexDimension(context.Background(), key)
	if err != nil {
		t.Fatalf("vectorIndexDimension(): %v", err)
	}
	if !found {
		t.Fatal("the vector index must be discoverable via db.indexes()")
	}
	if dimension != embedder.Identity().Dimension {
		t.Fatalf("reported dimension = %d, want %d", dimension, embedder.Identity().Dimension)
	}
	if !adapter.vectorEnabledForKey(key) {
		t.Fatal("a freshly bootstrapped, matching index must leave vector retrieval enabled")
	}
	_ = embedprovider.DefaultSimilarityFloor
}
