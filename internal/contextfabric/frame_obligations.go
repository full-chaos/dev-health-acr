package contextfabric

// CHAOS-4452 stage 2 (S7b-i), design §13.2.3: obligation derivation from
// the whole frame, re-derived UNDER the eight algebra laws of §13.2.2a.
//
// SHADOW ONLY -- see frame_vocab.go's package-level note.
//
// SCOPE BOUNDARY, stated first because it is the one this slice is most
// likely to be accused of crossing. This file derives OBLIGATIONS from the
// frame. It does NOT derive requirements. The obligation -> requirement
// layer -- FactKinds seeds, the SubjectRole table, completion scope and
// quantifier, the dimension intersection, post-resolution refinement -- is
// EXCISED to §13.15 and is S7b-ii's, gated on oracle O9 being green
// against the real fact registry. §13.15.3 forbids this slice from writing
// a seed, a quantifier or an intersection rule. The reason is executed,
// not stylistic: the finalizer transcribed the frozen requirement rules
// into a Go test and ran them against all 21 Capability() declarations,
// producing 22 EMPTY requirement cells over 13 frames, including every
// acceptance question. What survives here is what was traced and is
// registry-FREE: these four tables, and the obligation KIND classification
// in frame_vocab.go.
//
// WHY THE TABLES ARE WRITTEN UNDER LAWS RATHER THAN ON THEIR OWN. Round 2
// of the design review stopped the design lane with eight re-finds and the
// root cause was one sentence: stage 2 specified derivation TABLES without
// specifying the ALGEBRA those tables must satisfy. Each table was locally
// defensible; composed, they permitted removal, omission and conflict.
// Every table below therefore names the law it is derived under, and the
// laws become property tests in the family slice.

// AxisDischargeMode names HOW a set axis is discharged. Law L2 requires
// the mode to be NAMED in the table, never implicit -- "writing that down
// is what stops 'no obligation' from looking like a decision".
//
// Closed vocabulary, telemetry-safe: no prose, no identifiers.
type AxisDischargeMode string

const (
	// DischargeByObligation: the derived obligation set contains an
	// obligation characteristic of this axis.
	DischargeByObligation AxisDischargeMode = "obligation"
	// DischargeByRequirementProperty: the axis constrains HOW the
	// obligations already derived are served, rather than adding one.
	// The property itself is S7b-ii's to implement; naming it here is
	// what makes the discharge checkable now.
	DischargeByRequirementProperty AxisDischargeMode = "requirement_property"
)

// RequirementProperty names a declared discharge that is not an
// obligation. These are LABELS on a discharge, not fields of a
// PlanRequirement -- this slice may not write requirement rows (§13.15.3),
// and naming a discharge is not writing one.
//
// Both discharge modes are legitimate and the distinction is the point
// (§13.2.2a L2): period_comparison needs its OWN obligation
// (period_delta -- different evidence); bounded_window does not, because
// it bounds the window of reads the other obligations already make.
type RequirementProperty string

const (
	// PropertyWindowBoundedReads: every requirement's reads are bounded
	// to the stated window. Discharges Temporal == bounded_window.
	PropertyWindowBoundedReads RequirementProperty = "window_bounded_reads"
	// PropertyCompletionScopeEachOperand: the SAME evidence must be read
	// on every operand. Discharges the compare goal.
	//
	// §13.4.2: "A comparison is not answered by reading `state` once; it
	// must read the SAME evidence on every operand. That is a completion
	// SCOPE over operands (`each_operand`) on the requirement -- not a
	// separate obligation." Round 1 was right that the earlier text
	// asserted "matched on every operand" parenthetically with no
	// mechanism; the mechanism is NAMED here and BUILT in S7b-ii.
	PropertyCompletionScopeEachOperand RequirementProperty = "completion_scope_each_operand"
	// PropertyFactKindConstraint: the axis constrains which fact kinds
	// serve an existing obligation. Discharges the five HealthDimension
	// members that add no obligation of their own.
	PropertyFactKindConstraint RequirementProperty = "fact_kind_constraint"
	// PropertySubjectRoleAssignment: SubjectExpression.Kind assigns roles
	// to requirements rather than adding obligations (§13.2.3 table 4).
	PropertySubjectRoleAssignment RequirementProperty = "subject_role_assignment"
)

