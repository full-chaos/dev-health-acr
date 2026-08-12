package contextfabric

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	// ErrUnavailable identifies a bounded dependency failure that callers may
	// safely surface as a retryable degraded/unavailable outcome.
	ErrUnavailable = errors.New("context fabric dependency unavailable")
	// ErrInvalidResult identifies an implementation result that violated the
	// Context Fabric domain contract before it reached a consumer.
	ErrInvalidResult = errors.New("context fabric result is invalid")
	// ErrProjectionConflict identifies an out-of-order or incompatible
	// projection batch. The worker must not advance its checkpoint.
	ErrProjectionConflict = errors.New("context fabric projection conflict")
	// ErrRateLimited signals a bounded dependency (graph backend or
	// canonical fact source) rejected a call because a rate or quota limit
	// was exceeded. Adapters must wrap their own vendor-specific
	// rate-limit classification into this at their package boundary --
	// e.g. zepgraph.ErrRateLimited also wraps this -- so callers (Engine,
	// the route layer) can classify the failure without importing any
	// specific backend's package. This keeps ErrModelRateLimited (a
	// distinct, pre-existing sentinel for the model runtime specifically)
	// and this one both reachable from one vendor-neutral check.
	ErrRateLimited = errors.New("context fabric dependency rate limited")
	// ErrUnsupportedTimeAxis identifies a request that asked a
	// historical or point-in-time question the Context Fabric cannot
	// currently answer (CHAOS-3755 adversarial review finding H6).
	//
	// The v1 request contract accepts four temporal axes, but every
	// canonical fact source behind this engine reads CURRENT state only.
	// Answering a "what was the status last month" question with today's
	// data -- presented as if it were that answer -- is a false
	// historical answer, and the worst kind, because nothing in the
	// response marks it as wrong. So the engine refuses the request
	// outright rather than silently degrading it.
	//
	// This is a REQUEST-level refusal (the route maps it to 400), not a
	// dependency failure: the caller asked a well-formed question the
	// service does not support, and the honest answer is to say so. The
	// providers refuse the same thing independently at their own
	// boundary; this is the clean, early half of that pair.
	//
	// Deliberately not enforced in ContextFabricInvestigationRequest.
	// Validate(): the wire contract's accepted axes are unchanged, so
	// tightening it there would be a contract-level meaning change
	// requiring a new major version. What is unsupported is this
	// engine's ability to answer, which is a service concern.
	ErrUnsupportedTimeAxis = errors.New("context fabric time axis is not supported")
)

// Investigator is the consumer-neutral Context Fabric entry point. Ask Dev,
// the standalone Workbench, and MCP must all consume projections of this same
// result rather than implementing separate investigation pipelines.
type Investigator interface {
	Investigate(context.Context, storage.Principal, InvestigationRequest) (InvestigationResult, error)
}

// QuestionInterpreter interprets natural-language engineering questions. It
// may classify reusable analytical capabilities, but it must not require the
// question text to match a finite supported-question registry.
type QuestionInterpreter interface {
	Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error)
}

// GraphReader owns subject/cohort discovery and bounded relationship context.
// Implementations may use any selected graph/index backend behind this
// interface; consumers never receive graph-native queries or identifiers.
type GraphReader interface {
	ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion) (SubjectResolution, error)
	DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error)
}

type GraphDiscoveryRequest struct {
	Request        InvestigationRequest `json:"request"`
	Interpretation InterpretedQuestion  `json:"interpretation"`
	Resolution     SubjectResolution    `json:"resolution"`
}

// CanonicalFactReader is the typed, read-only boundary back to canonical Dev
// Health services. ACR chooses what facts it needs; this interface remains the
// authority for values and domain rules.
type CanonicalFactReader interface {
	ReadFacts(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error)
}

// AnswerSynthesizer combines graph context and canonical facts into an
// evidence-closed, answer-capable result. It may use deterministic rules and
// optional models, but it cannot introduce facts absent from the input.
type AnswerSynthesizer interface {
	Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error)
}

// InvestigationResultStore persists immutable result snapshots for prior-turn
// binding, replay, Workbench inspection, and future consumer projections.
//
// Binding precondition: Get MUST be scoped to principal.OrgID. It must
// never return a result belonging to a different organization, regardless
// of whether resultID happens to collide, is guessed, or is otherwise
// supplied by a caller outside that organization. ContextFabricInvestigationResult
// carries no organization discriminator of its own (by design -- it is a
// per-principal-scoped read, not a self-describing record), so a Get
// implementation that skips organization scoping is a full cross-tenant
// data leak with nothing downstream positioned to catch the wrong record
// by inspecting it.
//
// This is a binding precondition on implementations, not something Engine
// enforces or can enforce by inspecting the returned value. What Engine
// does provide is a second, independent layer specifically for the
// PriorSubjectReceipts consumer of Get (see resolvePriorSubjectHints):
// every subject a receipt resolves to is re-authorized through
// GraphReader's exact-hint path before it can become a candidate, and that
// re-authorization is unconditionally scoped to the *calling* principal's
// own organization graph (GraphReader.ResolveSubjects's node lookups are
// keyed by principal.OrgID, never by anything read back from the prior
// result). So even a Get implementation that violates this precondition
// and returns another organization's result cannot leak that organization's
// subject into the caller's investigation: GraphReader will look for it in
// the caller's own org graph, where it does not exist, and the receipt is
// silently skipped exactly like any other unresolvable one.
type InvestigationResultStore interface {
	Save(context.Context, storage.Principal, InvestigationResult) error
	Get(context.Context, storage.Principal, string) (InvestigationResult, error)
}

// ProjectionBackend is the write-side graph/index boundary. Applying a batch
// must be idempotent for the same batch ID and source version.
//
// Precondition: callers must serialize ApplyProjectionBatch calls per
// organization. The zepgraph implementation has no compare-and-swap (no
// attribute-level CAS) against concurrent writers touching the same
// subject: it reads a node's canonical attributes, merges in the new
// projection's attributes, and writes the merge back, with no protection
// against a second, concurrent projection interleaving between that read
// and write for the same org. Two sources projecting the same subject at
// the same time can lose one side's canonical metadata (aliases, provider
// IDs) to a stale merge that overwrites it. A CHAOS-3753 projection worker
// MUST serialize batches per organization (e.g. one in-flight
// ApplyProjectionBatch call per org at a time); it must not run multiple
// sources' batches for the same org concurrently. This is a caller
// obligation, not something ProjectionBackend enforces or can enforce
// without a real CAS primitive.
type ProjectionBackend interface {
	ApplyProjectionBatch(context.Context, ProjectionBatch) (ProjectionReceipt, error)
	ProjectionWatermark(context.Context, string, string) (ProjectionWatermark, error)
	PurgeOrganization(context.Context, string) error
}

// ProjectionSource reads canonical projection batches from a bounded adapter,
// such as a Dev Health outbox or typed canonical read service.
type ProjectionSource interface {
	NextProjectionBatch(context.Context, ProjectionCheckpoint) (ProjectionBatch, bool, error)
}

// ProjectionCheckpointStore advances only after the backend has durably
// accepted a batch. CompareAndSwapProjectionCheckpoint must return
// ErrProjectionConflict when expected no longer matches the durable cursor, so
// concurrent projectors cannot silently skip or reorder batches.
type ProjectionCheckpointStore interface {
	LoadProjectionCheckpoint(context.Context, string, string) (ProjectionCheckpoint, error)
	CompareAndSwapProjectionCheckpoint(context.Context, ProjectionCheckpoint, ProjectionCheckpoint) error
}
