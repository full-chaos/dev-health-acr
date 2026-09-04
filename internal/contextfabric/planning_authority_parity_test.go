package contextfabric_test

// THE SIX-AUTHORITY PARITY PROOF. Design 13.8a (the six planning
// authorities), 13.15.2 (the N2 parity row), 13.15.3 (nothing is retired
// before this proof), law L10.
//
// THE QUESTION THIS ANSWERS, precisely. 13.8a: "after stage 2 there is ONE
// source of semantic truth -- the frame -- and [SIX] legacy sources of
// PLANNING truth that S7a does not retire." 13.15.2's N2 row asks the
// retirement proof to show that "the derived requirement rows reproduce,
// per acceptance question, the union of [the six] -- OR states which it
// drops and why the answer does not change on the section 9 projection".
// This file computes that, per frame, per authority.
//
// IT IS A SHADOW PROOF AND DRIVES NOTHING. No authority is deleted, no
// engine path changes, no wire field moves. The output is a measurement
// and a per-authority verdict; the retirement ORDER it implies is a
// paragraph in the PR body, not code. That is deliberate: retiring
// authorities 1-3 moves plan.fact_kinds and require_drivers/require_ranking
// (design 13.9 B7/B9), whose own gates are a labelled-set before/after
// programme on the rig, not a table test.
//
// WHAT MAKES THIS A MEASUREMENT RATHER THAN A TABLE. Every authority's
// contribution is produced by CALLING that authority's own production
// function through a test-only shim in export_test.go. Nothing here
// re-implements an authority, and nothing here writes down what an
// authority "should" return. The verdicts are set operations over those
// outputs, and the numbers live in the regenerated artifact rather than in
// constants this file could be edited to agree with -- the correction the
// declaration slice needed after three rounds each found a hole in the
// previous round's fix.
//
// CORPUS SAFETY. Frames come from traceFrames() (requirement_trace_test.go):
// question IDs and STRUCTURAL labels only, built through the shipped frame
// layer. No corpus question text reaches this file, its output or its
// artifact.
//
// HOST-ONLY. The registry reader is liveCapabilityList (obligation_seed_test.go),
// which reads Capability() declarations and constructs no provider client.
// Nothing here starts a container.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const planningAuthorityParityArtifact = "testdata/planning_authority_parity.txt"

// authorityVerdict is the closed per-(frame, authority) verdict vocabulary.
//
// The tokens are PRE-REGISTERED: they and their decision rules were fixed
// before the harness was run, so a number that came out awkward could not
// be relabelled into a pass. That is the standing rule for a sweep whose
// result is the evidence.
type authorityVerdict string

const (
	// verdictSubsumed: everything this authority contributes for this
	// frame is ALSO named by the derived requirement rows. Retiring it
	// loses nothing here.
	verdictSubsumed authorityVerdict = "subsumed"
	// verdictNotSubsumed: the authority contributes at least one fact
	// kind the derived rows do not name. Retiring it LOSES those kinds.
	// The lost kinds are printed; this is a delta reported with both
	// numbers, never rounded to a pass.
	verdictNotSubsumed authorityVerdict = "not_subsumed"
	// verdictNotApplicable: this authority contributes nothing to this
	// frame -- it does not fire on this topology. Distinct from
	// `subsumed`, because "contributes nothing" and "contributes only
	// things we also derive" are different facts about a retirement.
	verdictNotApplicable authorityVerdict = "not_applicable"
	// verdictAdvisoryByRule: the authority is model-proposed widening,
	// which design 13.2.4 rules advisory and unable to degrade
	// completeness. Its retirement is governed by that rule, not by a
	// kind comparison, and it has no per-frame value to compare.
	verdictAdvisoryByRule authorityVerdict = "advisory_by_rule"
	// verdictNotReproducible: the authority reads POST-RESOLUTION or
	// POST-RETRIEVAL state. Design 13.8a, verbatim: "Sources 4 and 5 are
	// POST-RESOLUTION and POST-RETRIEVAL, which is why requirement rows
	// derived from a PRE-resolution frame cannot reproduce them and why
	// the 'derived fact_kinds union' will still not equal the read set."
	// This is a DISCLOSED DROP under 13.15.2's escape clause, never a pass.
	verdictNotReproducible authorityVerdict = "not_reproducible_by_construction"
	// verdictDroppedPendingMechanism: the authority's stated retirement
	// mechanism does not exist in the tree yet.
	verdictDroppedPendingMechanism authorityVerdict = "dropped_pending_mechanism"
)

var authorityVerdicts = [...]authorityVerdict{
	verdictSubsumed, verdictNotSubsumed, verdictNotApplicable,
	verdictAdvisoryByRule, verdictNotReproducible, verdictDroppedPendingMechanism,
}

// lossCause explains WHY a not_subsumed cell loses what it loses, and it is
// the part of this proof that decides a retirement.
//
// THE FIRST FULL PASS REFUTED THE NAIVE PROPERTY, and the refutation is the
// result rather than a problem to tune away. "Every reproducible authority
// is subsumed" is FALSE: 14 of 42 pre-resolution cells lose something. But
// the losses are not one thing. Some are the derivation being NARROWER AND
// RIGHT -- an authority injecting fact kinds for a step this frame never
// asks for -- and some are a REAL GAP the derivation has to close before
// anything is retired. A single boolean cannot tell those apart, and a
// harness edited until the boolean went green would have hidden both.
//
// THE ONLY BRANCH THAT MAY RULE THE DERIVATION SUPERIOR is the one where
// nothing in the frame could consume the lost kinds. Every other shape
// falls through to the conservative cause, which says "not retirable on
// this evidence". A classifier whose default is the favourable answer
// decides its own result.
type lossCause string

const (
	// causeUnavailableNamedInstead: every derived row for this frame is an
	// explicit `unavailable` with a closed reason, where the authority
	// would have emitted a requirement no producer can serve.
	//
	// RULED SUPERIOR, and the rule is the design's own B5 row plus
	// devhealthfacts/shared.go, which say in terms that an organization
	// state requirement should derive an explicit `unavailable` rather
	// than a confident wrong answer. Naming the reason is strictly more
	// than emitting a requirement guaranteed to be pruned
	// subject_kind_unsupported.
	causeUnavailableNamedInstead lossCause = "unavailable_named_instead"

	// causeNotRequiredByAnyObligation: this frame derives NO computed
	// obligation, so there is no step whose inputs the lost kinds could
	// be, and no read obligation names them either. The authority is
	// injecting them unconditionally.
	//
	// RULED SUPERIOR. The rule is that a requirement layer plans what the
	// frame's OBLIGATIONS demand: the lost kinds serve no read obligation
	// this frame derives and no computed step it names, so the authority
	// is planning a read for a purpose the question did not state. That is
	// the widening design 13.2.4 rules "advisory" and forbids from
	// changing completeness.
	//
	// A SECOND, WEAKER CORROBORATION, stated as weaker because it does not
	// hold for every cell in this class: where the lost kind also has no
	// producer for that subject kind, the retired read could only ever
	// prune, which graphrank/cohort_fact_requirements.go independently
	// calls "not a requirement, it is noise in the record an operator
	// reads to find real gaps". That is true of the project-member cells
	// here and NOT of the team-operand ones, so it corroborates the ruling
	// on some cells and carries none of it on its own.
	causeNotRequiredByAnyObligation lossCause = "not_required_by_any_obligation"

	// causeComputedStepInputUnserved: the lost kind IS a declared input of a
	// computed step this frame derives, and NO read row serves it. Retiring
	// the authority would remove the only thing planning to read a fact the
	// computation consumes.
	//
	// THIS TOKEN REPLACES `computed_obligation_inputs_undeclared`, and the
	// replacement is the §13.2.3 amendment's whole effect on this proof.
	// Before the amendment a computed row named its server step and nothing
	// else, so this classifier could not ask whether a lost kind was an
	// input -- it had to assume every loss on a frame with a computation
	// might be one, and said so with a token that named the GAP rather than
	// the defect. Now the step declares what it consumes, the question is
	// answerable per cell, and the losses that were never inputs fall
	// through to the superior cause above.
	//
	// STILL NOT SUPERIOR, and deliberately so. Declaring an input makes the
	// NEED legible; it does not plan a read for it. A cell here is one where
	// the computation's input is declared and unserved, so retiring the
	// authority still changes what gets read. Erring toward "not retirable"
	// remains the only safe direction for a proof whose output is a deletion
	// order. What must change to clear such a cell is now NAMEABLE, which it
	// was not before: either a producer serves the kind for that subject, or
	// the computed step's declared input is planned as a read of its own.
	causeComputedStepInputUnserved lossCause = "computed_step_input_unserved"

	// causeComputedStepNotWired: this frame derives a computed obligation
	// whose server step is DECLARED ONLY -- named by the vocabulary, executed
	// by nothing. Its "consumes no fact kinds" declaration is therefore not
	// evidence about what the ANSWER depends on.
	//
	// FOUND BY AN ADVERSARIAL ROUND ON THIS CHANGE, and it is the mirror of
	// the rule this slice already states in the other direction. Declaring an
	// INPUT is not planning a read; declaring NO input is not proof the answer
	// needs nothing. `count` is satisfied today by the model narrating over
	// whatever facts the plan read (status_shadow.go records it as unobserved:
	// "a cardinality is carried in the answer text, not in a countable
	// field"), so retiring whatever caused those facts to be read CAN change
	// the answer, and a SUPERIOR ruling asserts precisely that it cannot.
	//
	// NOT SUPERIOR. Without this the five counting-frame cells clear on the
	// strength of a step nothing runs, and the proof -- whose output is a
	// deletion order -- authorizes removing a real fact read. What must change
	// is nameable: wire the step, i.e. satisfy `count` from the resolved
	// member set as a server result rather than as narrated prose.
	causeComputedStepNotWired lossCause = "computed_step_not_wired"

	// causeComputedPopulationUnavailable: this frame derives a computed
	// obligation whose POPULATION cannot be resolved, so the step has no
	// input on this frame and the cell is unavailable.
	//
	// FOUND BY THE SECOND ADVERSARIAL ROUND ON THE WIRING SLICE, and it is
	// the same rule as `computed_step_not_wired` narrowed from the STEP to
	// the FRAME. That cause asks "does anything execute this step at all";
	// this one asks "can it execute on THIS frame". An organization-scope
	// frame naming a member kind derives `count` legitimately and nothing
	// discovers the population, so the value still reaches the reader by
	// narration over whatever facts were read -- and under that mechanism
	// retiring an authority's reads CAN change the answer, which is exactly
	// what a SUPERIOR ruling asserts it cannot.
	//
	// NOT SUPERIOR, for the reason the execution cause is not: a declaration
	// that the server executes a step is evidence about the answer only
	// where the step can actually run. Reading the per-step declaration
	// alone and clearing these cells is how the withdrawn "authorities 1 and
	// 5a are retirable" measurement was produced the first time; reading it
	// per frame is what stops that recurring one level in.
	//
	// What must change is nameable: give the organization scope a member-set
	// source, or stop deriving a countable population where none can be
	// resolved.
	causeComputedPopulationUnavailable lossCause = "computed_population_unavailable"
)

