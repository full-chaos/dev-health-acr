package falkorgraph

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
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

func TestAboveFloorDropsAtOrBelowTauKeepsStrictlyGreater(t *testing.T) {
	ranked := []oracleMatch{
		{oracleVector: oracleVector{CanonicalID: "above"}, Similarity: 0.56},
		{oracleVector: oracleVector{CanonicalID: "at-tau"}, Similarity: 0.55},
		{oracleVector: oracleVector{CanonicalID: "below"}, Similarity: 0.10},
	}
	got := aboveFloor(ranked, 0.55)
	if len(got) != 1 || got[0].CanonicalID != "above" {
		t.Fatalf("aboveFloor(0.55) = %+v, want only %q (a similarity AT tau must be dropped, matching vector.go's <= tau rule)", got, "above")
	}
}

func TestTopKInclusiveIncludesBoundaryTies(t *testing.T) {
	// Two entries tied at the K=2 boundary similarity (0.5): a plain
	// ranked[:2] slice would arbitrarily keep one and drop the other.
	ranked := []oracleMatch{
		{oracleVector: oracleVector{CanonicalID: "first"}, Similarity: 0.9},
		{oracleVector: oracleVector{CanonicalID: "tied-a"}, Similarity: 0.5},
		{oracleVector: oracleVector{CanonicalID: "tied-b"}, Similarity: 0.5},
		{oracleVector: oracleVector{CanonicalID: "far"}, Similarity: 0.1},
	}
	got := topKInclusive(ranked, 2)
	if len(got) != 3 {
		t.Fatalf("topKInclusive(k=2) = %+v, want 3 entries (both boundary ties included)", got)
	}
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.CanonicalID] = true
	}
	if !ids["tied-a"] || !ids["tied-b"] {
		t.Fatalf("topKInclusive dropped a boundary tie: %+v", got)
	}
	if ids["far"] {
		t.Fatalf("topKInclusive included a row past the boundary: %+v", got)
	}
}

func TestTopKInclusiveNoTieMatchesPlainSlice(t *testing.T) {
	ranked := []oracleMatch{
		{oracleVector: oracleVector{CanonicalID: "a"}, Similarity: 0.9},
		{oracleVector: oracleVector{CanonicalID: "b"}, Similarity: 0.5},
		{oracleVector: oracleVector{CanonicalID: "c"}, Similarity: 0.1},
	}
	got := topKInclusive(ranked, 2)
	if len(got) != 2 || got[0].CanonicalID != "a" || got[1].CanonicalID != "b" {
		t.Fatalf("topKInclusive(k=2) with no tie = %+v, want [a b]", got)
	}
}

func TestTopKInclusiveKAtOrPastLength(t *testing.T) {
	ranked := []oracleMatch{{oracleVector: oracleVector{CanonicalID: "a"}, Similarity: 0.9}}
	if got := topKInclusive(ranked, 5); len(got) != 1 {
		t.Fatalf("k past length: got %+v, want the whole (1-entry) ranking", got)
	}
	if got := topKInclusive(ranked, 0); got != nil {
		t.Fatalf("k=0: got %+v, want nil", got)
	}
	if got := topKInclusive(nil, 5); got != nil {
		t.Fatalf("empty ranking: got %+v, want nil", got)
	}
}

func TestSubjectExistenceDistinguishesMissingFromUnembedded(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch params["id"] {
		case "exists-embedded":
			return []row{{"n": &node{Properties: map[string]interface{}{
				propKind: "project", propCanonicalID: "exists-embedded", propEmbedding: []interface{}{1.0, 0.0},
			}}}}, nil
		case "exists-unembedded":
			return []row{{"n": &node{Properties: map[string]interface{}{
				propKind: "project", propCanonicalID: "exists-unembedded",
			}}}}, nil
		default:
			return nil, nil // not found
		}
	}}
	adapter := newFakeAdapter(t, fake)

	exists, embedded, err := adapter.subjectExistence(context.Background(), "key", "org", "project", "exists-embedded")
	if err != nil || !exists || !embedded {
		t.Fatalf("exists-embedded: exists=%v embedded=%v err=%v, want true/true/nil", exists, embedded, err)
	}
	exists, embedded, err = adapter.subjectExistence(context.Background(), "key", "org", "project", "exists-unembedded")
	if err != nil || !exists || embedded {
		t.Fatalf("exists-unembedded: exists=%v embedded=%v err=%v, want true/false/nil", exists, embedded, err)
	}
	exists, embedded, err = adapter.subjectExistence(context.Background(), "key", "org", "project", "missing")
	if err != nil || exists || embedded {
		t.Fatalf("missing: exists=%v embedded=%v err=%v, want false/false/nil", exists, embedded, err)
	}
}

