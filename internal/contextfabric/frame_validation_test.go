package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Validation-side oracles for the CHAOS-4452 frame layer.
//
// THE REPAIR-BOUND ORACLES ARE NOT HERE. Oracle O12(a) (the repair bound)
// and every `TestRepairBound*` case moved with the bounded repair into its
// own change, because five adversarial rounds found defects in that bound
// and none in this surface. What remains is what decides whether a frame is
// VALID: O12(b) -- the operand union, which is invariant I19's oracle
// rather than the bound's -- plus the normalization, widening and telemetry
// properties.

// discoveredTeamsEmphasisFrame is a discovered-team-cohort frame carrying
// both emphasis ends -- the shape invariant I14 is stated over.
func discoveredTeamsEmphasisFrame(goals ...InvestigationGoal) QuestionFrame {
	return QuestionFrame{
		Goals: goals,
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
		Emphasis: []AnswerEmphasis{EmphasisNegativeOutliers, EmphasisPositiveOutliers},
	}
}

// TestO12bSubjectOperandUnionFailuresAreRecordedAsI19 is O12(b) verbatim:
// "A SubjectOperand with both pointers nil, both non-nil, or a pointer
// that disagrees with Kind fails I19 by name; a scoped operand with empty
// AnchorTerms fails I5 through I19; the failed invariant is recorded as
// i19."
//
// Round 3's finding 9 is why this exists: the frozen design added the
// operand TYPE without extending any invariant, so an operand with two
// optional pointers had no discriminator and no exactly-one rule, and I3
// (which validates only Named.Terms) could not be satisfied by a scoped
// operand at all.
func TestO12bSubjectOperandUnionFailuresAreRecordedAsI19(t *testing.T) {
	compareFrame := func(operands ...SubjectOperand) QuestionFrame {
		return QuestionFrame{
			Goals: []InvestigationGoal{GoalCompare},
			SubjectExpression: SubjectExpression{
				Kind:     SubjectExpressionExplicitSet,
				Explicit: &ExplicitSetExpression{Operands: operands},
			},
			Temporal: TemporalIntentCurrent,
		}
	}
	goodNamed := SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"team a"}}}

	for _, testCase := range []struct {
		name    string
		operand SubjectOperand
		detail  FrameFailureDetail
	}{
		{
			name:    "both pointers nil",
			operand: SubjectOperand{Kind: SubjectOperandNamed},
			detail:  FrameFailureOperandNoVariant,
		},
		{
			name: "both pointers non-nil",
			operand: SubjectOperand{
				Kind:   SubjectOperandNamed,
				Named:  &NamedSubjectExpression{Terms: []string{"team b"}},
				Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject},
			},
			detail: FrameFailureOperandMultiVariant,
		},
		{
			name: "pointer disagrees with Kind",
			operand: SubjectOperand{
				Kind:   SubjectOperandNamed,
				Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject},
			},
			detail: FrameFailureOperandKindMismatch,
		},
		{
			name:    "operand Kind unset",
			operand: SubjectOperand{Named: &NamedSubjectExpression{Terms: []string{"team b"}}},
			detail:  FrameFailureOperandKindUnset,
		},
		{
			// "a scoped operand with empty AnchorTerms fails I5 through
			// I19; the failed invariant is recorded as i19."
			name: "scoped operand with empty AnchorTerms",
			operand: SubjectOperand{
				Kind:   SubjectOperandScoped,
				Scoped: &ScopedSetExpression{MemberKind: contractsv1.ContextFabricSubjectProject},
			},
			detail: FrameFailureOperandNoAnchor,
		},
		{
			name: "named operand with no terms",
			operand: SubjectOperand{
				Kind:  SubjectOperandNamed,
				Named: &NamedSubjectExpression{},
			},
			detail: FrameFailureOperandNoTerms,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure, bad := ValidateFramePhaseA1(compareFrame(goodNamed, testCase.operand))
			if !bad {
				t.Fatal("a malformed operand must fail phase A1")
			}
			if failure.Invariant != FrameInvariantI19 {
				t.Fatalf("failed invariant = %q, want %q -- operand failures are recorded as i19, never folded into i2 or i5",
					failure.Invariant, FrameInvariantI19)
			}
			if failure.Detail != testCase.detail {
				t.Fatalf("failure detail = %q, want %q", failure.Detail, testCase.detail)
			}
		})
	}

	// The negative control: a well-formed mixed-variant comparison passes,
	// which is the R10 closure the union was added for -- "compare team
	// A's PROJECTS with team B's PROJECTS" is expressible.
	mixed := compareFrame(
		goodNamed,
		SubjectOperand{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"team b"}, MemberKind: contractsv1.ContextFabricSubjectProject}},
	)
	if failure, bad := ValidateFramePhaseA1(mixed); bad {
		t.Fatalf("a well-formed mixed-variant comparison must pass A1; failed on %q/%q", failure.Invariant, failure.Detail)
	}
}