var lossCauses = [...]lossCause{
	causeUnavailableNamedInstead,
	causeNotRequiredByAnyObligation,
	causeComputedStepInputUnserved,
	causeComputedStepNotWired,
	causeComputedPopulationUnavailable,
}

// supersedingCauses are the causes under which the derived rows are ruled
// SUPERIOR to the authority: the authority's extra kinds are not needed for
// this frame, and the answer does not change on the section 9 projection
// because nothing was planning to read them for a purpose the frame states.
var supersedingCauses = map[lossCause]bool{
	causeUnavailableNamedInstead:    true,
	causeNotRequiredByAnyObligation: true,
}

// classifyLoss decides the cause from the frame's OWN derived rows and the
// SPECIFIC kinds this cell lost.
//
// It reads the rows rather than the frame's vocabulary, so it cannot
// disagree with the derivation it is explaining -- and it never consults
// the authority, so the explanation is not computed by the thing it is
// about.
//
// WHAT THE §13.2.3 AMENDMENT CHANGED HERE, and it is the only reason this
// function takes `lost` at all. The pre-amendment version could ask only
// "does this frame derive ANY computation?", because a computed row named no
// inputs; one computation anywhere meant every loss on the frame had to be
// treated as a possible input of it. Now the step declares what it consumes,
// so the question is asked of THE KINDS ACTUALLY LOST, per cell. A loss that
// is not an input of any computation this frame derives falls through to the
// superior cause it always belonged to.
func classifyLoss(rows []contextfabric.DerivedRequirement, lost []contextfabric.FactKind) lossCause {
	if len(rows) == 0 {
		// No rows at all is not a shape any frame in the corpus has (O9
		// fails a frame that derives none), but a future frame that
		// derived nothing must not read as superior.
		return causeComputedStepInputUnserved
	}
	served := 0
	// declaredInputs is the union of every computed row's DECLARED inputs.
	// Read off the ROWS, never re-derived from the step table: the rows are
	// what the artifact and any consumer see, so a classifier consulting the
	// table again could disagree with the thing it claims to explain.
	declaredInputs := map[contextfabric.FactKind]bool{}
	// servedByARead is every kind a READ row names. A computed step's input
	// that appears here is already planned for; one that does not is the
	// blocking case.
	servedByARead := map[contextfabric.FactKind]bool{}
	// declaredOnly records whether ANY computation on this frame is named but
	// unexecuted. Read off the ROWS, like everything else here.
	declaredOnly := false
	// populationUnavailable records whether ANY computation on this frame
	// came back unavailable because its population cannot be resolved. Also
	// read off the rows: the derivation already decided it, and deciding it
	// a second time here is how two authorities for one fact begin.
	populationUnavailable := false
	for _, row := range rows {
		if row.Served() {
			served++
		}
		if row.StepExecution == contextfabric.ComputedStepDeclaredOnly {
			declaredOnly = true
		}
		if row.Kind == contextfabric.ObligationKindComputed && !row.Served() &&
			row.Unavailable == contextfabric.RequirementReasonComputedPopulationAbsent {
			populationUnavailable = true
		}
		for _, kind := range row.InputFactKinds {
			declaredInputs[kind] = true
		}
		for _, kind := range row.FactKinds {
			servedByARead[kind] = true
		}
	}
	if populationUnavailable {
		// FIRST, and ahead of the all-unavailable superior ruling, because
		// that ruling would otherwise absorb this case and clear the cell.
		//
		// `unavailable_named_instead` is superior on the reasoning that
		// naming a closed reason beats emitting a requirement guaranteed to
		// be pruned -- the authority's extra kinds buy the reader nothing
		// because NOTHING answers that obligation. That reasoning does not
		// hold here: the count still reaches the reader, by the model
		// narrating over whatever facts were read. So the authority's reads
		// can still change the answer, and clearing the cell would authorize
		// removing them on evidence that does not cover the mechanism
		// actually producing the value. That is precisely how the withdrawn
		// retirable measurement was produced the first time.
		return causeComputedPopulationUnavailable
	}
	if served == 0 {
		return causeUnavailableNamedInstead
	}
	for _, kind := range lost {
		if declaredInputs[kind] && !servedByARead[kind] {
			// One unserved input is enough. A cell that lost several kinds
			// of which only one is an unserved input still blocks: the
			// retirement is a single act, and it would take that read with
			// it.
			return causeComputedStepInputUnserved
		}
	}
	if declaredOnly {
		// Checked AFTER the input test so the more specific cause wins: a
		// frame carrying both an unserved input and an unwired step is
		// reported as the input defect, which is the one with a named fix.
		return causeComputedStepNotWired
	}
	return causeNotRequiredByAnyObligation
}

// authorityReach says WHEN an authority's inputs exist, which is what
// decides whether a pre-resolution frame can reproduce it at all.
type authorityReach string

const (
	// reachPreResolution: every input exists on the validated frame, so
	// the contribution is computable and comparable.
	reachPreResolution authorityReach = "pre_resolution"
	// reachPostResolution: the contribution depends on the resolved
	// graph context (a cohort pointer, discovered edges), which does not
	// exist when the frame is validated.
	reachPostResolution authorityReach = "post_resolution"
	// reachModelInput: the contribution IS a model emission, not a
	// derivation of anything.
	reachModelInput authorityReach = "model_input"
	// reachPriorTurn: the contribution comes from a previous turn.
	reachPriorTurn authorityReach = "prior_turn"
)

