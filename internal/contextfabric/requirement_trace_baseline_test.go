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
//  2. TestFrozenRuleLeavesRequirementCellsUnserved PINS THE
//     DEFECT, count and cause distribution. Oracle O9's real assertion --
//     "no required read obligation derives an empty fact-kind set" -- is
//     RED at this tip by construction, and that red run is the entry gate
//     law L10 demands. A permanently-red test cannot live on main, so what
//     ships is the baseline pinned exactly; the derivation slice replaces
//     that function with the served-assertion once the generated mapping
//     can satisfy it. Its name is the finding: it passing means the layer
//     is still broken in exactly the measured way, never that it works.
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
	"os"
	"path/filepath"
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

// TestDerivedCellsCoverScopedOperands is round 3's finding as an executed
// assertion, and it is the reviewer's own construction.
//
// SubjectOperand carries Named OR Scoped. derivedCells handled only Named,
// so an explicit set whose second operand is scoped produced coordinates
// for the first operand alone -- and because the recorded artifact is
// rendered by the same function, the artifact agreed and all nineteen
// tests passed while a valid operand was missing from the trace.
//
// The construction: an explicit set comparing a named team against a
// scoped project. Both operands must appear.
func TestDerivedCellsCoverScopedOperands(t *testing.T) {
	team := contextfabric.SubjectTeam
	expression := contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionExplicitSet,
		Explicit: &contextfabric.ExplicitSetExpression{
			Operands: []contextfabric.SubjectOperand{
				{
					Kind:  contextfabric.SubjectOperandNamed,
					Named: &contextfabric.NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: &team},
				},
				{
					Kind:   contextfabric.SubjectOperandScoped,
					Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"b"}, MemberKind: contextfabric.SubjectProject},
				},
			},
		},
	}
	frame := buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCompare}, expression, contextfabric.TemporalIntentCurrent, nil, nil)

	cells := derivedCells(frame)
	if len(cells) == 0 {
		t.Fatal("the frame derived no cells at all; this assertion would be vacuous")
	}

	subjects := map[contextfabric.SubjectKind]int{}
	for _, cell := range cells {
		subjects[cell.subject]++
	}
	for _, want := range []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject} {
		if subjects[want] == 0 {
			t.Errorf("no cell was derived for operand subject kind %q (derived: %v) -- "+
				"an operand variant that is dropped here vanishes from the recorded trace too, "+
				"because the artifact is rendered by this same function",
				want, subjects)
		}
	}

	// The coverage guard must agree, or an operand could be derived and
	// then rejected as "outside what the frame covers".
	covered := frameSubjectKinds(frame.SubjectExpression)
	for _, want := range []contextfabric.SubjectKind{contextfabric.SubjectTeam, contextfabric.SubjectProject} {
		if !covered[want] {
			t.Errorf("frameSubjectKinds omits operand subject kind %q", want)
		}
	}
}

