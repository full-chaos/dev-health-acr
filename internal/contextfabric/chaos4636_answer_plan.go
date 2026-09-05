package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4636 (S5 of the CHAOS-4452 intent-engine design, §6): the AnswerPlan
// and the deterministic PlanAnswer stage that produces it.
//
// PlanAnswer is a PURE FUNCTION of the family definition, the interpretation,
// the declared fact capabilities and the request's effective budget. It is
// not a model call, it performs no I/O, and it consults no clock -- the same
// discipline RankCohort holds itself to, and for the same reason: a planning
// decision that could vary between two identical requests is not a plan.
//
// What the plan replaces. Three things that were previously implicit or
// special-cased become one lookup into a declared table:
//
//   - `interpretation.FactRequirements` stops BEING the plan and becomes one
//     input to it -- a WIDENING-ONLY input. The model may add a kind the
//     family did not name; it may never remove one.
//   - `statusCategoryFactKindComposition`, which was a `status`-only special
//     case bolted on beside the plan.
//   - the `if Cohort != nil` ranking-kind injection in engine.go, which
//     becomes a family row: a family whose subject axis is a cohort declares
//     the ranking-formula kinds, rather than the engine testing a pointer.
//
// The family table (chaos4632_question_family_registry.go) already DECLARES
// every column this stage needs -- ApplicableAxes, FactRoles, RequireDrivers,
// RequireRanking, RenderKinds, Budget. S2 shipped them "declared, not
// consumed". This is the slice that consumes them.

// AnswerPlan is the wire type. Alias, never a copy -- see
// contractsv1.ContextFabricAnswerPlan for the shape and for why it is
// persisted rather than merely computed.
type AnswerPlan = contractsv1.ContextFabricAnswerPlan

// AnswerPlanBudget, PlanNarrowing, NarrowingBasis and PlanNarrowingStage are
// the plan's own supporting types, aliased for the same reason.
type (
	AnswerPlanBudget    = contractsv1.ContextFabricAnswerPlanBudget
	PlanNarrowing       = contractsv1.ContextFabricPlanNarrowing
	NarrowingBasis      = contractsv1.ContextFabricNarrowingBasis
	PlanNarrowingStage  = contractsv1.ContextFabricPlanNarrowingStage
	ResponseBudget      = contractsv1.ContextFabricResponseBudget
	ResponseMeasurement = contractsv1.ContextFabricResponseMeasurement
	BudgetOverrun       = contractsv1.ContextFabricBudgetOverrun
)

// cohortRankingFormulaKinds is the five-kind set RankCohort's documented
// formula needs. It was an inline literal in engine.go behind
// `if graphContext.Cohort != nil`; it is a named set here so a family row can
// declare it and the engine can stop testing a pointer.
//
// The set itself is UNCHANGED, deliberately: this slice moves where the
// decision is made, not what the decision is. RankCohort's formula and
// weights are explicitly out of scope (design §8, "left alone, on purpose").
var cohortRankingFormulaKinds = []FactKind{
	FactHealth, FactWorkload, FactReadiness, FactOperationalDeficiencies, FactInvestment,
}

// planSynthesisHeadroom is the item allowance stage 2 holds back for what
// SYNTHESIS WILL ADD -- drivers, remaining work, readiness gaps, conflicts
// and claimed facts. Those are the terms the item budget charges, and none of
// them exists before synthesis has run, which is the whole reason stage 2
// cannot simply count the answer.
//
// These are the values the rig gate MEASURES; they are a starting estimate
// per budget profile, not a derivation. Design §6.3 is explicit that "the
// exact clamp is not derivable on paper -- it depends on drivers-per-member,
// which synthesis decides", and §10 D4 defers the magnitude to S5's
// measurement rather than letting anyone pick it in advance. If the measured
// value differs, THIS TABLE is what moves, and the narrowing telemetry is
// what says by how much.
func planSynthesisHeadroom(profile PlanBudgetProfile) int {
	switch profile {
	case PlanBudgetSingleSubject:
		return 12
	case PlanBudgetFlatCohort:
		return 16
	case PlanBudgetGroupedCohort:
		// Higher than a flat cohort: a grouped answer carries per-group
		// drivers on top of per-member ones, which is exactly what
		// RequireDrivers=true on the grouped family asks for.
		return 20
	case PlanBudgetMatchedPair:
		return 12
	}
	// PlanBudgetUnbounded and any future profile reserve nothing. Reserving
	// a guess would narrow an answer that had no need to narrow.
	return 0
}

