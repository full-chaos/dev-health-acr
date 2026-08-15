package falkorgraph

// CHAOS-3833 wiring of the CHAOS-3836 task-prefix seam (embed-text spec §6
// T6). The tests here pin the three load-bearing properties of that wiring:
//
//  1. NEUTRALITY: with the default prefix family ("none", or no capability
//     captured at all) every text crosses to the embedder byte-identical to
//     the composed text, and the composition tag stays "pnone" -- upgrading
//     to this code changes nothing for an unconfigured deployment.
//  2. TRANSMISSION-ONLY: a configured family prefixes what Embed RECEIVES,
//     on both the write (document) and read (query) paths, while the
//     composed search_text corpus -- the byte-identity surface both
//     retrieval arms share (spec §0) -- is untouched. The family also moves
//     the composition tag, so the stamp/fence and the answer-reuse key both
//     flip with it.
//  3. WRAP-SURVIVAL: the capabilities ride EmbedderOptions, captured from
//     the concrete embedder by EmbedderFromEnv, because the hosted API wraps
//     the Embedder port in a read-path cache (embedcache, CHAOS-3841) that
//     implements only Embed and Identity. A duck-typed probe on the wrapped
//     port would silently fall back to defaults exactly when the cache is
//     enabled -- for the rune cap that would stamp/verify the WRONG tag.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// capturingEmbedder records every text handed to Embed, so a test can assert
// exactly what crossed the transmission boundary.
type capturingEmbedder struct {
	vector []float32
	texts  []string
}

func (c *capturingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.texts = append(c.texts, texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = c.vector
	}
	return out, nil
}

func (c *capturingEmbedder) Identity() contextfabric.EmbedderIdentity {
	return contextfabric.EmbedderIdentity{Provider: "stub", Model: "stub-embed", Dimension: len(c.vector)}
}

func prefixTestBatch(label string) contextfabric.ProjectionBatch {
	return contextfabric.ProjectionBatch{
		OrgID: "org",
		Entities: []contextfabric.EntityProjection{{
			Subject: contextfabric.SubjectRef{
				Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: label,
			},
			ObservedAt: time.Now().UTC(), SourceVersion: "v1",
		}},
	}
}

// prefixSeamAdapter builds an adapter over a permissive fake connection with
// the given capability options, returning the capturing embedder it embeds
// through.
func prefixSeamAdapter(t *testing.T, options EmbedderOptions) (*Adapter, *capturingEmbedder) {
	t.Helper()
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	embedder := &capturingEmbedder{vector: []float32{1, 0, 0, 0}}
	options.Embedder = embedder
	options.SimilarityFloor = 0.55
	adapter := newFakeAdapter(t, fake)
	adapter.attachEmbedder(options)
	return adapter, embedder
}

// With no captured capabilities (a zero EmbedderOptions beyond the port
// itself), both paths transmit the composed text verbatim and the stamp
// carries "pnone" -- byte-for-byte the pre-CHAOS-3836 behavior.
func TestDefaultPrefixFamilyIsBehaviorNeutral(t *testing.T) {
	t.Parallel()
	adapter, embedder := prefixSeamAdapter(t, EmbedderOptions{})

	batch := prefixTestBatch("Authentication Service")
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}
	targets, _ := collectEmbedTargets(batch, adapter.embedBudgetRunes(), adapter.config.IncludeEmbedBodies)
	if len(embedder.texts) != 1 || len(targets) != 1 {
		t.Fatalf("expected exactly one embedded text, got %d (targets %d)", len(embedder.texts), len(targets))
	}
	if embedder.texts[0] != targets[0].text {
		t.Fatalf("default family must transmit the composed text verbatim:\n got %q\nwant %q", embedder.texts[0], targets[0].text)
	}

	embedder.texts = nil
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, temporalFilter{}); err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if len(embedder.texts) != 1 || embedder.texts[0] != "auth" {
		t.Fatalf("default family must transmit the query term verbatim, got %v", embedder.texts)
	}

	wantSuffix := "#" + EmbedCompositionTag(embedprovider.DefaultMaxTextRunes, false, "")
	if got := adapter.stampedEmbedderIdentity(embedder.Identity()); !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("stamp %q must carry the pnone tag %q", got, wantSuffix)
	}
	if !strings.HasSuffix(wantSuffix, ":"+EmbedPrefixTagComponentNone) {
		t.Fatalf("the default tag %q must end in the canonical %q component", wantSuffix, EmbedPrefixTagComponentNone)
	}
}

