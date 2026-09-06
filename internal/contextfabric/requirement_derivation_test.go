package contextfabric

// Tests for the obligation -> requirement derivation against a CONSTRUCTED
// registry.
//
// WHY A FIXTURE REGISTRY HERE AND THE LIVE ONE IN THE GATE. The gate
// (requirement_trace_test.go) measures what the real producers can serve
// and is a MEASUREMENT: its numbers move when a declaration moves, and that
// is the point. These tests pin the RULES, which must not move when a
// declaration does -- so they need inputs they control.
//
// EVERY UNAVAILABLE TIER GETS A POSITIVE FIXTURE, and that is the standing
// rule rather than a nicety: "a gate tier with no positive fixture can be
// dead for its whole life and read as green -- 0 enforced is
// indistinguishable from the enforcement check always answering no". The
// classifier has three tiers and each one is landed in below, by
// construction, so none of them can be dead code that reads as a healthy
// zero in the live distribution.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The fixture registry. Three capabilities, arranged so that a single
// frame lands one cell in each unavailable tier plus one served cell.
//
//	SubjectTeam       -- supported, `state` declared        => SERVED
//	SubjectProject    -- supported, `state` NOT declared    => no_declaring_producer
//	SubjectRepository -- supported, `trend_series` declared
//	                     but no time_series table there     => table_shape_undeclared
//	SubjectWorkItem   -- supported by nobody                => subject_kind_unsupported
func fixtureCapabilities() []FactCapability {
	return []FactCapability{
		{
			Kind:                  FactHealth,
			SupportedSubjectKinds: []SubjectKind{SubjectTeam, SubjectProject, SubjectRepository},
			Obligations: map[SubjectKind][]AnswerObligation{
				SubjectTeam:       {ObligationState, ObligationHealth},
				SubjectRepository: {ObligationTrendSeries},
			},
			Tables: map[SubjectKind][]FactTableShape{
				// Deliberately NOT a time_series for the repository: that
				// is what puts the trend cell in the shape tier rather
				// than in the declaration tier.
				SubjectRepository: {FactTableBreakdown},
			},
		},
		{
			Kind:                  FactFlow,
			SupportedSubjectKinds: []SubjectKind{SubjectTeam, SubjectProject},
			Obligations: map[SubjectKind][]AnswerObligation{
				SubjectTeam: {ObligationState},
			},
		},
		{
			Kind:                  FactWorkload,
			SupportedSubjectKinds: []SubjectKind{SubjectTeam},
			Obligations:           map[SubjectKind][]AnswerObligation{},
		},
	}
}

func fixtureSeed() ObligationSeed {
	return GenerateObligationSeed(fixtureCapabilities())
}

func requirementFor(t *testing.T, rows []DerivedRequirement, obligation AnswerObligation, subject SubjectKind) DerivedRequirement {
	t.Helper()
	for _, row := range rows {
		if row.Obligation == obligation && row.Subject == subject {
			return row
		}
	}
	t.Fatalf("no requirement row for %s@%s among %d rows", obligation, subject, len(rows))
	return DerivedRequirement{}
}

// TestEveryUnavailableReasonHasAPositiveFixture lands one cell in each tier
// of the classifier.
//
// The three reasons are actionable by DIFFERENT parties -- a new producer,
// a declaration change, a query change -- so collapsing any two would
// produce the bare count §13.15.1 warns hands the next reader a number they
// cannot act on. This is the test that proves each tier is reachable.
func TestEveryUnavailableReasonHasAPositiveFixture(t *testing.T) {
	// Each reason is landed in through the PRODUCTION path -- a real frame
	// derived into real rows -- rather than by calling the classifier
	// helper. That matters for `computed_population_absent`, which the
	// classifier never produces: it is decided in deriveRequirement, and a
	// helper-only fixture could not reach it. Exercising the production
	// path is also the stronger test for the other three.
	cases := map[RequirementUnavailableReason]struct {
		goals      []InvestigationGoal
		expression SubjectExpression
	}{
		RequirementReasonSubjectKindUnsupported: {
			[]InvestigationGoal{GoalAssessState}, discoveredExpression(SubjectWorkItem)},
		RequirementReasonNoDeclaringProducer: {
			[]InvestigationGoal{GoalAssessState}, discoveredExpression(SubjectProject)},
		RequirementReasonTableShapeUndeclared: {
			[]InvestigationGoal{GoalDescribeTrend}, discoveredExpression(SubjectRepository)},
		RequirementReasonComputedPopulationAbsent: {
			[]InvestigationGoal{GoalRankOrSurvey}, orgExpression(nil)},
	}

	checked := 0
	for _, reason := range RequirementUnavailableReasonVocabulary() {
		testCase, built := cases[reason]
		if !built {
			t.Errorf("reason %q has no positive fixture: a tier with no fixture that lands in it can be dead for its whole life and read as green", reason)
			continue
		}
		frame := frameWith(testCase.goals, testCase.expression, TemporalIntentCurrent, nil)
		rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())
		if len(rows) == 0 {
			t.Errorf("reason %q: its fixture frame derived no rows at all", reason)
			continue
		}
		landed := false
		for _, row := range rows {
			if row.Unavailable == reason {
				landed = true
				break
			}
		}
		if !landed {
			t.Errorf("reason %q: its fixture frame derived %d rows and none carried it", reason, len(rows))
		}
		checked++
	}
	if checked != RequirementUnavailableReasonCount {
		t.Fatalf("exercised %d of %d reason tiers", checked, RequirementUnavailableReasonCount)
	}
}

