package falkorgraph

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// chaos4155_confirmed_kind_vector_census.go implements CHAOS-4155's
// kind-scoped vector completeness census -- the falkorgraph
// (graphrank.ResolveDeps.ConfirmedKindVectorCensus) side of the mechanism
// graphrank/chaos4155_confirmed_kind_vector_scope.go's own doc comment
// designs. Read that file first; this one is the concrete implementation of
// the exact enumeration + count-closure + stable-snapshot check it
// describes.
//
// codex R2 (Low, confirmed, CHAOS-4311): this file's own outcome computation
// has never changed shape across phases (see that other file's PHASE 1 /
// PHASE 2 / PHASE 3 section) -- SHADOW-ONLY described Phase 1's caller
// behavior, not this file's. As of CHAOS-4311 (Phase 3) the outcome this
// file returns IS decision-bearing in resolve.go's own caller; "SHADOW-ONLY"
// no longer describes the current deployment and is removed here to avoid
// misleading a reader of this security-sensitive completeness gate.
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
// observes and never decides. temporal (CHAOS-4311, codex R1 High,
// confirmed): the SAME admission window this resolution's own SearchKind
// pass already applies (reader.go's ConfirmedKindVectorCensus closure binds
// it to the identical newTemporalFilter value) -- an inactive (current-axis)
// filter costs nothing extra and changes no query text, but a historical
// (ValidTime/ObservedTime/Range) resolution now excludes out-of-window
// nodes from both the population count(n) and the enumerated corpus, the
// same as every other retrieval path in this package already does.
func (a *Adapter) confirmedKindVectorCensus(ctx context.Context, key, orgID string, kind contextfabric.SubjectKind, terms []string, temporal temporalFilter) graphrank.ConfirmedKindVectorCensusOutcome {
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

	populationCount, err := a.countKindEmbedderFenceCorpus(ctx, key, orgID, string(kind), identity, temporal)
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

	corpus, enumeratedCount, malformedCount, err := a.fetchKindEmbedderFenceCorpus(ctx, key, orgID, string(kind), identity, temporal)
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
	// rivalsByCanonicalID (CHAOS-4311, Phase 3) accumulates the DISTINCT
	// candidates that clear the similarity floor, across every term --
	// see ConfirmedKindVectorCensusOutcome.Rivals' own doc comment for why
	// this is deduplicated while rivalCount above (unchanged from Phase
	// 1/2) is not. Keyed by CanonicalID alone: every row in corpus shares
	// the SAME kind (this cypher's own WHERE clause), so CanonicalID is
	// already a unique key within this fetch. Value keeps the HIGHEST
	// similarity seen across every term this row cleared the floor for.
	rivalsByCanonicalID := make(map[string]censusRival)
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
			similarity := trueCosineSimilarity(queryVector, candidate.Vector)
			if similarity > a.similarityFloor {
				rivalCount++
				if existing, ok := rivalsByCanonicalID[candidate.CanonicalID]; !ok || similarity > existing.similarity {
					rivalsByCanonicalID[candidate.CanonicalID] = censusRival{candidate: candidate.Candidate, similarity: similarity}
				}
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
	// CHAOS-4311: Rivals attached ONLY on the Complete outcome -- see
	// ConfirmedKindVectorCensusOutcome.Rivals' own doc comment. Every other
	// return path above (OverBudget/Malformed/Failed/Drift) leaves this at
	// its nil zero value, unchanged from Phase 1/2.
	outcome.Rivals = a.sortedCensusRivals(rivalsByCanonicalID)
	return outcome
}

// watermarkSnapshotEntry is one source's own (epoch, generation) pair --
// see watermarkSnapshotsEqual's own doc comment for why BOTH fields, not
// generation alone, are what make the drift check survive a graph purge.
type watermarkSnapshotEntry struct {
	Generation int64
	Epoch      string
}

// censusRival pairs one deduplicated rival's graphrank.CandidateNode
// conversion with the highest cosine similarity any query term found for it
// -- see rivalsByCanonicalID's own declaration comment (confirmedKindVectorCensus).
type censusRival struct {
	candidate  graphrank.CandidateNode
	similarity float64
}

// sortedCensusRivals converts confirmedKindVectorCensus's own deduplicated
// rival map into the CandidateNode slice ConfirmedKindVectorCensusOutcome.Rivals
// carries: Mechanism/VectorSimilarity/Relevance stamped from each rival's own
// highest similarity, mirroring vectorSearchNodesWithOverFetch's exact
// construction (Mechanism=MatchVector, VectorSimilarity=the raw unclamped
// value, Relevance=vectorRelevanceFromSimilarity against this adapter's OWN
// a.similarityFloor -- the SAME tau every rival here already cleared to earn
// its place in the map. This census enumerates the FULL embedder-fenced
// corpus, so there is no truncation concept here and the floor-clamped
// Relevance vectorSearchNodesWithOverFetch uses under truncation never
// applies). Sorted by CanonicalID for a deterministic artifact/telemetry
// ordering independent of Go's randomized map iteration.
func (a *Adapter) sortedCensusRivals(rivalsByCanonicalID map[string]censusRival) []graphrank.CandidateNode {
	if len(rivalsByCanonicalID) == 0 {
		return nil
	}
	canonicalIDs := make([]string, 0, len(rivalsByCanonicalID))
	for canonicalID := range rivalsByCanonicalID {
		canonicalIDs = append(canonicalIDs, canonicalID)
	}
	sort.Strings(canonicalIDs)
	rivals := make([]graphrank.CandidateNode, 0, len(canonicalIDs))
	for _, canonicalID := range canonicalIDs {
		rival := rivalsByCanonicalID[canonicalID]
		candidate := rival.candidate
		candidate.Mechanism = contextfabric.MatchVector
		similarity := rival.similarity
		candidate.VectorSimilarity = &similarity
		candidate.Relevance = graphrank.Normalized(vectorRelevanceFromSimilarity(similarity, a.similarityFloor))
		rivals = append(rivals, candidate)
	}
	return rivals
}

// chaos4155WatermarkSnapshot reads every _AcrWatermark node for orgID under
// the GIVEN graph key (never re-resolved -- see
// chaos4155_confirmed_kind_vector_scope.go's "STABLE-SNAPSHOT CHECK" note
// for why this, not a re-resolved "whatever is active now" read, is the
// correct comparison). The returned map is source -> (epoch, generation)
// (CHAOS-4298: projection.go's writeWatermark's own monotonic
// per-(org,source) counter and per-node-lifetime nonce, both stamped on
// EVERY write); comparing two of these maps for equality
// (watermarkSnapshotsEqual) is the whole drift check: EITHER field
// changing, or a source appearing/disappearing, means a projection write
// (or a purge-and-rebuild) landed between the two calls. Generation+epoch,
// not backend_watermark's raw value, is what makes this check ABA-proof --
// see watermarkSnapshotsEqual's own doc comment.
func (a *Adapter) chaos4155WatermarkSnapshot(ctx context.Context, key, orgID string) (map[string]watermarkSnapshotEntry, error) {
	cypher := fmt.Sprintf("MATCH (w:%s {%s:$org}) RETURN w.source AS source, w.generation AS generation, w.epoch AS epoch", labelWatermark, propOrgID)
	rows, err := a.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID}, true)
	if err != nil {
		return nil, safeDependencyError("read confirmed-kind vector census watermark snapshot", err)
	}
	snapshot := make(map[string]watermarkSnapshotEntry, len(rows))
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
		// CHAOS-4298: same fail-closed discipline as the empty-source check
		// above, extended to the new field -- a row with a real source but
		// no numeric generation (e.g. a pre-CHAOS-4298 node that has never
		// been written since this field was added, read on a code path that
		// somehow bypasses writeWatermark's own coalesce-based self-heal)
		// must abort the census, never silently read as generation 0 and
		// risk a false-stable comparison against a genuinely different
		// absent-vs-zero state.
		generation, ok := generationFromRow(r["generation"])
		if !ok {
			return nil, fmt.Errorf("confirmed-kind vector census watermark snapshot: malformed row with non-numeric generation for source %q, org %s", source, orgID)
		}
		// CHAOS-4298 follow-up: identical discipline extended to epoch --
		// every node writeWatermark ever produces has a non-empty epoch
		// (either a fresh per-creation nonce or the fixed
		// chaos4298SentinelEpoch self-heal), so an empty one here can only
		// mean a row written before this follow-up's own generation-only
		// predecessor was updated, or a corrupted node -- abort, matching
		// every other malformed-row case on this same node.
		epoch := propStringValue(r["epoch"])
		if epoch == "" {
			return nil, fmt.Errorf("confirmed-kind vector census watermark snapshot: malformed row with empty epoch for source %q, org %s", source, orgID)
		}
		snapshot[source] = watermarkSnapshotEntry{Generation: generation, Epoch: epoch}
	}
	return snapshot, nil
}

