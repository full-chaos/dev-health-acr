package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// roleKindPtr returns a pointer to kind, for the optional kind fields.
func roleKindPtr(kind SubjectKind) *SubjectKind { return &kind }

// roleScopedFrame builds a children_of_scope frame with anchor terms.
func roleScopedFrame(member SubjectKind) *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:   SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{"anchor-term"}, MemberKind: member},
	}}
}

// roleGroupedFrame builds a grouped_members frame.
func roleGroupedFrame(member, group SubjectKind) *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:    SubjectExpressionGroupedMembers,
		Grouped: &GroupedSetExpression{MemberKind: member, GroupKind: group},
	}}
}

// roleDiscoveredFrame builds a discovered_kind frame.
func roleDiscoveredFrame(member SubjectKind) *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:       SubjectExpressionDiscoveredKind,
		Discovered: &DiscoveredSetExpression{MemberKind: member},
	}}
}

// roleOrgFrame builds an organization_scope frame.
func roleOrgFrame(member *SubjectKind) *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind: SubjectExpressionOrganizationScope,
		Org:  &OrganizationScopeExpression{MemberKind: member},
	}}
}

// roleExplicitFrame builds an explicit_set frame over the given operands.
func roleExplicitFrame(operands ...SubjectOperand) *QuestionFrame {
	return &QuestionFrame{SubjectExpression: SubjectExpression{
		Kind:     SubjectExpressionExplicitSet,
		Explicit: &ExplicitSetExpression{Operands: operands},
	}}
}

func roleNamedOperand(expected *SubjectKind) SubjectOperand {
	return SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"operand-term"}, ExpectedKind: expected}}
}

func roleScopedOperand(member SubjectKind) SubjectOperand {
	return SubjectOperand{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"operand-anchor"}, MemberKind: member}}
}

// roleOffer is one offer of kind on channel.
func roleOffer(channel answerabilityChannel, kind SubjectKind) answerabilityOffer {
	return answerabilityOffer{Channel: channel, Kind: kind}
}

const (
	roleTeam       = SubjectTeam
	roleProject    = SubjectProject
	roleRepository = SubjectRepository
	roleCIRun      = contractsv1.ContextFabricSubjectCIRun
	rolePR         = contractsv1.ContextFabricSubjectPullRequest
	roleOOV        = SubjectKind("a_kind_no_vocabulary_names")
)

