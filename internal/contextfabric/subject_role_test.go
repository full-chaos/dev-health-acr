package contextfabric

// Tests for the subject-role layer: which (role, subject kind) coordinates
// a frame's obligations attach to.
//
// EVERY LOOP IN THIS FILE COUNTS THE INPUTS THAT REACHED ITS ASSERTIONS AND
// FAILS AT ZERO. That rule is not decoration here: the declaration slice
// shipped a coordinate derivation whose oracle passed while a whole operand
// variant was never derived, and a sibling lane shipped a property test in
// which a mutation routed all 200 trials down a refusal branch while the
// test stayed green. A loop that can `continue` past its assertions proves
// nothing about the cases it skipped, and "how many reached the assertion"
// is the only thing that distinguishes the two.

import (
	"strings"
	"testing"
)

func kindPointer(kind SubjectKind) *SubjectKind {
	return &kind
}

// frameWith builds a frame THROUGH the shipped derivation, never by
// hand-typing an obligation list. A change to §13.2.3's tables therefore
// moves these tests with it rather than leaving them silently disagreeing.
func frameWith(goals []InvestigationGoal, expression SubjectExpression, temporal TemporalIntent, dimensions []HealthDimension) QuestionFrame {
	return DeriveFrameObligations(QuestionFrame{
		Goals:             goals,
		SubjectExpression: expression,
		Temporal:          temporal,
		Dimensions:        dimensions,
		Version:           QuestionFrameVersion,
	}, nil)
}

func namedExpression(kind SubjectKind) SubjectExpression {
	return SubjectExpression{
		Kind:  SubjectExpressionNamed,
		Named: &NamedSubjectExpression{Terms: []string{"s"}, ExpectedKind: kindPointer(kind)},
	}
}

func groupedExpression(member, group SubjectKind) SubjectExpression {
	return SubjectExpression{
		Kind:    SubjectExpressionGroupedMembers,
		Grouped: &GroupedSetExpression{MemberKind: member, GroupKind: group},
	}
}

func scopedExpression(member SubjectKind) SubjectExpression {
	return SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: member},
	}
}

func discoveredExpression(member SubjectKind) SubjectExpression {
	return SubjectExpression{
		Kind:       SubjectExpressionDiscoveredKind,
		Discovered: &DiscoveredSetExpression{MemberKind: member},
	}
}

func orgExpression(member *SubjectKind) SubjectExpression {
	return SubjectExpression{
		Kind: SubjectExpressionOrganizationScope,
		Org:  &OrganizationScopeExpression{MemberKind: member},
	}
}

// rolesPresent reduces a coordinate list to the set of roles it uses.
func rolesPresent(coordinates []RequirementCoordinate) map[SubjectRole]bool {
	present := map[SubjectRole]bool{}
	for _, coordinate := range coordinates {
		present[coordinate.Role] = true
	}
	return present
}

// TestEveryVariantOffersTheRolesItsTopologyHas quantifies over the WHOLE
// closed union rather than over the variants this file happened to think
// of. SubjectExpressionKindVocabulary is the population; a variant added to
// the union with no case here fails the test by name rather than being
// silently untested.
func TestEveryVariantOffersTheRolesItsTopologyHas(t *testing.T) {
	expressions := map[SubjectExpressionKind]SubjectExpression{
		SubjectExpressionNamed:             namedExpression(SubjectTeam),
		SubjectExpressionGroupedMembers:    groupedExpression(SubjectProject, SubjectTeam),
		SubjectExpressionChildrenOfScope:   scopedExpression(SubjectProject),
		SubjectExpressionDiscoveredKind:    discoveredExpression(SubjectTeam),
		SubjectExpressionOrganizationScope: orgExpression(nil),
		SubjectExpressionExplicitSet: {
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: kindPointer(SubjectTeam)}},
			}},
		},
	}
	wantRoles := map[SubjectExpressionKind][]SubjectRole{
		SubjectExpressionNamed:             {SubjectRoleSubject},
		SubjectExpressionGroupedMembers:    {SubjectRoleMember, SubjectRoleGroup},
		SubjectExpressionChildrenOfScope:   {SubjectRoleMember},
		SubjectExpressionDiscoveredKind:    {SubjectRoleMember},
		SubjectExpressionOrganizationScope: {SubjectRoleSubject},
		SubjectExpressionExplicitSet:       {SubjectRoleOperand},
	}

	checked := 0
	for _, variant := range SubjectExpressionKindVocabulary() {
		expression, built := expressions[variant]
		if !built {
			t.Errorf("variant %q has no case in this test: the closed union grew and the role table was not exercised for the new member", variant)
			continue
		}
		frame := frameWith([]InvestigationGoal{GoalAssessState}, expression, TemporalIntentCurrent, nil)
		got := rolesPresent(DeriveRequirementCoordinates(frame))
		for _, want := range wantRoles[variant] {
			if !got[want] {
				t.Errorf("variant %q: expected role %q among the derived coordinates, got %v", variant, want, got)
			}
		}
		if len(got) != len(wantRoles[variant]) {
			t.Errorf("variant %q: derived roles %v, want exactly %v", variant, got, wantRoles[variant])
		}
		checked++
	}
	if checked != SubjectExpressionKindCount {
		t.Fatalf("reached the assertions for %d variants, want all %d -- a loop that skips its cases proves nothing about them", checked, SubjectExpressionKindCount)
	}
}

