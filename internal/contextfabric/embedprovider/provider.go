package embedprovider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// Embedder is the contextfabric.Embedder implementation over an
// OpenAI-compatible /v1/embeddings endpoint.
type Embedder struct {
	client   openai.Client
	config   Config
	identity contextfabric.EmbedderIdentity
}

var _ contextfabric.Embedder = (*Embedder)(nil)

// New builds an Embedder from a validated Config. It makes no network call --
// a deployment must be able to start without the embedder being reachable,
// because the read path degrades to lexical retrieval rather than failing.
func New(cfg Config) (*Embedder, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Embedder{
		client: openai.NewClient(newClientOptions(cfg)...),
		config: cfg,
		identity: contextfabric.EmbedderIdentity{
			Provider:  strings.TrimSpace(cfg.Provider),
			Model:     strings.TrimSpace(cfg.Model),
			Dimension: cfg.Dimension,
		},
	}, nil
}

// FromEnv builds an Embedder from the environment, returning ErrNotConfigured
// when vector retrieval is not enabled for this deployment.
func FromEnv(lookup func(string) (string, bool)) (*Embedder, error) {
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		return nil, err
	}
	return New(cfg)
}

func (e *Embedder) Identity() contextfabric.EmbedderIdentity { return e.identity }

// SimilarityFloor exposes the configured tau so the graph adapter can apply
// the AC-3778-4 no-match guard with the same value this embedder was tuned
// with. It lives on the embedder rather than on the graph adapter's own config
// deliberately: the floor is a property of the EMBEDDING MODEL's similarity
// distribution, so it must travel with the model, not with the database.
func (e *Embedder) SimilarityFloor() float64 { return e.config.SimilarityFloor }

// MaxTextRunes exposes the per-text truncation budget for callers preparing
// projection input.
func (e *Embedder) MaxTextRunes() int { return e.config.MaxTextRunes }

// MaxBatch exposes the per-request batch bound.
func (e *Embedder) MaxBatch() int { return e.config.MaxBatch }

// Embed returns one vector per input text, in input order.
//
// Batching: texts are split into chunks of at most Config.MaxBatch and issued
// sequentially, each under its own Config.Timeout. Sequential rather than
// concurrent is deliberate -- a local embedder is a single process with a
// single model loaded, so concurrent requests queue inside it anyway while
// multiplying the chance of tripping a per-request timeout.
//
// Order: the response is reordered by each Embedding's own Index field rather
// than trusting the order the server returned. An OpenAI-compatible server is
// free to return data in any order, and a mis-paired vector is not a degraded
// result -- it silently attaches one node's meaning to a different node's
// identity, which nothing downstream could ever detect. A response with a
// duplicated, missing, or out-of-range index is rejected outright
// (ErrResponseShape) rather than partially trusted.
//
// Dimension: every returned vector's width is checked against the configured
// dimension (ErrDimensionMismatch). The `dimensions` request parameter is
// deliberately NOT sent -- it is supported only by some OpenAI models and a
// BYO server may reject or ignore it, so this package states its expectation
// by VERIFYING the response rather than by asking the server to conform.
//
// Errors never carry a provider response body: the transport installs
// modelprovider.SanitizeProviderErrorBody, so a non-2xx body is replaced with
// a fixed, content-free shape before the SDK ever reads it.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.config.MaxBatch {
		end := start + e.config.MaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		chunk, err := e.embedChunk(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, chunk...)
	}
	return vectors, nil
}

func (e *Embedder) embedChunk(ctx context.Context, texts []string) ([][]float32, error) {
	inputs := make([]string, 0, len(texts))
	for _, text := range texts {
		inputs = append(inputs, TruncateRunes(text, e.config.MaxTextRunes))
	}
	callCtx, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()
	response, err := e.client.Embeddings.New(callCtx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: inputs},
		Model: openai.EmbeddingModel(e.config.Model),
	})
	if err != nil {
		// The SDK error's text is already provider-content-free thanks to the
		// sanitizer; it is still wrapped rather than returned raw so callers
		// classify on this package's own sentinel surface.
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	if response == nil || len(response.Data) != len(inputs) {
		return nil, fmt.Errorf("%w: expected %d vectors", ErrResponseShape, len(inputs))
	}
	ordered := make([][]float32, len(inputs))
	for _, item := range response.Data {
		index := int(item.Index)
		if index < 0 || index >= len(inputs) {
			return nil, fmt.Errorf("%w: vector index out of range", ErrResponseShape)
		}
		if ordered[index] != nil {
			return nil, fmt.Errorf("%w: duplicate vector index", ErrResponseShape)
		}
		if len(item.Embedding) != e.config.Dimension {
			return nil, fmt.Errorf("%w: expected %d values", ErrDimensionMismatch, e.config.Dimension)
		}
		vector := make([]float32, len(item.Embedding))
		for i, value := range item.Embedding {
			vector[i] = float32(value)
		}
		ordered[index] = vector
	}
	for _, vector := range ordered {
		if vector == nil {
			return nil, fmt.Errorf("%w: missing vector index", ErrResponseShape)
		}
	}
	return ordered, nil
}