func TestRedactTextIncludeRawReturnsVerbatim(t *testing.T) {
	if got := redactText("what is the auth project doing", true); got != "what is the auth project doing" {
		t.Fatalf("includeRaw=true must return the text unchanged, got %q", got)
	}
}

func TestRedactTextDefaultHidesRawContent(t *testing.T) {
	raw := "contact jane.doe@example.com about the auth work"
	got := redactText(raw, false)
	if got == raw {
		t.Fatal("redactText(includeRaw=false) must not return the raw text")
	}
	if strings.Contains(got, "jane.doe") || strings.Contains(got, "example.com") {
		t.Fatalf("redacted output leaks raw content: %q", got)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("redacted output = %q, want a sha256: prefix", got)
	}
}

func TestRedactTextIsStableAndDistinguishing(t *testing.T) {
	a := redactText("question one", false)
	b := redactText("question one", false)
	c := redactText("question two", false)
	if a != b {
		t.Fatalf("redactText is not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("two different inputs redacted to the same digest")
	}
}

func TestDedupeHardNegativesKeepsHighestSimilarityPerSubjectAndCapsAtLimit(t *testing.T) {
	negatives := []hardNegative{
		{Kind: "project", CanonicalID: "p1", Similarity: 0.4},
		{Kind: "project", CanonicalID: "p1", Similarity: 0.7}, // same subject, higher similarity -- must win
		{Kind: "project", CanonicalID: "p2", Similarity: 0.6},
		{Kind: "project", CanonicalID: "p3", Similarity: 0.5},
	}
	got := dedupeHardNegatives(negatives, 2)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (capped at limit): %+v", len(got), got)
	}
	if got[0].CanonicalID != "p1" || got[0].Similarity != 0.7 {
		t.Fatalf("top entry = %+v, want p1 at 0.7 (the higher of its two duplicate observations)", got[0])
	}
	if got[1].CanonicalID != "p2" {
		t.Fatalf("second entry = %+v, want p2", got[1])
	}
}

func TestDedupeHardNegativesLimitZeroReturnsNone(t *testing.T) {
	negatives := []hardNegative{{Kind: "project", CanonicalID: "p1", Similarity: 0.9}}
	got := dedupeHardNegatives(negatives, 0)
	if len(got) != 0 {
		t.Fatalf("limit=0 must return no hard negatives, got %+v", got)
	}
}

func TestWriteFileMode0600OverwritesAPreExistingLoosePermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	// Pre-create the path at a world-readable mode -- the exact scenario
	// codex round-2 flagged: os.WriteFile's mode argument only governs a
	// NEW file, so overwriting a report left behind at 0644 (an earlier
	// run, a permissive umask, a reused ACR_TEST_ORACLE_OUTPUT path) must
	// not silently keep that mode.
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("pre-create fixture: %v", err)
	}
	if err := writeFileMode0600(path, []byte(`{"fresh":true}`)); err != nil {
		t.Fatalf("writeFileMode0600: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0600 (must be enforced on overwrite, not only on creation)", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != `{"fresh":true}` {
		t.Fatalf("content = %q, want the fresh write (O_TRUNC must discard the stale content)", content)
	}
}

func TestWriteFileMode0600OnANewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeFileMode0600(path, []byte("data")); err != nil {
		t.Fatalf("writeFileMode0600: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0600", got)
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
