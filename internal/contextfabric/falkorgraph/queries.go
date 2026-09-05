package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fulltextRelevanceFloor and fulltextRelevanceCeiling bound the confidence
// band a RediSearch full-text hit can ever normalize into (D11 / AC-3778-0).
// Chosen so the shipped commit thresholds in
// graphrank.ResolveFromMergedCandidates -- lone candidate >= 0.72; top-of-two
// >= 0.88 with gap >= 0.12 -- keep their intended meaning: a hit that fully
// satisfies its own query (every OR-tokenized term matched -- the ceiling)
// clears the lone-candidate gate exactly the way
// docs/design/context-fabric-falkordb-adapter.md §6.2's proposed "sole
// full-text hit, label-field match" rung (0.75) was chosen to, and the floor
// matches that same table's "body-only hit (today's default)" rung (0.50) --
// a genuine hit never reads as "no signal" (0), nor as more confident than
// an exact canonical/alias match (1.0, unchanged, set in candidate.go).
//
// The design doc's ladder is field-level (label/alias/body); this adapter
// cannot reproduce that today because the fulltext index covers one merged
// search_text property (identity.go's createFulltextIndex) with no separate
// fields to distinguish a match by. This is the coarsest faithful
// approximation available -- see fulltextRelevanceFromMatchedTerms's doc
// comment for what signal is used instead, and why (Codex P1/P3, fix round
// 2: this band was originally reached by max-min normalizing within one
// query's own result set, which round-2 review found two things wrong
// with: (1) a singleton or all-tied result set always read as the ceiling
// regardless of how weak that lone hit actually was, indistinguishable
// from a server-side-truncated result set that could have had stronger
// candidates cut off before normalization ever ran; (2) two DIFFERENT
// queries' independently-relative-normalized confidences were then
// compared directly against each other by ResolveSubjects' merge/sort
// (resolve.go, resolution.go) as if they shared one scale, when each one's
// ceiling only ever meant "best of its own, unrelated result set").
const (
	fulltextRelevanceFloor   = 0.50
	fulltextRelevanceCeiling = 0.75
)

// fulltextRelevanceFromMatchedTerms maps ONE candidate's own matched-term
// coverage -- how many of the query's termCount OR-tokenized terms this
// specific candidate's OWN indexed text actually contains -- into
// graphrank's documented [fulltextRelevanceFloor, fulltextRelevanceCeiling]
// band (D11 / AC-3778-0, Codex P1/P3 fix round 2):
//
//	proportion := clamp(matchedTermCount / termCount, 0, 1)
//	confidence := floor + (ceiling-floor) * proportion
//
// This is an ABSOLUTE, per-candidate, EXACT function -- it depends on
// nothing but that one candidate's own (matchedTermCount, termCount) pair,
// never on what else came back in the same query's result set, and never on
// which particular query call produced it. A hit that only satisfies 1 of 4
// OR-tokenized query terms scores low regardless of how weak or strong its
// raw RediSearch score looks in isolation, because it is being measured
// against what a hit that satisfied all 4 terms would look like, not
// against whatever else happened to be in this particular result set (Codex
// P1). This also fixes the truncation trap an earlier version of this fix
// had: queries.go's Cypher LIMIT can shrink a result set to size 1 BEFORE
// normalization ever runs, but since this function never looks at the batch
// at all, a lone-but-weak hit is scored identically whether or not stronger
// competitors existed and were truncated away.
//
// Because the formula is fixed and identical for every call, its output is
// directly comparable across calls with different termCount, different
// underlying corpora, or different result-set sizes (Codex P3) -- there is
// exactly one normalization domain, not one per query.
//
// Two earlier designs for matchedTermCount, both falsified by a live
// FalkorDB server before landing here:
//
//  1. score/termCount against a fixed reference constant, calibrated and
//     live-validated against one corpus (a 3-of-3-term OR match scoring 6,
//     2-of-3 scoring 4, 1-of-3 scoring 2 -- exactly 2 points per matched
//     term). A SECOND live corpus
//     (TestLiveRelationshipProjectionPreservesPriorCanonicalEntityMetadata's
//     "Dev Agent" previous-name, a genuine 2-of-2-term full match) scored
//     only 1.0 total -- four times weaker -- and would have been WRONGLY
//     demoted below the lone-candidate gate. RediSearch's real
//     per-matched-term score contribution depends on corpus-wide term
//     rarity/idf, which this adapter cannot query, so no fixed score-based
//     constant is safe.
//  2. Exact term coverage via one single-term db.idx.fulltext.queryNodes
//     sub-query per term, counting literal server-side membership. This
//     failed on the SAME live corpus for a different reason: FalkorDB's
//     fulltext tokenizer failed to match a bare single-term query for
//     "Dev" even though "Dev" is genuinely present in that candidate's own
//     search_text (confirmed independently via a raw Cypher query, no org
//     filter, still 0 rows) -- the same family of compound-token-adjacent
//     indexing quirk this adapter's own live tests already document for
//     "AskDev" (see adapter_live_invariants_test.go), just triggered by a
//     different token. A per-term Cypher round-trip inherits FalkorDB's
//     own indexing inconsistencies; this adapter has no way to work around
//     them from outside the server.
//
// matchedTermCount is therefore computed entirely in Go, from the SAME
// search_text property value this candidate's own row already carries
// (projection.go writes it onto every entity/content/episode Subject node;
// toCandidateNode's Attributes carries it through unchanged) -- see
// fulltextSearchNodes. Tokenizing that text with the exact same
// fulltextWords function used to derive the query's own matchTerms (Codex
// R2-2 -- tokenizeForFulltext itself stays reserved for building the
// RediSearch query string, see fulltextWords' doc comment) means "does
// this candidate contain term X" is answered identically to how the terms
// themselves were derived, with zero additional queries and zero
// dependency on RediSearch's own per-term matching behavior.
//
// NaN-safe / defensive: a non-positive termCount normalizes to the floor
// rather than dividing by zero; matchedTermCount is clamped into [0,
// termCount] before the ratio, so an over- or under-count from a caller
// bug still returns a value inside the documented band rather than
// propagating out of range.
func fulltextRelevanceFromMatchedTerms(matchedTermCount, termCount int) float64 {
	if termCount <= 0 {
		return fulltextRelevanceFloor
	}
	if matchedTermCount < 0 {
		matchedTermCount = 0
	}
	if matchedTermCount > termCount {
		matchedTermCount = termCount
	}
	proportion := float64(matchedTermCount) / float64(termCount)
	return fulltextRelevanceFloor + (fulltextRelevanceCeiling-fulltextRelevanceFloor)*proportion
}

