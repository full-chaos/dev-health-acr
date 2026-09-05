// Package falkorgraph implements the Context Fabric graph backend
// (contextfabric.ProjectionBackend + contextfabric.GraphReader) on FalkorDB,
// a self-hosted, SSPL-licensed graph database consumed here as a deployment
// dependency (a container image), not as linked code -- the same posture as
// this repository's Postgres and ClickHouse dependencies. See ADR 0009 and
// docs/design/context-fabric-falkordb-adapter.md.
//
// Client boundary: github.com/FalkorDB/falkordb-go/v2 is used ONLY as a
// compact-protocol result decoder (sdkConn in client.go), never through its
// high-level Graph.Query/CallProcedure methods. That client has no
// context.Context support anywhere in its high-level API (every call goes
// through a package-level context.Background()), ToString panics on several
// common Go scalar types, CallProcedure silently returns empty results
// instead of an error when called with arguments, and GraphSchema has no
// mutex despite being mutated during result parsing -- all independently
// verified against the pinned v2.1.0. sdkConn calls db.Conn.Do(ctx, ...)
// directly (a real go-redis call that DOES accept a context) and only reuses
// the client's exported, I/O-free QueryResultNew decoder and
// BuildParamsHeader helper. Do not "simplify" this back to the client's
// high-level API -- that would reintroduce every defect above.
package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

const (
	SDKModule  = "github.com/FalkorDB/falkordb-go/v2"
	SDKVersion = "v2.1.0"
)

// CHAOS-4874: every sentinel below that HAS a backend-neutral meaning is
// declared WRAPPING the contextfabric sentinel that names it, so one error
// satisfies errors.Is against both this package's vendor-specific sentinel
// (which this package's own callers check) and the vendor-neutral one every
// CONSUMING seam classifies on.
//
// Why the wrap lives at the declaration and not at the wrap sites: the
// consuming seams -- projectionrun.classifyOutcomeError and the
// investigation route's writeContextFabricError -- classify ONLY on
// contextfabric.Err*/context.*, so before this change errors.Is(err,
// contextfabric.ErrRateLimited) was FALSE for a falkorgraph.ErrRateLimited
// and every FalkorDB write failure logged failure_class=unclassified while
// every read failure answered a generic 500. Attaching the class in
// classifyFalkorError's arms instead would have been erased on the second
// pass: safeDependencyError REBUILDS an already-classified error from the
// bare sentinel (fmt.Errorf("%s: %w", operation, sentinel)), which is a
// documented and deliberate part of its contract. Declaring the pairing
// once, on the sentinel itself, makes it survive every construction site --
// including a caller that returns a sentinel bare -- and makes it
// impossible to flatten.
//
// neutralClass (client.go) is the SPECIFICATION of these pairings and
// TestSentinelsCarryTheirDeclaredNeutralClass proves the declarations here
// agree with it. Same defect and same remedy as pgmodelreceipts.sanitizeError.
var (
	// ErrNotFound is deliberately NOT given a neutral class. A confirmed
	// ABSENCE is not a dependency failure, and it already has two
	// backend-neutral translations that say what it really means:
	// reader.go's graphNotProjectedError ->
	// contextfabric.ErrGraphNotProjected and projection.go's
	// notFoundWatermarkErr -> contextfabric.ErrProjectionWatermarkNotFound.
	// Wrapping contextfabric.ErrUnavailable here as well would make a
	// never-projected organization -- which Engine degrades to a clean
	// empty answer -- classify as a dependency outage and answer 503.
	// ErrGraphNotProjected's own doc comment (contextfabric/ports.go)
	// states the symmetric rule in the other direction: a rate limit, a
	// timeout or a real outage must NOT satisfy errors.Is(err,
	// ErrGraphNotProjected). Do not "fix" this to ErrUnavailable.
	ErrNotFound = errors.New("context fabric graph record not found")
	// ErrUnauthorized means ACR's OWN service credential was refused, so
	// its neutral class is ErrUnavailable, never anything the caller could
	// read as "you are unauthorized" -- internal/api/context_fabric_routes.go
	// makes exactly this argument at its ErrUnavailable branch.
	ErrUnauthorized = fmt.Errorf("%w: context fabric graph request unauthorized", contextfabric.ErrUnavailable)
	// ErrRateLimited carries contextfabric.ErrRateLimited so the route's
	// 429 branch and projectionrun's rate_limited class both fire.
	//
	// REPORTED LIMIT: NOTHING IN THIS PACKAGE PRODUCES IT at this commit.
	// classifyFalkorError has no rate-limit arm because no FalkorDB
	// rate-limit message has been verified live, and every other text in
	// that switch has been. This declaration makes the classification
	// correct if such an error is ever produced or passed in by a caller;
	// it does not by itself make a FalkorDB rate limit observable. Adding
	// the producing arm is a separate, evidence-bearing change.
	ErrRateLimited = fmt.Errorf("%w: context fabric graph request rate limited", contextfabric.ErrRateLimited)
	// ErrConstraintViolation classifies a FalkorDB unique-constraint
	// rejection (verified error text: "unique constraint violation on node
	// of type X"). FalkorDB's constraint violation messages carry no
	// property name or value, so the adapter must already know which
	// constraint it created to say anything more specific.
	//
	// Its neutral class is ErrInvalidResult, not ErrUnavailable: the store
	// REJECTED the write as contract-invalid, and ErrUnavailable is
	// documented (contextfabric/ports.go) as a RETRYABLE degraded outcome,
	// while retrying an identical constraint-violating write fails
	// identically forever. This is the same argument projectionrun already
	// made for ErrQueryBudgetExceeded sitting beside, not inside,
	// ErrUnavailable.
	ErrConstraintViolation = fmt.Errorf("%w: context fabric graph unique constraint violation", contextfabric.ErrInvalidResult)
	// errAlreadyExists classifies FalkorDB's "already indexed" /
	// "already exists" schema-object errors -- index and constraint
	// creation are NOT idempotent server-side (verified), so bootstrap
	// treats this as success for the concurrent-bootstrap race rather than
	// as a failure.
	// Neutral class ErrInvalidResult for the same reason as
	// ErrConstraintViolation: a schema-object state the server rejected,
	// not a retryable outage. Both this and errIndexNotFound are
	// success-treated by every caller in this package, so reaching a
	// classifier at all means a path nobody expected.
	errAlreadyExists = fmt.Errorf("%w: context fabric graph schema object already exists", contextfabric.ErrInvalidResult)
	// errIndexNotFound classifies FalkorDB's "no such index" rejection from a
	// DROP INDEX / DROP VECTOR INDEX against a property that was never
	// indexed (verified live: "Unable to drop index on :Subject(embedding):
	// no such index"). Symmetric with errAlreadyExists: dropping an
	// already-absent index is treated as success by callers (CHAOS-3832's
	// index recreate), the same idempotent posture createVectorIndex already
	// takes on an already-indexed property.
	errIndexNotFound = fmt.Errorf("%w: context fabric graph schema object does not exist", contextfabric.ErrInvalidResult)
	// Bootstrap could not complete: transient and retryable, so these two
	// are genuine ErrUnavailable.
	errConstraintBootstrapFailed   = fmt.Errorf("%w: context fabric graph constraint bootstrap failed", contextfabric.ErrUnavailable)
	errConstraintBootstrapTimedOut = fmt.Errorf("%w: context fabric graph constraint bootstrap timed out waiting for OPERATIONAL status", contextfabric.ErrUnavailable)
	errAdapterRequiresConn         = errors.New("falkordb graph connection is required")
)

