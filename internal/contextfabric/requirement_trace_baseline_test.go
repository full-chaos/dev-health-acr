package contextfabric_test

// O9 -- the S7b-ii ENTRY GATE, behavioural half. Design §13.11a (O9),
// §13.15.1 (the executed trace that forced the excision), §13.15.2 (the
// per-rule evidence obligations).
//
// WHAT THIS FILE IS. Law L10 forbids writing any derivation rule that reads
// the fact registry into the design before it has been EXECUTED against the
// real registry on the four acceptance questions and the composed cases,
// RED first. This file is that execution. It transcribes the FROZEN
// §13.4.2a rules -- the SubjectRole table, the FactKinds seed, and the
// intersection rule -- and runs them against devhealthfacts.NewProviders,
// the live registry, at THIS tip.
//
// IT LIVES IN THE EXTERNAL TEST PACKAGE ON PURPOSE. devhealthfacts imports
// contextfabric, so an in-package test file cannot import it back. Running
// the frozen rules against a hand-built fixture registry instead would test
// the fixture, not the registry the engine actually plans against -- which
// is the whole point of the gate.
//
// TWO ASSERTIONS, AND THEY MEAN DIFFERENT THINGS.
//
//  1. TestFrozenRuleTranscriptionReproducesTheRecordedTrace asserts this
//     transcription reproduces §13.15.1's OWN recorded per-cell output,
//     cell by cell. It must PASS. It is the salted-positive check: an
//     emptiness count from a transcription nobody verified is worthless,
//     because a transcription error produces empty cells for a reason that
//     has nothing to do with the rule under test. This test is what makes
//     the next one's redness evidence rather than noise.
//
//  2. TestO9EveryRequiredReadObligationIsServed is the GATE. It asserts no
//     required READ obligation derives an empty fact-kind set. It is RED at
//     this tip BY CONSTRUCTION and stays red until the generated mapping
//     fills every cell a producer can serve and names `unavailable` for
//     every cell none can.
//
// CORPUS SAFETY. Frames are identified by QUESTION ID only. No corpus
// question text appears in this file, in its output, or in any artifact
// derived from it -- the ids and the structural labels are the whole
// vocabulary. The frames themselves are built THROUGH the shipped frame
// layer (DeriveFrameObligations), never by hand-typing an obligation list,
// so a change to §13.2.3's tables moves this trace with it instead of
// silently disagreeing with it.

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// ---------------------------------------------------------------------------
// The frozen §13.4.2a rules, transcribed
// ---------------------------------------------------------------------------

// frozenRequirementRule is one row of the frozen per-obligation table: the
// FactKinds SEED, the required FactTable.Shape, and whether that shape
// requirement was the "breakdown or scalar" clause (shapeOptional) or a
// hard table demand.
//
// The seed values are recovered from §13.15.2's own description of the
// frozen table ("state->status, count->landscape, ranking->ranking-shaped
// table, ...") plus §13.15.1's recorded per-cell outputs, and the recovery
// is VERIFIED rather than asserted: test 1 above re-derives every recorded
// cell from these rows. A seed that were wrong would fail that test loudly
// instead of quietly changing the empty count.
type frozenRequirementRule struct {
	// seed is the fact-kind set the frozen table named for this
	// obligation. A nil seed with shape != "" means "every kind that
	// declares a table of this shape" -- the form the frozen table used
	// for `ranking`, which named a shape and no kinds.
	seed []contextfabric.FactKind
	// shape is the FactTable.Shape the frozen rule demanded, or "" when it
	// demanded none (a scalar read satisfies it).
	shape contextfabric.FactTableShape
	// shapeOptional encodes the frozen "breakdown or scalar" clause: the
	// declared table MAY be absent, in which case the capability answers
	// with a scalar and still serves. §13.15.1 evaluated this clause both
	// literally (a table is required) and charitably (absence is a
	// scalar); every empty cell it found is a SupportedSubjectKinds or
	// Dimension miss, not a shape ambiguity, and this transcription
	// reproduces that.
	shapeOptional bool
	// quantifier is the frozen CompletionQuantifier for this obligation.
	quantifier string
}

// The five driver kinds. The frozen seed for principal_drivers,
// trend_series and period_delta is this set: they are the kinds that
// describe how a subject is DOING as opposed to what it IS.
var frozenDriverKinds = []contextfabric.FactKind{
	contextfabric.FactFlow,
	contextfabric.FactHealth,
	contextfabric.FactInvestment,
	contextfabric.FactReadiness,
	contextfabric.FactWorkload,
}

