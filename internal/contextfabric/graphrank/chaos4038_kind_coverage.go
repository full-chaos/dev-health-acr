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
// or any other existing budget, and worse when aliasLookupTrustworthy is
// false widens effectiveCoverageFloorKinds to all seven offerable kinds.
// Capping to the FIRST few terms (SubjectTerms' own preference order, most
// salient first) keeps the worst case at
// len(effectiveCoverageFloorKinds) * kindCoverageMaxTermsPerKind queries --
// at most 7*3=21 per resolution, independent of how many terms this
// resolution's own interpretation produced. A conservative, always-safe
// ceiling (mirrors structureOfferMaxOptions' own "truncate a supplemental
// pass to a fixed bound, never let it scale with unbounded input" idiom,
// chaos3900_structure_offers.go), not a calibrated recall/cost tradeoff --
// this ticket has no live measurement slot to calibrate one, and a
// generous-but-bounded default is strictly safer than an unbounded one.
const kindCoverageMaxTermsPerKind = 3

// kindCoverageFloorKinds is the coverage floor's BASE set: exactly
// structureOfferKinds (chaos3900_structure_offers.go) minus every
// isAliasLookupScopedKind kind (repository/project/team, subject.go) --
// pull_request, work_item, ci_run, pull_request_review. These four are
// exactly the "census kinds" that enter candidatesBySubject ONLY via the
// shared, kind-blind Search/SearchQuestion passes; they never get an
// AliasLookup read at all, at any completeness, so the floor always covers
// them regardless of this resolution's own AliasLookup outcome.
//
// The alias-lookup-scoped three are handled separately and CONDITIONALLY --
// see effectiveCoverageFloorKinds' own doc comment: they are covered by
// kindCoverageFloorKinds too whenever THIS resolution's own AliasLookup did
// not run, or ran but could not prove completeness, since in that case
// nothing else in this resolution covers them either.
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
// randomized map order" reason.
var kindCoverageOrder = sortedKinds(kindCoverageFloorKinds)

// aliasLookupScopedCoverageKinds is structureOfferKinds' alias-lookup-scoped
// subset (repository/project/team) in the SAME deterministic order
// discipline as kindCoverageOrder -- effectiveCoverageFloorKinds' own
// conditional addition (codex CHAOS-4038 review, finding 3).
var aliasLookupScopedCoverageKinds = sortedKinds(computeAliasLookupScopedCoverageKinds())

func computeKindCoverageFloorKinds() map[contextfabric.SubjectKind]bool {
	kinds := make(map[contextfabric.SubjectKind]bool, len(structureOfferKinds))
	for kind := range structureOfferKinds {
		if isAliasLookupScopedKind(kind) {
			continue
		}
		kinds[kind] = true
	}
	return kinds
}

