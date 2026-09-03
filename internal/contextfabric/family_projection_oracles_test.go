package contextfabric

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Oracles O1, O2, O10 and O5 of design §13.11a -- the exact checks the
// slice gate table cannot make.
//
// Round 1 of the design review was right that a gate table of reachability
// and agreement RATES can pass while the composed cases still lose
// semantics: those are shape checks, not oracles. Each oracle below is a
// table test over closed vocabularies, red on origin/main by construction
// (the projection does not exist there).
//
// WHERE THE EXPECTATIONS COME FROM, said once for all four:
//
//   - O1's rows are TRANSCRIBED from design §13.4.2's own table -- a
//     document outside this package that was derived by hand three times
//     and re-derived by the finalizer. That is an external authority, which
//     is what an oracle needs. It is emphatically not "run the derivation
//     and write down what it said".
//   - O2's expectation is EQUALITY between two frames the test builds, so
//     there is no expected value to compute at all.
//   - O10's expectation is the shipped FAMILY REGISTRY's own SubjectAxis
//     and Budget columns, read for whatever family the projection returns.
//     The registry is a different declaration from the projection table.
//   - O5's expectation is IDENTITY with the un-widened frame's own results.

func obligationsString(obligations []AnswerObligation) string {
	out := make([]string, 0, len(obligations))
	for _, obligation := range obligations {
		out = append(out, string(obligation))
	}
	sort.Strings(out)
	return "{" + strings.Join(out, ", ") + "}"
}

func sameOperandKinds(got, want []SubjectOperandKind) bool {
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

func sameObligationSet(got, want []AnswerObligation) bool {
	if len(got) != len(want) {
		return false
	}
	gotSet := obligationSet(got)
	for _, obligation := range want {
		if !gotSet[obligation] {
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------
// O1 -- COMPOSITION
// -----------------------------------------------------------------------

// compositionRow is one row of design §13.4.2, transcribed.
type compositionRow struct {
	// id is the row's position in the design table. NEVER the question
	// text: the questions live in the design document, and a corpus-safe
	// test identifies a case by coordinate.
	id string
	// shape is a STRUCTURAL label naming the frame, not a paraphrase.
	shape       string
	goals       []InvestigationGoal
	expression  SubjectExpression
	temporal    TemporalIntent
	emphasis    []AnswerEmphasis
	dimensions  []HealthDimension
	wantFamily  QuestionFamily
	wantRow     FamilyProjectionRow
	obligations []AnswerObligation
}

func compositionRows() []compositionRow {
	repository := SubjectRepository
	grouped := SubjectExpression{Kind: SubjectExpressionGroupedMembers,
		Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectProject}}
	discovered := SubjectExpression{Kind: SubjectExpressionDiscoveredKind,
		Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam}}
	scoped := SubjectExpression{Kind: SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: SubjectTeam}}
	explicit := SubjectExpression{Kind: SubjectExpressionExplicitSet,
		Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
			projectionNamedOperand("a", SubjectTeam), projectionNamedOperand("b", SubjectTeam)}}}
	org := SubjectExpression{Kind: SubjectExpressionOrganizationScope,
		Org: &OrganizationScopeExpression{MemberKind: &repository}}

	return []compositionRow{
		{
			id: "C1", shape: "grouped team->project, assess + trend, time_series",
			goals:      []InvestigationGoal{GoalAssessState, GoalDescribeTrend},
			expression: grouped, temporal: TemporalIntentTimeSeries,
			wantFamily: QuestionFamilyGroupedCohortStatus, wantRow: FamilyProjectionRowGrouped,
			// health NOW derives, which round 2's row-1 finding said it did
			// not; the goal SET is what lets "how has health changed" name
			// both operations. trend_series is O1's named requirement here.
			obligations: []AnswerObligation{ObligationState, ObligationHealth, ObligationTrendSeries, ObligationEvidence, ObligationCoverage},
		},
		{
			id: "C2", shape: "explicit_set two named team operands, compare, dims=investment_balance",
			goals:      []InvestigationGoal{GoalCompare},
			expression: explicit, temporal: TemporalIntentCurrent,
			dimensions: []HealthDimension{HealthDimensionInvestmentBalance},
			wantFamily: QuestionFamilyExplicitComparison, wantRow: FamilyProjectionRowExplicit,
			obligations: []AnswerObligation{ObligationState, ObligationEvidence, ObligationCoverage, ObligationAllocationBreakdown},
		},
		{
			id: "C3", shape: "grouped team->project, explain_change, period_comparison, dims=delivery_flow",
			goals:      []InvestigationGoal{GoalExplainChange},
			expression: grouped, temporal: TemporalIntentPeriodComparison,
			dimensions: []HealthDimension{HealthDimensionDeliveryFlow},
			wantFamily: QuestionFamilyGroupedCohortStatus, wantRow: FamilyProjectionRowGrouped,
			// O1 names this row: it must contain BOTH principal_drivers and
			// period_delta. explain_change is a drivers question ABOUT a
			// change, so dropping either half loses one of the two
			// operations the question composes.
			obligations: []AnswerObligation{ObligationState, ObligationPrincipalDrivers, ObligationPeriodDelta, ObligationEvidence, ObligationCoverage},
		},
		{
			id: "C4", shape: "discovered team, rank, both-ends emphasis",
			goals:      []InvestigationGoal{GoalRankOrSurvey},
			expression: discovered, temporal: TemporalIntentCurrent,
			emphasis:   []AnswerEmphasis{EmphasisPositiveOutliers, EmphasisNegativeOutliers},
			wantFamily: QuestionFamilyDiscoveredCohortRanking, wantRow: FamilyProjectionRowDiscovered,
			obligations: []AnswerObligation{ObligationRanking, ObligationState, ObligationHealth, ObligationEvidence, ObligationCoverage},
		},
		{
			id: "C5", shape: "scoped repository->team, count",
			goals:      []InvestigationGoal{GoalCountOrAggregate},
			expression: scoped, temporal: TemporalIntentCurrent,
			wantFamily: QuestionFamilyScopedCohortStatus, wantRow: FamilyProjectionRowScoped,
			// O1 names this row TWICE: it must be exactly {count, evidence,
			// coverage} and it MUST NOT contain health. health now comes
			// only from the three state-ish goals, never from an
			// empty-Dimensions special case -- the rule law L1 killed.
			obligations: []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage},
		},
		{
			id: "C6", shape: "explicit_set two named team operands, compare + trend, time_series, dims=investment_balance",
			goals:      []InvestigationGoal{GoalCompare, GoalDescribeTrend},
			expression: explicit, temporal: TemporalIntentTimeSeries,
			dimensions: []HealthDimension{HealthDimensionInvestmentBalance},
			wantFamily: QuestionFamilyExplicitComparison, wantRow: FamilyProjectionRowExplicit,
			// O1's first named row: all three operations survive AT THE
			// OBLIGATION LEVEL. Whether any producer can SERVE them for
			// team operands is a different question, and the answer today
			// is no -- which is why this asserts obligations, not service.
			obligations: []AnswerObligation{ObligationState, ObligationTrendSeries, ObligationAllocationBreakdown, ObligationEvidence, ObligationCoverage},
		},
		{
			id: "Q2", shape: "discovered team, rank + explain_drivers (chris's governing question / BAR Q2)",
			goals:      []InvestigationGoal{GoalRankOrSurvey, GoalExplainDrivers},
			expression: discovered, temporal: TemporalIntentCurrent,
			wantFamily: QuestionFamilyDiscoveredCohortRanking, wantRow: FamilyProjectionRowDiscovered,
			// BOTH operations REQUIRED, neither advisory. Unrepresentable
			// under the singular-goal design (round-2 P1-5): no single goal
			// made both ranking and principal_drivers required, so the
			// governing acceptance question had no legal frame at all.
			obligations: []AnswerObligation{ObligationRanking, ObligationState, ObligationHealth, ObligationPrincipalDrivers, ObligationEvidence, ObligationCoverage},
		},
		{
			id: "C7", shape: "organization scope, count, MemberKind=repository",
			goals:      []InvestigationGoal{GoalCountOrAggregate},
			expression: org, temporal: TemporalIntentCurrent,
			wantFamily: QuestionFamilySubjectInvestigation, wantRow: FamilyProjectionRowSubject,
			obligations: []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage},
		},
		{
			id: "B7", shape: "(structural) count over a discovered kind -- the DECLARED loss",
			goals:      []InvestigationGoal{GoalCountOrAggregate},
			expression: discovered, temporal: TemporalIntentCurrent,
			// The family NAME says ranking while the frame derives no
			// ranking obligation. The eight-member vocabulary has no count
			// member, so a count question must project onto a topology
			// family and the name necessarily overstates. Declared, not
			// repaired -- and asserted here so the declaration is a fact
			// about the code rather than a paragraph.
			wantFamily: QuestionFamilyDiscoveredCohortRanking, wantRow: FamilyProjectionRowDiscovered,
			obligations: []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage},
		},
	}
}