// goalObligations is §13.2.3 TABLE 1. Every goal in the set contributes;
// the union is taken. UNCONDITIONAL, which is what makes law L1
// (MONOTONICITY) hold.
//
// TWO MOVES IN THIS TABLE ARE THE FIX FOR ROUND-2 P1-1 AND P1-2, and both
// are worth stating because a future edit that "tidies" either one
// reintroduces a defect the design already paid for:
//
//   - `health` is UNCONDITIONAL on the three state-ish goals. It used to
//     be gated on Dimensions being EMPTY, so naming any dimension REMOVED
//     it -- a model emission narrowing the plan, which is the L1 violation
//     round 2 caught. Asking "how is delivery flow for team X?" now reads
//     health as well. That is a widening, not a narrowing.
//   - `trend_series` sits on describe_trend and `period_delta` on
//     explain_change, so a goal's characteristic obligation comes from the
//     GOAL and satisfies law L2 regardless of Temporal. Round 2's P1-2 was
//     {describe_trend, named_subject, bounded_window} deriving NO temporal
//     obligation at all -- round 1's F4 defect having MOVED from family
//     routing into obligation derivation.
//
// BAR question Q2 is the test that the Goals-as-a-set reversal is real:
// "What teams are struggling and what are the driving factors?" derives
// Goals={rank_or_survey, explain_drivers}, hence {ranking, state, health,
// principal_drivers, evidence, coverage} with BOTH operations REQUIRED and
// neither advisory. Under the singular-goal design that question was
// unrepresentable.
var goalObligations = map[InvestigationGoal][]AnswerObligation{
	GoalAssessState: {
		ObligationState, ObligationHealth, ObligationEvidence, ObligationCoverage,
	},
	GoalExplainDrivers: {
		ObligationState, ObligationHealth, ObligationPrincipalDrivers,
		ObligationEvidence, ObligationCoverage,
	},
	GoalCompare: {
		ObligationState, ObligationEvidence, ObligationCoverage,
	},
	GoalRankOrSurvey: {
		ObligationRanking, ObligationState, ObligationHealth,
		ObligationEvidence, ObligationCoverage,
	},
	GoalDescribeTrend: {
		ObligationState, ObligationTrendSeries, ObligationEvidence, ObligationCoverage,
	},
	GoalExplainChange: {
		ObligationState, ObligationPrincipalDrivers, ObligationPeriodDelta,
		ObligationEvidence, ObligationCoverage,
	},
	GoalAllocateInvestment: {
		ObligationAllocationBreakdown, ObligationEvidence, ObligationCoverage,
	},
	GoalCountOrAggregate: {
		ObligationCount, ObligationEvidence, ObligationCoverage,
	},
}

// temporalObligations is §13.2.3 TABLE 2. `current` and `bounded_window`
// add nothing; bounded_window's discharge is a requirement PROPERTY, named
// in temporalDischarge below rather than left implicit.
var temporalObligations = map[TemporalIntent][]AnswerObligation{
	TemporalIntentCurrent:          nil,
	TemporalIntentBoundedWindow:    nil,
	TemporalIntentTimeSeries:       {ObligationTrendSeries},
	TemporalIntentPeriodComparison: {ObligationPeriodDelta},
}