// TestOnlyStateAttachesToTheGroupAxis is THE group rule, and it is the
// correction the declaration slice's flat product lacked.
//
// The flat version crossed every derived obligation with every role, so a
// grouped frame demanded `health`, `principal_drivers` and `trend_series`
// OF A GROUPING AXIS. §13.15.2 carries a group-role row for `state` alone;
// everything else is read of the members. Whether the group `state`
// requirement is then served by a post-fact-read step or by rolling up
// member facts is a separate question decided on the rig -- this asserts
// only which coordinates the frame demands.
func TestOnlyStateAttachesToTheGroupAxis(t *testing.T) {
	// A goal set wide enough that a flat product would put five distinct
	// obligations on the group axis: assess_state contributes state and
	// health, explain_drivers adds principal_drivers, describe_trend adds
	// trend_series.
	frame := frameWith(
		[]InvestigationGoal{GoalAssessState, GoalExplainDrivers, GoalDescribeTrend},
		groupedExpression(SubjectProject, SubjectTeam),
		TemporalIntentCurrent,
		nil,
	)
	if len(frame.Obligations) < 4 {
		t.Fatalf("frame derived %d obligations, too few to distinguish a flat product from the role rule: %v", len(frame.Obligations), frame.Obligations)
	}

	groupCells := 0
	memberCells := 0
	for _, coordinate := range DeriveRequirementCoordinates(frame) {
		switch coordinate.Role {
		case SubjectRoleGroup:
			groupCells++
			if coordinate.Obligation != ObligationState {
				t.Errorf("obligation %q attached to the group axis; only state does", coordinate.Obligation)
			}
			if coordinate.Subject != SubjectTeam {
				t.Errorf("group coordinate carries subject %q, want the GroupKind %q", coordinate.Subject, SubjectTeam)
			}
		case SubjectRoleMember:
			memberCells++
			if coordinate.Subject != SubjectProject {
				t.Errorf("member coordinate carries subject %q, want the MemberKind %q", coordinate.Subject, SubjectProject)
			}
		default:
			t.Errorf("grouped frame produced role %q, which its topology does not have", coordinate.Role)
		}
	}
	if groupCells != 1 {
		t.Errorf("group axis carries %d coordinates, want exactly 1 (state)", groupCells)
	}
	if memberCells == 0 {
		t.Fatal("no member coordinates reached the assertions -- the loop proved nothing")
	}
}

