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
//
// GoalRankOrSurvey is admitted ONLY when the frame requests no ordering
// (workItemTupleOrderingRequested). "Survey" and "rank" share one goal
// token; Emphasis is what tells them apart within it (see that function).
// RankCohort and the cohort ranking injection stay suppressed for this arm
// regardless of admission -- the tuple serves a plain enumeration, never a
// computed order, whichever goal admitted it.
func prospectiveWorkItemTupleAdmission(frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext) workItemTupleAdmission {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil || frame.SubjectExpression.Scoped.MemberKind != SubjectWorkItem {
		return workItemTupleNotApplicable
	}
	if !familyAllowsWorkItemTuple || len(frame.Goals) == 0 || frame.Temporal != TemporalIntentCurrent || timeContext.Axis != TemporalCurrent || MemberQualifierPresent(frame.SubjectExpression.Scoped.MemberQualifier) {
		return workItemTupleRefused
	}
	orderingRequested := workItemTupleOrderingRequested(frame)
	for _, goal := range frame.Goals {
		switch goal {
		case GoalAssessState, GoalCountOrAggregate:
			continue
		case GoalRankOrSurvey:
			if orderingRequested {
				return workItemTupleRefused
			}
			continue
		default:
			return workItemTupleRefused
		}
	}
	return workItemTupleProspective
}

// workItemTupleOrderingRequested reports whether the frame asks for an
// actual ordering over the population rather than a plain enumeration of
// it. Emphasis (positive_outliers/negative_outliers) is the one frame
// signal that names an END of an ordering -- I14 refuses Emphasis without
// the ranking obligation for exactly that reason -- so its presence is
// what distinguishes "rank" from "survey" within the single
// GoalRankOrSurvey token. A frame with no Emphasis asks only to enumerate
// the scoped cohort, the same answer contract GoalAssessState already
// serves for this arm.
func workItemTupleOrderingRequested(frame *QuestionFrame) bool {
	return frame != nil && len(frame.Emphasis) > 0
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
//
// The promotion branch is the ONLY place this arm's admission turns a
// refusal into a pass (tightenWorkItemTupleFrameGate never reaches it: it
// calls this function only when the gate does not already refuse, and the
// promotion guard below requires the gate to be exactly the tuple's own
// member_kind_unservable refusal). That makes it the one correct place to
// also strip the ranking obligation this arm can never discharge -- see
// workItemTupleSurveyObligations -- rather than a second, later pass that
// would have to rediscover which frames this admission touched.
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
			// FrameGate stays a comparable value (`==`/`!=` on it are load-
			// bearing across this package's tests, chaos5465_boundary_pins_
			// test.go and others) -- a slice field here would break that, so
			// what this call strips is NOT carried on the gate. The caller
			// that wants it observes the frame's Obligations before and
			// after this call instead (see resolveFrame's own
			// strippedObligations capture).
			if frame != nil {
				workItemTupleStripSurveyObligations(frame)
			}
			return FrameGate{Outcome: FrameGatePassed}
		}
	}
	return gate
}

// workItemTupleStripSurveyObligations removes ObligationRanking from an
// admitted frame's derived obligation set, in place, and returns exactly
// what it removed (nil when nothing was) -- direct callers (this file's own
// tests, and obligationsRemoved's diff for a caller that only has the frame
// before/after) read this to make the mutation observable rather than
// inferred.
//
// GoalRankOrSurvey unconditionally derives ObligationRanking
// (frame_obligations.go), whether the frame asks to rank or only to
// survey -- the goal-to-obligation table has no way to see
// workItemTupleOrderingRequested's answer. This arm never computes a
// ranking regardless: RankCohort is skipped for every work-item tuple
// (engine.go, `!workItemTuple`), and the registry declares no ranking
// producer for work_item (graphrank/cohort_fact_requirements.go: work_item
// -> {FactStatus, FactWork} only). Left in place, a survey-admitted
// question would derive a REQUIRED requirement no producer can ever serve,
// and the answer would report degraded completeness on every admitted
// survey -- contradicting the one promise this arm makes for it: the same
// answer contract GoalAssessState already gets. GoalAssessState and
// GoalCountOrAggregate never carry ObligationRanking, so this is a no-op
// (nil returned) for every other admitted goal.
func workItemTupleStripSurveyObligations(frame *QuestionFrame) []AnswerObligation {
	var stripped []AnswerObligation
	kept := frame.Obligations[:0:0]
	for _, obligation := range frame.Obligations {
		if obligation == ObligationRanking {
			stripped = append(stripped, obligation)
			continue
		}
		kept = append(kept, obligation)
	}
	frame.Obligations = kept
	return stripped
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

// obligationsRemoved reports which members of before are absent from after,
// in before's own order -- the observable half of workItemTupleFrameGate's
// promotion-time strip for a caller that only has the frame's Obligations
// snapshotted on either side of the call, not the mutating call itself.
// Nil, never an empty non-nil slice, when nothing was removed, so a log
// line's rendering of "nothing stripped" and "the field was never touched"
// are one value, not two.
func obligationsRemoved(before, after []AnswerObligation) []AnswerObligation {
	stillPresent := make(map[AnswerObligation]bool, len(after))
	for _, obligation := range after {
		stillPresent[obligation] = true
	}
	var removed []AnswerObligation
	for _, obligation := range before {
		if !stillPresent[obligation] {
			removed = append(removed, obligation)
		}
	}
	return removed
}