// isFulltextWordRune reports whether r counts as part of a "word" for
// LOCAL matched-term coverage purposes (fulltextWords/fulltextMatchedTermCount)
// -- every Unicode letter or digit, not merely ASCII a-z0-9. This is
// deliberately MORE aggressive than tokenizeForFulltext (which strips only
// RediSearch's own query-syntax punctuation, for building the literal
// query string RediSearch itself parses): Codex R2-2 found that
// tokenizeForFulltext's narrower strip set left an underscore, period, or
// unicode punctuation mark glued to its neighboring letters on ONE side of
// the local coverage comparison, so e.g. a query term "gateway" would never
// be found inside a candidate's "payment.gateway" even though both sides
// plainly share the word "gateway". unicode.IsLetter/IsDigit (not a
// hand-picked ASCII set) is what keeps a genuine non-ASCII word like "café"
// intact as one token while still treating a unicode punctuation mark as a
// separator.
func isFulltextWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// fulltextWords splits text into lowercased whole words using
// isFulltextWordRune, for LOCAL matched-term coverage purposes only --
// never for building the Cypher query string sent to FalkorDB (that stays
// tokenizeForFulltext, which must preserve RediSearch's own query-syntax
// meaning for characters like "|" and "@"). Applying this SAME, more
// aggressive splitter to BOTH sides of the comparison (the query's own
// words, via fulltextSearchNodes' matchTerms, and each candidate's
// search_text, via fulltextMatchedTermCount) is what makes the two sides
// symmetric.
func fulltextWords(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool { return !isFulltextWordRune(r) })
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		words = append(words, strings.ToLower(field))
	}
	return words
}

// fulltextMatchedTermCount reports how many of matchTerms appear as whole
// words in text, both sides already split with the identical
// fulltextWords splitter (matchTerms is fulltextWords(question) --
// see fulltextSearchNodes -- and text is tokenized again here with the
// same function) -- see fulltextRelevanceFromMatchedTerms' doc comment for
// why this is computed client-side from the candidate's own already-fetched
// search_text, rather than via additional per-term full-text queries
// against FalkorDB.
//
// Residual-divergence policy (Codex R2-2): with both sides parsed by the
// identical splitter, a literal word-for-word mismatch can only ever be a
// FALSE NEGATIVE (a real word that FalkorDB's own analyzer would recognize
// as equivalent under stemming/fuzzy matching -- e.g. "running" vs "run" --
// but this literal comparison does not), never a false positive: this
// function does not invent word boundaries or word equivalences the
// splitter itself did not produce, so it can never report a match neither
// side's tokenization actually contains. A false negative demotes a
// candidate's confidence (conservative -- the candidate can still surface
// as a lower-confidence result, just not an auto-committed one); it can
// never promote one. That asymmetry is the same one
// fulltextRelevanceFromMatchedTerms' clamping and the truncation handling
// in fulltextSearchNodes both lean on: every failure mode this adapter's
// confidence computation has is a failure toward LESS confidence, never
// more.
func fulltextMatchedTermCount(text string, matchTerms []string) int {
	words := make(map[string]struct{}, 8)
	for _, w := range fulltextWords(text) {
		words[w] = struct{}{}
	}
	matched := 0
	for _, term := range matchTerms {
		// fulltextWords already lowercases, but matchTerms is not always
		// its own output (fulltextSearchNodes' matchTerms is, but this
		// function is also called and unit-tested directly with raw-case
		// terms) -- lowercase defensively here too rather than relying on
		// every caller to have already normalized case.
		if _, ok := words[strings.ToLower(term)]; ok {
			matched++
		}
	}
	return matched
}

// fulltextSearchNodes runs a lexical full-text search over Subject nodes'
// search_text property, bounded strictly at `limit`, with NO CHAOS-3838
// lexicon expansion (codex round-6 P2, fix A): this is the PLAIN,
// pre-CHAOS-3838 contract, and DiscoverContext (reader.go) is its ONLY
// production caller. DiscoverContext calls edgesOfNode PER RETURNED NODE
// immediately after this returns, so this function's own result size is a
// direct multiplier on DiscoverContext's sequential graph-query cost --
// letting it silently grow beyond `limit` for a lexicon-matching question
// (e.g. one containing "pull request") would make that cost
// question-content-dependent in a way DiscoverContext's own caller never
// asked for or bounded against. See fulltextSearchNodesForResolution for
// the union/lexicon-expansion-aware counterpart, used ONLY by
// hybridSearchNodes' subject-RESOLUTION search (vector.go) -- the ONE path
// this ticket's "expansion only ever adds, graphrank's downstream
// authorization + final truncation is the one real cut" contract belongs
// to. Every OTHER production caller of a fulltextSearchNodes-family
// function must stay on THIS plain one; there are exactly two callers
// total (reader.go's DiscoverContext, vector.go's hybridSearchNodes) and
// this doc comment is the audit trail for that claim.
//
// Space in query is AND by RediSearch default (verified:
// docs/design/context-fabric-falkordb-adapter.md §6.1) -- a multi-word
// question passed as-is would almost always match nothing, so terms are
// joined with "|" (OR) instead. Field names in a full-text query must never
// come from caller text (an unrecognized @field: silently returns empty,
// no error) -- this function never emits one. Codex P2b/P2d: orgID is a
// mandatory predicate on every read, enforced by runFulltextQuery, the ONE
// shared query-building/row-conversion authority this function and
// fulltextSearchNodesForResolution both delegate to -- see that function's
// own doc comment for the truncation-sentinel (Codex R2-1) and
// matched-term-coverage confidence (D11 / AC-3778-0) rationale this
// function inherits unchanged.
func (a *Adapter) fulltextSearchNodes(ctx context.Context, key, orgID, text string, limit int, temporal temporalFilter) ([]graphrank.CandidateNode, bool, error) {
	terms := tokenizeForFulltext(text)
	if len(terms) == 0 {
		return nil, false, nil
	}
	if limit <= 0 || limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	matchTerms := fulltextWords(text)
	termCount := len(matchTerms)
	query := strings.Join(terms, "|")
	return a.runFulltextQuery(ctx, key, orgID, query, limit, temporal, matchTerms, termCount, "")
}