func computeAliasLookupScopedCoverageKinds() map[contextfabric.SubjectKind]bool {
	kinds := make(map[contextfabric.SubjectKind]bool)
	for kind := range structureOfferKinds {
		if isAliasLookupScopedKind(kind) {
			kinds[kind] = true
		}
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

// effectiveCoverageFloorKinds is kindCoverageOrder, WIDENED to also include
// aliasLookupScopedCoverageKinds (repository/project/team) whenever
// aliasLookupTrustworthy is false -- codex CHAOS-4038 review, finding 3: the
// original, unreviewed version excluded those three UNCONDITIONALLY on the
// premise that deps.AliasLookup always covers them, but AliasLookup can be
// nil (backend does not implement it), or non-nil and still report
// complete=false (a historical read, an exceeded row budget, a source-table
// existence-check failure -- see AliasLookup's own doc comment, resolve.go).
// In every one of those cases nothing else in this resolution covers
// repository/project/team either, so the SAME starved-kind failure
// CHAOS-4038 exists to fix would still be reachable for those three kinds.
// aliasLookupTrustworthy should be deps.AliasLookup != nil && complete, the
// SAME two facts ResolveSubjects already computes for aliasIdentityComplete
// -- never a separate, independently-derived judgment.
func effectiveCoverageFloorKinds(aliasLookupTrustworthy bool) []contextfabric.SubjectKind {
	if aliasLookupTrustworthy {
		return kindCoverageOrder
	}
	combined := make([]contextfabric.SubjectKind, 0, len(kindCoverageOrder)+len(aliasLookupScopedCoverageKinds))
	combined = append(combined, kindCoverageOrder...)
	combined = append(combined, aliasLookupScopedCoverageKinds...)
	return combined
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
// dependency's convention. aliasLookupTrustworthy selects the kind set via
// effectiveCoverageFloorKinds -- see that function's own doc comment.
//
// For each kind still missing from pool (missingCoverageKinds, a snapshot
// taken ONCE up front -- an earlier missing kind that a LATER kind's own
// coverage query happens to also surface, e.g. via mergeSearchResults'
// observation-traversal side effects, is still worth its own dedicated
// query below; this mirrors the ordinary per-term Search loop's own
// unconditional per-term iteration rather than re-checking pool state
// between kinds), this walks terms in order and calls deps.SearchKind(term,
// kind, kindCoverageQueryLimit), merging every result into pool through the
// SAME mergeSearchResults path every other pass uses (allowExactMatch=true:
// terms here are the SAME genuine caller-derived subject terms the per-term
// Search loop already used; vectorArmSimilarity=nil: this pass is a lexical
// coverage floor, not a vector-arm competitor, so it must not participate
// in CHAOS-3829's commit-path carve-out, the same exclusion the
// question-level pass documents). Stops issuing further per-term calls for
// a kind as soon as poolHasKind reports it satisfied -- a coverage floor
// only ever needs ONE candidate to give kindOfferMaterial something to
// offer, so the remaining terms' calls are unneeded spend. Terms beyond
// kindCoverageMaxTermsPerKind are never tried at all (codex CHAOS-4038
// review round 2, finding 3) -- see that constant's own doc comment for the
// unbounded-fan-out concern it bounds.
//
// Returns `added`: every pool candidate belonging to a kind that started
// this call missing and is now present -- i.e. exactly what this call put
// into pool (codex CHAOS-4038 review, finding 1). ResolveSubjects' own final
// ranked-candidate truncation (ResolveFromMergedCandidatesWithGate's `max`
// cap) can drop a coverage-floor find from resolution.Candidates when the
// pool already held `max` higher-confidence candidates of OTHER kinds --
// exactly the scenario a small MaxSubjectCandidates plus a common-kind-heavy
// pool produces. kindOfferMaterial's own call site (resolve.go) unions
// `added` into resolution.Candidates for offer purposes ONLY, so a coverage
// find still gets OFFERED even when it does not survive that cap -- the cap
// itself, and everything commit/ranking-related, is completely untouched.
//
// A genuine SearchKind failure aborts the pass and propagates as an error,
// exactly like Search/SearchQuestion/AliasLookup's own error handling
// (resolve.go) -- a real backend fault is never silently downgraded to
// "found nothing" here either.
func applyKindCoverageFloor(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, terms []string, pool map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, identity identityClaimants, identityTerms identityMatchTerms, aliasLookupTrustworthy bool) (added []contextfabric.SubjectCandidate, traversalDegraded int, authzDropped int, truncated bool, degraded bool, missingKinds int, missingKindsList []string, err error) {
	if deps.SearchKind == nil {
		return nil, 0, 0, false, false, 0, nil, nil
	}
	missing := missingCoverageKinds(pool, effectiveCoverageFloorKinds(aliasLookupTrustworthy))
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
	for _, kind := range missing {
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
			termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, results, pool, observationParentKey, observationBlocked, true, nil, identity, identityTerms)
			traversalDegraded += termTraversalDegraded
			authzDropped += termAuthzDropped
			if poolHasKind(pool, kind) {
				break
			}
		}
	}
	for _, kind := range missing {
		added = append(added, candidatesOfKind(pool, kind)...)
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