// PredictedAnswerItems is what the PLAN ITSELF budgeted a cohort of this size
// to cost: one item per member, plus the synthesis headroom the profile
// reserved for what synthesis adds on top. It is the plan's own arithmetic, not
// a second model of it -- every term is a field the plan already publishes.
//
// It exists to be logged BESIDE the measured count, because the plan's
// expectation is exactly the thing that turned out to be wrong on the rig and
// there was no way to see that from the artifacts. Measured against this
// prediction, a grouped answer of 7 members came in anywhere from 21 to 41
// items against a predicted 27 (testdata/grouped_cohort_item_ratio.json).
//
// Deliberately NOT a per-member rate. An earlier revision of this function
// predicted from a measured items-per-member ratio and used it to clamp the
// cohort; the rig then showed the ratio RISES as the cohort shrinks (2.80-3.90
// at 10 members, 3.00-5.86 at 7), so total items are largely insensitive to
// member count and the rate was not a property of the system. Publishing a
// prediction from that model would be a number that looks like evidence.
func PredictedAnswerItems(headroom, members int) int {
	if members <= 0 || headroom < 0 {
		return 0
	}
	return members + headroom
}

// PredictedItemsForPlan is PredictedAnswerItems addressed by the plan a
// telemetry site already holds, so a caller cannot pair a measurement with a
// prediction derived from a different budget than the one that planned it.
//
// DOMAIN: every member count planBudget can admit, which is
// MaxItems - SynthesisHeadroom and rises with the item budget -- 25 at
// ACR_MAX_ITEMS=45, 38 at the configured maximum of 50. There is no clamp here
// and there must not be one: codex round 3 found that capping this helper at a
// constant leaves every call site holding the right count while the helper
// corrupts it afterwards, and no test noticed because they all drove cohorts of
// ten or fewer. A clamp here would be invisible exactly where the item budget is
// raised, which is the configuration the rig is moving to.
func PredictedItemsForPlan(plan AnswerPlan, members int) int {
	return PredictedAnswerItems(plan.Budget.SynthesisHeadroom, members)
}

// PlanAnswerInput is everything PlanAnswer is allowed to see. It is a struct
// rather than a parameter list so that adding an input is a visible change to
// the stage's contract rather than a silently widened signature.
type PlanAnswerInput struct {
	// Family is the resolved outcome from the S2 consensus resolver. Its
	// WinningSample carries the GroupKind and ScopeAnchorTerm the grouped
	// and scoped families key on.
	Family QuestionFamilyOutcome
	// Interpretation is the model's own reading. Its FactRequirements are a
	// WIDENING input: they may add a kind, never remove one.
	Interpretation InterpretedQuestion
	// Budget is the EFFECTIVE ceiling for this request -- service
	// configuration already narrowed by anything the caller asked for. A
	// zero on either axis means unbounded on that axis.
	Budget ResponseBudget
	// MaxCohortMembers is the caller's own cohort cap. The plan never
	// exceeds it, so a caller asking for fewer members always gets fewer.
	MaxCohortMembers int
	// Requirements are THIS TURN'S derived requirement rows, and they are an
	// input to the plan rather than only an output stamped onto it.
	//
	// The rows are what say a computed obligation's server step consumes
	// named fact kinds (the §13.2.3 amendment), and a declaration nothing
	// reads plans nothing -- which is the blocking cell the parity proof
	// recorded. planFactKinds reads them so the declaration becomes the
	// plan, and it is passed in rather than derived here because the stage is
	// pure over its input: PlanAnswer holds no frame, no registry and no
	// deriver, and giving it one would make the plan a function of state it
	// cannot see.
	//
	// EMPTY IS LEGAL AND MEANS EXACTLY WHAT IT SAYS: a turn with no validated
	// frame derives no rows, so the plan's fact kinds are what they were
	// before this field existed. That is the same honest absence
	// deriveTurnRequirements already returns for a nil frame.
	Requirements []DerivedRequirement
}