// fulltextSearchNodesForResolution is fulltextSearchNodes' CHAOS-3838 (spec
// L13) union/lexicon-expansion-aware counterpart -- codex round-6 P2, fix
// A: subject RESOLUTION's "over-return, let graphrank's downstream
// authorization + final truncation do the one real cut" contract is a
// SUBJECT-RESOLUTION-SPECIFIC feature, not a property of fulltextSearchNodes
// in general. vector.go's hybridSearchNodes (subject resolution, one call
// per interpreted term) is its ONLY caller; DiscoverContext stays on the
// plain fulltextSearchNodes above. See that function's own doc comment for
// why the split matters (DiscoverContext's per-node edgesOfNode cost).
//
// matchTerms/termCount anchor to the ORIGINAL text (CHAOS-3838 spec L13):
// confidence is a promise about how much of THIS query's own terms a
// candidate covers; widening the denominator with synonym words the caller
// never typed would dilute -- never improve -- the coverage ratio of every
// candidate that matches only the original term. Expansion is allowed to
// find MORE candidates; it must never change how confidently an
// already-findable one scores.
func (a *Adapter) fulltextSearchNodesForResolution(ctx context.Context, key, orgID, text string, limit int, temporal temporalFilter) ([]graphrank.CandidateNode, bool, error) {
	terms := tokenizeForFulltext(text)
	if len(terms) == 0 {
		return nil, false, nil
	}
	if limit <= 0 || limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	matchTerms := fulltextWords(text)
	termCount := len(matchTerms)

	// The BASE query, byte-identical to fulltextSearchNodes' own shape:
	// exactly `terms`, exactly `limit`, its own truncation sentinel, no
	// kind filter.
	baseQuery := strings.Join(terms, "|")
	baseCandidates, baseTruncated, err := a.runFulltextQuery(ctx, key, orgID, baseQuery, limit, temporal, matchTerms, termCount, "")
	if err != nil {
		return nil, false, err
	}

	additions := lexiconAdditions(text)
	if len(additions) == 0 {
		// The overwhelmingly common case: no lexicon phrase matched, so
		// this call never runs a second query at all -- same one round
		// trip as before this ticket.
		a.recordLexiconExpansion(ctx, orgID, false, 0, 0, false)
		return baseCandidates, baseTruncated, nil
	}

	// codex round-3 P2 (fix C) + round-4 P1 (fix A layer 1, kind-scoped
	// batches): a widened single query is NOT a union under a server-side
	// LIMIT -- synonym-heavy rows can out-rank and displace an
	// already-correct base hit past the limit+1 cutoff, so expansion could
	// REMOVE a previously-resolvable subject instead of only adding
	// candidates. Fix: base is never touched again -- every expansion BATCH
	// (the kind-agnostic additions, plus one per distinct targetKind --
	// lexiconExpansionBatches, in a FIXED deterministic order) runs as its
	// own SEPARATE query, deduplicated by subject UUID against everything
	// already collected.
	//
	// codex round-4 P2 (fix B): an earlier version of this union additionally
	// capped each batch's CONTRIBUTION to "however much of `limit` base
	// hadn't already used" -- computed from base's RAW candidate count,
	// BEFORE authorization. falkorgraph has no principal/scope here at all
	// (authorization is graphrank.NodeCandidate's job, one layer up, per
	// candidate); a base batch whose top rows are mostly UNAUTHORIZED for
	// THIS caller (a repo/project/team-scoped principal) would still count
	// as "using" that capacity here, silently discarding authorized
	// expansion rows that would otherwise have survived -- an emptied
	// resolution despite genuinely visible matches. This mirrors the SAME
	// class of mistake DiscoverContext's hopWalk/AdmitEdges doc comments
	// already name and reject ("collection is bounded by a generous
	// superset... NEVER by the final per-request admission budget... the
	// ONE and ONLY truncation happens after ranking"): the fix is to stop
	// trying to split a tight budget across sources HERE, before
	// authorization can even run, and instead let every source contribute
	// its own full, honestly-bounded (limit+1-sentinelled) set -- graphrank's
	// NodeCandidate filters unauthorized rows per candidate, and
	// ResolveFromMergedCandidates' own `max` truncation (request.Options.MaxSubjectCandidates)
	// is the ONE final cut, downstream, after both authorization AND
	// cross-source/cross-term ranking. This is strictly SAFER than the
	// capped version, never less correct: it can only ever surface a
	// genuine, authorized candidate the capped version would have dropped.
	// This over-return-then-cut-downstream contract is EXACTLY why this
	// logic lives in a separate function from fulltextSearchNodes -- codex
	// round-6 P2 fix A: DiscoverContext must never inherit it.
	//
	// codex round-5 P2 (fix), round-6 P2 fix B closes it fully: a batch's
	// OWN top-ranked rows can overlap heavily with candidates ALREADY
	// collected -- not just base, but any PRIOR batch's newly-added rows
	// too (a "pull request"-matching question can trigger more than one
	// lexicon group) -- and that batch's own limit+1 internal truncation
	// would discard genuinely NEW rows ranked just past the overlap BEFORE
	// this loop ever got a chance to dedup them out of the way. The
	// invariant this fetchBudget enforces: at the START of EACH batch's own
	// query, its fetch window must cover `limit` NOT-YET-SEEN rows if that
	// many exist, regardless of what already filled `seen` -- overlap with
	// anything already collected can never exceed len(seen) at that exact
	// moment (there is nothing else it could be overlap WITH), so
	// `limit + len(seen)` (before that batch's own +1 sentinel row)
	// guarantees it. seen grows incrementally as each batch's genuinely-new
	// rows are added below, so len(seen) is always THIS batch's own correct,
	// up-to-date budget input, not a value computed once up front.
	seen := make(map[string]bool, len(baseCandidates))
	for _, c := range baseCandidates {
		seen[c.UUID] = true
	}
	result := baseCandidates
	truncated := baseTruncated
	batches := lexiconExpansionBatches(additions)
	addedCandidates := 0
	for _, batch := range batches {
		fetchBudget := limit + len(seen)
		query := lexiconExpansionQuery(batch.additions)
		batchCandidates, batchTruncated, err := a.runFulltextQuery(ctx, key, orgID, query, fetchBudget, temporal, matchTerms, termCount, batch.kind)
		if err != nil {
			return nil, false, err
		}
		// batchTruncated is this batch's OWN (widened) limit+1 server-side
		// sentinel -- unaffected by authorization, which this layer has no
		// visibility into at all (same as the pre-CHAOS-3838 base query
		// always was).
		truncated = truncated || batchTruncated
		added := 0
		for _, c := range batchCandidates {
			if seen[c.UUID] {
				continue
			}
			if added >= limit {
				// More than `limit` genuinely NEW (not-yet-seen) candidates
				// existed in this batch's widened window -- this batch's
				// own natural per-source budget was exceeded, a real
				// competitor may have gone unshown.
				truncated = true
				continue
			}
			seen[c.UUID] = true
			result = append(result, c)
			added++
		}
		addedCandidates += added
	}
	// CHAOS-3890: fired/batch_count/added_candidates/truncated_by_expansion
	// were all computed above and previously discarded once this function
	// returned -- reported here instead of at every intermediate return so
	// exactly one event describes this call's WHOLE expansion outcome.
	// truncatedByExpansion isolates expansion's OWN contribution to
	// truncated: true only when THIS call's overall truncated flipped true
	// despite the base query alone NOT having truncated -- i.e. expansion
	// itself is the reason, not merely present alongside a base query that
	// was already truncated on its own.
	a.recordLexiconExpansion(ctx, orgID, true, len(batches), addedCandidates, truncated && !baseTruncated)
	return result, truncated, nil
}

// recordLexiconExpansion reports one fulltextSearchNodesForResolution call's
// CHAOS-3838 lexicon-expansion outcome to telemetry (CHAOS-3890) -- nil-safe,
// same convention as recordVectorDegraded/recordVectorSuppressed (vector.go).
func (a *Adapter) recordLexiconExpansion(ctx context.Context, orgID string, fired bool, batchCount, addedCandidates int, truncatedByExpansion bool) {
	if a.config.Telemetry == nil {
		return
	}
	a.config.Telemetry.RecordLexiconExpansion(ctx, orgID, fired, batchCount, addedCandidates, truncatedByExpansion)
}

