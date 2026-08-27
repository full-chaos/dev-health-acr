package graphrank

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4348: repository/project/team subjects were found live and
// reproducibly UNREACHABLE via ordinary retrieval on a real corpus (kiac
// org 70d529e0: project 0/20, team 0/2 expected_subject_in_pool), despite
// well-formed graph data (nodes, edges, embeddings all present and correct
// -- CHAOS-4099/CHAOS-4108/CHAOS-4109's own project/team projection work).
// Root cause, in order of contribution:
//
//  1. Ordinary Search/SearchQuestion run UNSCOPED across the whole node
//     population. repository/project/team are numerically rare (kiac: 11 +
//     20 + 3 = 34 nodes) against ci_pipeline_run/work_item/pull_request
//     (kiac: ~34,000+) -- a project or team's own distinctive tokens are
//     routinely outscored by unrelated nodes that merely share a token
//     (live-verified: kiac's "chaos-ops" gitlab project, crowded out of the
//     top 15 unscoped fulltext results entirely by pull_request/deployment/
//     pull_request_review nodes containing "chaos" or "ops" -- this
//     organization's Linear ticket prefix, CHAOS-nnnn, is itself the single
//     most common token in the corpus, making the crowding structurally
//     worse for exactly these three kinds).
//  2. The one mechanism that DOES run kind-scoped and reliably finds them --
//     CHAOS-4038's coverage floor (applyKindCoverageFloor,
//     chaos4038_kind_coverage.go) -- is architecturally barred from ever
//     landing a repository/project/team find in the real pool: CHAOS-4271
//     codex round 1 (HIGH, BLOCK) ruled that unsafe (an offer-only find
//     could otherwise trip identityCollision against an unrelated
//     candidate's OWN exact match), so these three kinds' floor finds merge
//     into a private offerOnlyPool instead -- visible to kindOfferMaterial's
//     clarification offers, invisible to candidatesBySubject, corroboration,
//     and commit.
//  3. Observation-traversal (TraverseObservationToSubject, traverse.go) does
//     not help either: IsObservationAttributionRelation is a closed,
//     2-member set (DOCUMENTED_BY, HAS_EPISODE) with nothing to do with
//     BELONGS_TO_PROJECT/OWNED_BY_TEAM, so a work item or PR that mentions
//     its project/team never traverses back to it.
//
// Ruled (team-lead/chris, CHAOS-4348): (1) alone (landing coverage-floor
// finds in the real pool) is REJECTED -- it reopens the exact CHAOS-4271
// identity-collision hazard the offer-only split exists to avoid. Fix is
// two DIFFERENT, narrower mechanisms, both scoped to
// isAliasLookupScopedKind (repository/project/team) ONLY -- work_item/
// pull_request/ci_run/pull_request_review are untouched by this file:
//
//   - applyKindHintedPoolSearch: SearchKind's EXISTING kind-filtered fulltext
//     query, called from here (not the coverage floor) and merged into the
//     REAL pool, but ONLY when the request already carries a kind hint --
//     explicit ExpectedKinds, an interpreter-inferred kind from the
//     question's own text, or a confirmed prior-kind receipt. No hint, no
//     call, no ranking change for any other kind -- see
//     TestApplyKindHintedPoolSearch_NoHintProducesByteIdenticalPool.
//   - applyExactNameArm: an always-on, unranked equality scan (via
//     deps.ExactNameCandidates) reusing NodeCandidate's OWN existing exact-
//     match check -- see that hook's doc comment (resolve.go) for why an
//     equality scan, not another ranked query, is what a token-crowded
//     fulltext index cannot reliably substitute for.
//
// Neither mechanism reaches observation-traversal (item 3 above) --
// deliberately out of scope for this change; BELONGS_TO_PROJECT/
// OWNED_BY_TEAM traversal is a follow-up (see this ticket's own comment).

// hintedPoolKinds returns the isAliasLookupScopedKind members this specific
// resolution has a KIND HINT for, from the three sources CHAOS-4348 was
// ruled to trust: an explicit request.ExpectedKinds (client-declared,
// closed-vocabulary), a confirmed prior-kind receipt (confirmedKind --
// ConfirmedExpectedKind's own doc comment: only canonicalizeStructure's
// receipt-confirmation path may construct one, so this is never
// interpreter-inferred), and inferredKindHints (this resolution's own
// interpreted text). A kind outside isAliasLookupScopedKind is never
// returned -- this function exists ONLY to gate the CHAOS-4348 pool-search
// arm, never a general-purpose kind-hint reader.
func hintedPoolKinds(request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, confirmedKind *contextfabric.ConfirmedExpectedKind) []contextfabric.SubjectKind {
	seen := make(map[contextfabric.SubjectKind]bool, 3)
	var hints []contextfabric.SubjectKind
	add := func(kind contextfabric.SubjectKind) {
		if !isAliasLookupScopedKind(kind) || seen[kind] {
			return
		}
		seen[kind] = true
		hints = append(hints, kind)
	}
	for _, kind := range request.ExpectedKinds {
		add(contextfabric.SubjectKind(kind))
	}
	if confirmedKind != nil {
		add(contextfabric.SubjectKind(confirmedKind.Kind))
	}
	for _, kind := range inferredKindHints(interpreted) {
		add(kind)
	}
	return hints
}