// generationFromRow parses a watermark row's own generation value STRICTLY
// -- deliberately not oracle.go's intFromCount (that helper truncates via
// int(v), which is right for a count(n) query aggregate FalkorDB always
// returns as a genuine integer, but wrong here). codex R1 (Medium,
// confirmed): a stray non-integral or negative value on this property
// (only reachable via tampering outside this file's own writeWatermark,
// but a graph is not otherwise trusted to be internally consistent by this
// file's own "abort, never skip-and-reconcile" discipline) could silently
// truncate two DIFFERENT generations to the SAME int (e.g. 1.9 and 1.1 both
// truncating to 1), reporting a stable comparison over values that were
// never actually equal. Every value this codebase's own writer ever
// produces is a non-negative whole number by construction
// (coalesce(w.generation, 0) + 1, starting at 1) -- anything else fails
// closed.
func generationFromRow(value interface{}) (int64, bool) {
	var f float64
	switch v := value.(type) {
	case int64:
		f = float64(v)
	case int:
		f = float64(v)
	case float64:
		f = v
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 {
		return 0, false
	}
	return int64(f), true
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
// set of sources at the SAME (epoch, generation). codex R1 (Medium,
// confirmed): an earlier version only checked len(before)==len(after) plus
// a one-way after[source]!=watermark scan, which reads a source being
// REPLACED by a different one of equal cardinality (e.g. {github:""} ->
// {jira:""}) as stable -- after["github"] is the zero value "", which
// happened to equal before["github"]'s own "" watermark. Checking key
// PRESENCE with the comma-ok form in both directions closes that gap; that
// structure is unchanged here.
//
// CHAOS-4298 (closes codex R2's Medium, orchestrator-waived for Phase 1,
// ruling 2026-08-25 12:44): the prior version compared before/after
// backend_watermark STRINGS -- a two-point-in-time VALUE comparison, not a
// transactional read, so it could not see a source's watermark move
// w1 -> w2 -> w1 (a write landing and then being followed by another write
// that happens to restore the exact prior value) between the two reads.
// That sequence read as SnapshotStable=true/State=Complete even though a
// projection write genuinely occurred mid-census and the corpus may have
// changed underneath the comparison.
//
// Comparing GENERATION closes THIS EXACT gap, with no transactional read
// needed: projection.go's writeWatermark bumps generation by exactly 1 on
// EVERY write to a (org, source), regardless of whether backend_watermark's
// own value changed -- so generation can never revert the way a value can.
// If before[source] == after[source] here, NO watermark write landed on
// that source between the two reads (an intervening write, value-reverting
// or not, would have advanced generation past what the first read saw); if
// a watermark write DID land -- even a w1 -> w2 -> w1 round trip -- generation
// strictly increased, so the two reads can never compare equal for that
// source. A source's own generation identity therefore implies its
// backend_watermark was ALSO stable (both are set by the SAME writeWatermark
// call), without needing to fetch or compare that value at all.
//
// EPOCH closes generation's own residual gap (team-lead ruling, 2026-08-26,
// same session as the generation fence above): generation alone is scoped
// to one graph NODE's lifetime -- PurgeOrganization deletes the graph
// outright, and the next write to that (org, source) creates a FRESH node
// whose generation self-heals to 1 again, indistinguishable from the SAME
// node still sitting at generation 1. epoch is a writer-generated nonce
// stamped ONCE, the instant a node is created (writeWatermark's own ON
// CREATE branch) or, for a pre-epoch node, self-healed ONCE to a FIXED
// sentinel constant (ON MATCH) -- either way, never reassigned again while
// that node exists. A purge-and-rebuild always re-creates the node (MERGE
// finds nothing to match), so it always gets a NEW epoch, even on the
// (most likely) case where its generation lands back on 1 -- the two reads
// can then never agree on (epoch, generation) across a purge.
//
// ONE KNOWN, NAMED LIMITATION this does NOT close (codex R1 review,
// 2026-08-26; team-lead ruling: WAIVED, documented, no follow-up ticket --
// not a regression from the value-based comparison this replaces, which had
// the identical exposure): WRITE-ORDERING, NOT WRITE-ATOMICITY.
// ApplyProjectionBatch (projection.go) issues entity/relationship/content/
// episode/tombstone/vector writes as SEPARATE GRAPH.QUERY calls, and only
// calls writeWatermark LAST, once that whole batch has otherwise succeeded.
// If a census's own before/after reads straddle a batch that has already
// written corpus data but has not yet reached its own writeWatermark call
// (e.g. still embedding), (epoch, generation) reads identical across the
// census's two reads even though the corpus already changed -- this fence
// proves "no committed batch completed," not "no corpus mutation is in
// flight." The value-based comparison this replaces had the SAME exposure
// (backend_watermark is also written last, by the same call), so this is
// not new; closing it for real needs either a pre-batch marker or true
// multi-statement transactions, which FalkorDB does not offer -- the
// ticket's own alternative design ("a transactional read spanning corpus
// fetch + before/after snapshot") is the shape that would.
//
// WAIVER BASIS (team-lead ruling, 2026-08-26 -- states the premise the
// waiver above rests on, since it shifts once CHAOS-4311 lands): this
// census is SHADOW-ONLY and decision-inert TODAY, but CHAOS-4311 (in build)
// makes it decision-BEARING -- a rival found above the similarity floor
// feeds an OFFER-ONLY candidate pool, never a commit. Undetected drift from
// the residual write-ordering gap above therefore means, AT WORST, a rival
// offer built from a slightly stale corpus -- never a wrong commit, by
// CHAOS-4311's own offer-only-pool construction. The waiver's basis is this
// bounded blast radius, not "shadow-only, nothing depends on it."
func watermarkSnapshotsEqual(before, after map[string]watermarkSnapshotEntry) bool {
	if len(before) != len(after) {
		return false
	}
	for source, entry := range before {
		value, ok := after[source]
		if !ok || value != entry {
			return false
		}
	}
	return true
}

// countKindEmbedderFenceCorpus is countEmbedderFenceCorpus's kind-scoped
// sibling -- same aggregate-count-is-not-RESULTSET_SIZE-bound reasoning
// (oracle.go's own doc comment), additionally constrained to exactly one
// SubjectKind. temporal (CHAOS-4311, codex R1 High, confirmed): the SAME
// admission window kindScopedFulltextSearchNodes' own lexical pass already
// applies -- an inactive (current-axis) filter renders as the empty string,
// byte-identical to before this parameter existed.
func (a *Adapter) countKindEmbedderFenceCorpus(ctx context.Context, key, orgID, kind, identity string, temporal temporalFilter) (int64, error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND n.%s = $identity "+
			"AND n.%s = $kind AND n.%s IS NOT NULL%s "+
			"RETURN count(n) AS total",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity, propKind, propCanonicalID, temporal.predicate("n"),
	)
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "identity": identity, "kind": kind}), true)
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