// TestCountAttachesOnceToThePopulation is design finding S3 made
// executable: the frozen SubjectRole table "read Kind alone, which planned
// per-member reads for a count".
//
// A cardinality is one requirement over one population. It must not
// multiply across the roles a frame happens to have, and in a grouped frame
// it must land on the members rather than on the grouping axis.
func TestCountAttachesOnceToThePopulation(t *testing.T) {
	cases := []struct {
		name        string
		expression  SubjectExpression
		wantRole    SubjectRole
		wantSubject SubjectKind
	}{
		{"grouped: the counted population is the members", groupedExpression(SubjectProject, SubjectTeam), SubjectRoleMember, SubjectProject},
		{"scoped: the counted population is the members under the anchor", scopedExpression(SubjectProject), SubjectRoleMember, SubjectProject},
		{"discovered: the counted population is the discovered kind", discoveredExpression(SubjectTeam), SubjectRoleMember, SubjectTeam},
		{"organization with a counted kind: I17's member kind, not the org", orgExpression(kindPointer(SubjectTeam)), SubjectRoleMember, SubjectTeam},
		{"organization without one: the org is itself the subject", orgExpression(nil), SubjectRoleSubject, SubjectOrganization},
		{"named: the subject is its own population", namedExpression(SubjectTeam), SubjectRoleSubject, SubjectTeam},
	}

	checked := 0
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			frame := frameWith([]InvestigationGoal{GoalCountOrAggregate}, testCase.expression, TemporalIntentCurrent, nil)
			var counts []RequirementCoordinate
			for _, coordinate := range DeriveRequirementCoordinates(frame) {
				if coordinate.Obligation == ObligationCount {
					counts = append(counts, coordinate)
				}
			}
			if len(counts) != 1 {
				t.Fatalf("derived %d count coordinates, want exactly 1: %v", len(counts), counts)
			}
			if counts[0].Role != testCase.wantRole || counts[0].Subject != testCase.wantSubject {
				t.Errorf("count attached to (%s, %s), want (%s, %s)", counts[0].Role, counts[0].Subject, testCase.wantRole, testCase.wantSubject)
			}
		})
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("ran %d of %d cases", checked, len(cases))
	}
}

// TestScopedOperandContributesItsOwnCoordinates is round 3's finding,
// preserved as its red.
//
// SubjectOperand is a discriminated union of named and scoped (invariant
// I19). The declaration slice's derivation read the named arm only, so a
// scoped operand's cells silently vanished -- and every test stayed green
// because the artifact was rendered by the same function that dropped them.
// The construction here is the reviewer's: an explicit set whose SECOND
// operand is scoped.
func TestScopedOperandContributesItsOwnCoordinates(t *testing.T) {
	frame := frameWith(
		[]InvestigationGoal{GoalCompare},
		SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: kindPointer(SubjectTeam)}},
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"b"}, MemberKind: SubjectProject}},
			}},
		},
		TemporalIntentCurrent,
		nil,
	)

	subjects := map[SubjectKind]int{}
	for _, coordinate := range DeriveRequirementCoordinates(frame) {
		if coordinate.Role != SubjectRoleOperand {
			t.Errorf("explicit set produced role %q, want operand only", coordinate.Role)
			continue
		}
		subjects[coordinate.Subject]++
	}
	if subjects[SubjectTeam] == 0 {
		t.Error("the NAMED operand contributed no coordinate")
	}
	if subjects[SubjectProject] == 0 {
		t.Error("the SCOPED operand contributed no coordinate -- the operand union's second arm is being dropped, which is round 3's finding")
	}
	if len(subjects) != 2 {
		t.Fatalf("derived operand subjects %v, want exactly team and project", subjects)
	}
}

// TestAnchorContributesNoCoordinate asserts the design bound rather than
// leaving it in a comment.
//
// AnchorTerms are retrieval pointers, never values, so the anchor's subject
// kind is settled at resolution and the frame cannot name it. The test that
// matters is not "the anchor is absent" in the abstract -- it is that
// changing the anchor terms changes NOTHING about the derived coordinates,
// which is what "the frame does not know the anchor's kind" means
// operationally.
func TestAnchorContributesNoCoordinate(t *testing.T) {
	first := frameWith([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectProject), TemporalIntentCurrent, nil)
	second := frameWith([]InvestigationGoal{GoalAssessState}, SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"an entirely different anchor", "and a second one"}, MemberKind: SubjectProject},
	}, TemporalIntentCurrent, nil)

	firstRendered := RenderRequirementCoordinates("scoped", DeriveRequirementCoordinates(first))
	secondRendered := RenderRequirementCoordinates("scoped", DeriveRequirementCoordinates(second))
	if firstRendered != secondRendered {
		t.Fatalf("the anchor terms changed the derived coordinates, so something is treating a retrieval pointer as a value:\n%s\nvs\n%s", firstRendered, secondRendered)
	}
	if strings.Contains(firstRendered, "anchor") {
		t.Fatalf("an anchor coordinate reached the render:\n%s", firstRendered)
	}
	// And the member coordinate IS present, so the test above is not
	// passing because nothing was derived at all.
	if !strings.Contains(firstRendered, string(SubjectRoleMember)) {
		t.Fatalf("no member coordinate derived; the equality above proved nothing:\n%s", firstRendered)
	}
}