// TestAnswerabilityRolesOverTheWholeReadingDomain executes every cell of the
// reading side in one pass: every union variant, every pointer the
// discriminator names absent, every kind field absent / empty /
// out-of-vocabulary / canonical, and every anchor-kind cell the retrieval gate
// distinguishes. Each cell asserts the rendered role list, which is what the
// log line carries, so a derivation that moved a role or its state fails here.
func TestAnswerabilityRolesOverTheWholeReadingDomain(t *testing.T) {
	t.Parallel()
	empty := SubjectKind("")
	for _, testCase := range []struct {
		cell       string
		frame      *QuestionFrame
		anchorKind SubjectKind
		want       string
	}{
		{"frame absent", nil, "", "none"},
		{"variant out of vocabulary", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionKind("not_a_variant")}}, "", "none"},
		{"variant empty", &QuestionFrame{}, "", "none"},

		{"named, pointer absent", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed}}, "", "none"},
		{"named, ExpectedKind absent", chaos5660NamedFrame(nil), "", "subject:undeclared:open"},
		{"named, ExpectedKind empty", chaos5660NamedFrame(&empty), "", "subject:undeclared:open"},
		{"named, ExpectedKind out of vocabulary", chaos5660NamedFrame(roleKindPtr(roleOOV)), "", "subject:undeclared:open"},
		{"named, ExpectedKind canonical", chaos5660NamedFrame(roleKindPtr(roleProject)), "", "subject:project:open"},
		{"named, an anchor kind is ignored", chaos5660NamedFrame(roleKindPtr(roleProject)), roleTeam, "subject:project:open"},

		{"discovered, pointer absent", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind}}, "", "none"},
		{"discovered, MemberKind empty", roleDiscoveredFrame(""), "", "member:undeclared:open"},
		{"discovered, MemberKind out of vocabulary", roleDiscoveredFrame(roleOOV), "", "member:undeclared:open"},
		{"discovered, MemberKind canonical", roleDiscoveredFrame(roleTeam), "", "member:team:open"},

		{"scoped, pointer absent", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionChildrenOfScope}}, roleTeam, "none"},
		{"scoped, anchor kind absent", roleScopedFrame(roleProject), "", "anchor:undeclared:open,member:project:population"},
		{"scoped, anchor kind out of vocabulary", roleScopedFrame(roleProject), roleOOV, "anchor:undeclared:open,member:project:population"},
		{"scoped, anchor kind equal to the member kind", roleScopedFrame(roleProject), roleProject, "anchor:undeclared:open,member:project:population"},
		{"scoped, anchor kind canonical", roleScopedFrame(roleProject), roleTeam, "anchor:team:open,member:project:population"},
		{"scoped, anchor kind canonical, no anchor terms", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{MemberKind: roleProject}}}, roleTeam, "anchor:undeclared:open,member:project:population"},
		{"scoped, MemberKind empty", roleScopedFrame(""), roleTeam, "anchor:team:open,member:undeclared:population"},
		{"scoped, repository anchor over team members", roleScopedFrame(roleTeam), roleRepository, "anchor:repository:open,member:team:population"},

		{"grouped, pointer absent", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionGroupedMembers}}, "", "none"},
		{"grouped, both axes canonical", roleGroupedFrame(roleProject, roleTeam), "", "member:project:population,group:team:population"},
		{"grouped, both axes empty", roleGroupedFrame("", ""), "", "member:undeclared:population,group:undeclared:population"},

		{"explicit, pointer absent", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet}}, "", "none"},
		{"explicit, no operands", roleExplicitFrame(), "", "none"},
		{"explicit, two named operands", roleExplicitFrame(roleNamedOperand(roleKindPtr(roleProject)), roleNamedOperand(nil)), "", "operand:project:open,operand:undeclared:open"},
		{"explicit, a scoped operand", roleExplicitFrame(roleScopedOperand(roleProject)), "", "operand:undeclared:open"},
		{"explicit, operand discriminator out of vocabulary", roleExplicitFrame(SubjectOperand{Kind: SubjectOperandKind("nope"), Named: &NamedSubjectExpression{ExpectedKind: roleKindPtr(roleProject)}}), "", "none"},
		{"explicit, named discriminator with its pointer absent", roleExplicitFrame(SubjectOperand{Kind: SubjectOperandNamed, Scoped: &ScopedSetExpression{MemberKind: roleProject}}), "", "none"},
		{"explicit, scoped discriminator with its pointer absent", roleExplicitFrame(SubjectOperand{Kind: SubjectOperandScoped, Named: &NamedSubjectExpression{ExpectedKind: roleKindPtr(roleProject)}}), "", "none"},

		{"org, pointer absent", &QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope}}, "", "none"},
		{"org, MemberKind absent", roleOrgFrame(nil), "", "subject:organization:resolved"},
		{"org, MemberKind empty", roleOrgFrame(&empty), "", "subject:organization:resolved,member:undeclared:population"},
		{"org, MemberKind canonical", roleOrgFrame(roleKindPtr(roleProject)), "", "subject:organization:resolved,member:project:population"},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			decision := decideAnswerability(answerabilityReadingOf(testCase.frame, testCase.anchorKind), nil)
			if got := decision.ObservableAnswerability().EvaluatedRoles; got != testCase.want {
				t.Fatalf("evaluated_roles = %q, want %q", got, testCase.want)
			}
			if decision.Unsatisfiable {
				t.Fatal("a decision with no offer reported unsatisfiable -- that state is CHAOS-5637's")
			}
		})
	}
}