// A configured family prefixes ONLY what Embed receives; the composed corpus
// (collectEmbedTargets' text, the byte-identity surface) is untouched, and
// the composition tag -- hence the stamp, the fence, and the reuse key --
// moves with the family.
func TestConfiguredPrefixFamilyWrapsTransmissionOnly(t *testing.T) {
	t.Parallel()
	sourceEnv := map[string]string{
		embedprovider.EnvBaseURL: "https://embed.example/v1", embedprovider.EnvProvider: "nomic",
		embedprovider.EnvModel: "nomic-embed-text", embedprovider.EnvDimension: "768",
		embedprovider.EnvPrefixFamily: "nomic",
	}
	cfg, err := embedprovider.ConfigFromEnv(func(key string) (string, bool) { value, ok := sourceEnv[key]; return value, ok })
	if err != nil {
		t.Fatalf("embedprovider.ConfigFromEnv: %v", err)
	}
	source, err := embedprovider.New(cfg)
	if err != nil {
		t.Fatalf("embedprovider.New: %v", err)
	}
	adapter, embedder := prefixSeamAdapter(t, EmbedderOptions{
		MaxTextRunes:        source.MaxTextRunes(),
		ApplyDocumentPrefix: source.ApplyDocumentPrefix,
		ApplyQueryPrefix:    source.ApplyQueryPrefix,
		PrefixTagComponent:  source.PrefixTagComponent(),
	})

	batch := prefixTestBatch("Authentication Service")
	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}
	targets, _ := collectEmbedTargets(batch, adapter.embedBudgetRunes(), adapter.config.IncludeEmbedBodies)
	if len(embedder.texts) != 1 || len(targets) != 1 {
		t.Fatalf("expected exactly one embedded text, got %d (targets %d)", len(embedder.texts), len(targets))
	}
	if want := source.DocumentPrefix() + targets[0].text; embedder.texts[0] != want {
		t.Fatalf("write path must transmit the document-prefixed text:\n got %q\nwant %q", embedder.texts[0], want)
	}
	if strings.HasPrefix(targets[0].text, source.DocumentPrefix()) {
		t.Fatalf("the composed corpus must stay unprefixed, got %q", targets[0].text)
	}

	embedder.texts = nil
	if _, _, _, err := adapter.hybridSearchNodes(context.Background(), "k", "org", "auth", 5, &resolutionFence{}, temporalFilter{}); err != nil {
		t.Fatalf("hybridSearchNodes: %v", err)
	}
	if want := source.QueryPrefix() + "auth"; len(embedder.texts) != 1 || embedder.texts[0] != want {
		t.Fatalf("read path must transmit the query-prefixed term %q, got %v", want, embedder.texts)
	}

	stamp := adapter.stampedEmbedderIdentity(embedder.Identity())
	if !strings.HasSuffix(stamp, ":pnomic") {
		t.Fatalf("stamp %q must carry the configured family's tag component", stamp)
	}
	neutral, _ := prefixSeamAdapter(t, EmbedderOptions{MaxTextRunes: source.MaxTextRunes()})
	if neutral.stampedEmbedderIdentity(embedder.Identity()) == stamp {
		t.Fatal("a family change must move the stamped identity, or the fence never fails closed over it")
	}
}

// The capabilities must survive an Embedder wrapper that implements only the
// two-method port -- the hosted API's embedcache does exactly that on the
// read path. A duck-typed fallback would compute the DEFAULT rune cap and
// the "pnone" component behind such a wrapper, stamping and verifying a tag
// the projector (unwrapped) never wrote.
func TestCapturedCapabilitiesSurviveAnEmbedderWrapper(t *testing.T) {
	t.Parallel()
	inner := &capturingEmbedder{vector: []float32{1, 0, 0, 0}}
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.attachEmbedder(EmbedderOptions{
		Embedder:           portOnlyWrapper{inner: inner},
		SimilarityFloor:    0.55,
		MaxTextRunes:       2222,
		PrefixTagComponent: "pnomic",
	})
	want := "#" + EmbedCompositionTag(2222, false, "pnomic")
	if got := adapter.stampedEmbedderIdentity(inner.Identity()); !strings.HasSuffix(got, want) {
		t.Fatalf("stamp %q must use the CAPTURED cap and component (%q), not a duck-typed fallback", got, want)
	}
}