var frozenRequirementRules = map[contextfabric.AnswerObligation]frozenRequirementRule{
	// state -> status. This single row is the origin of thirteen of the
	// twenty-two empty cells: StatusProvider reads work_items.status and
	// declares SupportedSubjectKinds = [work_item] only (devhealthfacts/
	// workitems.go), so a team, project, repository or organization
	// subject derives nothing at all.
	contextfabric.ObligationState: {
		seed:          []contextfabric.FactKind{contextfabric.FactStatus},
		shape:         contextfabric.FactTableBreakdown,
		shapeOptional: true,
		quantifier:    "corroborated",
	},
	contextfabric.ObligationHealth: {
		seed:          []contextfabric.FactKind{contextfabric.FactHealth},
		shape:         contextfabric.FactTableBreakdown,
		shapeOptional: true,
		quantifier:    "all",
	},
	contextfabric.ObligationPrincipalDrivers: {
		seed:       frozenDriverKinds,
		quantifier: "at_least_one",
	},
	contextfabric.ObligationTrendSeries: {
		seed:       frozenDriverKinds,
		shape:      contextfabric.FactTableTimeSeries,
		quantifier: "all",
	},
	contextfabric.ObligationPeriodDelta: {
		seed:       frozenDriverKinds,
		quantifier: "all",
	},
	contextfabric.ObligationAllocationBreakdown: {
		seed:       []contextfabric.FactKind{contextfabric.FactInvestment},
		shape:      contextfabric.FactTableBreakdown,
		quantifier: "all",
	},
	// count -> landscape. LandscapeProvider serves team and project
	// (landscape.go), so a count anchored on a repository or taken over
	// the organization derives nothing.
	contextfabric.ObligationCount: {
		seed:       []contextfabric.FactKind{contextfabric.FactLandscape},
		quantifier: "exact",
	},
	// ranking named a SHAPE and no kinds: "a ranking-shaped table". No
	// producer declares FactTableRanking, so this row is empty for every
	// subject kind -- and it is empty for a deeper reason than a missing
	// producer. §13.2.3 classifies `ranking` as a COMPUTED obligation
	// (RankCohort over already-read facts); modelling it as a read with a
	// required table shape is the mis-typing round 4 recorded as N3.
	contextfabric.ObligationRanking: {
		shape:      contextfabric.FactTableRanking,
		quantifier: "all",
	},
}

// ---------------------------------------------------------------------------
// The 13 frames
// ---------------------------------------------------------------------------

// traceCell is one expected requirement cell from §13.15.1, transcribed
// from the recorded trace output.
type traceCell struct {
	obligation contextfabric.AnswerObligation
	// role and subject are the frozen SubjectRole table's assignment.
	role    string
	subject contextfabric.SubjectKind
	// wantLiteral and wantCharitable are the recorded fact-kind sets, in
	// the order §13.15.1 printed them (alphabetical). A nil slice is a
	// recorded EMPTY cell.
	wantLiteral    []contextfabric.FactKind
	wantCharitable []contextfabric.FactKind
}

// traceCase is one frame of the executed trace.
type traceCase struct {
	// id is the question ID. NEVER the question text -- see the corpus
	// safety note at the top of this file.
	id string
	// shape is a STRUCTURAL label (topology + operation), not a
	// paraphrase: it names the frame, not the question.
	shape string
	frame contextfabric.QuestionFrame
	cells []traceCell
}

func namedFrame(kind contextfabric.SubjectKind) contextfabric.SubjectExpression {
	expected := kind
	return contextfabric.SubjectExpression{
		Kind:  contextfabric.SubjectExpressionNamed,
		Named: &contextfabric.NamedSubjectExpression{Terms: []string{"s"}, ExpectedKind: &expected},
	}
}

func discoveredFrame(member contextfabric.SubjectKind) contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: member},
	}
}

func scopedFrame(member contextfabric.SubjectKind) contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind:   contextfabric.SubjectExpressionChildrenOfScope,
		Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: member},
	}
}

func groupedFrame(group, member contextfabric.SubjectKind) contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind:    contextfabric.SubjectExpressionGroupedMembers,
		Grouped: &contextfabric.GroupedSetExpression{GroupKind: group, MemberKind: member},
	}
}

func explicitFrame(kind contextfabric.SubjectKind) contextfabric.SubjectExpression {
	expected := kind
	operand := func(term string) contextfabric.SubjectOperand {
		return contextfabric.SubjectOperand{
			Kind:  contextfabric.SubjectOperandNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{term}, ExpectedKind: &expected},
		}
	}
	return contextfabric.SubjectExpression{
		Kind:     contextfabric.SubjectExpressionExplicitSet,
		Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{operand("a"), operand("b")}},
	}
}

