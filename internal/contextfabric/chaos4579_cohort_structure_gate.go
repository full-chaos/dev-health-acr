package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4634 (S4 of the CHAOS-4452 intent-engine design, §6.4/§8) --
// SUBSUMES CHAOS-4579 / CHAOS-4531's §1.3 class-conditional gate below.
//
// The original gate (GateSubjectAxisOffers, subjectAxisAbsent) filtered
// AFTER the offer builders had already run, and it only ever knew ONE
// binary: does this Shape have a subject axis at all. That binary is
// `subjectAxisAbsent`'s own `shape == ShapeDiscoveredCohort` check --
// narrow by design (CHAOS-4531's ruled scope), and it left two gaps its
// own doc comment named as future work: `kindOfferMaterial`'s class gap
// (a cohort question still unconditionally offers a kind axis) and
// `subject_candidate` (CHAOS-4012's independent axis, never touched by
// this gate at all -- exactly the CHAOS-4622 §2 defect where Q-B was
// asked to pick a single CI-run candidate on a question with no single
// subject).
//
// GateOffersByFamily replaces it with ONE lookup into the family table
// (chaos4632_question_family_registry.go's ApplicableAxes column) that
// decides ALL FIVE structure axes at once -- kind, anchor, handle,
// candidate, window -- rather than a hand-picked pair. It is family-keyed,
// not Shape-keyed: Shape is unstable across replicates of the identical
// question (design §4.2's own six-replicate measurement; Q-B's own two
// replicates disagreed on Shape while the resolved family did not), so a
// gate that still read Shape directly would inherit exactly that
// instability. The two production call sites (chaos4234_offers_only.go's
// gatedOfferMaterial, unresolved.go's terminalResult) now call this one
// function; see each call site's own comment for why both are required.
//
// scope_anchor (declared on the family table, not yet a wire vocabulary
// member -- chaos4632_question_family_registry.go's own doc comment)
// deliberately maps onto the EXISTING wire subject_anchor axis here: Q-B's
// own scoping ambiguity ("Fullchaos" vs "fullchaos") is disclosed through
// the SAME AnchorOptions vehicle a subject anchor already uses, because S4
// makes NO contract change (CHAOS-4634's own "no new field, no new enum").
// group_kind has no such existing wire vehicle to borrow, so it maps to
// nothing here -- a family whose ApplicableAxes names only group_kind and
// window (grouped_cohort_status) gates every kind/anchor/handle/candidate
// builder off, exactly as if only window were declared, until a wire
// vehicle for group_kind exists (S5+).
//
// The standing zero-candidates ruling (chaos3900_structure_offers.go:
// 1062-1067) is UNCHANGED: this gate decides which axes are eligible to be
// disclosed at all, never whether an eligible axis with nothing to offer
// still discloses itself as Missing with an empty options list.

// allWireStructureNeedAxes is the CLOSED set of structure axes that exist
// on the wire today (contractsv1.ContextFabricStructureNeedKindVocabulary,
// minus explicit_cohort's own non-axis members -- these five are exactly
// the ones StructureOfferMaterial ever populates: KindOptions,
// AnchorOptions, HandleOptions, CandidateOptions, and the window member
// composeGatedStructureNeeds/composeWindowClarification mint directly).
// Declared here, not derived from the wire vocabulary function, because
// this gate's own contract is these five and only these five -- a future
// wire addition must be a deliberate decision about this gate too, never
// an automatic inclusion.
var allWireStructureNeedAxes = [...]contractsv1.ContextFabricStructureNeedKind{
	contractsv1.ContextFabricStructureNeedExpectedKind,
	contractsv1.ContextFabricStructureNeedSubjectAnchor,
	contractsv1.ContextFabricStructureNeedSubjectHandle,
	contractsv1.ContextFabricStructureNeedSubjectCandidate,
	contractsv1.ContextFabricStructureNeedWindow,
}