// Config configures the FalkorDB adapter. Mirrors zepgraph.Config's shape
// and validation posture so the two backends are operationally
// interchangeable at the config layer.
type Config struct {
	Addr           string // host:port, e.g. "falkordb:6379"
	Password       string
	TLS            bool
	GraphPrefix    string
	RequestTimeout time.Duration
	MaxAttempts    uint
	// MaxResults (ACR_CONTEXT_FABRIC_FALKOR_MAX_RESULTS, default 25) is a
	// SEPARATE, deployment-level ceiling on top of a request's own
	// Options.MaxSubjectCandidates (CHAOS-4117 calibrated it to 20; see
	// internal/mcp.defaultMaxSubjectCandidates) -- resolve.go's
	// effectiveSearchLimit takes the LOWER of the two
	// (queries.go/vector.go clamp identically). A deployment that has set
	// this env var below 20 silently re-truncates every search even
	// though the request asked for the calibrated limit, undoing
	// CHAOS-4117 without touching a single line this ticket changed. No
	// deployment manifest in this repo sets it (checked at CHAOS-4117
	// ship time; the default of 25 is comfortably above 20), but this
	// field is exactly where that coupling needs to be visible to anyone
	// who later tunes it down.
	MaxResults    int
	PoolSize      int
	AllowInsecure bool // permit TLS=false outside development; see validate()
	// IncludeEmbedBodies is the CHAOS-3833 §3 body gate's EFFECTIVE value
	// (embedprovider.BodiesIncluded): whether free-text body heads (PR
	// body, incident description) join the ONE shared search-text
	// composition. It lives on the graph Config, not on the embedder,
	// because BOTH retrieval arms index the same composed text -- the
	// write path composes search_text with this value whether or not an
	// embedder is configured. It is SEMANTIC configuration: its value is
	// a component of the embed composition tag, so a flip moves the
	// stamped identity and forces the prescribed rebuild.
	IncludeEmbedBodies bool
	// Telemetry is optional (nil-safe), same contract as
	// zepgraph.GraphTelemetry.
	Telemetry GraphTelemetry
	// EmbedFailureMaxRetries bounds a short, in-process retry loop
	// embedProjectionBatch runs on a TRANSIENT embed failure
	// (embedprovider.IsTransientEmbedError) BEFORE clearing the batch's
	// vectors (CHAOS-4147 item 3 / CHAOS-4259). A PERSISTENT failure is
	// never retried -- see IsTransientEmbedError's doc comment for why an
	// identical retry cannot change that outcome. The ZERO VALUE means no
	// retry: a transient failure clears immediately, byte-identical to
	// pre-CHAOS-4259 behavior, so a Config built directly (most existing
	// tests, and any future in-process caller) is unaffected unless it
	// opts in. Production composition goes through ConfigFromEnv, which
	// applies DefaultEmbedFailureMaxRetries.
	EmbedFailureMaxRetries int
	// EmbedFailureRetryBackoff is the delay between each bounded retry
	// above. Only consulted when EmbedFailureMaxRetries > 0.
	EmbedFailureRetryBackoff time.Duration
	// EmbedFailureEscalateAfter is the CONSECUTIVE-BATCH-failure threshold
	// (post-retry, per organization) that promotes
	// GraphTelemetry.RecordVectorProjectionEmbedFailuresEscalated from
	// silent to firing -- see that method's doc comment for why a
	// sustained outage needs a signal louder than the routine per-batch
	// Warn RecordVectorProjection already emits. The ZERO VALUE disables
	// escalation entirely (matching pre-CHAOS-4259 behavior); production
	// composition goes through ConfigFromEnv, which applies
	// DefaultEmbedFailureEscalateAfter.
	EmbedFailureEscalateAfter int
	// ConfirmedKindVectorCensusMaxComparisons (CHAOS-4155 Phase 1,
	// ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS) is
	// the SOLE enable+budget knob for the shadow kind-scoped vector
	// completeness census (chaos4155_confirmed_kind_vector_census.go).
	// Zero (the default -- deliberately NOT applied by ConfigFromEnv the
	// way EmbedFailureMaxRetries above gets a nonzero default; this is a
	// brand-new, unvalidated-at-scale mechanism that must stay off until
	// explicitly turned on for a measurement run) disables the shadow arm
	// entirely: confirmedKindVectorCensus returns
	// ConfirmedKindVectorScopeNotAttempted immediately, zero queries, zero
	// cost, byte-identical to the arm not existing. A positive value caps
	// population*queryCount per resolution -- see that function's own doc
	// comment for why exceeding it is a correctness-safe refusal
	// (ConfirmedKindVectorScopeOverBudget), never a partial/sampled
	// attempt.
	ConfirmedKindVectorCensusMaxComparisons int64
	// RawSignalObserver (CHAOS-3858, measurement-only) is optional
	// (nil-safe), set directly by the composition root the same way
	// Telemetry is -- a Go interface value, never env-derived, and
	// independent of whether an embedder is configured (unlike
	// EmbedderOptions.CommitGatePolicy, this is not vector-retrieval-gated:
	// it reports the lexical raw signal too). See
	// graphrank.ResolveDeps.RawSignalObserver's doc comment for the full
	// scope. No production composition root sets this.
	RawSignalObserver graphrank.RawSignalObserver
	// IdentityUniverse (CHAOS-3884, Option C) is optional (nil-safe): the
	// composition root's closure over devhealthsource.IdentityUniverse and
	// its own ClickHouse client, bound to everything except orgID/ctx. A
	// nil value means this deployment has no complete-enumeration identity
	// reader wired -- ResolveSubjects' own composition leaves
	// graphrank.ResolveDeps.AliasLookup nil in that case, byte-identical to
	// every pre-CHAOS-3884 backend (same convention as RawSignalObserver
	// above: no production composition root REQUIRES this, but the
	// recommended one sets it). Kept as a plain closure rather than a
	// devhealthsource-typed dependency so this package never imports
	// devhealthsource (backend adapter subpackages own their own
	// vendor/SQL client directly; devhealthsource's ClickHouse client type
	// has no business appearing in a graph adapter's Config).
	IdentityUniverse func(ctx context.Context, orgID string) (rows []graphrank.IdentityRow, observedAt time.Time, complete bool, err error)
	// ResolutionTracer (CHAOS-3884, team-lead ruling 2026-08-17) is
	// optional (nil-safe), same convention as RawSignalObserver above --
	// set directly by the composition root, independent of whether an
	// embedder or IdentityUniverse is configured. See
	// graphrank.ResolveDeps.ResolutionTracer / graphrank.ResolutionTracer's
	// own doc comments for the event vocabulary and the corpus-safety
	// discipline every event field is held to.
	ResolutionTracer graphrank.ResolutionTracer
	// CensusFunc (CHAOS-3899; CONSUMED LIVE in the commit decision as of
	// CHAOS-3896 Slice C, design brief v6 §1.4) is optional (nil-safe),
	// same convention as RawSignalObserver/ResolutionTracer above: the
	// composition root's closure over devhealthsource.NewCensusFunc and its
	// own ClickHouse client, bound to everything except ctx. A nil value
	// means graphrank.ResolveDeps.CensusFunc stays nil too, so
	// RunShadowEvidenceRound is never even called -- byte-identical to
	// every pre-CHAOS-3899 backend. Kept as a plain closure rather than a
	// devhealthsource-typed dependency for the exact same reason
	// IdentityUniverse is (this package never imports devhealthsource).
	//
	// WIRED IN PRODUCTION (independent codex xhigh review finding: this doc
	// comment had drifted stale after Slice C's own open.go wiring landed,
	// fixed here): production's composition root
	// (internal/runtime/hosted/open.go's buildContextFabricGraphReader)
	// sets this, gated alongside IdentityUniverse -- see that call site's
	// own doc comment. The CHAOS-3884 replay/trial harness
	// (chaos3884_replay_harness_test.go) also wires this, independently,
	// through its own separate composition (buildReplayGraphReader).
	CensusFunc graphrank.CensusFunc
	// HandleGrammarChecker (CHAOS-3972 P3) is optional (nil-safe), same
	// convention as CensusFunc above: a nil value means an explicit
	// subject_handle can never become a receipt-bound offer (see that
	// type's own doc comment, internal/contextfabric/structure.go).
	// Threaded straight through to graphrank.ResolveDeps.HandleGrammarChecker.
	HandleGrammarChecker contextfabric.HandleGrammarChecker
	// EpochResolver (CHAOS-3898 S2a, design brief §3.1) is optional
	// (nil-safe), same convention as IdentityUniverse/CensusFunc above: the
	// composition root's pglifecycle.Resolver (or a bounded-lease
	// pglifecycle.CachedResolver wrapping one), bound to a live
	// GraphLifecycleStore. A nil value -- the default for every existing
	// production composition root today -- means every one of this
	// package's six graph-key call sites derives epoch 0's key exactly as
	// it always has: byte-identical to pre-CHAOS-3898 behavior. Wiring
	// this is what lets an organization actually serve from a non-zero
	// epoch; S2a ships the resolver and the call-site plumbing with this
	// field left unset everywhere in production, so no organization's
	// resolved key changes as a result of this slice landing.
	EpochResolver contextfabric.OrgEpochResolver
	// LifecycleTelemetry (CHAOS-3898 S2a, §5b) is optional (nil-safe): the
	// cf_resolved_graph_key / cf_graph_key_divergence signal sink. A nil
	// value discards both signals, the same "optional dependency, absent
	// means degrade" convention every sink on this Config already uses.
	LifecycleTelemetry contextfabric.GraphLifecycleTelemetry
	// AnchorMembershipOffersEnabled (CHAOS-4042, team-lead ruling: ship the
	// interim membership verifier DARK) is false by default -- the
	// composition root (internal/runtime/hosted/open.go) sets it from
	// config.Config.AnchorMembershipOffersEnabled
	// (ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_ENABLED), which itself
	// defaults false. Threaded straight through to
	// graphrank.ResolveDeps.AnchorMembershipOffersEnabled -- see that
	// field's own doc comment for what staying false guarantees.
	AnchorMembershipOffersEnabled bool
}