func orgFrame(member *contextfabric.SubjectKind) contextfabric.SubjectExpression {
	return contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionOrganizationScope,
		Org:  &contextfabric.OrganizationScopeExpression{MemberKind: member},
	}
}

// buildFrame runs the SHIPPED derivation. Obligations are never hand-typed
// here: DeriveFrameObligations is the same function the engine calls, so if
// §13.2.3's tables move, this trace moves with them and the mismatch
// surfaces as a failing expectation instead of a silently stale fixture.
func buildFrame(goals []contextfabric.InvestigationGoal, expression contextfabric.SubjectExpression, temporal contextfabric.TemporalIntent, emphasis []contextfabric.AnswerEmphasis, dimensions []contextfabric.HealthDimension) contextfabric.QuestionFrame {
	return contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals:             goals,
		SubjectExpression: expression,
		Temporal:          temporal,
		Emphasis:          emphasis,
		Dimensions:        dimensions,
		Version:           contextfabric.QuestionFrameVersion,
	}, nil)
}

func kinds(values ...contextfabric.FactKind) []contextfabric.FactKind { return values }

// traceCases is §13.15.1's table, frame for frame, with each recorded cell
// transcribed from the trace output the design reproduces verbatim.
func traceCases() []traceCase {
	repository := contextfabric.SubjectRepository
	drivers := kinds(contextfabric.FactFlow, contextfabric.FactHealth, contextfabric.FactInvestment, contextfabric.FactReadiness, contextfabric.FactWorkload)
	trend := kinds(contextfabric.FactFlow, contextfabric.FactHealth, contextfabric.FactReadiness, contextfabric.FactWorkload)
	health := kinds(contextfabric.FactHealth)
	flow := kinds(contextfabric.FactFlow)

	return []traceCase{
		{
			id:    "Q1",
			shape: "named subject (team), assess_state",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, namedFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "subject", subject: contextfabric.SubjectTeam, wantLiteral: health, wantCharitable: health},
				{obligation: contextfabric.ObligationState, role: "subject", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "Q1'",
			shape: "named subject (project), assess_state",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, namedFrame(contextfabric.SubjectProject), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "subject", subject: contextfabric.SubjectProject, wantLiteral: health, wantCharitable: health},
				{obligation: contextfabric.ObligationState, role: "subject", subject: contextfabric.SubjectProject},
			},
		},
		{
			id:    "Q2",
			shape: "discovered team, rank + explain",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey, contextfabric.GoalExplainDrivers}, discoveredFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "member", subject: contextfabric.SubjectTeam, wantLiteral: health, wantCharitable: health},
				{obligation: contextfabric.ObligationPrincipalDrivers, role: "member", subject: contextfabric.SubjectTeam, wantLiteral: drivers, wantCharitable: drivers},
				{obligation: contextfabric.ObligationRanking, role: "member", subject: contextfabric.SubjectTeam},
				{obligation: contextfabric.ObligationState, role: "member", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "Q-A",
			shape: "grouped team->project, assess + explain",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalExplainDrivers}, groupedFrame(contextfabric.SubjectTeam, contextfabric.SubjectProject), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "member", subject: contextfabric.SubjectProject, wantLiteral: health, wantCharitable: health},
				{obligation: contextfabric.ObligationPrincipalDrivers, role: "member", subject: contextfabric.SubjectProject, wantLiteral: drivers, wantCharitable: drivers},
				{obligation: contextfabric.ObligationState, role: "member", subject: contextfabric.SubjectProject},
				{obligation: contextfabric.ObligationState, role: "group", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "Q-B",
			shape: "scoped team->project, assess",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, scopedFrame(contextfabric.SubjectProject), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "member", subject: contextfabric.SubjectProject, wantLiteral: health, wantCharitable: health},
				{obligation: contextfabric.ObligationState, role: "member", subject: contextfabric.SubjectProject},
			},
		},
		{
			id:    "C1",
			shape: "grouped team->project, assess + trend, time_series",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalDescribeTrend}, groupedFrame(contextfabric.SubjectTeam, contextfabric.SubjectProject), contextfabric.TemporalIntentTimeSeries, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "member", subject: contextfabric.SubjectProject, wantLiteral: health, wantCharitable: health},
				{obligation: contextfabric.ObligationState, role: "member", subject: contextfabric.SubjectProject},
				{obligation: contextfabric.ObligationTrendSeries, role: "member", subject: contextfabric.SubjectProject, wantLiteral: trend, wantCharitable: trend},
				{obligation: contextfabric.ObligationState, role: "group", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "C2",
			shape: "explicit_set team operands, compare, dims=investment_balance",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCompare}, explicitFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, []contextfabric.HealthDimension{contextfabric.HealthDimensionInvestmentBalance}),
			cells: []traceCell{
				{obligation: contextfabric.ObligationAllocationBreakdown, role: "subject(operand)", subject: contextfabric.SubjectTeam},
				{obligation: contextfabric.ObligationState, role: "subject(operand)", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "C3",
			shape: "grouped team->project, explain_change, period_comparison, dims=delivery_flow",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalExplainChange}, groupedFrame(contextfabric.SubjectTeam, contextfabric.SubjectProject), contextfabric.TemporalIntentPeriodComparison, nil, []contextfabric.HealthDimension{contextfabric.HealthDimensionDeliveryFlow}),
			cells: []traceCell{
				{obligation: contextfabric.ObligationPeriodDelta, role: "member", subject: contextfabric.SubjectProject, wantLiteral: flow, wantCharitable: flow},
				{obligation: contextfabric.ObligationPrincipalDrivers, role: "member", subject: contextfabric.SubjectProject, wantLiteral: flow, wantCharitable: flow},
				{obligation: contextfabric.ObligationState, role: "member", subject: contextfabric.SubjectProject},
				{obligation: contextfabric.ObligationState, role: "group", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "C4",
			shape: "discovered team, rank, both-ends emphasis",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey}, discoveredFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, []contextfabric.AnswerEmphasis{contextfabric.EmphasisPositiveOutliers, contextfabric.EmphasisNegativeOutliers}, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "member", subject: contextfabric.SubjectTeam, wantLiteral: health, wantCharitable: health},
				{obligation: contextfabric.ObligationRanking, role: "member", subject: contextfabric.SubjectTeam},
				{obligation: contextfabric.ObligationState, role: "member", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "C5",
			shape: "scoped repository->team, count",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate}, scopedFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationCount, role: "subject(anchor)", subject: contextfabric.SubjectRepository},
			},
		},
		{
			id:    "C6",
			shape: "explicit_set team operands, compare + trend, time_series, dims=investment_balance",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCompare, contextfabric.GoalDescribeTrend}, explicitFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentTimeSeries, nil, []contextfabric.HealthDimension{contextfabric.HealthDimensionInvestmentBalance}),
			cells: []traceCell{
				{obligation: contextfabric.ObligationAllocationBreakdown, role: "subject(operand)", subject: contextfabric.SubjectTeam},
				{obligation: contextfabric.ObligationState, role: "subject(operand)", subject: contextfabric.SubjectTeam},
				{obligation: contextfabric.ObligationTrendSeries, role: "subject(operand)", subject: contextfabric.SubjectTeam},
			},
		},
		{
			id:    "C7",
			shape: "organization scope, count, MemberKind=repository",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate}, orgFrame(&repository), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationCount, role: "subject(org)", subject: contextfabric.SubjectOrganization},
			},
		},
		{
			id:    "B5",
			shape: "organization scope, assess_state",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, orgFrame(nil), contextfabric.TemporalIntentCurrent, nil, nil),
			cells: []traceCell{
				{obligation: contextfabric.ObligationHealth, role: "subject", subject: contextfabric.SubjectOrganization},
				{obligation: contextfabric.ObligationState, role: "subject", subject: contextfabric.SubjectOrganization},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// The frozen intersection rule
// ---------------------------------------------------------------------------

// liveCapabilities reads the REAL registry. Not a fixture: the gate exists
// to answer what the engine can actually plan against.
func liveCapabilities(t *testing.T) map[contextfabric.FactKind]contextfabric.FactCapability {
	t.Helper()
	providers := devhealthfacts.NewProviders(nil)
	capabilities := make(map[contextfabric.FactKind]contextfabric.FactCapability, len(providers))
	for _, provider := range providers {
		capability := provider.Capability()
		if _, duplicate := capabilities[capability.Kind]; duplicate {
			t.Fatalf("registry declares fact kind %q twice", capability.Kind)
		}
		capabilities[capability.Kind] = capability
	}
	return capabilities
}

// frozenIntersection is §13.4.2a's rule, verbatim in behaviour:
//
//	SupportedSubjectKinds ∋ subjectKind
//	  ∩ Tables[subjectKind] ∋ requiredShape
//	  ∩ Dimension ∈ frame.Dimensions
//
// charitable relaxes ONLY the middle clause, and only for a rule whose
// shape requirement was the "breakdown or scalar" form: a capability that
// declares NO table for this subject kind answers with a scalar and is
// admitted. It never relaxes the subject-kind or dimension clauses -- which
// is exactly why §13.15.1 can say every empty cell is one of those two
// misses rather than a shape ambiguity.
func frozenIntersection(capabilities map[contextfabric.FactKind]contextfabric.FactCapability, obligation contextfabric.AnswerObligation, subject contextfabric.SubjectKind, dimensions []contextfabric.HealthDimension, charitable bool) []contextfabric.FactKind {
	rule, known := frozenRequirementRules[obligation]
	if !known {
		return nil
	}

	candidates := rule.seed
	if candidates == nil {
		// The `ranking` form: no seed, a shape demand over the whole
		// registry.
		for kind := range capabilities {
			candidates = append(candidates, kind)
		}
	}

	served := make([]contextfabric.FactKind, 0, len(candidates))
	for _, kind := range candidates {
		capability, registered := capabilities[kind]
		if !registered {
			continue
		}
		if !supportsSubject(capability, subject) {
			continue
		}
		if len(dimensions) > 0 && !dimensionAllowed(capability, dimensions) {
			continue
		}
		if rule.shape != "" && !declaresShape(capability, subject, rule.shape, charitable && rule.shapeOptional) {
			continue
		}
		served = append(served, kind)
	}
	sort.Slice(served, func(i, j int) bool { return served[i] < served[j] })
	if len(served) == 0 {
		return nil
	}
	return served
}

// emptyCellCause names WHY a cell came back empty, by first failing
// clause of the intersection rule. §13.15.1 makes a specific claim about
// this distribution and TestEveryEmptyCellIsASubjectOrDimensionMiss turns
// that claim into an assertion.
type emptyCellCause string

const (
	causeSubjectKind emptyCellCause = "subject_kind_unsupported"
	causeDimension   emptyCellCause = "dimension_excluded"
	causeShape       emptyCellCause = "table_shape_undeclared"
	causeUnseeded    emptyCellCause = "no_seeded_kind_registered"
)

// classifyEmptyCell reports, per seeded kind, the first clause that
// rejected it. A cell is attributed to a cause only when EVERY candidate
// failed at that clause or earlier -- so "subject_kind" means no seeded
// kind serves this subject at all, which is a different and much harder
// problem than "the right producer exists but declares no table".
func classifyEmptyCell(capabilities map[contextfabric.FactKind]contextfabric.FactCapability, obligation contextfabric.AnswerObligation, subject contextfabric.SubjectKind, dimensions []contextfabric.HealthDimension) emptyCellCause {
	rule := frozenRequirementRules[obligation]
	candidates := rule.seed
	if candidates == nil {
		for kind := range capabilities {
			candidates = append(candidates, kind)
		}
	}

	survivedSubject, survivedDimension := false, false
	registered := false
	for _, kind := range candidates {
		capability, known := capabilities[kind]
		if !known {
			continue
		}
		registered = true
		if !supportsSubject(capability, subject) {
			continue
		}
		survivedSubject = true
		if len(dimensions) > 0 && !dimensionAllowed(capability, dimensions) {
			continue
		}
		survivedDimension = true
	}

	switch {
	case !registered:
		return causeUnseeded
	case !survivedSubject:
		return causeSubjectKind
	case !survivedDimension:
		return causeDimension
	default:
		return causeShape
	}
}

// TestNoEmptyCellIsExplainedByTheBreakdownOrScalarAmbiguity asserts
// §13.15.1's own claim in the form it was written:
//
//	"The `breakdown` or scalar clause was evaluated both literally and
//	 charitably; EVERY empty cell below is a SupportedSubjectKinds/
//	 Dimension miss, not a shape ambiguity."
//
// The checkable content of that sentence is that the literal and charitable
// readings AGREE on every cell -- if they did, the ambiguity would explain
// nothing, and the requirement layer's problem could not be fixed by
// settling how generously "breakdown or scalar" is read. This test is what
// makes that a measured fact at THIS tip rather than a quoted one from
// a8441bce, and it is the reason a future producer that starts declaring a
// table cannot quietly move the finding's diagnosis.
func TestNoEmptyCellIsExplainedByTheBreakdownOrScalarAmbiguity(t *testing.T) {
	capabilities := liveCapabilities(t)

	for _, testCase := range traceCases() {
		for _, cell := range testCase.cells {
			literal := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, false)
			charitable := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, true)
			if !sameKinds(literal, charitable) {
				t.Errorf("%s %s subj=%s: the literal and charitable readings DISAGREE (literal=%v charitable=%v). "+
					"§13.15.1 records that they agree on every cell; a disagreement means the shape clause explains part of this cell and the finding's diagnosis needs re-stating.",
					testCase.id, cell.obligation, cell.subject, format(literal), format(charitable))
			}
		}
	}
}