// planningAuthority is one of the six (with source 5 SPLIT -- see the
// roster).
type planningAuthority struct {
	// id is the design's own numbering, so a reader can hold this table
	// against 13.8a's list without a mapping.
	id string
	// name is the production symbol, not a description.
	name string
	// site is where it lives, for the PR body and the retirement order.
	site string
	// reach decides the verdict class before any set is compared.
	reach authorityReach
	// contribute returns the fact kinds this authority puts into the plan
	// for this frame, by CALLING the authority. A nil return means "does
	// not fire on this frame".
	//
	// It returns ok=false when the authority has no per-frame value at
	// all (a model input, a prior turn), which is a different statement
	// from "fires and contributes nothing".
	contribute func(frame contextfabric.QuestionFrame) (kinds []contextfabric.FactKind, ok bool)

	// sinkArgument is WHICH argument group of the engine's
	// mergeFactRequirements call this authority rides into the fact read,
	// or 0 for an authority that does not reach that call at all.
	//
	// engine.go passes three groups:
	//   1  statusComposedRequirements   <- authority 1 (fed by 2)
	//   2  graphContext.FactRequirements <- authorities 5a and 5b
	//   3  cohortRankingRequirements     <- authorities 3 and 4
	// Authority 6 is 0: the carry acts on the FAMILY, upstream of the
	// sink, which is why it contributes no kinds of its own.
	sinkArgument int
}

// mergeSinkArgumentCount is DERIVED FROM THE ROSTER, never written down.
//
// An earlier revision made it a free-standing `const = 3`, and the mutation
// battery proved that version hollow: deleting an authority from the roster
// left the constant at 3, so the pin stayed green and only the artifact diff
// noticed. A pin that compares the source to a constant tests the constant.
// Deriving it from the roster is what makes losing a sink authority break
// the pin itself.
//
// WHAT THIS PIN CATCHES, stated exactly rather than generously: engine.go
// gaining or losing an argument group, and the roster losing the LAST
// authority of a group. It does NOT catch losing one of two authorities
// that share a group (5a vs 5b, 3 vs 4) -- the artifact diff is what covers
// that, and it is weaker because it depends on the committed artifact.
func mergeSinkArgumentCount() int {
	groups := map[int]bool{}
	for _, authority := range planningAuthorities() {
		if authority.sinkArgument != 0 {
			groups[authority.sinkArgument] = true
		}
	}
	return len(groups)
}

// planningAuthorities is the roster, in design order.
//
// SOURCE 5 IS SPLIT, and the split is a finding rather than a convenience.
// 13.8a describes source 5 as one thing ("graph-derived requirements merged
// after retrieval"). In the tree it is two mechanisms with different
// reachability: a DECLARED per-cohort-kind table (graphrank/
// cohort_fact_requirements.go), which a frame's member kind determines and
// which is therefore reproducible; and requirements derived from the
// relevance of live graph EDGES (graphrank/discover.go), which no frame can
// determine. Folding them together would report the reproducible half as
// unreproducible and hide that the table half is already subsumed.
func planningAuthorities() []planningAuthority {
	return []planningAuthority{
		{
			id: "1", name: "composeStatusCategoryRequirements", site: "chaos4347_status_category_composition.go",
			reach:        reachPreResolution,
			contribute:   contributeStatusComposition,
			sinkArgument: 1,
		},
		{
			id: "2", name: "InterpretedQuestion.FactRequirements (the model's widening)", site: "model.go / genkitruntime/runtime.go",
			reach: reachModelInput,
			// Rides into the sink INSIDE group 1: the composition is
			// handed interpretation.FactRequirements as its input.
			sinkArgument: 1,
			contribute: func(contextfabric.QuestionFrame) ([]contextfabric.FactKind, bool) {
				// There is no per-frame value: this authority IS the
				// model's emission for a particular question, and two
				// paraphrases of one question may emit different sets.
				// Comparing it against the derived rows would be
				// comparing the derivation to an input.
				return nil, false
			},
		},
		{
			id: "3", name: "planFactKinds (the family DEFINITION's own contribution)", site: "chaos4636_answer_plan.go",
			reach:        reachPreResolution,
			contribute:   contributeFamilyDefinition,
			sinkArgument: 3,
		},
		{
			id: "4", name: "cohortRankingFormulaKinds (unconditional cohort injection)", site: "engine.go / chaos4636_answer_plan.go",
			reach:        reachPostResolution,
			contribute:   contributeCohortRankingInjection,
			sinkArgument: 3,
		},
		{
			id: "5a", name: "graphrank.CohortFactRequirements (the DECLARED per-kind table)", site: "graphrank/cohort_fact_requirements.go",
			reach:        reachPreResolution,
			contribute:   contributeDeclaredCohortRequirements,
			sinkArgument: 2,
		},
		{
			id: "5b", name: "graphrank edge-derived requirements (relationMeaning over live edges)", site: "graphrank/discover.go",
			reach:        reachPostResolution,
			sinkArgument: 2,
			contribute: func(contextfabric.QuestionFrame) ([]contextfabric.FactKind, bool) {
				// The kinds depend on which edges the graph returned and
				// on their relevance scores. A frame determines none of it.
				return nil, false
			},
		},
		{
			id: "6", name: "applyCarriedPlan (the carried plan)", site: "chaos4636_plan_carry.go",
			reach: reachPriorTurn,
			// 0: the carry acts upstream of the sink, on the family.
			sinkArgument: 0,
			contribute: func(contextfabric.QuestionFrame) ([]contextfabric.FactKind, bool) {
				// Contributes no fact kinds directly. What it overlays is
				// the FAMILY and the group axis -- and the family is
				// authority 3's input, which is what makes it a planning
				// authority. TestCarriedPlanContributesFamilyNotFactKinds
				// demonstrates that rather than asserting it here.
				return nil, false
			},
		},
	}
}

// contributeStatusComposition runs AUTHORITY 1 for this frame.
//
// The composition expands a BARE status requirement over whichever subject
// kinds an investigation resolved. The subject kinds come from the
// PRODUCTION coordinate derivation for this frame -- never hand-listed --
// so a frame whose topology changes moves this input with it.
func contributeStatusComposition(frame contextfabric.QuestionFrame) ([]contextfabric.FactKind, bool) {
	subjects := frameSubjectRefs(frame)
	if len(subjects) == 0 {
		return nil, true
	}
	composed := contextfabric.ComposeStatusCategoryRequirementsForTest(
		[]contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}}, subjects)
	kinds := make([]contextfabric.FactKind, 0, len(composed))
	for _, requirement := range composed {
		kinds = append(kinds, requirement.Kind)
	}
	return sortedUniqueKinds(kinds), true
}

// contributeFamilyDefinition runs AUTHORITY 3 for this frame.
//
// The family is DERIVED from the frame (the stage-2 projection), then its
// registry definition is handed to the production planFactKinds with a ZERO
// interpretation, which isolates the family's own contribution from
// authority 2's.
func contributeFamilyDefinition(frame contextfabric.QuestionFrame) ([]contextfabric.FactKind, bool) {
	definition, found := contextfabric.LookupQuestionFamily(contextfabric.DeriveQuestionFamily(frame).Family)
	if !found {
		return nil, true
	}
	return sortedUniqueKinds(contextfabric.PlanFactKindsForTest(definition, contextfabric.InterpretedQuestion{})), true
}

// contributeCohortRankingInjection runs AUTHORITY 4 for this frame, at its
// FRAME-SIDE LOWER BOUND.
//
// The engine's own gate is a runtime pointer (`graphContext.Cohort != nil`),
// not a frame property, so a frame that declares no cohort can still get the
// injection when resolution produces one. What a frame CAN decide is whether
// the family it projects to declares a cohort axis, which is the same
// predicate authority 3 uses -- asked here through the production function
// rather than by re-listing the cohort axes.
func contributeCohortRankingInjection(frame contextfabric.QuestionFrame) ([]contextfabric.FactKind, bool) {
	definition, found := contextfabric.LookupQuestionFamily(contextfabric.DeriveQuestionFamily(frame).Family)
	if !found || !contextfabric.IsCohortSubjectAxisForTest(definition.SubjectAxis) {
		return nil, true
	}
	return sortedUniqueKinds(contextfabric.CohortRankingFormulaKindsForTest()), true
}

// contributeDeclaredCohortRequirements runs AUTHORITY 5a for this frame.
//
// The declared table is keyed on the COHORT's kind, which for a cohort
// frame is the member kind the frame itself declares. Read from the
// production coordinate derivation's member role rather than by reaching
// into the union's variant pointers here.
func contributeDeclaredCohortRequirements(frame contextfabric.QuestionFrame) ([]contextfabric.FactKind, bool) {
	var kinds []contextfabric.FactKind
	for _, subject := range frameSubjectKindsInRole(frame, contextfabric.SubjectRoleMember) {
		kinds = append(kinds, graphrank.CohortFactRequirements(subject)...)
	}
	return sortedUniqueKinds(kinds), true
}