// runFulltextQuery issues ONE RediSearch fulltext query and converts its
// rows into scored CandidateNode, applying the SAME limit+1 truncation
// sentinel and matched-term-coverage confidence math fulltextSearchNodes
// has always used (Codex R2-1 / D11 / AC-3778-0 -- see that function's own
// doc comment for the full rationale). Extracted so fulltextSearchNodes'
// base query and every lexicon-expansion batch (codex round-3 P2 / round-4
// P1, fix C / fix A) run through the exact same row-to-candidate
// conversion and can never independently drift on it -- the calls differ
// ONLY in which RediSearch query string, which optional kind filter, and
// which rows they see, never in how a row becomes a candidate.
//
// matchTerms/termCount are always the CALLER's original text's own words
// (fulltextSearchNodes' matchTerms), passed straight through regardless of
// which query string this particular call runs -- confidence stays a
// promise about the original text on every call, base or expansion alike.
//
// kindFilter, when non-empty, restricts matches to nodes of exactly that
// subject kind (codex round-4 P1, fix A layer 1) -- the mechanism a
// kind-scoped lexicon group (domainLexiconGroups' targetKind) uses to stay
// out of OTHER kinds' search_text entirely, closing the field-label
// collision class rather than merely demoting its score.
func (a *Adapter) runFulltextQuery(ctx context.Context, key, orgID, query string, limit int, temporal temporalFilter, matchTerms []string, termCount int, kindFilter contextfabric.SubjectKind) ([]graphrank.CandidateNode, bool, error) {
	kindPredicate := ""
	params := temporal.bind(map[string]interface{}{"query": query, "org": orgID})
	if kindFilter != "" {
		kindPredicate = fmt.Sprintf(" AND node.%s = $kind", propKind)
		params["kind"] = string(kindFilter)
	}
	// Codex R2-1: request one more row than the caller's budget so a
	// truncated result set can be told apart from a genuinely complete one
	// (see fulltextSearchNodes' doc comment).
	//
	// codex round-4 P2 (fix C): ORDER BY score alone lets FalkorDB break a
	// tie AT the LIMIT boundary arbitrarily -- verified live that two
	// otherwise-identical rows with equal scores are not guaranteed a
	// stable relative order across repeated calls, so a resolution sitting
	// exactly at the cutoff could return a DIFFERENT candidate/truncation
	// set for the SAME request. Two deterministic tie-break keys (subject
	// kind, then canonical id -- both always present, both already part of
	// every returned node) make the ORDER total, so a repeated identical
	// call always returns the identical result -- required for CHAOS-3782
	// answer reuse and for any measurement harness that expects
	// reproducible numbers. Applies to base and every expansion batch
	// alike, since they all share this one query-building authority.
	cypher := fmt.Sprintf(
		"CALL db.idx.fulltext.queryNodes('%s', $query) YIELD node, score "+
			"WHERE node.%s = $org%s%s "+
			"RETURN node, score ORDER BY score DESC, node.%s ASC, node.%s ASC LIMIT %d",
		labelSubject, propOrgID, kindPredicate, temporal.predicate("node"), propKind, propCanonicalID, limit+1,
	)
	rows, err := a.api.query(ctx, key, cypher, params, true)
	if err != nil {
		return nil, false, safeDependencyError("search context graph", err)
	}
	// truncated reports whether the corpus actually had MORE than `limit`
	// matches for THIS query -- never whether it merely equaled it.
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	candidates := make([]graphrank.CandidateNode, 0, len(rows))
	for _, row := range rows {
		n, ok := row["node"].(*node)
		if !ok || n == nil {
			continue
		}
		candidate := toCandidateNode(n)
		if score, ok := row["score"].(float64); ok {
			candidate.Score = &score
		}
		relevance := fulltextRelevanceFloor
		if !truncated {
			matched := fulltextMatchedTermCount(graphrank.StringAttribute(candidate.Attributes, propSearchText), matchTerms)
			relevance = fulltextRelevanceFromMatchedTerms(matched, termCount)
			// CHAOS-3858 measurement-only capture: the exact (matched,
			// termCount) pair the remap above just consumed, before it is
			// gone. See CandidateNode.LexicalMatchedTerms' doc comment --
			// never read by any confidence/decision path, only by an
			// optional RawSignalObserver.
			//
			// Gated on a.config.RawSignalObserver != nil (adversarial-review
			// finding): every production deployment leaves that nil, and
			// without this gate `matched`/`termCount` -- otherwise pure
			// stack locals consumed only by the remap above -- would escape
			// to the heap on EVERY non-truncated candidate (this function is
			// also DiscoverContext's shared authority, which never consumes
			// an observer at all), a real, unconditional per-candidate
			// allocation cost the "measurement-only, zero production cost"
			// design goal cannot pay. The two-word CandidateNode struct
			// widening this pair still costs is the same FIXED, already-
			// accepted precedent VectorSimilarity (CHAOS-3829) set; this
			// gate removes the ADDITIONAL heap-escape cost on top of it.
			if a.config.RawSignalObserver != nil {
				candidate.LexicalMatchedTerms = &matched
				candidate.LexicalTermCount = &termCount
			}
		}
		candidate.Relevance = graphrank.Normalized(relevance)
		candidates = append(candidates, candidate)
	}
	return candidates, truncated, nil
}

// lexiconExpansionBatch is one lexicon-expansion search: either the
// kind-agnostic additions (kind == "") or one distinct targetKind's own
// additions (codex round-4 P1, fix A layer 1).
type lexiconExpansionBatch struct {
	kind      contextfabric.SubjectKind
	additions []lexiconAddition
}

// lexiconExpansionBatches partitions additions into a FIXED, deterministic
// sequence of batches: the kind-agnostic batch first (if any present), then
// one batch per distinct targetKind present, sorted by kind name -- so two
// calls over the same additions always produce the same batch order, which
// fulltextSearchNodes' union step depends on for reproducible results
// (codex round-4 P2's determinism concern applies to this ordering too,
// not only to a single query's own ORDER BY).
func lexiconExpansionBatches(additions []lexiconAddition) []lexiconExpansionBatch {
	var global []lexiconAddition
	byKind := make(map[contextfabric.SubjectKind][]lexiconAddition)
	var kinds []string
	for _, addition := range additions {
		if addition.targetKind == "" {
			global = append(global, addition)
			continue
		}
		if _, exists := byKind[addition.targetKind]; !exists {
			kinds = append(kinds, string(addition.targetKind))
		}
		byKind[addition.targetKind] = append(byKind[addition.targetKind], addition)
	}
	sort.Strings(kinds)
	batches := make([]lexiconExpansionBatch, 0, 1+len(kinds))
	if len(global) > 0 {
		batches = append(batches, lexiconExpansionBatch{additions: global})
	}
	for _, kind := range kinds {
		k := contextfabric.SubjectKind(kind)
		batches = append(batches, lexiconExpansionBatch{kind: k, additions: byKind[k]})
	}
	return batches
}

// lexiconExpansionQuery builds the RediSearch query string for ONE
// lexicon-expansion batch's synonym additions (never re-including the
// caller's own terms -- fulltextSearchNodes' base query already covers
// those).
//
// codex round-3 P1 (fix B): a multi-word synonym ("pull request") must
// never be OR-tokenized into independent single-word disjuncts -- doing so
// let a candidate containing ONLY "request" (nothing about pull requests at
// all) read as a lexical hit for the "PR" group, purely because "request"
// happened to be one of the OR-joined words. A single-word synonym has no
// such hazard (the word itself IS the whole concept), so it stays an
// ordinary OR term via tokenizeForFulltext, identically to how a caller's
// own term is treated. A multi-word synonym instead becomes a RediSearch
// EXACT PHRASE clause (double-quoted), which requires the words to appear
// adjacently and in order -- injection-safe by construction, not merely by
// escaping: every element of additions is a domainLexiconGroups literal
// (compileLexicon's init-time validation panics if one ever contains a
// literal `"`), never caller-supplied text, so there is no untrusted value
// on this path for a quote to smuggle anything through.
func lexiconExpansionQuery(additions []lexiconAddition) string {
	parts := make([]string, 0, len(additions))
	for _, addition := range additions {
		if len(strings.Fields(addition.phrase)) <= 1 {
			parts = append(parts, tokenizeForFulltext(addition.phrase)...)
			continue
		}
		parts = append(parts, lexiconPhraseClause(addition.phrase))
	}
	return strings.Join(parts, "|")
}

