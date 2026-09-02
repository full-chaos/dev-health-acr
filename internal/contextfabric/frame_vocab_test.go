package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Registry tests for the CHAOS-4452 stage-2 frame vocabularies, per
// stage-1 principle 1 (§2.1): "every new field is an enum with a registry
// test, never a free string".
//
// These are the tests that make the closed vocabularies CLOSED rather than
// merely intended to be. A vocabulary whose size is asserted nowhere grows
// by one member in a rebase and nobody notices until a switch statement
// silently takes its default branch.

func TestFrameVocabularySizesMatchTheDesign(t *testing.T) {
	for _, testCase := range []struct {
		name string
		got  int
		want int
	}{
		{"InvestigationGoal", InvestigationGoalCount, 8},
		{"SubjectExpressionKind", SubjectExpressionKindCount, 6},
		{"TemporalIntent", TemporalIntentCount, 4},
		{"AnswerEmphasis", AnswerEmphasisCount, 2},
		{"AnswerObligation", AnswerObligationCount, 13},
		{"SubjectOperandKind", SubjectOperandKindCount, 2},
		{"FrameValidationOutcome", FrameValidationOutcomeCount, 4},
	} {
		if testCase.got != testCase.want {
			t.Errorf("%s vocabulary has %d members, design §13.2.2 says %d -- a size change here is a design change",
				testCase.name, testCase.got, testCase.want)
		}
	}
}

func TestFrameVocabulariesHaveNoDuplicatesAndRejectTheEmptyValue(t *testing.T) {
	goals := InvestigationGoalVocabulary()
	seenGoals := map[InvestigationGoal]bool{}
	for _, member := range goals {
		if seenGoals[member] {
			t.Errorf("duplicate InvestigationGoal %q", member)
		}
		seenGoals[member] = true
		if !ValidInvestigationGoal(member) {
			t.Errorf("ValidInvestigationGoal rejected its own member %q", member)
		}
	}
	if ValidInvestigationGoal("") {
		t.Error("the empty value must not be a vocabulary member")
	}
	if ValidSubjectExpressionKind("") || ValidTemporalIntent("") || ValidAnswerEmphasis("") ||
		ValidAnswerObligation("") || ValidSubjectOperandKind("") || ValidFrameValidationOutcome("") {
		t.Error("the empty value must not be a member of any frame vocabulary")
	}
}

// TestEveryObligationDeclaresAKind pins §13.2.3's kinds table against the
// obligation vocabulary.
//
// AN OBLIGATION WITH NO KIND IS THE HOLE THAT LET `ranking` BE PLANNED AS
// A READ. The frozen requirement layer modelled it with a required table
// shape no producer declares, so BAR question Q2's DEFINING obligation
// derived an empty fact-kind set -- unavailable by construction, on the
// design's own governing question (round 4, N3).
func TestEveryObligationDeclaresAKind(t *testing.T) {
	for _, obligation := range AnswerObligationVocabulary() {
		kind, ok := KindOfObligation(obligation)
		if !ok {
			t.Errorf("obligation %q declares no kind", obligation)
			continue
		}
		switch kind {
		case ObligationKindRead, ObligationKindComputed, ObligationKindAnswerContract:
		default:
			t.Errorf("obligation %q declares unknown kind %q", obligation, kind)
		}
	}
	// The two tables must agree in both directions: a computed obligation
	// names a server step, and nothing else does. Half of oracle O9 is
	// "every computed obligation names its server step"; that is
	// S7b-ii's assertion to make against the registry, and this is what
	// gives it a mapping to assert rather than one to invent.
	for _, obligation := range AnswerObligationVocabulary() {
		kind, _ := KindOfObligation(obligation)
		step, hasStep := StepForComputedObligation(obligation)
		if kind == ObligationKindComputed && !hasStep {
			t.Errorf("computed obligation %q names no server step", obligation)
		}
		if kind != ObligationKindComputed && hasStep {
			t.Errorf("non-computed obligation %q names server step %q", obligation, step)
		}
	}
	// The specific typing round 4 corrected.
	if kind, _ := KindOfObligation(ObligationRanking); kind != ObligationKindComputed {
		t.Errorf("ranking kind = %q, want computed -- RankCohort computes it; modelling it as a read is what emptied BAR Q2", kind)
	}
	if kind, _ := KindOfObligation(ObligationCount); kind != ObligationKindComputed {
		t.Errorf("count kind = %q, want computed", kind)
	}
}

