package falkorgraph

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// kindScopedFulltextSearchNodes is CHAOS-4038's graphrank.ResolveDeps.
// SearchKind implementation: the SAME lexical retrieval fulltextSearchNodes
// already runs (tokenize -> runFulltextQuery), constrained to exactly one
// subject kind via runFulltextQuery's existing kindFilter parameter
// (queries.go) -- the SAME mechanism CHAOS-3838's own kind-scoped
// lexicon-expansion batches (fulltextSearchNodesForResolution) already use,
// applied here as a dedicated per-kind coverage query rather than a
// synonym-triggered batch. codex CHAOS-4038 review round 2, finding 2: this
// ALSO widens with the domain lexicon (CHAOS-3838), scoped to kind -- an
// earlier version searched only the caller's literal term text, so a
// candidate discoverable ONLY through a lexicon synonym (e.g. "PR" ->
// "pull request") would be missed by the coverage floor even though the
// ordinary per-term Search pass (fulltextSearchNodesForResolution) would
// have found it. See lexiconAdditionsForKind's own doc comment for the
// kind-relevance filter this applies before widening.
//
// Deliberately lexical-only, with no vector-arm counterpart in this ticket:
// db.idx.vector.queryNodes has no per-kind predicate today (vector.go's own
// doc comment on THE ORG PREDICATE IS A POST-FILTER explains why any
// post-filter here would need a calibrated over-fetch depth to reliably
// surface a rare kind), and this ticket's own scope is build+unit-tests-now,
// live-measurement-deferred -- calibrating a new over-fetch depth blind,
// with no live corpus to validate against, would be exactly the kind of
// guess this repo's own CHAOS-3834/CHAOS-3829 calibration discipline
// rejects. A kind-scoped vector arm is the natural, flagged follow-up once
// a live measurement slot is available; see graphrank.ResolveDeps.SearchKind
// and CHAOS-4038's own PR description for this same note.
//
// degraded always reports false: unlike the vector arm, there is no
// "mechanism was expected but unavailable" case here -- a lexical query
// either runs or (blank/no-token term) has nothing to run against, mirroring
// fulltextSearchNodes' own behavior exactly.
func (a *Adapter) kindScopedFulltextSearchNodes(ctx context.Context, key, orgID, term string, kind contextfabric.SubjectKind, limit int, temporal temporalFilter) ([]graphrank.CandidateNode, bool, bool, error) {
	terms := tokenizeForFulltext(term)
	if len(terms) == 0 {
		return nil, false, false, nil
	}
	if limit <= 0 || limit > a.config.MaxResults {
		limit = a.config.MaxResults
	}
	matchTerms := fulltextWords(term)
	termCount := len(matchTerms)
	baseQuery := strings.Join(terms, "|")
	candidates, truncated, err := a.runFulltextQuery(ctx, key, orgID, baseQuery, limit, temporal, matchTerms, termCount, kind)
	if err != nil {
		return nil, false, false, err
	}
	// codex CHAOS-4038 review round 1, finding 2: runFulltextQuery itself
	// never sets CandidateNode.Mechanism -- every OTHER lexical caller
	// stamps it explicitly after the call (hybridSearchNodes does this for
	// its own lexical arm, vector.go). Omitting it here left a real Falkor
	// hit with an empty MatchMechanisms after mergeSearchResults/
	// NodeCandidate, unlike an ordinary per-term lexical find -- silently
	// breaking mechanism provenance/telemetry and corroboration for every
	// coverage-floor-sourced candidate.
	for i := range candidates {
		candidates[i].Mechanism = contextfabric.MatchLexical
	}

	relevant := lexiconAdditionsForKind(lexiconAdditions(term), kind)
	if len(relevant) == 0 {
		// The overwhelming common case: no lexicon phrase relevant to this
		// kind matched, so this call never runs a second query at all --
		// same one round trip as fulltextSearchNodesForResolution's own
		// fast path.
		a.recordLexiconExpansion(ctx, orgID, false, 0, 0, false)
		return candidates, truncated, false, nil
	}
	baseTruncated := truncated
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		seen[c.UUID] = true
	}
	// fetchBudget mirrors fulltextSearchNodesForResolution's own invariant
	// (queries.go): this batch's own fetch window must cover `limit`
	// NOT-YET-SEEN rows, regardless of what the base query already filled
	// `seen` with.
	fetchBudget := limit + len(seen)
	expansionQuery := lexiconExpansionQuery(relevant)
	expanded, expansionTruncated, err := a.runFulltextQuery(ctx, key, orgID, expansionQuery, fetchBudget, temporal, matchTerms, termCount, kind)
	if err != nil {
		return nil, false, false, err
	}
	added := 0
	for _, c := range expanded {
		if seen[c.UUID] {
			continue
		}
		if added >= limit {
			// More than `limit` genuinely NEW (not-yet-seen) candidates
			// existed in the expansion's own widened window -- this call's
			// own natural budget was exceeded, a real competitor may have
			// gone unshown.
			truncated = true
			continue
		}
		seen[c.UUID] = true
		c.Mechanism = contextfabric.MatchLexical
		candidates = append(candidates, c)
		added++
	}
	truncated = truncated || expansionTruncated
	// truncatedByExpansion isolates the expansion's OWN contribution:
	// true only when THIS call's overall truncated flipped true despite the
	// base query alone NOT having truncated -- mirrors
	// fulltextSearchNodesForResolution's own identical `truncated &&
	// !baseTruncated` computation (queries.go), computed here BEFORE
	// truncated could be re-mutated any further.
	a.recordLexiconExpansion(ctx, orgID, true, 1, added, truncated && !baseTruncated)
	return candidates, truncated, false, nil
}

// lexiconAdditionsForKind filters additions (lexiconAdditions' own matched
// domain-lexicon phrases) down to those relevant when a search is already
// scoped to exactly kind via runFulltextQuery's kindFilter -- kind-agnostic
// entries (targetKind == "", applies to every kind) plus any entry whose
// own targetKind equals kind. An entry scoped to a DIFFERENT kind is
// dropped: a node of that different kind could never survive this call's
// own kindFilter anyway, so widening the query with its phrase would only
// waste a round trip.
func lexiconAdditionsForKind(additions []lexiconAddition, kind contextfabric.SubjectKind) []lexiconAddition {
	var relevant []lexiconAddition
	for _, addition := range additions {
		if addition.targetKind == "" || addition.targetKind == kind {
			relevant = append(relevant, addition)
		}
	}
	return relevant
}
