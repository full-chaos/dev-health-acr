package falkorgraph

import (
	"context"
	"fmt"
	"math"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// chaos4155_confirmed_kind_vector_census.go implements CHAOS-4155 Phase 1's
// SHADOW-ONLY kind-scoped vector completeness census -- the falkorgraph
// (graphrank.ResolveDeps.ConfirmedKindVectorCensus) side of the mechanism
// graphrank/chaos4155_confirmed_kind_vector_scope.go's own doc comment
// designs. Read that file first; this one is the concrete implementation of
// the exact enumeration + count-closure + stable-snapshot check it
// describes.
//
// Reuses oracle.go's CHAOS-3831/CHAOS-3849 pagination + independent
// count(n) closure pattern (fetchEmbedderFenceCorpus/
// countEmbedderFenceCorpus), kind-scoped and hardened: a malformed row here
// aborts the census (ConfirmedKindVectorScopeMalformed), it is never
// skipped-and-reconciled the way the measurement-only oracle treats one.

// confirmedKindVectorCensus implements
// graphrank.ResolveDeps.ConfirmedKindVectorCensus. See that field's own doc
// comment for the "never returns an error" contract: every failure mode is
// captured as a returned State instead of propagated, because this arm
// observes and never decides.
func (a *Adapter) confirmedKindVectorCensus(ctx context.Context, key, orgID string, kind contextfabric.SubjectKind, terms []string) graphrank.ConfirmedKindVectorCensusOutcome {
	started := a.now()
	if a.config.ConfirmedKindVectorCensusMaxComparisons <= 0 || a.embedder == nil {
		return graphrank.ConfirmedKindVectorCensusOutcome{State: graphrank.ConfirmedKindVectorScopeNotAttempted}
	}
	if len(terms) == 0 {
		return graphrank.ConfirmedKindVectorCensusOutcome{State: graphrank.ConfirmedKindVectorScopeNotAttempted}
	}

	identity := a.stampedEmbedderIdentity(a.embedder.Identity())

	before, err := a.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		return graphrank.ConfirmedKindVectorCensusOutcome{State: graphrank.ConfirmedKindVectorScopeFailed, DurationMS: a.now().Sub(started).Milliseconds()}
	}

	populationCount, err := a.countKindEmbedderFenceCorpus(ctx, key, orgID, string(kind), identity)
	if err != nil {
		return graphrank.ConfirmedKindVectorCensusOutcome{State: graphrank.ConfirmedKindVectorScopeFailed, DurationMS: a.now().Sub(started).Milliseconds()}
	}

	// codex R1 (High, confirmed): no separate term-count cap -- a
	// resolution's own SubjectTerms feed this census's completeness claim
	// (sol's "every interpreted term"), so silently dropping terms beyond
	// a fixed bound would let Complete fire without actually covering
	// every term. The comparison budget below is the ONLY admission
	// control, exactly matching sol's own "no calibration, just a
	// correctness-safe refusal" design.
	comparisonCount := populationCount * int64(len(terms))
	if comparisonCount > a.config.ConfirmedKindVectorCensusMaxComparisons {
		return graphrank.ConfirmedKindVectorCensusOutcome{
			State: graphrank.ConfirmedKindVectorScopeOverBudget, PopulationCount: populationCount,
			QueryCount: len(terms), ComparisonCount: comparisonCount,
			DurationMS: a.now().Sub(started).Milliseconds(),
		}
	}

	corpus, enumeratedCount, malformedCount, err := a.fetchKindEmbedderFenceCorpus(ctx, key, orgID, string(kind), identity)
	if err != nil {
		return graphrank.ConfirmedKindVectorCensusOutcome{State: graphrank.ConfirmedKindVectorScopeFailed, PopulationCount: populationCount, DurationMS: a.now().Sub(started).Milliseconds()}
	}
	if malformedCount > 0 || enumeratedCount+malformedCount != populationCount {
		return graphrank.ConfirmedKindVectorCensusOutcome{
			State: graphrank.ConfirmedKindVectorScopeMalformed, PopulationCount: populationCount,
			EnumeratedCount: enumeratedCount, MalformedCount: malformedCount,
			DurationMS: a.now().Sub(started).Milliseconds(),
		}
	}

	var queriesScored int
	var embedFailed bool
	var rivalCount int64
	for _, term := range terms {
		vectors, embedErr := a.embedder.Embed(ctx, []string{a.queryPrefixed(vectorQueryText(term))})
		if embedErr != nil || len(vectors) == 0 {
			// codex R1 (High, confirmed): a skipped term used to fall
			// through to Complete whenever the watermark snapshot was
			// stable, silently understating what was actually censused
			// (QueriesScored < QueryCount reading as Complete). An
			// embed-provider failure is a genuine dependency error for
			// THIS census's own completeness claim -- unlike ordinary
			// vector search, which treats "found nothing" as a normal
			// outcome, this census cannot complete its proof without
			// scoring every term against the full corpus. embedFailed
			// aborts to Failed below rather than letting the loop's
			// otherwise-successful terms masquerade as full coverage.
			embedFailed = true
			continue
		}
		queryVector := make([]float64, len(vectors[0]))
		for i, f := range vectors[0] {
			queryVector[i] = float64(f)
		}
		// codex R2 (Medium, confirmed): an embedder returning a zero-length
		// or non-finite (NaN/Inf) vector previously fell through to
		// trueCosineSimilarity's own dimension/zero-norm guard, which is
		// the right defense for a merely UNUSUAL vector but silently
		// reports 0 similarity for a genuinely BROKEN one -- indistinguishable
		// from "this term legitimately has no rivals" in the resulting
		// telemetry. Treated as the same embed-provider failure as the
		// err/empty-slice case above: this census's completeness claim
		// requires every term to have been scored against a trustworthy
		// vector, not silently zeroed out.
		if !finiteVector(queryVector) {
			embedFailed = true
			continue
		}
		queriesScored++
		for _, candidate := range corpus {
			if trueCosineSimilarity(queryVector, candidate.Vector) > a.similarityFloor {
				rivalCount++
			}
		}
	}
	if embedFailed {
		return graphrank.ConfirmedKindVectorCensusOutcome{
			State: graphrank.ConfirmedKindVectorScopeFailed, PopulationCount: populationCount,
			EnumeratedCount: enumeratedCount, QueryCount: len(terms), QueriesScored: queriesScored,
			ComparisonCount: int64(queriesScored) * enumeratedCount, RivalCountAboveTau: rivalCount,
			DurationMS: a.now().Sub(started).Milliseconds(),
		}
	}

	after, err := a.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		return graphrank.ConfirmedKindVectorCensusOutcome{
			State: graphrank.ConfirmedKindVectorScopeFailed, PopulationCount: populationCount,
			EnumeratedCount: enumeratedCount, QueryCount: len(terms), QueriesScored: queriesScored,
			ComparisonCount: int64(queriesScored) * enumeratedCount, RivalCountAboveTau: rivalCount,
			DurationMS: a.now().Sub(started).Milliseconds(),
		}
	}
	stable := watermarkSnapshotsEqual(before, after)
	outcome := graphrank.ConfirmedKindVectorCensusOutcome{
		PopulationCount: populationCount, EnumeratedCount: enumeratedCount,
		QueryCount: len(terms), QueriesScored: queriesScored,
		ComparisonCount: int64(queriesScored) * enumeratedCount, RivalCountAboveTau: rivalCount,
		SnapshotStable: stable, DurationMS: a.now().Sub(started).Milliseconds(),
	}
	if !stable {
		outcome.State = graphrank.ConfirmedKindVectorScopeDrift
		return outcome
	}
	outcome.State = graphrank.ConfirmedKindVectorScopeComplete
	return outcome
}