// TestO1CompositionDerivesTheExactFamilyAndObligationSet is oracle O1.
//
// EXACT sets, not subsets, on both axes. A subset assertion cannot see an
// obligation that should not be there, and "case 5 must not contain health"
// is one of O1's own named requirements -- a requirement a subset check is
// structurally unable to make.
func TestO1CompositionDerivesTheExactFamilyAndObligationSet(t *testing.T) {
	t.Parallel()
	rows := compositionRows()
	if len(rows) != 9 {
		t.Fatalf("design §13.4.2 has nine rows; this transcription has %d", len(rows))
	}
	for _, row := range rows {
		t.Run(row.id, func(t *testing.T) {
			t.Parallel()
			result := ValidateFrame(QuestionFrame{
				Goals:             row.goals,
				SubjectExpression: row.expression,
				Temporal:          row.temporal,
				Emphasis:          row.emphasis,
				Dimensions:        row.dimensions,
			}, nil, "")
			if result.Outcome != FrameValidationOutcomeValid {
				t.Fatalf("%s (%s) does not VALIDATE: %+v -- a composed case the design lists as legal must be legal", row.id, row.shape, result.Failure)
			}

			projection := DeriveQuestionFamily(result.Frame)
			if projection.Family != row.wantFamily {
				t.Errorf("%s family = %q, want %q", row.id, projection.Family, row.wantFamily)
			}
			if projection.Row != row.wantRow {
				t.Errorf("%s fired row %q, want %q -- the right family via the wrong rule is a different decision", row.id, projection.Row, row.wantRow)
			}
			if !sameObligationSet(result.Frame.Obligations, row.obligations) {
				t.Errorf("%s obligations = %s, want EXACTLY %s", row.id, obligationsString(result.Frame.Obligations), obligationsString(row.obligations))
			}
		})
	}
}

// TestO1B7DerivedRankingIsFalseOnACountOverADiscoveredKind is the second
// half of the declared loss, and the half that makes it safe.
//
// The projection lands a count question on a family whose name says
// ranking. What keeps that honest is that the derived require_ranking for
// the plan is FALSE -- read from the frame's obligations, which carry no
// ranking, rather than from the family's registry row, which says true.
// This is behaviour change B7, and it is the concrete reason no new stage
// may read the family.
func TestO1B7DerivedRankingIsFalseOnACountOverADiscoveredKind(t *testing.T) {
	t.Parallel()
	result := ValidateFrame(QuestionFrame{
		Goals: []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam}},
		Temporal: TemporalIntentCurrent,
	}, nil, "")
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("the structural count-over-discovered-kind frame must be legal under I9: %+v", result.Failure)
	}
	projection := DeriveQuestionFamily(result.Frame)

	definition, ok := LookupQuestionFamily(projection.Family)
	if !ok {
		t.Fatalf("projected family %q has no registry row", projection.Family)
	}
	if !definition.RequireRanking {
		t.Fatalf("registry row for %q has RequireRanking=false, so this test can no longer show the divergence it exists to show", projection.Family)
	}
	if result.Frame.HasObligation(ObligationRanking) {
		t.Fatalf("the FRAME derives a ranking obligation for a pure count question -- the loss B7 declares is that it does not, and a plan reading the frame would now demand an ordering nobody asked for")
	}
}