// lexiconPhraseClause renders a multi-word lexicon phrase as a RediSearch
// exact-phrase clause. See lexiconExpansionQuery's doc comment for the
// injection-safety argument -- phrase MUST be a domainLexiconGroups
// literal, never caller text.
func lexiconPhraseClause(phrase string) string {
	return `"` + phrase + `"`
}

// tokenizeForFulltext splits free text into RediSearch-safe search terms.
// Punctuation that RediSearch's query syntax treats specially (the OR "|",
// fuzzy "%", field-scope "@", quotes) is stripped from each term rather than
// escaped, since a caller-typed question is untrusted input and this
// function's only job is producing a query that means "any of these words",
// never anything structurally richer.
//
// CHAOS-3827: stripping that narrow set is not sufficient on its own,
// because a separator can leave punctuation STANDING ALONE as a term of its
// own -- the trailing "?" of `... "Horizontal scaling readiness"?` once the
// quote it was glued to is stripped. fulltextSearchNodes OR-joins terms, and
// RediSearch rejects a bare punctuation element in that join as a SYNTAX
// ERROR rather than treating it as a term that simply matches nothing, so
// the whole search fails ("context fabric graph dependency error during
// search context graph") and subject resolution dies for that question.
// Live-verified against the dev graph: the query
// 'What|is|the|status|of|Horizontal|scaling|readiness|?' errors at offset
// 50, while the identical query without that last element returns 915
// nodes. Every bare punctuation rune probed behaves the same way in a join
// (`|.`, `|_`, `|#`, `|~`, ...), so the rule below is a rune CLASS -- "keep
// a term only if it carries a Unicode letter or digit" -- not a hand-picked
// metacharacter list, for the same reason isFulltextWordRune is one.
//
// A leading run of such runes is trimmed rather than kept for the same
// live-verified reason: `{foo`, `}foo`, `[foo`, `]foo`, `;foo` and `$foo`
// are syntax errors even with a real word attached, and `~foo` is worse than
// an error -- RediSearch reads the leading `~` as its own fuzzy/optional
// operator and silently matches 35987 nodes where the bare word matches 47.
//
// What deliberately does NOT change is a term whose punctuation TRAILS a
// real word ("readiness?", "acr`"): RediSearch accepts those and, live,
// 'What|...|readiness?' returns exactly the same 915 nodes as
// 'What|...|readiness'. Rewriting them would shift the candidate sets (and
// so the lexical relevance numbers) of every search that already works.
// hasLexicalContent reports whether text yields at least one search term --
// i.e. whether it carries anything a retrieval mechanism could act ON.
//
// This is the single predicate the hybrid path gates BOTH of its mechanisms
// on (see hybridSearchNodes): it delegates to tokenizeForFulltext, the same
// function whose result fulltextSearchNodes checks for emptiness, so the
// lexical arm and the vector arm can never disagree about whether a term
// means anything. Written as a named predicate rather than an inline length
// check precisely so the agreement is structural and a future edit to one
// arm cannot quietly desynchronize the other.
func hasLexicalContent(text string) bool {
	return len(tokenizeForFulltext(text)) > 0
}

func tokenizeForFulltext(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '|', '%', '@', '"', '\'', '*', '-', '(', ')', ':':
			return true
		// CHAOS-3827 residual: these six are live-verified as a RediSearch
		// syntax error in EVERY position -- leading-glued, trailing-glued
		// and bare alike (`{foo`/`foo{`, `[foo`/`foo[`, `;foo`/`foo;`, and
		// `~`, which errors trailing-glued and silently fuzzes
		// leading-glued). The leading trim below cannot reach the trailing
		// case, so a bracketed question still failed outright: `What is the
		// status of [Q3] readiness?` tokenized to
		// `What|is|the|status|of|Q3]|readiness?`, which the dev graph
		// rejects, versus 905 nodes for the same query with the bracket
		// gone. Because NO working query can contain any of these runes
		// anywhere, dropping them is error recovery only: it cannot change
		// the result set -- and so cannot change the lexical relevance -- of
		// any query that works today. That is what separates them from a
		// blanket trailing trim, which would rewrite the WORKING
		// "readiness?" (live: identical result set to the bare word) and is
		// therefore deliberately not done.
		case '{', '}', '[', ']', ';', '~':
			return true
		}
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		// Trimming the leading non-word run also drops a field that is
		// punctuation and nothing else: it trims away to empty, which is
		// exactly the "no letter and no digit" case.
		field = strings.TrimLeftFunc(field, func(r rune) bool { return !isFulltextWordRune(r) })
		if field != "" {
			terms = append(terms, field)
		}
	}
	return terms
}

// subjectUUID is the stable, portable identifier this adapter uses in place
// of a backend-internal ID for anything graphrank needs to key on
// (ReceiptID/PathID/DriverID derivation, dedup maps): kind and canonical_id
// joined with the same NUL separator subjectKey-shaped helpers use
// elsewhere in this repository. Deliberately NOT FalkorDB's own internal
// node ID (*node.ID, a uint64): that ID's value depends on insertion
// history, not on subject identity, so two different environments (or a
// replay after a rebuild) would derive different ReceiptIDs for the exact
// same canonical subject.
func subjectUUID(kind, canonicalID string) string {
	return kind + "\x00" + canonicalID
}

