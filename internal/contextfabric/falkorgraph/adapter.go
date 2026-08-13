package falkorgraph

import (
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// Adapter implements contextfabric.ProjectionBackend and
// contextfabric.GraphReader on FalkorDB. See the package doc comment
// (config.go) for the client boundary this adapter maintains, and
// docs/design/context-fabric-falkordb-adapter.md / ADR 0009 for the
// tenancy, schema, and retrieval design this implements.
type Adapter struct {
	api    conn
	config Config
	now    func() time.Time

	// embedder is OPTIONAL (CHAOS-3778). A nil embedder means vector
	// retrieval is not configured for this deployment: the adapter runs the
	// lexical path exactly as it did before, creates no vector index, and
	// writes no embeddings. Nothing about the adapter fails closed over an
	// embedder a deployment did not choose.
	embedder contextfabric.Embedder
	// similarityFloor is tau -- the ABSOLUTE cosine similarity below which a
	// vector neighbor is dropped rather than scored (AC-3778-4). It is
	// captured from the embedder's configuration at construction because it
	// is a property of the embedding MODEL's similarity distribution, not of
	// the database.
	similarityFloor float64

	bootstrapMu   sync.RWMutex
	bootstrapDone map[string]bool
	// vectorFence caches the per-organization AC-3778-7 verdict: whether the
	// stored vector index and stored embedder identity match the currently
	// configured embedder. See ensureVectorReadable for the caching rules and
	// why a DISABLED verdict expires while an ENABLED one does not.
	vectorFence map[string]vectorFenceEntry
}

// vectorFenceEntry is one organization's cached fence verdict.
type vectorFenceEntry struct {
	enabled   bool
	decidedAt time.Time
}

// vectorFenceRecheckInterval bounds how long a DISABLED verdict is cached.
// An enabled verdict never expires (the configured embedder cannot change
// without a restart), but a disabled one must, or an operator who fixed the
// graph with `acr-projector rebuild --org` would also have to restart acr-api
// to get vector retrieval back -- turning a recoverable state into a deploy.
const vectorFenceRecheckInterval = 5 * time.Minute

// EmbedderOptions carries the optional vector-retrieval dependencies
// (CHAOS-3778). A zero value, or a nil Embedder, leaves vector retrieval off.
//
// SimilarityFloor must be in (0, 1); a value outside that range is replaced by
// embedprovider.DefaultSimilarityFloor rather than accepted, because a floor
// of 0 would silently disable the AC-3778-4 no-match guard -- the highest
// severity failure in this issue -- and that must not be reachable through a
// zero-valued struct field.
type EmbedderOptions struct {
	Embedder        contextfabric.Embedder
	SimilarityFloor float64
}

func New(config Config) (*Adapter, error) {
	client, err := newSDKAPI(config)
	if err != nil {
		return nil, err
	}
	return newWithAPI(config, client)
}

// NewWithEmbedder builds an adapter with vector retrieval enabled
// (CHAOS-3778). It is a separate constructor rather than a Config field so
// that a deployment without an embedder cannot accidentally half-configure
// one, and so Config stays a pure value type with no port in it.
func NewWithEmbedder(config Config, options EmbedderOptions) (*Adapter, error) {
	adapter, err := New(config)
	if err != nil {
		return nil, err
	}
	adapter.attachEmbedder(options)
	return adapter, nil
}

func (a *Adapter) attachEmbedder(options EmbedderOptions) {
	if options.Embedder == nil {
		return
	}
	floor := options.SimilarityFloor
	if floor <= 0 || floor >= 1 {
		floor = embedprovider.DefaultSimilarityFloor
	}
	a.embedder = options.Embedder
	a.similarityFloor = floor
}

func newWithAPI(config Config, client conn) (*Adapter, error) {
	if client == nil {
		return nil, errAdapterRequiresConn
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 3
	}
	if config.MaxResults == 0 {
		config.MaxResults = 25
	}
	if config.PoolSize == 0 {
		config.PoolSize = 10
	}
	if strings.TrimSpace(config.GraphPrefix) == "" {
		config.GraphPrefix = "acr-cf"
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Adapter{api: client, config: config, now: time.Now, bootstrapDone: make(map[string]bool)}, nil
}

var _ contextfabric.ProjectionBackend = (*Adapter)(nil)
var _ contextfabric.GraphReader = (*Adapter)(nil)
