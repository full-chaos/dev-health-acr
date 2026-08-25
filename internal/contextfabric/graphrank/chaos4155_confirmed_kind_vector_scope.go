package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-4155 Phase 1: kind-scoped vector completeness census, SHADOW ONLY.
//
// BACKGROUND: chaos4154_confirmed_kind_scope.go's buildConfirmedKindScopedSnapshot
// proves a confirmed-kind candidate population complete via an exhaustive
// per-term SearchKind (lexical) pass, but ONLY when
// deps.VectorMechanismConfigured==false -- on any deployment with a live
// vector mechanism (our own trial org included), that lexical proof is
// necessarily incomplete (a vector-surfaced rival could exist with zero
// literal token overlap on any field), so the function returns
// confirmedKindScopePlanIncomplete and the confirmed-kind-scoped commit gate
// re-evaluation never runs. CHAOS-4155's ticket asked for a genuine
// vector-channel completeness proof to close that gap.
//
// DESIGN (sol-max consult, effort=max, 2026-08-25, "(a) CALIBRATION-COMPLIANT
// DESIGN" verdict, ratified by team-lead): NOT a calibrated ANN over-fetch
// arm -- sol was explicit (consult Q3) that "N_kind <= k" alone cannot prove
// completeness against FalkorDB's k-NN index (the org/kind predicate is a
// POST-filter after queryNodes returns -- vector.go's own "THE ORG PREDICATE
// IS A POST-FILTER" doc comment -- so other kinds can consume the top-k
// window before a confirmed-kind row is even seen, and the deployed HNSW has
// measured imperfect recall regardless). Instead: an EXACT, stable-snapshot,
// kind-scoped vector CENSUS -- exhaustively enumerate every embedded Subject
// node of the confirmed kind under the org + current embedder identity via a
// paginated MATCH scan (reusing oracle.go's CHAOS-3831/CHAOS-3849 pagination
// + independent count(n) closure pattern, hardened per sol's note: a
// malformed row here is a FAILURE, never a skip-and-reconcile like the
// measurement-only oracle takes), score every enumerated row by TRUE COSINE
// against each of this resolution's own query terms, and count the result.
// Cardinality closure over real enumeration -- not "count implies ANN
// recall" -- is what makes this a sound completeness proof rather than
// another blind calibration guess.
//
// STABLE-SNAPSHOT CHECK (team-lead ruling, 2026-08-25, superseding sol's
// "acquire the projector's org lock" suggestion): a READ path must never
// acquire projectionrun's coordinator lock -- a reader holding it is exactly
// how CHAOS-3882's projector-starvation/divergence-recovery incident class
// happens. Uses a lock-free CHECK instead: falkorgraph's own _AcrWatermark
// nodes (identity.go's labelWatermark, written by every ApplyProjectionBatch
// -- projection.go's writeWatermark) are read, scoped to the SAME GraphKey
// this whole resolution is already reading from (never re-resolved), once
// before the census and once after count-closure. Any source's watermark
// value changing, or a source appearing/disappearing between the two reads,
// means a projection write landed mid-census -- reported as
// ConfirmedKindVectorScopeDrift, fail-closed, never Complete. If instead the
// GraphKey itself is a RETIRING (grace) epoch from a mid-census rebuild
// swap, that key's own watermark set is frozen (the coordinator only ever
// writes to the ACTIVE epoch), so the same before/after comparison correctly
// reads stable -- no separate epoch check is needed. This needs no
// coordinator-package change and reuses only already-public read primitives.
//
// SCOPE REDUCTION (Phase 1, disclosed): sol's provable claim requires
// covering "every interpreted term plus the bounded full-question query"
// for Complete to be a TRUE completeness proof. This Phase only queries the
// per-term channel (the SAME terms chaos4154's own exhaustive SearchKind
// pass already exhausts) -- the question-channel query is deferred to
// whichever change activates the gate (Phase 2), since NOTHING in Phase 1
// reads ConfirmedKindVectorScopeState for a decision. Complete here is
// therefore a PROVISIONAL label (telemetry-grade, not gate-grade) until that
// gap closes.
//
// PHASE 1 / PHASE 2 SPLIT (sol's own split, team-lead: "GO exactly as sol
// split it"): Phase 1 (THIS CHANGE) is instrumentation-only -- the shadow
// census runs, records ConfirmedKindVectorScope* telemetry, and NEVER
// changes buildConfirmedKindScopedSnapshot's own returned scopeState/pool:
// every vector-enabled row still reads confirmedKindScopeState=
// "plan_incomplete" and the commit gate's decision is BYTE-IDENTICAL to
// pre-CHAOS-4155 behavior -- see
// TestBuildConfirmedKindScopedSnapshot_VectorCensusNeverChangesReturnedScopeState.
// Phase 2 (a LATER, SEPARATE change, after lane-baseline's kiac
// closure/cost measurement selects a real
// ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS budget and
// adds the question-channel query) is what would let
// ConfirmedKindVectorScopeState=="complete" actually flip scopeState to
// confirmedKindScopeComplete and let the isolated snapshot merge in the
// census's own vector-corroborated candidates.
//
// DO-NOT-BUILD (carried over from chaos4154_confirmed_kind_scope.go's own
// list, still binding here):
//   - Do not mutate or reinterpret the resolution-wide searchTruncated bit.
//   - Do not infer completeness from non-emptiness, counts, observed
//     truncation rates, limit+1, or top-result stability -- the count-
//     closure check here is enumeration-verified cardinality, never a bare
//     count comparison against k.
//   - Do not let this shadow arm's outcome influence scopeState, the
//     isolated pool, or any gate in THIS change.

