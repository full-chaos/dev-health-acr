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
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

const (
	SDKModule  = "github.com/FalkorDB/falkordb-go/v2"
	SDKVersion = "v2.1.0"
)

var (
	ErrNotFound     = errors.New("context fabric graph record not found")
	ErrUnauthorized = errors.New("context fabric graph request unauthorized")
	ErrRateLimited  = errors.New("context fabric graph request rate limited")
	// ErrConstraintViolation classifies a FalkorDB unique-constraint
	// rejection (verified error text: "unique constraint violation on node
	// of type X"). FalkorDB's constraint violation messages carry no
	// property name or value, so the adapter must already know which
	// constraint it created to say anything more specific.
	ErrConstraintViolation = errors.New("context fabric graph unique constraint violation")
	// errAlreadyExists classifies FalkorDB's "already indexed" /
	// "already exists" schema-object errors -- index and constraint
	// creation are NOT idempotent server-side (verified), so bootstrap
	// treats this as success for the concurrent-bootstrap race rather than
	// as a failure.
	errAlreadyExists = errors.New("context fabric graph schema object already exists")
	// errIndexNotFound classifies FalkorDB's "no such index" rejection from a
	// DROP INDEX / DROP VECTOR INDEX against a property that was never
	// indexed (verified live: "Unable to drop index on :Subject(embedding):
	// no such index"). Symmetric with errAlreadyExists: dropping an
	// already-absent index is treated as success by callers (CHAOS-3832's
	// index recreate), the same idempotent posture createVectorIndex already
	// takes on an already-indexed property.
	errIndexNotFound               = errors.New("context fabric graph schema object does not exist")
	errConstraintBootstrapFailed   = errors.New("context fabric graph constraint bootstrap failed")
	errConstraintBootstrapTimedOut = errors.New("context fabric graph constraint bootstrap timed out waiting for OPERATIONAL status")
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
	MaxResults     int
	PoolSize       int
	AllowInsecure  bool // permit TLS=false outside development; see validate()
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
}

// NoopTelemetry discards every signal. Callers that want no telemetry pass
// this explicitly rather than leaving Config.Telemetry nil, so "no signals" is
// a decision in the source rather than an omission.
type NoopTelemetry struct{}

func (NoopTelemetry) RecordObservationTraversalDegraded(context.Context, string, int)      {}
func (NoopTelemetry) RecordVectorRetrievalDegraded(context.Context, string)                {}
func (NoopTelemetry) RecordVectorRetrievalSuppressed(context.Context, string)              {}
func (NoopTelemetry) RecordVectorProjection(context.Context, string, int, int, int, int)   {}
func (NoopTelemetry) RecordVectorIndexEfRuntimeMismatch(context.Context, string, int, int) {}

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
	return nil
}

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
	return Config{
		Addr:               envString(lookup, EnvAddr, ""),
		Password:           password,
		TLS:                envBool(lookup, EnvTLS, true),
		GraphPrefix:        envString(lookup, EnvGraphPrefix, "acr-cf"),
		RequestTimeout:     timeout,
		MaxAttempts:        maxAttempts,
		MaxResults:         maxResults,
		PoolSize:           poolSize,
		AllowInsecure:      envBool(lookup, EnvAllowInsecure, false),
		IncludeEmbedBodies: includeBodies,
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

// explicitEnvFloat reports (parsed, true) only when key is set to a
// non-blank, valid float; otherwise (0, false) -- for every other
// envFloat-family helper in this codebase, "unset/blank/unparseable" all
// collapse into "use the fallback", but CHAOS-3857's per-knob commit-gate
// overrides (EmbedderFromEnv, vector.go) need to tell "explicitly
// overridden" apart from "not set" for EACH of three independent knobs, the
// same "explicit override wins over the calibrated default" precedent
// EnvSimilarityFloor already established (a blank or unparseable value is
// deliberately NOT treated as an override -- it falls through to the
// calibrated default exactly like an absent var does, never to a zero
// threshold).
func explicitEnvFloat(lookup func(string) (string, bool), key string) (float64, bool) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
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