// GraphTelemetry is the graph adapter's operational signal sink.
//
// Codex round-3 F2: the vector signals are METHODS ON THIS INTERFACE, not an
// optional extension discovered by type assertion. The earlier design put them
// behind an optional `VectorTelemetry` interface so that "adding vector
// retrieval does not force every existing telemetry implementation to change"
// -- which sounded considerate and was the exact mechanism by which the
// signals ended up unwired: an optional extension that nothing implements is
// indistinguishable, at runtime, from no telemetry at all, while still reading
// like an instrumented system. Documentation then claimed operators could
// detect a re-embedding backlog through a signal that was never emitted, which
// is the measurement layer failing toward "fine".
//
// A required method cannot be silently skipped. NoopTelemetry exists for
// callers that genuinely want no signals, so declining is explicit.
type GraphTelemetry interface {
	RecordObservationTraversalDegraded(ctx context.Context, orgID string, count int)
	// RecordVectorRetrievalDegraded fires when a query could not run the
	// vector mechanism at all -- an embed failure or timeout, a wrong serving
	// model, or a fence mismatch.
	RecordVectorRetrievalDegraded(ctx context.Context, orgID string)
	// RecordVectorRetrievalSuppressed fires when the vector mechanism was
	// deliberately NOT run because the question is historical (CHAOS-3781).
	//
	// Deliberately separate from RecordVectorRetrievalDegraded rather than
	// reusing it with a reason string, because the existing signal has no
	// reason vocabulary and the two facts are operationally opposite: one
	// says the mechanism BROKE and someone should look at the embedder, the
	// other says it was correctly withheld because a k-NN index cannot
	// honour a validity window. Folding them together would make a healthy
	// system with historical traffic indistinguishable from an embedder
	// outage -- the measurement layer failing toward "something is wrong"
	// instead of toward "fine", which is no better.
	//
	// Both fire alongside an answer-level degraded flag, so answer and
	// telemetry never disagree about whether a mechanism was missing.
	RecordVectorRetrievalSuppressed(ctx context.Context, orgID string)
	// RecordVectorProjection reports one projection batch's vector outcome:
	// how many nodes were embedded and how many had a stale vector CLEARED.
	//
	// The cleared count is the signal that makes a mass clear visible. A
	// prolonged embedder outage shows up here as a sustained nonzero cleared
	// count long before anything else notices, and the running total against
	// the organization's node count is what tells an operator how much of the
	// corpus is currently vectorless.
	//
	// CHAOS-3835 round-2 finding 1: cleared counts ONLY genuine stale/error
	// clears -- a dimension mismatch, an embed failure, or a mid-batch write
	// failure. It deliberately EXCLUDES the routine clear every id-only-
	// skipped subject also receives (vector_projection.go's finding-1 fix):
	// that clear is a deterministic, mechanical consequence of the id-only
	// skip decision itself, not a symptom of anything breaking, and it is
	// already fully accounted for via skippedIDOnly. Folding it into cleared
	// would make the id-only population (a large, steady fraction of a live
	// ci_pipeline_run corpus) masquerade as a mass stale-vector event on
	// every batch that touched one.
	//
	// skippedKind and skippedIDOnly (spec §7 D2) count nodes the embed pass
	// DELIBERATELY left unembedded, BY REASON:
	//   - skippedKind (CHAOS-3833): the whole-kind skip-list (today, the
	//     organization node).
	//   - skippedIDOnly (CHAOS-3835): the per-row id-only skip (today,
	//     ci_pipeline_run rows whose name/branch carry no content beyond a
	//     bare identifier).
	//
	// Two counters, not one combined number: they are operationally
	// different facts (kind-wide vs a per-row content decision), and
	// collapsing them would make a rise in one indistinguishable from a
	// rise in the other -- the reasoning that already keeps
	// RecordVectorRetrievalDegraded and RecordVectorRetrievalSuppressed
	// separate. Without either, a skipped node is indistinguishable from a
	// healthy corpus: the read path reports degraded=false over a
	// partially embedded graph, so "N nodes deliberately unembedded, by
	// reason" must be a reported number, never an inference.
	RecordVectorProjection(ctx context.Context, orgID string, embedded, cleared, skippedKind, skippedIDOnly int)
	// RecordVectorProjectionEmbedFailuresEscalated (CHAOS-4147 item 3 /
	// CHAOS-4259) fires when a batch's embed-call failure -- after any
	// bounded transient retry embedProjectionBatch ran -- is the Nth
	// CONSECUTIVE batch failure for this organization (threshold:
	// Config.EmbedFailureEscalateAfter), so a SUSTAINED embed outage is a
	// loud, distinguishable signal rather than indistinguishable from the
	// routine per-batch Warn RecordVectorProjection already emits for one
	// bad batch. Fires on every failing batch from the threshold onward
	// (not just once at the crossing), so the signal stays live for the
	// duration of the outage; the caller resets the streak to 0 on the
	// next SUCCESSFUL batch, which is what stops this firing -- there is
	// no separate "recovered" signal. transient reports whether the
	// failure that crossed/held the threshold was itself classified
	// transient (embedprovider.IsTransientEmbedError) or persistent: the
	// operator response differs (check upstream connectivity/rate limits
	// vs. fix the credential or model configuration).
	RecordVectorProjectionEmbedFailuresEscalated(ctx context.Context, orgID string, consecutiveFailures int, transient bool)
	// RecordVectorIndexEfRuntimeMismatch fires when bootstrap discovers a
	// pre-existing OPERATIONAL vector index whose built efRuntime disagrees
	// with the calibrated CHAOS-3834 policy (round-8 P2's detection-only
	// Warn, round-9 P2 wiring fix). key is the graph key (graphKey's hashed
	// form), not an organization ID -- ensureVectorIndex runs at bootstrap,
	// before any per-request organization context reaches this deep, and
	// the hashed key is the only identifier available at that point (a
	// one-way hash of orgID, per graphKey's doc comment -- it cannot be
	// reversed back to orgID here). See ensureVectorIndex's doc comment for
	// the CHAOS-3832/3835 rebuild path this signal is asking an operator to
	// run, and why this is DETECTION only, never a compare-and-recreate.
	RecordVectorIndexEfRuntimeMismatch(ctx context.Context, key string, policyEfRuntime, indexEfRuntime int)
	// RecordIdentityGraphMissing (CHAOS-3884) fires when the Option-C
	// identity reader's complete source-table enumeration found a
	// repository/project/team/work_item claimant that is NOT (yet) present
	// in the graph -- projection lag. count is how many such claimants this
	// ONE resolution's AliasLookup call found; every one of them is
	// excluded from the identity fast path (folded into complete=false,
	// graphrank.ResolveDeps.AliasLookup's own doc comment) rather than
	// silently committed on unverified data. This signal is what makes
	// that exclusion LOUD rather than merely safe: a sustained nonzero
	// count is a standing silent-projection-loss detector, independent of
	// this ticket's own alias-identity concern.
	RecordIdentityGraphMissing(ctx context.Context, orgID string, count int)
	// RecordVectorFence (CHAOS-3890) fires on every AC-3778-7 read-fence
	// check this process makes (ensureVectorReadable, via
	// resolutionFence.readable), carrying the SPECIFIC reason the fence
	// already computes internally -- see VectorFenceResult's own doc
	// comment -- instead of collapsing it into RecordVectorRetrievalDegraded's
	// bare bool. That existing signal is untouched (still fires exactly as
	// before); this is an ADDITIONAL, more specific signal alongside it, so
	// a dashboard/alert built on the old one keeps working unchanged.
	//
	// memoized reports whether THIS call reused the one probe
	// resolutionFence caches for the lifetime of a single ResolveSubjects
	// call (that type's own doc comment) rather than issuing a fresh one --
	// so a reader can tell "the same verdict, reported again for a later
	// term in this resolution" apart from "a fresh probe just ran". Fires
	// even when result==VectorFenceOK, so an operator can see the fence
	// PASSING, not merely infer it from an absence of failure signals.
	RecordVectorFence(ctx context.Context, orgID string, result VectorFenceResult, memoized bool)
	// RecordLexiconExpansion (CHAOS-3890) fires once per
	// fulltextSearchNodesForResolution call (subject resolution's own
	// lexical arm), reporting whether the CHAOS-3838 domain-lexicon
	// expansion fired for this call's term, how many separate batches it
	// ran (lexiconExpansionBatches), how many genuinely new candidates it
	// contributed beyond the base query, and whether expansion itself was
	// the reason this call's own truncated signal flipped true.
	//
	// That last fact matters operationally: fulltextSearchNodesForResolution's
	// truncated return feeds ResolveFromMergedCandidates' searchTruncated,
	// which can block a downstream auto-commit -- previously this was
	// computed, used for exactly that gate, and then discarded, so an
	// unexpected non-commit traced back to expansion-caused truncation had
	// no operational signal to confirm it against.
	RecordLexiconExpansion(ctx context.Context, orgID string, fired bool, batchCount, addedCandidates int, truncatedByExpansion bool)
	// RecordSubjectCandidatesAuthzDropped (CHAOS-3888) reports how many
	// candidate nodes ONE ResolveSubjects call found and then excluded
	// specifically because AuthorizedAttributes denied them -- see
	// graphrank.ResolveDeps.SubjectCandidatesAuthzDropped's doc comment for
	// the exact scope (explicit-hint and hybrid-search candidates; never an
	// invalid-subject or internal-bookkeeping exclusion, neither an
	// authorization event). Before this signal, such a drop was
	// indistinguishable from a candidate that simply never existed --
	// nothing anywhere counted it.
	RecordSubjectCandidatesAuthzDropped(ctx context.Context, orgID string, count int)
	// RecordCohortMembersAuthzDropped (CHAOS-3888) reports how many
	// subjectless-cohort member nodes ONE DiscoverContext call excluded
	// specifically because AuthorizedAttributes denied them -- see
	// graphrank.DiscoveredCohort's doc comment.
	RecordCohortMembersAuthzDropped(ctx context.Context, orgID string, count int)
	// RecordEdgesFilteredByReason (CHAOS-3888) reports how many candidate
	// relationship edges ONE DiscoverContext call excluded, split by reason:
	// authz (AuthorizedAttributes denied the edge or one of its resolved
	// endpoints), temporalWindow (an endpoint did not exist inside the
	// requested historical window), and selfLoop (the edge's own two
	// endpoints resolved to the same subject). Before this signal all three
	// collapsed into the same unclassified `continue` -- a relationship
	// hidden by authorization was indistinguishable from one that never
	// existed at all.
	RecordEdgesFilteredByReason(ctx context.Context, orgID string, authz, temporalWindow, selfLoop int)
	// RecordCohortDeniedByAuthorization (CHAOS-4577) reports ONE DiscoverContext
	// call whose entire discovered cohort was dropped by AuthorizedAttributes --
	// distinct from RecordCohortMembersAuthzDropped's ordinary, expected
	// "nothing is wrong" narrowing posture. This fires only when authorization
	// denied EVERY candidate member (count is the same number
	// RecordCohortMembersAuthzDropped already saw for that call), which is the
	// shape a whole org's team_repo_ownership being empty produces: every Team
	// node carries the CHAOS-4390 fail-closed sentinel, no repository-scoped
	// principal can ever match it, and the terminal outcome is `no_match` --
	// indistinguishable, from the caller's side, from "there really are no
	// such teams" unless this signal exists.
	RecordCohortDeniedByAuthorization(ctx context.Context, orgID string, count int)
	// RecordCohortExactNameCensusGate (CHAOS-4622 remainder) reports ONE
	// DiscoverContext call's exact-name org-wide census admission decision
	// for a cohort-shaped (discovered_cohort/explicit_cohort) request --
	// see cohortExactNameCensusEligibility's own doc comment for the
	// closed CohortExactNameCensusBasis vocabulary this reports. Never
	// called for a non-cohort Shape (the gate is not applicable there).
	// Before this signal, an explicit_cohort request that should have
	// received the same census a discovered_cohort request gets had no
	// observable trace of WHY it didn't -- an operator saw only the
	// downstream garbled clarification, never the gate decision that
	// produced it.
	RecordCohortExactNameCensusGate(ctx context.Context, orgID string, admitted bool, basis CohortExactNameCensusBasis)
	// RecordCohortKindBasis (CHAOS-4736, seam 7) reports what decided the
	// discovered cohort's KIND for one DiscoverContext call, or what
	// prevented a cohort from being discovered at all.
	//
	// Emitted on EVERY call to DiscoveredCohort, admitted or not. Before
	// seam 7 the kind came from a substring match over model prose that
	// could not fail -- it always returned something -- so there was
	// nothing to report and no way to tell "no frame" from "no matching
	// nodes". Deleting the matcher makes the refusals real, and a real
	// refusal that is not counted is indistinguishable from an empty
	// graph. `discovered` is carried beside the basis so the two questions
	// ("what decided" and "did anything come back") are never conflated in
	// the reader.
	//
	// poolTruncation (CHAOS-5168) is the third such question -- "was the
	// candidate pool whole" -- carried beside the other two for the same
	// reason they are carried apart: a cohort under the member cap and a
	// cohort assembled from a clipped pool are the same document to a
	// reader, and before this the difference was diagnosable only by
	// re-reading source. See CohortPoolTruncationBasis' own doc comment for
	// the closed vocabulary and for why one of its members means "cut, and
	// it did not matter".
	// poolTruncationArms names WHICH retrieval arms were cut, beside the
	// decision -- see CohortPoolTruncationArm. Reported separately rather
	// than folded into the basis because arm COMBINATIONS grow 2^n and a
	// vocabulary that enumerates them is a cross-product, not a vocabulary.
	RecordCohortKindBasis(ctx context.Context, orgID string, declaredKind contextfabric.SubjectKind, basis graphrank.CohortKindBasis, discovered bool, poolTruncation CohortPoolTruncationBasis, poolTruncationArms []CohortPoolTruncationArm)
	// RecordNeighborLookupFailed reports ONE neighbour the hop walk reached
	// through an admitted edge and then could not read back.
	//
	// It exists because the count alone was not diagnosable: `failedLookups`
	// reaches the answer as `endpoint_lookup_failed:<n>`, which says how many
	// were lost and never which, so two different missing cohort members are
	// indistinguishable in a run's own artifacts. The neighbour's subject uuid
	// and the walk's origin are graph identifiers, never corpus content; the
	// error is logged for its message, which is the part a count throws away.
	RecordNeighborLookupFailed(ctx context.Context, orgID, originCanonicalID, neighborUUID string, err error)
}

