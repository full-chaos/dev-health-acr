package contextfabric

// Admission and comparison over the persisted semantic snapshot.

import (
	"encoding/json"
	"sort"
)

// semanticStateAdmission decides whether a carrier's snapshot can be continued,
// and names the reason when it cannot.
//
// FAIL-CLOSED ON EVERY STATUS BUT ONE. Only an `available` snapshot admits.
// An absent one (a pre-column row, or a turn that recorded a closed absence)
// is its own reason; an unsupported format is a version mismatch, which is
// what it is; everything else -- malformed, oversized, or a status the store
// did not report -- is an invalid snapshot. None of them falls back to the
// plan's family label.
func semanticStateAdmission(stored StoredInvestigationResult, plan *AnswerPlan) ContinuationDecisionReason {
	switch stored.SemanticStateRead {
	case SemanticStateReadAvailable:
		if stored.SemanticState == nil {
			return ContinuationReasonSemanticStateInvalid
		}
	case SemanticStateReadAbsent:
		return ContinuationReasonSemanticStateAbsent
	case SemanticStateReadUnsupportedVersion:
		return ContinuationReasonContextVersionMismatch
	default:
		return ContinuationReasonSemanticStateInvalid
	}
	state := stored.SemanticState
	// THE RECORDED STANDARD MUST BE THE ONE IN FORCE. A snapshot built under
	// another family table, frame derivation table or requirement derivation
	// is not reinterpreted under today's.
	if state.FamilyTableVersion != QuestionFamilyTableVersion ||
		state.FrameVersion != QuestionFrameVersion ||
		state.RequirementDerivationVersion != RequirementDerivationVersion {
		return ContinuationReasonContextVersionMismatch
	}
	// The snapshot and the carrier's own public plan describe ONE reading.
	// The family must agree; the group axis may not (a plan drops its axis
	// when grouping fails on evidence, which is not a change of reading).
	if plan == nil || state.Family != plan.Family {
		return ContinuationReasonSemanticStateInvalid
	}
	return ContinuationReasonNone
}

// semanticStateDifferences reports, per comparable component, whether the two
// readings differ. A reading with no snapshot differs on every component.
func semanticStateDifferences(carried, fresh *PersistedSemanticState) map[ContinuationConflictField]bool {
	differs := map[ContinuationConflictField]bool{}
	if carried == nil || fresh == nil {
		for _, field := range continuationComparableFields() {
			differs[field] = true
		}
		return differs
	}
	frameOf := func(state *PersistedSemanticState) QuestionFrame {
		if state.Frame == nil {
			return QuestionFrame{}
		}
		return *state.Frame
	}
	cf, ff := frameOf(carried), frameOf(fresh)
	differs[ContinuationConflictFieldSubjectExpression] = carried.FramePresent != fresh.FramePresent ||
		!sameJSON(cf.SubjectExpression, ff.SubjectExpression) || carried.GroupKind != fresh.GroupKind
	differs[ContinuationConflictFieldRoles] = !sameJSON(carried.Roles, fresh.Roles)
	differs[ContinuationConflictFieldGoals] = !sameSet(cf.Goals, ff.Goals)
	differs[ContinuationConflictFieldTemporal] = cf.Temporal != ff.Temporal
	differs[ContinuationConflictFieldEmphasis] = !sameSet(cf.Emphasis, ff.Emphasis)
	differs[ContinuationConflictFieldDimensions] = !sameSet(cf.Dimensions, ff.Dimensions)
	differs[ContinuationConflictFieldObligations] = !sameSet(cf.Obligations, ff.Obligations)
	differs[ContinuationConflictFieldWidenedObligations] = !sameSet(cf.WidenedObligations, ff.WidenedObligations)
	differs[ContinuationConflictFieldRequirements] = carried.RequirementsDeclared != fresh.RequirementsDeclared ||
		!sameJSON(carried.Requirements, fresh.Requirements)
	differs[ContinuationConflictFieldScopeAnchor] = carried.ScopeAnchor != fresh.ScopeAnchor
	differs[ContinuationConflictFieldEmittedShape] = carried.Validation.EmittedShape != fresh.Validation.EmittedShape
	differs[ContinuationConflictFieldFrameGate] = carried.Validation.GateOutcome != fresh.Validation.GateOutcome ||
		carried.Validation.FailedInvariant != fresh.Validation.FailedInvariant ||
		carried.Validation.RefuseBasis != fresh.Validation.RefuseBasis ||
		carried.Validation.DeclaredMemberKind != fresh.Validation.DeclaredMemberKind
	return differs
}

// sameJSON compares two values by their encodings. Arrays keep their order:
// operand order and derivation order are meaningful.
func sameJSON(a, b any) bool {
	ae, aerr := json.Marshal(a)
	be, berr := json.Marshal(b)
	return aerr == nil && berr == nil && string(ae) == string(be)
}

// sameSet compares two closed-vocabulary sets order-insensitively.
func sameSet[T ~string](a, b []T) bool {
	canonical := func(values []T) []string {
		out := make([]string, 0, len(values))
		seen := map[T]bool{}
		for _, value := range values {
			if !seen[value] {
				seen[value] = true
				out = append(out, string(value))
			}
		}
		sort.Strings(out)
		return out
	}
	ac, bc := canonical(a), canonical(b)
	if len(ac) != len(bc) {
		return false
	}
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