// TestEveryInvariantHasAVocabularyMemberAndASpec is the check round 2's
// P2-4 asked for and the design then broke twice more.
//
// "An invariant whose failure is unobservable is not enforced." The
// vocabulary was capped at i10 while I14 existed; the revision that fixed
// that capped it at i17 while I18 existed. This test is what makes the
// third occurrence impossible.
func TestEveryInvariantHasAVocabularyMemberAndASpec(t *testing.T) {
	specs := FrameInvariantSpecs()
	if len(specs) != FrameInvariantCount {
		t.Fatalf("invariant table has %d rows, want %d (i1...i19)", len(specs), FrameInvariantCount)
	}
	seen := map[FrameInvariant]bool{}
	for _, spec := range specs {
		if seen[spec.ID] {
			t.Errorf("duplicate invariant %q in the table", spec.ID)
		}
		seen[spec.ID] = true
		if !ValidFrameInvariant(spec.ID) {
			t.Errorf("invariant %q is not a telemetry vocabulary member", spec.ID)
		}
		if len(spec.Reads) == 0 {
			t.Errorf("invariant %q declares no fields -- law L4's property test quantifies over this list", spec.ID)
		}
	}
	for _, want := range []FrameInvariant{
		FrameInvariantI1, FrameInvariantI2, FrameInvariantI3, FrameInvariantI4, FrameInvariantI5,
		FrameInvariantI6, FrameInvariantI7, FrameInvariantI8, FrameInvariantI9, FrameInvariantI10,
		FrameInvariantI11, FrameInvariantI12, FrameInvariantI13, FrameInvariantI14, FrameInvariantI15,
		FrameInvariantI16, FrameInvariantI17, FrameInvariantI18, FrameInvariantI19,
	} {
		if !seen[want] {
			t.Errorf("invariant %q is declared but absent from the table", want)
		}
	}
}

// TestLawL4NoPhaseA1InvariantReadsADerivedField is LAW L4's property test,
// verbatim from §13.2.2a: "every invariant declares the fields it reads;
// assert no A1 invariant names a derived field."
//
// This is the STRUCTURAL half of L4 and it lands in this slice because the
// A1/A2 split is what this slice builds. Round 2's P1-6: I10 and I14 sat
// in a single phase that runs BEFORE normalization, so as written they
// were either evaluated before their inputs existed or obligations were
// added after validation to a frame the design calls immutable.
func TestLawL4NoPhaseA1InvariantReadsADerivedField(t *testing.T) {
	for _, spec := range FrameInvariantSpecs() {
		if spec.Phase != FrameValidationPhaseA1 {
			continue
		}
		if FrameInvariantReadsDerivedField(spec) {
			t.Errorf("phase-A1 invariant %q reads a derived field (%v) -- law L4 forbids evaluating an invariant before its inputs are derived",
				spec.ID, spec.Reads)
		}
	}
	// The converse, so the test cannot pass by every invariant being A1:
	// the four invariants the design places in A2 must actually read a
	// derived value, or the split is decorative.
	a2 := map[FrameInvariant]bool{}
	for _, spec := range FrameInvariantSpecs() {
		if spec.Phase != FrameValidationPhaseA2 {
			continue
		}
		a2[spec.ID] = true
		if !FrameInvariantReadsDerivedField(spec) {
			t.Errorf("phase-A2 invariant %q reads no derived field -- it belongs in A1", spec.ID)
		}
	}
	for _, want := range []FrameInvariant{FrameInvariantI10, FrameInvariantI14, FrameInvariantI16, FrameInvariantI18} {
		if !a2[want] {
			t.Errorf("invariant %q must be phase A2", want)
		}
	}
}