// VectorFenceResult is CHAOS-3890's reason enum for the AC-3778-7 read
// fence's verdict (ensureVectorReadable/vectorFenceCheck). "ok" means the
// fence passed; every other value names EXACTLY one of the checks the fence
// already runs internally, in the order it runs them -- so a reader can
// tell "no index has been built yet" apart from "an index exists but its
// status/dimension could not be trusted" apart from "the embedder's
// dimension changed" apart from "this graph holds vectors from a different
// (or unverifiable) embedder identity" apart from "the fence's own probe
// query failed" -- five distinct causes RecordVectorRetrievalDegraded's
// bare bool had always flattened into one signal.
type VectorFenceResult string

const (
	// VectorFenceOK: the fence passed. Vector retrieval proceeds.
	VectorFenceOK VectorFenceResult = "ok"
	// VectorFenceIndexAbsent: no vector index on Subject.embedding exists
	// yet for this graph key (vectorIndexAbsent), or no embedder is
	// configured for this deployment at all.
	VectorFenceIndexAbsent VectorFenceResult = "index_absent"
	// VectorFenceIndexUnknown: an index exists but its OPERATIONAL status
	// or dimension could not be determined (vectorIndexUnknown) -- never
	// treated as a match, per vectorIndexDimension's own fail-closed rule.
	VectorFenceIndexUnknown VectorFenceResult = "index_unknown"
	// VectorFenceDimMismatch: the index reports a dimension different from
	// the currently configured embedder's -- e.g. a model swap that changed
	// output width without a rebuild.
	VectorFenceDimMismatch VectorFenceResult = "dim_mismatch"
	// VectorFenceIdentityMismatch: verifyStoredEmbedderIdentity found (or
	// could not rule out) a node whose stored embedder_identity disagrees
	// with the currently configured embedder -- a same-dimension model
	// swap, or a vector of unknown provenance (a NULL identity is treated
	// as a mismatch, never as verified).
	VectorFenceIdentityMismatch VectorFenceResult = "identity_mismatch"
	// VectorFenceQueryError: the fence's own dependency query (the index
	// inspection, or the stored-identity probe) failed -- an unverifiable
	// fence is not a passed fence, so this fails closed exactly like every
	// other non-ok result, but for a genuinely different, dependency-fault
	// reason worth telling apart from a configuration/projection-lag state.
	VectorFenceQueryError VectorFenceResult = "query_error"
)

