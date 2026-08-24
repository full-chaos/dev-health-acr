package embedprovider

import (
	"context"
	"math"
	"os"
	"testing"
	"time"
)

// Live acceptance for CHAOS-3778's embedder. Unlike provider_test.go, which
// serves canned responses, this file talks to a REAL OpenAI-compatible
// embeddings server. It is skipped unless one is named explicitly:
//
//	ACR_TEST_EMBED_BASE_URL=http://localhost:1234/v1/ \
//	ACR_TEST_EMBED_MODEL=text-embedding-nomic-embed-text-v1.5 \
//	ACR_TEST_EMBED_DIMENSION=768 \
//	  go test ./internal/contextfabric/embedprovider -run Live -v
//
// The development vehicle is LM Studio on loopback (TRD §19.4.2: local,
// OpenAI-compatible, no API spend, no credential). Nothing here depends on
// that particular server -- the endpoint, model, and dimension all come from
// the environment, and no assertion below names a vendor.
//
// These tests deliberately assert only STRUCTURAL properties (shape, order,
// dimension, norm, and the ordering of similarities between clearly related
// and clearly unrelated texts). They never assert a specific similarity value,
// because that would bind the suite to one embedding model, which §19.4.2
// forbids.
//
// No response content is ever logged.
func liveEmbedder(t *testing.T) *Embedder {
	t.Helper()
	baseURL := os.Getenv("ACR_TEST_EMBED_BASE_URL")
	if baseURL == "" {
		t.Skip("ACR_TEST_EMBED_BASE_URL is not set; skipping the live embedder acceptance")
	}
	lookup := func(key string) (string, bool) {
		switch key {
		case EnvBaseURL:
			return baseURL, true
		case EnvModel:
			return os.Getenv("ACR_TEST_EMBED_MODEL"), true
		case EnvDimension:
			return os.Getenv("ACR_TEST_EMBED_DIMENSION"), true
		case EnvProvider:
			return "live-test", true
		case EnvAllowInsecureBaseURL:
			return "true", true
		case EnvAllowNoCredential:
			// The development vehicle (LM Studio on loopback, per this
			// file's own doc comment) genuinely needs no credential --
			// explicit opt-in (CHAOS-4192), matching AllowInsecureBaseURL
			// above.
			return "true", true
		case EnvTimeout:
			// A cold model load was measured at 9.3 s against 10-17 ms warm,
			// so the FIRST live call needs a probe-sized budget rather than
			// the read path's 250 ms.
			return ProbeTimeout.String(), true
		default:
			return "", false
		}
	}
	embedder, err := FromEnv(lookup)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	return embedder
}

func TestLiveEmbedderReturnsWellFormedVectors(t *testing.T) {
	embedder := liveEmbedder(t)
	texts := []string{
		"the authentication service",
		"login and identity handling",
		"quarterly finance reconciliation",
	}
	vectors, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vectors), len(texts))
	}
	identity := embedder.Identity()
	for i, vector := range vectors {
		if len(vector) != identity.Dimension {
			t.Fatalf("vector %d has width %d, want the configured %d", i, len(vector), identity.Dimension)
		}
		var sum float64
		var nonZero bool
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				t.Fatalf("vector %d contains a non-finite value", i)
			}
			if value != 0 {
				nonZero = true
			}
			sum += float64(value) * float64(value)
		}
		if !nonZero {
			t.Fatalf("vector %d is all zeros", i)
		}
		if math.Sqrt(sum) == 0 {
			t.Fatalf("vector %d has zero norm", i)
		}
	}
}

// The semantic ordering that makes vector retrieval worth having at all: a
// paraphrase must be closer to its subject than an unrelated topic is. This is
// asserted as an ORDERING, never as a threshold, so it holds for any competent
// embedding model.
func TestLiveEmbedderRanksAParaphraseAboveAnUnrelatedTopic(t *testing.T) {
	embedder := liveEmbedder(t)
	vectors, err := embedder.Embed(context.Background(), []string{
		"the authentication service",       // the subject
		"login and identity handling",      // a paraphrase of it
		"quarterly finance reconciliation", // unrelated
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	paraphrase := cosine(vectors[0], vectors[1])
	unrelated := cosine(vectors[0], vectors[2])
	if paraphrase <= unrelated {
		t.Fatalf("a paraphrase must be nearer than an unrelated topic: paraphrase=%v unrelated=%v", paraphrase, unrelated)
	}
}

// AC-3778-5's cost side, measured against the real server. This asserts the
// WARM budget only; the cold path is what ProbeTimeout and the read path's
// fail-open behavior exist for.
func TestLiveEmbedderWarmSingleCallFitsTheRetrievalBudget(t *testing.T) {
	embedder := liveEmbedder(t)
	ctx := context.Background()
	// Warm the model first; a cold load is not what this measures.
	if _, err := embedder.Embed(ctx, []string{"warm up"}); err != nil {
		t.Fatalf("warm-up Embed: %v", err)
	}
	const budget = 150 * time.Millisecond
	var worst time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		if _, err := embedder.Embed(ctx, []string{"the thing that kept cycling in review"}); err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if elapsed := time.Since(start); elapsed > worst {
			worst = elapsed
		}
	}
	if worst > budget {
		t.Fatalf("warm single-text embed took %v, over the AC-3778-5 retrieval budget of %v", worst, budget)
	}
	t.Logf("warm single-text embed worst-of-five: %v (budget %v)", worst, budget)
}

func cosine(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
