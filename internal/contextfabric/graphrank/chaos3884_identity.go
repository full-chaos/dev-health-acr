package graphrank

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// identityKeyClass (CHAOS-3884) names WHICH identity signal produced a
// candidate's identity-class mechanism -- the merge-condition requirement
// from the architecture reconciliation that key classes survive the
// normalized surface, so collision/uniqueness counting (identityCollision,
// below) can be scoped PER CLASS rather than merged into one flat total
// (spot-check MEDIUM-C: "the rescue guard must key on the CLAIMANT COUNT
// per key class"). A same-string coincidence ACROSS classes (a
// provider-qualified alias that happens to textually equal a different
// subject's bare name) is weaker evidence of genuine ambiguity than a
// same-class collision and must not be conflated with one.
type identityKeyClass string

const (
	identityKeyClassLabel           identityKeyClass = "label"          // MatchExact
	identityKeyClassAlias           identityKeyClass = "alias"          // MatchAlias
	identityKeyClassProviderVariant identityKeyClass = "provider_alias" // MatchProviderKey
)

// identityMatchTermEntry pairs a key class with the SPECIFIC normalized
// term that produced an identity mechanism for one candidate -- MEDIUM-B:
// uniqueness must bind to the term that produced the match, not any term
// the candidate happens to also match elsewhere (a candidate colliding on
// term A must not fast-path on the strength of an unrelated unique term
// B).
type identityMatchTermEntry struct {
	class identityKeyClass
	term  string // already NormalizeAliasTerm'd
}

// identityClaimants is the per-(class,term) claimant SET, built during
// merge (mergeSearchResults) from every NodeCandidate result that carries
// an identity-class mechanism -- ANY kind in isAliasLookupScopedKind, not
// just commit-eligible ones (HIGH-5: counting is broader than
// eligibility). Keyed class -> normalized term -> subject key -> present.
type identityClaimants map[identityKeyClass]map[string]map[string]bool

// identityMatchTerms is the per-SUBJECT list of (class,term) pairs that
// produced an identity mechanism for it -- built at the SAME merge point,
// from the SAME per-call NodeCandidate result, before MergeCandidates'
// union flattens per-term provenance away. Keyed subject key -> entries.
type identityMatchTerms map[string][]identityMatchTermEntry

// recordIdentityClaim is called once per NodeCandidate result (BEFORE
// MergeCandidates unions it into candidatesBySubject) by mergeSearchResults.
// POST-AUTHORIZATION BY CONSTRUCTION (spot-check finding 4a, settled): the
// caller only ever has a `candidate` to pass here because NodeCandidate
// already returned ok==true, which requires AuthorizedAttributes to have
// passed first (candidate.go). This is the OPPOSITE side of
// vectorArmSimilarity's own pre-authorization choice, and deliberately so:
// vectorArmSimilarity sits pre-auth so a hidden competitor cannot INFLATE a
// margin it should not be able to affect; this collision side-channel sits
// post-auth so a hidden (unauthorized) claimant cannot SUPPRESS a commit
// the caller IS authorized to see -- the same unscopedVisibility
// existence-oracle class (resolution.go's own precedent) applied to the
// opposite direction of leak.
//
// No-op (nil-safe) when claimants/terms are nil -- mirrors
// vectorArmSimilarity's own "the question-pass call passes nil, deliberately"
// convention: a caller that does not want identity-collision tracking for
// this merge (today: the SearchQuestion call, whose allowExactMatch=false
// guarantees NodeCandidate can never produce an identity mechanism from it
// anyway) simply passes nil maps.
func recordIdentityClaim(candidate contextfabric.SubjectCandidate, claimants identityClaimants, terms identityMatchTerms) {
	if claimants == nil && terms == nil {
		return
	}
	class, ok := identityClassOf(candidate.MatchMechanisms)
	if !ok {
		return
	}
	// Every NodeCandidate call sets MatchedTerms to a SINGLETON
	// ([]string{term}) before any merge/union happens -- mergeSearchResults
	// calls this on the FRESH, pre-union return value, so [0] is always the
	// term THIS call matched on.
	if len(candidate.MatchedTerms) == 0 {
		return
	}
	term := NormalizeAliasTerm(candidate.MatchedTerms[0])
	key := SubjectKey(candidate.Subject)
	if claimants != nil {
		if claimants[class] == nil {
			claimants[class] = map[string]map[string]bool{}
		}
		if claimants[class][term] == nil {
			claimants[class][term] = map[string]bool{}
		}
		claimants[class][term][key] = true
	}
	if terms != nil {
		terms[key] = append(terms[key], identityMatchTermEntry{class: class, term: term})
	}
}

// identityClassOf reports the identity key class a FRESH (pre-union)
// candidate's mechanism set carries, if any recognized one is present.
// MatchExact takes priority if somehow present alongside another identity
// mechanism (defensive; NodeCandidate's own mutual-exclusivity guarantees
// this never actually happens for a single call's mechanisms, but reading
// it as a priority rather than assuming exclusivity costs nothing and
// avoids a silent behavior change if that guarantee is ever loosened).
func identityClassOf(mechanisms []contextfabric.MatchMechanism) (identityKeyClass, bool) {
	switch {
	case HasMechanism(mechanisms, contextfabric.MatchExact):
		return identityKeyClassLabel, true
	case HasMechanism(mechanisms, contextfabric.MatchAlias):
		return identityKeyClassAlias, true
	case HasMechanism(mechanisms, contextfabric.MatchProviderKey):
		return identityKeyClassProviderVariant, true
	default:
		return "", false
	}
}