// frameSubjectKindsInRole reads the subject kinds this frame demands in one
// role, from the PRODUCTION coordinate derivation.
func frameSubjectKindsInRole(frame contextfabric.QuestionFrame, role contextfabric.SubjectRole) []contextfabric.SubjectKind {
	seen := map[contextfabric.SubjectKind]bool{}
	var kinds []contextfabric.SubjectKind
	for _, coordinate := range contextfabric.DeriveRequirementCoordinates(frame) {
		if coordinate.Role != role || seen[coordinate.Subject] {
			continue
		}
		seen[coordinate.Subject] = true
		kinds = append(kinds, coordinate.Subject)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// frameSubjectRefs builds the investigation-wide subject set an authority
// that keys on resolved subjects would see, one ref per distinct subject
// kind the frame's coordinates name.
//
// The CanonicalID is a placeholder because no authority in the roster reads
// it -- they key on Kind. A test that needed a real id would be measuring
// resolution, not planning.
func frameSubjectRefs(frame contextfabric.QuestionFrame) []contextfabric.SubjectRef {
	seen := map[contextfabric.SubjectKind]bool{}
	var refs []contextfabric.SubjectRef
	for _, coordinate := range contextfabric.DeriveRequirementCoordinates(frame) {
		if seen[coordinate.Subject] {
			continue
		}
		seen[coordinate.Subject] = true
		refs = append(refs, contextfabric.SubjectRef{Kind: coordinate.Subject, CanonicalID: string(coordinate.Subject) + ":parity"})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Kind < refs[j].Kind })
	return refs
}

// derivedFactKinds is the union of every fact kind the derived requirement
// rows name for this frame -- the "derived fact_kinds union" 13.8a talks
// about, built from the PRODUCTION derivation against the LIVE registry.
func derivedFactKinds(frame contextfabric.QuestionFrame, seed contextfabric.ObligationSeed, capabilities []contextfabric.FactCapability) []contextfabric.FactKind {
	var kinds []contextfabric.FactKind
	for _, row := range contextfabric.DeriveRequirements(frame, seed, capabilities) {
		kinds = append(kinds, row.FactKinds...)
	}
	return sortedUniqueKinds(kinds)
}

func sortedUniqueKinds(kinds []contextfabric.FactKind) []contextfabric.FactKind {
	seen := map[contextfabric.FactKind]bool{}
	out := make([]contextfabric.FactKind, 0, len(kinds))
	for _, kind := range kinds {
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true
		out = append(out, kind)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func kindsMinus(left, right []contextfabric.FactKind) []contextfabric.FactKind {
	present := map[contextfabric.FactKind]bool{}
	for _, kind := range right {
		present[kind] = true
	}
	var out []contextfabric.FactKind
	for _, kind := range left {
		if !present[kind] {
			out = append(out, kind)
		}
	}
	return out
}

func renderKinds(kinds []contextfabric.FactKind) string {
	if len(kinds) == 0 {
		return "-"
	}
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return strings.Join(names, " ")
}

// parityCell is one (frame, authority) row of the proof.
type parityCell struct {
	frameID     string
	authorityID string
	contributed []contextfabric.FactKind
	derived     []contextfabric.FactKind
	// lost is what retiring this authority would REMOVE from the plan:
	// contributed \ derived. Empty is the only safe value.
	lost []contextfabric.FactKind
	// gained is derived \ contributed -- the growth design 13.9 B9
	// predicts for plan.fact_kinds. Reported, never asserted away.
	gained  []contextfabric.FactKind
	verdict authorityVerdict
	// cause is set on not_subsumed cells ONLY, and decides whether the
	// derivation is ruled superior for this cell or the authority stays.
	cause lossCause
}

// superior reports whether this cell's loss is one the derivation is ruled
// superior on.
func (c parityCell) superior() bool {
	return c.verdict == verdictNotSubsumed && supersedingCauses[c.cause]
}

// computeParityCell is the whole decision, and it is a set operation over
// two production outputs. There is no table to edit.
func computeParityCell(
	frameID string,
	authority planningAuthority,
	frame contextfabric.QuestionFrame,
	seed contextfabric.ObligationSeed,
	capabilities []contextfabric.FactCapability,
) parityCell {
	rows := contextfabric.DeriveRequirements(frame, seed, capabilities)
	derived := derivedFactKinds(frame, seed, capabilities)
	cell := parityCell{frameID: frameID, authorityID: authority.id, derived: derived}

	contributed, comparable := authority.contribute(frame)
	cell.contributed = contributed

	if !comparable {
		switch authority.reach {
		case reachModelInput:
			cell.verdict = verdictAdvisoryByRule
		case reachPriorTurn:
			cell.verdict = verdictDroppedPendingMechanism
		default:
			cell.verdict = verdictNotReproducible
		}
		return cell
	}

	cell.lost = kindsMinus(contributed, derived)
	cell.gained = kindsMinus(derived, contributed)

	switch {
	case authority.reach == reachPostResolution:
		// Reproducibility is decided by REACH before any set is compared:
		// even a cell whose frame-side lower bound happens to be subsumed
		// cannot speak for what resolution would have injected.
		cell.verdict = verdictNotReproducible
	case len(contributed) == 0:
		cell.verdict = verdictNotApplicable
	case len(cell.lost) == 0:
		cell.verdict = verdictSubsumed
	default:
		cell.verdict = verdictNotSubsumed
		cell.cause = classifyLoss(rows, cell.lost)
	}
	return cell
}

// parityCells computes the whole table: every frame crossed with every
// authority.
func parityCells(t *testing.T) []parityCell {
	t.Helper()
	capabilities := liveCapabilityList(t)
	seed := contextfabric.GenerateObligationSeed(capabilities)

	var cells []parityCell
	for _, testCase := range traceFrames() {
		for _, authority := range planningAuthorities() {
			cells = append(cells, computeParityCell(testCase.id, authority, testCase.frame, seed, capabilities))
		}
	}
	return cells
}

// ---------------------------------------------------------------------------
// THE ASSERTIONS
// ---------------------------------------------------------------------------

// TestEveryLossIsClassifiedByAClosedCause is the parity proof's ACCEPTANCE
// CLAIM, and it is the same shape as oracle O9's own condition: not "the
// derivation serves everything", but "where it does not, it SAYS SO, with a
// token from a closed vocabulary that names what would have to change".
//
// WHY THIS AND NOT "EVERY AUTHORITY IS SUBSUMED". That property was the one
// this file was written to assert, it was run first, and it is FALSE: 14 of
// the 42 pre-resolution cells lose at least one fact kind. The measurement
// stands and the assertion moved, rather than the harness being edited
// until the assertion passed -- a sweep whose disagreements are settled by
// changing its subject is not a sweep. What the losses turned out to be is
// the actual finding, and it is in the artifact and the PR body.
//
// A cell that is not_subsumed with NO cause would be the silent emptiness
// this whole slice exists to forbid, so an unclassified loss FAILS.
func TestEveryLossIsClassifiedByAClosedCause(t *testing.T) {
	validCause := map[lossCause]bool{}
	for _, cause := range lossCauses {
		validCause[cause] = true
	}

	checked, losses := 0, 0
	for _, cell := range parityCells(t) {
		checked++
		if cell.verdict != verdictNotSubsumed {
			// A cell that lost nothing must not carry a cause: a cause on
			// a subsumed cell would be an explanation for something that
			// did not happen.
			if cell.cause != "" {
				t.Errorf("frame %s / authority %s: verdict %q carries loss cause %q", cell.frameID, cell.authorityID, cell.verdict, cell.cause)
			}
			continue
		}
		losses++
		if !validCause[cell.cause] {
			t.Errorf("frame %s / authority %s: LOST [%s] with cause %q, which is not in the closed vocabulary -- a loss with no named cause is the silent emptiness this slice forbids",
				cell.frameID, cell.authorityID, renderKinds(cell.lost), cell.cause)
		}
	}

	// Reach counts, both of them. A green loop that reached no assertion
	// proves nothing (this package lost a property test to exactly that),
	// and a green loop that found no LOSS would mean the classifier was
	// never exercised at all.
	if checked == 0 {
		t.Fatal("no parity cell was checked -- the roster or the corpus is empty, and this test proved nothing")
	}
	if losses == 0 {
		t.Fatal("no not_subsumed cell was found, so the loss classifier never ran. Either every authority became subsumed -- which is a result worth its own commit message -- or the harness stopped computing losses.")
	}
	t.Logf("checked %d cells, classified %d losses", checked, losses)
}

// TestEverySupersedingCauseIsQuotedAgainstARule guards the one direction
// that can silently overclaim.
//
// `superior` is the verdict that authorizes a deletion, so the set of
// causes that grant it may not grow by accident: a cause added to
// lossCauses and casually added to supersedingCauses would retire an
// authority on no argument at all. This pins the superseding set by NAME
// and by COUNT, so widening it is a deliberate edit that fails this test
// first and has to be justified in the same commit.
func TestEverySupersedingCauseIsQuotedAgainstARule(t *testing.T) {
	want := map[lossCause]bool{
		// Rule: design 13.9 B5 + devhealthfacts/shared.go -- an
		// organization state requirement derives an explicit
		// `unavailable` rather than a confident wrong answer.
		causeUnavailableNamedInstead: true,
		// Rule: design 13.2.4 -- a widening is advisory and may not change
		// completeness. The lost kinds serve no obligation this frame
		// derives, so the authority plans a read the question never asked
		// for. (cohort_fact_requirements.go's "can only ever prune" line
		// corroborates this on the project-member cells and not on the
		// team-operand ones, so it is not the rule.)
		causeNotRequiredByAnyObligation: true,
	}
	if len(supersedingCauses) != len(want) {
		t.Fatalf("supersedingCauses has %d members, want %d -- a cause that rules the derivation SUPERIOR authorizes retiring an authority, so adding one is a deliberate act that must carry its quoted rule",
			len(supersedingCauses), len(want))
	}
	for cause := range want {
		if !supersedingCauses[cause] {
			t.Errorf("cause %q lost its superseding status", cause)
		}
	}
	for cause := range supersedingCauses {
		if !want[cause] {
			t.Errorf("cause %q became superseding without a quoted rule in this test", cause)
		}
	}
	// The conservative default must NOT be superseding, or every loss on
	// a frame with a computed obligation would silently authorize a
	// deletion.
	if supersedingCauses[causeComputedStepInputUnserved] {
		t.Error("the conservative default cause is superseding -- the classifier's fallback would authorize retiring an authority")
	}
}

// TestNonReproducibleAuthoritiesEachCarryADisclosedReason is the other half
// of 13.15.2's escape clause: an authority the proof DROPS must say so with
// a token from the closed vocabulary, never by being quietly absent from
// the table.
//
// A disclosure nothing verifies is indistinguishable from an omission, so
// the drop is ENFORCED here rather than described in the PR body.
func TestNonReproducibleAuthoritiesEachCarryADisclosedReason(t *testing.T) {
	expected := map[string]authorityVerdict{}
	for _, authority := range planningAuthorities() {
		switch authority.reach {
		case reachPostResolution:
			expected[authority.id] = verdictNotReproducible
		case reachModelInput:
			expected[authority.id] = verdictAdvisoryByRule
		case reachPriorTurn:
			expected[authority.id] = verdictDroppedPendingMechanism
		}
	}
	if len(expected) == 0 {
		t.Fatal("no authority is classified as non-reproducible -- the roster lost its reach classification and this test proved nothing")
	}

	seen := map[string]int{}
	for _, cell := range parityCells(t) {
		want, isDropped := expected[cell.authorityID]
		if !isDropped {
			continue
		}
		seen[cell.authorityID]++
		if cell.verdict != want {
			t.Errorf("frame %s / authority %s: verdict %q, want the disclosed drop %q",
				cell.frameID, cell.authorityID, cell.verdict, want)
		}
	}
	for id := range expected {
		if seen[id] == 0 {
			t.Errorf("authority %s is classified as a disclosed drop but appears in NO cell -- it is absent from the table rather than disclosed in it", id)
		}
	}
}

// TestEveryParityCellCarriesAVerdictFromTheClosedVocabulary is the totality
// check: no cell may carry an empty or out-of-vocabulary verdict.
//
// The zero value of authorityVerdict is the empty string, so a cell that
// fell through every branch of computeParityCell would be silently
// unclassified -- the "never silently empty" condition O9 asserts for
// requirement cells, applied to this table.
func TestEveryParityCellCarriesAVerdictFromTheClosedVocabulary(t *testing.T) {
	valid := map[authorityVerdict]bool{}
	for _, verdict := range authorityVerdicts {
		valid[verdict] = true
	}

	checked := 0
	for _, cell := range parityCells(t) {
		checked++
		if !valid[cell.verdict] {
			t.Errorf("frame %s / authority %s: verdict %q is not in the closed vocabulary", cell.frameID, cell.authorityID, cell.verdict)
		}
		// A not_subsumed cell whose lost set is empty would be a verdict
		// disagreeing with its own evidence.
		if cell.verdict == verdictNotSubsumed && len(cell.lost) == 0 {
			t.Errorf("frame %s / authority %s: verdict not_subsumed with an EMPTY lost set -- the verdict and its evidence disagree", cell.frameID, cell.authorityID)
		}
		if cell.verdict == verdictSubsumed && len(cell.lost) != 0 {
			t.Errorf("frame %s / authority %s: verdict subsumed while losing [%s]", cell.frameID, cell.authorityID, renderKinds(cell.lost))
		}
	}
	if checked != len(traceFrames())*len(planningAuthorities()) {
		t.Fatalf("checked %d cells, want %d (frames x authorities) -- the table is not the full cross product", checked, len(traceFrames())*len(planningAuthorities()))
	}
}

// TestCarriedPlanContributesFamilyNotFactKinds DEMONSTRATES authority 6's
// mechanism instead of asserting it in a comment.
//
// The carried plan is a planning authority not because it names fact kinds
// -- it names none -- but because it overlays the FAMILY, and the family is
// authority 3's input. So a carry changes the planned fact kinds
// INDIRECTLY, through authority 3. This test drives the production
// applyCarriedPlan and shows the family moving, which is what makes the
// "retired by the 13.4.1 carry rule" sentence in 13.8a a claim about a
// mechanism that has to exist.
func TestCarriedPlanContributesFamilyNotFactKinds(t *testing.T) {
	unclassified := contextfabric.QuestionFamilyOutcome{Family: contextfabric.QuestionFamilyUnclassified}

	carried, applied := contextfabric.ApplyCarriedPlanForTest(
		unclassified, contextfabric.QuestionFamilyGroupedCohortStatus, contextfabric.SubjectTeam)
	if !applied {
		t.Fatal("applyCarriedPlan did not apply to an unclassified outcome -- the carry precondition changed and this test no longer drives the authority")
	}
	if carried.Family != contextfabric.QuestionFamilyGroupedCohortStatus {
		t.Fatalf("carried family = %q, want the carried-in family -- authority 6 did not overlay the family", carried.Family)
	}

	// The family it carried in is the one authority 3 then reads, so the
	// carry's fact-kind effect is authority 3's output under a DIFFERENT
	// family. Shown by computing authority 3 for both families.
	own, ownFound := contextfabric.LookupQuestionFamily(contextfabric.QuestionFamilyUnclassified)
	carriedDefinition, carriedFound := contextfabric.LookupQuestionFamily(carried.Family)
	if !ownFound || !carriedFound {
		t.Fatal("a family in the closed vocabulary is missing from the registry")
	}
	ownKinds := sortedUniqueKinds(contextfabric.PlanFactKindsForTest(own, contextfabric.InterpretedQuestion{}))
	carriedKinds := sortedUniqueKinds(contextfabric.PlanFactKindsForTest(carriedDefinition, contextfabric.InterpretedQuestion{}))
	t.Logf("authority 6 moves authority 3's output: unclassified -> [%s]; carried %q -> [%s]",
		renderKinds(ownKinds), carried.Family, renderKinds(carriedKinds))
}

// TestMergeSinkArityPinsTheAuthorityRoster is what stops this file from
// being blind to a SEVENTH authority.
//
// Every authority that reaches the fact read does so through ONE sink --
// the mergeFactRequirements call in engine.go -- so the number of argument
// groups that call takes is the number of merge-time authorities. A check
// that quantifies over the roster cannot notice something missing from the
// roster; this one reads the SOURCE instead.
//
// It parses engine.go with go/parser and matches the CALL EXPRESSION, not a
// substring: this package's history carries three static-guard defects
// caused by grepping a whole file (a guard that matched an example command
// inside a comment, a parser that pulled a flag out of prose, a whole-file
// match that missed a continuation line). An AST match cannot see a comment.
func TestMergeSinkArityPinsTheAuthorityRoster(t *testing.T) {
	const enginePath = "engine.go"

	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, enginePath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", enginePath, err)
	}

	var callSites []*ast.CallExpr
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "mergeFactRequirements" {
			callSites = append(callSites, call)
		}
		return true
	})

	// The positive control: a guard that found nothing to check would pass
	// silently, and "zero call sites" reads exactly like "arity unchanged".
	if len(callSites) != 1 {
		t.Fatalf("found %d mergeFactRequirements call sites in %s, want exactly 1 -- the single sink this roster is pinned against has moved or multiplied", len(callSites), enginePath)
	}

	arity := len(callSites[0].Args)
	if arity != mergeSinkArgumentCount() {
		t.Fatalf("the fact-requirement sink at %s takes %d argument groups, the roster is pinned at %d.\n"+
			"A group was added or removed, which means a PLANNING AUTHORITY was added or removed.\n"+
			"Update planningAuthorities() and this pin together, and say in the commit which authority moved.",
			fileSet.Position(callSites[0].Pos()), arity, mergeSinkArgumentCount())
	}
}

