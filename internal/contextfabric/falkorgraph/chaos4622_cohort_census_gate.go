package falkorgraph

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// CohortExactNameCensusBasis is the closed-vocabulary basis
// RecordCohortExactNameCensusGate reports for ONE DiscoverContext call's
// exact-name org-wide census admission decision.
type CohortExactNameCensusBasis string

const (
	// CohortExactNameCensusBasisDiscoveredKind is CHAOS-4395's original
	// basis, re-keyed by seam 7 (CHAOS-4736) from Shape onto the frame:
	// the subject expression is `discovered_kind`, which IS "no term to
	// match, give me the kind's whole census." Always admits (subject to
	// the separate Resolution.Committed check the caller applies).
	//
	// THE VALUE CHANGED with the field it reads. It was
	// "discovered_cohort_shape", naming a Shape this gate no longer
	// consults; a telemetry value that describes a deleted mechanism is
	// the same rot as a comment naming a deleted symbol. The
	// vocabulary change is called out in seam 7's PR body.
	CohortExactNameCensusBasisDiscoveredKind CohortExactNameCensusBasis = "discovered_kind_expression"
	// CohortExactNameCensusBasisFrameAbsent: no validated frame reached
	// DiscoverContext, so there is no topology to gate on. DENIES.
	//
	// The old gate could not produce this outcome -- Shape is always
	// present, so the gate always had an answer, even when that answer was
	// derived from the least stable field in the interpretation. Denying
	// here is the same fail-closed choice cohort discovery itself makes for
	// an absent frame, and counting it is how the cost shows up.
	CohortExactNameCensusBasisFrameAbsent CohortExactNameCensusBasis = "frame_absent"
	// CohortExactNameCensusBasisAnchorUnset is CHAOS-4622's remainder fix,
	// re-keyed onto the frame: the expression is a cohort variant OTHER
	// than discovered_kind, and the winning interpretation sample named no
	// specific subject (ScopeAnchorTerm unset). Admits.
	//
	// CHAOS-4622's own root cause is WHY this row survives the re-keying
	// unchanged in meaning: CHAOS-4395's carve-out assumed explicit_cohort
	// always means a named member, which was false whenever Shape itself
	// was the unstable variable (a bare-kind-noun survey like "which teams
	// are struggling" landed on explicit_cohort some replicates and
	// discovered_cohort others). Reading the union removes that
	// instability at the source, but the anchor half of the rule is about
	// whether a subject was NAMED, which the anchor signal still answers.
	CohortExactNameCensusBasisAnchorUnset CohortExactNameCensusBasis = "cohort_expression_anchor_unset"
	// CohortExactNameCensusBasisAnchorSet is the preserved half of CHAOS-
	// 4395's carve-out: a cohort variant AND the winning sample named a
	// specific subject (ScopeAnchorTerm set) -- exactly the "compare the
	// frontend and backend teams" case the carve-out exists to protect:
	// admitting the org-wide census here would widen a question naming
	// specific members into every member in the org. Denies.
	CohortExactNameCensusBasisAnchorSet CohortExactNameCensusBasis = "cohort_expression_anchor_set"
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
func cohortExactNameCensusEligibility(frame *contextfabric.QuestionFrame, scopeAnchorResolved bool) (eligible bool, basis CohortExactNameCensusBasis) {
	if frame == nil {
		return false, CohortExactNameCensusBasisFrameAbsent
	}
	expression := frame.SubjectExpression
	switch {
	case expression.Kind == contextfabric.SubjectExpressionDiscoveredKind:
		return true, CohortExactNameCensusBasisDiscoveredKind
	case !expression.IsCohortVariant():
		return false, ""
	case !scopeAnchorResolved:
		return true, CohortExactNameCensusBasisAnchorUnset
	default:
		return false, CohortExactNameCensusBasisAnchorSet
	}
}
