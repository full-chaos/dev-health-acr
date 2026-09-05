package falkorgraph

// CohortPoolTruncationBasis names what happened to the CANDIDATE POOL a
// cohort was assembled from, for one DiscoverContext call.
//
// It is a closed vocabulary rather than a boolean because "true" and "false"
// each have two readings here, and telling them apart is the whole point.
// A pool can be cut by either of two independent retrieval arms, and -- the
// case that produced the defect this vocabulary was added for -- a cut
// full-text arm does NOT always mean a cut pool: when the exhaustive
// org-wide kind census ran and was itself untruncated, it already holds
// every subject of every servable cohort kind, so nothing the full-text arm
// dropped could have been a missing member. Reporting that as `none` would
// leave an operator unable to tell "the search was never truncated" from
// "the search was truncated and the census covered it", which is exactly the
// distinction someone diagnosing a suspicious member count needs.
//
// The members are the exhaustive classification of
// (fulltextTruncated, exactNameTruncated, censusAdmitted) -- see
// cohortPoolTruncation, which is the only producer.
type CohortPoolTruncationBasis string

const (
	// CohortPoolTruncationNone: neither retrieval arm was cut. The pool is
	// everything those arms could find.
	CohortPoolTruncationNone CohortPoolTruncationBasis = "none"
	// CohortPoolTruncationFulltext: the full-text arm returned more matches
	// than the collect budget and the rest were dropped, and no exhaustive
	// census ran to cover the loss. This is the pool the cohort was built
	// from, so the cohort cannot claim completeness.
	CohortPoolTruncationFulltext CohortPoolTruncationBasis = "fulltext"
	// CohortPoolTruncationExactNameCensus: the exhaustive org-wide kind
	// census hit its own row limit, so the census itself is not exhaustive.
	CohortPoolTruncationExactNameCensus CohortPoolTruncationBasis = "exact_name_census"
	// CohortPoolTruncationBothArms: both arms were cut. Reported as its own
	// member rather than collapsed into either, so a run's artifacts say
	// which arms to look at without re-reading source.
	CohortPoolTruncationBothArms CohortPoolTruncationBasis = "fulltext_and_exact_name_census"
	// CohortPoolTruncationFulltextCoveredByCensus: the full-text arm was
	// cut, but the exhaustive census ran and was NOT cut, and the census
	// fetches every kind a cohort can be served for (proven equal to
	// graphrank.ServableCohortKindsForAudit by a test in this package). A
	// member the full-text arm dropped is therefore still in the pool via
	// the census, so the pool is NOT truncated -- the cohort keeps its
	// completeness claim, and the fact that the decision was made at all is
	// visible here rather than being indistinguishable from "no truncation
	// happened".
	CohortPoolTruncationFulltextCoveredByCensus CohortPoolTruncationBasis = "fulltext_covered_by_census"
)

// cohortPoolTruncation classifies one DiscoverContext call's candidate pool
// and reports whether the cohort assembled from it may be missing members.
//
// Pure, and an ALLOW-LIST over the closed input space rather than a
// deny-list with a default: every combination of the three inputs is named
// below, so a fourth retrieval arm cannot silently inherit "not truncated".
//
// censusAdmitted is the gate reader.go already computes for the exact-name
// arm. exactNameTruncated can only be true when censusAdmitted is true (it
// is assigned inside that branch), so the two "census truncated but census
// did not run" rows are unreachable by construction; they are classified
// anyway, conservatively, rather than left to a default.
//
// WHY THE FULL-TEXT ARM IS CONDITIONAL AND THE CENSUS ARM IS NOT. The census
// is a term-free `MATCH (n:Subject) WHERE kind IN (repository, project,
// team)` fetch -- when it completes it holds every candidate of every
// servable cohort kind, so a full-text arm that dropped rows dropped rows
// that are in the pool anyway. When the census is skipped (a subject is
// already committed, or the shape/anchor gate refused) the full-text and
// hop-walk arms ARE the pool, and a cut full-text arm is a real loss of
// candidate members. When the census ran but was itself cut, it is no longer
// exhaustive and cannot cover anything.
func cohortPoolTruncation(fulltextTruncated, exactNameTruncated, censusAdmitted bool) (CohortPoolTruncationBasis, bool) {
	switch {
	case exactNameTruncated && fulltextTruncated:
		return CohortPoolTruncationBothArms, true
	case exactNameTruncated:
		return CohortPoolTruncationExactNameCensus, true
	case fulltextTruncated && censusAdmitted:
		return CohortPoolTruncationFulltextCoveredByCensus, false
	case fulltextTruncated:
		return CohortPoolTruncationFulltext, true
	default:
		return CohortPoolTruncationNone, false
	}
}

// CohortPoolTruncationBasisVocabulary returns every declared member, in
// declaration order.
//
// It exists so a test can quantify over "every value this line can carry"
// instead of over a hand-typed list beside it. A hand list is a SAMPLE: it
// agrees with the implementation by construction until someone adds a member,
// at which point it goes on agreeing and stops covering. The classifier itself
// never calls this, so it cannot become a second, drifting definition of the
// vocabulary.
func CohortPoolTruncationBasisVocabulary() []CohortPoolTruncationBasis {
	return []CohortPoolTruncationBasis{
		CohortPoolTruncationNone,
		CohortPoolTruncationFulltext,
		CohortPoolTruncationExactNameCensus,
		CohortPoolTruncationBothArms,
		CohortPoolTruncationFulltextCoveredByCensus,
	}
}