// dimensionObligations is §13.2.3 TABLE 3. ADDITIVE ONLY (law L1): it
// never removes and never gates.
//
// THERE IS NO EMPTY-SET SPECIAL CASE, and its absence is the fix rather
// than an omission: the empty-Dimensions branch is exactly where the L1
// violation lived. dependencies_and_blockers has its OWN row because round
// 2 caught a straight contradiction -- table 3 classified it as "any
// other, adds nothing" while the prose derived remaining_work from it. The
// prose was deleted, not reconciled.
//
// The five members with no row here (delivery_flow, review_and_ci_pressure,
// code_ownership_risk, cognitive_workload_pressure, data_trust) CONSTRAIN
// WHICH FACT KINDS serve the existing obligations -- that is S7b-ii's, and
// their discharge is named PropertyFactKindConstraint so the absence of an
// obligation is a recorded decision rather than a gap.
var dimensionObligations = map[HealthDimension][]AnswerObligation{
	HealthDimensionExecutionCompletion: {ObligationCompletion},
	HealthDimensionReliabilityRelease:  {ObligationReadiness},
	HealthDimensionInvestmentBalance:   {ObligationAllocationBreakdown},
	HealthDimensionDependenciesBlocked: {ObligationRemainingWork},
}

// AxisKind names WHICH axis of the frame a discharge belongs to. Closed
// vocabulary; it is what invariant I16 reports when an axis is
// undischarged, so a failure says which axis rather than only that one
// failed.
type AxisKind string

const (
	// AxisGoal: one member of Goals.
	AxisGoal AxisKind = "goal"
	// AxisTemporal: the Temporal axis, when it is not `current`.
	AxisTemporal AxisKind = "temporal"
	// AxisDimension: one member of Dimensions.
	AxisDimension AxisKind = "dimension"
	// AxisEmphasis: a non-empty Emphasis.
	AxisEmphasis AxisKind = "emphasis"
	// AxisSubjectKind: SubjectExpression.Kind.
	AxisSubjectKind AxisKind = "subject_kind"
)

// AxisDischarge is one axis's declared discharge: either a characteristic
// obligation that must be present, or a named requirement property.
type AxisDischarge struct {
	Axis AxisKind
	// Value is the axis member, as a string, for telemetry and for the
	// failure message. Always a closed-vocabulary member -- never prose.
	Value string
	Mode  AxisDischargeMode
	// Obligation is the characteristic obligation, set only when Mode is
	// DischargeByObligation.
	Obligation AnswerObligation
	// Property is the declared requirement property, set only when Mode
	// is DischargeByRequirementProperty.
	Property RequirementProperty
}

// goalDischarge is the CHARACTERISTIC obligation of each goal -- the one
// whose absence means the goal axis is no longer discharged.
//
// It is NOT the same as goalObligations: assess_state CONTRIBUTES state,
// health, evidence and coverage, but only `state` is characteristic of it,
// because health/evidence/coverage are contributed by other goals too and
// their presence would mask a dropped assess_state.
//
// `compare` is discharged by a requirement PROPERTY rather than an
// obligation, per §13.4.2: its distinguishing demand is that the SAME
// evidence is read on every operand, which is a completion scope, not an
// extra obligation.
var goalDischarge = map[InvestigationGoal]AxisDischarge{
	GoalAssessState:        {Axis: AxisGoal, Value: string(GoalAssessState), Mode: DischargeByObligation, Obligation: ObligationState},
	GoalExplainDrivers:     {Axis: AxisGoal, Value: string(GoalExplainDrivers), Mode: DischargeByObligation, Obligation: ObligationPrincipalDrivers},
	GoalCompare:            {Axis: AxisGoal, Value: string(GoalCompare), Mode: DischargeByRequirementProperty, Property: PropertyCompletionScopeEachOperand},
	GoalRankOrSurvey:       {Axis: AxisGoal, Value: string(GoalRankOrSurvey), Mode: DischargeByObligation, Obligation: ObligationRanking},
	GoalDescribeTrend:      {Axis: AxisGoal, Value: string(GoalDescribeTrend), Mode: DischargeByObligation, Obligation: ObligationTrendSeries},
	GoalExplainChange:      {Axis: AxisGoal, Value: string(GoalExplainChange), Mode: DischargeByObligation, Obligation: ObligationPeriodDelta},
	GoalAllocateInvestment: {Axis: AxisGoal, Value: string(GoalAllocateInvestment), Mode: DischargeByObligation, Obligation: ObligationAllocationBreakdown},
	GoalCountOrAggregate:   {Axis: AxisGoal, Value: string(GoalCountOrAggregate), Mode: DischargeByObligation, Obligation: ObligationCount},
}