// wireApplicableAxes projects a family definition's ApplicableAxes column
// onto the wire vocabulary: every declared axis that already has a wire
// member maps to itself, and StructureNeedScopeAnchor additionally maps
// onto subject_anchor (see this file's own package doc comment above for
// why). StructureNeedGroupKind maps onto nothing.
func wireApplicableAxes(def QuestionFamilyDefinition) map[contractsv1.ContextFabricStructureNeedKind]bool {
	out := make(map[contractsv1.ContextFabricStructureNeedKind]bool, len(def.ApplicableAxes)+1)
	for _, axis := range def.ApplicableAxes {
		out[axis] = true
		if axis == StructureNeedScopeAnchor {
			out[contractsv1.ContextFabricStructureNeedSubjectAnchor] = true
		}
	}
	return out
}

// familyRestrictsWireAxes reports whether def excludes at least one of the
// five wire structure axes -- the family-keyed generalization of the old
// subjectAxisAbsent(shape) predicate. unclassified (ApplicableAxes = every
// axis, including both not-yet-wire members) and every family whose
// ApplicableAxes covers all five wire axes (subject_investigation,
// explicit_comparison) report false here, which is what keeps Q1
// (subject_investigation) byte-identical: GateOffersByFamily short-circuits
// to CohortStructureGateSubjectBearing without ever comparing material.
func familyRestrictsWireAxes(def QuestionFamilyDefinition) bool {
	applicable := wireApplicableAxes(def)
	for _, axis := range allWireStructureNeedAxes {
		if !applicable[axis] {
			return true
		}
	}
	return false
}

// CohortStructureGateOutcome is the closed vocabulary
// EngineTelemetry.RecordCohortStructureGate reports. Closed enum only --
// no question text, no subject identifier, no offer label. Unchanged by
// CHAOS-4634 (same three values, same telemetry sink contract) -- only
// WHAT decides which outcome fires generalized from a Shape binary to a
// family lookup covering five axes instead of two.
type CohortStructureGateOutcome string

const (
	// CohortStructureGateApplied: the family excludes at least one wire
	// axis the material actually carried a Missing row and/or option list
	// for, and this gate removed it. This is the ONLY outcome that changed
	// what the caller sees.
	CohortStructureGateApplied CohortStructureGateOutcome = "applied"
	// CohortStructureGateNoOp: the family excludes at least one wire axis,
	// but the material carried nothing on any excluded axis, so the gate
	// had nothing to remove. Distinguished from "applied" so a reader can
	// tell a gate that FIRED from a gate that merely MATCHED -- without
	// it, a regression that stopped producing offer material for an
	// unrelated reason would be indistinguishable, in the artifacts alone,
	// from this gate doing its job.
	CohortStructureGateNoOp CohortStructureGateOutcome = "no_op"
	// CohortStructureGateSubjectBearing: the family places NO restriction
	// on any of the five wire axes (subject_investigation,
	// explicit_comparison, unclassified today), so the material passes
	// through byte-identical. Emitted so the gate's denominator is
	// observable: every composed disclosure reports which side of the
	// family decision it landed on, which is what makes "cohort vs
	// subject clarification" a countable split rather than an inference
	// from the absence of a log line.
	CohortStructureGateSubjectBearing CohortStructureGateOutcome = "subject_bearing"
)