// portOnlyWrapper forwards Embed and Identity and nothing else, mirroring
// embedcache.Cache's deliberate surface.
type portOnlyWrapper struct{ inner contextfabric.Embedder }

func (w portOnlyWrapper) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return w.inner.Embed(ctx, texts)
}
func (w portOnlyWrapper) Identity() contextfabric.EmbedderIdentity { return w.inner.Identity() }

// EmbedderFromEnv is the single point that captures the concrete embedder's
// capabilities; this pins that every field actually crosses, with the real
// embedprovider behind it.
func TestEmbedderFromEnvCapturesTheConcreteCapabilities(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		embedprovider.EnvBaseURL: "https://embed.example/v1/", embedprovider.EnvProvider: "nomic",
		embedprovider.EnvModel: "nomic-embed-text", embedprovider.EnvDimension: "768",
		embedprovider.EnvMaxTextRunes: "2345", embedprovider.EnvPrefixFamily: "nomic",
	}
	options, err := EmbedderFromEnv(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	if err != nil {
		t.Fatalf("EmbedderFromEnv: %v", err)
	}
	if options.MaxTextRunes != 2345 {
		t.Fatalf("MaxTextRunes = %d, want the configured 2345", options.MaxTextRunes)
	}
	if options.PrefixTagComponent != "pnomic" {
		t.Fatalf("PrefixTagComponent = %q, want pnomic", options.PrefixTagComponent)
	}
	if got := options.ApplyQueryPrefix("auth"); got != "search_query: auth" {
		t.Fatalf("ApplyQueryPrefix = %q, want the nomic query prefix applied", got)
	}
	once := options.ApplyDocumentPrefix("auth")
	if once != "search_document: auth" {
		t.Fatalf("ApplyDocumentPrefix = %q, want the nomic document prefix applied", once)
	}
	if twice := options.ApplyDocumentPrefix(once); twice != once {
		t.Fatalf("ApplyDocumentPrefix must be idempotent, got %q then %q", once, twice)
	}
}

// The persisted answer-reuse dimension moves with the prefix family through
// the same tag authority as the node stamp (spec §4: one discriminator,
// three layers).
func TestEmbedRetrievalIdentityTracksThePrefixFamily(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		embedprovider.EnvBaseURL: "https://embed.example/v1/", embedprovider.EnvProvider: "openai",
		embedprovider.EnvModel: "text-embedding-3-large", embedprovider.EnvDimension: "3072",
	}
	env := func(values map[string]string) func(string) (string, bool) {
		return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	}
	plain, err := EmbedRetrievalIdentityFromEnv(env(base))
	if err != nil {
		t.Fatalf("EmbedRetrievalIdentityFromEnv: %v", err)
	}
	withFamily := map[string]string{embedprovider.EnvPrefixFamily: "nomic"}
	for key, value := range base {
		withFamily[key] = value
	}
	prefixed, err := EmbedRetrievalIdentityFromEnv(env(withFamily))
	if err != nil {
		t.Fatalf("EmbedRetrievalIdentityFromEnv(nomic): %v", err)
	}
	if !strings.HasSuffix(prefixed, ":pnomic") || prefixed == plain {
		t.Fatalf("a prefix-family change must move the reuse identity: %q vs %q", plain, prefixed)
	}
	invalid := map[string]string{embedprovider.EnvPrefixFamily: "e6"}
	for key, value := range base {
		invalid[key] = value
	}
	if _, err := EmbedRetrievalIdentityFromEnv(env(invalid)); err == nil {
		t.Fatal("an unrecognized prefix family must be a configuration error, never a silent pnone")
	}
}
