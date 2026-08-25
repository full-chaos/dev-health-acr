package falkorgraph

import (
	"context"
	"fmt"

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

// confirmedKindVectorCensusMaxTermQueries bounds how many of the caller's
// own terms this census embeds per call -- a defensive cap independent of
// the comparison budget, so a resolution with an unusually large term list
// cannot multiply embed-provider calls unboundedly even before the
// population*queryCount budget check runs. terms beyond this bound are
// simply not queried; QueryCount reports what was ACTUALLY embedded, never
// len(terms), so this is visible in telemetry rather than a silent
// narrowing.
const confirmedKindVectorCensusMaxTermQueries = 8

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
	queryTerms := terms
	if len(queryTerms) > confirmedKindVectorCensusMaxTermQueries {
		queryTerms = queryTerms[:confirmedKindVectorCensusMaxTermQueries]
	}
	if len(queryTerms) == 0 {
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

	comparisonCount := populationCount * int64(len(queryTerms))
	if comparisonCount > a.config.ConfirmedKindVectorCensusMaxComparisons {
		return graphrank.ConfirmedKindVectorCensusOutcome{
			State: graphrank.ConfirmedKindVectorScopeOverBudget, PopulationCount: populationCount,
			QueryCount: len(queryTerms), ComparisonCount: comparisonCount,
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
	var rivalCount int64
	for _, term := range queryTerms {
		vectors, embedErr := a.embedder.Embed(ctx, []string{a.queryPrefixed(vectorQueryText(term))})
		if embedErr != nil || len(vectors) == 0 {
			// A single term's embed failure does not abort the whole
			// census -- it is scored as one fewer QueriesScored, exactly
			// like a term ordinary vector search silently found nothing
			// for. The comparison budget already reserved room for it,
			// so ComparisonCount below intentionally overstates by this
			// term's share when this happens (visible via
			// QueriesScored < QueryCount, never hidden).
			continue
		}
		queryVector := make([]float64, len(vectors[0]))
		for i, f := range vectors[0] {
			queryVector[i] = float64(f)
		}
		queriesScored++
		for _, candidate := range corpus {
			if trueCosineSimilarity(queryVector, candidate.Vector) > a.similarityFloor {
				rivalCount++
			}
		}
	}

	after, err := a.chaos4155WatermarkSnapshot(ctx, key, orgID)
	if err != nil {
		return graphrank.ConfirmedKindVectorCensusOutcome{
			State: graphrank.ConfirmedKindVectorScopeFailed, PopulationCount: populationCount,
			EnumeratedCount: enumeratedCount, QueryCount: len(queryTerms), QueriesScored: queriesScored,
			ComparisonCount: int64(queriesScored) * enumeratedCount, RivalCountAboveTau: rivalCount,
			DurationMS: a.now().Sub(started).Milliseconds(),
		}
	}
	stable := watermarkSnapshotsEqual(before, after)
	outcome := graphrank.ConfirmedKindVectorCensusOutcome{
		PopulationCount: populationCount, EnumeratedCount: enumeratedCount,
		QueryCount: len(queryTerms), QueriesScored: queriesScored,
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
			continue
		}
		snapshot[source] = propStringValue(r["backend_watermark"])
	}
	return snapshot, nil
}

func watermarkSnapshotsEqual(before, after map[string]string) bool {
	if len(before) != len(after) {
		return false
	}
	for source, watermark := range before {
		if after[source] != watermark {
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