// GateOffersByFamily applies the family's ApplicableAxes to one
// StructureOfferMaterial and reports which side of the family decision it
// took. See this file's own package doc comment for the full CHAOS-4634
// rationale; SUBSUMES CHAOS-4579/CHAOS-4531's GateSubjectAxisOffers.
//
// When the family restricts at least one wire axis, EVERY excluded axis's
// Missing row and option list are dropped TOGETHER, exactly mirroring the
// predecessor gate's own "one decision, not two" discipline: dropping only
// the Missing row would leave options on the wire with no member asking
// for them (a receipt redeemable against a need never disclosed); dropping
// only the options would leave an empty-offer shell.
//
// outcome.Family absent from the registry (should be structurally
// impossible -- the registry test asserts every vocabulary member has a
// row) degrades to CohortStructureGateSubjectBearing: an unrecognized
// family must never silently over-gate a real disclosure.
//
// The returned material is a copy on the gated path; the input is never
// mutated, so a caller holding the pre-gate material for telemetry still
// reads the pre-gate truth (same contract GateSubjectAxisOffers's own doc
// comment made, and the same reason: the CHAOS-3884 replay harness reads
// ResolveSubjects' StructureOfferMaterial directly).
func GateOffersByFamily(material StructureOfferMaterial, outcome QuestionFamilyOutcome) (StructureOfferMaterial, CohortStructureGateOutcome) {
	def, ok := LookupQuestionFamily(outcome.Family)
	if !ok || !familyRestrictsWireAxes(def) {
		return material, CohortStructureGateSubjectBearing
	}
	applicable := wireApplicableAxes(def)
	gated := material
	gated.Missing = nil
	removed := false
	for _, member := range material.Missing {
		if !applicable[member] {
			removed = true
			continue
		}
		gated.Missing = append(gated.Missing, member)
	}
	if applicable[contractsv1.ContextFabricStructureNeedExpectedKind] {
		gated.KindOptions = material.KindOptions
	} else {
		if len(material.KindOptions) > 0 {
			removed = true
		}
		gated.KindOptions = nil
	}
	if applicable[contractsv1.ContextFabricStructureNeedSubjectAnchor] {
		gated.AnchorOptions = material.AnchorOptions
		gated.AnchorOptionsRequireV2 = material.AnchorOptionsRequireV2
	} else {
		if len(material.AnchorOptions) > 0 {
			removed = true
		}
		gated.AnchorOptions = nil
		// AnchorOptionsRequireV2 (CHAOS-4042) is the sole signal that
		// promotes a result to the v2 semantic major, and it means "at
		// least one anchor option on this result needs membership-verify
		// redemption". With every anchor option removed, that statement
		// is false -- leaving it set would mint a v2 result carrying no
		// v2-bearing option at all, and unresolved.go/window.go both
		// dispatch schemaVersion directly off this field.
		gated.AnchorOptionsRequireV2 = false
	}
	if applicable[contractsv1.ContextFabricStructureNeedSubjectHandle] {
		gated.HandleOptions = material.HandleOptions
	} else {
		if len(material.HandleOptions) > 0 {
			removed = true
		}
		gated.HandleOptions = nil
	}
	if applicable[contractsv1.ContextFabricStructureNeedSubjectCandidate] {
		gated.CandidateOptions = material.CandidateOptions
	} else {
		if len(material.CandidateOptions) > 0 {
			removed = true
		}
		gated.CandidateOptions = nil
	}
	if !removed {
		return gated, CohortStructureGateNoOp
	}
	return gated, CohortStructureGateApplied
}

// recordCohortStructureGate is the single telemetry emission point for the
// gate, shared by both call sites so the two can never drift on what they
// report. nil-telemetry safe, exactly like recordStructureNeedsTelemetry.
//
// Unchanged signature (still `shape`, not `family`) -- CHAOS-4634's own
// ticket requires the event and its sink assertions to "survive or be
// superseded with equivalent countability", and Shape is still available
// and still a meaningful telemetry dimension (it lets an operator compare
// what the model reported against what the family gate actually decided).
// The DECISION is family-keyed; the REPORTED dimension stays Shape.
func recordCohortStructureGate(ctx context.Context, telemetry EngineTelemetry, principal storage.Principal, outcome CohortStructureGateOutcome, shape InvestigationShape) {
	if telemetry == nil {
		return
	}
	telemetry.RecordCohortStructureGate(ctx, principal, outcome, shape)
}