// TestServedReadRequirementNamesItsFactKinds is the other half: the
// classifier's tiers are only meaningful if a served cell is distinguishable
// from an unserved one.
func TestServedReadRequirementNamesItsFactKinds(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectTeam), TemporalIntentCurrent, nil)
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())

	row := requirementFor(t, rows, ObligationState, SubjectTeam)
	if !row.Served() {
		t.Fatalf("state@team is unavailable (%s) though two fixture capabilities declare it", row.Unavailable)
	}
	if len(row.FactKinds) != 2 {
		t.Errorf("state@team names %v, want both declaring kinds", row.FactKinds)
	}
	if row.Quantifier != CompletionQuantifierCorroborated {
		t.Errorf("state@team quantifier is %q; two distinct serving kinds means corroboration is AVAILABLE and law L3 derives it from the measured cardinality", row.Quantifier)
	}
	if row.Scope != CompletionScopeSingleSubject {
		t.Errorf("named subject derived scope %q, want single_subject", row.Scope)
	}
}

// TestQuantifierTracksTheMeasuredCardinality is law L3 made executable, and
// it is the fix for the frozen `state = corroborated` constant whose escape
// clause silently degraded it to at_least_one wherever it bit.
//
// The property is directional: corroboration is demanded exactly where
// corroboration is available, never asserted and then quietly lowered.
func TestQuantifierTracksTheMeasuredCardinality(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectTeam), TemporalIntentCurrent, nil)
	seed := fixtureSeed()
	rows := DeriveRequirements(frame, seed, fixtureCapabilities())

	checked := 0
	for _, row := range rows {
		if row.Kind != ObligationKindRead || !row.Served() {
			continue
		}
		cardinality := seed.Cardinality(row.Obligation, row.Subject)
		want := CompletionQuantifierAtLeastOne
		if cardinality >= 2 {
			want = CompletionQuantifierCorroborated
		}
		if row.Quantifier != want {
			t.Errorf("%s@%s: cardinality %d derived quantifier %q, want %q", row.Obligation, row.Subject, cardinality, row.Quantifier, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no served read requirement reached the assertion -- the property proved nothing")
	}
	// Both arms must be exercised, or the rule is only half tested.
	sawOne, sawMany := false, false
	for _, row := range rows {
		switch row.Quantifier {
		case CompletionQuantifierAtLeastOne:
			sawOne = true
		case CompletionQuantifierCorroborated:
			sawMany = true
		}
	}
	if !sawOne || !sawMany {
		t.Errorf("the fixture exercised at_least_one=%v corroborated=%v; both arms are needed for the rule to be tested", sawOne, sawMany)
	}
}

// TestComputedRequirementNamesItsServerStep: `ranking` and `count` read no
// fact kind of their own. The frozen table modelled `ranking` as a read
// with a required table shape no producer declares, which made BAR question
// Q2's DEFINING obligation unavailable by construction (round 4, N3).
func TestComputedRequirementNamesItsServerStep(t *testing.T) {
	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey, GoalCountOrAggregate},
		discoveredExpression(SubjectTeam),
		TemporalIntentCurrent,
		nil,
	)
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())

	wantSteps := map[AnswerObligation]ComputedObligationStep{
		ObligationRanking: ComputedStepRankCohort,
		ObligationCount:   ComputedStepMembershipCardinality,
	}
	checked := 0
	for obligation, wantStep := range wantSteps {
		row := requirementFor(t, rows, obligation, SubjectTeam)
		if !row.Served() {
			t.Errorf("computed obligation %s is unavailable (%s); a computed obligation is unavailable only when its INPUTS are, never for want of a producer", obligation, row.Unavailable)
		}
		if row.Step != wantStep {
			t.Errorf("%s names step %q, want %q", obligation, row.Step, wantStep)
		}
		if len(row.FactKinds) != 0 {
			t.Errorf("%s names fact kinds %v; a computed obligation reads none of its own", obligation, row.FactKinds)
		}
		checked++
	}
	if checked != len(wantSteps) {
		t.Fatalf("checked %d computed obligations, want %d", checked, len(wantSteps))
	}
}