// TestRepairBoundRefusesAnUnnamedKindChange pins the safety half of §13.6
// rule 2. The bound was LOOSENED after round 2 showed it made I7 and I9
// repairs structurally unreachable; this test is what stops the loosening
// from becoming "the repair may reinterpret the question".

// TestValidFrameEmitsTelemetryWithNoFailure is the "fires on EVERY frame
// reaching validation, INCLUDING valid ones" rule. An event that appears
// only on failure makes "the validator never rejects anything" and "the
// validator never ran" the same observation.
func TestValidFrameEmitsTelemetryWithNoFailure(t *testing.T) {
	proposed := namedFrame(GoalAssessState)
	result := ValidateFrame(proposed, nil, "")
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("outcome = %q, want valid", result.Outcome)
	}
	event := FrameValidationEventFrom(proposed, result, "", nil)
	if event.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("event outcome = %q, want valid", event.Outcome)
	}
	if event.FailedInvariant != "" {
		t.Fatalf("event failed_invariant = %q on a valid frame, want empty", event.FailedInvariant)
	}
	if event.DerivedObligationCount == 0 {
		t.Fatal("event derived_obligation_count = 0 on a valid frame")
	}
	if event.FrameVersion != QuestionFrameVersion {
		t.Fatalf("event frame_version = %q, want %q", event.FrameVersion, QuestionFrameVersion)
	}
	if len(event.ProposedGoals) != 1 || event.ProposedGoals[0] != GoalAssessState {
		t.Fatalf("event proposed_goals = %v, want [assess_state] -- §13.2.4 rule 3's governance depends on this field", event.ProposedGoals)
	}
}

// TestWidenedObligationsAreAdvisoryAndNeverNarrow pins §13.2.4's two
// safe-direction rules at the type level.

// TestWidenedObligationsAreAdvisoryAndNeverNarrow pins §13.2.4's two
// safe-direction rules at the type level.
func TestWidenedObligationsAreAdvisoryAndNeverNarrow(t *testing.T) {
	frame := NormalizeFrame(namedFrame(GoalAssessState))
	base := DeriveFrameObligations(frame, nil)

	// A model emits a spurious `ranking` obligation on a plain
	// current-state question, plus a member the server already derived.
	widened := DeriveFrameObligations(frame, []AnswerObligation{ObligationRanking, ObligationState})

	if len(widened.Obligations) != len(base.Obligations) {
		t.Fatalf("derived obligations moved under a widening: %v -> %v -- the model may never change the DERIVED set",
			base.Obligations, widened.Obligations)
	}
	requiredness, ok := widened.Requiredness(ObligationRanking)
	if !ok || requiredness != RequirednessAdvisory {
		t.Fatalf("widened ranking requiredness = %q/%v, want advisory", requiredness, ok)
	}
	// A member the server already derived stays REQUIRED and does not
	// also appear as advisory.
	requiredness, ok = widened.Requiredness(ObligationState)
	if !ok || requiredness != RequirednessRequired {
		t.Fatalf("state requiredness = %q/%v, want required", requiredness, ok)
	}
	for _, member := range widened.WidenedObligations {
		if member == ObligationState {
			t.Fatal("a derived obligation was also recorded as widened -- one member cannot be both required and advisory")
		}
	}
	// HasObligation must NOT see the advisory member: an advisory
	// obligation satisfying I14 would let a model emission discharge an
	// invariant that exists to check a DERIVED value.
	if widened.HasObligation(ObligationRanking) {
		t.Fatal("HasObligation returned true for a model-widened obligation")
	}
}