// TestAnswerabilityOverTheWholeOfferDomain crosses every reading that has a
// role with every offer cell: absent, empty list, empty kind, out of
// vocabulary, duplicate, the role's own kind, a kind of a role no pick
// decides, and an unrelated kind -- asserting the verdict AND the role the
// winning offer advanced, so a decision that reached the right verdict for the
// wrong role fails.
func TestAnswerabilityOverTheWholeOfferDomain(t *testing.T) {
	t.Parallel()
	scopedTeamAnchor := answerabilityReadingOf(roleScopedFrame(roleProject), roleTeam)
	scopedUnknownAnchor := answerabilityReadingOf(roleScopedFrame(roleProject), "")
	repositoryAnchor := answerabilityReadingOf(roleScopedFrame(roleTeam), roleRepository)
	grouped := answerabilityReadingOf(roleGroupedFrame(roleProject, roleTeam), "")
	named := answerabilityReadingOf(chaos5660NamedFrame(roleKindPtr(roleProject)), "")
	namedUndeclared := answerabilityReadingOf(chaos5660NamedFrame(nil), "")
	discovered := answerabilityReadingOf(roleDiscoveredFrame(roleTeam), "")
	org := answerabilityReadingOf(roleOrgFrame(roleKindPtr(SubjectOrganization)), "")
	orgNoMember := answerabilityReadingOf(roleOrgFrame(nil), "")
	operands := answerabilityReadingOf(roleExplicitFrame(roleNamedOperand(roleKindPtr(roleProject)), roleNamedOperand(roleKindPtr(roleRepository))), "")
	scopedOperand := answerabilityReadingOf(roleExplicitFrame(roleScopedOperand(roleProject)), "")
	noFrame := answerabilityReadingOf(nil, "")

	candidate := answerabilityChannelSubjectCandidate
	for _, testCase := range []struct {
		cell          string
		reading       answerabilityReading
		offers        []answerabilityOffer
		unsatisfiable bool
		advanced      string
		channel       string
	}{
		{"scoped team anchor, offers absent", scopedTeamAnchor, nil, false, "none", "none"},
		{"scoped team anchor, offers empty list", scopedTeamAnchor, []answerabilityOffer{}, false, "none", "none"},
		{"scoped team anchor, one offer with empty kind", scopedTeamAnchor, []answerabilityOffer{roleOffer(candidate, "")}, false, "none", "none"},
		{"scoped team anchor, team candidates (the measured shape)", scopedTeamAnchor, []answerabilityOffer{roleOffer(candidate, roleTeam), roleOffer(candidate, roleTeam)}, false, "anchor:team", "subject_candidate"},
		{"scoped team anchor, member-kind candidates only", scopedTeamAnchor, []answerabilityOffer{roleOffer(candidate, roleProject)}, true, "none", "none"},
		{"scoped team anchor, unrelated kind only", scopedTeamAnchor, []answerabilityOffer{roleOffer(answerabilityChannelHandleOption, rolePR)}, true, "none", "none"},
		{"scoped team anchor, out-of-vocabulary kind only", scopedTeamAnchor, []answerabilityOffer{roleOffer(candidate, roleOOV)}, true, "none", "none"},
		{"scoped team anchor, kind option of the anchor kind first", scopedTeamAnchor, []answerabilityOffer{roleOffer(answerabilityChannelKindOption, roleProject), roleOffer(answerabilityChannelKindOption, roleTeam), roleOffer(candidate, roleTeam)}, false, "anchor:team", "kind_option"},
		{"scoped team anchor, anchor option", scopedTeamAnchor, []answerabilityOffer{roleOffer(answerabilityChannelAnchorOption, roleTeam)}, false, "anchor:team", "anchor_option"},
		{"scoped unknown anchor, team candidate", scopedUnknownAnchor, []answerabilityOffer{roleOffer(candidate, roleTeam)}, false, "anchor:team", "subject_candidate"},
		{"scoped unknown anchor, member-kind candidate only", scopedUnknownAnchor, []answerabilityOffer{roleOffer(candidate, roleProject)}, true, "none", "none"},
		{"scoped unknown anchor, out-of-vocabulary candidate", scopedUnknownAnchor, []answerabilityOffer{roleOffer(candidate, roleOOV)}, false, "anchor:" + string(roleOOV), "subject_candidate"},
		{"repository anchor over team members, repository candidate", repositoryAnchor, []answerabilityOffer{roleOffer(candidate, roleRepository)}, false, "anchor:repository", "subject_candidate"},
		{"repository anchor over team members, team candidate only", repositoryAnchor, []answerabilityOffer{roleOffer(candidate, roleTeam)}, true, "none", "none"},
		{"grouped, group-kind candidates", grouped, []answerabilityOffer{roleOffer(candidate, roleTeam)}, true, "none", "none"},
		{"grouped, member-kind candidates", grouped, []answerabilityOffer{roleOffer(candidate, roleProject)}, true, "none", "none"},
		{"grouped, kind options restating both declared axes", grouped, []answerabilityOffer{roleOffer(answerabilityChannelKindOption, roleProject), roleOffer(answerabilityChannelKindOption, roleTeam)}, true, "none", "none"},
		{"named, declared-kind candidate", named, []answerabilityOffer{roleOffer(candidate, roleCIRun), roleOffer(candidate, roleProject)}, false, "subject:project", "subject_candidate"},
		{"named, wrong kinds only (the CHAOS-5660 shape)", named, []answerabilityOffer{roleOffer(answerabilityChannelHandleOption, roleCIRun), roleOffer(answerabilityChannelCandidateOption, rolePR)}, true, "none", "none"},
		{"named, duplicates of the wrong kind", named, []answerabilityOffer{roleOffer(candidate, roleCIRun), roleOffer(candidate, roleCIRun)}, true, "none", "none"},
		{"named undeclared, any kind", namedUndeclared, []answerabilityOffer{roleOffer(candidate, roleCIRun)}, false, "subject:ci_pipeline_run", "subject_candidate"},
		{"discovered, member-kind candidate", discovered, []answerabilityOffer{roleOffer(candidate, roleTeam)}, false, "member:team", "subject_candidate"},
		{"discovered, other kind only", discovered, []answerabilityOffer{roleOffer(candidate, roleProject)}, true, "none", "none"},
		{"org with organization member, the measured offers", org, []answerabilityOffer{roleOffer(candidate, contractsv1.ContextFabricSubjectPullRequestReview), roleOffer(candidate, roleCIRun), roleOffer(candidate, roleProject)}, true, "none", "none"},
		{"org with organization member, an organization-kind offer", org, []answerabilityOffer{roleOffer(candidate, SubjectOrganization)}, true, "none", "none"},
		{"org without member kind, a project candidate", orgNoMember, []answerabilityOffer{roleOffer(candidate, roleProject)}, true, "none", "none"},
		{"operands, second operand's kind", operands, []answerabilityOffer{roleOffer(candidate, roleCIRun), roleOffer(candidate, roleRepository)}, false, "operand:repository", "subject_candidate"},
		{"operands, no operand's kind", operands, []answerabilityOffer{roleOffer(candidate, roleCIRun)}, true, "none", "none"},
		{"scoped operand, a team candidate", scopedOperand, []answerabilityOffer{roleOffer(candidate, roleTeam)}, false, "operand:team", "subject_candidate"},
		{"scoped operand, member-kind candidate only", scopedOperand, []answerabilityOffer{roleOffer(candidate, roleProject)}, true, "none", "none"},
		{"no frame, any offer", noFrame, []answerabilityOffer{roleOffer(candidate, roleCIRun)}, false, "none", "none"},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			decision := decideAnswerability(testCase.reading, testCase.offers)
			if decision.Unsatisfiable != testCase.unsatisfiable {
				t.Fatalf("Unsatisfiable = %v, want %v (roles %s, offered %v)", decision.Unsatisfiable, testCase.unsatisfiable,
					decision.ObservableAnswerability().EvaluatedRoles, decision.OfferedKinds)
			}
			observed := decision.ObservableAnswerability()
			if observed.AdvancedRole != testCase.advanced || observed.AdvancingChannel != testCase.channel {
				t.Fatalf("advanced_role/advancing_channel = %q/%q, want %q/%q", observed.AdvancedRole, observed.AdvancingChannel, testCase.advanced, testCase.channel)
			}
		})
	}
}