// -----------------------------------------------------------------------
// O2 -- SEMANTIC TOTALITY OF THE DERIVATION (the F4 class)
// -----------------------------------------------------------------------

// TestO2TemporalNeverChangesTheDerivedFamily is oracle O2.
//
// "For every pair of frames that differ only in Temporal while Goals and
// SubjectExpression.Kind are equal, assert the derived family is EQUAL."
//
// THE PROPERTY WHOSE ABSENCE F4 EXPLOITED. The frozen trend row keyed on
// Temporal in {time_series, period_comparison}, so the legal frame
// {describe_trend, named_subject, bounded_window} missed the row and fell
// through to subject_investigation -- while the SAME intent expressed as
// time_series produced trend. Two permitted readings of one intent gave two
// families, which is exactly the instability this design exists to remove.
//
// EXHAUSTIVE, not sampled: the corpus is the whole grammar-legal space, and
// this groups it by (shape, goals, emphasis, dimensions) and asserts one
// family per group however the temporal varies. There is no expected value
// to compute -- the expectation is equality within the group.
func TestO2TemporalNeverChangesTheDerivedFamily(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "O2"}

	type observation struct {
		temporal TemporalIntent
		family   QuestionFamily
		frame    generatedFrame
	}
	groups := map[string][]observation{}
	for _, generated := range frames {
		key := fmt.Sprintf("%s|%v|%v|%v", generated.shape.name, generated.goals, generated.emphasis, generated.dimension)
		groups[key] = append(groups[key], observation{
			temporal: generated.frame.Temporal,
			family:   DeriveQuestionFamily(generated.frame).Family,
			frame:    generated,
		})
	}

	multiTemporalGroups := 0
	for key, observations := range groups {
		if len(observations) < 2 {
			reach.skip()
			continue
		}
		multiTemporalGroups++
		first := observations[0]
		for _, other := range observations[1:] {
			reach.reach()
			if other.family != first.family {
				t.Fatalf("O2 VIOLATED: group %s projects to TWO families across temporals\n  temporal=%s -> %s\n  temporal=%s -> %s\nOne intent with two permitted temporal readings must not route two ways.",
					key, first.temporal, first.family, other.temporal, other.family)
			}
		}
	}

	// A group of one has no pair to compare, so a corpus in which every
	// group is a singleton would pass this test having compared nothing.
	if multiTemporalGroups == 0 {
		t.Fatal("O2: no group carried more than one temporal, so no pair was ever compared -- the oracle proves nothing")
	}
	// The trend goal is the one F4 actually exploited. If it never reaches
	// a multi-temporal group, the oracle is green on the cases that never
	// broke.
	trendGroups := 0
	for _, observations := range groups {
		if len(observations) < 2 {
			continue
		}
		for _, goal := range observations[0].frame.goals {
			if goal == GoalDescribeTrend {
				trendGroups++
				break
			}
		}
	}
	if trendGroups == 0 {
		t.Error("O2: no multi-temporal group carries describe_trend -- the exact goal F4 exploited is untested")
	}
	reach.require(t, multiTemporalGroups)
	t.Logf("O2: %d multi-temporal groups compared, %d of them carrying describe_trend", multiTemporalGroups, trendGroups)
}

// -----------------------------------------------------------------------
// O10 -- TOPOLOGY-FIRST PROJECTION (round 4, N1)
// -----------------------------------------------------------------------

// cohortSubjectAxes are the registry SubjectAxis values that describe a SET
// of subjects. O10's assertion is that a cohort frame always lands on a
// family declaring one of these.
var cohortSubjectAxes = map[SubjectAxisKind]bool{
	SubjectAxisManyNamed:      true,
	SubjectAxisManyDiscovered: true,
	SubjectAxisManyScoped:     true,
	SubjectAxisManyGrouped:    true,
}

// cohortBudgetProfiles are the budget profiles sized for a SET of subjects.
//
// THIS LIST WAS WRONG ON ITS FIRST WRITING and the sweep caught it: it held
// flat_cohort and grouped_cohort only, and an explicit_set frame projected
// to explicit_comparison, whose declared profile is matched_pair. A matched
// pair IS a set -- two operands with evidence matched across them -- so the
// CODE was right and the property was too narrow. Recorded rather than
// quietly widened, because "the sweep failed so I edited the sweep" is how
// a property stops being one.
//
// The membership is stated positively AND checked against the closed
// vocabulary below, so a profile added later cannot land here by default:
// what O10 actually forbids is a cohort frame planned against
// single_subject, and a new profile that is neither cohort nor
// single-subject must be classified by whoever adds it, not absorbed.
var cohortBudgetProfiles = map[PlanBudgetProfile]bool{
	PlanBudgetFlatCohort:    true,
	PlanBudgetGroupedCohort: true,
	PlanBudgetMatchedPair:   true,
}

// nonCohortBudgetProfiles are the profiles a cohort frame must never be
// planned against. Together with cohortBudgetProfiles this partitions the
// closed vocabulary, and TestO10BudgetProfilePartitionIsTotal asserts the
// partition covers it -- so an unclassified new profile fails loudly rather
// than passing as "not forbidden".
var nonCohortBudgetProfiles = map[PlanBudgetProfile]bool{
	PlanBudgetSingleSubject: true,
	PlanBudgetUnbounded:     true,
}

