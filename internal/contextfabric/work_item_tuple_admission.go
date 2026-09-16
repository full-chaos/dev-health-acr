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
// PURE: it never mutates frame, and neither does anything else in this
// arm any more. This function runs at TWO points with two different
// family readings (resolveFrame's heuristic DeriveQuestionFamily
// projection, then tightenWorkItemTupleFrameGate's later calls with the
// routed and carry-adjusted family), and the second or third call can
// still turn a promoted gate back to refused; a MUTATION applied at the
// first call would never be undone by a later reversal. The settled
// value -- workItemTuple's final admission, decided in engine.go only
// after the LAST tighten call -- instead feeds a pure obligation-set
// computation (workItemTupleEffectiveObligations), never a write to the
// frame.
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
// Obligations this arm's settled admission omits from the PLAN, WITHOUT
// touching the frame -- a pure prediction for a caller (the frame-validation
// telemetry line) that runs before the engine's own final tighten call can
// still reverse this interpretation's promotion, and the same authority the
// settled-admission line and workItemTupleEffectiveObligations both read.
// Nil when the frame does not carry the ranking obligation at all.
func workItemTupleObligationsToStrip(frame *QuestionFrame) []AnswerObligation {
	if frame == nil || !frame.HasObligation(ObligationRanking) {
		return nil
	}
	return []AnswerObligation{ObligationRanking}
}

// workItemTupleEffectiveObligations reports the obligation set the PLAN
// should derive requirement rows from for an admitted work-item survey
// tuple -- the frame's own canonical Obligations, minus ObligationRanking
// when workItemTupleObligationsToStrip says this frame carries it. PURE:
// the frame itself is never written to, here or anywhere else in this
// arm.
//
// GoalRankOrSurvey unconditionally derives ObligationRanking
// (frame_obligations.go), whether the frame asks to rank or only to
// survey -- the goal-to-obligation table has no way to see
// workItemTupleOrderingRequested's answer. This arm never computes a
// ranking regardless: RankCohort is skipped for every work-item tuple
// (engine.go, `!workItemTuple`), and the registry declares no ranking
// producer for work_item (graphrank/cohort_fact_requirements.go: work_item
// -> {FactStatus, FactWork} only). Left in the PLAN, a survey-admitted
// question would derive a REQUIRED requirement no producer can ever serve,
// and the answer would report degraded completeness on every admitted
// survey -- contradicting the one promise this arm makes for it: the same
// answer contract GoalAssessState already gets. GoalAssessState and
// GoalCountOrAggregate never carry ObligationRanking, so this returns the
// frame's own Obligations unchanged for every other admitted goal.
//
// THE FRAME ITSELF STAYS CANONICAL, ALWAYS: frame.Obligations is never
// written to, here or anywhere in this arm. The PERSISTED reading, the
// frame consumers receive, and the frame the composition boundary
// revalidates (chaos5465_composition_boundary.go) are the SAME canonical
// object -- composeAcceptedContext's own "revalidate, do not repair" law
// depends on this: the carried frame must re-derive to itself under
// today's validation, and a frame whose Obligations were removed after
// validation can never do that. Only the plan -- built from this
// function's return value, never from the
// frame directly -- omits the requirement nothing can serve.
func workItemTupleEffectiveObligations(frame *QuestionFrame) []AnswerObligation {
	stripped := workItemTupleObligationsToStrip(frame)
	if len(stripped) == 0 {
		return frame.Obligations
	}
	effective := make([]AnswerObligation, 0, len(frame.Obligations))
	for _, obligation := range frame.Obligations {
		omit := false
		for _, member := range stripped {
			if obligation == member {
				omit = true
				break
			}
		}
		if !omit {
			effective = append(effective, obligation)
		}
	}
	return effective
}