// chaos4155WatermarkSnapshot reads every _AcrWatermark node for orgID under
// the GIVEN graph key (never re-resolved -- see
// chaos4155_confirmed_kind_vector_scope.go's "STABLE-SNAPSHOT CHECK" note
// for why this, not a re-resolved "whatever is active now" read, is the
// correct comparison). The returned map is source -> backend_watermark;
// comparing two of these maps for equality (watermarkSnapshotsEqual) is the
// whole drift check: a value changing, or a source appearing/disappearing,
// means a projection write landed between the two calls.
func (a *Adapter) chaos4155WatermarkSnapshot(ctx context.Context, key, orgID string) (map[string]string, error) {
	cypher := fmt.Sprintf("MATCH (w:%s {%s:$org}) RETURN w.source AS source, w.backend_watermark AS backend_watermark", labelWatermark, propOrgID)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID}, true)
	if err != nil {
		return nil, safeDependencyError("read confirmed-kind vector census watermark snapshot", err)
	}
	snapshot := make(map[string]string, len(rows))
	for _, r := range rows {
		source := propStringValue(r["source"])
		if source == "" {
			// codex R2 (Medium, confirmed): source is part of a watermark
			// node's own MERGE identity key (projection.go's write path),
			// so a row with no source can only be a malformed/corrupted
			// node -- never a legitimate "not yet synced" state. Silently
			// dropping it here (as the earlier version did) let two
			// snapshots that both happen to drop the SAME malformed row
			// compare equal, hiding a data-integrity problem behind a
			// false Complete/Stable reading. This file's own top-of-file
			// doc comment already states the contract for a malformed
			// row elsewhere (fetchKindEmbedderFenceCorpus): abort, never
			// skip-and-reconcile.
			return nil, fmt.Errorf("confirmed-kind vector census watermark snapshot: malformed row with empty source for org %s", orgID)
		}
		snapshot[source] = propStringValue(r["backend_watermark"])
	}
	return snapshot, nil
}