// TestEverySinkAuthorityDeclaresItsArgumentGroup closes the hole the
// mutation battery found in the arity pin.
//
// The battery mutated authority 1's `sinkArgument` from 1 to 0 and the
// whole suite stayed GREEN: group 1 still had authority 2 in it, so the
// derived arity was unchanged, and the artifact does not print the field.
// A surviving mutation is a finding, not a pass -- the property it deleted
// was pinned by nothing.
//
// The invariant is a RELATION BETWEEN TWO ROSTER FIELDS, not a second table
// of the answers. Each field exists for its own reason (`reach` decides the
// verdict class; `sinkArgument` records how the kinds reach the read), so
// neither is free to be edited into agreement with the other:
//
//	sinkArgument == 0  IFF  reach == reachPriorTurn
//
// Every authority reaches the fact read through the merge sink except the
// carried plan, which acts UPSTREAM on the family -- and that is exactly
// why it contributes no fact kinds of its own. If a future authority ever
// acts upstream WITHOUT being a prior-turn source, this test is where that
// shows up, and it should be widened deliberately rather than relaxed.
func TestEverySinkAuthorityDeclaresItsArgumentGroup(t *testing.T) {
	checked := 0
	groups := map[int]bool{}
	for _, authority := range planningAuthorities() {
		checked++
		upstream := authority.reach == reachPriorTurn
		switch {
		case upstream && authority.sinkArgument != 0:
			t.Errorf("authority %s acts upstream of the sink (reach %q) but declares sink group %d", authority.id, authority.reach, authority.sinkArgument)
		case !upstream && authority.sinkArgument == 0:
			t.Errorf("authority %s reaches the fact read (reach %q) but declares NO sink argument group -- the arity pin cannot see it, and the fact kinds it contributes have no stated route into the read",
				authority.id, authority.reach)
		}
		if authority.sinkArgument != 0 {
			groups[authority.sinkArgument] = true
		}
	}
	if checked == 0 {
		t.Fatal("the roster is empty -- this test proved nothing")
	}

	// The groups must be exactly 1..N with no gaps: they are ARGUMENT
	// POSITIONS of a real call, so a typo'd 7 is not a new authority, it
	// is a wrong position that would inflate the arity pin.
	for position := 1; position <= len(groups); position++ {
		if !groups[position] {
			t.Errorf("sink argument groups are not contiguous from 1: group %d is empty while %d groups are declared -- a group number is an argument POSITION, not a label", position, len(groups))
		}
	}
}