// TestAnswerabilityCountsAreMeasuredNotInferred holds the two counts apart:
// an empty-kind offer is not evaluated, a non-advancing offer is evaluated
// but not advancing, and a duplicate is counted each time it was offered.
func TestAnswerabilityCountsAreMeasuredNotInferred(t *testing.T) {
	t.Parallel()
	decision := decideAnswerability(answerabilityReadingOf(roleScopedFrame(roleProject), roleTeam), []answerabilityOffer{
		roleOffer(answerabilityChannelKindOption, ""),
		roleOffer(answerabilityChannelKindOption, roleProject),
		roleOffer(answerabilityChannelSubjectCandidate, roleTeam),
		roleOffer(answerabilityChannelSubjectCandidate, roleTeam),
		roleOffer(answerabilityChannelSubjectCandidate, rolePR),
	})
	if decision.OffersEvaluated != 4 || decision.OffersAdvancing != 2 {
		t.Fatalf("offers evaluated/advancing = %d/%d, want 4/2", decision.OffersEvaluated, decision.OffersAdvancing)
	}
	if got := decision.ObservableOfferedKinds(); got != "project,team,pull_request" {
		t.Fatalf("offered_kinds = %q, want each kind once, first-seen", got)
	}
}

// TestTheTurnAdapterReadsEveryChannelInOrder executes the composing-turn
// adapter over all five channels at once and asserts the channel order the
// winning offer is chosen in.
func TestTheTurnAdapterReadsEveryChannelInOrder(t *testing.T) {
	t.Parallel()
	offers := answerabilityOffersOfTurn(SubjectResolution{Candidates: []SubjectCandidate{chaos5660SubjectCandidate(roleTeam)}}, StructureOfferMaterial{
		KindOptions:      []contractsv1.ContextFabricKindOption{chaos5660KindOption(roleProject)},
		AnchorOptions:    []contractsv1.ContextFabricAnchorOption{chaos5660AnchorOption(roleRepository)},
		HandleOptions:    []contractsv1.ContextFabricHandleOption{chaos5660HandleOption(rolePR)},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{chaos5660CandidateOption(roleCIRun)},
	})
	want := []answerabilityOffer{
		{answerabilityChannelKindOption, roleProject},
		{answerabilityChannelAnchorOption, roleRepository},
		{answerabilityChannelHandleOption, rolePR},
		{answerabilityChannelCandidateOption, roleCIRun},
		{answerabilityChannelSubjectCandidate, roleTeam},
	}
	if len(offers) != len(want) {
		t.Fatalf("offers = %v, want %v", offers, want)
	}
	for index := range want {
		if offers[index] != want[index] {
			t.Fatalf("offers[%d] = %v, want %v", index, offers[index], want[index])
		}
	}
}