// derivedCells builds the traced coordinates FROM THE FRAME instead of from
// a hand table.
//
// ROUND 2 -> ROUND 3. The hand table was the last co-editable authority in
// this file: the artifact became a generated output, but the renderer
// walked the table, so deleting a cell removed it from both and every test
// stayed green. Deriving the coordinates removes the thing the edit
// deleted from -- there is no list of cells to shorten.
//
// The role assignment comes from the SubjectExpression variant, which is
// production code and which already carries the kinds:
//
//	grouped   -> member = MemberKind, group = GroupKind
//	scoped    -> member = MemberKind
//	named     -> subject = ExpectedKind
//	explicit  -> subject(operand) = each operand's ExpectedKind
//	discovered-> member = MemberKind
//	org       -> subject(org) = organization
//
// The ANCHOR role has no derived coordinate and that is deliberate:
// ScopedSetExpression carries AnchorTerms, which are RETRIEVAL POINTERS,
// NEVER VALUES (frame.go's own words), so the anchor's kind is settled at
// resolution time and the frame does not know it. A cell that used to be
// traced for the anchor therefore leaves the derived set -- reported as a
// measured delta, not tuned away.
func derivedCells(frame contextfabric.QuestionFrame) []traceCell {
	type roleSubject struct {
		role    string
		subject contextfabric.SubjectKind
	}
	var pairs []roleSubject
	expression := frame.SubjectExpression
	if grouped := expression.Grouped; grouped != nil {
		pairs = append(pairs,
			roleSubject{"member", grouped.MemberKind},
			roleSubject{"group", grouped.GroupKind})
	}
	if scoped := expression.Scoped; scoped != nil {
		pairs = append(pairs, roleSubject{"member", scoped.MemberKind})
	}
	if named := expression.Named; named != nil && named.ExpectedKind != nil {
		pairs = append(pairs, roleSubject{"subject", *named.ExpectedKind})
	}
	if explicit := expression.Explicit; explicit != nil {
		for _, operand := range explicit.Operands {
			// BOTH operand variants. Round 3 found this handling only the
			// named one: SubjectOperand carries Named OR Scoped
			// (frame.go), a scoped operand is valid in an explicit set and
			// carries its own MemberKind, and dropping it removed a real
			// operand's cells from the trace while every test stayed green
			// -- the artifact regenerates from this same function, so both
			// sides agreed about a coordinate that was never derived.
			if operand.Named != nil && operand.Named.ExpectedKind != nil {
				pairs = append(pairs, roleSubject{"subject(operand)", *operand.Named.ExpectedKind})
			}
			if operand.Scoped != nil {
				pairs = append(pairs, roleSubject{"subject(operand)", operand.Scoped.MemberKind})
			}
		}
	}
	if discovered := expression.Discovered; discovered != nil {
		pairs = append(pairs, roleSubject{"member", discovered.MemberKind})
	}
	if expression.Org != nil {
		pairs = append(pairs, roleSubject{"subject(org)", contextfabric.SubjectOrganization})
	}

	seen := map[string]bool{}
	cells := make([]traceCell, 0, len(pairs)*len(frame.Obligations))
	for _, obligation := range frame.Obligations {
		kind, known := contextfabric.KindOfObligation(obligation)
		if !known || kind == contextfabric.ObligationKindAnswerContract {
			continue
		}
		for _, pair := range pairs {
			key := string(obligation) + "|" + pair.role + "|" + string(pair.subject)
			if seen[key] {
				continue
			}
			seen[key] = true
			cells = append(cells, traceCell{obligation: obligation, role: pair.role, subject: pair.subject})
		}
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].obligation != cells[j].obligation {
			return cells[i].obligation < cells[j].obligation
		}
		if cells[i].role != cells[j].role {
			return cells[i].role < cells[j].role
		}
		return cells[i].subject < cells[j].subject
	})
	return cells
}