// TestRepairBoundRefusesARetargetedSubjectOnAPermittedKindMove is codex
// round 2's first finding, reproduced and closed.
//
// The previous rule skipped the variant comparison entirely whenever the
// Kind move itself was permitted, on the (correct) ground that changing
// the discriminator necessarily rewrites the variant. Correct, and too
// broad: it excused the whole payload. The executed counterexample was a
// count over `named_subject("team a")` failing I9 and coming back as
// `children_of_scope(anchor_terms=["platform team"], member_kind=project)`
// -- ACCEPTED. I9's condition is "a count needs a set-valued kind"; it
// names Goals and the Kind and says nothing about WHICH subject, so
// deciding that is not a repair, it is a different question.
//
// The rule now: a repair may RE-TYPE the structure, never RE-TARGET it.
// Every retrieval pointer in the repaired expression must already have
// been in the proposed one.

// TestFrameVersionIsAlwaysTheServerConstant is codex round 2's second
// finding.
//
// NormalizeFrame defaulted the version only when absent, so a non-empty
// proposed version survived into the validated frame, the receipt and the
// `frame_version` log field. Two things wrong at once: free text reached a
// closed telemetry field, and a model or repairer could FALSIFY which
// derivation table produced a frame -- the one claim the version exists to
// make.
func TestFrameVersionIsAlwaysTheServerConstant(t *testing.T) {
	const forged = "zzz-confidential-frame-version"
	proposed := namedFrame(GoalAssessState)
	proposed.Version = forged

	result := ValidateFrame(proposed, nil, "")
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("outcome = %q, want valid", result.Outcome)
	}
	if result.Frame.Version != QuestionFrameVersion {
		t.Fatalf("validated frame version = %q, want %q -- the version is SERVER-DERIVED and stamped unconditionally, never defaulted",
			result.Frame.Version, QuestionFrameVersion)
	}
	event := FrameValidationEventFrom(proposed, result, "", nil)
	if event.FrameVersion != QuestionFrameVersion {
		t.Fatalf("event frame_version = %q, want %q", event.FrameVersion, QuestionFrameVersion)
	}
}

// TestGoalsAreCanonicalizedIntoASet is codex round 2's third finding.
//
// Goals are documented as a SET in vocabulary order, but only the emission
// boundary's sanitizer produced that shape: a frame built directly kept
// duplicates and emission order, validated as `valid`, and was persisted
// and logged verbatim. Beyond the representation inconsistency, the family
// derivation of the NEXT slice is specified as a pure function of the
// frame, and a function whose input can differ by order is not one.