// ---------------------------------------------------------------------------
// THE BRANCHES THE CORPUS CANNOT REACH
//
// Round 1 found a SURVIVING MUTATION: `classifyLoss`'s zero-rows branch
// could be changed from the conservative cause to a SUPERSEDING one -- which
// would rule a total loss "superior" and authorize retiring an authority --
// and the whole package suite stayed green, because no frame in the corpus
// derives zero rows. The branch was reasoned about in a comment and pinned by
// nothing. A tier with no fixture that lands in it is indistinguishable from
// an enforcement check that always answers no.
//
// The fix is the CLASS, not the instance. Every branch of this harness's
// classification and contribution logic was enumerated against the corpus,
// and the ones the fourteen frames cannot reach are driven directly here with
// constructed inputs:
//
//	site                                     branch                              corpus reach
//	classifyLoss                             len(rows) == 0                      NEVER  <- the finding
//	classifyLoss                             served == 0                         B5
//	classifyLoss                             lost kind is an UNSERVED input      C4
//	classifyLoss                             lost kind is an input, but SERVED   NEVER  <- new, see below
//	classifyLoss                             fallthrough (not an input at all)   reached
//	contributeStatusComposition              no coordinates                      NEVER
//	contributeFamilyDefinition               family not found                    UNREACHABLE (proven below)
//	contributeCohortRankingInjection         family not found                    UNREACHABLE (proven below)
//	contributeCohortRankingInjection         non-cohort axis                     reached
//	computeParityCell                        every verdict branch                reached
//
// "UNREACHABLE" is a stronger claim than "never reached", so it carries its
// own proof rather than a fixture: the family lookup is TOTAL over the closed
// vocabulary, which makes those two branches provably dead code instead of
// untested code. Those are different findings and are not collapsed.
//
// THE NEW CORPUS-UNREACHABLE BRANCH, recorded rather than left implicit. The
// §13.2.3 amendment split the old "a computation is present" branch in two:
// a lost kind that is a declared input AND unserved (blocking), and one that
// is a declared input but ALREADY SERVED by a read row (not blocking -- the
// plan reads it anyway, so retiring the authority takes nothing). No corpus
// frame reaches the second: a kind served by a read row is in `derived`, so
// it is never in `lost` to begin with. It is driven directly below, because a
// branch whose only argument for correctness is "the caller cannot reach it"
// is exactly the shape round 1's surviving mutation lived in.
// ---------------------------------------------------------------------------

// parityTestRow builds one derived-requirement row with just the fields
// classifyLoss reads, so a branch can be driven without a frame that
// produces it.
//
// inputs are the computed step's DECLARED inputs and reads are what the row
// serves; both are needed since the amendment, because the classifier now
// compares the two rather than merely counting computations.
func parityTestRow(
	step contextfabric.ComputedObligationStep,
	unavailable contextfabric.RequirementUnavailableReason,
	inputs []contextfabric.FactKind,
	reads []contextfabric.FactKind,
) contextfabric.DerivedRequirement {
	return parityTestRowExec(step, unavailable, inputs, reads, contextfabric.ComputedStepServerExecuted)
}

// parityTestRowExec is parityTestRow with the step's EXECUTION stated. The
// plain form defaults to server-executed so the older cases keep asserting
// what they were written to assert; a case about the unwired branch says so.
func parityTestRowExec(
	step contextfabric.ComputedObligationStep,
	unavailable contextfabric.RequirementUnavailableReason,
	inputs []contextfabric.FactKind,
	reads []contextfabric.FactKind,
	execution contextfabric.ComputedStepExecution,
) contextfabric.DerivedRequirement {
	if step == "" {
		// A read row has no step and therefore no execution; stamping one
		// would make the fixture assert a shape the derivation never builds.
		execution = ""
	}
	return contextfabric.DerivedRequirement{
		Step:           step,
		Unavailable:    unavailable,
		InputFactKinds: inputs,
		FactKinds:      reads,
		StepExecution:  execution,
	}
}

