package falkorgraph

import "strings"

// What happened to the CANDIDATE POOL a cohort was assembled from, for one
// DiscoverContext call, reported as TWO closed vocabularies rather than one.
//
// WHY TWO. There are three independent retrieval arms that can clip the pool
// (the full-text search, the committed-origin hop walk, and the exhaustive
// exact-name census), and an operator needs two different answers about them:
// "may this cohort be missing members" and "which arm cut it". A single
// vocabulary naming arm COMBINATIONS answers both at once and grows 2^n with
// the arms -- it is a cross-product wearing a vocabulary's clothes, and the
// third arm is what made that visible. Split, each vocabulary stays small,
// genuinely closed, and independently exhaustive.
//
// CohortPoolTruncationBasis is the DECISION: does the cohort's completeness
// claim survive. Three members, and the third exists because a cut arm does
// not always mean a cut pool -- see CohortPoolTruncationCoveredByCensus.
type CohortPoolTruncationBasis string

const (
	// CohortPoolTruncationNone: no arm was cut. The pool is everything the
	// arms could find.
	CohortPoolTruncationNone CohortPoolTruncationBasis = "none"
	// CohortPoolTruncationTruncated: at least one arm was cut and nothing
	// covers the loss, so a kind-matching subject may exist that this cohort
	// does not carry. The cohort cannot claim completeness.
	CohortPoolTruncationTruncated CohortPoolTruncationBasis = "truncated"
	// CohortPoolTruncationCoveredByCensus: an arm was cut, but the exhaustive
	// org-wide kind census ran and was NOT itself cut, and that census
	// fetches every kind a cohort can be served for (proven equal to
	// graphrank.ServableCohortKindsForAudit by a test in this package). A
	// member a bounded arm dropped is therefore still in the pool via the
	// census, so the pool is NOT truncated -- the cohort keeps its
	// completeness claim, and the fact that the decision was made at all is
	// visible here rather than being indistinguishable from "nothing was
	// cut".
	CohortPoolTruncationCoveredByCensus CohortPoolTruncationBasis = "covered_by_census"
)

// CohortPoolTruncationArm names ONE retrieval arm that was cut. The arms are
// reported beside the decision so a diagnosis says WHERE to look without
// re-reading source; adding a fourth arm adds one member here and changes
// nothing about the decision vocabulary.
type CohortPoolTruncationArm string

const (
	// CohortPoolTruncationArmFulltext: the lexical search matched more rows
	// than the collect budget and the remainder were dropped.
	CohortPoolTruncationArmFulltext CohortPoolTruncationArm = "fulltext"
	// CohortPoolTruncationArmHopWalk: the committed-origin walk spent its
	// edge budget with candidate edges or an unvisited frontier still
	// outstanding. Their endpoints are lost to the cohort, because a
	// neighbour is only ever discovered through an admitted edge -- see
	// hopWalk's own doc comment.
	//
	// A SPENT BUDGET IS TRUNCATION; A DEPTH BOUND IS NOT. The walk's maxHops
	// limit DEFINES the pool -- what lies beyond two hops is out of scope,
	// not missing -- while a spent collect budget with an unvisited frontier
	// CLIPS a pool that was in scope. WHEN BOTH BIND AT ONCE THIS DISCLOSES,
	// because the budget's loss is real either way; an attempt to suppress the
	// both-bind case was reverted, since it re-introduced exactly the
	// over-claim this arm exists to remove. That
	// distinction is the semantic the count step leans on: it is what makes
	// "not truncated" mean "nothing in scope was dropped" rather than
	// "nothing at all exists beyond what you see".
	CohortPoolTruncationArmHopWalk CohortPoolTruncationArm = "hop_walk"
	// CohortPoolTruncationArmExactNameCensus: the exhaustive org-wide kind
	// census hit its own row limit, so the census itself is not exhaustive
	// and cannot cover anything.
	CohortPoolTruncationArmExactNameCensus CohortPoolTruncationArm = "exact_name_census"
	// CohortPoolTruncationArmEndpointLookupFailed: a neighbour the walk had
	// already reached through an admitted edge could not be READ BACK -- its
	// bookkeeping lookup errored -- so it never entered the visited set and
	// never became a cohort member.
	//
	// ITS OWN ARM, not folded into hop_walk. A clipped budget and an
	// unconfirmable member are different causes with different remedies: the
	// first says "raise the budget or narrow the question", the second says
	// "the backend failed on a specific subject". Collapsing them would make
	// a transient backend fault read as a capacity problem.
	//
	// The COHORT-LEVEL consequence is identical, which is why it belongs here
	// at all: an in-scope, kind-matching subject exists that this cohort does
	// not carry, so the completeness claim cannot stand. Coverage.Partial
	// already reported the failure (see reader.go's endpoint_lookup_failed
	// reason and the matching contract detail code) -- what was missing is
	// that the same event is ALSO a lost member, and Partial beside
	// Complete=true is a contradiction a reader cannot resolve.
	CohortPoolTruncationArmEndpointLookupFailed CohortPoolTruncationArm = "endpoint_lookup_failed"
)