// TestGoalsAreCanonicalizedIntoASet is codex round 2's third finding.
//
// Goals are documented as a SET in vocabulary order, but only the emission
// boundary's sanitizer produced that shape: a frame built directly kept
// duplicates and emission order, validated as `valid`, and was persisted
// and logged verbatim. Beyond the representation inconsistency, the family
// derivation of the NEXT slice is specified as a pure function of the
// frame, and a function whose input can differ by order is not one.
func TestGoalsAreCanonicalizedIntoASet(t *testing.T) {
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalRankOrSurvey, GoalAssessState, GoalRankOrSurvey},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
	}
	result := ValidateFrame(proposed, nil, "")
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("outcome = %q, want valid", result.Outcome)
	}
	assertExactGoals(t, result.Frame.Goals, []InvestigationGoal{GoalAssessState, GoalRankOrSurvey})

	// AND THE EMITTED EVENT, not only the frame. The first version of this
	// test asserted `result.Frame.Goals` alone and passed while the
	// telemetry projection kept the model's emission order and its
	// duplicates -- found by adversarial review. A canonicalization test
	// that checks the object but not what is recorded about it covers half
	// the property and reads as covering all of it.
	event := FrameValidationEventFrom(proposed, result, "", nil)
	assertExactGoals(t, event.ProposedGoals, []InvestigationGoal{GoalAssessState, GoalRankOrSurvey})

	// Two orderings of one goal set must produce the identical frame --
	// this is the property the family derivation will rest on.
	other := proposed
	other.Goals = []InvestigationGoal{GoalAssessState, GoalRankOrSurvey}
	otherResult := ValidateFrame(other, nil, "")
	assertExactGoals(t, otherResult.Frame.Goals, result.Frame.Goals)

	// Canonicalization must NOT swallow an out-of-vocabulary member --
	// that is I15's failure to name, and dropping it here would take the
	// failure away from the invariant that reports it.
	invalid := proposed
	invalid.Goals = []InvestigationGoal{GoalAssessState, InvestigationGoal("zzz-not-a-goal")}
	failure, bad := ValidateFramePhaseA1(NormalizeFrame(invalid))
	if !bad || failure.Invariant != FrameInvariantI15 {
		t.Fatalf("canonicalization hid an out-of-vocabulary goal: failure = %q/%v", failure.Invariant, bad)
	}
}

// TestRoundThreeShapeSweep is a table of concrete REPAIR SCENARIOS, kept
// as regression cases for the specific defects rounds 1-4 found.
//
// IT IS NOT THE SWEEP ANY MORE. The sweep is generated from the derived
// path set in frame_sweep_test.go, because a hand-listed axis
// table is exactly what let four rounds find one defect at four depths --
// this table's own I2 rows exercised term replacement and addition and
// never a nested discriminator change, which is how round 4 got in. These
// rows survive as worked examples with their negative controls; the
// coverage claim belongs to the generated sweep.
//
// THE CLASS, stated once: "the repair bound permits rewriting subject
// payload the failed invariant does not actually constrain." It was found
// three times in three disguises -- by the lane (a variant read licensing
// a discriminator change), by review round 2 (a permitted Kind move
// excusing the whole payload), and by review round 3 (I2 and I3 declaring
// the whole variant as read while constraining only the operand count and
// the terms). Every previous fix was per-instance, which is exactly the
// pattern that produces re-finds.
//
// The general fix is the Reads/Constrains split: the bound now quantifies
// over what each invariant's CONDITION constrains, not over what it reads.
// This test sweeps EVERY phase-A invariant against EVERY axis a repair
// could touch, so a future edit that widens one Constrains list has to
// justify itself here.

// TestEmphasisAndDimensionsAreCanonicalizedIntoSets is round 3's finding 3
// -- the same defect the goal axis had, one field over. All three
// set-valued axes are canonicalized in one place now, rather than each
// where it happened to be noticed.
func TestEmphasisAndDimensionsAreCanonicalizedIntoSets(t *testing.T) {
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalRankOrSurvey},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
		Emphasis:   []AnswerEmphasis{EmphasisPositiveOutliers, EmphasisNegativeOutliers, EmphasisPositiveOutliers},
		Dimensions: []HealthDimension{HealthDimensionInvestmentBalance, HealthDimensionExecutionCompletion, HealthDimensionInvestmentBalance},
	}
	normalized := NormalizeFrame(frame)

	if len(normalized.Emphasis) != 2 || normalized.Emphasis[0] != EmphasisPositiveOutliers || normalized.Emphasis[1] != EmphasisNegativeOutliers {
		t.Errorf("emphasis = %v, want the deduplicated pair in vocabulary order", normalized.Emphasis)
	}
	if len(normalized.Dimensions) != 2 || normalized.Dimensions[0] != HealthDimensionExecutionCompletion || normalized.Dimensions[1] != HealthDimensionInvestmentBalance {
		t.Errorf("dimensions = %v, want the deduplicated pair in published order", normalized.Dimensions)
	}
	// A duplicate dimension produced a DUPLICATE AXIS DISCHARGE, so the
	// set property is not cosmetic here.
	discharges := FrameAxisDischarges(DeriveFrameObligations(normalized, nil))
	seen := map[string]int{}
	for _, discharge := range discharges {
		if discharge.Axis == AxisDimension {
			seen[discharge.Value]++
		}
	}
	for value, count := range seen {
		if count != 1 {
			t.Errorf("dimension %q produced %d discharges, want 1", value, count)
		}
	}
}