// TestObligationDerivationTablesAreTotalOverTheVocabularies pins §13.2.3
// tables 1-3 against the vocabularies they are keyed on.
func TestObligationDerivationTablesAreTotalOverTheVocabularies(t *testing.T) {
	for _, goal := range InvestigationGoalVocabulary() {
		obligations, ok := goalObligations[goal]
		if !ok || len(obligations) == 0 {
			t.Errorf("goal %q contributes no obligation -- table 1 must be total, or that goal plans no evidence", goal)
		}
		for _, obligation := range obligations {
			if !ValidAnswerObligation(obligation) {
				t.Errorf("goal %q contributes non-member %q", goal, obligation)
			}
		}
		if _, ok := goalDischarge[goal]; !ok {
			t.Errorf("goal %q declares no discharge -- law L2 requires the mode to be NAMED, never implicit", goal)
		}
	}
	for _, temporal := range TemporalIntentVocabulary() {
		if _, ok := temporalObligations[temporal]; !ok {
			t.Errorf("temporal %q has no table-2 row", temporal)
		}
	}
	// Every dimension resolves to a discharge -- the four with an
	// obligation row and the five constrained to fact kinds.
	for _, dimension := range HealthDimensionVocabulary() {
		discharge := dimensionDischarge(dimension)
		if discharge.Mode == DischargeByObligation && !ValidAnswerObligation(discharge.Obligation) {
			t.Errorf("dimension %q discharges by obligation %q, which is not a member", dimension, discharge.Obligation)
		}
		if discharge.Mode == DischargeByRequirementProperty && discharge.Property == "" {
			t.Errorf("dimension %q discharges by an unnamed requirement property", dimension)
		}
	}
}

// TestHealthIsUnconditionalOnTheStateIshGoals is round 2's P1-1 pinned.
//
// `health` used to be gated on Dimensions being EMPTY, so naming any
// dimension REMOVED it -- a model emission NARROWING the plan, which is
// the law-L1 violation. A rebase that reintroduces the gate fails here.
func TestHealthIsUnconditionalOnTheStateIshGoals(t *testing.T) {
	for _, goal := range []InvestigationGoal{GoalAssessState, GoalExplainDrivers, GoalRankOrSurvey} {
		bare := DeriveObligations([]InvestigationGoal{goal}, TemporalIntentCurrent, nil)
		withDimension := DeriveObligations([]InvestigationGoal{goal}, TemporalIntentCurrent, []HealthDimension{HealthDimensionDeliveryFlow})

		if !containsObligation(bare, ObligationHealth) {
			t.Errorf("goal %q derives no health with empty dimensions: %v", goal, bare)
		}
		if !containsObligation(withDimension, ObligationHealth) {
			t.Errorf("goal %q LOST health once a dimension was named: %v -- that is the L1 violation round 2 found", goal, withDimension)
		}
	}
}

// TestBARQ2DerivesBothOperationsAsRequired is the test that the
// Goals-as-a-SET reversal is real.
//
// "What teams are struggling and what are the driving factors?" -- the
// design's governing question. No legal SINGLE-goal frame made both
// `ranking` and `principal_drivers` required: rank_or_survey gave ranking
// without drivers, explain_drivers gave drivers without ranking, and
// widening drivers made them ADVISORY so a bare ranking counted as
// complete.
func TestBARQ2DerivesBothOperationsAsRequired(t *testing.T) {
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalRankOrSurvey, GoalExplainDrivers},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
		Temporal: TemporalIntentCurrent,
	}
	frame = DeriveFrameObligations(frame, nil)

	for _, obligation := range []AnswerObligation{ObligationRanking, ObligationPrincipalDrivers} {
		requiredness, ok := frame.Requiredness(obligation)
		if !ok {
			t.Fatalf("BAR Q2 did not derive %q: %v", obligation, frame.Obligations)
		}
		if requiredness != RequirednessRequired {
			t.Fatalf("%q requiredness = %q, want required -- an advisory ranking means a bare ranking counts as complete", obligation, requiredness)
		}
	}
	// The EXACT set, per oracle O1's shape (its full seven-row table is
	// the family slice's).
	want := []AnswerObligation{
		ObligationState, ObligationHealth, ObligationPrincipalDrivers,
		ObligationRanking, ObligationEvidence, ObligationCoverage,
	}
	assertExactObligations(t, frame.Obligations, want)
}

// TestCountOverAScopeDerivesNoHealth pins §13.4.2's case 5, which oracle
// O1 calls out by name: "case 5 yields {count, evidence, coverage} and
// MUST NOT contain `health`".
//
// It is the row that proves `health` now comes only from the three
// state-ish goals rather than from an empty-Dimensions special case.
func TestCountOverAScopeDerivesNoHealth(t *testing.T) {
	got := DeriveObligations([]InvestigationGoal{GoalCountOrAggregate}, TemporalIntentCurrent, nil)
	assertExactObligations(t, got, []AnswerObligation{ObligationCount, ObligationEvidence, ObligationCoverage})
	if containsObligation(got, ObligationHealth) {
		t.Fatalf("a count question derived health: %v", got)
	}
}