// censusCorpusRow (CHAOS-4311, Phase 3) pairs one enumerated corpus row's
// oracleVector (used for cosine scoring, unchanged from Phase 1/2) with the
// SAME row's graphrank.CandidateNode conversion (toCandidateNode) -- needed
// only once a row scores above the similarity floor and must become an
// authorized offer. See ConfirmedKindVectorCensusOutcome.Rivals' own doc
// comment for why this raw conversion is not itself caller-visible until it
// passes through mergeSearchResults' own authorization gate downstream.
type censusCorpusRow struct {
	oracleVector
	Candidate graphrank.CandidateNode
}

// fetchKindEmbedderFenceCorpus is fetchEmbedderFenceCorpus's kind-scoped
// sibling, hardened per sol's CHAOS-4155 consult note: unlike the
// measurement-only oracle, a malformed row here is NEVER silently skipped
// into the reconciliation -- it is counted and reported, and the caller
// (confirmedKindVectorCensus) treats ANY malformedCount>0 (or a
// enumerated+malformed count that still disagrees with the independently
// queried population) as ConfirmedKindVectorScopeMalformed, fail-closed.
// temporal (CHAOS-4311, codex R1 High, confirmed): the SAME admission
// window countKindEmbedderFenceCorpus and kindScopedFulltextSearchNodes'
// own lexical pass already apply -- see that function's own doc comment.
func (a *Adapter) fetchKindEmbedderFenceCorpus(ctx context.Context, key, orgID, kind, identity string, temporal temporalFilter) (corpus []censusCorpusRow, enumeratedCount int64, malformedCount int64, err error) {
	cypher := fmt.Sprintf(
		"MATCH (n:%s {%s:$org}) WHERE n.%s IS NOT NULL AND n.%s = $identity "+
			"AND n.%s = $kind AND n.%s IS NOT NULL%s "+
			"RETURN n ORDER BY n.%s SKIP $skip LIMIT $limit",
		labelSubject, propOrgID, propEmbedding, propEmbedderIdentity, propKind, propCanonicalID, temporal.predicate("n"), propCanonicalID,
	)
	for skip := 0; ; skip += oracleFetchBatchSize {
		rows, qerr := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{
			"org": orgID, "identity": identity, "kind": kind, "skip": skip, "limit": oracleFetchBatchSize,
		}), true)
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
			corpus = append(corpus, censusCorpusRow{
				oracleVector: oracleVector{Kind: kind, CanonicalID: canonicalID, Label: propStringValue(n.Properties[propLabel]), Vector: vector},
				// CHAOS-4311: toCandidateNode carries n's FULL raw
				// Properties (Attributes) through unfiltered -- this row
				// has NOT been authorized yet. See censusCorpusRow's own
				// doc comment.
				Candidate: toCandidateNode(n),
			})
			enumeratedCount++
		}
		if len(rows) < oracleFetchBatchSize {
			break
		}
	}
	return corpus, enumeratedCount, malformedCount, nil
}
