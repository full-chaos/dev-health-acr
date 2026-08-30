package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4579 / CHAOS-4531 -- the §1.3 class-conditional gate.
//
// chaos3900_structure_offers.go names this gap in its own words at
// handleOfferMaterial's doc comment: "subject_handle is Missing whenever
// this function runs (P1.C' does not yet implement §1.3's class-conditional
// gate, the same known, accepted scope gap kindOfferMaterial already
// carries)". anchorOfferMaterial carries the identical unconditional
// disclosure on its own axis. Both builders decide their Missing row from
// the CANDIDATE POOL alone; neither has ever been told what CLASS of
// question it is building offers for.
//
// The consequence chris hit on the kiac rig (2026-08-29 19:59 PDT, "huge
// miss, didn't understand plural teams still, asked for a singularity"):
// turn 1 of "What teams are struggling and what are the contributing
// factors?" resolves kind=team correctly, then discloses
// missing=[window, subject_anchor, subject_handle] with EMPTY
// AnchorOptions/HandleOptions -- the UI renders "Which repository,
// project, or team?" and "Which specific item?" with no candidates behind
// either. Turn 2, carrying only the window receipt, then produces the
// correct discovered_cohort answer. The investigation understands the
// plural intent; the clarification planner did not.
//
// Why this is NOT a change to the standing zero-candidates ruling
// (chaos3900_structure_offers.go:1062-1067): that ruling governs
// SUBJECT-BEARING shapes, where an anchor genuinely IS missing and the
// empty options list is the honest "missing-and-helpful, nothing
// offerable" disclosure. Its rationale does not reach a question that has
// no subject axis at all -- there, nothing is missing, so disclosing it as
// Missing is a false statement about the question, not a helpful one. The
// ruling is preserved verbatim for every shape that has a subject axis;
// see TestGateSubjectAxisOffers_SubjectBearingShapeKeepsEmptyOfferedRows.
//
// The signal is REUSED, never re-derived. InterpretedQuestion.Shape is the
// same model-set field graphrank.DiscoveredCohort already gates the whole
// cohort ranking path on (discover.go:256) and the same one
// classStructurallyCompatible/fallbackClass already class the window on
// (chaos3900_window_class.go:81-110). There is deliberately no second
// heuristic here -- no plural-noun scan, no question-text regex. If the
// interpreter's shape is wrong, that is an interpret-stage defect with one
// place to fix it, not a disagreement between two detectors.
//
// CHAOS-4452 (the question-family investigation-planning stage) is the
// structural home for this decision: a family would carry, per class, the
// whole set of axes that are even applicable, and every offer builder
// would consult it rather than each disclosing unconditionally and being
// filtered afterwards. This gate is the narrow fix, deliberately placed at
// the composition boundary so that when 4452 lands it has exactly one
// call-site pair to absorb.

// subjectAxisAbsent reports whether an interpreted shape has NO subject
// axis at all -- the precondition for the §1.3 gate.
//
// discovered_cohort ONLY, and the exclusions are each deliberate:
//
//   - single_subject has one subject to anchor; the standing ruling applies
//     unchanged.
//   - explicit_cohort NAMES its members ("compare team A and team B"), so
//     an anchor/handle is exactly what disambiguates which named things
//     were meant -- it has a subject axis, just a plural one. This is why
//     this predicate is narrower than graphrank.DiscoveredCohort's own
//     two-member gate (discover.go:256), which asks a different question
//     ("may this run cohort ranking?"), not this one ("does this question
//     have a subject to anchor?").
//   - open makes no claim about its own subject structure. Treating it as
//     axis-less would silently suppress the anchor offer for every
//     unclassified question -- far wider than CHAOS-4531's ruled scope
//     ("only for interpreted shapes with no subject axis
//     (discovered_cohort)").
func subjectAxisAbsent(shape InvestigationShape) bool {
	return shape == ShapeDiscoveredCohort
}

// CohortStructureGateOutcome is the closed vocabulary
// EngineTelemetry.RecordCohortStructureGate reports. Closed enum only --
// no question text, no subject identifier, no offer label.
type CohortStructureGateOutcome string

