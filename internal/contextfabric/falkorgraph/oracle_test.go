package falkorgraph

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

func TestTrueCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float64
		want float64
	}{
		{"identical unit vectors", []float64{1, 0, 0}, []float64{1, 0, 0}, 1},
		{"orthogonal", []float64{1, 0, 0}, []float64{0, 1, 0}, 0},
		{"opposite", []float64{1, 0, 0}, []float64{-1, 0, 0}, -1},
		{"empty a", nil, []float64{1, 0}, 0},
		{"mismatched lengths", []float64{1, 0}, []float64{1, 0, 0}, 0},
		{"zero vector b", []float64{1, 2, 3}, []float64{0, 0, 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trueCosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("trueCosineSimilarity(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestTrueCosineSimilarityIsNotADotProduct proves the ORACLE'S defining
// property (embed-text spec §5 L1): stored vectors are unnormalized, so a
// bare dot product would rank a longer vector as "more similar" purely by
// magnitude. Scaling one operand must not move the cosine at all.
func TestTrueCosineSimilarityIsNotADotProduct(t *testing.T) {
	a := []float64{3, 4, 0} // norm 5
	b := []float64{1, 0, 0} // norm 1, same direction as neither -- angle matters
	baseline := trueCosineSimilarity(a, b)

	scaled := []float64{30, 40, 0} // same direction as a, 10x magnitude
	got := trueCosineSimilarity(scaled, b)

	if math.Abs(got-baseline) > 1e-9 {
		t.Fatalf("cosine changed under pure rescaling: baseline=%v scaled=%v -- this is measuring dot product, not cosine", baseline, got)
	}
	// A bare dot product WOULD have changed 10x here, so this also documents
	// what a regression to dot-product ranking would look like.
	dotBaseline := a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
	dotScaled := scaled[0]*b[0] + scaled[1]*b[1] + scaled[2]*b[2]
	if dotBaseline == dotScaled {
		t.Fatal("test fixture does not actually distinguish cosine from dot product")
	}
}

func TestTrueCosineSimilarityClampsFloatDrift(t *testing.T) {
	// A pair of parallel vectors with enough dimensions for accumulated
	// float64 rounding to occasionally push the ratio a hair over 1.
	a := make([]float64, 500)
	b := make([]float64, 500)
	for i := range a {
		a[i] = 1.0000001
		b[i] = 1.0000001
	}
	got := trueCosineSimilarity(a, b)
	if got > 1 || got < -1 {
		t.Fatalf("similarity %v escaped [-1, 1]", got)
	}
}

func TestDecodeVectorProperty(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  []float64
		ok    bool
	}{
		{"typed float64 slice", []float64{1, 2, 3}, []float64{1, 2, 3}, true},
		{"typed float32 slice", []float32{1, 2, 3}, []float64{1, 2, 3}, true},
		{"decoded interface slice of float64", []interface{}{1.5, -2.5}, []float64{1.5, -2.5}, true},
		{"decoded interface slice with int64", []interface{}{int64(1), int64(2)}, []float64{1, 2}, true},
		{"nil", nil, nil, false},
		{"string", "not a vector", nil, false},
		{"mixed-type slice", []interface{}{1.0, "oops"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := decodeVectorProperty(tt.value)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestBruteForceRankOrdersDescendingByTrueCosine(t *testing.T) {
	query := []float64{1, 0}
	corpus := []oracleVector{
		{Kind: "project", CanonicalID: "far", Vector: []float64{0, 1}},
		{Kind: "project", CanonicalID: "exact", Vector: []float64{5, 0}}, // same direction, different magnitude
		{Kind: "project", CanonicalID: "near", Vector: []float64{1, 0.1}},
	}
	ranked := bruteForceRank(query, corpus)
	if len(ranked) != 3 {
		t.Fatalf("got %d ranked entries, want 3", len(ranked))
	}
	if ranked[0].CanonicalID != "exact" {
		t.Fatalf("top match = %q, want %q", ranked[0].CanonicalID, "exact")
	}
	if ranked[0].Similarity < ranked[1].Similarity || ranked[1].Similarity < ranked[2].Similarity {
		t.Fatalf("ranking is not descending: %+v", ranked)
	}
	if ranked[2].CanonicalID != "far" {
		t.Fatalf("bottom match = %q, want %q", ranked[2].CanonicalID, "far")
	}
}

func TestBruteForceRankIsDeterministicOnTies(t *testing.T) {
	query := []float64{1, 0}
	corpus := []oracleVector{
		{Kind: "project", CanonicalID: "b", Vector: []float64{1, 0}},
		{Kind: "project", CanonicalID: "a", Vector: []float64{1, 0}},
	}
	first := bruteForceRank(query, corpus)
	second := bruteForceRank(query, corpus)
	if first[0].CanonicalID != "a" || second[0].CanonicalID != "a" {
		t.Fatalf("tie-break is not stable/deterministic: %+v / %+v", first, second)
	}
}

func TestFindVector(t *testing.T) {
	corpus := []oracleVector{{Kind: "project", CanonicalID: "p1", Vector: []float64{1}}}
	if _, ok := findVector(corpus, "project", "p1"); !ok {
		t.Fatal("expected to find p1")
	}
	if _, ok := findVector(corpus, "project", "missing"); ok {
		t.Fatal("expected not to find a canonical id absent from the corpus")
	}
}

func TestContainsSubject(t *testing.T) {
	matches := []oracleMatch{{oracleVector: oracleVector{Kind: "project", CanonicalID: "p1"}}}
	if !containsSubject(matches, "project", "p1") {
		t.Fatal("expected containsSubject to find p1")
	}
	if containsSubject(matches, "project", "p2") {
		t.Fatal("containsSubject found a subject that was not there")
	}
}

func TestContainsANNCandidate(t *testing.T) {
	candidates := []graphrank.CandidateNode{
		{Attributes: map[string]interface{}{propKind: "project", propCanonicalID: "p1"}},
	}
	if !containsANNCandidate(candidates, "project", "p1") {
		t.Fatal("expected containsANNCandidate to find p1")
	}
	if containsANNCandidate(candidates, "work_item", "p1") {
		t.Fatal("containsANNCandidate matched across kinds")
	}
}

func TestBestWrongNeighborSkipsTheCorrectAnswer(t *testing.T) {
	ranked := []oracleMatch{
		{oracleVector: oracleVector{Kind: "project", CanonicalID: "correct"}, Similarity: 0.9},
		{oracleVector: oracleVector{Kind: "project", CanonicalID: "imposter"}, Similarity: 0.8},
	}
	got, ok := bestWrongNeighbor(ranked, "project", "correct")
	if !ok {
		t.Fatal("expected a wrong neighbor")
	}
	if got.CanonicalID != "imposter" {
		t.Fatalf("got %q, want %q", got.CanonicalID, "imposter")
	}
}

func TestBestWrongNeighborReportsNoneForASingleNodeCorpus(t *testing.T) {
	ranked := []oracleMatch{{oracleVector: oracleVector{Kind: "project", CanonicalID: "correct"}, Similarity: 1}}
	if _, ok := bestWrongNeighbor(ranked, "project", "correct"); ok {
		t.Fatal("a corpus with only the correct answer has no wrong neighbor")
	}
}

func TestFetchEmbedderFenceCorpusRequiresAnEmbedder(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	_, err := adapter.fetchEmbedderFenceCorpus(context.Background(), "key", "org")
	if !errors.Is(err, errOracleEmbedderRequired) {
		t.Fatalf("got %v, want errOracleEmbedderRequired", err)
	}
}

func TestFetchEmbedderFenceCorpusScopesToOrgAndIdentityAndDecodesRows(t *testing.T) {
	var capturedCypher string
	var capturedParams map[string]interface{}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		capturedCypher = cypher
		capturedParams = params
		return []row{
			// Valid.
			{"n": &node{Properties: map[string]interface{}{
				propKind: "project", propCanonicalID: "p1", propLabel: "Auth",
				propEmbedding: []interface{}{1.0, 0.0, 0.0},
			}}},
			// Missing canonical id -- skipped, not fatal.
			{"n": &node{Properties: map[string]interface{}{
				propKind: "project", propEmbedding: []interface{}{1.0, 0.0, 0.0},
			}}},
			// Malformed embedding -- skipped, not fatal.
			{"n": &node{Properties: map[string]interface{}{
				propKind: "project", propCanonicalID: "p2", propEmbedding: "not-a-vector",
			}}},
			// Not a node at all -- skipped, not fatal.
			{"n": "unexpected"},
		}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0}}, 0.5)
	corpus, err := adapter.fetchEmbedderFenceCorpus(context.Background(), "key", "org-1")
	if err != nil {
		t.Fatalf("fetchEmbedderFenceCorpus: %v", err)
	}
	if len(corpus) != 1 {
		t.Fatalf("got %d corpus entries, want 1 (malformed rows must be skipped, not fatal): %+v", len(corpus), corpus)
	}
	if corpus[0].CanonicalID != "p1" || corpus[0].Kind != "project" {
		t.Fatalf("unexpected corpus entry: %+v", corpus[0])
	}
	if len(corpus[0].Vector) != 3 {
		t.Fatalf("vector not decoded: %+v", corpus[0].Vector)
	}
	if capturedParams["org"] != "org-1" {
		t.Fatalf("org predicate not bound: %+v", capturedParams)
	}
	if capturedParams["identity"] != "stub/stub-embed" {
		t.Fatalf("identity predicate not bound to the configured embedder's identity: %+v", capturedParams)
	}
	for _, want := range []string{propOrgID, propEmbedding, propEmbedderIdentity} {
		if !strings.Contains(capturedCypher, want) {
			t.Fatalf("cypher %q missing expected predicate on %q", capturedCypher, want)
		}
	}
}