// newClientOptions builds the OpenAI-compatible client options.
//
// Both the credential and the base URL are ALWAYS passed explicitly, for the
// same reason modelprovider.newClientOptions does so (see its doc comment):
// openai.NewClient seeds itself from the ambient process environment
// (OPENAI_API_KEY, OPENAI_BASE_URL, ...) and applies caller options
// afterwards, so passing both explicitly is what guarantees an ambient
// OPENAI_BASE_URL cannot redirect this service's embedding traffic and an
// ambient OPENAI_API_KEY is never sent to a loopback embedder. Do not
// "simplify" either option away on the grounds that the SDK has a default --
// the point is that the SDK's default is environment-derived.
func newClientOptions(cfg Config) []option.RequestOption {
	credential := option.WithAPIKey(cfg.APIKey)
	if strings.TrimSpace(cfg.APIKey) == "" {
		// A credential-free embedder should receive no Authorization header
		// at all, not an empty bearer -- and the header must be actively
		// removed, because the SDK will have populated it from an ambient
		// OPENAI_API_KEY.
		credential = option.WithHeaderDel("authorization")
	}
	return []option.RequestOption{
		credential,
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(cfg.MaxTransportRetries),
		option.WithMiddleware(modelprovider.SanitizeProviderErrorBody),
	}
}

// TruncateRunes bounds one text to at most limit runes, cutting on a rune
// boundary so a multi-byte character is never split into invalid UTF-8 (which
// some servers reject outright).
//
// Truncation is silent by design: the embedded text is a SEARCH SURFACE, not
// evidence, and a document whose first two thousand runes do not identify it
// is not one a longer prefix would rescue. Nothing about the node's stored
// content or its evidence references is affected.
func TruncateRunes(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// CosineFromDistance converts FalkorDB's vector index score into a cosine
// SIMILARITY in [0, 1].
//
// This is the D11-class hazard of CHAOS-3778, verified live against FalkorDB
// (graph module 42002) before any of this was written: the score yielded by
// db.idx.vector.queryNodes is a cosine DISTANCE, not a similarity. An
// identical vector scores 0; an unrelated one scored 0.699398, which is
// exactly 1 - cos for a cosine of 0.3007. The range is [0, 2].
//
// Handing that number to graphrank.ResultConfidence unchanged would take its
// `score >= 0 && score <= 1 -> return score` arm and award the BEST possible
// match a confidence of 0.0 while a poor match got 0.699 -- the same inversion
// D11 found on the lexical path, in a new place, and precisely the case
// graphrank/types.go's ResultConfidence doc comment warns a future vector
// backend about. Every vector score therefore passes through this function and
// lands in CandidateNode.Relevance; none is ever left in Score for
// ResultConfidence to interpret.
//
// The clamp to [0, 1] discards the negative-similarity half of the range
// (distance in (1, 2]). That half is genuinely meaningless here: it says a
// candidate points AWAY from the query, which is not "slightly relevant", and
// it is far below any usable similarity floor anyway.
//
// This function lives in embedprovider rather than in the graph adapter
// because the distance-versus-similarity convention belongs to the embedding
// domain, and because it must be unit-testable without a database.
func CosineFromDistance(distance float64) float64 {
	cosine := 1 - distance
	if cosine < 0 {
		return 0
	}
	if cosine > 1 {
		return 1
	}
	return cosine
}

// ProbeTimeout is a slightly more generous budget than a read-path call, for
// a deliberate one-off reachability check at startup or in a live test, where
// a cold model load (measured at 9.3 s against 10-17 ms warm) is expected
// rather than a failure.
const ProbeTimeout = 30 * time.Second