// TestAnswerContractObligationsDeriveNoCoordinate: evidence and coverage
// are satisfied by the answer contract itself and involve no subject, so a
// coordinate for them would be a cell nothing can ever serve -- an
// unavailable row manufactured by the derivation rather than measured.
func TestAnswerContractObligationsDeriveNoCoordinate(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectTeam), TemporalIntentCurrent, nil)
	if !frame.HasObligation(ObligationEvidence) || !frame.HasObligation(ObligationCoverage) {
		t.Fatalf("the frame does not carry the answer-contract obligations, so this test cannot see them: %v", frame.Obligations)
	}
	checked := 0
	for _, coordinate := range DeriveRequirementCoordinates(frame) {
		kind, known := KindOfObligation(coordinate.Obligation)
		if !known {
			t.Errorf("coordinate carries out-of-vocabulary obligation %q", coordinate.Obligation)
			continue
		}
		if kind == ObligationKindAnswerContract {
			t.Errorf("answer-contract obligation %q produced a coordinate", coordinate.Obligation)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no coordinates reached the assertions")
	}
}

// TestWidenedObligationsDeriveNoCoordinate: a model-widened obligation is
// ADVISORY and "may not degrade answer completeness" (§13.2.4 rule 1).
// Deriving a coordinate for one would put an advisory member into the same
// cell space as a required one, which is the confusion the two fields exist
// to prevent.
func TestWidenedObligationsDeriveNoCoordinate(t *testing.T) {
	frame := DeriveFrameObligations(QuestionFrame{
		Goals:             []InvestigationGoal{GoalAssessState},
		SubjectExpression: namedExpression(SubjectTeam),
		Temporal:          TemporalIntentCurrent,
		Version:           QuestionFrameVersion,
	}, []AnswerObligation{ObligationAllocationBreakdown})

	if len(frame.WidenedObligations) != 1 || frame.WidenedObligations[0] != ObligationAllocationBreakdown {
		t.Fatalf("the widening did not take, so this test cannot see it: %v", frame.WidenedObligations)
	}
	for _, coordinate := range DeriveRequirementCoordinates(frame) {
		if coordinate.Obligation == ObligationAllocationBreakdown {
			t.Fatalf("the widened obligation produced coordinate %+v", coordinate)
		}
	}
}

// TestNamedSubjectWithoutAnExpectedKindDerivesNothing: ExpectedKind is
// optional, and absent means "the kind is not constrained, which is a
// weaker claim than guessing one" (frame.go). Deriving a coordinate would
// require inventing the kind.
func TestNamedSubjectWithoutAnExpectedKindDerivesNothing(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalAssessState}, SubjectExpression{
		Kind:  SubjectExpressionNamed,
		Named: &NamedSubjectExpression{Terms: []string{"s"}},
	}, TemporalIntentCurrent, nil)
	if got := DeriveRequirementCoordinates(frame); len(got) != 0 {
		t.Fatalf("derived %d coordinates for a subject whose kind is unknown: %v", len(got), got)
	}
}

// TestCoordinateDerivationIsDeterministicAndDeduplicated: the regenerated
// artifact is only trustworthy if two runs of one frame produce an
// identical list. An explicit set whose two operands name the SAME kind is
// the case that exercises deduplication -- and it is deliberately a
// shared-identifier fixture, the shape that hid three aliasing defects in
// this program before it became a standing rule.
func TestCoordinateDerivationIsDeterministicAndDeduplicated(t *testing.T) {
	frame := frameWith([]InvestigationGoal{GoalCompare}, SubjectExpression{
		Kind: SubjectExpressionExplicitSet,
		Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
			{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: kindPointer(SubjectTeam)}},
			{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"b"}, ExpectedKind: kindPointer(SubjectTeam)}},
		}},
	}, TemporalIntentCurrent, nil)

	first := DeriveRequirementCoordinates(frame)
	second := DeriveRequirementCoordinates(frame)
	if RenderRequirementCoordinates("x", first) != RenderRequirementCoordinates("x", second) {
		t.Fatal("two derivations of one frame disagree; the artifact cannot be diffed")
	}
	seen := map[RequirementCoordinate]bool{}
	for _, coordinate := range first {
		if seen[coordinate] {
			t.Errorf("duplicate coordinate %+v: two operands of the same kind produced two identical cells", coordinate)
		}
		seen[coordinate] = true
	}
	if len(first) == 0 {
		t.Fatal("no coordinates derived; the deduplication assertion above was vacuous")
	}
}