// inferredKindHints reads the SAME two model-authored fields
// interpretedCohortKind (discover.go) already trusts for this exact
// question -- RequestedJudgment and SubjectTerms -- for a keyword hint
// toward repository/project/team. Deliberately NOT interpretedCohortKind
// itself: that function always returns exactly one kind (defaulting to
// SubjectTeam when neither keyword set matches), a single-subject
// cohort-discovery convention wrong for a hint SOURCE, which must be able
// to return NOTHING when the text gives no real signal -- a silent
// default here would turn every kindless question into a spurious team
// hint and defeat CHAOS-4348's own "no hint, no call" byte-identical
// requirement (TestApplyKindHintedPoolSearch_NoHintProducesByteIdenticalPool).
// Order is fixed (repository, project, team) so a caller iterating the
// result never depends on Go's randomized map order.
func inferredKindHints(interpreted contextfabric.InterpretedQuestion) []contextfabric.SubjectKind {
	values := make([]string, 0, len(interpreted.SubjectTerms)+1)
	values = append(values, interpreted.RequestedJudgment)
	values = append(values, interpreted.SubjectTerms...)
	var repo, project, team bool
	for _, value := range values {
		lower := strings.ToLower(value)
		if strings.Contains(lower, "repo") {
			repo = true
		}
		if strings.Contains(lower, "project") || strings.Contains(lower, "initiative") {
			project = true
		}
		if strings.Contains(lower, "team") || strings.Contains(lower, "group") {
			team = true
		}
	}
	var hints []contextfabric.SubjectKind
	if repo {
		hints = append(hints, contextfabric.SubjectRepository)
	}
	if project {
		hints = append(hints, contextfabric.SubjectProject)
	}
	if team {
		hints = append(hints, contextfabric.SubjectTeam)
	}
	return hints
}

// applyKindHintedPoolSearch runs deps.SearchKind for every hinted kind
// (hintedPoolKinds), merging results into the REAL pool -- candidatesBySubject,
// with real identity/identityTerms tracking, exactly like the ordinary
// per-term Search loop -- rather than applyKindCoverageFloor's private
// offerOnlyPool. This is the one deliberate behavioral difference from the
// coverage floor's own SearchKind usage: a HINT is caller/interpreter
// intent, not a blind lexical guess, so CHAOS-4271's identity-collision
// concern (an unrelated candidate's exact match getting suppressed by a
// low-confidence, unsolicited floor find) does not apply here -- the caller
// already told this resolution it expects this kind.
//
// Bounded exactly like applyKindCoverageFloor's own SearchKind usage
// (kindCoverageQueryLimit rows, kindCoverageMaxTermsPerKind terms, stopping
// once a kind is satisfied) -- the SAME dial, deliberately, not a second one
// this ticket would need to separately calibrate.
func applyKindHintedPoolSearch(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, pool map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, identity identityClaimants, identityTerms identityMatchTerms, hinted []contextfabric.SubjectKind) (traversalDegraded int, authzDropped int, truncated bool, degraded bool, err error) {
	if deps.SearchKind == nil || len(hinted) == 0 {
		return 0, 0, false, false, nil
	}
	boundedTerms := terms
	if len(boundedTerms) > kindCoverageMaxTermsPerKind {
		boundedTerms = boundedTerms[:kindCoverageMaxTermsPerKind]
	}
	for _, kind := range hinted {
		if poolHasKind(pool, kind) {
			continue
		}
		for _, term := range boundedTerms {
			results, kindTruncated, kindDegraded, searchErr := deps.SearchKind(ctx, term, kind, kindCoverageQueryLimit)
			if searchErr != nil {
				return traversalDegraded, authzDropped, truncated, degraded, searchErr
			}
			if kindTruncated {
				truncated = true
			}
			if kindDegraded {
				degraded = true
			}
			for i := range results {
				results[i].Mechanism = contextfabric.MatchLexical
			}
			traceRetrievalSource(deps, request.RequestID, "kind_hint_search", term, results)
			termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, results, pool, observationParentKey, observationBlocked, true, nil, identity, identityTerms)
			traversalDegraded += termTraversalDegraded
			authzDropped += termAuthzDropped
			if poolHasKind(pool, kind) {
				break
			}
		}
	}
	return traversalDegraded, authzDropped, truncated, degraded, nil
}