// workItemTupleRequirementFrame returns a COPY of frame carrying the
// effective (plan-time) obligation set, for requirement derivation ONLY.
// frame itself is never mutated -- see workItemTupleEffectiveObligations's
// own doc comment for why that matters. Nil in, nil out.
func workItemTupleRequirementFrame(frame *QuestionFrame) *QuestionFrame {
	if frame == nil {
		return nil
	}
	planFrame := *frame
	planFrame.Obligations = workItemTupleEffectiveObligations(frame)
	return &planFrame
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

// carriedWorkItemTupleGateOverride is the composition boundary's
// CarriedGateOverride for exactly one carried shape: a work-item tuple's
// own frame. Nil for every other carried shape -- composeAcceptedContext's
// raw DecideFrameGate is already correct for them, and supplying anything
// here would be a second, unnecessary authority over a gate nothing about
// this arm concerns.
//
// CHAOS-5787: composeAcceptedContext's own raw DecideFrameGate refuses
// EVERY children_of_scope/work_item frame by construction. Only this arm's
// own admission (workItemTupleFrameGate) ever promotes that refusal, and
// the boundary itself never runs it -- so the composition boundary needs
// this arm's promoted verdict supplied, not re-derived from scratch.
// tightenWorkItemTupleFrameGate is the SAME construction engine.go's own
// fresh-path tighten call uses, seeded from what the carrier actually
// recorded (not re-derived, not assumed passed): a reading recorded
// refused for an unrelated reason stays refused; a reading recorded passed
// is re-decided under TODAY's family and time axis, promoted or demoted to
// match whichever the two currently disagree on.
func carriedWorkItemTupleGateOverride(carried *PersistedSemanticState, timeContext TimeContext) *FrameGate {
	if carried == nil || !carried.FramePresent || carried.Frame == nil || !workItemTupleInScope(carried.Frame) {
		return nil
	}
	definition, known := LookupQuestionFamily(carried.Family)
	familyAllowsWorkItemTuple := known && definition.allowsWorkItemTuple
	recorded := FrameGate{
		Outcome:            carried.Validation.GateOutcome,
		RefuseBasis:        carried.Validation.RefuseBasis,
		DeclaredMemberKind: carried.Validation.DeclaredMemberKind,
	}
	tightened := tightenWorkItemTupleFrameGate(recorded, carried.Frame, familyAllowsWorkItemTuple, timeContext)
	return &tightened
}

// carriedWorkItemTupleLegacyFrame is the composition boundary's
// CarriedLegacyFrame for exactly one carried shape: a work-item tuple's own
// frame with ObligationRanking already absent. Nil for every other carried
// shape, and nil when the carried frame already carries ranking -- there is
// nothing to reconstruct.
//
// This arm's own obligation-omission rule (workItemTupleEffectiveObligations)
// removes ONLY ObligationRanking, and only from the PLAN's copy, never the
// persisted frame -- but a settled admission that omitted it from the
// persisted frame directly is the SAME arm's own rule, applied to the frame
// instead of the plan. That is a legitimate shape this arm can have
// produced, on a rule this file still owns; the composition boundary just
// cannot tell it apart from a corrupted frame on its own. Restoring
// ObligationRanking and letting the boundary's own revalidation decide
// whether THAT reconstruction re-derives to itself is the whole function --
// this never invents a shape the arm could not have produced, and never
// substitutes a repair the boundary would not independently confirm.
func carriedWorkItemTupleLegacyFrame(carried *PersistedSemanticState) *QuestionFrame {
	if carried == nil || !carried.FramePresent || carried.Frame == nil || !workItemTupleInScope(carried.Frame) || carried.Frame.HasObligation(ObligationRanking) {
		return nil
	}
	legacy := cloneFrame(*carried.Frame)
	legacy.Obligations = sortedObligations(append(append([]AnswerObligation(nil), legacy.Obligations...), ObligationRanking))
	return &legacy
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

// WorkItemTupleAdmissionStrippedObligationsVocabulary is the settled-
// admission line's stripped_obligations field, for the event
// specification. DERIVED from AnswerObligationVocabulary, never retyped:
// workItemTupleObligationsToStrip can only ever name a member of that
// vocabulary, so the declared closed vocabulary and the predicate that
// fills the field read the same source.
func WorkItemTupleAdmissionStrippedObligationsVocabulary() []string {
	members := AnswerObligationVocabulary()
	return tokenStrings(members[:])
}

// WorkItemTupleAdmissionEvent is the SETTLED half of this arm's admission
// decision -- the ENFORCED outcome, recorded once every earlier and later
// family reading (resolveFrame's heuristic, finishFamilyResolution's
// routed-family tighten, the engine's own carry-adjusted-family tighten)
// has already been through. Emitted only for a frame workItemTupleInScope
// names as this arm's concern; StrippedObligations is nil/empty on every
// line, admitted or refused, that omits nothing -- including every
// refusal, since workItemTupleObligationsToStrip is only ever consulted
// when workItemTuple admitted. This is the settled counterpart to FrameValidationEvent's
// PredictedStrippedObligations, which is stamped before this decision is
// final and can therefore disagree with it on a turn whose family reading
// changes between the two.
type WorkItemTupleAdmissionEvent struct {
	Admitted            bool
	StrippedObligations []AnswerObligation
}
