package falkorgraph

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// CohortExactNameCensusBasis is the closed-vocabulary basis
// RecordCohortExactNameCensusGate reports for ONE DiscoverContext call's
// exact-name org-wide census admission decision.
type CohortExactNameCensusBasis string

const (
	// CohortExactNameCensusBasisDiscoveredCohortShape is CHAOS-4395's
	// original, unchanged basis: Shape itself already said "no term to
	// match, give me the kind's whole census." Always admits (subject to
	// the separate Resolution.Committed check the caller applies).
	CohortExactNameCensusBasisDiscoveredCohortShape CohortExactNameCensusBasis = "discovered_cohort_shape"
	// CohortExactNameCensusBasisAnchorUnset is CHAOS-4622's remainder fix:
	// Shape resolved to explicit_cohort, but the winning interpretation
	// sample named no specific subject (ScopeAnchorTerm unset) -- CHAOS-
	// 4395's own carve-out assumed explicit_cohort always means a named
	// member, which CHAOS-4622 traced as false whenever Shape itself is
	// the unstable variable (a bare-kind-noun cohort survey like "which
	// teams are struggling" non-deterministically lands on explicit_cohort
	// some replicates and discovered_cohort others). Admits.
	CohortExactNameCensusBasisAnchorUnset CohortExactNameCensusBasis = "explicit_cohort_anchor_unset"
	// CohortExactNameCensusBasisAnchorSet is the preserved half of CHAOS-
	// 4395's carve-out: Shape resolved to explicit_cohort AND the winning
	// sample named a specific subject (ScopeAnchorTerm set) -- exactly the
	// "compare the frontend and backend teams" case the carve-out exists
	// to protect: admitting the org-wide census here would widen a
	// question naming specific members into every member in the org.
	// Denies.
	CohortExactNameCensusBasisAnchorSet CohortExactNameCensusBasis = "explicit_cohort_anchor_set"
	// CohortExactNameCensusBasisAlreadyCommitted is codex round-2's
	// existing finding (reader.go): Shape/anchor said the census would be
	// eligible, but request.Resolution already carries a committed
	// subject (an exact hint, a prior-turn carry-over) -- appending the
	// org-wide census onto an already-anchored request would widen a
	// subject-specific investigation into an organization-wide cohort.
	// Denies. Reported only when the Shape/anchor half would otherwise
	// have admitted -- an ordinary single-subject investigation with a
	// committed subject is not this gate's concern at all (see
	// cohortExactNameCensusEligibility's own basis="" case) and is never
	// reported through this basis.
	CohortExactNameCensusBasisAlreadyCommitted CohortExactNameCensusBasis = "already_committed"
)

// cohortExactNameCensusEligibility decides whether the exact-name org-wide
// census (chaos4348ExactNameCandidates) may run for this DiscoverContext
// call, and the closed-vocabulary basis for the decision -- Shape and
// ScopeAnchorResolved only, never Resolution.Committed (kept out of this
// function: "already committed" is Resolution-derived, a materially
// different concern from Shape/anchor, and the caller applies it as a
// separate, independent check -- codex round-2's own P1 finding already
// established the two checks must not be merged into one condition).
//
// scopeAnchorResolved is CHAOS-4632's ScopeAnchorTerm signal, reduced to
// whether it resolved -- see GraphDiscoveryRequest.ScopeAnchorResolved's
// own doc comment for why only the bool crosses this boundary, never the
// term text.
//
// A shape outside {discovered_cohort, explicit_cohort} returns
// (false, "") -- this gate has nothing to decide for an ordinary
// single-subject or open-shaped investigation, and "" is the caller's own
// signal to skip telemetry for it (see RecordCohortExactNameCensusGate's
// own doc comment: never called for a non-cohort Shape).
func cohortExactNameCensusEligibility(shape contextfabric.InvestigationShape, scopeAnchorResolved bool) (eligible bool, basis CohortExactNameCensusBasis) {
	switch {
	case shape == contextfabric.ShapeDiscoveredCohort:
		return true, CohortExactNameCensusBasisDiscoveredCohortShape
	case shape == contextfabric.ShapeExplicitCohort && !scopeAnchorResolved:
		return true, CohortExactNameCensusBasisAnchorUnset
	case shape == contextfabric.ShapeExplicitCohort:
		return false, CohortExactNameCensusBasisAnchorSet
	default:
		return false, ""
	}
}