// TestRowsAreWellFormed is the "never silently empty" invariant §13.15's
// green condition names, checked over every row the fixture produces.
func TestRowsAreWellFormed(t *testing.T) {
	frame := frameWith(
		[]InvestigationGoal{GoalAssessState, GoalRankOrSurvey, GoalDescribeTrend},
		groupedExpression(SubjectProject, SubjectTeam),
		TemporalIntentTimeSeries,
		nil,
	)
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())
	if len(rows) == 0 {
		t.Fatal("no rows derived; the well-formedness check below would be vacuous")
	}
	if err := RequirementRowsAreWellFormed(rows); err != nil {
		t.Fatalf("rows are not well formed: %v", err)
	}
	// The check must be able to FAIL, or its passing says nothing. A row
	// mutated into the forbidden state -- unavailable, yet carrying a
	// quantifier that reads as satisfiable -- must be rejected by name.
	broken := append([]DerivedRequirement(nil), rows...)
	broken = append(broken, DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleMember, Subject: SubjectWorkItem},
		Kind:                  ObligationKindRead,
		Scope:                 CompletionScopeEachMember,
		Quantifier:            CompletionQuantifierAtLeastOne,
		Unavailable:           RequirementReasonSubjectKindUnsupported,
	})
	err := RequirementRowsAreWellFormed(broken)
	if err == nil {
		t.Fatal("an unavailable row carrying a satisfiable quantifier passed the well-formedness check")
	}
	if !strings.Contains(err.Error(), "satisfiable") {
		t.Errorf("the rejection does not say what is wrong: %v", err)
	}
}

// TestUnservedRowsCarryNoServerAndNoQuantifier pins the pairing directly,
// over the live shape of the fixture, so a future edit that fills in a
// default quantifier on an unavailable row is caught here as well as by the
// well-formedness function.
func TestUnservedRowsCarryNoServerAndNoQuantifier(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalAssessState}, discoveredExpression(SubjectWorkItem), TemporalIntentCurrent, nil)
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())

	unserved := 0
	for _, row := range rows {
		if row.Served() {
			continue
		}
		unserved++
		if row.Quantifier != CompletionQuantifierNone {
			t.Errorf("%s@%s is unavailable but carries quantifier %q", row.Obligation, row.Subject, row.Quantifier)
		}
		if len(row.FactKinds) > 0 || row.Step != "" {
			t.Errorf("%s@%s is unavailable but names a server", row.Obligation, row.Subject)
		}
		if row.Unavailable == "" {
			t.Errorf("%s@%s is unserved with no reason token -- silently empty", row.Obligation, row.Subject)
		}
	}
	if unserved == 0 {
		t.Fatal("no unserved rows reached the assertions; work_item is supported by no fixture capability, so some were expected")
	}
}

// TestCauseDistributionDecomposesTheUnservedCells: §13.15.1's own lesson is
// that a bare count "would have handed the next reader a number they cannot
// act on". The distribution must account for every unserved cell exactly
// once.
func TestCauseDistributionDecomposesTheUnservedCells(t *testing.T) {
	frame := frameWith(
		[]InvestigationGoal{GoalAssessState, GoalDescribeTrend},
		groupedExpression(SubjectRepository, SubjectWorkItem),
		TemporalIntentTimeSeries,
		nil,
	)
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())

	unserved := 0
	for _, row := range rows {
		if !row.Served() {
			unserved++
		}
	}
	if unserved == 0 {
		t.Fatal("no unserved cells; the distribution assertion would be vacuous")
	}
	total := 0
	for _, line := range RequirementCauseDistribution(rows) {
		if line.Cells <= 0 {
			t.Errorf("distribution line %+v carries a non-positive count", line)
		}
		total += line.Cells
	}
	if total != unserved {
		t.Errorf("the distribution accounts for %d cells, but %d are unserved -- a cell is being lost or double-counted", total, unserved)
	}
}