// TestEveryAnswerabilityTokenHasAProductionDriver enumerates each closed
// vocabulary FROM ITS DECLARATION and fails when any member is never produced
// by the production derivation over a reading/offer sweep. A member nothing
// produces is a token the log line can never carry.
func TestEveryAnswerabilityTokenHasAProductionDriver(t *testing.T) {
	t.Parallel()
	readings := []answerabilityReading{
		answerabilityReadingOf(chaos5660NamedFrame(roleKindPtr(roleProject)), ""),
		answerabilityReadingOf(roleScopedFrame(roleProject), roleTeam),
		answerabilityReadingOf(roleGroupedFrame(roleProject, roleTeam), ""),
		answerabilityReadingOf(roleExplicitFrame(roleNamedOperand(nil)), ""),
		answerabilityReadingOf(roleOrgFrame(roleKindPtr(roleProject)), ""),
		answerabilityReadingOf(roleDiscoveredFrame(roleTeam), ""),
	}
	material := StructureOfferMaterial{
		KindOptions:      []contractsv1.ContextFabricKindOption{chaos5660KindOption(roleProject)},
		AnchorOptions:    []contractsv1.ContextFabricAnchorOption{chaos5660AnchorOption(roleTeam)},
		HandleOptions:    []contractsv1.ContextFabricHandleOption{chaos5660HandleOption(roleProject)},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{chaos5660CandidateOption(roleProject)},
	}
	roles := map[answerabilityRole]bool{}
	states := map[answerabilityRoleState]bool{}
	channels := map[answerabilityChannel]bool{}
	for _, reading := range readings {
		for _, slot := range reading.slots() {
			roles[slot.Role] = true
			states[slot.State] = true
		}
		// One channel at a time, so each channel can be the winning one.
		for _, single := range []StructureOfferMaterial{
			{KindOptions: material.KindOptions}, {AnchorOptions: material.AnchorOptions},
			{HandleOptions: material.HandleOptions}, {CandidateOptions: material.CandidateOptions}, {},
		} {
			resolution := SubjectResolution{Candidates: []SubjectCandidate{chaos5660SubjectCandidate(roleTeam)}}
			if len(single.KindOptions)+len(single.AnchorOptions)+len(single.HandleOptions)+len(single.CandidateOptions) > 0 {
				resolution = SubjectResolution{}
			}
			decision := decideDeclaredKind(reading.Frame, reading.AnchorKind, resolution, single)
			if decision.Advance != nil {
				channels[decision.Advance.Channel] = true
			}
		}
	}
	for _, role := range answerabilityRoles {
		if !roles[role] {
			t.Errorf("role %q is never derived", role)
		}
	}
	for _, state := range answerabilityRoleStates {
		if !states[state] {
			t.Errorf("role state %q is never derived", state)
		}
	}
	for _, channel := range answerabilityChannels {
		if !channels[channel] {
			t.Errorf("channel %q never carries a winning offer", channel)
		}
	}
}