// TestO10BudgetProfilePartitionIsTotal keeps O10's classification honest.
func TestO10BudgetProfilePartitionIsTotal(t *testing.T) {
	t.Parallel()
	for _, profile := range []PlanBudgetProfile{
		PlanBudgetSingleSubject, PlanBudgetFlatCohort, PlanBudgetGroupedCohort,
		PlanBudgetMatchedPair, PlanBudgetUnbounded,
	} {
		cohort, nonCohort := cohortBudgetProfiles[profile], nonCohortBudgetProfiles[profile]
		if cohort == nonCohort {
			t.Errorf("budget profile %q is classified %s by O10 -- every profile must be exactly one of cohort-sized or not, or O10 silently stops forbidding something", profile,
				map[bool]string{true: "as BOTH cohort and non-cohort", false: "as NEITHER cohort nor non-cohort"}[cohort])
		}
	}
}

// TestO10CohortFramesAlwaysProjectToACohortRegistryRow is oracle O10.
//
// "For every legal frame whose SubjectExpression.Kind is a cohort variant,
// assert the derived family's registry row has a cohort SubjectAxis and a
// cohort Budget profile, for EVERY goal set including {describe_trend} and
// {allocate_investment}."
//
// RED ON THE FROZEN TABLE, GREEN ON THE FINALIZED ONE. Under the frozen row
// order, "which teams' health is trending down?" -- discovered_kind plus
// describe_trend -- projected to `trend`, whose registry row is
// SubjectAxisOne with PlanBudgetSingleSubject (headroom 12, no cohort
// clamp) and whose ApplicableAxes include subject_handle and
// subject_candidate. That is the single-subject clarification garble
// removed for Q-B, reintroduced by the projection itself.
//
// The expectation is the REGISTRY's own columns, read for whatever family
// the projection returns. The registry is a separate declaration from the
// projection table, which is what makes this an oracle rather than the
// projection agreeing with itself.
func TestO10CohortFramesAlwaysProjectToACohortRegistryRow(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "O10"}
	goalsSeen := map[InvestigationGoal]int{}

	for _, generated := range frames {
		if !generated.frame.SubjectExpression.IsCohortVariant() {
			reach.skip()
			continue
		}
		reach.reach()
		for _, goal := range generated.frame.Goals {
			goalsSeen[goal]++
		}
		projection := DeriveQuestionFamily(generated.frame)
		definition, ok := LookupQuestionFamily(projection.Family)
		if !ok {
			t.Fatalf("O10: cohort frame %s projected to %q, which has NO registry row", generated, projection.Family)
		}
		if !cohortSubjectAxes[definition.SubjectAxis] {
			t.Fatalf("O10 VIOLATED: cohort frame %s projected to %q, whose registry SubjectAxis is %q -- a SET of subjects routed to a family that answers about one. This is the clarification garble N1 found.",
				generated, projection.Family, definition.SubjectAxis)
		}
		if !cohortBudgetProfiles[definition.Budget] {
			t.Fatalf("O10 VIOLATED: cohort frame %s projected to %q, whose registry Budget profile is %q -- a cohort planned against a single-subject budget has no cohort clamp",
				generated, projection.Family, definition.Budget)
		}
	}

	// The two goals N1 named EXPLICITLY must both have been exercised on a
	// cohort topology. They are the goals whose rows fired first under the
	// frozen table, so an O10 that never sees them is green on everything
	// except the case it was written for.
	for _, goal := range []InvestigationGoal{GoalDescribeTrend, GoalAllocateInvestment} {
		if goalsSeen[goal] == 0 {
			t.Errorf("O10: no legal cohort frame carries goal %q -- the exact case round 4's N1 found is untested", goal)
		}
	}
	reach.require(t, 1)
	t.Logf("O10: %d cohort frames checked against the family registry", reach.reached)
}

// -----------------------------------------------------------------------
// O5 -- WIDENING CANNOT CHANGE WHAT IS DECIDED (the F3 class)
// -----------------------------------------------------------------------