// PlanAnswer produces the plan. It never fails: an unresolved or
// unrecognized family plans as `unclassified`, which is today's behaviour
// unchanged, never an error and never a guess.
func PlanAnswer(input PlanAnswerInput) AnswerPlan {
	family := input.Family.Family
	source := input.Family.Source
	if !contractsv1.ValidContextFabricQuestionFamily(family) {
		family = QuestionFamilyUnclassified
	}
	if !contractsv1.ValidContextFabricQuestionFamilySource(source) {
		source = QuestionFamilySourceNone
	}
	definition, found := LookupQuestionFamily(family)
	if !found {
		// LookupQuestionFamily covers every vocabulary member, so this is
		// unreachable today. It is handled rather than asserted because an
		// unplannable question must still be answerable the way it is
		// answered now.
		definition = QuestionFamilyDefinition{Family: family, Budget: PlanBudgetUnbounded}
	}

	plan := AnswerPlan{
		Family:         family,
		FamilySource:   source,
		FamilyVersion:  QuestionFamilyTableVersion,
		RequireDrivers: definition.RequireDrivers,
		RequireRanking: definition.RequireRanking,
		RenderKinds:    append([]contractsv1.ContextFabricRenderKind(nil), definition.RenderKinds...),
		Axes:           planWireAxes(definition),
		FactKinds:      planFactKinds(definition, input.Interpretation, input.Requirements),
		Budget:         planBudget(definition.Budget, input.Budget, input.MaxCohortMembers),
	}
	// The group axis is a MODEL-EMITTED signal, and only the grouped family
	// keys on it. Reading it on any other family would let a spurious
	// emission -- the exact failure mode CHAOS-4632's gate measured for,
	// finding 0 across 18 labelled negative cases -- partition an answer
	// that nobody asked to have partitioned.
	//
	// SEAM 7 (CHAOS-4736) changed the SOURCE, not the field. The name
	// stays `plan.GroupKind` and every existing reader of it -- the grouped
	// cohort assembly, the plan contract's own GroupKind != MemberKind
	// refusal -- is untouched. What changed is where the value comes from:
	// the frame's `grouped_members.group_kind`, a field whose invariant I6
	// already forbids equalling MemberKind, in preference to the winning
	// sample's loose GroupKind capture.
	//
	// The sample remains the source for a turn with NO validated frame,
	// and that is not a prose fallback: the sample's GroupKind is itself a
	// closed-vocabulary model emission, sanitized, never a substring match.
	// It is the pre-seam-7 path preserved for the turns the frame did not
	// reach, and such a turn discovers no cohort anyway, so the axis has
	// nothing to group.
	if definition.SubjectAxis == SubjectAxisManyGrouped {
		plan.GroupKind = input.Family.WinningSample.GroupKind
		if input.Family.Frame != nil {
			if groupKind, ok := input.Family.Frame.SubjectExpression.GroupKind(); ok {
				plan.GroupKind = groupKind
			}
		}
	}
	return plan
}

// planWireAxes narrows the family's declared ApplicableAxes to the axes that
// are actually WIRE vocabulary members, preserving the family's own order.
//
// Two of the family table's axes are deliberately package-local and are NOT
// members of ContextFabricStructureNeedKind: StructureNeedGroupKind and
// StructureNeedScopeAnchor (S4 kept them off the wire, and a registry test
// pins that they stay off). The plan is a WIRE document, so it may not carry
// them. Scope anchor is not lost by this: S4 already maps it onto the wire's
// existing subject_anchor axis, which is the vehicle a scoping
// disambiguation actually rides, and that mapping is applied here too so the
// plan discloses the axis a caller will really be asked about.
func planWireAxes(definition QuestionFamilyDefinition) []contractsv1.ContextFabricStructureNeedKind {
	axes := make([]contractsv1.ContextFabricStructureNeedKind, 0, len(definition.ApplicableAxes))
	seen := make(map[contractsv1.ContextFabricStructureNeedKind]struct{}, len(definition.ApplicableAxes))
	appendAxis := func(axis contractsv1.ContextFabricStructureNeedKind) {
		if !contractsv1.ValidContextFabricStructureNeedKind(axis) {
			return
		}
		if _, exists := seen[axis]; exists {
			return
		}
		seen[axis] = struct{}{}
		axes = append(axes, axis)
	}
	for _, axis := range definition.ApplicableAxes {
		if axis == StructureNeedScopeAnchor {
			appendAxis(contractsv1.ContextFabricStructureNeedSubjectAnchor)
			continue
		}
		appendAxis(axis)
	}
	return axes
}

