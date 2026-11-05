package contextfabric

import "testing"

func readFallbackTestCapabilities() []FactCapability {
	return []FactCapability{
		{Kind: FactMetrics, SupportedSubjectKinds: []SubjectKind{SubjectRepository, SubjectTeam, SubjectProject}},
		{Kind: FactHealth, SupportedSubjectKinds: []SubjectKind{SubjectRepository, SubjectTeam, SubjectProject}},
		{Kind: FactBlockers, SupportedSubjectKinds: []SubjectKind{SubjectWorkItem, SubjectTeam, SubjectProject}},
		{Kind: FactFileHotspots, SupportedSubjectKinds: []SubjectKind{SubjectRepository}},
		{Kind: FactStatus, SupportedSubjectKinds: []SubjectKind{SubjectWorkItem}},
		{Kind: FactSourceHealth, SupportedSubjectKinds: []SubjectKind{SubjectOrganization}},
		{Kind: FactCommitment, SupportedSubjectKinds: []SubjectKind{SubjectProject}},
		{Kind: FactLandscape, SupportedSubjectKinds: []SubjectKind{SubjectTeam, SubjectProject}},
	}
}

func readFallbackTestDefinition(t *testing.T, family QuestionFamily) QuestionFamilyDefinition {
	t.Helper()
	definition, ok := LookupQuestionFamily(family)
	if !ok {
		t.Fatalf("family %q is not in the registry", family)
	}
	return definition
}

// TestDecideReadFallback is the family-fit table: F1 (a ranking family asked
// for a judgment its attention measure cannot serve) and the static F3 (a
// listed kind no capability serves for the member kind), with the controls
// that must never fire.
func TestDecideReadFallback(t *testing.T) {
	t.Parallel()
	ranking := readFallbackTestDefinition(t, QuestionFamilyDiscoveredCohortRanking)
	scoped := readFallbackTestDefinition(t, QuestionFamilyScopedCohortStatus)
	subject := readFallbackTestDefinition(t, QuestionFamilySubjectInvestigation)
	capabilities := readFallbackTestCapabilities()
	for _, tc := range []struct {
		name  string
		input ReadFallbackDecisionInput
		want  ReadFallbackReason
	}{
		{
			name: "cycle time: discovered_cohort_ranking asked for performance",
			input: ReadFallbackDecisionInput{Definition: ranking, JudgmentKind: RequestedJudgmentKindPerformance,
				InterpretedKinds: []FactKind{FactBlockers}, MemberKind: SubjectTeam, Capabilities: capabilities},
			want: ReadFallbackReasonJudgmentUnservable,
		},
		{
			name: "control: the same family asked for attention",
			input: ReadFallbackDecisionInput{Definition: ranking, JudgmentKind: RequestedJudgmentKindAttention,
				InterpretedKinds: []FactKind{FactBlockers}, MemberKind: SubjectTeam, Capabilities: capabilities},
		},
		{
			name: "control: performance on a family that does not rank",
			input: ReadFallbackDecisionInput{Definition: scoped, JudgmentKind: RequestedJudgmentKindPerformance,
				InterpretedKinds: []FactKind{FactBlockers}, MemberKind: SubjectTeam, Capabilities: capabilities},
		},
		{
			name: "control: a ranking family with no judgment kind",
			input: ReadFallbackDecisionInput{Definition: ranking,
				InterpretedKinds: []FactKind{FactBlockers}, MemberKind: SubjectTeam, Capabilities: capabilities},
		},
		{
			name: "control: a judgment kind this build does not know",
			input: ReadFallbackDecisionInput{Definition: ranking, JudgmentKind: "productivity",
				InterpretedKinds: []FactKind{FactBlockers}, MemberKind: SubjectTeam, Capabilities: capabilities},
		},
		{
			name: "control: no member kind to plan reads for",
			input: ReadFallbackDecisionInput{Definition: ranking, JudgmentKind: RequestedJudgmentKindPerformance,
				InterpretedKinds: []FactKind{FactBlockers}, Capabilities: capabilities},
		},
		{
			name: "control: a work-item walk reads its own fixed kinds",
			input: ReadFallbackDecisionInput{Definition: ranking, JudgmentKind: RequestedJudgmentKindPerformance,
				InterpretedKinds: []FactKind{FactBlockers}, MemberKind: SubjectTeam, Capabilities: capabilities, WorkItemList: true},
		},
		{
			name: "F3: a listed kind no capability serves for the member kind",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactHealth, FactFileHotspots}, MemberKind: SubjectTeam, Capabilities: capabilities},
			want: ReadFallbackReasonKindUnservedForMember,
		},
		{
			name: "F3 control: every listed kind is served for the member kind",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactHealth, FactMetrics}, MemberKind: SubjectTeam, Capabilities: capabilities},
		},
		{
			name: "F3 control: a kind an enabled fact-scope policy reaches from the member kind",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactStatus}, MemberKind: SubjectTeam, Capabilities: capabilities},
		},
		{
			name: "F3: a fact-scope rule from the member kind that is not enabled reaches nothing",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactCommitment}, MemberKind: SubjectTeam, Capabilities: capabilities},
			want: ReadFallbackReasonKindUnservedForMember,
		},
		{
			name: "F3: a fact-scope row from the member kind that names no policy reaches nothing",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactHealth}, MemberKind: SubjectProject,
				Capabilities: []FactCapability{{Kind: FactHealth, SupportedSubjectKinds: []SubjectKind{SubjectRepository, SubjectTeam}}}},
			want: ReadFallbackReasonKindUnservedForMember,
		},
		{
			name: "F3: an enabled fact-scope row that names no policy reaches nothing",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactFileHotspots}, MemberKind: SubjectTeam, Capabilities: capabilities,
				scopePolicies: map[FactKind]map[SubjectKind]factScopePolicyRule{FactFileHotspots: {SubjectTeam: {Policy: FactScopePolicyNone, Enabled: true}}}},
			want: ReadFallbackReasonKindUnservedForMember,
		},
		{
			name: "F3 control: the table the decision reads is the one it was given",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactFileHotspots}, MemberKind: SubjectTeam, Capabilities: capabilities,
				scopePolicies: map[FactKind]map[SubjectKind]factScopePolicyRule{FactFileHotspots: {SubjectTeam: {Policy: FactScopePolicyTeamPrimaryAttributionRepository, Enabled: true}}}},
		},
		{
			name: "F3 control: a listed kind with no registered capability",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactEvidence}, MemberKind: SubjectTeam, Capabilities: capabilities},
		},
		{
			name: "F3 control: no capabilities declared",
			input: ReadFallbackDecisionInput{Definition: subject,
				InterpretedKinds: []FactKind{FactFileHotspots}, MemberKind: SubjectTeam},
		},
		{
			name: "F1 wins over F3 when both hold",
			input: ReadFallbackDecisionInput{Definition: ranking, JudgmentKind: RequestedJudgmentKindPerformance,
				InterpretedKinds: []FactKind{FactFileHotspots}, MemberKind: SubjectTeam, Capabilities: capabilities},
			want: ReadFallbackReasonJudgmentUnservable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DecideReadFallback(tc.input); got != tc.want {
				t.Fatalf("DecideReadFallback() = %q, want %q", got, tc.want)
			}
		})
	}
}