// TestEmptyCellCauseBreakdown records WHICH clause emptied each cell.
//
// It is a recording test, not a gate: the numbers below are the input to
// PR1's declaration design (a subject-kind miss is fixed by a producer
// declaring the obligation; a table-shape miss is a producer question about
// what it can actually emit; a dimension miss is the narrowing question
// this lane must settle on the rig). Publishing the count without the
// causes would hand the next reader a number they cannot act on.
func TestEmptyCellCauseBreakdown(t *testing.T) {
	capabilities := liveCapabilities(t)

	type key struct {
		obligation contextfabric.AnswerObligation
		cause      emptyCellCause
	}
	counts := map[key]int{}
	perObligation := map[contextfabric.AnswerObligation]int{}

	for _, testCase := range traceCases() {
		for _, cell := range testCase.cells {
			if len(frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, true)) > 0 {
				continue
			}
			cause := classifyEmptyCell(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions)
			counts[key{cell.obligation, cause}]++
			perObligation[cell.obligation]++
		}
	}

	obligations := make([]string, 0, len(perObligation))
	for obligation := range perObligation {
		obligations = append(obligations, string(obligation))
	}
	sort.Strings(obligations)

	var report strings.Builder
	total := 0
	for _, name := range obligations {
		obligation := contextfabric.AnswerObligation(name)
		for _, cause := range []emptyCellCause{causeSubjectKind, causeDimension, causeShape, causeUnseeded} {
			if count := counts[key{obligation, cause}]; count > 0 {
				fmt.Fprintf(&report, "\n  %-22s %-26s %d", obligation, cause, count)
			}
		}
		total += perObligation[obligation]
	}
	t.Logf("empty cells by obligation and first failing clause (total %d):%s", total, report.String())
}

