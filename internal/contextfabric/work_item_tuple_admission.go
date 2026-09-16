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
// PURE: it never mutates frame. An earlier revision stripped the ranking
// obligation here, at the first promotion -- but this function runs at
// TWO points with two different family readings (resolveFrame's heuristic
// DeriveQuestionFamily projection, then tightenWorkItemTupleFrameGate's
// later calls with the routed and carry-adjusted family), and the second
// or third call can still turn a promoted gate back to refused. A strip
// applied at the first call was never undone by a later reversal, leaving
// a re-refused frame with its ranking obligation permanently gone even
// though nothing ever served without it. The mutation now happens exactly
// once, in engine.go, only after the LAST tighten call has settled
// workItemTuple's true value -- see that call site's own comment.
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

// workItemTupleObligationsToStrip reports which of the frame's current
// Obligations workItemTupleStripSurveyObligations would remove, WITHOUT
// removing them -- a pure prediction for a caller (the frame-validation
// telemetry line) that runs before the engine's own final tighten call can
// still reverse this interpretation's promotion. Nil when the frame does
// not carry the ranking obligation at all.
func workItemTupleObligationsToStrip(frame *QuestionFrame) []AnswerObligation {
	if frame == nil || !frame.HasObligation(ObligationRanking) {
		return nil
	}
	return []AnswerObligation{ObligationRanking}
}

// workItemTupleStripSurveyObligations removes ObligationRanking from an
// admitted frame's derived obligation set, in place, and returns exactly
// what it removed (nil when nothing was).
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
//
// CALLED FROM EXACTLY ONE PLACE: engine.go, immediately after the LAST
// tighten call decides workItemTuple's final value, gated on that value
// being true. That is the one point every earlier heuristic and every
// later carry-driven reversal has already been resolved, so a re-refused
// frame is never reached by this function at all and never loses the
// obligation it would otherwise need back.
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

// workItemTupleInScope reports whether frame is this arm's structural
// concern at all -- children_of_scope over work_item -- independent of
// whether it ends up admitted. This is the ONE condition
// prospectiveWorkItemTupleAdmission's first guard clause already checks;
// named here so the settled-admission telemetry call site (engine.go) can
// ask it without re-deriving the whole admission decision just to decide
// whether to log.
func workItemTupleInScope(frame *QuestionFrame) bool {
	return frame != nil && frame.SubjectExpression.Kind == SubjectExpressionChildrenOfScope && frame.SubjectExpression.Scoped != nil && frame.SubjectExpression.Scoped.MemberKind == SubjectWorkItem
}

// WorkItemTupleAdmissionEvent is the SETTLED half of this arm's admission
// decision -- the ENFORCED outcome, recorded once every earlier and later
// family reading (resolveFrame's heuristic, finishFamilyResolution's
// routed-family tighten, the engine's own carry-adjusted-family tighten)
// has already been through. Emitted only for a frame workItemTupleInScope
// names as this arm's concern; StrippedObligations is nil/empty on every
// line, admitted or refused, that removed nothing -- including every
// refusal, since workItemTupleStripSurveyObligations never runs for one.
// This is the settled counterpart to FrameValidationEvent's
// PredictedStrippedObligations, which is stamped before this decision is
// final and can therefore disagree with it on a turn whose family reading
// changes between the two.
type WorkItemTupleAdmissionEvent struct {
	Admitted            bool
	StrippedObligations []AnswerObligation
}