// TestO5AWidenedObligationChangesNothingThatDecides is oracle O5, at the
// altitude this slice can actually assert.
//
// O5 as the design states it ends at "the answer's completeness is
// IDENTICAL", and completeness derived from requirement OUTCOMES is S7c's.
// What is assertable HERE is the whole chain that precedes it: a spurious
// model-widened obligation must not change the projected family, must not
// change the REQUIRED obligation set, and must not change any requirement
// row -- so there is nothing left for it to change completeness THROUGH.
// The bound is stated rather than glossed: this proves the widening is
// inert on every decision this slice owns, not that S7c's completeness is
// unchanged, which is S7c's own oracle to write.
//
// §13.2.4's rule is what this makes falsifiable: widening is ADMITTED and
// every widened member becomes ADVISORY. A widening that could make a
// requirement required would be a model emission changing the plan, which
// is the narrowing power round-1 F3 took away.
func TestO5AWidenedObligationChangesNothingThatDecides(t *testing.T) {
	t.Parallel()
	seed, capabilities := fixtureSeed(), fixtureCapabilities()
	frames := generateFrames(t)
	reach := &reachCounter{name: "O5"}
	widenedSeen := 0

	for _, generated := range frames {
		// Rebuild the PROPOSAL so the widening goes in where a model's
		// would: as an input to validation, not bolted onto a validated
		// frame.
		proposal := generated.frame
		proposal.Obligations = nil
		proposal.WidenedObligations = nil
		proposal.Version = ""

		plain := ValidateFrame(proposal, nil, "")
		if plain.Outcome != FrameValidationOutcomeValid {
			reach.skip()
			continue
		}
		// A SPURIOUS ranking: the model asking for an ordering the frame's
		// own goals never required.
		widened := ValidateFrame(proposal, []AnswerObligation{ObligationRanking}, "")
		if widened.Outcome != FrameValidationOutcomeValid {
			t.Fatalf("O5: adding an ADVISORY obligation made frame %s invalid -- a widening may never refuse a frame that was legal without it", generated)
		}
		reach.reach()
		if len(widened.Frame.WidenedObligations) > 0 {
			widenedSeen++
		}

		// Compared field by field rather than by struct equality: the
		// projection now carries a slice, and `==` on a struct containing
		// one does not compile. Every field is named explicitly so a field
		// added later fails to compile here rather than silently dropping
		// out of the comparison -- which is what a reflect.DeepEqual would
		// have hidden.
		got, want := DeriveQuestionFamily(widened.Frame), DeriveQuestionFamily(plain.Frame)
		if got.Family != want.Family || got.Row != want.Row || got.Topology != want.Topology ||
			!sameOperandKinds(got.OperandKinds, want.OperandKinds) {
			t.Fatalf("O5 VIOLATED: a widened obligation changed the projection for %s: %+v vs %+v -- model output must not reroute", generated, got, want)
		}
		if !sameObligationSet(widened.Frame.Obligations, plain.Frame.Obligations) {
			t.Fatalf("O5 VIOLATED: a widened obligation changed the REQUIRED set for %s: %s vs %s -- a widening is advisory and must not join the required set",
				generated, obligationsString(widened.Frame.Obligations), obligationsString(plain.Frame.Obligations))
		}

		plainRows := DeriveRequirements(plain.Frame, seed, capabilities)
		widenedRows := DeriveRequirements(widened.Frame, seed, capabilities)
		if len(plainRows) != len(widenedRows) {
			t.Fatalf("O5 VIOLATED: a widened obligation changed the requirement ROW COUNT for %s: %d vs %d", generated, len(widenedRows), len(plainRows))
		}
		for i := range plainRows {
			if plainRows[i].Served() != widenedRows[i].Served() {
				t.Fatalf("O5 VIOLATED: a widened obligation changed row %d's served state for %s", i, generated)
			}
		}
	}

	// The widening must actually have LANDED somewhere. If every frame
	// already derived `ranking`, the widened set would be empty on all of
	// them and this oracle would compare a frame with itself, which is the
	// "guard checked that a field was POPULATED, not that the input
	// STRESSED the property" failure recorded on this branch.
	if widenedSeen == 0 {
		t.Fatal("O5: the advisory obligation was absorbed into the derived set on EVERY frame, so no frame in this run actually carried a widening -- the oracle compared each frame with itself")
	}
	reach.require(t, len(frames))
	t.Logf("O5: %d frames compared, %d of them carrying a non-empty widened set", reach.reached, widenedSeen)
}

// -----------------------------------------------------------------------
// THE DECLARED LOSSES, ENUMERATED RATHER THAN EXHIBITED
// -----------------------------------------------------------------------

// declaredLoss is one (family, obligation) pair where the projection lands
// a frame on a family whose registry row does NOT require an operation the
// frame's own obligations DO require.
type declaredLoss struct {
	family     QuestionFamily
	obligation AnswerObligation
	why        string
}

// declaredLosses is EVERY such pair, and the sweep below asserts the set is
// exactly this and no larger.
//
// WHY AN ENUMERATION AND NOT AN EXAMPLE. The first version of this slice
// declared ONE loss -- a count over a discovered kind landing on a family
// whose name says ranking -- and asserted it with a test that exhibited
// that one case. Review found a SECOND, undeclared instance immediately: a
// legal explicit set with {rank_or_survey, explain_drivers} projects to
// explicit_comparison, whose registry row says neither ranking nor drivers
// is required, while the frame requires both.
//
// The narrow fix would have been to add a second example. That is the
// per-instance fix this branch's history is a catalogue of -- a class found
// at five successive depths because each fix closed the instance in front
// of it. So the property is stated over ALL of them instead: sweep every
// legal frame, collect every (family, obligation) mismatch, and assert the
// DISTINCT SET equals this table. A new one -- from a new goal, a new
// variant, or a registry edit -- fails here without anyone remembering to
// look for it.
//
// WHAT A ROW HERE MEANS, precisely: the family NAME and its registry flags
// understate the question. The OPERATION is not lost, because the frame's
// obligations carry it and the plan reads those. What a row records is that
// a stage reading `RequireRanking`/`RequireDrivers` off the family -- which
// the design forbids any NEW stage from doing, and which the compatibility
// contract schedules for removal -- would understate this question.
var declaredLosses = []declaredLoss{
	{
		family: QuestionFamilyDiscoveredCohortRanking, obligation: ObligationCount,
		why: "a count over a discovered kind: the family NAME says ranking and the frame derives none. The eight-member vocabulary has no count member, so a count question must project onto a topology family. This is behaviour change B7, and here the registry OVERSTATES rather than understates -- it is in this table because the sweep quantifies over both directions.",
	},
	{
		family: QuestionFamilyExplicitComparison, obligation: ObligationRanking,
		why: "an explicit set with a ranking goal. Nothing makes `compare` the only goal an explicit set may carry -- invariant I7 constrains `compare` to explicit sets, not explicit sets to `compare` -- so 'rank these two named teams' is legal and lands on a family whose row says ranking is not required.",
	},
	{
		family: QuestionFamilyExplicitComparison, obligation: ObligationPrincipalDrivers,
		why: "the same explicit set with an explain-drivers goal, for the same reason. Found by review as one finding with the row above; they are two table rows because they are two independent flags.",
	},
	{
		family: QuestionFamilyScopedCohortStatus, obligation: ObligationRanking,
		why: "a scoped cohort asked to be ranked or surveyed. Its registry row requires no ranking because a scope is not an ordering.",
	},
	{
		family: QuestionFamilyScopedCohortStatus, obligation: ObligationPrincipalDrivers,
		why: "a scoped cohort asked why. Drivers are attempted, never required, on this family's row.",
	},
	{
		family: QuestionFamilyGroupedCohortStatus, obligation: ObligationRanking,
		why: "a grouped cohort asked to be ranked. Ranking within groups is a later slice's question; the row does not require it.",
	},
	{
		family: QuestionFamilySubjectInvestigation, obligation: ObligationRanking,
		why: "a single subject with a survey goal -- degenerate (a population of one) but legal, and the row requires no ranking.",
	},
	{
		family: QuestionFamilySubjectInvestigation, obligation: ObligationPrincipalDrivers,
		why: "a why-phrased single-subject question. **REPORTED AND UNPROTECTED, not an accepted loss** -- this row previously claimed the never-a-bare-score bar was 'enforced by an acceptance case rather than by this column', and review showed that claim does not hold. Traced: the frame derives principal_drivers; the registry row sets RequireDrivers=false; the plan COPIES that false flag; nothing in the plan path or the synthesis validation requires a non-empty driver set; and the only acceptance case asserting the bar covers RENDER-SHAPE selection, not the plan. So no mechanism enforces the stated condition. The waiver is withdrawn and the gap is stated instead. What DOES now observe it: the B8 status shadow measures against the frame's obligations rather than the plan's flag, so a served answer with an empty required driver set is counted (616 of the 770 driver-requiring corpus frames sit on a family whose flag says drivers are not required). REPORTING is all this slice does; ENFORCING belongs to the slice that flips routing off the family -- see the reported-gaps note in the PR body.",
	},
	{
		family: QuestionFamilyTrend, obligation: ObligationRanking,
		why: "a single-subject trend question that also carries a survey goal; the trend row is reached first and requires no ranking.",
	},
	{
		family: QuestionFamilyTrend, obligation: ObligationPrincipalDrivers,
		why: "the same, with an explain-drivers goal.",
	},
	{
		family: QuestionFamilyInvestmentAllocation, obligation: ObligationRanking,
		why: "an investment question that also surveys; the investment row is reached first.",
	},
	{
		family: QuestionFamilyInvestmentAllocation, obligation: ObligationPrincipalDrivers,
		why: "the same, with an explain-drivers goal.",
	},
}