// TestSubjectRoleVocabularyIsClosedAndTotal guards the two tables that must
// stay total over the role vocabulary. A role added without a completion
// scope would give every requirement in that role an empty scope, and a
// completeness claim is made against its scope.
func TestSubjectRoleVocabularyIsClosedAndTotal(t *testing.T) {
	checked := 0
	for _, role := range SubjectRoleVocabulary() {
		if !ValidSubjectRole(role) {
			t.Errorf("vocabulary member %q fails its own membership test", role)
		}
		if scopeForRole[role] == "" {
			t.Errorf("role %q has no completion scope", role)
		}
		checked++
	}
	if checked != SubjectRoleCount {
		t.Fatalf("checked %d roles, want %d", checked, SubjectRoleCount)
	}
	if ValidSubjectRole("") {
		t.Error("the empty value is a vocabulary member")
	}
}

// TestRankingAttachesToEachOperandOfAnExplicitSet pins a rule that had NO
// positive fixture, found by an adversarial review round.
//
// `attachesToRole` says `ranking` attaches to each operand when the frame has
// no member slot. Nothing exercised it: every explicit set in the corpus and
// in this file carries the `compare` goal, which derives no `ranking`
// obligation at all, so the operand arm of that rule was unreachable in every
// test. The reviewer's mutation -- return only `subject` for `ranking` --
// left the regenerated artifact byte-identical and the whole suite green.
//
// That is the same failure this package already guards against elsewhere: a
// rule no input reaches is untested code, and "0 cells" is indistinguishable
// from "the rule always answers no". The question is real -- rank the
// operands of an explicit comparison -- so the rule stays and gains the
// fixture that isolates it, rather than being removed as subsumed.
func TestRankingAttachesToEachOperandOfAnExplicitSet(t *testing.T) {
	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey},
		SubjectExpression{
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: kindPointer(SubjectTeam)}},
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"b"}, MemberKind: SubjectProject}},
			}},
		},
		TemporalIntentCurrent,
		nil,
	)
	if !frame.HasObligation(ObligationRanking) {
		t.Fatalf("the frame derives no ranking obligation, so this test cannot see the rule: %v", frame.Obligations)
	}

	subjects := map[SubjectKind]bool{}
	for _, coordinate := range DeriveRequirementCoordinates(frame) {
		if coordinate.Obligation != ObligationRanking {
			continue
		}
		if coordinate.Role != SubjectRoleOperand {
			t.Errorf("ranking attached to role %q on an explicit set; the operands are the population being ordered", coordinate.Role)
		}
		subjects[coordinate.Subject] = true
	}
	if !subjects[SubjectTeam] || !subjects[SubjectProject] {
		t.Fatalf("ranking reached operands %v, want BOTH -- an ordering over a comparison must cover every operand, and both operand variants must contribute", subjects)
	}
}

// everyVariantExpression builds one representative expression per variant,
// with a member kind supplied everywhere the variant admits one -- INCLUDING
// organization_scope, which is the pairing the recorded corpus never builds.
func everyVariantExpression() map[SubjectExpressionKind]SubjectExpression {
	member := SubjectRepository
	return map[SubjectExpressionKind]SubjectExpression{
		SubjectExpressionNamed:             namedExpression(SubjectTeam),
		SubjectExpressionGroupedMembers:    groupedExpression(SubjectProject, SubjectTeam),
		SubjectExpressionChildrenOfScope:   scopedExpression(SubjectProject),
		SubjectExpressionDiscoveredKind:    discoveredExpression(SubjectTeam),
		SubjectExpressionOrganizationScope: orgExpression(&member),
		SubjectExpressionExplicitSet: {
			Kind: SubjectExpressionExplicitSet,
			Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
				{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"a"}, ExpectedKind: kindPointer(SubjectTeam)}},
				{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"b"}, MemberKind: SubjectProject}},
			}},
		},
	}
}

