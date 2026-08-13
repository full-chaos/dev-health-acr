package embedprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testDimension is the narrowest width Config.validate accepts, so these
// fixtures stay small while still exercising the real bounds.
const testDimension = 8

func testConfig(baseURL string) Config {
	return Config{
		Provider: "lmstudio", BaseURL: baseURL, Model: "probe-embed", Dimension: testDimension,
		SimilarityFloor: DefaultSimilarityFloor, Timeout: 5 * time.Second,
		MaxBatch: DefaultMaxBatch, MaxTextRunes: DefaultMaxTextRunes,
		MaxTransportRetries: 0, AllowInsecureBaseURL: true,
	}
}

// embeddingsServer serves a canned /v1/embeddings response built by respond.
func embeddingsServer(t *testing.T, respond func(inputs []string) (int, any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		status, payload := respond(body.Input)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// embeddingItem builds a correctly-sized vector whose FIRST value is lead, so
// a test can tell which input a returned vector corresponds to.
func embeddingItem(index int, lead float64) map[string]any {
	values := make([]float64, testDimension)
	values[0] = lead
	return map[string]any{"object": "embedding", "index": index, "embedding": values}
}

// embeddingItemWidth builds a DELIBERATELY wrong-width vector.
func embeddingItemWidth(index, width int) map[string]any {
	return map[string]any{"object": "embedding", "index": index, "embedding": make([]float64, width)}
}

func okResponse(items ...map[string]any) map[string]any {
	return map[string]any{"object": "list", "model": "probe-embed", "data": items,
		"usage": map[string]any{"prompt_tokens": 0, "total_tokens": 0}}
}

// A mis-paired vector attaches one node's meaning to a different node's
// identity, and nothing downstream can detect it. The client must reorder by
// the response's own index rather than trusting arrival order.
func TestEmbedReordersByResponseIndexNotArrivalOrder(t *testing.T) {
	server := embeddingsServer(t, func(inputs []string) (int, any) {
		// Deliberately reversed: index 1 first.
		return http.StatusOK, okResponse(
			embeddingItem(1, 1),
			embeddingItem(0, 0),
		)
	})
	embedder, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	vectors, err := embedder.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if vectors[0][0] != 0 || vectors[1][0] != 1 {
		t.Fatalf("vectors were not reordered by index: %v", vectors)
	}
}

func TestEmbedRejectsMalformedResponseShapes(t *testing.T) {
	cases := []struct {
		name string
		data []map[string]any
		want error
	}{
		{"duplicate index", []map[string]any{embeddingItem(0, 1), embeddingItem(0, 2)}, ErrResponseShape},
		{"index out of range", []map[string]any{embeddingItem(0, 1), embeddingItem(9, 2)}, ErrResponseShape},
		{"wrong dimension", []map[string]any{embeddingItemWidth(0, 3), embeddingItemWidth(1, 3)}, ErrDimensionMismatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := embeddingsServer(t, func(inputs []string) (int, any) {
				return http.StatusOK, okResponse(testCase.data...)
			})
			embedder, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = embedder.Embed(context.Background(), []string{"a", "b"})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Embed error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// A short count must be rejected, not silently padded or truncated.
func TestEmbedRejectsCountMismatch(t *testing.T) {
	server := embeddingsServer(t, func(inputs []string) (int, any) {
		return http.StatusOK, okResponse(embeddingItem(0, 1))
	})
	embedder, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := embedder.Embed(context.Background(), []string{"a", "b"}); !errors.Is(err, ErrResponseShape) {
		t.Fatalf("Embed error = %v, want ErrResponseShape", err)
	}
}

// The provider's response body must never survive into an error string. This
// is the same guarantee modelprovider makes; embedprovider gets it by
// installing the SAME middleware rather than a copy.
func TestProviderErrorBodyNeverReachesTheErrorString(t *testing.T) {
	const secret = "rejected prompt: internal customer roadmap Q3"
	server := embeddingsServer(t, func(inputs []string) (int, any) {
		return http.StatusBadRequest, map[string]any{"error": map[string]any{"message": secret}}
	})
	embedder, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = embedder.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("a 400 response must surface as an error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "roadmap") {
		t.Fatalf("provider response body leaked into the error string: %v", err)
	}
}

// Batching must not lose, duplicate, or reorder texts across chunk
// boundaries.
func TestEmbedBatchesAndPreservesGlobalOrder(t *testing.T) {
	cfg := testConfig("")
	cfg.MaxBatch = 2
	server := embeddingsServer(t, func(inputs []string) (int, any) {
		items := make([]map[string]any, 0, len(inputs))
		for i, input := range inputs {
			// Encode the input's own first byte so ordering is verifiable.
			items = append(items, embeddingItem(i, float64(input[0])))
		}
		return http.StatusOK, okResponse(items...)
	})
	cfg.BaseURL = server.URL
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	texts := []string{"a", "b", "c", "d", "e"}
	vectors, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vectors), len(texts))
	}
	for i, text := range texts {
		if vectors[i][0] != float32(text[0]) {
			t.Fatalf("vector %d does not correspond to text %q: %v", i, text, vectors[i])
		}
	}
}

// D11 class, pinned per the orchestrator's ruling: FalkorDB's vector score is
// a cosine DISTANCE. This test proves the conversion is order-CORRECT -- a
// nearer neighbor always yields a HIGHER similarity -- which is exactly the
// property the raw score does not have.
func TestCosineFromDistanceInvertsFalkorsDistanceIntoASimilarity(t *testing.T) {
	// Both values were measured live against FalkorDB graph module 42002.
	const identicalVectorDistance = 0.0
	const unrelatedVectorDistance = 0.699398159980774

	identical := CosineFromDistance(identicalVectorDistance)
	unrelated := CosineFromDistance(unrelatedVectorDistance)
	if identical != 1 {
		t.Fatalf("an identical vector must have similarity 1, got %v", identical)
	}
	if unrelated >= identical {
		t.Fatalf("a nearer neighbor must score HIGHER: identical=%v unrelated=%v", identical, unrelated)
	}
	// The measured distance corresponds to a cosine of 0.3007.
	if unrelated < 0.30 || unrelated > 0.31 {
		t.Fatalf("similarity for the measured live distance = %v, want ~0.3007", unrelated)
	}
	// Monotone decreasing in distance across the whole usable range.
	previous := 2.0
	for distance := 0.0; distance <= 2.0; distance += 0.01 {
		got := CosineFromDistance(distance)
		if got > previous {
			t.Fatalf("similarity rose from %v to %v as distance grew to %v", previous, got, distance)
		}
		previous = got
	}
	// The negative-similarity half of the range clamps to 0, never below.
	if got := CosineFromDistance(2.0); got != 0 {
		t.Fatalf("maximum distance must clamp to 0, got %v", got)
	}
}

func TestTruncateRunesCutsOnRuneBoundaries(t *testing.T) {
	if got := TruncateRunes("héllo wörld", 5); got != "héllo" {
		t.Fatalf("TruncateRunes = %q, want %q", got, "héllo")
	}
	if got := TruncateRunes("short", 100); got != "short" {
		t.Fatalf("a text under the limit must be unchanged, got %q", got)
	}
	for _, r := range TruncateRunes("日本語テキスト", 3) {
		if r == '�' {
			t.Fatal("truncation split a multi-byte rune")
		}
	}
}
