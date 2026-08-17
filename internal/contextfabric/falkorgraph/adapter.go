package falkorgraph

import (
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
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
	// The CHAOS-3836 task-prefix seam and the embed rune budget, captured
	// from EmbedderOptions at construction for the same reason
	// similarityFloor is: they are properties of the embedding MODEL's
	// configuration, and the a.embedder handle cannot be asked for them --
	// the hosted API wraps it (embedcache.Wrap, CHAOS-3841) in a cache that
	// forwards only Embed and Identity, so a duck-typed assertion on the
	// port would silently fall back to defaults exactly when the cache is
	// enabled. Nil funcs / zero values mean "no capability captured" and
	// behave as the unprefixed, default-budget deployment.
	applyDocumentPrefix func(string) string
	applyQueryPrefix    func(string) string
	prefixTagComponent  string
	embedTextRunes      int

	// overFetchMultiplier and efRuntime are the CHAOS-3834 per-identity
	// RetrievalPolicy values, captured the same way as similarityFloor:
	// zero means "no calibrated policy for this identity", which
	// vectorSearchNodesWithOverFetch and ensureVectorIndex already treat as
	// their respective pre-CHAOS-3834 defaults (multiplier 1, the server's
	// own efRuntime default). See EmbedderOptions' field docs and
	// RetrievalPolicy's doc comment (retrieval_policy.go) for why EfRuntime
	// specifically only ever affects a NEWLY CREATED index.
	overFetchMultiplier int
	efRuntime           int
	// vectorMarginCommitThreshold is CHAOS-3829's calibrated M, captured
	// the same way: zero means "no calibrated policy for this identity",
	// which graphrank.ResolveFromMergedCandidates' commit-path carve-out
	// already treats as "disabled" (see retrieval_policy.go's
	// RetrievalPolicy.VectorMarginCommitThreshold doc comment).
	vectorMarginCommitThreshold float64
	// calibratedTopK is CHAOS-3829 codex r5 K1's (accepted) companion to
	// vectorMarginCommitThreshold, captured the same way: zero means "no
	// calibrated policy for this identity", which graphrank's commit-path
	// carve-out already treats as "disabled" (see retrieval_policy.go's
	// RetrievalPolicy.CalibratedTopK doc comment).
	calibratedTopK int
	// commitGatePolicy is CHAOS-3857's sweep/measurement override for
	// graphrank.ResolveFromMergedCandidatesWithGate's three commit-gate
	// thresholds. The ZERO VALUE means "not overridden" -- reader.go's
	// ResolveDeps construction leaves it at zero for an unconfigured
	// deployment, and graphrank.ResolveSubjects (resolve.go) is the ONE
	// place a zero CommitGatePolicy gets replaced with
	// graphrank.DefaultCommitGatePolicy() -- never here, and never
	// silently treated as a valid (auto-commit-everything) policy. See
	// also CommitGatePolicy.Validate() (resolution.go): even a NON-zero
	// but invalid policy (a partial override with one field left at 0, for
	// instance) cannot reach a live commit decision -- Validate is
	// enforced independently at the env-var boundary
	// (EmbedderFromEnv, vector.go) AND inside the evaluator itself.
	commitGatePolicy graphrank.CommitGatePolicy

	bootstrapMu   sync.RWMutex
	bootstrapDone map[string]bool
}

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

	// The remaining fields are capabilities of the CONCRETE embedder,
	// captured by EmbedderFromEnv before any caller wraps Embedder (the
	// hosted API's read-path embedcache does exactly that, and the wrapper
	// deliberately implements only the two-method port). Capturing them here
	// is what lets the prefix seam and the composition tag survive wrapping;
	// asking the possibly-wrapped Embedder via type assertion would not.

	// MaxTextRunes is the per-text truncation budget the embedder was
	// configured with. Zero means "not captured": the adapter falls back to
	// duck-typing the Embedder, then to embedprovider.DefaultMaxTextRunes.
	MaxTextRunes int
	// ApplyDocumentPrefix / ApplyQueryPrefix are the CHAOS-3836 task-prefix
	// pair appliers (budget-enforcing, idempotent -- see embedprovider).
	// The write path runs ApplyDocumentPrefix over each composed subject
	// text immediately before Embed; the read path runs ApplyQueryPrefix
	// over the extracted query term immediately before Embed. Nil means no
	// prefixing, which is byte-identical to the "none" family.
	ApplyDocumentPrefix func(string) string
	ApplyQueryPrefix    func(string) string
	// PrefixTagComponent is embedprovider's composition-tag component for
	// the configured prefix family ("pnone", "pnomic"). Empty normalizes to
	// EmbedPrefixTagComponentNone inside EmbedCompositionTag.
	PrefixTagComponent string

	// OverFetchMultiplier and EfRuntime are the CHAOS-3834 per-identity
	// RetrievalPolicy values (retrieval_policy.go), resolved by
	// EmbedderFromEnv from LookupRetrievalPolicy against this deployment's
	// exact embed retrieval identity. Zero means "not calibrated": the
	// adapter falls back to multiplier 1 and the server's own efRuntime
	// default, respectively -- byte-identical to pre-CHAOS-3834 behavior.
	// See RetrievalPolicy's doc comment for why EfRuntime specifically only
	// ever governs a NEWLY CREATED vector index.
	OverFetchMultiplier int
	EfRuntime           int
	// VectorMarginCommitThreshold is CHAOS-3829's calibrated M, resolved
	// the SAME way as OverFetchMultiplier/EfRuntime. Zero means "not
	// calibrated": graphrank's commit-path carve-out stays disabled,
	// byte-identical to pre-CHAOS-3829 behavior.
	VectorMarginCommitThreshold float64
	// CalibratedTopK is CHAOS-3829 codex r5 K1's (accepted) companion to
	// VectorMarginCommitThreshold, resolved the SAME way and gated
	// together with it (EmbedderFromEnv installs both only when the
	// effective floor equals the calibrated tau). Zero means "not
	// calibrated": graphrank's commit-path carve-out stays disabled.
	CalibratedTopK int
	// CommitGatePolicy is CHAOS-3857's sweep/measurement override,
	// resolved by EmbedderFromEnv from three explicit env vars (see that
	// function's doc comment) using the SAME per-knob "explicit override
	// wins" precedent EnvSimilarityFloor already established. The zero
	// value means "no override": graphrank.ResolveSubjects falls back to
	// graphrank.DefaultCommitGatePolicy(), never to a zero-threshold
	// policy -- see ResolveDeps.CommitGatePolicy's doc comment. A
	// non-zero but INVALID override (see CommitGatePolicy.Validate())
	// is rejected here, loudly, by EmbedderFromEnv before it can ever
	// reach that fallback decision. Threaded through this
	// vector-retrieval-configuration seam
	// (rather than a separate, non-embedder-gated one) deliberately
	// mirrors VectorMarginCommitThreshold/CalibratedTopK's own scoping:
	// this is a MEASUREMENT surface for the SAME sweep, on the SAME seam,
	// not a claim that the commit gates are vector-retrieval-specific
	// (they are not -- they also govern lexical-only commits). A
	// deployment with no embedder configured gets no override either way
	// today; broadening the seam is future work if a non-vector sweep
	// ever needs it.
	CommitGatePolicy graphrank.CommitGatePolicy
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
	a.applyDocumentPrefix = options.ApplyDocumentPrefix
	a.applyQueryPrefix = options.ApplyQueryPrefix
	a.prefixTagComponent = options.PrefixTagComponent
	a.embedTextRunes = options.MaxTextRunes
	a.overFetchMultiplier = options.OverFetchMultiplier
	a.efRuntime = options.EfRuntime
	a.vectorMarginCommitThreshold = options.VectorMarginCommitThreshold
	a.calibratedTopK = options.CalibratedTopK
	a.commitGatePolicy = options.CommitGatePolicy
}