// TestTheRoleRulesHoldOverEveryVariantAndGoalPair is the GENERATED SWEEP.
//
// WHY A SWEEP AND NOT ANOTHER FIXTURE. A review round found that
// {assess_state, organization_scope with a member kind} derived a
// per-repository read for an organization-level question. The corpus could
// not see it because C7 is organization+count+member-present and B5 is
// organization+assess_state+member-absent -- the pair is never built. And
// the coverage test guarding that corpus asked only that each VARIANT and
// each ROLE appear at least once, which is one-dimensional coverage over a
// two-dimensional matrix and accepts exactly this hole. Adding a fixture for
// the pair someone noticed would leave the next pair open.
//
// So every (variant x goal) pair the grammar allows is built and checked
// against PROPERTIES of the stated rules rather than against an expected
// coordinate list. Properties, deliberately: a second table of expected
// coordinates would be a second implementation of the rule, and a table
// beside the thing it checks is the co-editable-authority defect this slice
// has already paid for twice.
func TestTheRoleRulesHoldOverEveryVariantAndGoalPair(t *testing.T) {
	expressions := everyVariantExpression()
	pairs, coordinates := 0, 0

	for _, variant := range SubjectExpressionKindVocabulary() {
		expression, built := expressions[variant]
		if !built {
			t.Errorf("variant %q has no expression in the sweep: the closed union grew and the sweep did not", variant)
			continue
		}
		for _, goal := range InvestigationGoalVocabulary() {
			frame := frameWith([]InvestigationGoal{goal}, expression, TemporalIntentCurrent, nil)
			pairs++

			offered := map[SubjectRole]map[SubjectKind]bool{}
			for _, slot := range frameRoleSlots(frame.SubjectExpression) {
				if offered[slot.Role] == nil {
					offered[slot.Role] = map[SubjectKind]bool{}
				}
				offered[slot.Role][slot.Subject] = true
			}

			populations := map[AnswerObligation][]SubjectRole{}
			for _, coordinate := range DeriveRequirementCoordinates(frame) {
				coordinates++
				where := string(variant) + "/" + string(goal) + " " + string(coordinate.Obligation)

				// P1: a coordinate may only name a (role, subject) the
				// topology actually offers. Anything else is invented.
				if !offered[coordinate.Role][coordinate.Subject] {
					t.Errorf("%s: derived (%s, %s), which this variant does not offer", where, coordinate.Role, coordinate.Subject)
				}

				kind, known := KindOfObligation(coordinate.Obligation)
				// P2: answer-contract obligations never yield a coordinate.
				if !known || kind == ObligationKindAnswerContract {
					t.Errorf("%s: an answer-contract obligation produced a coordinate", where)
					continue
				}
				// P3: only `state` reaches a grouping axis.
				if coordinate.Role == SubjectRoleGroup && coordinate.Obligation != ObligationState {
					t.Errorf("%s: attached to the group axis; only state does", where)
				}
				// P4: the organization's member slot serves POPULATION
				// obligations only -- it names a counted entity kind.
				if variant == SubjectExpressionOrganizationScope && coordinate.Role == SubjectRoleMember &&
					coordinate.Obligation != ObligationCount && coordinate.Obligation != ObligationRanking {
					t.Errorf("%s: a read obligation attached to the organization's counted-member slot", where)
				}
				if kind == ObligationKindComputed {
					populations[coordinate.Obligation] = append(populations[coordinate.Obligation], coordinate.Role)
				}
			}

			// P5: a computed obligation attaches within ONE role, never
			// across several.
			//
			// THE FIRST VERSION OF THIS PROPERTY WAS WRONG AND THE SWEEP SAID
			// SO. It required at most one coordinate per computed obligation,
			// which fails on an explicit comparison: "how many X does team A
			// have versus team B" counts EACH operand, and a ranking over a
			// comparison orders each operand too -- a rule this package
			// already pins in TestRankingAttachesToEachOperandOfAnExplicitSet.
			// The real invariant, and the one design finding S3 names, is that
			// a cardinality must not MULTIPLY ACROSS ROLE KINDS: not member
			// and group and subject at once. Several operands are one role.
			//
			// The property was corrected rather than the code, because the
			// code was right. A sweep whose disagreements are resolved by
			// editing the subject is not a sweep.
			for obligation, roles := range populations {
				first := roles[0]
				for _, role := range roles[1:] {
					if role != first {
						t.Errorf("%s/%s: computed obligation %s attached to roles %v; a cardinality names one kind of population",
							variant, goal, obligation, roles)
						break
					}
				}
			}
		}
	}

	want := SubjectExpressionKindCount * InvestigationGoalCount
	if pairs != want {
		t.Fatalf("swept %d pairs, want %d -- the sweep is not covering the grammar it claims to", pairs, want)
	}
	if coordinates == 0 {
		t.Fatal("no coordinates reached the assertions across the whole sweep")
	}
	t.Logf("swept %d (variant x goal) pairs, %d coordinates", pairs, coordinates)
}