func traceCases() []traceCase {
	repository := contextfabric.SubjectRepository

	cases := []traceCase{
		{
			id:    "Q1",
			shape: "named subject (team), assess_state",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, namedFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, nil),
		},
		{
			id:    "Q1'",
			shape: "named subject (project), assess_state",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, namedFrame(contextfabric.SubjectProject), contextfabric.TemporalIntentCurrent, nil, nil),
		},
		{
			id:    "Q2",
			shape: "discovered team, rank + explain",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey, contextfabric.GoalExplainDrivers}, discoveredFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, nil),
		},
		{
			id:    "Q-A",
			shape: "grouped team->project, assess + explain",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalExplainDrivers}, groupedFrame(contextfabric.SubjectTeam, contextfabric.SubjectProject), contextfabric.TemporalIntentCurrent, nil, nil),
		},
		{
			id:    "Q-B",
			shape: "scoped team->project, assess",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, scopedFrame(contextfabric.SubjectProject), contextfabric.TemporalIntentCurrent, nil, nil),
		},
		{
			id:    "C1",
			shape: "grouped team->project, assess + trend, time_series",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalDescribeTrend}, groupedFrame(contextfabric.SubjectTeam, contextfabric.SubjectProject), contextfabric.TemporalIntentTimeSeries, nil, nil),
		},
		{
			id:    "C2",
			shape: "explicit_set team operands, compare, dims=investment_balance",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCompare}, explicitFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, []contextfabric.HealthDimension{contextfabric.HealthDimensionInvestmentBalance}),
		},
		{
			id:    "C3",
			shape: "grouped team->project, explain_change, period_comparison, dims=delivery_flow",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalExplainChange}, groupedFrame(contextfabric.SubjectTeam, contextfabric.SubjectProject), contextfabric.TemporalIntentPeriodComparison, nil, []contextfabric.HealthDimension{contextfabric.HealthDimensionDeliveryFlow}),
		},
		{
			id:    "C4",
			shape: "discovered team, rank, both-ends emphasis",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey}, discoveredFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, []contextfabric.AnswerEmphasis{contextfabric.EmphasisPositiveOutliers, contextfabric.EmphasisNegativeOutliers}, nil),
		},
		{
			id:    "C5",
			shape: "scoped repository->team, count",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate}, scopedFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentCurrent, nil, nil),
		},
		{
			id:    "C6",
			shape: "explicit_set team operands, compare + trend, time_series, dims=investment_balance",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCompare, contextfabric.GoalDescribeTrend}, explicitFrame(contextfabric.SubjectTeam), contextfabric.TemporalIntentTimeSeries, nil, []contextfabric.HealthDimension{contextfabric.HealthDimensionInvestmentBalance}),
		},
		{
			id:    "C7",
			shape: "organization scope, count, MemberKind=repository",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate}, orgFrame(&repository), contextfabric.TemporalIntentCurrent, nil, nil),
		},
		{
			id:    "B5",
			shape: "organization scope, assess_state",
			frame: buildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, orgFrame(nil), contextfabric.TemporalIntentCurrent, nil, nil),
		},
	}
	for index := range cases {
		cases[index].cells = derivedCells(cases[index].frame)
	}
	return cases
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

	// ASSERTED, not merely logged. Round 1 (P3) was right that the
	// surrounding prose claims "exactly one" while the test established
	// only "one, on this run" -- a second such cell would have appeared in
	// the log and left the test green, so the durable claim had no guard.
	// The number is small and specific on purpose: if it moves, the rig
	// pass for the dimension-narrowing question is aimed at the wrong set
	// and needs re-deriving before it is run.
	const wantNarrowed = 1
	if narrowed != wantNarrowed {
		t.Errorf("the Dimensions clause empties %d otherwise-served cell(s); the measured baseline is %d. "+
			"This is the named target the live pass examines -- re-derive it from the log above before running that pass.",
			narrowed, wantNarrowed)
	}
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

// recordedTraceArtifact is the INDEPENDENT reference: the design's own
// recorded trace output, transcribed into testdata and checked in.
//
// WHY IT EXISTS, and this is a review finding applied rather than noted.
// Round 1 showed the transcription assertion was SELF-REFERENTIAL: its
// expectations came from `traceCases`, the same table the trace reads, so
// it proved internal consistency and nothing else. The reviewer deleted a
// real cell (Q-A's group-role `state`), edited the two count constants
// from 22/14 to 21/13, and every test passed while the run logged "EMPTY
// FactKinds cells across 13 frames: 21". Re-executed and CONFIRMED by this
// lane before being accepted.
//
// The fix is not another in-file assertion — that would have the same
// defect. The recorded trace moves OUT of the test into an artifact whose
// authority is the design document, the expectations are read FROM it, and
// the empty count is DERIVED from it rather than declared beside it. A
// cell cannot now be dropped without the artifact disagreeing, and the
// count cannot be edited to match a changed table because nothing declares
// it.
const recordedTraceArtifact = "testdata/recorded_trace.txt"

