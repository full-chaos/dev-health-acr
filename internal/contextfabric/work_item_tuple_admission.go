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
	if !familyAllowsWorkItemTuple || len(frame.Goals) == 0 || frame.Temporal != TemporalIntentCurrent || timeContext.Axis != TemporalCurrent || !workItemTupleQualifierServable(frame.SubjectExpression.Scoped) {
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

// Member-filter basis tokens: the closed vocabulary of the settled
// admission line's member_filter field. Only "status" with a closed-set
// value is served; every other qualifier shape stays refused.
const (
	WorkItemMemberFilterNone               = "none"
	WorkItemMemberFilterStatus             = "status"
	WorkItemMemberFilterStatusWithoutValue = "status_without_value"
	WorkItemMemberFilterAssignee           = "assignee"
	WorkItemMemberFilterUnrecognized       = "unrecognized"
	// WorkItemMemberFilterWindow: a bounded window on one bound time field.
	WorkItemMemberFilterWindow = "window"
	// WorkItemMemberFilterWindowRoleUnresolved: a stated window whose time
	// field the question did not fix to exactly one reading.
	WorkItemMemberFilterWindowRoleUnresolved = "window_role_unresolved"
	// WorkItemMemberFilterWindowNotServed: a period frame the read cannot
	// serve (no committed window, a comparison or series, or a historical
	// axis).
	WorkItemMemberFilterWindowNotServed = "window_not_served"
	// WorkItemMemberFilterWindowNotApplied: a current frame served with a
	// committed window and no single time field; members are as of now.
	WorkItemMemberFilterWindowNotApplied = "window_not_applied"
)

// WorkItemMemberFilterVocabulary is the closed member_filter vocabulary.
func WorkItemMemberFilterVocabulary() []string {
	return []string{WorkItemMemberFilterNone, WorkItemMemberFilterStatus, WorkItemMemberFilterStatusWithoutValue, WorkItemMemberFilterAssignee, WorkItemMemberFilterUnrecognized, WorkItemMemberFilterWindow, WorkItemMemberFilterWindowRoleUnresolved, WorkItemMemberFilterWindowNotServed, WorkItemMemberFilterWindowNotApplied}
}

// workItemTupleMemberFilterBasis names which qualifier shape decided the
// member filter, for the admission telemetry line.
func workItemTupleMemberFilterBasis(frame *QuestionFrame) string {
	if !workItemTupleInScope(frame) {
		return WorkItemMemberFilterNone
	}
	scoped := frame.SubjectExpression.Scoped
	switch scoped.MemberQualifier {
	case "":
		return WorkItemMemberFilterNone
	case MemberQualifierStatus:
		if scoped.MemberQualifierValue == "" {
			return WorkItemMemberFilterStatusWithoutValue
		}
		return WorkItemMemberFilterStatus
	case MemberQualifierAssignee:
		return WorkItemMemberFilterAssignee
	default:
		return WorkItemMemberFilterUnrecognized
	}
}

// workItemTupleQualifierServable reports whether the member qualifier is
// absent or a status qualifier carrying a value of the closed status set.
// A status qualifier without a value cannot be applied in the read, and an
// assignee or unrecognized qualifier has no source, so those stay refused
// rather than answered as unqualified membership.
func workItemTupleQualifierServable(scoped *ScopedSetExpression) bool {
	if scoped == nil {
		return false
	}
	if !MemberQualifierPresent(scoped.MemberQualifier) {
		return true
	}
	return scoped.MemberQualifier == MemberQualifierStatus && InWorkItemStatusVocabulary(scoped.MemberQualifierValue)
}

// workItemTupleStatusFilter is the status the member read must apply, or
// empty for unqualified membership. It is non-empty only where admission
// admitted the frame.
func workItemTupleStatusFilter(frame *QuestionFrame) string {
	if !workItemTupleInScope(frame) || frame.SubjectExpression.Scoped.MemberQualifier != MemberQualifierStatus || !workItemTupleQualifierServable(frame.SubjectExpression.Scoped) {
		return ""
	}
	return frame.SubjectExpression.Scoped.MemberQualifierValue
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
	MemberFilter        string
	// MemberTimeRole is what the role binder decided for a committed window:
	// the bound role, ambiguous, no_verb, or not_evaluated when no window was
	// committed.
	MemberTimeRole string
}

// Member time-role log tokens beside the three roles.
const (
	WorkItemMemberTimeRoleNotEvaluated = "not_evaluated"
)

// WorkItemMemberTimeRoleVocabulary is the closed member_time_role vocabulary.
func WorkItemMemberTimeRoleVocabulary() []string {
	vocabulary := []string{WorkItemMemberTimeRoleNotEvaluated, string(MemberTimeRoleNoVerb), string(MemberTimeRoleAmbiguous)}
	for _, role := range MemberTimeRoleVocabulary() {
		vocabulary = append(vocabulary, string(role))
	}
	return vocabulary
}

// memberTimeRoleLogValue is the binder decision for the admission line.
func (basis workItemTupleWindowBasis) memberTimeRoleLogValue() string {
	switch {
	case !basis.Committed:
		return WorkItemMemberTimeRoleNotEvaluated
	case basis.Role != "":
		return string(basis.Role)
	case basis.RoleReason == MemberTimeRoleAmbiguous:
		return string(MemberTimeRoleAmbiguous)
	}
	return string(MemberTimeRoleNoVerb)
}

// workItemMemberTimeRoleLogValue refuses any token outside the closed
// vocabulary rather than logging it.
func workItemMemberTimeRoleLogValue(value string) string {
	for _, token := range WorkItemMemberTimeRoleVocabulary() {
		if value == token {
			return token
		}
	}
	return WorkItemMemberTimeRoleNotEvaluated
}

// workItemMemberFilterLogValue maps the unset basis to "none" and refuses
// any token outside the closed vocabulary rather than logging it.
func workItemMemberFilterLogValue(basis string) string {
	if basis == "" {
		return WorkItemMemberFilterNone
	}
	for _, token := range WorkItemMemberFilterVocabulary() {
		if basis == token {
			return token
		}
	}
	return WorkItemMemberFilterNone
}

// workItemTupleWindowBasis is what the server bound from the question text and
// the request for a period frame: whether a window is committed (a window the
// caller supplied, or one the question states as a trailing period) and which
// time field the question's verb names. The frame carries neither.
type workItemTupleWindowBasis struct {
	Committed  bool
	Role       MemberTimeRole
	RoleReason MemberTimeRoleReason
}

// deriveWorkItemTupleWindowBasis mirrors the two decisive branches of
// composeEffectiveWindow: a request-side window, or a trailing period the
// question states. A window carried from an earlier turn, remembered, or
// inferred from the class table is not committed here.
func deriveWorkItemTupleWindowBasis(question string, windowCanon requestWindowCanonicalization, remembered bool) workItemTupleWindowBasis {
	basis := workItemTupleWindowBasis{RoleReason: MemberTimeRoleNoVerb}
	span := BoundWindowSpan{SpanStart: 0, SpanEnd: len(question)}
	if spans := BindWindowSpans(question); len(spans) == 1 {
		span = spans[0]
	}
	switch {
	case remembered:
		// A window remembered from an earlier turn is not one this request
		// states or supplies.
	case windowCanon.Effective != nil && windowCanon.Effective.Start != nil && windowCanon.Effective.End != nil:
		basis.Committed = true
	case windowCanon.BinderProposal.Reason == WindowBindRoutedInferred && windowCanon.BinderProposal.Trailing:
		basis.Committed = true
	}
	if !basis.Committed {
		return basis
	}
	outcome := BindMemberTimeRole(question, span)
	basis.RoleReason = outcome.Reason
	if outcome.Reason == MemberTimeRoleBound {
		basis.Role = outcome.Role
	}
	return basis
}

// workItemTupleIsPeriodFrame reports a work-item tuple frame that asks for a
// bounded window. Only that temporal intent engages the window arm.
func workItemTupleIsPeriodFrame(frame *QuestionFrame) bool {
	return workItemTupleInScope(frame) && frame.Temporal == TemporalIntentBoundedWindow
}

// prospectiveWorkItemWindowAdmission decides a period frame: every clause of
// the ordinary admission holds as if the frame were current, the axis is
// current, and the server committed a window and bound exactly one time
// field. Comparison and series intents never reach it.
func prospectiveWorkItemWindowAdmission(frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext, basis workItemTupleWindowBasis) workItemTupleAdmission {
	if !workItemTupleIsPeriodFrame(frame) {
		return workItemTupleNotApplicable
	}
	asCurrent := *frame
	asCurrent.Temporal = TemporalIntentCurrent
	if prospectiveWorkItemTupleAdmission(&asCurrent, familyAllowsWorkItemTuple, timeContext) != workItemTupleProspective {
		return workItemTupleRefused
	}
	if !basis.Committed || basis.Role == "" {
		return workItemTupleRefused
	}
	return workItemTupleProspective
}

// windowedWorkItemTupleFrameGate settles the gate for a period frame. It may
// promote the ordinary kind refusal, as the first admission does, because the
// window basis exists only where the question text does. Other refusals and
// invalid frames keep their gate.
func windowedWorkItemTupleFrameGate(gate FrameGate, frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext, basis workItemTupleWindowBasis) FrameGate {
	if gate.Outcome == FrameGateNotProposed || gate.Outcome == FrameGateRejectedInvalid || (gate.Refuses() && !(gate.Outcome == FrameGateRefusedBasis && gate.RefuseBasis == CohortMemberKindUnservable && gate.DeclaredMemberKind == SubjectWorkItem)) {
		return gate
	}
	switch prospectiveWorkItemWindowAdmission(frame, familyAllowsWorkItemTuple, timeContext, basis) {
	case workItemTupleProspective:
		return FrameGate{Outcome: FrameGatePassed}
	case workItemTupleRefused:
		return FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectWorkItem}
	}
	return gate
}