// NoopTelemetry discards every signal. Callers that want no telemetry pass
// this explicitly rather than leaving Config.Telemetry nil, so "no signals" is
// a decision in the source rather than an omission.
type NoopTelemetry struct{}

func (NoopTelemetry) RecordObservationTraversalDegraded(context.Context, string, int)    {}
func (NoopTelemetry) RecordVectorRetrievalDegraded(context.Context, string)              {}
func (NoopTelemetry) RecordVectorRetrievalSuppressed(context.Context, string)            {}
func (NoopTelemetry) RecordVectorProjection(context.Context, string, int, int, int, int) {}
func (NoopTelemetry) RecordVectorProjectionEmbedFailuresEscalated(context.Context, string, int, bool) {
}
func (NoopTelemetry) RecordVectorIndexEfRuntimeMismatch(context.Context, string, int, int) {}
func (NoopTelemetry) RecordIdentityGraphMissing(context.Context, string, int)              {}
func (NoopTelemetry) RecordVectorFence(context.Context, string, VectorFenceResult, bool)   {}
func (NoopTelemetry) RecordLexiconExpansion(context.Context, string, bool, int, int, bool) {}
func (NoopTelemetry) RecordSubjectCandidatesAuthzDropped(context.Context, string, int)     {}
func (NoopTelemetry) RecordCohortMembersAuthzDropped(context.Context, string, int)         {}
func (NoopTelemetry) RecordEdgesFilteredByReason(context.Context, string, int, int, int)   {}
func (NoopTelemetry) RecordCohortDeniedByAuthorization(context.Context, string, int)       {}
func (NoopTelemetry) RecordCohortExactNameCensusGate(context.Context, string, bool, CohortExactNameCensusBasis) {
}
func (NoopTelemetry) RecordCohortKindBasis(context.Context, string, contextfabric.SubjectKind, graphrank.CohortKindBasis, bool, CohortPoolTruncationBasis, []CohortPoolTruncationArm) {
}

func (NoopTelemetry) RecordNeighborLookupFailed(context.Context, string, string, string, error) {}

// SlogTelemetry is the production GraphTelemetry: structured operational logs
// through log/slog, the repository's standard.
//
// It logs organization IDs (an internal identifier, not a credential, evidence
// body, or request payload) because "which organization" is the first question
// every one of these signals raises. It logs no text, no vectors, no model
// output, and no provider response content.
type SlogTelemetry struct{ Logger *slog.Logger }

func (t SlogTelemetry) logger() *slog.Logger {
	if t.Logger != nil {
		return t.Logger
	}
	return slog.Default()
}

func (t SlogTelemetry) RecordObservationTraversalDegraded(_ context.Context, orgID string, count int) {
	t.logger().Warn("context_fabric: observation traversal degraded", "org_id", orgID, "count", count)
}

func (t SlogTelemetry) RecordVectorRetrievalDegraded(_ context.Context, orgID string) {
	t.logger().Warn("context_fabric: vector retrieval unavailable for a request", "org_id", orgID)
}

// RecordVectorRetrievalSuppressed logs at INFO, not Warn: nothing is wrong.
// A historical question correctly declined a mechanism that cannot answer it,
// and paging on correct behaviour is how operators learn to ignore a signal.
func (t SlogTelemetry) RecordVectorRetrievalSuppressed(_ context.Context, orgID string) {
	t.logger().Info("context_fabric: vector retrieval suppressed for a historical question", "org_id", orgID)
}