// TestSummaryBalancesAndCarriesTheVersion guards the telemetry row's own
// accounting, and the distinction between "the derivation ran and found
// nothing" and "the derivation never ran".
func TestSummaryBalancesAndCarriesTheVersion(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectTeam), TemporalIntentCurrent, nil)
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())

	summary := RequirementDerivationSummaryFrom(rows)
	if !summary.Balanced() {
		t.Errorf("summary does not balance: %d derived, %d served, %d unserved", summary.Derived, summary.Served, summary.Unserved)
	}
	if summary.Version != RequirementDerivationVersion {
		t.Errorf("summary version %q, want %q", summary.Version, RequirementDerivationVersion)
	}
	if summary.Derived != len(rows) {
		t.Errorf("summary counts %d rows, %d were derived", summary.Derived, len(rows))
	}

	// An EMPTY but non-nil row set is a derivation that ran and required
	// nothing; the zero value is one that never ran. If those two were
	// indistinguishable, an operator could not tell a frame with no
	// requirements from a build with the deriver unwired.
	ran := RequirementDerivationSummaryFrom([]DerivedRequirement{})
	if ran.Version != RequirementDerivationVersion {
		t.Error("a derivation that ran and produced no rows is indistinguishable from one that never ran")
	}
	var neverRan RequirementDerivationSummary
	if neverRan.Version != "" {
		t.Error("the zero summary claims a version")
	}
}

// TestSummaryCountsEveryClosedTokenIncludingZeroes: the histogram arrays are
// indexed by vocabulary position, so a member added to a vocabulary without
// a corresponding array widening is a compile error rather than a silently
// dropped count. This asserts the indexing agrees with the vocabulary order,
// which is the assumption the log keys rest on.
func TestSummaryCountsEveryClosedTokenIncludingZeroes(t *testing.T) {
	rows := []DerivedRequirement{
		{
			RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleGroup, Subject: SubjectTeam},
			Kind:                  ObligationKindRead,
			Scope:                 CompletionScopeEachGroup,
			Quantifier:            CompletionQuantifierNone,
			Unavailable:           RequirementReasonTableShapeUndeclared,
		},
	}
	summary := RequirementDerivationSummaryFrom(rows)

	index, found := unavailableReasonIndex(RequirementReasonTableShapeUndeclared)
	if !found {
		t.Fatal("the reason token is not in its own vocabulary")
	}
	if summary.UnavailableCells[index] != 1 {
		t.Errorf("the table_shape_undeclared slot holds %d, want 1 -- the array index disagrees with the vocabulary order the log keys use", summary.UnavailableCells[index])
	}
	roleIndex, found := subjectRoleIndex(SubjectRoleGroup)
	if !found {
		t.Fatal("the group role is not in its own vocabulary")
	}
	if summary.Roles[roleIndex] != 1 {
		t.Errorf("the group-role slot holds %d, want 1", summary.Roles[roleIndex])
	}
	// Every other slot is an OBSERVED zero, not an absent key. That is the
	// property the log line depends on.
	zeroes := 0
	for position, count := range summary.UnavailableCells {
		if position != index && count == 0 {
			zeroes++
		}
	}
	if zeroes != RequirementUnavailableReasonCount-1 {
		t.Errorf("%d reason slots are zero, want %d", zeroes, RequirementUnavailableReasonCount-1)
	}
}