// applyExactNameArm fetches deps.ExactNameCandidates ONCE (nil-safe: a
// backend without the hook is a no-op, same convention every other optional
// ResolveDeps field uses), then for every one of this resolution's OWN
// terms, filters to nodes whose label/alias/provider-alias equals that term
// exactly (exactNameMatches -- the SAME check NodeCandidate independently
// re-derives, so a match here is guaranteed to also be a match there; this
// function only decides WHICH NODES REACH NodeCandidate, never re-implements
// its verdict) and merges those into the real pool, always on, with real
// identity/identityTerms tracking so identityCollision covers a same-term
// multi-claimant exactly like every other exact-match path already does.
func applyExactNameArm(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, pool map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, identity identityClaimants, identityTerms identityMatchTerms) (traversalDegraded int, authzDropped int, err error) {
	if deps.ExactNameCandidates == nil {
		return 0, 0, nil
	}
	candidates, fetchErr := deps.ExactNameCandidates(ctx)
	if fetchErr != nil {
		return 0, 0, fetchErr
	}
	if len(candidates) == 0 {
		return 0, 0, nil
	}
	for _, term := range terms {
		matches := exactNameMatches(term, candidates)
		if len(matches) == 0 {
			continue
		}
		for i := range matches {
			matches[i].Mechanism = contextfabric.MatchLexical
		}
		traceRetrievalSource(deps, request.RequestID, "exact_name_search", term, matches)
		termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, matches, pool, observationParentKey, observationBlocked, true, nil, identity, identityTerms)
		traversalDegraded += termTraversalDegraded
		authzDropped += termAuthzDropped
	}
	return traversalDegraded, authzDropped, nil
}

// traceRetrievalSource emits one ResolutionTraceEvent per matched node, tagged
// with the passed Stage ("kind_hint_search" or "exact_name_search") and the
// node's own Subject -- the ONLY signal a reader needs to compute the
// CHAOS-4348 report's per-subject retrieval-source tag
// (ordinary/kind_scoped/exact_name): a subject that reaches "corroboration"
// (i.e. IS in the real pool) with a preceding kind_hint_search/
// exact_name_search event for the SAME Subject was NOT reachable via
// ordinary Search/SearchQuestion/AliasLookup alone. No new struct field on
// ResolutionTraceEvent or SubjectCandidate -- Stage and Subject already
// exist and are already read this way for every other stage. A nil tracer
// (the overwhelming default for a production call with no attached
// harness/debug consumer) makes this a no-op, exactly like every other
// ResolutionTracer.Trace call site in this package.
func traceRetrievalSource(deps ResolveDeps, requestID, stage, term string, results []CandidateNode) {
	if deps.ResolutionTracer == nil {
		return
	}
	for _, node := range results {
		subject, ok := NodeSubject(node)
		if !ok {
			continue
		}
		deps.ResolutionTracer.Trace(ResolutionTraceEvent{
			RequestID: requestID, Stage: stage,
			TermHash: traceTermHash(term), Subject: subject,
		})
	}
}

// exactNameMatches applies NodeCandidate's OWN label/alias/provider-alias
// exact-match predicate (candidate.go) to a fixed candidate set, WITHOUT
// re-implementing NodeCandidate's authorization/internal-node/confidence
// logic -- this function decides retrieval membership only; NodeCandidate,
// called downstream by mergeSearchResults, remains the single place that
// decides whether a match becomes a real SubjectCandidate.
func exactNameMatches(term string, nodes []CandidateNode) []CandidateNode {
	trimmed := strings.TrimSpace(term)
	if trimmed == "" {
		return nil
	}
	var matches []CandidateNode
	for _, node := range nodes {
		subject, ok := NodeSubject(node)
		if !ok {
			continue
		}
		if strings.EqualFold(trimmed, node.Name) || strings.EqualFold(trimmed, subject.Label) {
			matches = append(matches, node)
			continue
		}
		matched := false
		for _, alias := range AliasAttributes(node.Attributes) {
			if strings.EqualFold(trimmed, alias) {
				matched = true
				break
			}
		}
		if !matched {
			for _, alias := range ProviderAliasAttributes(node.Attributes) {
				if strings.EqualFold(trimmed, alias) {
					matched = true
					break
				}
			}
		}
		if matched {
			matches = append(matches, node)
		}
	}
	return matches
}
