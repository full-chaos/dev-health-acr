package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// workItemTupleAdmission is the prospective A decision from D-WI v5 §1.
// The ordinary frame validator owns validity. Tuple admission adds the
// family and interpreted time-axis conditions before subject resolution.
//
// The prospective state permits project-anchor resolution only. It does not
// assert that an anchor exists or is authorized: stage C must establish both
// under the current principal and requested scope before membership is read.
// Design: https://linear.app/fullchaos/document/intent-engine-design-of-record-volume-3-from-2026-09-10-fb417f8b3773
// (D-WI, Scope and admission).
type workItemTupleAdmission uint8

const (
	workItemTupleNotApplicable workItemTupleAdmission = iota
	workItemTupleRefused
	workItemTupleProspective
)

// prospectiveWorkItemTupleAdmission evaluates only this tuple's A conditions.
// Other expressions and member kinds retain their existing admission paths.
// A present but unrecognized qualifier is not unqualified membership.
// The family prerequisite is registry policy resolved by LookupQuestionFamily
// at the initial, final routing, or post-carry composition point.
func prospectiveWorkItemTupleAdmission(frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext) workItemTupleAdmission {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil || frame.SubjectExpression.Scoped.MemberKind != SubjectWorkItem {
		return workItemTupleNotApplicable
	}
	if !familyAllowsWorkItemTuple || len(frame.Goals) == 0 || frame.Temporal != TemporalIntentCurrent || timeContext.Axis != TemporalCurrent || MemberQualifierPresent(frame.SubjectExpression.Scoped.MemberQualifier) {
		return workItemTupleRefused
	}
	for _, goal := range frame.Goals {
		if goal != GoalAssessState && goal != GoalCountOrAggregate {
			return workItemTupleRefused
		}
	}
	return workItemTupleProspective
}

// anchorAnswerable is stage B's prospective kind match, not a committed or
// authorized SubjectRef. A team-to-work-item relation is not membership.
func (admission workItemTupleAdmission) anchorAnswerable(kind SubjectKind) bool {
	return admission == workItemTupleProspective && kind == SubjectProject
}

// refusalBasis uses the shipped fallback until the proposed axis/filter
// refusal tokens are approved. No new public token is introduced here.
func (admission workItemTupleAdmission) refusalBasis() contractsv1.ContextFabricRefusalBasis {
	if admission == workItemTupleRefused {
		return contractsv1.ContextFabricRefusalBasisMemberKindUnservable
	}
	return contractsv1.ContextFabricRefusalBasisUnspecified
}

// workItemTupleFrameGate refines only the tuple's existing kind refusal.
// Invalid frames and unrelated gate outcomes retain precedence.
func workItemTupleFrameGate(gate FrameGate, frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext) FrameGate {
	if gate.Outcome == FrameGateNotProposed || gate.Outcome == FrameGateRejectedInvalid || (gate.Refuses() && !(gate.Outcome == FrameGateRefusedBasis && gate.RefuseBasis == CohortMemberKindUnservable && gate.DeclaredMemberKind == SubjectWorkItem)) {
		return gate
	}
	admission := prospectiveWorkItemTupleAdmission(frame, familyAllowsWorkItemTuple, timeContext)
	switch admission {
	case workItemTupleRefused:
		return FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectWorkItem}
	case workItemTupleProspective:
		if gate.Outcome == FrameGateRefusedBasis && gate.RefuseBasis == CohortMemberKindUnservable && gate.DeclaredMemberKind == SubjectWorkItem {
			return FrameGate{Outcome: FrameGatePassed}
		}
	}
	return gate
}

// tightenWorkItemTupleFrameGate checks the final family and effective time.
// Only initial frame admission may replace the ordinary kind refusal. Later
// routing and family-only carry cannot promote a refusal to admission.
func tightenWorkItemTupleFrameGate(gate FrameGate, frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext) FrameGate {
	if gate.Refuses() {
		return gate
	}
	return workItemTupleFrameGate(gate, frame, familyAllowsWorkItemTuple, timeContext)
}