// workItemWindowFilterBasis names the window decision for the admission line.
func workItemWindowFilterBasis(frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext, basis workItemTupleWindowBasis) string {
	if workItemTupleInScope(frame) && (frame.Temporal == TemporalIntentPeriodComparison || frame.Temporal == TemporalIntentTimeSeries) {
		return WorkItemMemberFilterWindowNotServed
	}
	if workItemCurrentFrameCarriesUnappliedWindow(frame, basis) && prospectiveWorkItemTupleAdmission(frame, familyAllowsWorkItemTuple, timeContext) == workItemTupleProspective {
		return WorkItemMemberFilterWindowNotApplied
	}
	if !workItemTupleIsPeriodFrame(frame) {
		return ""
	}
	switch prospectiveWorkItemWindowAdmission(frame, familyAllowsWorkItemTuple, timeContext, basis) {
	case workItemTupleProspective:
		return WorkItemMemberFilterWindow
	}
	asCurrent := *frame
	asCurrent.Temporal = TemporalIntentCurrent
	if basis.Committed && basis.Role == "" && prospectiveWorkItemTupleAdmission(&asCurrent, familyAllowsWorkItemTuple, timeContext) == workItemTupleProspective {
		return WorkItemMemberFilterWindowRoleUnresolved
	}
	return WorkItemMemberFilterWindowNotServed
}

