package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-4155: kind-scoped vector completeness census. Introduced in Phase 1
// as SHADOW ONLY telemetry; CHAOS-4311 (Phase 3, see the PHASE 1 / PHASE 2 /
// PHASE 3 section below) made its outcome decision-bearing in resolve.go's
// own caller -- codex R2 (Low, confirmed) flagged this header as stale
// after that flip, since "SHADOW ONLY" no longer describes the current
// deployment.
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
// PHASE 1 / PHASE 2 / PHASE 3 (sol's own Phase 1/2 split, team-lead: "GO
// exactly as sol split it"; Phase 3 = CHAOS-4311, chris "Okay" 2026-08-26):
// Phase 1 was instrumentation-only -- the shadow census ran, recorded
// ConfirmedKindVectorScope* telemetry, and never changed
// buildConfirmedKindScopedSnapshot's own returned scopeState/pool; every
// vector-enabled row read confirmedKindScopeState="plan_incomplete" and the
// commit gate's decision was byte-identical to pre-CHAOS-4155 behavior (see
// TestBuildConfirmedKindScopedSnapshot_VectorCensusNeverChangesReturnedScopeState,
// which still pins this file's OWN outcome-computation contract unchanged --
// Phase 3 only widens what resolve.go's CALLER does with a Complete outcome,
// never this function's own state machine). Phase 2 (acr #267/#269/#272/#274,
// kiac measurement, cf-measurement-trials.md 02:00 08-26) measured the
// mechanism live: decision metrics byte-identical across cold+2 warm reps at
// budget 2,000,000 (real comparison counts topped out at 44/query), and 48%
// of Complete cases surfaced >=1 vector rival above tau a lexical-only proof
// would miss.
//
// Phase 3 (THIS CHANGE, CHAOS-4311) makes the outcome decision-bearing in
// resolve.go's own caller: Complete with RivalCountAboveTau==0 lets the
// isolated confirmed-kind-scoped population commit exactly like
// confirmedKindScopeComplete already does (SAME re-decision call, SAME
// confirmedKindScopedBasis=true population_basis telemetry -- see that call
// site's own doc comment); Complete WITH rivals never commits anything new
// (the isolated population's own re-decision does not run) but the rivals
// this outcome now carries (Rivals, below) become an OFFER-ONLY population,
// following CHAOS-4271's own offerOnlyPool precedent exactly (private pool
// the commit gate never reads, nil identity so an offer-only find can never
// collide-suppress an unrelated real commit). Every other state
// (OverBudget/Malformed/Drift/Failed/NotAttempted) still fails closed,
// unchanged from Phase 1/2 -- the plan stays incomplete, nothing new offers
// or commits.
//
// DO-NOT-BUILD (carried over from chaos4154_confirmed_kind_scope.go's own
// list, still binding here):
//   - Do not mutate or reinterpret the resolution-wide searchTruncated bit.
//   - Do not infer completeness from non-emptiness, counts, observed
//     truncation rates, limit+1, or top-result stability -- the count-
//     closure check here is enumeration-verified cardinality, never a bare
//     count comparison against k.
//   - A rival never auto-commits, regardless of its own similarity or rank
//     -- offer-only is not a lesser commit tier, it is the caller's own
//     private pool the commit gate structurally cannot see (resolve.go).

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
// return value -- see that field's own doc comment. Every scalar field is a
// closed-vocabulary state, a count, a bool, or a duration -- no corpus text.
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
	// Rivals (CHAOS-4311, Phase 3) carries one CandidateNode per DISTINCT
	// subject this census found scoring above the similarity floor against
	// ANY of the resolution's own query terms -- non-nil only when
	// State==Complete and at least one candidate cleared the floor. This is
	// a DEDUPLICATED count, unlike RivalCountAboveTau above (which is
	// unchanged from Phase 1/2 for measurement continuity: it sums every
	// (term, candidate) pair that cleared the floor, so a single candidate
	// matching two different query terms counts twice there) -- so
	// len(Rivals) <= RivalCountAboveTau, with equality only when no
	// candidate matched more than one term. A row appearing for multiple
	// terms is represented ONCE here, keeping its own highest similarity
	// across those terms. Sorted by CanonicalID for deterministic ordering
	// (the census builds this via a Go map internally; map iteration order
	// is not deterministic).
	//
	// Every element's own Attributes come from the SAME raw graph node this
	// census's own exhaustive enumeration already read (falkorgraph's
	// toCandidateNode conversion) -- i.e. genuinely UNFILTERED by
	// authorization. This is deliberately NOT a caller-visible candidate
	// list by construction: a Rival must still pass THROUGH the SAME
	// authorization gate (graphrank.NodeCandidate/AuthorizedAttributes,
	// via mergeSearchResults) every other candidate source in this package
	// goes through before resolve.go's own offer-only merge may expose it
	// to a caller -- see that call site's own doc comment. Mechanism is
	// always contextfabric.MatchVector; VectorSimilarity carries the
	// UNCLAMPED raw cosine similarity that won it inclusion (never fed into
	// Relevance/ResultConfidence directly -- see CandidateNode.Score's own
	// "loaded gun" doc comment).
	Rivals []CandidateNode
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