// TestOrganizationScopeUnsupportedOverItsWholeDomain executes the D48 predicate
// over every variant and every goal-set cell that decides it.
func TestOrganizationScopeUnsupportedOverItsWholeDomain(t *testing.T) {
	t.Parallel()
	project := SubjectProject
	withGoals := func(frame *QuestionFrame, goals ...InvestigationGoal) *QuestionFrame {
		frame.Goals = goals
		return frame
	}
	for _, testCase := range []struct {
		cell  string
		frame *QuestionFrame
		want  bool
	}{
		{"frame absent", nil, false},
		{"org, goals absent", withGoals(roleOrgFrame(nil)), true},
		{"org, assess_state", withGoals(roleOrgFrame(nil), GoalAssessState), true},
		{"org, explain_drivers", withGoals(roleOrgFrame(nil), GoalExplainDrivers), true},
		{"org, count_or_aggregate", withGoals(roleOrgFrame(&project), GoalCountOrAggregate), false},
		{"org, count_or_aggregate beside assess_state", withGoals(roleOrgFrame(&project), GoalAssessState, GoalCountOrAggregate), true},
		{"org, count_or_aggregate beside explain_drivers", withGoals(roleOrgFrame(&project), GoalCountOrAggregate, GoalExplainDrivers), true},
		{"org, count_or_aggregate twice", withGoals(roleOrgFrame(&project), GoalCountOrAggregate, GoalCountOrAggregate), false},
		{"org, pointer absent, assess_state", withGoals(&QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope}}, GoalAssessState), true},
		{"named, assess_state", withGoals(chaos5660NamedFrame(&project), GoalAssessState), false},
		{"discovered, assess_state", withGoals(roleDiscoveredFrame(SubjectTeam), GoalAssessState), false},
		{"scoped, assess_state", withGoals(roleScopedFrame(SubjectProject), GoalAssessState), false},
		{"grouped, assess_state", withGoals(roleGroupedFrame(SubjectProject, SubjectTeam), GoalAssessState), false},
		{"explicit, assess_state", withGoals(roleExplicitFrame(roleNamedOperand(&project)), GoalAssessState), false},
		{"variant out of vocabulary", withGoals(&QuestionFrame{SubjectExpression: SubjectExpression{Kind: SubjectExpressionKind("not_a_variant")}}, GoalAssessState), false},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			t.Parallel()
			if got := organizationScopeUnsupported(testCase.frame); got != testCase.want {
				t.Fatalf("organizationScopeUnsupported = %v, want %v", got, testCase.want)
			}
			if got := decideAnswerability(answerabilityReadingOf(testCase.frame, ""), nil).OrganizationScopeUnsupported; got != testCase.want {
				t.Fatalf("decision.OrganizationScopeUnsupported = %v, want %v -- the decision must carry the predicate it is taken on", got, testCase.want)
			}
		})
	}
}