// ConfirmedKindVectorScope* is the closed vocabulary
// ResolutionTraceEvent.ConfirmedKindVectorScopeState carries.
const (
	// ConfirmedKindVectorScopeNotAttempted: deps.ConfirmedKindVectorCensus
	// is nil (the deployment default), the term list was empty, or this
	// resolution never reached the one branch
	// (buildConfirmedKindScopedSnapshot's confirmedKindScopePlanIncomplete
	// case) that invokes this shadow arm at all.
	ConfirmedKindVectorScopeNotAttempted = "not_attempted"
	// ConfirmedKindVectorScopeComplete: the enumeration's own count(n)
	// closed exactly against the assembled corpus, zero malformed rows,
	// the before/after watermark snapshot was stable, and every query term
	// was scored against the full enumerated population. See this file's
	// own "SCOPE REDUCTION" note for why this is a provisional, not a
	// gate-grade, label in Phase 1.
	ConfirmedKindVectorScopeComplete = "complete"
	// ConfirmedKindVectorScopeOverBudget: population*queryCount exceeded
	// ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS --
	// the census never ran (a correctness-safe refusal, never a partial
	// or sampled attempt reported as complete).
	ConfirmedKindVectorScopeOverBudget = "over_budget"
	// ConfirmedKindVectorScopeMalformed: at least one enumerated row could
	// not be decoded (missing canonical ID or embedding), or the
	// assembled corpus size disagreed with the independent count(n)
	// check. Fail-closed -- see this file's own "hardened per sol's note"
	// comment above; unlike oracle.go's measurement-only corpus fetch,
	// this path never skips-and-reconciles.
	ConfirmedKindVectorScopeMalformed = "malformed"
	// ConfirmedKindVectorScopeDrift: the org's _AcrWatermark set (scoped
	// to this resolution's own GraphKey) differed between the read taken
	// before the census and the one taken after count-closure -- a
	// projection write landed mid-scan. See this file's own
	// "STABLE-SNAPSHOT CHECK" note.
	ConfirmedKindVectorScopeDrift = "incomplete_snapshot_drift"
	// ConfirmedKindVectorScopeFailed: a genuine dependency error (query
	// failure, embed failure) occurred. Captured here rather than
	// propagated -- see ResolveDeps.ConfirmedKindVectorCensus's own doc
	// comment for why this shadow arm never fails the resolution.
	ConfirmedKindVectorScopeFailed = "failed"
)

// ConfirmedKindVectorCensusOutcome is ResolveDeps.ConfirmedKindVectorCensus's
// return value -- see that field's own doc comment. Every field is a
// closed-vocabulary state, a count, a bool, or a duration: no corpus text,
// no candidate identities (this is a SHADOW arm; it produces no candidates
// any caller in this change ever sees).
type ConfirmedKindVectorCensusOutcome struct {
	State              string
	PopulationCount    int64
	EnumeratedCount    int64
	MalformedCount     int64
	QueryCount         int
	QueriesScored      int
	ComparisonCount    int64
	RivalCountAboveTau int64
	// SnapshotStable is true iff the before/after _AcrWatermark comparison
	// found no drift. Meaningful only when State is Complete or Malformed
	// (a Drift state already implies false; an OverBudget/Failed/
	// NotAttempted outcome never reached the second watermark read, so
	// this stays at its zero value, false, for those states too --
	// callers must key off State, never this field alone, to learn why).
	SnapshotStable bool
	DurationMS     int64
}

// attemptConfirmedKindVectorCensus is buildConfirmedKindScopedSnapshot's own
// call site for the CHAOS-4155 Phase 1 shadow arm -- see this file's own
// top-level doc comment for the full design. Nil-safe: a nil
// deps.ConfirmedKindVectorCensus (every composition root unless explicitly
// wired) or an empty terms list returns ConfirmedKindVectorScopeNotAttempted
// with zero further cost, byte-identical to the arm not existing at all.
func attemptConfirmedKindVectorCensus(ctx context.Context, deps ResolveDeps, kind contextfabric.SubjectKind, terms []string) ConfirmedKindVectorCensusOutcome {
	if deps.ConfirmedKindVectorCensus == nil || len(terms) == 0 {
		return ConfirmedKindVectorCensusOutcome{State: ConfirmedKindVectorScopeNotAttempted}
	}
	return deps.ConfirmedKindVectorCensus(ctx, kind, terms)
}