// temporalDischarge names each temporal mode's discharge. `current` has no
// entry because an unset axis is not a SET axis and law L2 quantifies over
// axes the frame SETS.
var temporalDischarge = map[TemporalIntent]AxisDischarge{
	TemporalIntentBoundedWindow:    {Axis: AxisTemporal, Value: string(TemporalIntentBoundedWindow), Mode: DischargeByRequirementProperty, Property: PropertyWindowBoundedReads},
	TemporalIntentTimeSeries:       {Axis: AxisTemporal, Value: string(TemporalIntentTimeSeries), Mode: DischargeByObligation, Obligation: ObligationTrendSeries},
	TemporalIntentPeriodComparison: {Axis: AxisTemporal, Value: string(TemporalIntentPeriodComparison), Mode: DischargeByObligation, Obligation: ObligationPeriodDelta},
}

// dimensionDischarge names each dimension's discharge. The four with a
// table-3 row discharge by that obligation; the other five discharge by
// the named fact-kind constraint.
func dimensionDischarge(dimension HealthDimension) AxisDischarge {
	if obligations, ok := dimensionObligations[dimension]; ok && len(obligations) > 0 {
		return AxisDischarge{
			Axis:       AxisDimension,
			Value:      string(dimension),
			Mode:       DischargeByObligation,
			Obligation: obligations[0],
		}
	}
	return AxisDischarge{
		Axis:     AxisDimension,
		Value:    string(dimension),
		Mode:     DischargeByRequirementProperty,
		Property: PropertyFactKindConstraint,
	}
}

// DeriveObligations computes the SERVER-DERIVED obligation set for a
// frame, as the union of §13.2.3 tables 1-4.
//
// It reads Goals, Temporal and Dimensions ONLY. It does NOT read Emphasis
// (table 4: emphasis adds no obligation and no fact read), it does NOT
// read SubjectExpression.Kind (table 4: Kind assigns roles, not
// obligations), and it reads NOTHING from resolution or fact-read state.
//
// The last exclusion is a design invariant, not an implementation
// convenience (§13.8a): obligations derive from PRE-RESOLUTION data only,
// and the frame is immutable once validated. An obligation set that could
// change after the read would make the frame a moving target and every
// completeness claim derived from it unreproducible.
//
// The result is a SET, returned deduplicated in vocabulary order, so
// oracle O1 can assert an EXACT set rather than a subset.
func DeriveObligations(goals []InvestigationGoal, temporal TemporalIntent, dimensions []HealthDimension) []AnswerObligation {
	derived := make([]AnswerObligation, 0, AnswerObligationCount)

	// Table 1 -- every goal contributes, the union is taken.
	for _, goal := range goals {
		derived = append(derived, goalObligations[goal]...)
	}
	// Table 2 -- the temporal axis. An unset Temporal is treated as
	// `current` here as well as in normalization, so a caller that has
	// not normalized yet gets the same answer rather than a different
	// one.
	effective := temporal
	if effective == "" {
		effective = TemporalIntentCurrent
	}
	derived = append(derived, temporalObligations[effective]...)
	// Table 3 -- dimensions, additive only.
	for _, dimension := range dimensions {
		derived = append(derived, dimensionObligations[dimension]...)
	}
	// Table 4 -- Emphasis and Kind contribute NOTHING. Written as a
	// comment rather than as an empty loop on purpose: an empty loop
	// invites a later edit to fill it, and filling it would give a
	// paraphrase an extra fact read, which the 12:42 08-31 ruling
	// forbids.

	return sortedObligations(derived)
}