// TestEveryProjectionLossIsDeclared sweeps the corpus for mismatches
// between what a frame's obligations REQUIRE and what its projected
// family's registry row DECLARES, and asserts the distinct set is exactly
// the declared table.
//
// Both directions are swept. A family whose row requires an operation the
// frame does not derive OVERSTATES the question (B7's case); one whose row
// does not require an operation the frame derives UNDERSTATES it (the case
// review found). Sweeping one direction would have missed the other, which
// is how the first version of this property missed the finding.
func TestEveryProjectionLossIsDeclared(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "declared-loss sweep"}

	type key struct {
		family     QuestionFamily
		obligation AnswerObligation
	}
	declared := map[key]declaredLoss{}
	for _, loss := range declaredLosses {
		k := key{loss.family, loss.obligation}
		if _, dup := declared[k]; dup {
			t.Errorf("declaredLosses lists %s/%s twice", loss.family, loss.obligation)
		}
		declared[k] = loss
	}

	// The two registry flags that can disagree with an obligation.
	flags := []struct {
		obligation AnswerObligation
		declaredBy func(QuestionFamilyDefinition) bool
	}{
		{ObligationRanking, func(d QuestionFamilyDefinition) bool { return d.RequireRanking }},
		{ObligationPrincipalDrivers, func(d QuestionFamilyDefinition) bool { return d.RequireDrivers }},
		// B7's direction: the family's name/row promises a ranking the
		// frame never asked for. Detected as `count` present WITH
		// RequireRanking set, since that is the shape B7 describes.
		{ObligationCount, nil},
	}

	// THE SWEEP POPULATION IS BASE FRAMES PLUS THEIR LEGAL SINGLE-GOAL
	// EXTENSIONS, and that is not an embellishment -- it is what makes this
	// property able to see the defect that prompted it.
	//
	// The generated corpus is single-goal by construction. The frame review
	// found is {rank_or_survey, explain_drivers} on an explicit set: TWO
	// goals. A sweep over the base corpus alone would have been written to
	// close a finding it structurally could not observe, and would have
	// reported "all declared" while the reported frame sailed past. Four
	// rows of the declared table are likewise reachable only here.
	population := make([]generatedFrame, 0, len(frames)*2)
	population = append(population, frames...)
	for _, base := range frames {
		for _, extension := range extensionsOf(base) {
			if extension.axis != "goals" {
				continue
			}
			result := ValidateFrame(extension.frame, nil, "")
			if result.Outcome != FrameValidationOutcomeValid {
				continue
			}
			extended := base
			extended.frame = result.Frame
			extended.goals = result.Frame.Goals
			population = append(population, extended)
		}
	}
	multiGoal := 0
	for _, member := range population {
		if len(member.frame.Goals) > 1 {
			multiGoal++
		}
	}
	if multiGoal == 0 {
		t.Fatal("the sweep population contains NO multi-goal frame, so it cannot observe the class this property exists for")
	}

	found := map[key][]string{}
	for _, generated := range population {
		projection := DeriveQuestionFamily(generated.frame)
		definition, ok := LookupQuestionFamily(projection.Family)
		if !ok {
			t.Fatalf("projected family %q has no registry row", projection.Family)
		}
		reach.reach()
		for _, flag := range flags {
			has := generated.frame.HasObligation(flag.obligation)
			if flag.declaredBy == nil {
				// B7 direction: the frame derives `count` and the family
				// nonetheless declares a ranking requirement.
				if has && definition.RequireRanking {
					k := key{projection.Family, flag.obligation}
					found[k] = append(found[k], generated.String())
				}
				continue
			}
			if has && !flag.declaredBy(definition) {
				k := key{projection.Family, flag.obligation}
				found[k] = append(found[k], generated.String())
			}
		}
	}

	for k, witnesses := range found {
		if _, isDeclared := declared[k]; !isDeclared {
			t.Errorf("UNDECLARED projection loss: a frame requiring %q projects to %q, whose registry row does not.\n  %d witnesses, first: %s\n  Either declare it in declaredLosses with the reason it is acceptable, or the projection is routing this question to the wrong family.",
				k.obligation, k.family, len(witnesses), witnesses[0])
		}
	}
	for k, loss := range declared {
		if len(found[k]) == 0 {
			t.Errorf("declaredLosses claims %s/%s occurs, but NO frame in the corpus exhibits it -- a declared loss nothing reaches is a claim nobody can check.\n  It says: %s", k.family, k.obligation, loss.why)
		}
	}
	reach.require(t, len(frames))
	t.Logf("declared-loss sweep: %d frames (%d multi-goal), %d distinct (family, obligation) losses, all declared", reach.reached, multiGoal, len(found))
}