// CohortPoolTruncationBasisVocabulary returns every declared decision member,
// in declaration order.
//
// It exists so a test can quantify over "every value this line can carry"
// instead of over a hand-typed list beside it. A hand list is a SAMPLE: it
// agrees with the implementation by construction until someone adds a member,
// at which point it goes on agreeing and stops covering. The classifier never
// calls this, so it cannot become a second, drifting definition.
func CohortPoolTruncationBasisVocabulary() []CohortPoolTruncationBasis {
	return []CohortPoolTruncationBasis{
		CohortPoolTruncationNone,
		CohortPoolTruncationTruncated,
		CohortPoolTruncationCoveredByCensus,
	}
}

// CohortPoolTruncationArmVocabulary returns every declared arm, in the fixed
// order the reported list uses. The order is part of the contract: a caller
// diffing two lines must not see a difference that is only iteration order.
func CohortPoolTruncationArmVocabulary() []CohortPoolTruncationArm {
	return []CohortPoolTruncationArm{
		CohortPoolTruncationArmFulltext,
		CohortPoolTruncationArmHopWalk,
		CohortPoolTruncationArmExactNameCensus,
		CohortPoolTruncationArmEndpointLookupFailed,
	}
}

// cohortPoolTruncation classifies one DiscoverContext call's candidate pool
// and reports whether the cohort assembled from it may be missing members.
//
// Pure. Returns the decision, the cut arms in vocabulary order, and the
// boolean the cohort producer consumes.
//
// WHY THE CENSUS COVERS THE OTHER TWO ARMS AND NOTHING COVERS THE CENSUS. The
// census is a term-free `MATCH (n:Subject) WHERE kind IN (repository,
// project, team)` fetch: when it completes it holds every candidate of every
// servable cohort kind, so rows the bounded arms dropped are in the pool
// anyway. When the census is skipped (a subject is already committed, or the
// shape/anchor gate refused) the bounded arms ARE the pool. When the census
// ran but was itself cut, it is no longer exhaustive and covers nothing.
//
// UNREACHABLE BY CONSTRUCTION, classified anyway. `censusAdmitted` requires
// ZERO committed subjects and `hopWalkTruncated` requires at least one (the
// walk only runs per committed subject), so the two are mutually exclusive in
// production; `exactNameTruncated` is likewise only ever assigned inside the
// census branch. Those combinations are still named below rather than left to
// a default, because a classification over a closed space is an allow-list and
// a future caller that reaches one should inherit a stated answer.
func cohortPoolTruncation(fulltextTruncated, hopWalkTruncated, exactNameTruncated, endpointLookupFailed, censusAdmitted bool) (CohortPoolTruncationBasis, []CohortPoolTruncationArm, bool) {
	arms := make([]CohortPoolTruncationArm, 0, len(CohortPoolTruncationArmVocabulary()))
	if fulltextTruncated {
		arms = append(arms, CohortPoolTruncationArmFulltext)
	}
	if hopWalkTruncated {
		arms = append(arms, CohortPoolTruncationArmHopWalk)
	}
	if exactNameTruncated {
		arms = append(arms, CohortPoolTruncationArmExactNameCensus)
	}
	if endpointLookupFailed {
		arms = append(arms, CohortPoolTruncationArmEndpointLookupFailed)
	}
	switch {
	case len(arms) == 0:
		return CohortPoolTruncationNone, arms, false
	case exactNameTruncated:
		// The census is cut, so it covers nothing -- including itself.
		return CohortPoolTruncationTruncated, arms, true
	case endpointLookupFailed:
		// NOT coverable by the census. The census covers a BOUNDED arm -- a
		// subject that exists and was simply not fetched, which a term-free
		// org-wide fetch will return anyway. A failed read is different: the
		// backend did not answer for that subject, and there is no reason to
		// believe a second read of the same subject in the same call
		// succeeded. Treating it as covered would turn a backend fault into a
		// completeness claim.
		return CohortPoolTruncationTruncated, arms, true
	case censusAdmitted:
		// Only bounded arms were cut and a COMPLETE census ran beside them.
		return CohortPoolTruncationCoveredByCensus, arms, false
	default:
		return CohortPoolTruncationTruncated, arms, true
	}
}

