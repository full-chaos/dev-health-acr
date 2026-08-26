package graphrank

import (
	"context"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4038 (split from CHAOS-4012 per taxonomy): kindOfferMaterial
// (chaos3900_structure_offers.go) builds the expected_kind offer's
// KindOptions from nothing but the resolved SubjectCandidate pool --
// candidatesBySubject, as ResolveSubjects (resolve.go) assembles it via the
// per-term Search loop, AliasLookup, and the question-level SearchQuestion
// pass. Every one of those passes shares ONE cross-kind top-K budget
// (request.Options.MaxSubjectCandidates): a comparatively lower-relevance
// but genuinely-present kind can be starved out of the pool entirely by a
// more common kind filling that same shared budget. No offer-builder change
// can recover a kind the pool never contained -- kindOfferMaterial is a
// pure reducer over its input. This file adds a small, additive,
// kind-scoped SUPPLEMENTAL retrieval pass (applyKindCoverageFloor, wired
// into ResolveSubjects) that runs strictly after every ordinary pass, only
// for a kind still absent from the pool at that point, so kindOfferMaterial
// has something to offer regardless of how the ordinary top-K ranked.

// kindCoverageQueryLimit bounds each CHAOS-4038 SearchKind call -- a small
// floor, not a competing top-K: this pass exists to prove AT LEAST ONE
// candidate of an otherwise-absent kind exists, never to out-rank the
// ordinary Search/SearchQuestion passes' own budget or displace what they
// already found.
//
// SHARED WITH CHAOS-4132: applyConfirmedKindRescue (chaos4132_confirmed_kind_
// rescue.go) reuses this SAME constant for its own deps.SearchKind calls --
// deliberately one dial, not two independently-tuned ones (2026-08-23
// scope-first report + chris ruling, CHAOS-4038 ticket comment thread). That
// makes this value's blast radius wider than "turn-1 offers": when the
// CHAOS-4132 rescue fires, its `truncated` output is folded into the commit
// gate's own searchTruncated input (resolve.go's rescue call site), so
// raising this limit can also change confirmed-kind rescue commit outcomes,
// not just kindOfferMaterial's offer set. That coupling is an ACCEPTED,
// DISCLOSED secondary effect of this value, not an oversight -- see the
// CHAOS-4038 ticket comment for the full accounting and the
// confirmed_kind_rescue trace-stage fields (resolve.go, tracer.go) that make
// it observable from a run's own artifacts.
//
// 5 -> 20 (2026-08-23): 20 is not a fresh guess -- it is deps.CalibratedTopK
// (falkorgraph/retrieval_policy.go), the SAME already-calibrated cross-kind
// top-K this deployment already trusts for ordinary retrieval, safely under
// falkorgraph's own MaxResults clamp (default 25, chaos4038_kind_coverage.go
// falkorgraph implementation) so the value passes through unclamped. Tying
// this floor's own limit to an existing calibrated constant, rather than
// picking a new number blind, mirrors this file's own kindCoverageMaxTermsPerKind
// doc comment's "a conservative, always-safe ceiling... not a calibrated
// recall/cost tradeoff" caution -- this ticket has no live measurement slot
// to calibrate a genuinely NEW value either, so reusing one this codebase
// already validated is the safer choice available. kindCoverageMaxTermsPerKind
// is UNCHANGED by this -- a separate dial, out of this ticket's scope.
const kindCoverageQueryLimit = 20

// kindCoverageMaxTermsPerKind bounds how many of this resolution's own terms
// applyKindCoverageFloor will spend a SearchKind call on for any ONE still-
// unsatisfied kind (codex CHAOS-4038 review round 2, finding 3): without
// this cap, a kind that genuinely has no matching subject anywhere in the
// corpus gets queried once per term, every term, for every resolution that
// never confirms a kind -- unbounded by request.Options.MaxSubjectCandidates
// or any other existing budget, across all seven offerable kinds
// (kindCoverageFloorKinds, CHAOS-4271: always the full set now). Capping to
// the FIRST few terms (SubjectTerms' own preference order, most salient
// first) keeps the worst case at
// len(kindCoverageFloorKinds) * kindCoverageMaxTermsPerKind queries -- at
// most 7*3=21 per resolution, independent of how many terms this
// resolution's own interpretation produced. A conservative, always-safe
// ceiling (mirrors structureOfferMaxOptions' own "truncate a supplemental
// pass to a fixed bound, never let it scale with unbounded input" idiom,
// chaos3900_structure_offers.go), not a calibrated recall/cost tradeoff --
// this ticket has no live measurement slot to calibrate one, and a
// generous-but-bounded default is strictly safer than an unbounded one.
const kindCoverageMaxTermsPerKind = 3

// kindCoverageFloorKinds is the coverage floor's set: exactly
// structureOfferKinds (chaos3900_structure_offers.go), including
// isAliasLookupScopedKind kinds (repository/project/team, subject.go) --
// never a second, independently maintained list.
//
// CHAOS-4271 (fix): repository/project/team used to be excluded whenever
// aliasLookupTrustworthy (deps.AliasLookup != nil && the identity-universe
// READ was complete, i.e. not budget-truncated) was true, on the premise
// that AliasLookup already "covers them completely" in that case. That
// premise conflated two different facts: a complete READ only proves the
// identity-universe table was read exhaustively, never that this
// resolution's own terms actually MATCHED a row in it -- AliasLookup finds
// registered aliases/identities by near-exact term match, a narrower
// mechanism than the lexical/fulltext SearchKind this floor otherwise uses.
// A repository whose alias table has no row for it (or whose registered
// alias does not literally equal any term this resolution's interpretation
// produced) went permanently unrescuable even though a fulltext SearchKind
// call might well have found it -- exactly the failure the four "census"
// kinds (pull_request/work_item/ci_run/pull_request_review) never suffer,
// since nothing ever excludes THEM from this set.
//
// The fix is to trust the SAME pool-presence check (missingCoverageKinds,
// below) that already governs the four census kinds for these three too,
// rather than a separate, coarser completeness-based exclusion.
// AliasLookup's own claimants are merged into the pool BEFORE this floor
// runs (resolve.go, the AliasLookup block precedes the "CHAOS-4038: the
// kind-coverage floor runs LAST" comment) -- so a kind AliasLookup actually
// matched is already present in pool and missingCoverageKinds naturally
// skips it, at zero added cost. Only a kind AliasLookup did NOT produce --
// regardless of why -- ever reaches an extra SearchKind call, which is
// exactly the rescue this floor exists to provide.
//
// Derived from structureOfferKinds (never a second, independently
// maintained kind list) so the two sets cannot silently drift apart --
// widening structureOfferKinds automatically widens the coverage floor's
// candidate set too.
var kindCoverageFloorKinds = computeKindCoverageFloorKinds()

// kindCoverageOrder pins a fixed, deterministic iteration order over
// kindCoverageFloorKinds (lexicographic on the kind string) -- computed once
// from kindCoverageFloorKinds itself, never a second hand-maintained list,
// mirroring anchorOfferMaterial's own explicit-sort discipline
// (chaos3900_structure_offers.go) for the same "never a function of Go's
// randomized map order" reason. This is the floor's ONE kind set now
// (CHAOS-4271) -- applyKindCoverageFloor's own missingCoverageKinds call
// uses it directly, no conditional widening.
var kindCoverageOrder = sortedKinds(kindCoverageFloorKinds)

func computeKindCoverageFloorKinds() map[contextfabric.SubjectKind]bool {
	kinds := make(map[contextfabric.SubjectKind]bool, len(structureOfferKinds))
	for kind := range structureOfferKinds {
		kinds[kind] = true
	}
	return kinds
}

func sortedKinds(kinds map[contextfabric.SubjectKind]bool) []contextfabric.SubjectKind {
	sorted := make([]contextfabric.SubjectKind, 0, len(kinds))
	for kind := range kinds {
		sorted = append(sorted, kind)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}

// missingCoverageKinds returns, in floorKinds' own order, every floorKinds
// member with ZERO representation in pool -- the set applyKindCoverageFloor
// still needs to query SearchKind for.
func missingCoverageKinds(pool map[string]contextfabric.SubjectCandidate, floorKinds []contextfabric.SubjectKind) []contextfabric.SubjectKind {
	present := make(map[contextfabric.SubjectKind]bool, len(pool))
	for _, candidate := range pool {
		present[candidate.Subject.Kind] = true
	}
	missing := make([]contextfabric.SubjectKind, 0, len(floorKinds))
	for _, kind := range floorKinds {
		if !present[kind] {
			missing = append(missing, kind)
		}
	}
	return missing
}

// poolHasKind reports whether pool already carries any candidate of kind --
// used to stop spending further per-term SearchKind calls on a kind as soon
// as the floor for it is satisfied.
func poolHasKind(pool map[string]contextfabric.SubjectCandidate, kind contextfabric.SubjectKind) bool {
	for _, candidate := range pool {
		if candidate.Subject.Kind == kind {
			return true
		}
	}
	return false
}

// candidatesOfKind returns, in a fixed (CanonicalID-sorted) order, every
// pool candidate of kind -- applyKindCoverageFloor's own way of recovering
// what it just added for a satisfied kind, deterministically.
func candidatesOfKind(pool map[string]contextfabric.SubjectCandidate, kind contextfabric.SubjectKind) []contextfabric.SubjectCandidate {
	var found []contextfabric.SubjectCandidate
	for _, candidate := range pool {
		if candidate.Subject.Kind == kind {
			found = append(found, candidate)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Subject.CanonicalID < found[j].Subject.CanonicalID })
	return found
}

// applyKindCoverageFloor is ResolveDeps.SearchKind's own caller (resolve.go
// wires this in, strictly after every ordinary retrieval pass, gated on
// confirmedKind == nil -- see that call site's own comment). nil
// deps.SearchKind is a no-op, matching every other optional ResolveDeps
// dependency's convention. The kind set is always kindCoverageOrder
// (CHAOS-4271) -- see kindCoverageFloorKinds' own doc comment for why a
// separate AliasLookup-completeness-based exclusion was removed in favor of
// missingCoverageKinds' existing pool-presence check.
//
// For each kind still missing from pool (missingCoverageKinds, a snapshot
// taken ONCE up front -- an earlier missing kind that a LATER kind's own
// coverage query happens to also surface, e.g. via mergeSearchResults'
// observation-traversal side effects, is still worth its own dedicated
// query below; this mirrors the ordinary per-term Search loop's own
// unconditional per-term iteration rather than re-checking pool state
// between kinds), this walks terms in order and calls deps.SearchKind(term,
// kind, kindCoverageQueryLimit). Stops issuing further per-term calls for a
// kind as soon as it is satisfied -- a coverage floor only ever needs ONE
// candidate to give kindOfferMaterial something to offer, so the remaining
// terms' calls are unneeded spend. Terms beyond kindCoverageMaxTermsPerKind
// are never tried at all (codex CHAOS-4038 review round 2, finding 3) -- see
// that constant's own doc comment for the unbounded-fan-out concern it
// bounds.
//
// MERGE TARGET SPLITS BY KIND (CHAOS-4271 codex round 1, finding 1 -- HIGH,
// BLOCK): the four census kinds (pull_request/work_item/ci_run/
// pull_request_review) merge straight into `pool` through the SAME
// mergeSearchResults path every other pass uses (allowExactMatch=true:
// terms here are the SAME genuine caller-derived subject terms the per-term
// Search loop already used), exactly as before this ticket --
// TestResolveSubjects_SearchKindCoverageTruncationNeverBlocksAnUnrelatedCommit
// pins that an EXACT-match census-kind floor find commits, by design,
// unchanged here. repository/project/team (isAliasLookupScopedKind) merge
// into offerOnlyPool instead, a map `pool` never sees: their finds reach
// `added` (below) and, through resolve.go's unionCandidatesForOffer, the
// expected_kind offer -- but they NEVER enter candidatesBySubject, so
// ResolveFromMergedCandidatesWithGateAndBasis (resolve.go), which reads
// `pool` alone, cannot rank, offer via resolution.Candidates, or COMMIT one,
// no matter how exact the match. This is the "rescue offers only; never
// change commit decisions" condition the CHAOS-4271 orchestrator ruling
// (2026-08-25 08:22 PDT) stated for these three kinds specifically -- unlike
// the four census kinds, whose floor-commit behavior predates this ticket
// and is deliberately left alone. recordIdentityClaim (chaos3884_identity.go)
// is ALSO suppressed (nil identity/identityTerms) for these three, below
// (codex round 2, finding 1 -- HIGH, BLOCK): identityClaimants is ONE map
// shared across every pass this resolution runs, so letting an offer-only
// repository/project/team floor find register a claim under the SAME
// (class, term) key a DIFFERENT, unrelated candidate's own exact match uses
// would let identityCollision suppress THAT candidate's commit -- changing
// an existing, unrelated commit decision as pure collateral damage, exactly
// what the ruling's condition forbids. TestResolveSubjects_SearchKindRescuedRepositoryNeverAutoCommitsWithoutDecisiveGrounds
// is the commit-eligibility regression, using an EXACT-confidence repository
// match (codex round 1, finding 2: the prior version only tested a non-exact
// 0.5 match, too weak to have exercised the exact_index gate finding 1 is
// actually about).
// TestResolveSubjects_SearchKindOfferOnlyFindNeverSuppressesAnUnrelatedExactCommit
// is the identity-collision regression (codex round 2, finding 1).
//
// Returns `added`: every candidate belonging to a kind that started this
// call missing and is now present -- census-kind finds read back from
// `pool` (codex CHAOS-4038 review, finding 1), alias-scoped-kind finds read
// back from offerOnlyPool. ResolveSubjects' own final ranked-candidate
// truncation (ResolveFromMergedCandidatesWithGateAndBasis's `max` cap) can
// still drop a CENSUS-kind coverage-floor find from resolution.Candidates
// when the pool already held `max` higher-confidence candidates of OTHER
// kinds; an alias-scoped-kind find is never IN resolution.Candidates to
// begin with, by this function's own design above. Either way,
// resolve.go's unionCandidatesForOffer folds `added` into the offer's own
// input, so a coverage find still gets OFFERED regardless of which map it
// came from.
//
// A genuine SearchKind failure aborts the pass and propagates as an error,
// exactly like Search/SearchQuestion/AliasLookup's own error handling
// (resolve.go) -- a real backend fault is never silently downgraded to
// "found nothing" here either.
func applyKindCoverageFloor(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, pool map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, identity identityClaimants, identityTerms identityMatchTerms) (added []contextfabric.SubjectCandidate, traversalDegraded int, authzDropped int, truncated bool, degraded bool, missingKinds int, missingKindsList []string, err error) {
	if deps.SearchKind == nil {
		return nil, 0, 0, false, false, 0, nil, nil
	}
	missing := missingCoverageKinds(pool, kindCoverageOrder)
	// missingKinds (CHAOS-4086) is the SNAPSHOT taken here, before any
	// SearchKind call runs -- how many floor kinds had zero representation
	// in the pool at the moment this pass began. Deliberately not
	// recomputed at the end: the question a reader asks of a report row is
	// "what was missing that this floor had to go looking for", and a
	// post-pass count would answer the different question "what is still
	// missing", conflating a floor that found everything with one that
	// never had anything to find.
	missingKinds = len(missing)
	// missingKindsList (CHAOS-4183, phase 2, team-lead ruling 2026-08-23) is
	// the SAME snapshot's own kind IDENTITY, closed-vocabulary
	// (contextfabric.SubjectKind values only, corpus-safe -- same
	// discipline as KindOfferBoundaryKinds/distinctCandidateKinds,
	// chaos3900_structure_offers.go). missingKinds alone (a bare count) was
	// not enough to disambiguate a CHAOS-4012 re-smoke finding: two
	// genuinely different situations -- "the floor searched for kind X and
	// still couldn't retain the corpus-target item" (a lexical-reach
	// question) vs. "the floor never touched kind Y at all because a
	// sibling of Y already occupied the pool, and Y's own later absence
	// from the offer builders' shared input is pure ranking/truncation" --
	// were indistinguishable from missingKinds' count alone once more than
	// one floor kind was in play. This field is what a future reader checks
	// FIRST, before re-deriving the same ambiguity CHAOS-4183 hit.
	missingKindsList = subjectKindStrings(missing)
	boundedTerms := terms
	if len(boundedTerms) > kindCoverageMaxTermsPerKind {
		boundedTerms = boundedTerms[:kindCoverageMaxTermsPerKind]
	}
	// offerOnlyPool is repository/project/team's OWN merge target -- see
	// this function's own doc comment above (CHAOS-4271 codex round 1,
	// finding 1) for why these three never touch `pool`.
	offerOnlyPool := make(map[string]contextfabric.SubjectCandidate)
	for _, kind := range missing {
		target := pool
		// mergeIdentity/mergeIdentityTerms (CHAOS-4271 codex round 2, finding
		// 1, HIGH, BLOCK): nil for repository/project/team -- recordIdentityClaim
		// (chaos3884_identity.go) is unconditional inside mergeSearchResults
		// and shares ONE identityClaimants/identityMatchTerms pair across
		// every pass this resolution runs, so an offer-only floor find that
		// happens to be an exact/alias/provider-key match would otherwise
		// still register a claim under the SAME (class, term) key a
		// DIFFERENT, unrelated candidate's own exact match uses --
		// identityCollision (chaos3884_identity.go) then sees >1 claimant
		// and suppresses THAT candidate's commit, even though it has nothing
		// to do with repository/project/team. Before this ticket, a
		// repository/project/team floor SearchKind call in the "AliasLookup
		// complete but unmatched" scenario never happened at all, so this
		// collision was never reachable there -- passing nil here keeps that
		// scenario's OTHER candidates' commit decisions byte-identical,
		// exactly the "must not change commit decisions" condition asks for.
		// mergeSearchResults' own doc comment already documents nil as a
		// deliberate, pre-existing convention (the SearchQuestion pass uses
		// it for the same "this pass must not participate in identity-
		// collision tracking" reason).
		mergeIdentity, mergeIdentityTerms := identity, identityTerms
		if isAliasLookupScopedKind(kind) {
			target = offerOnlyPool
			mergeIdentity, mergeIdentityTerms = nil, nil
		}
		for _, term := range boundedTerms {
			results, kindTruncated, kindDegraded, searchErr := deps.SearchKind(ctx, term, kind, kindCoverageQueryLimit)
			if searchErr != nil {
				return added, traversalDegraded, authzDropped, truncated, degraded, missingKinds, missingKindsList, searchErr
			}
			if kindTruncated {
				truncated = true
			}
			if kindDegraded {
				degraded = true
			}
			// vectorArmSimilarity=nil: this pass is a lexical coverage
			// floor, not a vector-arm competitor, so it must not
			// participate in CHAOS-3829's commit-path carve-out, the same
			// exclusion the question-level pass documents.
			termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, results, target, observationParentKey, observationBlocked, true, nil, mergeIdentity, mergeIdentityTerms)
			traversalDegraded += termTraversalDegraded
			authzDropped += termAuthzDropped
			if poolHasKind(target, kind) {
				break
			}
		}
	}
	for _, kind := range missing {
		source := pool
		if isAliasLookupScopedKind(kind) {
			source = offerOnlyPool
		}
		added = append(added, candidatesOfKind(source, kind)...)
	}
	return added, traversalDegraded, authzDropped, truncated, degraded, missingKinds, missingKindsList, nil
}

// subjectKindStrings converts a []contextfabric.SubjectKind to []string,
// preserving order -- the same closed-vocabulary, corpus-safe conversion
// distinctCandidateKinds (chaos3900_structure_offers.go) already uses for a
// sibling telemetry field, kept as its own small function here rather than
// exported and shared, since the two operate on different input shapes
// (a candidate slice there, a kind slice here).
func subjectKindStrings(kinds []contextfabric.SubjectKind) []string {
	if len(kinds) == 0 {
		return nil
	}
	out := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, string(kind))
	}
	return out
}