// documentPrefixed applies the captured document-side task prefix, or returns
// the text unchanged when none was captured. Applied to the text HANDED TO
// Embed only, never to the composed text stored as search_text -- the
// byte-identity guarantee (spec §0) covers the composed text both retrieval
// arms share; a prefix wraps only its transmission to the model.
func (a *Adapter) documentPrefixed(text string) string {
	if a.applyDocumentPrefix == nil {
		return text
	}
	return a.applyDocumentPrefix(text)
}

// queryPrefixed is documentPrefixed's read-path counterpart, applied to the
// extracted query term immediately before Embed.
func (a *Adapter) queryPrefixed(text string) string {
	if a.applyQueryPrefix == nil {
		return text
	}
	return a.applyQueryPrefix(text)
}

// embedPrefixTagComponent returns the captured prefix tag component, or the
// "none" component when nothing was captured -- the same normalization
// EmbedCompositionTag applies, surfaced here so callers comparing components
// directly see the canonical literal.
func (a *Adapter) embedPrefixTagComponent() string {
	if a.prefixTagComponent == "" {
		return EmbedPrefixTagComponentNone
	}
	return a.prefixTagComponent
}

// embedBudgetRunes returns the per-text embed budget: the captured
// configuration value when EmbedderFromEnv supplied one, else the concrete
// embedder's own answer (direct attachEmbedder construction in tests), else
// the package default. The captured value comes first because the hosted
// API's cache wrapper hides the concrete embedder from the duck-type probe.
func (a *Adapter) embedBudgetRunes() int {
	if a.embedTextRunes > 0 {
		return a.embedTextRunes
	}
	return embedMaxRunes(a.embedder)
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