// TestTheOrganizationScopeBasisOverItsWholeVocabularyDomain executes the new
// member's own domain on both sides of every rule that classifies a basis.
func TestTheOrganizationScopeBasisOverItsWholeVocabularyDomain(t *testing.T) {
	t.Parallel()
	basis := organizationScopeTerminalBasis
	if basis != contractsv1.ContextFabricRefusalBasisOrganizationScopeUnsupported || string(basis) != "organization_scope_unsupported" {
		t.Fatalf("organizationScopeTerminalBasis = %q", basis)
	}
	if !contractsv1.ValidContextFabricRefusalBasis(basis) {
		t.Fatalf("%q is not a vocabulary member", basis)
	}
	seen := 0
	for _, member := range contractsv1.ContextFabricRefusalBasisVocabulary() {
		if member == basis {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("member occurs %d times in the vocabulary, want exactly 1", seen)
	}
	if contractsv1.ValidContextFabricFrameRefusalBasis(basis) {
		t.Fatal("admitted to the FRAME refusal allow-list: the frame validated and its gate passed")
	}
	if basis == contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched || organizationScopeTerminalLimitation == declaredKindTerminalLimitation {
		t.Fatal("shares a basis or a sentence with declared_kind_unmatched -- two decisions would read alike")
	}
	for _, nearMiss := range []contractsv1.ContextFabricRefusalBasis{"organization_scope_unsupported_", "ORGANIZATION_SCOPE_UNSUPPORTED", "organization_scope", ""} {
		if contractsv1.ValidContextFabricRefusalBasis(nearMiss) {
			t.Fatalf("near miss %q was accepted", nearMiss)
		}
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(organizationScopeTerminalLimitation) {
		t.Fatalf("the sentence is not service-authored: %q", organizationScopeTerminalLimitation)
	}
	for _, fragment := range []string{string(basis), "not supported", "counts"} {
		if !strings.Contains(organizationScopeTerminalLimitation, fragment) {
			t.Fatalf("the sentence lacks %q: it must name its basis, what is not supported, and what is", fragment)
		}
	}
	if observableRefusalBasis(basis) != string(basis) {
		t.Fatal("the log renderer and the wire disagree")
	}
}