// TestTrendGoalDerivesItsSeriesRegardlessOfTemporal is round 2's P1-2
// pinned: {describe_trend, bounded_window} derived NO temporal obligation
// at all, which was round 1's F4 defect having MOVED from family routing
// into obligation derivation. `trend_series` now comes from the GOAL, so
// law L2 holds whatever the temporal axis says.
func TestTrendGoalDerivesItsSeriesRegardlessOfTemporal(t *testing.T) {
	for _, temporal := range []TemporalIntent{TemporalIntentBoundedWindow, TemporalIntentTimeSeries, TemporalIntentPeriodComparison} {
		got := DeriveObligations([]InvestigationGoal{GoalDescribeTrend}, temporal, nil)
		if !containsObligation(got, ObligationTrendSeries) {
			t.Errorf("temporal %q: describe_trend derived no trend_series: %v", temporal, got)
		}
	}
}

// TestDerivedObligationsAreASetInVocabularyOrder. Oracle O1 asserts an
// EXACT set; that is only decidable if the derivation returns a
// deduplicated, deterministically ordered slice.
func TestDerivedObligationsAreASetInVocabularyOrder(t *testing.T) {
	// Two goals that overlap heavily -- state, evidence and coverage come
	// from both.
	got := DeriveObligations([]InvestigationGoal{GoalAssessState, GoalExplainDrivers}, TemporalIntentCurrent, nil)
	seen := map[AnswerObligation]bool{}
	last := -1
	order := map[AnswerObligation]int{}
	for position, member := range AnswerObligationVocabulary() {
		order[member] = position
	}
	for _, obligation := range got {
		if seen[obligation] {
			t.Fatalf("duplicate obligation %q in %v", obligation, got)
		}
		seen[obligation] = true
		if order[obligation] <= last {
			t.Fatalf("obligations are not in vocabulary order: %v", got)
		}
		last = order[obligation]
	}
	// Order-insensitivity of the INPUT: the same goal set in the other
	// order must derive the identical output, or the family would become
	// a function of emission order.
	reversed := DeriveObligations([]InvestigationGoal{GoalExplainDrivers, GoalAssessState}, TemporalIntentCurrent, nil)
	assertExactObligations(t, reversed, got)
}

// TestSanitizersDropUnknownMembersAndNeverError pins §13.2.1: "an unknown
// string is DROPPED from the set, never an error".
func TestSanitizersDropUnknownMembersAndNeverError(t *testing.T) {
	goals, dropped := SanitizeInvestigationGoals([]string{"assess_state", "vibe_check", "assess_state", "  ", "rank_or_survey"})
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1 (an all-unknown emission must be countable, not silent)", dropped)
	}
	assertExactGoals(t, goals, []InvestigationGoal{GoalAssessState, GoalRankOrSurvey})

	// An ALL-unknown set sanitizes to empty, which invariant I15 then
	// fails on -- never a silent default to assess_state.
	empty, allDropped := SanitizeInvestigationGoals([]string{"vibe_check", "do_the_thing"})
	if len(empty) != 0 || allDropped != 2 {
		t.Errorf("all-unknown goals = %v dropped=%d, want empty/2", empty, allDropped)
	}

	if kind, unrecognized := SanitizeSubjectExpressionKind("squad_rollup"); kind != "" || !unrecognized {
		t.Errorf("unknown kind sanitized to %q/%v, want \"\"/true", kind, unrecognized)
	}
	if kind, unrecognized := SanitizeSubjectExpressionKind(""); kind != "" || unrecognized {
		t.Errorf("an UNSET kind must not be reported as unrecognized; got %q/%v", kind, unrecognized)
	}
	// An unset Temporal is neither an error nor unrecognized -- the
	// `current` default is a NORMALIZATION step, not a sanitization one,
	// and keeping them separate is what lets an A1 invariant read only
	// what the model emitted.
	if temporal, unrecognized := SanitizeTemporalIntent(""); temporal != "" || unrecognized {
		t.Errorf("unset temporal sanitized to %q/%v, want \"\"/false", temporal, unrecognized)
	}
	if NormalizeFrame(QuestionFrame{}).Temporal != TemporalIntentCurrent {
		t.Error("normalization must derive `current` for an unset Temporal")
	}

	// Terms preserve ORDER (retrieval pointers handed to the graph in the
	// order the user named them); sets do not.
	terms, truncated := SanitizeSubjectTerms([]string{" beta ", "", "alpha"})
	if truncated != 0 || len(terms) != 2 || terms[0] != "beta" || terms[1] != "alpha" {
		t.Errorf("terms = %v truncated=%d, want [beta alpha]/0 -- term ORDER is meaningful", terms, truncated)
	}
}