// RecordVectorProjection logs at Warn when anything was cleared -- a cleared
// vector means a node just became invisible to vector search. Otherwise, a
// batch that deliberately SKIPPED any subject (skippedKind or skippedIDOnly
// nonzero) logs at Info: internal/contextfabric/AGENTS.md's "the skip is a
// REPORTED count ... never an inference" requires the count to actually
// reach an operator running at the default ACR_LOG_LEVEL=info, and Debug
// does not (round-1 finding: a nonzero skip count logged at Debug is
// invisible in production, indistinguishable from "nothing was skipped").
// A batch with nothing cleared and nothing skipped -- ordinary steady-state
// embedding -- stays at Debug so healthy operation does not generate noise.
//
// skippedKind and skippedIDOnly log under separate, closed-vocabulary keys
// (CHAOS-3835 §7 D2) rather than one combined field -- an operator grepping
// for a specific reason must not have to guess which one moved -- and,
// like every field here, carry no raw search text: only counts and the
// organization id.
func (t SlogTelemetry) RecordVectorProjection(_ context.Context, orgID string, embedded, cleared, skippedKind, skippedIDOnly int) {
	switch {
	case cleared > 0:
		t.logger().Warn("context_fabric: projection batch cleared stale vectors",
			"org_id", orgID, "embedded", embedded, "cleared", cleared,
			"skipped_kind", skippedKind, "skipped_id_only", skippedIDOnly)
	case skippedKind > 0 || skippedIDOnly > 0:
		t.logger().Info("context_fabric: projection batch skipped subjects",
			"org_id", orgID, "embedded", embedded, "cleared", cleared,
			"skipped_kind", skippedKind, "skipped_id_only", skippedIDOnly)
	default:
		t.logger().Debug("context_fabric: projection batch embedded nodes",
			"org_id", orgID, "embedded", embedded, "cleared", cleared,
			"skipped_kind", skippedKind, "skipped_id_only", skippedIDOnly)
	}
}

// RecordVectorProjectionEmbedFailuresEscalated logs at ERROR -- deliberately
// louder than RecordVectorProjection's Warn, which a busy rebuild already
// uses for every single cleared batch. A SUSTAINED run of failures is a
// materially different operational fact than one bad batch (CHAOS-4147 item
// 3), and Error is what makes the two distinguishable in a log stream or an
// alert rule scoped to level.
func (t SlogTelemetry) RecordVectorProjectionEmbedFailuresEscalated(_ context.Context, orgID string, consecutiveFailures int, transient bool) {
	t.logger().Error("context_fabric: sustained embed failures clearing vectors",
		"org_id", orgID, "consecutive_failures", consecutiveFailures, "transient", transient)
}

// RecordVectorIndexEfRuntimeMismatch logs through the CONFIGURED logger
// (t.Logger, falling back to slog.Default() only when unset -- t.logger()'s
// existing contract), not a bare slog.Default() call at the ensureVectorIndex
// call site (codex round-9 P2 wiring fix): the earlier direct call bypassed
// whatever sink/level an operator configured via Config.Telemetry, the same
// class of gap CHAOS-3835's telemetry fix closed elsewhere in this package.
func (t SlogTelemetry) RecordVectorIndexEfRuntimeMismatch(_ context.Context, key string, policyEfRuntime, indexEfRuntime int) {
	t.logger().Warn("context_fabric: existing vector index efRuntime does not match the calibrated retrieval policy -- run the CHAOS-3832/CHAOS-3835 index rebuild to apply it",
		"key", key, "policy_ef_runtime", policyEfRuntime, "index_ef_runtime", indexEfRuntime)
}

// RecordIdentityGraphMissing logs at Warn -- unlike RecordVectorRetrievalSuppressed's
// deliberate INFO ("nothing is wrong"), a graph-missing identity claimant
// means the source-of-truth tables and the graph have DIVERGED, which is
// always worth an operator's attention regardless of whether this specific
// resolution needed the identity fast path.
func (t SlogTelemetry) RecordIdentityGraphMissing(_ context.Context, orgID string, count int) {
	t.logger().Warn("context_fabric: identity-universe claimant absent from the graph (projection lag)", "org_id", orgID, "count", count)
}

// RecordVectorFence logs at Debug when the fence passes (VectorFenceOK) --
// passing is the expected, steady-state outcome and would be pure noise at
// any level an operator runs in production -- and at Warn for every other
// result: each is either a configuration/projection-lag condition
// (index_absent, index_unknown, dim_mismatch, identity_mismatch) or a
// dependency fault (query_error) worth an operator's attention without
// needing to raise ACR_LOG_LEVEL first.
//
// request_id is read via observability.RequestIDFromContext(ctx) rather
// than threaded as a new parameter -- ctx already carries it by the time a
// graph read reaches this sink (api/app.go's requestIDMiddleware attaches
// it at the HTTP boundary), and this is the one signal in this file that
// needs per-request correlation rather than only an org_id.
func (t SlogTelemetry) RecordVectorFence(ctx context.Context, orgID string, result VectorFenceResult, memoized bool) {
	requestID, _ := observability.RequestIDFromContext(ctx)
	if result == VectorFenceOK {
		t.logger().DebugContext(ctx, "context_fabric: vector read fence passed",
			"org_id", orgID, "request_id", requestID, "result", string(result), "memoized", memoized)
		return
	}
	t.logger().WarnContext(ctx, "context_fabric: vector read fence did not pass",
		"org_id", orgID, "request_id", requestID, "result", string(result), "memoized", memoized)
}

// RecordLexiconExpansion logs at Debug -- lexicon expansion firing is
// ordinary, expected retrieval behavior (CHAOS-3838), not itself a problem
// -- UNLESS it is the reason this call's own truncated signal flipped true,
// which can block a downstream auto-commit (ResolveFromMergedCandidates'
// searchTruncated gate): that case logs at Info so it reaches an operator's
// default level without needing to raise it to explain an unexpected
// non-commit.
func (t SlogTelemetry) RecordLexiconExpansion(ctx context.Context, orgID string, fired bool, batchCount, addedCandidates int, truncatedByExpansion bool) {
	requestID, _ := observability.RequestIDFromContext(ctx)
	if !fired {
		t.logger().DebugContext(ctx, "context_fabric: lexicon expansion did not fire",
			"org_id", orgID, "request_id", requestID)
		return
	}
	if truncatedByExpansion {
		t.logger().InfoContext(ctx, "context_fabric: lexicon expansion fired and truncated this call's results",
			"org_id", orgID, "request_id", requestID, "batch_count", batchCount, "added_candidates", addedCandidates)
		return
	}
	t.logger().DebugContext(ctx, "context_fabric: lexicon expansion fired",
		"org_id", orgID, "request_id", requestID, "batch_count", batchCount, "added_candidates", addedCandidates)
}

// RecordSubjectCandidatesAuthzDropped logs at Info, not Warn: an
// authorization-narrowed candidate pool is ordinary, expected behavior for a
// scoped principal, not a sign anything is broken -- the same "nothing is
// wrong" posture RecordVectorRetrievalSuppressed already takes for a
// deliberate, correct exclusion. It exists so a no_match/clarification
// outcome is diagnosable (was the pool ever narrowed by authorization?)
// without paging an operator over expected, per-request scoping.
func (t SlogTelemetry) RecordSubjectCandidatesAuthzDropped(_ context.Context, orgID string, count int) {
	t.logger().Info("context_fabric: subject candidates dropped by authorization", "org_id", orgID, "count", count)
}

// RecordCohortMembersAuthzDropped mirrors
// RecordSubjectCandidatesAuthzDropped's own Info-level, "nothing is wrong"
// posture -- see its doc comment.
func (t SlogTelemetry) RecordCohortMembersAuthzDropped(_ context.Context, orgID string, count int) {
	t.logger().Info("context_fabric: cohort members dropped by authorization", "org_id", orgID, "count", count)
}

// RecordEdgesFilteredByReason mirrors RecordSubjectCandidatesAuthzDropped's
// own Info-level, "nothing is wrong" posture -- an authorization exclusion,
// a historical-window exclusion, and a self-loop exclusion are all ordinary,
// expected outcomes of a correct read, not degradation.
func (t SlogTelemetry) RecordEdgesFilteredByReason(_ context.Context, orgID string, authz, temporalWindow, selfLoop int) {
	t.logger().Info("context_fabric: relationship edges filtered",
		"org_id", orgID, "authz", authz, "temporal_window", temporalWindow, "self_loop", selfLoop)
}