// planFactKinds is the union the design specifies: the family's own declared
// kinds, plus the model's, as a widening.
//
// ORDER MATTERS AND IS DELIBERATE. The model's kinds come FIRST, so that when
// both name the same kind the model's entry -- which may carry its own
// Subjects or Parameters -- is the one that survives mergeFactRequirements'
// first-kind-wins dedup downstream. The family's kinds FILL what is otherwise
// absent. That is exactly the position the hardcoded ranking injection held
// in engine.go before this stage existed, so the resulting set is unchanged
// for every family that resolved to a cohort before.
func planFactKinds(definition QuestionFamilyDefinition, interpretation InterpretedQuestion, requirements []DerivedRequirement) []FactKind {
	kinds := make([]FactKind, 0, len(interpretation.FactRequirements)+len(cohortRankingFormulaKinds))
	seen := make(map[FactKind]struct{}, cap(kinds))
	appendKind := func(kind FactKind) {
		if kind == "" {
			return
		}
		if _, exists := seen[kind]; exists {
			return
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	for _, requirement := range interpretation.FactRequirements {
		appendKind(requirement.Kind)
	}
	// A cohort answer needs the ranking formula's inputs READ, whatever it
	// then does with them. RequireRanking decides whether a CROSS-COHORT
	// ranking is produced and rendered -- for a grouped status list it is
	// false, because one ranking across every team answers a question
	// nobody asked -- but it does not decide whether the member facts are
	// fetched. Conflating the two would leave a grouped answer with no
	// health or workload facts at all, which is a strictly worse answer,
	// not a narrower one.
	if isCohortSubjectAxis(definition.SubjectAxis) {
		for _, kind := range cohortRankingFormulaKinds {
			appendKind(kind)
		}
	}
	// EVERY DECLARED INPUT OF A COMPUTED STEP THIS TURN'S ROWS SAY THE SERVER
	// EXECUTES. Before this, a computed row declared what its step consumes
	// and nothing read the declaration, so the facts were fetched only
	// because a family's SubjectAxis happened to be a cohort -- which is why
	// the parity proof could not retire that injection without dropping a
	// real read.
	//
	// It is a WIDENING like the two above it, and it runs LAST for the same
	// first-kind-wins reason: a kind the model or the family already named
	// keeps that entry, with whatever Subjects or Parameters it carries. On
	// every cohort frame in the corpus this adds nothing at all -- rank_cohort
	// declares cohortRankingFormulaKinds ITSELF, so the two sources name the
	// same five kinds -- and that identity is what makes the unconditional
	// injection a candidate for retirement rather than a second spender.
	for _, kind := range ComputedStepInputReads(requirements) {
		appendKind(kind)
	}
	return kinds
}

// isCohortSubjectAxis reports whether this family answers about many
// subjects. It replaces engine.go's `if graphContext.Cohort != nil` pointer
// test as the reason the ranking kinds are read: a declared property of the
// family, checkable before the read, rather than a fact discovered after it.
func isCohortSubjectAxis(axis SubjectAxisKind) bool {
	switch axis {
	case SubjectAxisManyNamed, SubjectAxisManyDiscovered, SubjectAxisManyScoped, SubjectAxisManyGrouped:
		return true
	}
	return false
}

// planBudget derives the plan's ceiling and its stage-1 member clamp.
func planBudget(profile PlanBudgetProfile, budget ResponseBudget, maxCohortMembers int) AnswerPlanBudget {
	headroom := planSynthesisHeadroom(profile)
	planned := AnswerPlanBudget{
		MaxItems:           budget.MaxItems,
		MaxSerializedBytes: budget.MaxSerializedBytes,
		SynthesisHeadroom:  headroom,
		// Disclosed whether or not stage 1 needs to act. An arbitrary
		// order stated up front is honest; the same order applied silently
		// is the group-blind truncation this slice exists to remove.
		NarrowingBasis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
	}
	planned.MaxMembers = maxCohortMembers
	if budget.MaxItems > 0 {
		// Every cohort member costs one item BEFORE a single claimed fact
		// or driver is charged. That is the arithmetic §6.3 corrects: the
		// original estimate double-counted facts as items while omitting
		// cohort members entirely, and the binding constraint is much
		// tighter than it implied.
		//
		// Candidates are NOT subtracted here, and that is not an oversight:
		// SubjectResolution.Candidates is not known before discovery, and a
		// clamp guessing at it would narrow answers that had room. Stage 3
		// charges them, where they exist.
		allowance := budget.MaxItems - headroom
		if allowance < 1 {
			// A budget smaller than its own headroom still admits one
			// member. Zero members is not a narrower answer, it is a
			// different (and empty) one.
			allowance = 1
		}
		if planned.MaxMembers <= 0 || allowance < planned.MaxMembers {
			planned.MaxMembers = allowance
		}
	}
	if planned.MaxMembers < 0 {
		planned.MaxMembers = 0
	}
	if planned.SynthesisHeadroom > planned.MaxItems && planned.MaxItems > 0 {
		// Keep the plan self-consistent for its own validator: a profile
		// reserving more headroom than the whole budget reserves the whole
		// budget instead, and stage 3 then does the real work.
		planned.SynthesisHeadroom = planned.MaxItems
	}
	return planned
}

// planNarrowingStep records one narrowing on the plan. It is the ONLY way a
// narrowing reaches the wire, so a step that happened without one of these is
// a step the caller was never told about.
func (e *Engine) recordPlanNarrowingStep(plan *AnswerPlan, step PlanNarrowing) {
	if plan == nil || step.Before == step.After {
		return
	}
	if len(plan.Narrowing) >= contractsv1.ContextFabricPlanNarrowingMaxCount {
		return
	}
	plan.Narrowing = append(plan.Narrowing, step)
}

// stampAnswerPlan attaches the plan to a result produced by one of the
// EARLY-RETURN paths -- a window-confirmation clarification, or a terminal
// result with no investigable subject.
//
// Those paths are not an afterthought here; for a grouped question, turn 1 IS
// one of them. A caller asked to confirm a window on "the project statuses for
// each team" needs to know the answer was planned as a grouped cohort just as
// much as the caller of turn 2 does, and an operator diagnosing a
// clarification loop needs the family that produced it. Leaving the plan off
// these paths would mean the disclosure existed only where the answer already
// succeeded.
//
// Never overwrites a plan a caller path already stamped, and never stamps onto
// a zero result -- an errored return carries no answer to describe.
func stampAnswerPlan(result InvestigationResult, plan AnswerPlan) InvestigationResult {
	if result.ResultID == "" || result.AnswerPlan != nil {
		return result
	}
	stamped := plan
	result.AnswerPlan = &stamped
	return result
}

// finalizeResult stamps everything that is part of the SERVED document but is
// not produced by synthesis: the plan, the render shapes it authorizes, and
// the completeness block.
//
// It exists so that stage 3 measures the document the route will marshal.
// Codex round 1 finding 1 (P1) was exactly this: these three were stamped
// AFTER the budget check, so the engine measured a smaller thing than the
// route, and gate agreement -- the whole reason the measurement moved to
// internal/contracts/v1 -- did not hold on the byte axis.
//
// Pure, and deliberately telemetry-free: it runs once per synthesis pass, and
// a retry must not double-count a render-selection decision. The engine emits
// that event once, for the result it actually serves.
func (e *Engine) finalizeResult(result InvestigationResult, plan AnswerPlan, frame *QuestionFrame) InvestigationResult {
	stamped := plan
	result.AnswerPlan = &stamped
	renderShapes, _ := SelectRenderShapes(result, frame)
	result.RenderShapes = renderShapes
	// Seed the outcome set from this turn's requirement rows, ONCE.
	//
	// Idempotent on purpose: this function runs again on a retry pass and
	// again after assembly narrows, and re-seeding would either duplicate
	// the seed rows or overwrite the rows a later stage appended. Seeding
	// only into an empty set is what makes "stages append, nothing
	// rewrites" true of the code rather than of the intention.
	if len(result.Completeness.Outcomes) == 0 {
		result.Completeness.Outcomes = seedRequirementOutcomes(frame, e.requirements)
	}
	// Run the `membership_cardinality` server step over the member set this
	// document actually carries, and state the result on it.
	//
	// HERE, and not earlier, for the reason this function exists: the count
	// must describe the document the route will marshal. Stage 3 can narrow
	// the cohort and re-synthesize, and it re-finalizes, so a cardinality
	// computed before that would name a member set the reader never
	// receives -- the same defect round 1 finding 1 recorded for the plan
	// and the render shapes.
	//
	// AFTER the seed, because the step's own requirement identity is read
	// off the seeded rows rather than minted here, and appended through
	// appendOutcomeRows like every other stage's row.
	rows, _, _ := appendMembershipCardinality(result.Completeness.Outcomes, result.Cohort, plan.Narrowing)
	result.Completeness.Outcomes = rows
	// Say what the EVIDENCE made of each planned READ requirement.
	//
	// HERE, for the reason the cardinality above it runs here: the row must
	// describe the document the route will marshal, and stage 3 can narrow
	// the cohort and re-finalize. AFTER the seed, because the identity is
	// read off the published plan rather than minted, and BEFORE the state
	// is derived below -- appending after the derivation would be the
	// measure-then-shrink defect this whole layer exists to remove,
	// reproduced inside the fix for it.
	//
	// The plan is read from `stamped`, the copy this function attached to
	// the result, so the requirements evaluated are exactly the ones the
	// served document publishes. Reading the `plan` parameter instead would
	// be the same value today and a second source the first time a caller
	// stamps something else.
	result.Completeness.Outcomes = appendReadRequirementEvaluations(
		result.Completeness.Outcomes, stamped.Requirements, result.Coverage)
	result.Completeness = ComputeAnswerCompleteness(result)
	return result
}
