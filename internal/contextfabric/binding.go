package contextfabric

import "time"

// ResolvedGraphBinding is the CHAOS-3898 §2.1 per-investigation graph
// binding (design brief v4.1 F1/P1b): the full derived graph key AND the
// epoch it names, resolved EXACTLY ONCE by GraphReader.ResolveInvestigationBinding
// at request start -- BEFORE tryReuse (Engine.Investigate resolves it
// immediately after the wire-time bounds check, before e.tryReuse is ever
// called: tryReuse's own SQL predicate, the ingress taint gate, and its own
// recheck graph calls all consume this SAME value).
//
// One investigation makes at least two independent graph calls
// (ResolveSubjects then DiscoverContext, and -- on the reuse-recheck path --
// two MORE), each of which used to re-resolve the organization's active
// epoch independently (falkorgraph.Adapter.resolveReadKey, called fresh at
// every call site). Once a build/flip can move the active epoch mid-request,
// that independent re-resolution is a real race: two graph reads for the
// SAME investigation could silently read two DIFFERENT graphs. Binding once
// and threading the identical value everywhere makes a flip mid-request
// produce an honestly one-epoch-stale but internally CONSISTENT result,
// never a result silently split across two graphs.
//
// GraphKey is the opaque, sha256-derived key string falkorgraph resolves it
// to (telemetry-legal as an opaque id, never interpreted by this package --
// only falkorgraph.Adapter knows how to turn an epoch into the actual key
// text). Engine never parses or compares it structurally; it exists on the
// binding purely so a caller who already has the binding never needs to
// re-derive the key through a second port call.
//
// Epoch is the organization's ACTIVE graph-lifecycle epoch
// (OrgGraphLifecycle.ActiveEpoch, contextfabric.OrgEpochResolver) at the
// moment of resolution -- NOT contextfabric.RebuildEpoch (the CHAOS-3782
// answer-reuse invalidation counter, a structurally separate dimension
// bumped by ReuseInvalidator.InvalidateOrganizationReuse). The two epochs
// answer different questions ("which build-aside-and-swap graph is
// currently served" versus "has this organization's projected state been
// invalidated for reuse purposes since this candidate was generated") and
// must never be conflated or derived from one another.
//
// The binding is threaded EXPLICITLY everywhere it is needed (never
// ctx-smuggled, matching every other load-bearing Engine value -- see
// SourceWatermarkSnapshot's doc comment for the same discipline applied to
// a sibling piece of snapshot-time state): to both GraphReader calls, to
// Save (whose stamped graph_epoch column can then never name an epoch other
// than the one the result's own graph reads actually used), to the
// AnswerReuseGate.FindReusable lookup (via ReuseKey.GraphEpoch), and to
// every reuse-authorization recheck (reuseAuthorizationStillHolds' own two
// graph calls) -- a caller that forgets to pass it fails to compile.
type ResolvedGraphBinding struct {
	GraphKey string
	Epoch    int64
}

// StoredInvestigationResult is the CHAOS-3898 §2.4 metadata-bearing carrier
// InvestigationResultStore.Get returns, closing the gap a bare
// InvestigationResult left: a receipt-loaded prior result exposed NO
// persistence metadata, so the §2.2 ingress taint gate had nothing to
// compare a current ResolvedGraphBinding against.
//
// Persistence metadata lives ON the carrier, never inside Result: the
// stored payload stays byte-immutable (Result is exactly what Save
// persisted and what a caller ultimately reads), and org-scoping /
// validate-on-read semantics of Get are unchanged. No consumer of Get may
// unwrap the carrier ahead of the taint gate -- compile-enforced by the
// type: a caller holding a StoredInvestigationResult must explicitly reach
// through .Result to get an InvestigationResult, so a call site cannot
// accidentally skip the comparison.
//
// GraphEpoch is nil for a pre-CHAOS-3898-S2 row (Save never stamped one) or
// for a row saved by a composition whose GraphReader never resolved a
// binding -- both read as "cannot prove this result's graph epoch",
// stripped by default at the taint gate exactly like an absent reuse
// stamp elsewhere in this package (fail-closed, never a silent pass).
type StoredInvestigationResult struct {
	Result     InvestigationResult
	GraphEpoch *int64
	SavedAt    time.Time
	// ParentResultID is the result THIS result followed -- durable chain
	// identity, recorded by every Save regardless of whether any axis was
	// carried or disclosed.
	//
	// It lives HERE, on the persistence carrier, rather than on
	// InvestigationResult itself, for the same reason GraphEpoch does: only
	// the SERVER walks this chain (the carry resolvers read it through an
	// org-scoped Get), so putting it on the result payload would widen the
	// wire contract for a value no client consumes -- and would fail every
	// consumer pinning the result schema with additionalProperties:false
	// until its pin was bumped. A carry's ORIGIN is disclosed separately and
	// already has a wire home: ContextFabricConfirmedStructureEntry.PriorResultID.
	//
	// Empty means "no recorded parent" -- a first turn, a pre-migration row,
	// or a store that does not persist ancestry. It never means "unknown,
	// assume something": the walk fails closed on an empty parent exactly as
	// it does on a missing reference.
	ParentResultID string
}

// ReuseMissReason classifies WHY AnswerReuseGate.FindReusable found no
// reusable candidate (design brief v4.1 F5). Before this, a stale-epoch row
// and the complete absence of any candidate both surfaced identically as
// "no row" -- FindReusable's one payload-bearing conjunctive SELECT could
// not tell them apart, so ReuseMissStaleGraphEpoch could never be emitted
// even though it is a materially different, and separately actionable,
// signal (a build/flip invalidated an otherwise-fresh candidate, versus no
// candidate ever existed for this question). A closed, content-safe
// vocabulary -- never free text -- mirroring AnswerReuseOutcome's own
// contract.
type ReuseMissReason string

const (
	// ReuseMissNoCandidate: no row matched every OTHER reuse dimension
	// either -- the ordinary, most common miss.
	ReuseMissNoCandidate ReuseMissReason = "miss_no_candidate"
	// ReuseMissStaleGraphEpoch: a row matched every other reuse dimension
	// (question, versions, watermarks, staleness window, rebuild epoch)
	// but was generated under a DIFFERENT graph_epoch than this
	// investigation's own ResolvedGraphBinding -- proof that a build/flip,
	// not ordinary staleness, is why this candidate does not apply.
	ReuseMissStaleGraphEpoch ReuseMissReason = "stale_graph_epoch"
)