// TestComputedQuantifiersAreExactForCountAndAllForRanking pins a rule the
// tests read past, found by an adversarial review round.
//
// TestQuantifierTracksTheMeasuredCardinality deliberately SKIPS computed
// rows, and the computed-row test asserted only the server step, the served
// status and the absence of fact kinds. So nothing checked which quantifier a
// computed obligation gets: collapsing `quantifierForComputed` to always
// return `all` left the whole suite green once the artifact was regenerated,
// and the regeneration is exactly what a well-behaved contributor would do.
//
// The distinction is not cosmetic. A cardinality that is approximately right
// is wrong -- `exact` is the whole content of a count -- while an ordering
// must cover its population, which is `all`. Two obligations, two meanings,
// and the frozen design's mistake was carrying one constant for a rule that
// has two answers.
func TestComputedQuantifiersAreExactForCountAndAllForRanking(t *testing.T) {
	frame := frameWith(
		[]InvestigationGoal{GoalCountOrAggregate, GoalRankOrSurvey},
		discoveredExpression(SubjectTeam),
		TemporalIntentCurrent,
		nil,
	)
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())

	want := map[AnswerObligation]CompletionQuantifier{
		ObligationCount:   CompletionQuantifierExact,
		ObligationRanking: CompletionQuantifierAll,
	}
	checked := 0
	for obligation, quantifier := range want {
		row := requirementFor(t, rows, obligation, SubjectTeam)
		if row.Kind != ObligationKindComputed {
			t.Fatalf("%s is not classified computed, so this test is checking the wrong rule", obligation)
		}
		if row.Quantifier != quantifier {
			t.Errorf("%s derived quantifier %q, want %q", obligation, row.Quantifier, quantifier)
		}
		checked++
	}
	if checked != len(want) {
		t.Fatalf("checked %d computed obligations, want %d", checked, len(want))
	}
	// The two must DIFFER. A future edit collapsing both to one constant is
	// the mutation that survived; asserting them separately above would still
	// pass if the vocabulary itself were reduced to a single member.
	if want[ObligationCount] == want[ObligationRanking] {
		t.Fatal("the two expected quantifiers are equal, so this test cannot detect a collapse")
	}
}

// TestRankingTheOrganizationItselfIsUnavailable pins a real DERIVATION
// defect -- the first one a review round found in this layer rather than in
// its oracles.
//
// `organization_scope` with no member kind and the rank_or_survey goal
// derived `ranking / subject / organization` and reported `rank_cohort` as
// its server. That is a confident answer to an impossible question: ranking
// orders a member set, and the organization is the container, not the things
// in it. frame_vocab.go already said what should happen -- "a computed
// obligation is unavailable only when ITS INPUTS are" -- and the derivation
// did not implement it.
func TestRankingTheOrganizationItselfIsUnavailable(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalRankOrSurvey}, orgExpression(nil), TemporalIntentCurrent, nil)
	if !frame.HasObligation(ObligationRanking) {
		t.Fatalf("the frame derives no ranking obligation, so this test cannot see the rule: %v", frame.Obligations)
	}
	rows := DeriveRequirements(frame, fixtureSeed(), fixtureCapabilities())

	row := requirementFor(t, rows, ObligationRanking, SubjectOrganization)
	if row.Served() {
		t.Errorf("ranking the organization itself reports server %q; there is no population to order", row.Step)
	}
	if row.Unavailable != RequirementReasonComputedPopulationAbsent {
		t.Errorf("unavailable reason is %q, want %q", row.Unavailable, RequirementReasonComputedPopulationAbsent)
	}
	if row.Quantifier != CompletionQuantifierNone {
		t.Errorf("an unavailable row carries quantifier %q", row.Quantifier)
	}

	// SUPERSEDED ASSERTION, recorded rather than quietly rewritten.
	//
	// This block used to assert that `organization_scope` WITH a counted
	// member kind derives a SERVED ranking row named by rank_cohort. That
	// claim was false in the same way the block above it is about, one field
	// over: `organization_scope` is not a cohort variant, so
	// `IsCohortVariant` is false, so nothing discovers that population and
	// the engine resolves no cohort -- and `RankCohort` is invoked only when
	// a cohort exists. The row named a server that could never run.
	//
	// The incoherence was visible ON THIS VERY FIXTURE and is asserted below
	// rather than argued: `count` over the SAME frame was already unavailable
	// for exactly this reason, because the population guard read
	// `InputClass == resolved_member_set` -- which membership_cardinality
	// declares and rank_cohort (class `fact_kinds`) does not. Two computed
	// cells on one frame disagreed about whether that frame has a population,
	// and the disagreement was an artifact of which field the guard happened
	// to read.
	//
	// The counterpart's PURPOSE stands and is preserved below: the fix must
	// not collapse to "never rank". The honest control is a frame that
	// genuinely resolves a member set -- a cohort variant -- not an
	// organization scope that never did.
	kind := SubjectTeam
	orgWithMemberKind := DeriveRequirements(
		frameWith([]InvestigationGoal{GoalRankOrSurvey, GoalCountOrAggregate}, orgExpression(&kind), TemporalIntentCurrent, nil),
		fixtureSeed(), fixtureCapabilities())
	memberRow := requirementFor(t, orgWithMemberKind, ObligationRanking, SubjectTeam)
	if memberRow.Served() {
		t.Errorf("ranking a counted member kind under ORGANIZATION SCOPE reports server %q: that expression is not a cohort variant, so no member set is discovered and RankCohort is never invoked", memberRow.Step)
	}
	if memberRow.Unavailable != RequirementReasonComputedPopulationAbsent {
		t.Errorf("unavailable reason is %q, want %q", memberRow.Unavailable, RequirementReasonComputedPopulationAbsent)
	}
	// The parity statement, executed: the two computed cells on ONE frame
	// must agree about whether that frame has a population. This is the
	// assertion that makes the supersession above a correction rather than
	// an opinion.
	countRow := requirementFor(t, orgWithMemberKind, ObligationCount, SubjectTeam)
	if countRow.Served() != memberRow.Served() {
		t.Errorf("on one organization-scope frame `count` is served=%v and `ranking` is served=%v -- two computed cells disagree about whether the frame resolves a population (count unavailable=%q, ranking unavailable=%q)",
			countRow.Served(), memberRow.Served(), countRow.Unavailable, memberRow.Unavailable)
	}

	// THE COUNTERPART THAT MUST STILL WORK, on a frame that really does
	// resolve a member set. Without it the fix above could be "never rank",
	// which would be a different defect.
	cohortServed := DeriveRequirements(
		frameWith([]InvestigationGoal{GoalRankOrSurvey}, discoveredExpression(SubjectTeam), TemporalIntentCurrent, nil),
		fixtureSeed(), fixtureCapabilities())
	cohortRow := requirementFor(t, cohortServed, ObligationRanking, SubjectTeam)
	if !cohortRow.Served() || cohortRow.Step != ComputedStepRankCohort {
		t.Errorf("ranking a DISCOVERED member set should be served by rank_cohort; got served=%v step=%q unavailable=%q", cohortRow.Served(), cohortRow.Step, cohortRow.Unavailable)
	}
}