// finiteVector reports whether v is non-empty and every element is a
// finite float64 (no NaN, no +/-Inf). A zero-length or non-finite embedder
// output is a broken vector, not merely an unusual one:
// trueCosineSimilarity's own dimension-mismatch/zero-norm guard already
// defends the DECISION this census makes (nothing here ever reads
// RivalCountAboveTau to decide State), but a NaN or Inf component would
// propagate through that guard silently -- NaN comparisons are always
// false in Go, so a poisoned query vector would score zero rivals against
// every corpus vector without ever tripping the >tau check, indistinguishable
// telemetry-wise from "this term genuinely has no rivals".
func finiteVector(v []float64) bool {
	if len(v) == 0 {
		return false
	}
	for _, f := range v {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
	}
	return true
}

// watermarkSnapshotsEqual reports whether before and after name the SAME
// set of sources with the SAME values. codex R1 (Medium, confirmed): an
// earlier version only checked len(before)==len(after) plus a one-way
// after[source]!=watermark scan, which reads a source being REPLACED by a
// different one of equal cardinality (e.g. {github:""} -> {jira:""}) as
// stable -- after["github"] is the zero value "", which happened to equal
// before["github"]'s own "" watermark. Checking key PRESENCE with the
// comma-ok form in both directions closes that gap.
//
// codex R2 (Medium, orchestrator-waived for Phase 1, ruling
// 2026-08-25 12:44): this is still a two-point-in-time VALUE comparison,
// not a transactional read -- it cannot see a source's watermark move
// w1 -> w2 -> w1 (a write landing and then being followed by another write
// that happens to restore the exact prior value) between the before and
// after reads. That sequence reads as SnapshotStable=true/State=Complete
// even though a projection write genuinely occurred mid-census and the
// corpus may have changed. Closing this needs a monotonic generation
// fence on the watermark schema (projection.go's write path, shared far
// beyond this shadow arm) -- a backend schema change out of scope for
// Phase 1. Accepted because this arm is SHADOW-ONLY and proven inert to
// any commit decision (this file's own doc comment; codex R2's own
// verdict), and Phase 2's measurement design consumes aggregates over
// multiple replicates, so one ABA-masked Complete cannot by itself flip a
// measurement conclusion. Follow-up: generation-fence hardening tracked
// as a separate Linear issue related to CHAOS-4155.
func watermarkSnapshotsEqual(before, after map[string]string) bool {
	if len(before) != len(after) {
		return false
	}
	for source, watermark := range before {
		value, ok := after[source]
		if !ok || value != watermark {
			return false
		}
	}
	return true
}