// -----------------------------------------------------------------------
// THE SUBJECT-AXIS SWEEP — every topology loss, enumerated
// -----------------------------------------------------------------------

// topologyLoss is one (family, condition) pair where the projected family's
// registry SubjectAxis misdescribes how many subjects the frame names, or
// how they were obtained.
type topologyLoss struct {
	family QuestionFamily
	// condition names the frame-side property that makes the axis wrong.
	condition string
	why       string
}

// declaredTopologyLosses is EVERY such pair. The sweep below asserts the
// distinct set is exactly this and no larger.
//
// THIS TABLE REPLACED A SINGLE BOOLEAN CONSTANT, and the replacement is the
// finding. The first version declared ONE topology loss -- an explicit set
// of scoped operands landing on a many_named family -- as a named constant.
// The very next review round found a SECOND instance of the same class in a
// different variant: a `named_subject` expression naming TWO subjects
// projects to a single-subject family with a single-subject budget, and
// neither the operand-shape check (which reads the explicit set's inner
// union) nor O10 (which quantifies over IsCohortVariant, excluding named
// expressions by construction) could see it.
//
// A second constant would have been the per-instance fix for the third
// instance to find. The property is now stated over the thing all of them
// have in common: **what the registry axis says about subject multiplicity
// versus what the frame's expression actually names.**
//
// None of these is re-routed. Design row order keys on the discriminator,
// and changing which family a question reaches is a routing decision that
// belongs to the slice performing the flip. What this slice owes is that
// every one is visible and counted rather than found by the next reviewer.
var declaredTopologyLosses = []topologyLoss{
	{
		family: QuestionFamilyExplicitComparison, condition: "scoped operand under a many_named axis",
		why: "an explicit set may contain SCOPED operands -- that is how 'team A's projects versus team B's projects' is expressed -- but the family declares many_named, a subject fact-role and a matched-pair budget. Two populations planned as two named subjects under-authorizes the reads both need.",
	},
	{
		family: QuestionFamilySubjectInvestigation, condition: "multiple named subjects under a single-subject axis",
		why: "`named_subject` is defined as ONE OR MORE directly named subjects and its invariant requires only non-blank terms, so a two-term expression is legal and is two subjects. It lands on SubjectAxisOne with a single-subject budget and single-subject clarification axes.",
	},
	{
		family: QuestionFamilyTrend, condition: "multiple named subjects under a single-subject axis",
		why: "the same multi-term named expression carrying a trend goal reaches the trend row first; that row is also single-subject.",
	},
	{
		family: QuestionFamilyInvestmentAllocation, condition: "multiple named subjects under a single-subject axis",
		why: "the same, with an investment goal reaching the investment row first.",
	},
}

// TestEveryTopologyLossIsDeclared sweeps the corpus for frames whose
// projected family's registry axis misdescribes the subject multiplicity
// the expression actually names.
//
// The predicate is deliberately about MULTIPLICITY rather than about any
// one variant's internals, because the two instances review found were in
// two different variants and a per-variant check missed each other's.
func TestEveryTopologyLossIsDeclared(t *testing.T) {
	t.Parallel()
	frames := generateFramesWithMultiTermNamedSubjects(t)
	reach := &reachCounter{name: "topology-loss sweep"}

	type key struct {
		family    QuestionFamily
		condition string
	}
	declared := map[key]topologyLoss{}
	for _, loss := range declaredTopologyLosses {
		declared[key{loss.family, loss.condition}] = loss
	}

	found := map[key][]string{}
	for _, generated := range frames {
		projection := DeriveQuestionFamily(generated.frame)
		definition, ok := LookupQuestionFamily(projection.Family)
		if !ok {
			t.Fatalf("projected family %q has no registry row", projection.Family)
		}
		reach.reach()

		// A single-subject axis against an expression naming more than one.
		if definition.SubjectAxis == SubjectAxisOne && projection.NamedSubjectCount > 1 {
			k := key{projection.Family, "multiple named subjects under a single-subject axis"}
			found[k] = append(found[k], generated.String())
		}
		// A many_named axis against an operand that names a SCOPE.
		if definition.SubjectAxis == SubjectAxisManyNamed {
			for _, kind := range projection.OperandKinds {
				if kind == SubjectOperandScoped {
					k := key{projection.Family, "scoped operand under a many_named axis"}
					found[k] = append(found[k], generated.String())
					break
				}
			}
		}
	}

	for k, witnesses := range found {
		if _, isDeclared := declared[k]; !isDeclared {
			t.Errorf("UNDECLARED topology loss: %q frames project to %q, whose registry axis misdescribes them.\n  %d witnesses, first: %s\n  Either declare it with the reason it is acceptable, or the projection routes this question to the wrong family.",
				k.condition, k.family, len(witnesses), witnesses[0])
		}
	}
	for k, loss := range declared {
		if len(found[k]) == 0 {
			t.Errorf("declaredTopologyLosses claims %q / %q occurs, but NO frame exhibits it -- a declared loss nothing reaches is a claim nobody can check.\n  It says: %s", k.family, k.condition, loss.why)
		}
	}
	reach.require(t, len(frames))
	t.Logf("topology-loss sweep: %d frames, %d distinct topology losses, all declared", reach.reached, len(found))
}