// TestClassifyLossLandsInEveryBranch drives every branch of the classifier,
// including the ones the corpus cannot produce.
//
// The zero-rows case asserts the PROPERTY the surviving mutation broke, not
// merely the current constant: whatever that branch returns, it must not be
// a cause that authorizes a retirement. Asserting only the constant would
// pin the letter and miss a future cause that is renamed into the
// superseding set.
func TestClassifyLossLandsInEveryBranch(t *testing.T) {
	health := contextfabric.FactHealth
	deficiencies := contextfabric.FactOperationalDeficiencies

	cases := []struct {
		name string
		rows []contextfabric.DerivedRequirement
		lost []contextfabric.FactKind
		want lossCause
	}{
		{
			// THE BRANCH ROUND 1's MUTATION SURVIVED IN. No corpus frame
			// derives zero rows; oracle O9 fails a frame that does. A
			// future frame that derived nothing must not read as superior.
			name: "no rows at all (unreachable from the corpus)",
			rows: nil,
			lost: []contextfabric.FactKind{health},
			want: causeComputedStepInputUnserved,
		},
		{
			name: "every row unavailable",
			rows: []contextfabric.DerivedRequirement{parityTestRow("", "no_declaring_producer", nil, nil)},
			lost: []contextfabric.FactKind{health},
			want: causeUnavailableNamedInstead,
		},
		{
			name: "served reads, no computed obligation",
			rows: []contextfabric.DerivedRequirement{parityTestRow("", "", nil, []contextfabric.FactKind{health})},
			lost: []contextfabric.FactKind{deficiencies},
			want: causeNotRequiredByAnyObligation,
		},
		{
			// THE BLOCKING CASE. The computation declares the lost kind as
			// an input and no read row serves it, so retiring the authority
			// would remove the only thing planning to read it.
			name: "lost kind is an UNSERVED declared input",
			rows: []contextfabric.DerivedRequirement{
				parityTestRow("", "", nil, []contextfabric.FactKind{health}),
				parityTestRow("rank_cohort", "", []contextfabric.FactKind{deficiencies}, nil),
			},
			lost: []contextfabric.FactKind{deficiencies},
			want: causeComputedStepInputUnserved,
		},
		{
			// CORPUS-UNREACHABLE: a kind a read row serves is in `derived`,
			// so it never appears in `lost`. Driven anyway, because the
			// branch decides a retirement.
			name: "lost kind is a declared input that a read row already serves",
			rows: []contextfabric.DerivedRequirement{
				parityTestRow("", "", nil, []contextfabric.FactKind{deficiencies}),
				parityTestRow("rank_cohort", "", []contextfabric.FactKind{deficiencies}, nil),
			},
			lost: []contextfabric.FactKind{deficiencies},
			want: causeNotRequiredByAnyObligation,
		},
		{
			// A computation whose step declares NO fact input cannot make
			// any loss blocking. This is the branch that clears the counting
			// frames, and it is the amendment's whole retirement effect.
			// A step that consumes no fact AND IS EXECUTED clears the loss:
			// the server computes the answer from the member set, so the lost
			// reads served no purpose the frame states.
			name: "an EXECUTED computation that declares no fact input",
			rows: []contextfabric.DerivedRequirement{
				parityTestRow("", "", nil, []contextfabric.FactKind{health}),
				parityTestRow("membership_cardinality", "", nil, nil),
			},
			lost: []contextfabric.FactKind{deficiencies},
			want: causeNotRequiredByAnyObligation,
		},
		{
			// THE SAME STEP, DECLARED ONLY. Identical from the input side --
			// which is the whole reason execution cannot be inferred from the
			// inputs -- and it must NOT clear: nothing runs the step, so the
			// value reaches the answer by narration over the read facts, and
			// removing those reads can change it.
			name: "a DECLARED-ONLY computation that declares no fact input",
			rows: []contextfabric.DerivedRequirement{
				parityTestRow("", "", nil, []contextfabric.FactKind{health}),
				parityTestRowExec("membership_cardinality", "", nil, nil, contextfabric.ComputedStepDeclaredOnly),
			},
			lost: []contextfabric.FactKind{deficiencies},
			want: causeComputedStepNotWired,
		},
		{
			// Both defects on one frame: the more specific cause wins, so the
			// report names the one with a named fix.
			name: "an unserved input beats an unwired step",
			rows: []contextfabric.DerivedRequirement{
				parityTestRow("", "", nil, []contextfabric.FactKind{health}),
				parityTestRow("rank_cohort", "", []contextfabric.FactKind{deficiencies}, nil),
				parityTestRowExec("membership_cardinality", "", nil, nil, contextfabric.ComputedStepDeclaredOnly),
			},
			lost: []contextfabric.FactKind{deficiencies},
			want: causeComputedStepInputUnserved,
		},
		{
			// Several lost kinds, ONE of them an unserved input. The
			// retirement is a single act, so one is enough to block.
			name: "mixed loss where only one kind is an unserved input",
			rows: []contextfabric.DerivedRequirement{
				parityTestRow("", "no_declaring_producer", nil, nil),
				parityTestRow("", "", nil, []contextfabric.FactKind{health}),
				parityTestRow("rank_cohort", "", []contextfabric.FactKind{deficiencies}, nil),
			},
			lost: []contextfabric.FactKind{health, deficiencies},
			want: causeComputedStepInputUnserved,
		},
	}

	reached := 0
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifyLoss(testCase.rows, testCase.lost)
			if got != testCase.want {
				t.Fatalf("classifyLoss = %q, want %q", got, testCase.want)
			}
		})
		reached++
	}
	if reached != len(cases) {
		t.Fatalf("ran %d of %d branch cases", reached, len(cases))
	}

	// The property, stated separately from the constant: the branch that
	// handles a shape nobody has seen must never authorize a retirement.
	if supersedingCauses[classifyLoss(nil, []contextfabric.FactKind{contextfabric.FactHealth})] {
		t.Fatal("the zero-rows branch returns a SUPERSEDING cause -- a frame that derives nothing would rule every authority's whole contribution 'superior' and authorize retiring it")
	}
}

// TestContributionOfAFrameWithNoCoordinatesIsEmpty lands in the other
// corpus-unreachable branch: an authority asked about a frame that derives
// no requirement coordinates at all.
//
// A named subject with no expected kind is exactly that shape -- the frame
// layer emits no role slot, "which is a weaker claim than guessing one" --
// and no corpus frame has it, because every named frame in the corpus
// declares its kind.
func TestContributionOfAFrameWithNoCoordinatesIsEmpty(t *testing.T) {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{"s"}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)

	// The premise of the test, asserted rather than assumed: if this frame
	// ever starts deriving coordinates, the branch below is no longer the
	// one being exercised and the test has gone vacuous.
	if coordinates := contextfabric.DeriveRequirementCoordinates(frame); len(coordinates) != 0 {
		t.Fatalf("premise broken: this frame now derives %d coordinates, so it no longer reaches the no-coordinates branch", len(coordinates))
	}
	if refs := frameSubjectRefs(frame); len(refs) != 0 {
		t.Fatalf("frameSubjectRefs = %d refs for a frame with no coordinates", len(refs))
	}

	kinds, comparable := contributeStatusComposition(frame)
	if !comparable {
		t.Fatal("the status composition reported itself incomparable for a frame with no subjects; it has no per-frame value only when it is a model input")
	}
	if len(kinds) != 0 {
		t.Fatalf("the status composition contributed %v for a frame with no subjects -- it expanded a requirement for subjects that do not exist", kinds)
	}
}

// TestEveryFamilyInTheVocabularyIsInTheRegistry proves the two `family not
// found` branches are DEAD CODE rather than untested code.
//
// Both contribution functions handle a lookup miss defensively. If the
// lookup is total over the closed vocabulary, those branches can never run,
// and that is a different statement from "the corpus does not reach them" --
// dead code is a disclosure question, untested code is a coverage one. This
// test settles which they are, and turns into a real failure the day a
// vocabulary member is added without a registry row.
func TestEveryFamilyInTheVocabularyIsInTheRegistry(t *testing.T) {
	checked := 0
	for _, family := range contractsv1.ContextFabricQuestionFamilyVocabulary() {
		checked++
		if _, found := contextfabric.LookupQuestionFamily(family); !found {
			t.Errorf("family %q is in the closed vocabulary but has no registry row -- the defensive lookup branches in the contribution functions are now REACHABLE and need fixtures", family)
		}
	}
	if checked == 0 {
		t.Fatal("the family vocabulary is empty -- this test proved nothing")
	}
	t.Logf("family lookup is total over %d vocabulary members; the two `not found` branches are dead code", checked)
}

// ---------------------------------------------------------------------------
// THE ARTIFACT
// ---------------------------------------------------------------------------