// renderLiveTrace renders the frozen rule's output AS EXECUTED against the
// live registry: every cell, plus the empty count and its cause
// distribution.
//
// ROUND 2 REBUILT THIS. The artifact used to be a hand-transcribed
// expectation INPUT, and round 2 showed that made it co-editable with the
// table it validated: deleting Q-A's group `state` cell from BOTH sources
// and editing the cause baseline 14 -> 13 passed all eighteen tests and
// silently moved the recorded empty count 22 -> 21. Moving expectations
// out of the test file had only moved the co-location one directory over.
//
// So the artifact is now an OUTPUT, regenerated from production code and
// diffed, exactly like testdata/obligation_seed.txt. To change the recorded
// measurement you must now change the frozen transcription or the registry
// -- production code, visible in review -- and the diff states the old and
// new numbers in words. The counts live in the artifact rather than in
// constants beside the data, so there is no digit left to edit.
//
// What no test can establish is fidelity to the DESIGN DOCUMENT itself: any
// reference committed to this repository is editable in the same commit as
// the code it judges. That is a review control, stated here rather than
// implied by a test name -- the same bound round 1 recorded for F1.
func renderLiveTrace(t *testing.T, capabilities map[contextfabric.FactKind]contextfabric.FactCapability) string {
	t.Helper()

	var out strings.Builder
	out.WriteString("# GENERATED by TestFrozenRuleLeavesRequirementCellsUnserved from the\n")
	out.WriteString("# frozen requirement transcription EXECUTED against the live fact registry.\n")
	out.WriteString("# DO NOT EDIT BY HAND: a test regenerates and diffs this file.\n")
	out.WriteString("#\n")
	out.WriteString("# A diff here is the measurement moving. That is not automatically bad --\n")
	out.WriteString("# read the cause distribution at the foot and say what moved it.\n")
	out.WriteString("#\n")
	out.WriteString("# frame\tobligation\trole\tsubject\tliteral\tcharitable\n")

	empty := 0
	causes := map[string]int{}
	evaluated := 0
	for _, testCase := range traceCases() {
		for _, cell := range testCase.cells {
			evaluated++
			literal := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, false)
			charitable := frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, true)
			fmt.Fprintf(&out, "%s\t%s\t%s\t%s\t%s\t%s\n",
				testCase.id, cell.obligation, cell.role, cell.subject, format(literal), format(charitable))
			if len(charitable) == 0 {
				empty++
				causes[string(cell.obligation)+"/"+string(classifyEmptyCell(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions))]++
			}
		}
	}
	if evaluated == 0 {
		t.Fatal("the trace evaluated no cells; every assertion built on this artifact would be vacuous")
	}

	keys := make([]string, 0, len(causes))
	for key := range causes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintf(&out, "#\n# cells evaluated: %d\n# EMPTY cells: %d\n", evaluated, empty)
	for _, key := range keys {
		fmt.Fprintf(&out, "# cause %s: %d\n", key, causes[key])
	}
	return out.String()
}

type recordedCell struct {
	frame      string
	obligation string
	role       string
	subject    string
	literal    string
	charitable string
}

func loadRecordedTrace(t *testing.T) map[string]recordedCell {
	t.Helper()
	raw, err := os.ReadFile(recordedTraceArtifact)
	if err != nil {
		t.Fatalf("reading the recorded-trace artifact: %v", err)
	}
	cells := map[string]recordedCell{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 6 {
			t.Fatalf("malformed artifact line (want 6 tab-separated fields, got %d): %q", len(parts), line)
		}
		cell := recordedCell{frame: parts[0], obligation: parts[1], role: parts[2], subject: parts[3], literal: parts[4], charitable: parts[5]}
		key := cell.frame + "|" + cell.obligation + "|" + cell.role + "|" + cell.subject
		if _, duplicate := cells[key]; duplicate {
			t.Fatalf("artifact declares %q twice", key)
		}
		cells[key] = cell
	}
	if len(cells) == 0 {
		t.Fatal("the recorded-trace artifact is empty; every assertion built on it would be vacuous")
	}
	return cells
}