// TestDeriveShapeIsTotalAndScopedFramesDenyTheCensus pins §13.8b's
// derivation table, and its load-bearing row.
//
// Round 4's N2: under the frozen mapping a children_of_scope frame derived
// `discovered_cohort`, which ADMITS the org-wide kind census
// (falkorgraph/reader.go:736), and DiscoveredCohort then keeps every node
// of the requested kind -- so BAR question Q-B ("the fullchaos team's
// projects") would have returned EVERY PROJECT IN THE ORGANIZATION.
//
// Oracle O11 asserts this against the real census gate and lands with the
// retrieval slice, which is where the gate becomes a frame consumer. What
// this test pins NOW is the mapping O11 will rest on, so the row cannot be
// "tidied" to the obvious-but-wrong value in the meantime.
func TestDeriveShapeIsTotalAndScopedFramesDenyTheCensus(t *testing.T) {
	for _, testCase := range []struct {
		kind SubjectExpressionKind
		want InvestigationShape
	}{
		{SubjectExpressionNamed, ShapeSingleSubject},
		{SubjectExpressionExplicitSet, ShapeExplicitCohort},
		{SubjectExpressionChildrenOfScope, ShapeExplicitCohort},
		{SubjectExpressionDiscoveredKind, ShapeDiscoveredCohort},
		{SubjectExpressionGroupedMembers, ShapeDiscoveredCohort},
		{SubjectExpressionOrganizationScope, ShapeOpen},
	} {
		got := DeriveShape(SubjectExpression{Kind: testCase.kind})
		if got != testCase.want {
			t.Errorf("DeriveShape(%q) = %q, want %q", testCase.kind, got, testCase.want)
		}
	}
	if DeriveShape(SubjectExpression{Kind: SubjectExpressionChildrenOfScope}) == ShapeDiscoveredCohort {
		t.Fatal("children_of_scope must NOT derive discovered_cohort -- that shape admits the org-wide census and returns every project in the org for a question that named one team's")
	}
	// Totality: an unset kind has no projection and returns the zero
	// shape rather than guessing one.
	if DeriveShape(SubjectExpression{}) != "" {
		t.Error("an unset Kind must project to the zero shape, never a guess")
	}
}

// TestShapeAgreementTreatsAnAbsentEmissionAsAgreement. The
// omitted-subject-terms ticket measured the interpreter dropping
// structured fields it is asked for -- 11/14 emission on a field that has
// been on the contract for months. Counting "the model said nothing" as
// "the model contradicted us" would manufacture I18 divergences out of a
// known omission class and drown the signal.
func TestShapeAgreementTreatsAnAbsentEmissionAsAgreement(t *testing.T) {
	scoped := SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"fullchaos team"}, MemberKind: contractsv1.ContextFabricSubjectProject},
	}
	if _, diverged := ShapeAgreement("", scoped); diverged {
		t.Error("an absent emitted Shape must not be recorded as a divergence")
	}
	if _, diverged := ShapeAgreement(ShapeExplicitCohort, scoped); diverged {
		t.Error("a matching emitted Shape must not be recorded as a divergence")
	}
	divergence, diverged := ShapeAgreement(ShapeDiscoveredCohort, scoped)
	if !diverged {
		t.Fatal("a genuinely divergent Shape must be recorded")
	}
	if divergence.Derived != ShapeExplicitCohort {
		t.Errorf("derived = %q, want explicit_cohort -- I18's contract is that the DERIVED value wins", divergence.Derived)
	}
}

func containsObligation(set []AnswerObligation, want AnswerObligation) bool {
	for _, member := range set {
		if member == want {
			return true
		}
	}
	return false
}

func assertExactObligations(t *testing.T, got, want []AnswerObligation) {
	t.Helper()
	normalized := sortedObligations(want)
	if len(got) != len(normalized) {
		t.Fatalf("obligation set = %v, want exactly %v", got, normalized)
	}
	for i := range got {
		if got[i] != normalized[i] {
			t.Fatalf("obligation set = %v, want exactly %v", got, normalized)
		}
	}
}

func assertExactGoals(t *testing.T, got, want []InvestigationGoal) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("goal set = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("goal set = %v, want %v", got, want)
		}
	}
}