// DeriveFrameObligations derives the obligation set for a frame and
// returns a copy of the frame carrying it, plus the widened set.
//
// WIDENING IS ADMITTED AND IS ADVISORY (§13.2.4). modelEmitted is whatever
// obligation list the model produced; members already derived are dropped
// (they are required, and the model cannot make a required obligation
// advisory), and members outside the derived set become WidenedObligations
// -- advisory, disclosed, and unable to degrade answer completeness.
//
// THE MODEL CAN NEVER REMOVE. There is deliberately no code path by which
// modelEmitted subtracts from derived; the function unions and never
// intersects. §13.2.1: "a spurious extra obligation costs an extra read
// and a visible unsatisfied outcome; a missing one silently changes the
// question."
func DeriveFrameObligations(frame QuestionFrame, modelEmitted []AnswerObligation) QuestionFrame {
	derived := DeriveObligations(frame.Goals, frame.Temporal, frame.Dimensions)
	inDerived := make(map[AnswerObligation]bool, len(derived))
	for _, member := range derived {
		inDerived[member] = true
	}
	widened := make([]AnswerObligation, 0, len(modelEmitted))
	for _, member := range modelEmitted {
		if !ValidAnswerObligation(member) || inDerived[member] {
			continue
		}
		widened = append(widened, member)
	}
	frame.Obligations = derived
	frame.WidenedObligations = sortedObligations(widened)
	return frame
}

// FrameAxisDischarges returns the declared discharge for every axis the
// frame SETS, in a fixed order (goals in vocabulary order, then temporal,
// then dimensions in published order, then emphasis, then subject kind).
//
// This is law L2 made enumerable, and invariant I16 is its per-frame
// check. The fixed order matters for the same reason the telemetry rows
// are index-ordered: two runs of one frame must produce a diffable list.
func FrameAxisDischarges(frame QuestionFrame) []AxisDischarge {
	discharges := make([]AxisDischarge, 0, len(frame.Goals)+len(frame.Dimensions)+3)

	for _, goal := range frame.Goals {
		if discharge, ok := goalDischarge[goal]; ok {
			discharges = append(discharges, discharge)
		}
	}
	if discharge, ok := temporalDischarge[frame.Temporal]; ok {
		discharges = append(discharges, discharge)
	}
	for _, dimension := range frame.Dimensions {
		discharges = append(discharges, dimensionDischarge(dimension))
	}
	if len(frame.Emphasis) > 0 {
		// Emphasis is discharged by the DERIVED ranking obligation: it
		// says the answer must address both ends of an established
		// ordering, so an ordering must exist. This is the same
		// condition invariant I14 checks; I16 reports it as an axis
		// discharge so that a frame failing both fails I14 first (it is
		// earlier in table order) and the telemetry names the more
		// specific invariant.
		discharges = append(discharges, AxisDischarge{
			Axis:       AxisEmphasis,
			Value:      string(frame.Emphasis[0]),
			Mode:       DischargeByObligation,
			Obligation: ObligationRanking,
		})
	}
	if frame.SubjectExpression.Kind != "" {
		discharges = append(discharges, AxisDischarge{
			Axis:     AxisSubjectKind,
			Value:    string(frame.SubjectExpression.Kind),
			Mode:     DischargeByRequirementProperty,
			Property: PropertySubjectRoleAssignment,
		})
	}
	return discharges
}

// UndischargedAxis returns the FIRST axis the frame sets that its derived
// obligation set does not discharge, and whether one was found.
//
// A DischargeByRequirementProperty axis is discharged BY DECLARATION: the
// property is named in the table, and whether S7b-ii's requirement rows
// implement it is that slice's evidence obligation, not a check this one
// can make. Pretending otherwise would be the inaccurate-coverage failure
// acr AGENTS.md names -- a reader who sees a check stops verifying.
func UndischargedAxis(frame QuestionFrame) (AxisDischarge, bool) {
	for _, discharge := range FrameAxisDischarges(frame) {
		if discharge.Mode != DischargeByObligation {
			continue
		}
		if !frame.HasObligation(discharge.Obligation) {
			return discharge, true
		}
	}
	return AxisDischarge{}, false
}