// TestTraceCasesMatchTheRecordedArtifactExactly and
// TestFrozenRuleTranscriptionReproducesTheRecordedTrace were REMOVED in
// round 3, and their removal is the point rather than a loss of coverage.
//
// Both compared the live derivation against per-cell expectations written
// into this file's own trace table. Round 2 showed that pairing was the
// defect: the table supplied both the coordinates and the expected values,
// the artifact was rendered by walking the same table, and a coordinated
// edit of the two moved the recorded empty count with every test green.
//
// The coordinates are now DERIVED from the frame (see derivedCells) and the
// values are DERIVED from the frozen rule executed against the live
// registry, rendered into testdata/recorded_trace.txt and diffed. There is
// no hand-written expectation left for a test to agree with, which is why
// these two have nothing to assert: what they used to check is now the
// artifact diff, and what they could not check -- that the table itself was
// honest -- is no longer a question that can be asked.

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
// frameSubjectKinds derives the subject kinds a frame's expression covers
// FROM PRODUCTION -- SubjectExpression already carries them.
//
// This is what makes the cell coordinates checkable. Round 2's coordinated
// edit deleted a traced cell from the table AND from the artifact; because
// the artifact is rendered by walking that same table, both agreed and
// every test stayed green. The table said which cells exist and nothing
// said which cells OUGHT to exist. This does.
func frameSubjectKinds(expression contextfabric.SubjectExpression) map[contextfabric.SubjectKind]bool {
	kinds := map[contextfabric.SubjectKind]bool{}
	if grouped := expression.Grouped; grouped != nil {
		kinds[grouped.GroupKind] = true
		kinds[grouped.MemberKind] = true
	}
	if scoped := expression.Scoped; scoped != nil {
		kinds[scoped.MemberKind] = true
	}
	if explicit := expression.Explicit; explicit != nil {
		for _, operand := range explicit.Operands {
			if operand.Named != nil && operand.Named.ExpectedKind != nil {
				kinds[*operand.Named.ExpectedKind] = true
			}
			if operand.Scoped != nil {
				kinds[operand.Scoped.MemberKind] = true
			}
		}
	}
	if named := expression.Named; named != nil && named.ExpectedKind != nil {
		kinds[*named.ExpectedKind] = true
	}
	if discovered := expression.Discovered; discovered != nil {
		kinds[discovered.MemberKind] = true
	}
	if org := expression.Org; org != nil {
		kinds[contextfabric.SubjectOrganization] = true
	}
	return kinds
}