// generateFramesWithMultiTermNamedSubjects is the corpus plus a multi-term
// named variant of every named frame.
//
// The base corpus builds named expressions with ONE term, so it cannot
// exhibit the multi-subject case at all -- the same blindness the
// declared-loss sweep had against multi-goal frames, in a different axis.
// A sweep that cannot reach the case it was written for is the failure this
// branch has now produced twice; it is guarded here by a count that must be
// non-zero.
func generateFramesWithMultiTermNamedSubjects(t *testing.T) []generatedFrame {
	t.Helper()
	base := generateFrames(t)
	out := make([]generatedFrame, 0, len(base)*2)
	out = append(out, base...)
	multi := 0
	for _, generated := range base {
		if generated.frame.SubjectExpression.Kind != SubjectExpressionNamed || generated.frame.SubjectExpression.Named == nil {
			continue
		}
		widened := generated.frame
		named := *generated.frame.SubjectExpression.Named
		named.Terms = []string{"team-a", "team-b"}
		expression := generated.frame.SubjectExpression
		expression.Named = &named
		widened.SubjectExpression = expression

		result := ValidateFrame(widened, nil, "")
		if result.Outcome != FrameValidationOutcomeValid {
			continue
		}
		extended := generated
		extended.frame = result.Frame
		out = append(out, extended)
		multi++
	}
	if multi == 0 {
		t.Fatal("no multi-term named frame validated, so the topology sweep cannot exhibit the multi-subject case it exists for")
	}
	return out
}

// -----------------------------------------------------------------------
// O10, STRENGTHENED — a cohort axis is not enough
// -----------------------------------------------------------------------

// TestCohortAxisMatchesTheActualOperandShape closes the hole review found
// in O10.
//
// O10 asserts a cohort frame lands on a family with SOME cohort
// SubjectAxis. `many_named` is one, so O10 accepted an explicit set of two
// SCOPED populations landing on a family that declares two NAMED subjects,
// a subject fact-role and a matched-pair budget. The oracle written to
// catch topology mismatches was blind to a topology mismatch, because its
// predicate was "is it a cohort" rather than "is it THIS cohort".
//
// The axis has to match the shape the frame actually carries:
// `many_named` means every operand names a subject. An operand that names a
// SCOPE describes a population, and a plan built on a matched-pair budget
// under-authorizes the reads both populations need.
//
// The projection is NOT changed to route these elsewhere -- design row 3
// keys on the discriminator, and re-routing is a decision this slice has no
// standing to make. The mismatch is DECLARED below and asserted here, so it
// is a known, measured loss rather than an invisible one.
func TestCohortAxisMatchesTheActualOperandShape(t *testing.T) {
	t.Parallel()
	frames := generateFrames(t)
	reach := &reachCounter{name: "operand-shape axis check"}
	scopedOperandFrames := 0

	for _, generated := range frames {
		projection := DeriveQuestionFamily(generated.frame)
		definition, ok := LookupQuestionFamily(projection.Family)
		if !ok {
			t.Fatalf("projected family %q has no registry row", projection.Family)
		}
		if definition.SubjectAxis != SubjectAxisManyNamed {
			reach.skip()
			continue
		}
		reach.reach()

		carriesScoped := false
		for _, kind := range projection.OperandKinds {
			if kind == SubjectOperandScoped {
				carriesScoped = true
			}
		}
		if !carriesScoped {
			continue
		}
		scopedOperandFrames++
		if !declaredTopologyLossScopedOperandsAsNamedPair {
			t.Fatalf("a frame with a SCOPED operand projects to %q, whose registry axis is many_named -- two populations planned as two named subjects (%s)",
				projection.Family, generated)
		}
	}

	// NON-VACUITY. If no frame in the corpus carries a scoped operand under
	// a many_named family, this check passed without ever reaching the case
	// it exists for -- which is exactly how the original O10 read green.
	if scopedOperandFrames == 0 {
		t.Fatal("no frame exercised the scoped-operand-under-many_named case; this check proves nothing")
	}
	reach.require(t, 1)
	t.Logf("operand-shape axis check: %d many_named frames, %d of them carrying a scoped operand (the declared topology loss)", reach.reached, scopedOperandFrames)
}

// declaredTopologyLossScopedOperandsAsNamedPair records the ONE topology
// loss this slice declares, as a named constant rather than a silent
// tolerance in a condition.
//
// An explicit set containing a scoped operand projects to
// `explicit_comparison`, whose registry row declares `many_named`,
// `FactRoleSubject` and a matched-pair budget. The frame describes two
// POPULATIONS; the family describes two SUBJECTS.
//
// REPORTED, NOT FIXED, and the boundary is deliberate: correcting it means
// either a ninth family or re-routing a design-normative row, and both are
// routing decisions that belong to the slice that performs the flip. What
// this slice owes is that the loss is visible and counted rather than
// discovered by the next reviewer. Setting this to false turns the check
// above into an assertion that the loss does not exist, which is what the
// mutation harness uses to prove the check can fail.
const declaredTopologyLossScopedOperandsAsNamedPair = true