// TestRepairMayNotRewriteAWellFormedOperand is round 4's finding, and it
// is the SAME CLASS as rounds 2 and 3 one level further down: a blunt
// field token covering more than the invariant's condition does.
//
// `FrameFieldOperands` covered the operand COUNT (which I2 constrains) and
// every operand's discriminator, member kind and expected kind (which it
// does not). Review's executed repro changed an existing, well-formed
// `named_subject("team a")` operand into
// `children_of_scope(anchor "team a", member project)`. The TERM STRING is
// preserved, so the pointer rule let it through — and the question turned
// from "how is team A doing" into "how are team A's projects doing".
//
// Review also answered the sweep question the prompt asked: the sweep's I2
// rows exercised only term replacement and addition, never a nested
// discriminator change. That gap is closed by the rows below.

// TestOrganizationMemberKindIsValidatedWhenSupplied is round 5's P2.
//
// "Optional" and "unvalidated" are easy to conflate, and I17 conflated
// them: it checked Org.MemberKind only when the goal was a count, so
// `organization_scope{member_kind:"squad"}` with `Goals=[assess_state]`
// passed BOTH phases and `MemberKind()` returned `("squad", true)` to the
// retrieval seam — an out-of-vocabulary kind reaching the substrate
// through a frame the server had declared valid. The seam-7 slice calls
// that accessor on frames it assumes were validated, so this would have
// surfaced there as a retrieval mystery rather than a validation gap.
func TestOrganizationMemberKindIsValidatedWhenSupplied(t *testing.T) {
	squad := SubjectKind("squad")
	invalid := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionOrganizationScope,
			Org:  &OrganizationScopeExpression{MemberKind: &squad},
		},
	}
	failure, bad := ValidateFramePhaseA1(invalid)
	if !bad {
		t.Fatal("a supplied but out-of-vocabulary Org.MemberKind must fail A1 -- optionality exempts absence, never nonsense")
	}
	if failure.Invariant != FrameInvariantI17 || failure.Detail != FrameFailureMemberKindInvalid {
		t.Fatalf("failure = %q/%q, want i17/%q", failure.Invariant, failure.Detail, FrameFailureMemberKindInvalid)
	}

	// ABSENT is still fine for a non-counting goal: the org itself is the
	// subject and there is nothing to enumerate.
	absent := invalid
	absent.SubjectExpression.Org = &OrganizationScopeExpression{}
	if failure, bad := ValidateFramePhaseA1(absent); bad {
		t.Fatalf("an ABSENT Org.MemberKind must stay legal for a non-counting goal; failed on %q/%q", failure.Invariant, failure.Detail)
	}
	// And a VALID supplied kind passes.
	repo := contractsv1.ContextFabricSubjectRepository
	valid := invalid
	valid.SubjectExpression.Org = &OrganizationScopeExpression{MemberKind: &repo}
	if failure, bad := ValidateFramePhaseA1(valid); bad {
		t.Fatalf("a valid supplied Org.MemberKind must pass; failed on %q/%q", failure.Invariant, failure.Detail)
	}
}