// TestEveryDeclaredUnavailableReasonIsInItsVocabulary is a STRUCTURAL guard,
// added because a reason constant shipped without reaching its own
// vocabulary and every test stayed green.
//
// `computed_population_absent` was declared, returned by the derivation, and
// asserted by name in a test -- while the vocabulary array still held three
// members and the doc comment beside it claimed four. So the telemetry
// histogram had no bucket for it, the log sink emitted no key, and
// TestEveryUnavailableReasonHasAPositiveFixture could not notice, because it
// iterates the vocabulary and the reason was not in it. A test that
// quantifies over a list cannot see something missing FROM that list.
//
// This reads the DECLARATIONS out of the source instead, which is the one
// place the omission is visible.
func TestEveryDeclaredUnavailableReasonIsInItsVocabulary(t *testing.T) {
	source, err := os.ReadFile("requirement_derivation.go")
	if err != nil {
		t.Fatalf("reading the declarations: %v", err)
	}
	declared := regexp.MustCompile(`(RequirementReason\w+)\s+RequirementUnavailableReason\s*=\s*"([a-z_]+)"`).
		FindAllStringSubmatch(string(source), -1)
	if len(declared) == 0 {
		t.Fatal("found no reason declarations; this guard would be vacuous")
	}

	// MEMBERSHIP, NOT CARDINALITY. The first version of this guard built
	// exactly this set and then compared only the two LENGTHS -- so
	// replacing a vocabulary member with a DUPLICATE of another kept the
	// count at four and the guard passed, with the reason still returned by
	// the derivation and still absent from every histogram and log key. The
	// merge-gate round constructed that mutation; I ran it and the whole
	// suite stayed green.
	//
	// A guard written to catch "declared but not in the list" that checks
	// only how long the list is has the same defect as the bug it was
	// written for: it asserts something adjacent to what its name claims.
	inVocabulary := map[string]bool{}
	for _, member := range RequirementUnavailableReasonVocabulary() {
		inVocabulary[string(member)] = true
	}
	checked := 0
	for _, match := range declared {
		name, value := match[1], match[2]
		if !inVocabulary[value] {
			t.Errorf("%s is declared as %q but is not in RequirementUnavailableReasonVocabulary -- it has no telemetry bucket and no log key, and every list-driven test is blind to it", name, value)
		}
		checked++
	}
	if checked != len(declared) {
		t.Fatalf("checked %d of %d declarations", checked, len(declared))
	}
	// And no duplicates: a duplicated member is how a missing one hides
	// while the count still matches.
	seen := map[RequirementUnavailableReason]bool{}
	for _, member := range RequirementUnavailableReasonVocabulary() {
		if seen[member] {
			t.Errorf("vocabulary member %q appears twice; a duplicate masks an omission from the count", member)
		}
		seen[member] = true
	}
}