// TestDimensionsNarrowingEmptiesCellsThatAreOtherwiseServed measures
// execution-decided question (a) -- "may `Dimensions` narrow evidence at
// all?" -- on the half of it that is decidable OFFLINE, and states exactly
// where the offline half stops.
//
// WHY THE CAUSE BREAKDOWN CANNOT ANSWER THIS. classifyEmptyCell attributes
// a cell to the FIRST clause each candidate failed, so a cell where the
// dimension clause admits one producer that then fails the shape clause is
// recorded as a shape miss -- accurate, and NOT the question. The question
// is counterfactual: would this cell be served if the dimension clause were
// not applied? That needs both computations, which is what this does.
//
// WHAT IT DOES NOT DECIDE. Whether narrowing is CORRECT is a product
// question about what the answer should contain, and §13.4.2a says only
// running the composed cases on real data settles it. This test tells the
// rig run exactly which cells to look at, so the live pass measures a named
// set instead of hunting. It is a recording test and never fails on the
// count.
func TestDimensionsNarrowingEmptiesCellsThatAreOtherwiseServed(t *testing.T) {
	capabilities := liveCapabilities(t)

	var report strings.Builder
	narrowed := 0
	for _, testCase := range traceCases() {
		if len(testCase.frame.Dimensions) == 0 {
			continue
		}
		for _, cell := range testCase.cells {
			withDimensions := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, true)
			withoutDimensions := frozenIntersection(capabilities, cell.obligation, cell.subject, nil, true)
			if len(withDimensions) > 0 || len(withoutDimensions) == 0 {
				continue
			}
			narrowed++
			fmt.Fprintf(&report, "\n  %-4s %-22s subj=%-8s dims=%v: narrowed=[] un-narrowed=%s",
				testCase.id, cell.obligation, cell.subject, testCase.frame.Dimensions, format(withoutDimensions))
		}
	}

	t.Logf("cells the Dimensions clause empties that the un-narrowed set would SERVE: %d%s", narrowed, report.String())
	t.Logf("decision rule fixed BEFORE the rig pass: if narrowing empties a cell the un-narrowed set fills, " +
		"the pinned behaviour is fall back to the un-narrowed set with a disclosed not_applicable on the dimension -- never a silent empty set.")
}

