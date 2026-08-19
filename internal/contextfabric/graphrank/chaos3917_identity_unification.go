package graphrank

// CHAOS-3917 (alias-multi-claimant wrong commit; corpus case 45,
// gen-trial-chaos3896-sliceC-run1-20260819T124758Z.json, resolved_active_
// epoch=1): the identity universe minted two exact-alias repository
// claimants at confidence 1.0 for the SAME term a different subject's own
// label ALSO matched exactly. resolution.go's exactIndex fast path (the
// CHAOS-3810 override, :519-524) commits whenever exactly one RETAINED
// candidate has Confidence==1 && MatchExact -- it never reads the
// identity/identityTerms side channel at all, so a same-term rival
// recorded under a DIFFERENT identity class (alias or provider-key) sails
// straight through it. That is a genuine collision, not the "weaker,
// coincidental" cross-class case chaos3884_identity.go's own MEDIUM-C
// spot-check deliberately excludes from identityCollision (a
// provider-qualified alias happening to textually equal an unrelated
// subject's bare name): here the exact match's term IS
// character-for-character the winning subject's OWN name, so a second,
// IDENTICAL-STRING alias/provider-key claim by a DIFFERENT canonical
// subject is not a coincidence -- it is a second subject the caller's own
// literal term reaches exactly as directly as the first.
//
// Ratified design shape (chris, 2026-08-19): "unified claimant-uniqueness
// proof for all string-identity fast paths" -- (1) an exact alias/label
// match alone must never suffice to commit; (2) no fast-path commit
// without a complete, authorized claimant enumeration proving exactly one
// canonical subject claims the term; (3) per-term claimant association
// (already true of identityClaimants/identityMatchTerms, HIGH-5/MEDIUM-B);
// (4) an incomplete enumeration must fail toward clarify/refuse. This file
// adds the missing LABEL<->ALIAS/PROVIDER-KEY cross-class half of that
// proof and wires it into every commit path resolution.go already gates on
// identityCollision -- deliberately NOT into identityCollision itself
// (chaos3884_identity.go), which stays per-class exactly as MEDIUM-C
// ratified (TestIdentityCollision_DifferentKeyClassesDoNotCollide pins
// this, unchanged): the alias<->provider-key pairing that test covers is
// untouched by this ticket, both in code and in scope.
//
// Residual, deliberately unaddressed by this fix (same class of risk the
// exactIndex doc comment already concedes for same-class label
// duplicates): a rival claimant this resolution's own search/AliasLookup
// call never RETURNED (hidden entirely behind truncation, or an
// AliasLookup a caller never wired at all) cannot be detected by a
// claimant-set check that only ever sees what it was handed -- closing
// that requires the completeness signal (aliasIdentityComplete) to gate
// the exact path too, which would also disable CHAOS-3810's own
// truncation-immunity guarantee for every backend that does not wire
// AliasLookup (TestResolveSubjectsCommitsExactLabelMatchOnATruncatedSearch,
// exact_truncation_test.go, a deliberately still-ratified, unmodified
// regression). This fix closes the VISIBLE-but-uncounted blind spot case
// 45 actually hit (identity_universe_calls=1, identity_matched_rows=3 --
// every rival claimant WAS present in this same resolution's own pool);
// it does not additionally narrow the truncation-immunity guarantee for
// backends with no identity-completeness signal to consult.
//
// Codex xhigh review (task-mt05idj8-bxvfac, 2026-08-19) flagged this
// residual gap as a P1 ("visible-only checking does not satisfy the
// ratified completeness requirement") and proposed closing it by rewiring
// exact_truncation_test.go's own fixtures to supply a complete claimant
// enumeration, narrowing that test's guarantee to "exact labels survive
// truncation when COMPLETE identity enumeration proves uniqueness."
// Deliberately NOT implemented in this ticket: doing so changes production
// commit behavior for every backend that does not wire AliasLookup at all
// (aliasIdentityComplete is permanently false there, with or without
// truncation), which is a strictly bigger, cross-cutting change than the
// measured defect this ticket closes -- case 45 itself had a COMPLETE
// identity read with the rival fully visible (identity_universe_calls=1,
// identity_matched_rows=3), so this residual gap did not cause it.
// Escalated to team-lead/chris as a follow-on hardening decision requiring
// explicit sign-off, since it would also revise CHAOS-3810's own ratified
// regression guarantee -- not something this ticket's scope authorizes
// unilaterally.
//
// SHIP EXCEPTION RECORDED (orchestrator ruling, 2026-08-19, chris retains
// override until merge): ship this visible-rival fix now; codex's finding
// is real (epoch-1 healing mints claimants and truncation is endemic) but
// is not what case 45 measured, and closing it fully is a designed change
// to CHAOS-3810's own premise that gets its own ticket, design note, and
// review. Follow-on: CHAOS-3922 (completeness-gated exact-label commit
// proof), relations blocks CHAOS-3916 / related CHAOS-3917 / related
// CHAOS-3810 -- must land before CHAOS-3916's cutover flips (the boundary
// where the ratified design's "complete enumeration" words bind).

// identityRivalClasses names, for identity key class c, the OTHER classes
// CHAOS-3917's unified claimant-uniqueness proof also checks a term
// against before treating c's own uniqueness as proven. Deliberately
// excludes the alias<->provider_alias pairing (MEDIUM-C's own scope,
// chaos3884_identity.go, untouched): this ticket's finding is specifically
// about a LABEL-class match being trusted alone; it says nothing about the
// alias/provider-key same-string coincidence MEDIUM-C already, and
// separately, treats as weaker evidence.
func identityRivalClasses(c identityKeyClass) []identityKeyClass {
	switch c {
	case identityKeyClassLabel:
		return []identityKeyClass{identityKeyClassAlias, identityKeyClassProviderVariant}
	case identityKeyClassAlias, identityKeyClassProviderVariant:
		return []identityKeyClass{identityKeyClassLabel}
	default:
		return nil
	}
}

// identityCrossClassRivalClaimant reports whether ANY (class,term) pair
// that produced candidate key's own identity mechanism is ALSO claimed, on
// the IDENTICAL literal term, by a DIFFERENT canonical subject via one of
// identityRivalClasses(class) -- see that function's own doc comment for
// exactly which class pairs count and why. Bound to the PRODUCING term
// only, mirroring identityCollision's own MEDIUM-B discipline (a candidate
// that also matched an unrelated, genuinely-unique term elsewhere is
// unaffected by that term).
//
// A candidate with no identity match terms at all (never touched by any
// identity mechanism) returns false unconditionally -- exactly
// identityCollision's own "untouched candidate" guarantee, so this can
// only ever SUPPRESS a commit path this ticket's own finding concerns, and
// never touches an ordinary lexical/vector-only candidate.
//
// Supplements, never replaces, identityCollision's existing PER-CLASS
// check: every call site below calls both, exactly as resolution.go's
// existing identityCollision call sites already compose with
// identityTrustUnproven.
func identityCrossClassRivalClaimant(key string, claimants identityClaimants, terms identityMatchTerms) bool {
	for _, entry := range terms[key] {
		for _, rival := range identityRivalClasses(entry.class) {
			for rivalKey := range claimants[rival][entry.term] {
				if rivalKey != key {
					return true
				}
			}
		}
	}
	return false
}