// workItemTupleMemberFilterToken is the settled admission line's member_filter
// value: the window decision for a period frame, the qualifier shape for any
// other.
func workItemTupleMemberFilterToken(frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext, basis workItemTupleWindowBasis) string {
	if token := workItemWindowFilterBasis(frame, familyAllowsWorkItemTuple, timeContext, basis); token != "" {
		return token
	}
	return workItemTupleMemberFilterBasis(frame)
}

// workItemRoleClarificationReason is the binder's reason when the three
// readings are the one thing missing: the gate refused on the member-kind
// basis this arm sets, and every other admission clause holds. An invalid
// frame, or any other refusal, keeps its own explanation.
func workItemRoleClarificationReason(gate FrameGate, frame *QuestionFrame, familyAllowsWorkItemTuple bool, timeContext TimeContext, basis workItemTupleWindowBasis) MemberTimeRoleReason {
	if gate.Outcome != FrameGateRefusedBasis || gate.RefuseBasis != CohortMemberKindUnservable {
		return ""
	}
	if workItemWindowFilterBasis(frame, familyAllowsWorkItemTuple, timeContext, basis) != WorkItemMemberFilterWindowRoleUnresolved {
		return ""
	}
	return basis.RoleReason
}

// promoteCurrentWorkItemFrameToPeriod returns a copy of a current work-item
// tuple frame as a bounded-window frame when the request committed a window
// (supplied or stated; a remembered window never commits), the interpreted
// axis is current and the question binds exactly one time field, or names two
// for the one period: that question is a period question whose reading must be
// asked for, never a current read that drops the period. A question with no
// time-role verb is not promoted.
func promoteCurrentWorkItemFrameToPeriod(frame *QuestionFrame, timeContext TimeContext, basis workItemTupleWindowBasis) (*QuestionFrame, bool) {
	if timeContext.Axis != TemporalCurrent || !workItemTupleInScope(frame) || frame.Temporal != TemporalIntentCurrent || !basis.Committed || (basis.Role == "" && basis.RoleReason != MemberTimeRoleAmbiguous) {
		return frame, false
	}
	promoted := cloneFrame(*frame)
	promoted.Temporal = TemporalIntentBoundedWindow
	return &promoted, true
}

// workItemCurrentFrameCarriesUnappliedWindow reports a current work-item frame
// served with a committed window the membership read does not apply, because
// the question names no single time field. A status-qualified frame already
// states that its status is read as of now, over no period.
func workItemCurrentFrameCarriesUnappliedWindow(frame *QuestionFrame, basis workItemTupleWindowBasis) bool {
	return workItemTupleInScope(frame) && workItemTupleStatusFilter(frame) == "" && frame.Temporal == TemporalIntentCurrent && basis.Committed && basis.Role == ""
}