// formatCohortPoolTruncationArms renders the cut arms for the telemetry line:
// a comma-joined list in VOCABULARY order, and the empty string when no arm
// was cut.
//
// It NORMALIZES rather than joining what it was handed. The first version
// joined the caller's slice directly and its own doc comment claimed
// vocabulary order anyway -- true only because cohortPoolTruncation happens to
// build the slice in that order today. Any other caller, or a reordering of
// that construction, would emit a different string for an identical outcome,
// and two log lines describing the same state would not compare equal. The
// order is part of this key's contract, so it is enforced here, at the one
// place the string is produced, rather than assumed of every producer.
//
// Normalizing by ITERATING THE VOCABULARY collapses a repeated arm and keeps
// an unpublished value off the line as a value. It does NOT drop it silently:
// an undeclared arm renders as the single token
// `cohortPoolTruncationUnknownArm` appended after the declared ones.
//
// The first version dropped it. That was wrong for the reason this whole
// change exists: a signal that disappears is indistinguishable from a signal
// that was never produced, so a caller emitting a value this vocabulary does
// not know would have looked exactly like a caller emitting nothing. The
// marker is deliberately NOT a vocabulary member -- adding one would make the
// malformed case a legal classification. It is a fixed token that says "a
// value arrived here that this vocabulary cannot name", which is a different
// statement from any arm.
//
// The empty string is deliberate. A placeholder like "none" would collide with
// the DECISION vocabulary's own `none` on a different key, and an operator
// grepping one key's value should never match the other's.
func formatCohortPoolTruncationArms(arms []CohortPoolTruncationArm) string {
	if len(arms) == 0 {
		return ""
	}
	declared := make(map[CohortPoolTruncationArm]struct{}, len(CohortPoolTruncationArmVocabulary()))
	for _, arm := range CohortPoolTruncationArmVocabulary() {
		declared[arm] = struct{}{}
	}
	present := make(map[CohortPoolTruncationArm]struct{}, len(arms))
	unknown := false
	for _, arm := range arms {
		if _, ok := declared[arm]; !ok {
			unknown = true
			continue
		}
		present[arm] = struct{}{}
	}
	parts := make([]string, 0, len(arms)+1)
	for _, arm := range CohortPoolTruncationArmVocabulary() {
		if _, cut := present[arm]; cut {
			parts = append(parts, string(arm))
		}
	}
	if unknown {
		parts = append(parts, cohortPoolTruncationUnknownArm)
	}
	return strings.Join(parts, ",")
}

// cohortPoolTruncationUnknownArm is what an undeclared arm renders as: one
// fixed, greppable token, never the value itself and never silence.
//
// It is NOT a member of CohortPoolTruncationArmVocabulary and must never
// become one -- a marker that says "this vocabulary could not name what it was
// handed" stops being that the moment it is itself nameable.
const cohortPoolTruncationUnknownArm = "unknown_arm"