func splitSubjectUUID(uuid string) (kind, canonicalID string) {
	parts := strings.SplitN(uuid, "\x00", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// toCandidateNode converts a decoded FalkorDB node into graphrank's
// CandidateNode. Attributes pass through unchanged: this adapter already
// writes authorization_*/evidence_refs in graphrank's shared attribute-value
// convention at projection time (projection.go's authorizationValue),
// unlike zepgraph, which must translate its own pipe-encoded wire format on
// every read.
func toCandidateNode(n *node) graphrank.CandidateNode {
	if n == nil {
		return graphrank.CandidateNode{}
	}
	kind := propStringValue(n.Properties[propKind])
	canonicalID := propStringValue(n.Properties[propCanonicalID])
	return graphrank.CandidateNode{
		UUID: subjectUUID(kind, canonicalID), Name: propStringValue(n.Properties[propLabel]),
		Attributes: n.Properties,
	}
}

func (a *Adapter) nodeByUUID(ctx context.Context, key, orgID, uuid string, temporal temporalFilter) (*node, error) {
	kind, canonicalID := splitSubjectUUID(uuid)
	if kind == "" {
		return nil, nil
	}
	return a.nodeByKindID(ctx, key, orgID, kind, canonicalID, temporal)
}

// nodeByKindID looks up one Subject node by its natural key. Codex P2b: the
// org_id predicate is mandatory on every read query, not only the ones that
// happened to already carry it -- this is the standing review rule
// regardless of how strong graph-key tenancy isolation already is.
func (a *Adapter) nodeByKindID(ctx context.Context, key, orgID, kind, canonicalID string, temporal temporalFilter) (*node, error) {
	cypher := fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind, %s:$id}) WHERE true%s RETURN n",
		labelSubject, propOrgID, propKind, propCanonicalID, temporal.predicate("n"))
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "kind": kind, "id": canonicalID}), true)
	if err != nil {
		return nil, safeDependencyError("get node", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	n, _ := rows[0]["n"].(*node)
	return n, nil
}

// edgesOfNode returns every edge touching the subject identified by uuid, as
// graphrank.CandidateEdge (SourceNodeUUID/TargetNodeUUID in this adapter's
// subjectUUID form, Name set to the semantic relation type read back from
// the relation_type property -- the Cypher relationship type itself is
// always the generic labelRelation, so it carries no semantic information on
// its own). Used by ResolveSubjects' observation-to-entity traversal.
//
// Codex P2b: org_id is filtered on BOTH node patterns in the UNION (the
// origin subject `n` and the neighbor `other`) -- filtering only the origin
// side would still let a cross-tenant neighbor node leak into the result
// through a stray or corrupted edge.
//
// Codex P2a (round 2): the combined UNION result is wrapped in a `CALL {}`
// subquery so an outer `ORDER BY r.relationship_id ASC` can apply to it --
// verified live that a bare `ORDER BY` placed directly after a top-level
// `UNION` is silently NOT honored by this FalkorDB version (the combined
// rows come back in the union's own internal order regardless), while the
// exact same ORDER BY on a `CALL { <the union> } RETURN ...` wrapper sorts
// correctly. This makes edgesOfNode's own output deterministic by the same
// key graphrank's relevance tie-break uses (ascending relationship UUID) --
// see hopWalk's doc comment for why a real relevance-bearing property does
// not exist for a graph-walked edge in the first place, and why this
// property is the correct proxy for it.
func (a *Adapter) edgesOfNode(ctx context.Context, key, orgID, uuid string, temporal temporalFilter) ([]graphrank.CandidateEdge, error) {
	kind, canonicalID := splitSubjectUUID(uuid)
	if kind == "" {
		return nil, nil
	}
	// CHAOS-3781: the window is applied to the EDGE and to the NEIGHBOR
	// node, in both union arms. Filtering the edge alone would still walk
	// into a subject that did not exist at the requested time whenever a
	// window-less edge touched it.
	edgeWindow := temporal.predicate("r")
	neighborWindow := temporal.predicate("other")
	cypher := fmt.Sprintf(
		"CALL { "+
			"MATCH (n:%s {%s:$org, %s:$kind, %s:$id})-[r:%s]->(other:%s {%s:$org}) WHERE true%s%s RETURN r, %s AS srcKind, $id AS srcId, other.%s AS dstKind, other.%s AS dstId "+
			"UNION "+
			"MATCH (other:%s {%s:$org})-[r:%s]->(n:%s {%s:$org, %s:$kind, %s:$id}) WHERE true%s%s RETURN r, other.%s AS srcKind, other.%s AS srcId, %s AS dstKind, $id AS dstId "+
			"} RETURN r, srcKind, srcId, dstKind, dstId ORDER BY r.%s ASC",
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, labelSubject, propOrgID, edgeWindow, neighborWindow, "$kind", propKind, propCanonicalID,
		labelSubject, propOrgID, labelRelation, labelSubject, propOrgID, propKind, propCanonicalID, edgeWindow, neighborWindow, propKind, propCanonicalID, "$kind",
		propRelationshipID,
	)
	rows, err := a.api.query(ctx, key, cypher, temporal.bind(map[string]interface{}{"org": orgID, "kind": kind, "id": canonicalID}), true)
	if err != nil {
		return nil, safeDependencyError("get node edges", err)
	}
	edges := make([]graphrank.CandidateEdge, 0, len(rows))
	for _, row := range rows {
		e, ok := row["r"].(*edge)
		if !ok || e == nil {
			continue
		}
		srcKind, srcID := propStringValue(row["srcKind"]), propStringValue(row["srcId"])
		dstKind, dstID := propStringValue(row["dstKind"]), propStringValue(row["dstId"])
		edges = append(edges, toCandidateEdge(e, srcKind, srcID, dstKind, dstID))
	}
	return edges, nil
}

func toCandidateEdge(e *edge, srcKind, srcID, dstKind, dstID string) graphrank.CandidateEdge {
	relationType := propStringValue(e.Properties[propRelationType])
	fact := propStringValue(e.Properties["fact"])
	attrs := make(map[string]interface{}, len(e.Properties)+1)
	for k, v := range e.Properties {
		attrs[k] = v
	}
	return graphrank.CandidateEdge{
		UUID: propStringValue(e.Properties[propRelationshipID]), Name: relationType, Fact: fact,
		SourceNodeUUID: subjectUUID(srcKind, srcID), TargetNodeUUID: subjectUUID(dstKind, dstID),
		Attributes: attrs,
		CreatedAt:  propStringValue(e.Properties[propObservedAt]),
		ValidAt:    optionalString(e.Properties[propValidFrom]),
		InvalidAt:  optionalString(e.Properties[propValidTo]),
	}
}

func optionalString(value interface{}) *string {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	return &s
}

// edgeResolution classifies what resolveEdge did with one candidate edge,
// so a caller can tell a legitimate exclusion (unauthorized, or an endpoint
// that genuinely no longer exists) apart from a degraded one (a lookup that
// actually failed) -- Codex P2c: the two must never be reported identically,
// since collapsing them into one silent "not admitted" produced
// Coverage.Partial=false even when a real backend failure had quietly
// dropped material from the result.
type edgeResolution int

const (
	edgeAdmitted edgeResolution = iota
	edgeFiltered
	edgeLookupFailed
)

// edgeFilterReason (CHAOS-3888) classifies WHY resolveEdge returned
// edgeFiltered -- before this, an authorization denial, a historical-window
// exclusion, and (rarely) an unresolvable endpoint identity all collapsed
// into the same unclassified edgeFiltered value, indistinguishable from one
// another to any caller. Meaningful only alongside edgeFiltered; every other
// edgeResolution value pairs with edgeFilterReasonNone.
type edgeFilterReason int

const (
	edgeFilterReasonNone edgeFilterReason = iota
	// edgeFilterReasonAuthz: AuthorizedAttributes denied the edge itself or
	// one of its two resolved endpoints.
	edgeFilterReasonAuthz
	// edgeFilterReasonTemporalWindow: an endpoint resolved to nil under an
	// ACTIVE historical time filter (temporal.active) -- CHAOS-3781's "did
	// not exist at the requested time" exclusion, per resolveEdge's own
	// existing doc comment below.
	edgeFilterReasonTemporalWindow
	// edgeFilterReasonInvalidSubject: an endpoint's node resolved but its
	// UUID did not decode to a valid SubjectRef (graphrank.NodeSubject
	// returned false) -- malformed data, not an authorization or temporal
	// exclusion. Rare; not part of CHAOS-3888's three named counters, so
	// hopWalk/DiscoverContext do not tally it, but it is still named here
	// (rather than reusing edgeFilterReasonNone) so it is never
	// misclassified as one of the other two.
	edgeFilterReasonInvalidSubject
)

// edgeFilterCounts (CHAOS-3888) aggregates resolveEdge's edgeFilterReason
// across many calls -- hopWalk's own inner loop, and DiscoverContext's
// separate full-text-adjacent-edge loop, both accumulate into one of these
// before reporting through GraphTelemetry.RecordEdgesFilteredByReason.
// InvalidSubject is intentionally NOT a field here -- see
// edgeFilterReasonInvalidSubject's own doc comment for why it stays
// uncounted.
type edgeFilterCounts struct {
	Authz          int
	TemporalWindow int
}

// add folds one resolveEdge call's (resolution, reason) outcome into c.
// A no-op for edgeAdmitted/edgeLookupFailed (edgeFilterReasonNone) and for
// edgeFilterReasonInvalidSubject, which this type deliberately does not
// track.
func (c *edgeFilterCounts) add(resolution edgeResolution, reason edgeFilterReason) {
	if resolution != edgeFiltered {
		return
	}
	switch reason {
	case edgeFilterReasonAuthz:
		c.Authz++
	case edgeFilterReasonTemporalWindow:
		c.TemporalWindow++
	}
}

// resolveEdge converts a CandidateEdge (as returned by edgesOfNode) into a
// fully-resolved graphrank.ResolvedEdge by decoding its endpoint subjectUUIDs
// back into SubjectRefs via a node lookup for the label -- falkorgraph never
// needs a "second-hop verify" step the way zepgraph did (every lookup here
// is already structurally scoped to principal's own organization graph key),
// so this is a plain fetch, not a trust decision.
//
// Codex P1: authorization is checked here, on the edge's own attributes AND
// on both resolved endpoints' attributes, before the edge is ever handed to
// graphrank.AdmitEdges -- AdmitEdges itself applies no authorization check
// (it only excludes self-loops and internal-bookkeeping endpoints), so an
// unauthorized edge or an edge into/out of an unauthorized subject must
// never reach it. This mirrors zepgraph's DiscoverContext exactly:
// graphrank.AuthorizedAttributes gates the edge, then each endpoint,
// independently.
// Returns edgeFilterReasonNone alongside every non-edgeFiltered resolution
// (edgeAdmitted, edgeLookupFailed); the reason is only meaningful paired
// with edgeFiltered (CHAOS-3888) -- see edgeFilterReason's own doc comment
// for the vocabulary.
func (a *Adapter) resolveEdge(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, ce graphrank.CandidateEdge, temporal temporalFilter) (graphrank.ResolvedEdge, edgeResolution, edgeFilterReason) {
	if !graphrank.AuthorizedAttributes(principal, scope, ce.Attributes) {
		return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonAuthz
	}
	fromKind, fromID := splitSubjectUUID(ce.SourceNodeUUID)
	toKind, toID := splitSubjectUUID(ce.TargetNodeUUID)
	// CHAOS-3781: both endpoint lookups carry the same window, so an edge
	// whose endpoint did not exist at the requested time resolves to nil
	// and is FILTERED, not reported as a lookup failure -- an endpoint
	// outside the window is a legitimate exclusion, exactly like an
	// endpoint that no longer exists, and must not inflate
	// Coverage.Partial.
	//
	// CHAOS-3888: a nil endpoint is classified edgeFilterReasonTemporalWindow
	// only when temporal.active -- under a current-time read (no window in
	// effect at all) a nil endpoint just means the node does not exist, which
	// is not a temporal exclusion and must not be misreported as one.
	fromNode, err := a.nodeByKindID(ctx, key, orgID, fromKind, fromID, temporal)
	if err != nil {
		return graphrank.ResolvedEdge{}, edgeLookupFailed, edgeFilterReasonNone
	}
	if fromNode == nil {
		if temporal.active {
			return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonTemporalWindow
		}
		return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonNone
	}
	toNode, err := a.nodeByKindID(ctx, key, orgID, toKind, toID, temporal)
	if err != nil {
		return graphrank.ResolvedEdge{}, edgeLookupFailed, edgeFilterReasonNone
	}
	if toNode == nil {
		if temporal.active {
			return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonTemporalWindow
		}
		return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonNone
	}
	fromCandidate := toCandidateNode(fromNode)
	toCandidate := toCandidateNode(toNode)
	if !graphrank.AuthorizedAttributes(principal, scope, fromCandidate.Attributes) || !graphrank.AuthorizedAttributes(principal, scope, toCandidate.Attributes) {
		return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonAuthz
	}
	fromSubject, ok := graphrank.NodeSubject(fromCandidate)
	if !ok {
		return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonInvalidSubject
	}
	toSubject, ok := graphrank.NodeSubject(toCandidate)
	if !ok {
		return graphrank.ResolvedEdge{}, edgeFiltered, edgeFilterReasonInvalidSubject
	}
	return graphrank.ResolvedEdge{
		UUID: ce.UUID, Name: ce.Name, Fact: ce.Fact, From: fromSubject, To: toSubject,
		Relevance: ce.Relevance, Score: ce.Score, Attributes: ce.Attributes,
		CreatedAt: ce.CreatedAt, ValidAt: ce.ValidAt, InvalidAt: ce.InvalidAt, ExpiredAt: ce.ExpiredAt,
	}, edgeAdmitted, edgeFilterReasonNone
}

// rankCandidateEdges sorts edges by graphrank's own relevance tie-break.
// Callers walk the FULL ranked result in order, resolving/admitting until
// their own budget fills or the ranked list is exhausted -- never trim this
// return value by position before resolution.
//
// Codex P2a (round 2): a collection-side truncation decision must always
// operate on a RANKED set, never on whatever order a query happened to
// return -- collecting per-node/per-hop bounded but rank-aware, per the
// review's own alternative to a single fully-global pre-collection sort
// (which is not expressible as one Cypher pass across multiple frontier
// nodes and, per edgesOfNode's doc comment, is not even reliably honored by
// this FalkorDB version across a single UNION without the CALL{} wrapper).
//
// Codex P2a (round 3): an earlier version of this function additionally
// capped the ranked list to a generous multiple of the remaining budget
// BEFORE resolution, reasoning that a filtered/unresolvable candidate
// "doesn't consume budget so it can't starve a real contender" -- true for
// the ADMISSION budget, false for the PREFIX the cap itself imposed: a long
// enough run of filtered candidates (longer than the multiplier headroom)
// still pushed a genuinely admissible, lower-ranked-only-by-tie-break edge
// past the cap before resolution ever got to attempt it. There is no
// bound-safe prefix length to cap at, because "how many leading candidates
// will turn out filtered" is not knowable in advance. The candidate list is
// already bounded by the per-hop collection limit and already fully in
// memory, so walking all of it in ranked order until the admission budget
// fills (or candidates exhaust) adds no unbounded work of its own -- the
// prefix cap only ever saved resolution round-trips, at the cost of
// correctness. If resolution-call cost genuinely needs a bound, it must be
// on ATTEMPTED RESOLUTIONS OF UNFILTERED CANDIDATES (a count each caller
// already increments as it walks), never on ranked-list position.
func rankCandidateEdges(edges []graphrank.CandidateEdge) []graphrank.CandidateEdge {
	return graphrank.SortEdgesByRelevance(edges)
}

// hopWalk performs a bounded N-hop traversal from one origin subject,
// returning every neighbor node and resolved edge reached, plus a count of
// edges/nodes dropped due to a genuine lookup failure (Codex P2c -- see
// edgeResolution), plus (CHAOS-3888) an edgeFilterCounts breakdown of every
// edge resolveEdge excluded as edgeFiltered, by reason.
//
// Codex P2a: walk collection is bounded by a generous superset limit (the
// caller passes a.config.MaxResults, not request.Options.MaxRelationshipPaths),
// never by the final per-request path limit -- truncating to
// MaxRelationshipPaths during collection, before graphrank.SortEdgesByRelevance
// and graphrank.AdmitEdges ever see the full candidate set, could silently
// discard a higher-relevance edge in favor of one merely reached first. Final
// truncation to MaxRelationshipPaths happens exactly once, inside AdmitEdges,
// after ranking.
//
// Codex P2a (round 2): that alone was not enough -- collection could still
// exhaust collectLimit itself in arrival order (whichever frontier node was
// processed first, whichever row a query happened to return first) before a
// higher-ranked edge discovered later ever got a chance to compete for the
// budget. Every graph-walked edge ties at ResultConfidence=0 (there is no
// real relevance signal for a hop-walked edge the way a full-text search
// score is one), so graphrank.SortEdgesByRelevance's own deterministic
// tie-break -- ascending relationship UUID -- IS the correct admission
// order, not an approximation of one; this function now collects an ENTIRE
// hop's candidate edges from every frontier node before making ANY
// truncation decision, ranks that full set with the exact same tie-break,
// and only then resolves/admits edges in ranked order up to the remaining
// budget (rankCandidateEdges). A candidate that gets
// authorization-filtered or fails to resolve does not consume budget, so a
// lower-ranked-but-admissible edge is never starved by a higher-ranked one
// that turned out to be unauthorized.
// TRUNCATION (CHAOS-5168, r1 finding 1). The fifth return reports that this
// walk stopped at collectLimit with candidate edges still unexamined.
//
// It matters to the COHORT, not only to the edge set, and that is not
// obvious: neighbours are discovered ONLY through edges this walk admits
// (see the neighbour loop below), so an edge dropped at the cap takes its
// endpoint with it -- a kind-matching, authorized subject that never reaches
// `visited`, never reaches DiscoverContext's cohortNodes, and therefore
// never becomes a cohort member. With the retained member count still under
// MaxCohortMembers, DiscoveredCohort has nothing to derive incompleteness
// from and reports Complete=true over a pool this function clipped.
//
// Reported rather than acted on here: this function stays a retrieval
// primitive, and the caller is the layer that knows whether an exhaustive
// census also ran and covered the loss.
func (a *Adapter) hopWalk(ctx context.Context, key, orgID string, principal storage.Principal, scope contextfabric.RequestedScope, origin contextfabric.SubjectRef, maxHops, collectLimit int, temporal temporalFilter) ([]graphrank.CandidateNode, []graphrank.ResolvedEdge, int, edgeFilterCounts, bool, error) {
	originUUID := subjectUUID(string(origin.Kind), origin.CanonicalID)
	visited := make(map[string]graphrank.CandidateNode)
	var edges []graphrank.ResolvedEdge
	seenEdge := make(map[string]bool)
	failedLookups := 0
	var filterCounts edgeFilterCounts
	frontier := []string{originUUID}
	truncated := false
	for hop := 0; hop < maxHops && len(frontier) > 0 && (collectLimit <= 0 || len(edges) < collectLimit); hop++ {
		var hopCandidates []graphrank.CandidateEdge
		for _, uuid := range frontier {
			candidateEdges, err := a.edgesOfNode(ctx, key, orgID, uuid, temporal)
			if err != nil {
				return nil, nil, failedLookups, filterCounts, false, err
			}
			for _, ce := range candidateEdges {
				if seenEdge[ce.UUID] {
					continue
				}
				seenEdge[ce.UUID] = true
				hopCandidates = append(hopCandidates, ce)
			}
		}
		if len(hopCandidates) == 0 {
			frontier = nil
			continue
		}
		var next []string
		for _, ce := range rankCandidateEdges(hopCandidates) {
			if collectLimit > 0 && len(edges) >= collectLimit {
				// Candidate edges remain and the budget is spent: this walk
				// is returning less than it found, and the endpoints of
				// everything past here are lost to the cohort.
				truncated = true
				break
			}
			resolved, resolution, reason := a.resolveEdge(ctx, key, orgID, principal, scope, ce, temporal)
			filterCounts.add(resolution, reason)
			switch resolution {
			case edgeLookupFailed:
				failedLookups++
				continue
			case edgeFiltered:
				continue
			}
			edges = append(edges, resolved)
			for _, neighbor := range []string{ce.SourceNodeUUID, ce.TargetNodeUUID} {
				if neighbor == originUUID {
					continue
				}
				if _, ok := visited[neighbor]; ok {
					continue
				}
				// Codex P2c (round 2): a genuine lookup failure here was
				// previously indistinguishable from a legitimate "this
				// neighbor no longer exists" -- both fell through the same
				// `continue`, so a real backend failure reached only through
				// this bookkeeping fetch (the edge/path it belonged to was
				// already admitted via resolveEdge above) never surfaced as
				// Coverage.Partial. Only err != nil is a failure; n == nil
				// with no error is a legitimate miss.
				n, err := a.nodeByUUID(ctx, key, orgID, neighbor, temporal)
				if err != nil {
					failedLookups++
					continue
				}
				if n == nil {
					continue
				}
				candidate := toCandidateNode(n)
				visited[neighbor] = candidate
				next = append(next, neighbor)
			}
		}
		frontier = next
	}
	nodes := make([]graphrank.CandidateNode, 0, len(visited))
	for _, n := range visited {
		nodes = append(nodes, n)
	}
	// CHAOS-4630: visited is a Go map -- ranging it (immediately above)
	// yields a randomized order per call, not merely an unspecified-but-
	// stable one. Every consumer of this traversal (DiscoverContext's
	// resolvedNodes/cohortNodes, and through it graphrank.DiscoveredCohort's
	// positional Rank and cap truncation) is downstream of this ONE
	// materialization point, so sorting here -- on the same total,
	// tie-break-safe key sortCandidateNodesBySubjectKey already uses for
	// reader.go's exact-name census (the other node source DiscoveredCohort
	// reads) -- makes every consumer stable without re-fixing this
	// separately at each call site. An arbitrary-but-stable lexical order is
	// deliberately not "the more meaningful" order (hop distance, edge
	// count, ...) -- the ticket's own minimal-fix direction: correctness
	// (reproducibility once the caller's cap makes order outcome-affecting)
	// comes first, and a richer order can replace this key later without
	// touching any consumer's contract.
	// THE SECOND EXIT (CHAOS-5168 r2 finding 1). The flag above is set at the
	// INNER break, which fires only when a hop still has candidates after the
	// budget is spent. The OUTER loop has its own budget exit
	// (`len(edges) < collectLimit`), and a hop that admits EXACTLY
	// collectLimit edges and exhausts its candidate list reaches it with the
	// inner break never taken -- leaving an unvisited frontier and, before
	// this, truncated=false. A cohort built from that pool then claimed
	// completeness over subjects the walk had stopped short of.
	//
	// BUDGET, NOT DEPTH. A spent collect budget with an unvisited frontier
	// CLIPS the pool and is truncation; the maxHops bound DEFINES the pool
	// (this walk is two hops by construction, and what lies beyond is not
	// missing, it is out of scope) and is not. When both bind at once this
	// discloses, because the budget's loss is real whether or not the depth
	// bound would also have stopped the walk.
	if collectLimit > 0 && len(edges) >= collectLimit && len(frontier) > 0 {
		truncated = true
	}
	sortCandidateNodesBySubjectKey(nodes)
	return nodes, edges, failedLookups, filterCounts, truncated, nil
}