// renderParityArtifact renders the whole proof. Every number in it is
// COMPUTED here from the production outputs; none is a constant this file
// carries, which is what makes the artifact a measurement rather than a
// second copy of an expectation.
func renderParityArtifact(cells []parityCell) string {
	var out strings.Builder
	out.WriteString("# GENERATED by TestPlanningAuthorityParityArtifactIsRegenerated. DO NOT EDIT BY\n")
	out.WriteString("# HAND: the test regenerates this file and fails on any difference.\n")
	out.WriteString("#\n")
	out.WriteString("# The SIX-AUTHORITY PARITY PROOF (design 13.8a, 13.15.2's N2 row). For each\n")
	out.WriteString("# frame and each planning authority: what the authority contributes to the\n")
	out.WriteString("# plan, what the derived requirement rows name, and what retiring the\n")
	out.WriteString("# authority would LOSE. Question IDs only -- no corpus text.\n")
	out.WriteString("#\n")
	out.WriteString("# `lost` = contributed \\ derived. It is the only column that decides a\n")
	out.WriteString("# retirement: empty means the derived rows already name everything the\n")
	out.WriteString("# authority supplies. `gained` = derived \\ contributed, the plan.fact_kinds\n")
	out.WriteString("# GROWTH design 13.9 B9 predicts -- reported, never asserted away.\n")
	out.WriteString("#\n")
	out.WriteString("# Source 5 is SPLIT into 5a (the declared per-cohort-kind table, which a\n")
	out.WriteString("# frame determines) and 5b (edge-derived, which no frame determines).\n")
	out.WriteString("# Folding them together would report the reproducible half as unreproducible.\n\n")

	out.WriteString("## ROSTER\n\n")
	for _, authority := range planningAuthorities() {
		out.WriteString(fmt.Sprintf("  %-3s %-15s %-46s %s\n", authority.id, authority.reach, authority.site, authority.name))
	}

	out.WriteString("\n## PARITY TABLE\n\n")
	out.WriteString(fmt.Sprintf("  %-5s %-3s %-32s %-38s %s\n", "frame", "id", "verdict", "cause", "lost / gained"))
	for _, cell := range cells {
		cause := string(cell.cause)
		if cause == "" {
			cause = "-"
		} else if cell.superior() {
			cause += " (SUPERIOR)"
		}
		out.WriteString(fmt.Sprintf("  %-5s %-3s %-32s %-38s lost=[%s] gained=[%s]\n",
			cell.frameID, cell.authorityID, cell.verdict, cause, renderKinds(cell.lost), renderKinds(cell.gained)))
	}

	out.WriteString("\n## PER-AUTHORITY VERDICT HISTOGRAM (every token, including the zeroes)\n\n")
	for _, authority := range planningAuthorities() {
		counts := map[authorityVerdict]int{}
		for _, cell := range cells {
			if cell.authorityID == authority.id {
				counts[cell.verdict]++
			}
		}
		parts := make([]string, 0, len(authorityVerdicts))
		for _, verdict := range authorityVerdicts {
			parts = append(parts, fmt.Sprintf("%s=%d", verdict, counts[verdict]))
		}
		out.WriteString(fmt.Sprintf("  %-3s %s\n", authority.id, strings.Join(parts, "  ")))
	}

	out.WriteString("\n## LOSS CAUSE HISTOGRAM (every token, including the zeroes)\n\n")
	for _, cause := range lossCauses {
		count := 0
		for _, cell := range cells {
			if cell.cause == cause {
				count++
			}
		}
		ruling := "blocks retirement"
		if supersedingCauses[cause] {
			ruling = "derivation ruled SUPERIOR"
		}
		out.WriteString(fmt.Sprintf("  %-40s %3d  %s\n", cause, count, ruling))
	}

	out.WriteString("\n## RETIREMENT VERDICT, PER AUTHORITY\n")
	out.WriteString("#\n")
	out.WriteString("# `retirable_on_this_evidence` requires: every cell is subsumed, not_applicable,\n")
	out.WriteString("# or a loss under a SUPERSEDING cause. One blocking loss anywhere is enough to\n")
	out.WriteString("# withhold it. This verdict does NOT discharge design 13.9's B7/B9 gates, which\n")
	out.WriteString("# are a labelled-set before/after programme on the rig, not a table test.\n")
	out.WriteString("#\n")
	out.WriteString("# `RETIRABLE on this evidence` IS NOT A RETIREMENT, AND NOTHING HERE RETIRES\n")
	out.WriteString("# ANYTHING. It says one thing only: on the corpus above, this authority's\n")
	out.WriteString("# contribution is either reproduced by the derived rows or lost under a cause\n")
	out.WriteString("# ruled superior. Removing the authority is a SEPARATE change, gated on the\n")
	out.WriteString("# B7/B9 rig programme named above, and it carries its own before/after\n")
	out.WriteString("# measurement on real answers -- which this table, computed from frames\n")
	out.WriteString("# alone, cannot stand in for.\n\n")
	for _, authority := range planningAuthorities() {
		blocking, superior, cells4 := 0, 0, 0
		nonReproducible := false
		for _, cell := range cells {
			if cell.authorityID != authority.id {
				continue
			}
			cells4++
			switch {
			case cell.verdict == verdictSubsumed || cell.verdict == verdictNotApplicable:
			case cell.superior():
				superior++
			case cell.verdict == verdictNotSubsumed:
				blocking++
			default:
				nonReproducible = true
			}
		}
		verdict := "RETIRABLE on this evidence"
		switch {
		case nonReproducible:
			verdict = "NOT RETIRABLE -- disclosed drop, see the roster's reach column"
		case blocking > 0:
			verdict = fmt.Sprintf("NOT RETIRABLE -- %d blocking loss cell(s)", blocking)
		}
		out.WriteString(fmt.Sprintf("  %-3s %-58s (%d superior, %d blocking, %d cells)\n", authority.id, verdict, superior, blocking, cells4))
	}

	subsumed, notSubsumed, notApplicable, dropped, superiorTotal := 0, 0, 0, 0, 0
	for _, cell := range cells {
		switch cell.verdict {
		case verdictSubsumed:
			subsumed++
		case verdictNotSubsumed:
			notSubsumed++
			if cell.superior() {
				superiorTotal++
			}
		case verdictNotApplicable:
			notApplicable++
		default:
			dropped++
		}
	}
	out.WriteString("\n# MEASUREMENT\n")
	out.WriteString(fmt.Sprintf("#   %d cells = %d frames x %d authorities\n", len(cells), len(traceFrames()), len(planningAuthorities())))
	out.WriteString(fmt.Sprintf("#   subsumed %d / not_subsumed %d / not_applicable %d / disclosed drops %d\n", subsumed, notSubsumed, notApplicable, dropped))
	out.WriteString(fmt.Sprintf("#   of the %d not_subsumed cells, %d are ruled SUPERIOR and %d BLOCK retirement\n", notSubsumed, superiorTotal, notSubsumed-superiorTotal))

	// THE REFUTED PROPERTY, reported rather than asserted.
	//
	// "Every authority whose inputs exist on the validated frame is
	// subsumed by the derived rows" is the property this proof was written
	// to assert. It was written first, run, and it FAILED. It is recorded
	// here as a standing number rather than shipped as a permanently red
	// gate, so that a future slice can watch it fall to zero -- which is
	// exactly what closing the blocking cause would do.
	preResolutionCells, preResolutionLosses := 0, 0
	reachByID := map[string]authorityReach{}
	for _, authority := range planningAuthorities() {
		reachByID[authority.id] = authority.reach
	}
	for _, cell := range cells {
		if reachByID[cell.authorityID] != reachPreResolution {
			continue
		}
		preResolutionCells++
		if cell.verdict == verdictNotSubsumed {
			preResolutionLosses++
		}
	}
	out.WriteString("#\n")
	out.WriteString("# REFUTED PROPERTY (reported, not asserted -- see the file header)\n")
	out.WriteString("#   \"every pre-resolution authority is subsumed by the derived rows\"\n")
	out.WriteString(fmt.Sprintf("#   FAILS on %d of %d pre-resolution cells. This proof was written to assert it;\n", preResolutionLosses, preResolutionCells))
	out.WriteString("#   the measurement stands and the assertion moved to oracle O9's shape. Closing\n")
	out.WriteString("#   the blocking cause drives this number toward the superior-only remainder.\n")
	return out.String()
}

// TestPlanningAuthorityParityArtifactIsRegenerated regenerates the artifact
// and diffs it, the same discipline requirement_trace.txt uses: there is no
// hand-written copy of the table for an edit to be coordinated across.
func TestPlanningAuthorityParityArtifactIsRegenerated(t *testing.T) {
	rendered := renderParityArtifact(parityCells(t))

	existing, err := os.ReadFile(planningAuthorityParityArtifact)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", planningAuthorityParityArtifact, err)
		}
		if writeErr := os.WriteFile(planningAuthorityParityArtifact, []byte(rendered), 0o644); writeErr != nil {
			t.Fatalf("write %s: %v", planningAuthorityParityArtifact, writeErr)
		}
		t.Fatalf("%s did not exist and has been generated -- re-run to confirm it is stable, and commit it", planningAuthorityParityArtifact)
	}

	if string(existing) == rendered {
		return
	}
	if writeErr := os.WriteFile(planningAuthorityParityArtifact, []byte(rendered), 0o644); writeErr != nil {
		t.Fatalf("write %s: %v", planningAuthorityParityArtifact, writeErr)
	}
	t.Fatalf("%s is stale and has been regenerated. Read the diff: a change here means an\n"+
		"authority's contribution or the derivation moved. If that was deliberate, commit the\n"+
		"new artifact and say in the commit WHICH authority moved and why.", planningAuthorityParityArtifact)
}