// identityCollision reports whether candidate key's OWN identity-producing
// (class,term) pairs include at least one whose claimant set (identityClaimants)
// has more than one member -- MEDIUM-B (binds to the producing term, not
// any term) and MEDIUM-C (per key class, not a flat any-kind total): a
// candidate is safe to trust ONLY when EVERY class/term pair that earned it
// an identity mechanism is itself uniquely claimed.
//
// A candidate with NO identity match terms at all (never touched by this
// mechanism -- e.g. an ordinary lexical/vector-only candidate the CHAOS-3829
// rescue is considering) returns false: nothing here should ever SUPPRESS a
// commit path this ticket did not touch (spot-check item 1's own pinning
// requirement).
func identityCollision(key string, claimants identityClaimants, terms identityMatchTerms) bool {
	for _, entry := range terms[key] {
		if byTerm := claimants[entry.class]; byTerm != nil {
			if len(byTerm[entry.term]) > 1 {
				return true
			}
		}
	}
	return false
}

// identityTrustUnproven reports whether candidate's confidence==1 rests
// entirely on NodeCandidate's identity-trust bump (CHAOS-3884) and this
// resolution's own identity read cannot vouch that bump as proven: the
// SAME confidence==1/eligible-kind/MatchAlias-or-MatchProviderKey test
// resolution.go's identityIndex membership already applies, plus one more
// condition, !aliasIdentityComplete.
//
// Codex xhigh design-review (2026-08-17) finding, CHAOS-3891: aliasIdentityComplete=
// false means EITHER the identity-universe table read was truncated
// (devhealthsource.fetchIdentityKind's identityUniverseRowBudget) OR at
// least one matched claimant this call found failed its graph existence
// check (falkorgraph/reader.go's graphMissing sweep) -- either way, a
// competing claimant may exist that this resolution never even SAW, so
// identityCollision (which can only ever compare against claimants that
// DID make it into this resolution) cannot detect the collision a complete
// read would have. Before this fix, only resolution.go's dedicated
// identity fast path (the aliasIdentityComplete&&... case) required
// completeness -- the ordinary LoneFloor/TopFloor strength gates checked
// identityCollision but not aliasIdentityComplete, so an identity-trust
// candidate from an INCOMPLETE read could still auto-commit through them
// on an unproven uniqueness claim. This closes that gap at both sites
// without touching the fast path's own (already-correct) gating.
//
// CHRIS RULING (2026-08-17): KEEP -- landed ahead of an in-flight "defer,
// instrument first" message that crossed in transit; the regression tests
// this predicate is pinned by (chaos3884_identity_resolution_test.go) prove
// the hole is real (the pre-fix gate wrongly commits, hand-verified both
// ways), so shipping the fix together with the tracer's observability is
// strictly better than deferring either alone. reviewer-3884 (the Option-C
// design authority) independently signed off 2026-08-17: this predicate's
// scope is correct because the confidence==1 it gates is NOT evidence
// strength -- it is manufactured by the eligibility-gated identity bump,
// and that bump's warrant is a complete identity read; LoneFloor/TopFloor
// read the 1.0 AS strength, so without completeness both gates arbitrate
// on a number whose justification was never established. Adding this
// conjunct restores the precondition the bump silently assumed; it is not
// an extra restriction. A candidate carrying no identity mechanism, or one
// whose confidence==1 came from an exact label match (CHAOS-3810's
// override, which rests on an independent, already-complete guarantee --
// MEDIUM-D) rather than identity trust, returns false here unconditionally,
// exactly like identityCollision's own "untouched candidate" guarantee.
//
// A candidate this returns true for is NOT discarded -- it simply cannot
// win the ordinary strength gates on the strength of an unproven identity
// claim (falls through to ambiguous/clarify, per reviewer-3884's citation
// of the design doc's §16/§9.1 "degraded proof commits no candidate but is
// never silently removed" rule); it may still commit on a later call once
// aliasIdentityComplete is true again, or via whatever ordinary
// lexical/vector evidence it independently carries.
//
// Deliberately NOT applied to the CHAOS-3829 vector-margin rescue
// (resolution.go, vectorMarginCommit's own call site): that rescue picks
// on raw vector-arm similarity, never on Confidence, so a bump-derived 1.0
// never feeds its margin, and the rescue's own ratified geometry already
// tolerates an incomplete population (it may fire even when searchTruncated
// is the reason ambiguous is true) -- adding this conjunct there would
// narrow a separately-ratified path on a premise that path never rested on
// (reviewer-3884, 2026-08-17).
func identityTrustUnproven(candidate contextfabric.SubjectCandidate, aliasIdentityComplete bool) bool {
	if aliasIdentityComplete {
		return false
	}
	if candidate.Confidence != 1 || !isAliasIdentityEligibleKind(candidate.Subject.Kind) {
		return false
	}
	return HasMechanism(candidate.MatchMechanisms, contextfabric.MatchAlias) || HasMechanism(candidate.MatchMechanisms, contextfabric.MatchProviderKey)
}