// countKindEmbedderFenceCorpus is countEmbedderFenceCorpus's kind-scoped
// sibling -- same aggregate-count-is-not-RESULTSET_SIZE-bound reasoning
// (oracle.go's own doc comment), additionally constrained to exactly one
// SubjectKind.
func (a *Adapter) countKindEmbedderFenceCorpus(ctx context.Context, key, orgID, kind, identity string) (int64, error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND n.%s = $identity "+
			"AND n.%s = $kind AND n.%s IS NOT NULL "+
			"RETURN count(n) AS total",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity, propKind, propCanonicalID,
	)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "identity": identity, "kind": kind}, true)
	if err != nil {
		return 0, safeDependencyError("count confirmed-kind vector census corpus", err)
	}
	if len(rows) != 1 {
		return 0, fmt.Errorf("confirmed-kind vector census count query returned %d rows, want exactly 1", len(rows))
	}
	total, ok := intFromCount(rows[0]["total"])
	if !ok {
		return 0, fmt.Errorf("confirmed-kind vector census count query returned a non-numeric total: %#v", rows[0]["total"])
	}
	return int64(total), nil
}

// fetchKindEmbedderFenceCorpus is fetchEmbedderFenceCorpus's kind-scoped
// sibling, hardened per sol's CHAOS-4155 consult note: unlike the
// measurement-only oracle, a malformed row here is NEVER silently skipped
// into the reconciliation -- it is counted and reported, and the caller
// (confirmedKindVectorCensus) treats ANY malformedCount>0 (or a
// enumerated+malformed count that still disagrees with the independently
// queried population) as ConfirmedKindVectorScopeMalformed, fail-closed.
func (a *Adapter) fetchKindEmbedderFenceCorpus(ctx context.Context, key, orgID, kind, identity string) (corpus []oracleVector, enumeratedCount int64, malformedCount int64, err error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND n.%s = $identity "+
			"AND n.%s = $kind AND n.%s IS NOT NULL "+
			"RETURN n ORDER BY n.%s SKIP $skip LIMIT $limit",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity, propKind, propCanonicalID, propCanonicalID,
	)
	for skip := 0; ; skip += oracleFetchBatchSize {
		rows, qerr := a.api.query(ctx, key, cypher, map[string]interface{}{
			"org": orgID, "identity": identity, "kind": kind, "skip": skip, "limit": oracleFetchBatchSize,
		}, true)
		if qerr != nil {
			return nil, enumeratedCount, malformedCount, safeDependencyError("fetch confirmed-kind vector census corpus", qerr)
		}
		for _, r := range rows {
			n, ok := r["n"].(*node)
			if !ok || n == nil {
				malformedCount++
				continue
			}
			canonicalID := propStringValue(n.Properties[propCanonicalID])
			vector, vok := decodeVectorProperty(n.Properties[propEmbedding])
			if canonicalID == "" || !vok || len(vector) == 0 {
				malformedCount++
				continue
			}
			corpus = append(corpus, oracleVector{Kind: kind, CanonicalID: canonicalID, Label: propStringValue(n.Properties[propLabel]), Vector: vector})
			enumeratedCount++
		}
		if len(rows) < oracleFetchBatchSize {
			break
		}
	}
	return corpus, enumeratedCount, malformedCount, nil
}