// RecordCohortDeniedByAuthorization logs at WARN, unlike every other
// authz-drop signal above: those narrow an otherwise-nonempty result, which
// is ordinary; this one means the call's ENTIRE cohort came back empty
// because of authorization, which is the CHAOS-4577 "answer indistinguishable
// from no such teams" failure mode -- an operator should be able to find this
// without already suspecting it.
func (t SlogTelemetry) RecordCohortDeniedByAuthorization(_ context.Context, orgID string, count int) {
	t.logger().Warn("context_fabric: entire discovered cohort denied by authorization",
		"org_id", orgID, "count", count)
}

// RecordCohortExactNameCensusGate logs at Info -- both outcomes (admitted
// and denied) are ordinary, expected decisions of a correct gate, not
// degradation; see cohortExactNameCensusEligibility's own doc comment for
// what basis means.
func (t SlogTelemetry) RecordCohortExactNameCensusGate(ctx context.Context, orgID string, admitted bool, basis CohortExactNameCensusBasis) {
	args := []any{"org_id", orgID, "admitted", admitted, "basis", string(basis)}
	t.logger().Info("context_fabric: cohort exact-name census gate", append(args, graphRequestIDLogAttrs(ctx)...)...)
}

// graphRequestIDLogAttrs attaches the investigation's request id to a graph
// adapter log line, the same way the engine's own telemetry does.
//
// ADDED AFTER IT COST REAL TIME. Every line this adapter emitted carried
// org_id and nothing else, so nothing from the graph layer could be tied to
// an investigation. Diagnosing one lost answer, I grepped these lines by
// request id, got nothing, and concluded the code path had not run -- when
// what I had actually proven was that the line carries no request id. A
// telemetry line that cannot be attributed cannot be used to prove a path
// did NOT execute, and a reader with a request id in hand should never have
// to know which layer emitted which shape.
func graphRequestIDLogAttrs(ctx context.Context) []any {
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		return []any{"request_id", requestID}
	}
	return nil
}

// RecordCohortKindBasis logs at Info: every outcome here is an ordinary,
// expected decision, including the refusals. A frame-absent turn is not a
// degradation of this component -- it is this component correctly declining
// to guess -- so it is reported at the same level as a successful discovery
// and is distinguished by its basis, never by its log level.
func (t SlogTelemetry) RecordCohortKindBasis(ctx context.Context, orgID string, declaredKind contextfabric.SubjectKind, basis graphrank.CohortKindBasis, discovered bool, poolTruncation CohortPoolTruncationBasis, poolTruncationArms []CohortPoolTruncationArm) {
	args := []any{"org_id", orgID, "member_kind", string(declaredKind), "basis", string(basis), "discovered", discovered,
		"pool_truncation", string(poolTruncation), "pool_truncation_arms", formatCohortPoolTruncationArms(poolTruncationArms)}
	t.logger().Info("context_fabric: cohort kind basis", append(args, graphRequestIDLogAttrs(ctx)...)...)
}

// RecordNeighborLookupFailed logs at Warn: unlike the cohort-kind basis, this
// IS a degradation -- a subject that should have been in the answer is not,
// and the operator needs it at the production level without raising verbosity.
func (t SlogTelemetry) RecordNeighborLookupFailed(ctx context.Context, orgID, originCanonicalID, neighborUUID string, err error) {
	args := []any{"org_id", orgID, "origin_canonical_id", originCanonicalID, "neighbor_uuid", neighborUUID}
	if err != nil {
		args = append(args, "error", err.Error())
	}
	t.logger().Warn("context_fabric: cohort neighbor lookup failed", append(args, graphRequestIDLogAttrs(ctx)...)...)
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("falkordb address is required")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return errors.New("falkordb address must be host:port")
	}
	if !c.TLS && !c.AllowInsecure {
		return errors.New("falkordb connection must use TLS")
	}
	// CHAOS-3809: AllowInsecure ONLY relaxes the check directly above -- it
	// does not, and never has, turned TLS off (newSDKAPI in client.go only
	// omits TLSConfig when c.TLS is itself false). TLS=true + AllowInsecure=
	// true previously passed validate() silently and read to an operator as
	// "TLS is on, but I've allowed an insecure connection" -- a reasonable
	// misreading that produced a client which TLS-handshakes a server the
	// operator just declared might be plaintext, hanging until
	// RequestTimeout with no diagnosable error. Reject the pair outright,
	// naming both variables so the fix is obvious: either turn TLS off
	// (EnvTLS=false) for a genuinely insecure connection, or drop
	// AllowInsecure and fix the server's TLS.
	if c.TLS && c.AllowInsecure {
		return fmt.Errorf("%s=true and %s=true are contradictory: %s=true requires TLS, so %s has no effect -- set %s=false for a genuinely insecure connection, or remove %s if TLS is actually required",
			EnvTLS, EnvAllowInsecure, EnvTLS, EnvAllowInsecure, EnvTLS, EnvAllowInsecure)
	}
	if c.RequestTimeout < time.Second || c.RequestTimeout > 2*time.Minute {
		return errors.New("falkordb request timeout must be between one second and two minutes")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 5 {
		return errors.New("falkordb max attempts must be between one and five")
	}
	if c.MaxResults < 1 || c.MaxResults > 50 {
		return errors.New("falkordb max results must be between one and fifty")
	}
	if strings.TrimSpace(c.GraphPrefix) == "" || len(c.GraphPrefix) > 32 {
		return errors.New("falkordb graph prefix is required and must be bounded")
	}
	if c.PoolSize < 1 || c.PoolSize > 100 {
		return errors.New("falkordb pool size must be between one and one hundred")
	}
	if c.EmbedFailureMaxRetries < 0 || c.EmbedFailureMaxRetries > 10 {
		return errors.New("embed failure max retries must be between zero and ten")
	}
	if c.EmbedFailureRetryBackoff < 0 || c.EmbedFailureRetryBackoff > 30*time.Second {
		return errors.New("embed failure retry backoff must be between zero and thirty seconds")
	}
	if c.EmbedFailureEscalateAfter < 0 || c.EmbedFailureEscalateAfter > 1000 {
		return errors.New("embed failure escalate-after threshold must be between zero and one thousand")
	}
	return nil
}

// Defaults for the CHAOS-4259 embed-failure retry/escalation knobs. ONLY
// ConfigFromEnv applies these; a Config built directly (the zero value)
// stays at "no retry, no escalation" -- see each field's own doc comment.
const (
	// DefaultEmbedFailureMaxRetries is small and bounded: a transient embed
	// failure (a network blip, a rate limit) either clears on its own
	// within a couple of attempts or it does not, and retrying more just
	// delays the batch's checkpoint advance for no better odds.
	DefaultEmbedFailureMaxRetries = 2
	// DefaultEmbedFailureRetryBackoff keeps the added worst-case latency
	// small (at most MaxRetries * this, ~400ms at the defaults) against a
	// BatchTimeout-class budget of seconds.
	DefaultEmbedFailureRetryBackoff = 200 * time.Millisecond
	// DefaultEmbedFailureEscalateAfter is five consecutive batch failures
	// -- enough that one bad tick (the routine case RecordVectorProjection
	// already reports at Warn) never escalates, but a real outage is
	// flagged loudly well before an operator would otherwise notice only
	// by scrolling Warn lines.
	DefaultEmbedFailureEscalateAfter = 5
)