const (
	// CohortStructureGateApplied: the shape had no subject axis AND the
	// material actually carried a subject_anchor and/or subject_handle
	// disclosure, which this gate removed. This is the ONLY outcome that
	// changed what the caller sees.
	CohortStructureGateApplied CohortStructureGateOutcome = "applied"
	// CohortStructureGateNoOp: the shape had no subject axis but the
	// material carried nothing on either axis, so the gate had nothing to
	// remove. Distinguished from "applied" so a reader can tell a gate
	// that FIRED from a gate that merely MATCHED -- without it, a
	// regression that stopped producing anchor material for an unrelated
	// reason would be indistinguishable, in the artifacts alone, from this
	// gate doing its job.
	CohortStructureGateNoOp CohortStructureGateOutcome = "no_op"
	// CohortStructureGateSubjectBearing: the shape HAS a subject axis, so
	// the standing zero-candidates ruling applies and the material passes
	// through byte-identical. Emitted so the gate's denominator is
	// observable: every composed disclosure reports which side of the
	// class decision it landed on, which is what makes
	// "cohort vs subject clarification" a countable split rather than an
	// inference from the absence of a log line.
	CohortStructureGateSubjectBearing CohortStructureGateOutcome = "subject_bearing"
)

// gateSubjectAxisOffers applies the §1.3 class-conditional gate to one
// StructureOfferMaterial and reports which side of the class decision it
// took.
//
// When the shape has no subject axis, BOTH the Missing rows and their
// corresponding option lists are dropped, together. Dropping only the
// Missing row would leave AnchorOptions/HandleOptions on the wire with no
// member asking for them -- a receipt a caller could redeem against a need
// that was never disclosed. Dropping only the options would leave exactly
// the empty-offer shell CHAOS-4579 was filed about. They are one decision.
//
// Every other axis is untouched by construction: expected_kind (a cohort
// question still legitimately narrows WHICH kind of thing the cohort is
// drawn from -- chris's own turn 1 resolved kind=team correctly and that
// is not the defect), window (the one question CHAOS-4579 calls
// legitimate for this shape), and subject_candidate (CHAOS-4012's own
// independent axis, outside CHAOS-4531's ruled scope).
//
// The returned material is a copy on the gated path; the input is never
// mutated, so a caller holding the pre-gate material for telemetry still
// reads the pre-gate truth.
func gateSubjectAxisOffers(material StructureOfferMaterial, shape InvestigationShape) (StructureOfferMaterial, CohortStructureGateOutcome) {
	if !subjectAxisAbsent(shape) {
		return material, CohortStructureGateSubjectBearing
	}
	gated := material
	gated.Missing = nil
	removed := false
	for _, member := range material.Missing {
		if member == contractsv1.ContextFabricStructureNeedSubjectAnchor ||
			member == contractsv1.ContextFabricStructureNeedSubjectHandle {
			removed = true
			continue
		}
		gated.Missing = append(gated.Missing, member)
	}
	if len(material.AnchorOptions) > 0 || len(material.HandleOptions) > 0 {
		removed = true
	}
	gated.AnchorOptions = nil
	gated.HandleOptions = nil
	// AnchorOptionsRequireV2 (CHAOS-4042) is the sole signal that promotes
	// a result to the v2 semantic major, and it means "at least one anchor
	// option on this result needs membership-verify redemption". With every
	// anchor option removed, that statement is false -- leaving it set
	// would mint a v2 result carrying no v2-bearing option at all, and
	// unresolved.go/window.go both dispatch schemaVersion directly off this
	// field.
	gated.AnchorOptionsRequireV2 = false
	if !removed {
		return gated, CohortStructureGateNoOp
	}
	return gated, CohortStructureGateApplied
}

// recordCohortStructureGate is the single telemetry emission point for the
// gate, shared by both call sites so the two can never drift on what they
// report. nil-telemetry safe, exactly like recordStructureNeedsTelemetry.
func recordCohortStructureGate(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, outcome CohortStructureGateOutcome, shape InvestigationShape) {
	if telemetry == nil {
		return
	}
	telemetry.RecordCohortStructureGate(ctx, principal, outcome, shape)
}