func supportsSubject(capability contextfabric.FactCapability, subject contextfabric.SubjectKind) bool {
	for _, kind := range capability.SupportedSubjectKinds {
		if kind == subject {
			return true
		}
	}
	return false
}

func dimensionAllowed(capability contextfabric.FactCapability, dimensions []contextfabric.HealthDimension) bool {
	for _, dimension := range dimensions {
		if capability.Dimension == dimension {
			return true
		}
	}
	return false
}

func declaresShape(capability contextfabric.FactCapability, subject contextfabric.SubjectKind, shape contextfabric.FactTableShape, scalarCounts bool) bool {
	shapes, declared := capability.Tables[subject]
	if len(shapes) == 0 {
		// No declared table for this subject kind: the capability answers
		// with a scalar. The literal reading refuses it; the charitable
		// "breakdown or scalar" reading admits it.
		_ = declared
		return scalarCounts
	}
	for _, candidate := range shapes {
		if candidate == shape {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Assertion 1 -- the transcription is faithful
// ---------------------------------------------------------------------------

// TestFrozenRuleTranscriptionReproducesTheRecordedTrace is the
// salted-positive check that makes the gate's redness mean something.
//
// A transcription error also produces empty cells. Counting empties from an
// unverified transcription would therefore "confirm" the design's finding
// for a reason that has nothing to do with the frozen rule -- the same
// shape as every "a green signal does not mean what it says" instance in
// the lane brief, inverted: a RED signal that does not mean what it says.
// This test pins every NON-empty cell the recorded trace printed, so a
// transcription that under-serves fails here rather than passing as a
// larger empty count next door.
func TestFrozenRuleTranscriptionReproducesTheRecordedTrace(t *testing.T) {
	capabilities := liveCapabilities(t)

	for _, testCase := range traceCases() {
		t.Run(testCase.id, func(t *testing.T) {
			for _, cell := range testCase.cells {
				literal := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, false)
				charitable := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, true)

				if !sameKinds(literal, cell.wantLiteral) {
					t.Errorf("%s %s role=%s subj=%s: literal FactKinds = %v, recorded trace says %v",
						testCase.id, cell.obligation, cell.role, cell.subject, format(literal), format(cell.wantLiteral))
				}
				if !sameKinds(charitable, cell.wantCharitable) {
					t.Errorf("%s %s role=%s subj=%s: charitable FactKinds = %v, recorded trace says %v",
						testCase.id, cell.obligation, cell.role, cell.subject, format(charitable), format(cell.wantCharitable))
				}
			}
		})
	}
}

// TestEveryTracedFrameDerivesTheObligationsItsCellsAssume guards the OTHER
// direction of the same faithfulness question: the cells above are a
// hand-transcribed list, and a cell for an obligation the frame does not
// actually derive would be traced against nothing while looking like
// coverage. Quantifying over the frame's own derived set catches a cell
// that was dropped from the transcription as well as one that was invented.
//
// evidence and coverage are answer-contract obligations (§13.2.3): they are
// satisfied by the answer contract and read no facts, so they legitimately
// have no cell. Everything else must have one.
func TestEveryTracedFrameDerivesTheObligationsItsCellsAssume(t *testing.T) {
	for _, testCase := range traceCases() {
		t.Run(testCase.id, func(t *testing.T) {
			traced := make(map[contextfabric.AnswerObligation]bool, len(testCase.cells))
			for _, cell := range testCase.cells {
				if !testCase.frame.HasObligation(cell.obligation) {
					t.Errorf("%s traces a cell for %q, which the frame does not derive (derived: %v)",
						testCase.id, cell.obligation, testCase.frame.Obligations)
				}
				traced[cell.obligation] = true
			}
			for _, obligation := range testCase.frame.Obligations {
				kind, known := contextfabric.KindOfObligation(obligation)
				if !known {
					t.Fatalf("%s: obligation %q has no declared kind", testCase.id, obligation)
				}
				if kind == contextfabric.ObligationKindAnswerContract {
					continue
				}
				if !traced[obligation] {
					t.Errorf("%s derives %q (kind %s) but the trace has no cell for it", testCase.id, obligation, kind)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Assertion 2 -- THE GATE. Red at this tip by construction.
// ---------------------------------------------------------------------------

// TestO9EveryRequiredReadObligationIsServed is oracle O9's core assertion:
// for every acceptance question and every composed case, no required READ
// obligation derives an empty FactKinds set.
//
// It is RED at this tip. That redness is the ENTRY GATE: law L10 requires
// the rule to be executed against the real registry before it is written
// down, and this is that execution. It goes green only when the generated
// mapping fills every cell a producer can serve AND names `unavailable`
// for every cell none can -- a silent empty is never acceptable, which is
// the distinction the frozen rule could not express.
func TestO9EveryRequiredReadObligationIsServed(t *testing.T) {
	capabilities := liveCapabilities(t)

	empty := 0
	var report strings.Builder
	for _, testCase := range traceCases() {
		fmt.Fprintf(&report, "\n=== %s  %s\n", testCase.id, testCase.shape)
		fmt.Fprintf(&report, "    obligations: %v\n", testCase.frame.Obligations)
		for _, cell := range testCase.cells {
			rule := frozenRequirementRules[cell.obligation]
			literal := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, false)
			charitable := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, true)
			marker := ""
			if len(charitable) == 0 {
				empty++
				marker = "  <== EMPTY (unavailable)"
			}
			fmt.Fprintf(&report, "    %-20s role=%-16s subj=%-13s quant=%-12s literal=%v charitable=%v%s\n",
				cell.obligation, cell.role, cell.subject, rule.quantifier, format(literal), format(charitable), marker)
		}
	}

	t.Logf("registry: %d capabilities%s", len(capabilities), report.String())
	t.Logf("EMPTY FactKinds cells across %d frames: %d", len(traceCases()), empty)

	if empty > 0 {
		t.Fatalf("O9 RED: the frozen requirement rule derives %d empty requirement cell(s) across the acceptance and composed cases (see log). "+
			"A required read obligation with no fact kind is unanswerable and, worse, is SILENT about it: nothing in the row says `unavailable`. "+
			"This is the entry gate -- the derivation is not written until this is green.", empty)
	}
}

// TestComputedObligationsAreNotModelledAsReads records round 4's N3 finding
// as an executed assertion rather than a note.
//
// `ranking` and `count` are COMPUTED obligations (§13.2.3): a named server
// step over already-read facts or over the resolved set. The frozen rule
// modelled both as READS with a required table shape no producer declares,
// which is why BAR Q2's `ranking` derived the empty set. The shipped
// vocabulary already classifies them correctly and already names their
// steps -- so the mis-typing is in the requirement layer, and this test
// pins the classification the layer must honour.
func TestComputedObligationsAreNotModelledAsReads(t *testing.T) {
	for _, obligation := range []contextfabric.AnswerObligation{contextfabric.ObligationRanking, contextfabric.ObligationCount} {
		kind, known := contextfabric.KindOfObligation(obligation)
		if !known || kind != contextfabric.ObligationKindComputed {
			t.Fatalf("%q must be classified computed, got %q (known=%v)", obligation, kind, known)
		}
		step, named := contextfabric.StepForComputedObligation(obligation)
		if !named || step == "" {
			t.Fatalf("computed obligation %q must name its server step", obligation)
		}
		if _, modelled := frozenRequirementRules[obligation]; !modelled {
			t.Fatalf("the frozen transcription is expected to model %q as a read -- that mis-typing is the finding", obligation)
		}
	}
}

// ---------------------------------------------------------------------------
// The registry dump -- the trace's own INPUT record
// ---------------------------------------------------------------------------

// TestRegistryDump records what the trace actually ran against.
//
// A probe that cannot show its input reached the code under test is not a
// measurement: a wrong registry produces a confident, plausible, wrong
// empty count. This records the declarations cell by cell so the artifact
// carries its own inputs, and asserts the provider count so a registry that
// changed under the trace is loud rather than silent.
func TestRegistryDump(t *testing.T) {
	capabilities := liveCapabilities(t)

	const wantProviders = 21
	if len(capabilities) != wantProviders {
		t.Errorf("registry declares %d capabilities, the trace was recorded against %d -- "+
			"re-read the dump below before trusting any cell count", len(capabilities), wantProviders)
	}

	names := make([]string, 0, len(capabilities))
	for kind := range capabilities {
		names = append(names, string(kind))
	}
	sort.Strings(names)

	var dump strings.Builder
	for _, name := range names {
		capability := capabilities[contextfabric.FactKind(name)]
		tableKinds := make([]string, 0, len(capability.Tables))
		for subject := range capability.Tables {
			tableKinds = append(tableKinds, string(subject))
		}
		sort.Strings(tableKinds)
		tables := make([]string, 0, len(tableKinds))
		for _, subject := range tableKinds {
			shapes := capability.Tables[contextfabric.SubjectKind(subject)]
			rendered := make([]string, 0, len(shapes))
			for _, shape := range shapes {
				rendered = append(rendered, string(shape))
			}
			sort.Strings(rendered)
			tables = append(tables, fmt.Sprintf("%s:[%s]", subject, strings.Join(rendered, " ")))
		}
		subjects := make([]string, 0, len(capability.SupportedSubjectKinds))
		for _, subject := range capability.SupportedSubjectKinds {
			subjects = append(subjects, string(subject))
		}
		fmt.Fprintf(&dump, "\n  %-26s dim=%-32s subjects=[%s] tables=map[%s]",
			name, capability.Dimension, strings.Join(subjects, " "), strings.Join(tables, " "))
	}
	t.Logf("%s", dump.String())
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func sameKinds(got, want []contextfabric.FactKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func format(values []contextfabric.FactKind) string {
	rendered := make([]string, 0, len(values))
	for _, value := range values {
		rendered = append(rendered, string(value))
	}
	return "[" + strings.Join(rendered, " ") + "]"
}