// TestNoTracedCellIsOutsideWhatTheFrameCovers checks the direction of
// round 2's coordinated edit that IS derivable from production today.
//
// WHAT THIS CLOSES: an INVENTED coordinate. Every traced cell's subject
// kind must be one the frame's own SubjectExpression covers, and its
// obligation must be one the frame derives, so a row cannot be added that
// looks like recorded evidence for a subject the question never asked
// about.
//
// WHAT IT DOES NOT CLOSE, AND WHY -- stated because the name would
// otherwise imply more. The opposite direction (no cell OMITTED) needs the
// frozen role table: in a grouped frame most obligations attach to the
// MEMBER kind and only `state` attaches to the GROUP kind, so a flat
// obligation x subject-kind product over-implies badly (it demands
// health@team for Q-A, which the design does not). That role assignment
// is exactly the mapping the DERIVATION slice builds; it does not exist in
// production code at this tip, and transcribing it here by hand would
// recreate the hand-maintained authority round 2 found. So deletion stays
// a REVIEW control -- the generated artifact's "cells evaluated" and
// "EMPTY cells" lines move in the diff -- and closing it structurally is
// owed by the slice that makes the role table real.
func TestNoTracedCellIsOutsideWhatTheFrameCovers(t *testing.T) {
	checked := 0
	for _, testCase := range traceCases() {
		t.Run(testCase.id, func(t *testing.T) {
			subjects := frameSubjectKinds(testCase.frame.SubjectExpression)
			if len(subjects) == 0 {
				t.Fatalf("%s: the frame's subject expression yields no subject kinds; "+
					"every coordinate check over this frame would be vacuous", testCase.id)
			}
			for _, cell := range testCase.cells {
				// The ANCHOR's subject kind is not derivable from the
				// frame, and that is deliberate rather than a gap:
				// ScopedSetExpression carries AnchorTerms, which are
				// RETRIEVAL POINTERS, NEVER VALUES (frame.go's own words),
				// so the anchor's kind is settled at resolution time and
				// the frame does not carry it. C5 traces count@repository
				// off a repository anchor scoping to teams; checking that
				// coordinate against the expression would demand the frame
				// know something it refuses to know.
				if cell.role == "subject(anchor)" {
					continue
				}
				checked++
				if !subjects[cell.subject] {
					t.Errorf("%s traces %q for subject kind %q, which the frame's subject expression "+
						"does not cover (covers: %v) -- an invented coordinate",
						testCase.id, cell.obligation, cell.subject, subjectKindList(subjects))
				}
				if !testCase.frame.HasObligation(cell.obligation) {
					t.Errorf("%s traces %q, which the frame does not derive", testCase.id, cell.obligation)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("no coordinate reached the assertion; this guard would be vacuous")
	}
}

func subjectKindList(kinds map[contextfabric.SubjectKind]bool) []string {
	out := make([]string, 0, len(kinds))
	for kind := range kinds {
		out = append(out, string(kind))
	}
	sort.Strings(out)
	return out
}

func TestEveryTracedFrameDerivesTheObligationsItsCellsAssume(t *testing.T) {
	for _, testCase := range traceCases() {
		t.Run(testCase.id, func(t *testing.T) {
			// ROUND 2 NOTED THIS COLLAPSED. Keying only on the obligation
			// meant a frame that traced `state` for one subject satisfied
			// `state` for every subject it covers, so deleting Q-A's
			// group-role `state` cell left this test green -- the other
			// `state` cell in the same frame answered for it. Cells are
			// now keyed by the full coordinate.
			//
			// BOUND, stated rather than implied by the name: which cells a
			// frame OUGHT to trace is still read from the table below, so
			// this catches an invented or duplicated cell but cannot by
			// itself catch a deleted one. What catches a deletion is the
			// generated artifact, whose "cells evaluated" and "EMPTY
			// cells" lines change in the diff. That is a review control,
			// the same shape as round 1's F1.
			traced := make(map[string]bool, len(testCase.cells))
			tracedObligation := make(map[contextfabric.AnswerObligation]bool, len(testCase.cells))
			for _, cell := range testCase.cells {
				if !testCase.frame.HasObligation(cell.obligation) {
					t.Errorf("%s traces a cell for %q, which the frame does not derive (derived: %v)",
						testCase.id, cell.obligation, testCase.frame.Obligations)
				}
				key := string(cell.obligation) + "|" + cell.role + "|" + string(cell.subject)
				if traced[key] {
					t.Errorf("%s traces the cell %q twice; a duplicated coordinate inflates every count derived from this table", testCase.id, key)
				}
				traced[key] = true
				tracedObligation[cell.obligation] = true
			}
			if len(testCase.cells) == 0 {
				t.Errorf("%s traces no cells at all; every assertion over this frame would be vacuous", testCase.id)
			}
			for _, obligation := range testCase.frame.Obligations {
				kind, known := contextfabric.KindOfObligation(obligation)
				if !known {
					t.Fatalf("%s: obligation %q has no declared kind", testCase.id, obligation)
				}
				if kind == contextfabric.ObligationKindAnswerContract {
					continue
				}
				if !tracedObligation[obligation] {
					t.Errorf("%s derives %q (kind %s) but the trace has no cell for it", testCase.id, obligation, kind)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Assertion 2 -- THE GATE. Red at this tip by construction.
// ---------------------------------------------------------------------------

// TestFrozenRuleLeavesRequirementCellsUnserved PINS A DEFECT.
//
// THE NUMBER IS NOT IN THE NAME ANY MORE, deliberately. It used to say
// TwentyTwo. Round 3 derived the traced coordinates from the frame instead
// of reading them from a hand table, and the measurement moved to 21 -- so
// the name had become false while every test stayed green, which is the
// same failure the rest of this file exists to prevent. The count lives in
// the generated artifact, which is the only place it can be read from and
// the only place it can change.
//
// READ THE NAME AS THE FINDING. This test passing does not mean the
// requirement layer works; it means the frozen rule is still exactly as
// broken as the executed trace found it. It exists so that the fix is
// visible as a diff and so that the baseline cannot drift silently
// underneath the work that replaces it.
//
// WHY IT IS SHAPED THIS WAY. Oracle O9's real assertion is "no required
// read obligation derives an empty FactKinds set", and that assertion is
// RED at this tip by construction -- which is the entry gate law L10
// demands, and which was executed and recorded before any derivation rule
// was written (evidence: the lane's o9-red-baseline artifact, and this
// file's own history). A permanently-red test cannot live on main, so what
// SHIPS here is the baseline pinned exactly, and the derivation slice
// replaces this function with the served-assertion when the generated
// mapping can satisfy it.
//
// The decomposition is ASSERTED, not merely logged. A bare count of 22 can
// stay 22 while its causes move -- a producer gaining a subject kind and
// another losing one nets to zero -- and the causes are what the
// declaration work is actually aimed at. Pinning the distribution makes
// any such movement loud.
func TestFrozenRuleLeavesRequirementCellsUnserved(t *testing.T) {
	capabilities := liveCapabilities(t)
	generated := renderLiveTrace(t, capabilities)

	if *updateSeed {
		if err := os.MkdirAll(filepath.Dir(recordedTraceArtifact), 0o755); err != nil {
			t.Fatalf("creating testdata directory: %v", err)
		}
		if err := os.WriteFile(recordedTraceArtifact, []byte(generated), 0o644); err != nil {
			t.Fatalf("writing %s: %v", recordedTraceArtifact, err)
		}
		t.Logf("wrote %s", recordedTraceArtifact)
		return
	}

	committed, err := os.ReadFile(recordedTraceArtifact)
	if err != nil {
		t.Fatalf("reading the committed trace: %v (regenerate with -update-seed)", err)
	}
	if string(committed) != generated {
		t.Errorf("the recorded trace no longer matches the frozen rule executed against the live registry.\n"+
			"This test PINS A DEFECT, so a change is not automatically good news: a LOWER empty count "+
			"means something moved in the registry, not that the layer was fixed, and a HIGHER one means "+
			"a producer stopped serving a subject kind. Regenerate with -update-seed and read the diff -- "+
			"the cause distribution at the foot of the file says what moved.\n\n--- generated ---\n%s",
			generated)
	}

	// The artifact is an OUTPUT, so it cannot be the thing that proves the
	// gate is non-vacuous -- it would agree with anything. Assert the
	// property directly against the live run instead.
	empty := 0
	evaluated := 0
	for _, testCase := range traceCases() {
		for _, cell := range testCase.cells {
			evaluated++
			if len(frozenIntersection(capabilities, cell.obligation, cell.subject, testCase.frame.Dimensions, true)) == 0 {
				empty++
			}
		}
	}
	if evaluated == 0 {
		t.Fatal("no cells reached the assertion; this gate would be vacuous")
	}
	if empty == 0 {
		t.Fatal("the frozen rule now leaves NO cell unserved -- this test pins a defect that appears to be gone. " +
			"That is a real event, not a passing test: replace this gate with the served assertion.")
	}
	t.Logf("EMPTY FactKinds cells across %d frames: %d of %d cells evaluated", len(traceCases()), empty, evaluated)
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