// Environment variable names for ConfigFromEnv, matching the ACR_<COMPONENT>_
// naming and KEY/KEY_FILE secret convention used by internal/config, and the
// contract this repository's compose/Helm wiring targets (see the design
// doc's environment table).
const (
	EnvAddr           = "ACR_CONTEXT_FABRIC_FALKOR_ADDR"
	EnvPassword       = "ACR_CONTEXT_FABRIC_FALKOR_PASSWORD"
	EnvTLS            = "ACR_CONTEXT_FABRIC_FALKOR_TLS"
	EnvGraphPrefix    = "ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX"
	EnvRequestTimeout = "ACR_CONTEXT_FABRIC_FALKOR_REQUEST_TIMEOUT"
	EnvMaxAttempts    = "ACR_CONTEXT_FABRIC_FALKOR_MAX_ATTEMPTS"
	EnvMaxResults     = "ACR_CONTEXT_FABRIC_FALKOR_MAX_RESULTS"
	EnvPoolSize       = "ACR_CONTEXT_FABRIC_FALKOR_POOL_SIZE"
	EnvAllowInsecure  = "ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE"
	// EnvEmbedFailureMaxRetries / EnvEmbedFailureRetryBackoff /
	// EnvEmbedFailureEscalateAfter configure the CHAOS-4259 embed-failure
	// retry/escalation knobs -- see Config's own field doc comments.
	EnvEmbedFailureMaxRetries    = "ACR_CONTEXT_FABRIC_FALKOR_EMBED_FAILURE_MAX_RETRIES"
	EnvEmbedFailureRetryBackoff  = "ACR_CONTEXT_FABRIC_FALKOR_EMBED_FAILURE_RETRY_BACKOFF"
	EnvEmbedFailureEscalateAfter = "ACR_CONTEXT_FABRIC_FALKOR_EMBED_FAILURE_ESCALATE_AFTER"
	// EnvConfirmedKindVectorCensusMaxComparisons configures
	// Config.ConfirmedKindVectorCensusMaxComparisons -- see that field's
	// own doc comment. Deliberately NOT prefixed FALKOR_ like the knobs
	// above: this is a graphrank-visible ResolveDeps behavior, not an
	// adapter-internal connection/pooling setting.
	EnvConfirmedKindVectorCensusMaxComparisons = "ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS"
)

// Configured reports whether ACR_CONTEXT_FABRIC_FALKOR_ADDR is set at all, so
// a deployment that has not opted into Context Fabric never constructs the
// adapter and never fails closed over a dependency it did not choose.
// Mirrors zepgraph.Configured.
func Configured(lookup func(string) (string, bool)) bool {
	value, ok := lookup(EnvAddr)
	return ok && strings.TrimSpace(value) != ""
}

// ConfigFromEnv builds a Config from environment lookups, following the same
// KEY/KEY_FILE secret convention as every other ACR secret
// (internal/config.SecretValue). FalkorDB needs no external credential to
// deploy locally (no API key, unlike Zep) -- ACR_CONTEXT_FABRIC_FALKOR_PASSWORD
// is optional and empty by default, matching FalkorDB's own no-auth default.
func ConfigFromEnv(lookup func(string) (string, bool)) (Config, error) {
	password, err := acrconfig.SecretValue(lookup, EnvPassword)
	if err != nil {
		return Config{}, err
	}
	timeout, err := envDuration(lookup, EnvRequestTimeout, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxAttempts, err := envUint(lookup, EnvMaxAttempts, 3)
	if err != nil {
		return Config{}, err
	}
	maxResults, err := envInt(lookup, EnvMaxResults, 25)
	if err != nil {
		return Config{}, err
	}
	poolSize, err := envInt(lookup, EnvPoolSize, 10)
	if err != nil {
		return Config{}, err
	}
	// CHAOS-3833: the §3 body gate resolves from embedprovider's locality/
	// include-bodies variables here, in the ONE place both construction
	// sites (the hosted API's reader and acr-projector's backend) build
	// their Config, so the two processes cannot compose different text
	// from the same environment.
	includeBodies, err := embedprovider.BodiesIncluded(lookup)
	if err != nil {
		return Config{}, err
	}
	embedFailureMaxRetries, err := envInt(lookup, EnvEmbedFailureMaxRetries, DefaultEmbedFailureMaxRetries)
	if err != nil {
		return Config{}, err
	}
	embedFailureRetryBackoff, err := envDuration(lookup, EnvEmbedFailureRetryBackoff, DefaultEmbedFailureRetryBackoff)
	if err != nil {
		return Config{}, err
	}
	embedFailureEscalateAfter, err := envInt(lookup, EnvEmbedFailureEscalateAfter, DefaultEmbedFailureEscalateAfter)
	if err != nil {
		return Config{}, err
	}
	// CHAOS-4155 Phase 1: fallback 0, NEVER a nonzero default -- see
	// Config.ConfirmedKindVectorCensusMaxComparisons's own doc comment for
	// why this knob must stay off until an operator explicitly sets it,
	// unlike EmbedFailureMaxRetries/EmbedFailureEscalateAfter above.
	confirmedKindVectorCensusMaxComparisons, err := envInt64(lookup, EnvConfirmedKindVectorCensusMaxComparisons, 0)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Addr:                                    envString(lookup, EnvAddr, ""),
		Password:                                password,
		TLS:                                     envBool(lookup, EnvTLS, true),
		GraphPrefix:                             envString(lookup, EnvGraphPrefix, "acr-cf"),
		RequestTimeout:                          timeout,
		MaxAttempts:                             maxAttempts,
		MaxResults:                              maxResults,
		PoolSize:                                poolSize,
		AllowInsecure:                           envBool(lookup, EnvAllowInsecure, false),
		IncludeEmbedBodies:                      includeBodies,
		EmbedFailureMaxRetries:                  embedFailureMaxRetries,
		EmbedFailureRetryBackoff:                embedFailureRetryBackoff,
		EmbedFailureEscalateAfter:               embedFailureEscalateAfter,
		ConfirmedKindVectorCensusMaxComparisons: confirmedKindVectorCensusMaxComparisons,
	}, nil
}

func envString(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func envBool(lookup func(string) (string, bool), key string, fallback bool) bool {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envInt(lookup func(string) (string, bool), key string, fallback int) (int, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, errors.New(key + " must be an integer")
	}
	return parsed, nil
}

func envInt64(lookup func(string) (string, bool), key string, fallback int64) (int64, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, errors.New(key + " must be an integer")
	}
	return parsed, nil
}

// explicitEnvFloat reports (parsed, true, nil) only when key is set to a
// non-blank value that parses as a finite float; (0, false, nil) when key
// is genuinely absent or blank (not an override, not an error); and
// (0, false, err) when key IS set to something non-blank that fails to
// parse (or parses to NaN/±Inf).
//
// This mirrors EnvSimilarityFloor's OWN precedent EXACTLY (CHAOS-3857 sol
// review F2, correcting an earlier version of this comment that
// misstated it): embedprovider's envFloat treats unset/blank as "use the
// fallback" the same way this does, but once an operator has set a
// non-blank ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR, a garbage value
// does NOT silently fall back to the default -- embedprovider.
// ConfigFromEnv's own envFloat call returns a parse error that aborts
// EmbedderFromEnv's whole composition, loudly, at startup. A silent
// fallback on a garbage value would mask a real operator mistake (a typo
// in a sweep cell's env, say) as "no override" -- indistinguishable from
// a deliberately unset one. CHAOS-3857's four commit-gate env vars
// (EmbedderFromEnv, vector.go) get the identical treatment via this
// shared helper: blank/unset is silent and valid; set-but-unparseable is
// a loud composition-time error naming the offending var.
//
// Range/cross-field validation (e.g. CommitGatePolicy.Validate()) is
// deliberately NOT this function's job -- it only proves the raw string
// is a usable float, the same narrow scope embedprovider's envFloat has.
func explicitEnvFloat(lookup func(string) (string, bool), key string) (float64, bool, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false, fmt.Errorf("%s must be a finite number", key)
	}
	return parsed, true, nil
}

func envUint(lookup func(string) (string, bool), key string, fallback uint) (uint, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 8)
	if err != nil {
		return 0, errors.New(key + " must be a non-negative integer")
	}
	return uint(parsed), nil
}

func envDuration(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, errors.New(key + " must be a valid Go duration")
	}
	return parsed, nil
}
