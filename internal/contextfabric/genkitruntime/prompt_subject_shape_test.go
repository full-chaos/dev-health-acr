package genkitruntime

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestInterpretationPromptSubjectRulesStatedOnce(t *testing.T) {
	t.Parallel()
	for _, rule := range []string{
		`shape, subject_terms, comparison_terms, scope_anchor_term, scope_anchor_kind and the flat group_kind restate question_frame.subject_expression and must agree with it.`,
		`comparison_terms is used for explicit_set only, scope_anchor_term and scope_anchor_kind for children_of_scope only, and the flat group_kind for grouped_members only.`,
		`subject_terms and comparison_terms both list the terms of every operand, in operand order (for a children_of_scope operand, its anchor_terms)`,
		`scope_anchor_term is the first anchor term and scope_anchor_kind is the anchor's kind`,
		`Copy the proper name as written, without the kind word: for "the X team" or "team X" the first term is "X"; the kind word still sets the kind fields (requested_subject_kind, scope_anchor_kind).`,
		`The same holds for terms, anchor_terms, operand terms and scope_anchor_term.`,
		`(discovered_kind, grouped_members, organization_scope) has no subject_terms at all: never write a term for it.`,
		`is organization_scope with member_kind set to the counted kind; the organization is the scope bound, not the counted subject.`,
		`A count with no organization wording and no named parent is discovered_kind.`,
		`is named_subject with the goal assess_state. Never make it children_of_scope, and never a count over the members of the named subject.`,
		`A count of the members of a named parent ("how many pull requests did the X team merge") is children_of_scope.`,
		`"who owns repository X" is children_of_scope, not named_subject: the repository is the anchor and member_kind is team.`,
		`A state verb with a window on the members of a named parent ("merged", "closed", "stay unresolved" over a stated window) is a state condition too.`,
		`Emit it only when a kind word is said in the question`,
		`Omit it for a bare name, a bare ticket key and a bare "our".`,
		`take the kind word from the conversation turn that names that subject.`,
		`In a count, the counted kind word decides it, also when organization wording is present: "how many repositories are in the organization" says repository. Never organization there, and never omit it there.`,
		`Omit it for an explicit_set whose operands state different kinds: the field holds one kind.`,
		`Prefer the shape (single subject, explicit cohort, discovered cohort, or open) implied by the resolved reference`,
	} {
		if got := strings.Count(interpretationSystemPrompt, rule); got != 1 {
			t.Errorf("rule %q appears %d times, want exactly 1", rule, got)
		}
	}
}

func TestInterpretationPromptDropsContradictedSubjectSentences(t *testing.T) {
	t.Parallel()
	for _, gone := range []string{
		"Emit it whenever the question makes it clear",
		"omit it when the question does not say",
		"Infer the investigation shape",
		`named_subject when it names one or more subjects directly ("how is Dev Health Ops doing");`,
		`children_of_scope when it asks for the members OF a named parent of a different kind ("the fullchaos team's projects");`,
		`organization_scope when the organization itself is the subject ("how are we doing").`,
		"Only when the question describes a subject with no literal substring you could copy",
		"may that subject's first term be your own best non-verbatim term instead.\n",
		"(single subject, explicit cohort, or open)",
	} {
		if strings.Contains(interpretationSystemPrompt, gone) {
			t.Errorf("prompt still carries the contradicted sentence %q", gone)
		}
	}
}

// The prompt's shape of each kind is the service's own projection, and each
// kind has exactly one line.
func TestInterpretationShapeLinesFollowDeriveShape(t *testing.T) {
	t.Parallel()
	vocabulary := contextfabric.SubjectExpressionKindVocabulary()
	if len(interpretationFlatFieldRows) != len(vocabulary) {
		t.Fatalf("want one flat-field row per subject_expression kind (%d), got %d", len(vocabulary), len(interpretationFlatFieldRows))
	}
	want := map[contextfabric.SubjectExpressionKind]string{
		contextfabric.SubjectExpressionNamed:             "single_subject",
		contextfabric.SubjectExpressionExplicitSet:       "explicit_cohort, never discovered_cohort",
		contextfabric.SubjectExpressionDiscoveredKind:    "discovered_cohort",
		contextfabric.SubjectExpressionChildrenOfScope:   "explicit_cohort, never discovered_cohort",
		contextfabric.SubjectExpressionGroupedMembers:    "discovered_cohort",
		contextfabric.SubjectExpressionOrganizationScope: "open",
	}
	for i, kind := range vocabulary {
		if interpretationFlatFieldRows[i].Kind != kind {
			t.Errorf("row %d is %q, want the vocabulary order %q", i, interpretationFlatFieldRows[i].Kind, kind)
		}
		derived := string(contextfabric.DeriveShape(contextfabric.SubjectExpression{Kind: kind}))
		if derived == "" {
			t.Fatalf("DeriveShape has no shape for %q", kind)
		}
		if !strings.HasPrefix(want[kind], derived) {
			t.Errorf("DeriveShape(%s) = %q, the ruled mapping is %q", kind, derived, want[kind])
		}
		line := "\n- " + string(kind) + ": shape " + want[kind] + ". "
		if got := strings.Count(interpretationSystemPrompt, line); got != 1 {
			t.Errorf("shape line %q appears %d times, want 1", line, got)
		}
		if got := strings.Count(interpretationSystemPrompt, "\n- "+string(kind)+": shape "); got != 1 {
			t.Errorf("kind %q has %d shape lines, want 1", kind, got)
		}
	}
}

// Each row's statement about a flat field equals what the expression's own
// derivation returns for a fully filled expression of that kind.
func TestInterpretationFlatFieldRowsAgreeWithTheExpressionDerivations(t *testing.T) {
	t.Parallel()
	team, project := contextfabric.SubjectKind(contractsv1.ContextFabricSubjectTeam), contextfabric.SubjectKind(contractsv1.ContextFabricSubjectProject)
	filled := map[contextfabric.SubjectExpressionKind]contextfabric.SubjectExpression{
		contextfabric.SubjectExpressionNamed: {Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"a"}}},
		contextfabric.SubjectExpressionExplicitSet: {Kind: contextfabric.SubjectExpressionExplicitSet, Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
			{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"a"}}},
			{Kind: contextfabric.SubjectOperandScoped, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"b"}, MemberKind: project}},
		}}},
		contextfabric.SubjectExpressionDiscoveredKind:    {Kind: contextfabric.SubjectExpressionDiscoveredKind, Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: team}},
		contextfabric.SubjectExpressionChildrenOfScope:   {Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"a"}, MemberKind: project}},
		contextfabric.SubjectExpressionGroupedMembers:    {Kind: contextfabric.SubjectExpressionGroupedMembers, Grouped: &contextfabric.GroupedSetExpression{GroupKind: team, MemberKind: project}},
		contextfabric.SubjectExpressionOrganizationScope: {Kind: contextfabric.SubjectExpressionOrganizationScope, Org: &contextfabric.OrganizationScopeExpression{MemberKind: &team}},
	}
	for _, row := range interpretationFlatFieldRows {
		expression, ok := filled[row.Kind]
		if !ok {
			t.Fatalf("no filled expression for %q", row.Kind)
		}
		text := row.SubjectTerms + " " + row.Also
		if says, has := row.SubjectTerms != "", len(expression.SubjectTerms()) > 0; says != has {
			t.Errorf("%s: the row says subject_terms=%v, SubjectTerms() gives %v", row.Kind, says, expression.SubjectTerms())
		}
		if says, has := strings.Contains(text, "comparison_terms"), len(expression.ComparisonTerms()) > 0; says != has {
			t.Errorf("%s: the row says comparison_terms=%v, ComparisonTerms() gives %v", row.Kind, says, expression.ComparisonTerms())
		}
		_, grouped := expression.GroupKind()
		if says := strings.Contains(text, "group_kind"); says != grouped {
			t.Errorf("%s: the row says group_kind=%v, GroupKind() ok=%v", row.Kind, says, grouped)
		}
		if says, scoped := strings.Contains(text, "scope_anchor_term"), row.Kind == contextfabric.SubjectExpressionChildrenOfScope; says != scoped {
			t.Errorf("%s: the row says scope_anchor_term=%v, want %v", row.Kind, says, scoped)
		}
	}
	explicit := filled[contextfabric.SubjectExpressionExplicitSet]
	if got := strings.Join(explicit.SubjectTerms(), ","); got != "a,b" || strings.Join(explicit.ComparisonTerms(), ",") != got {
		t.Errorf("explicit_set terms are %q and %q, the prompt says both list every operand's terms in operand order", got, strings.Join(explicit.ComparisonTerms(), ","))
	}
}

// Every subject kind the requested_subject_kind and subject expression rules
// name as a value is a member of the closed vocabulary.
func TestInterpretationSubjectRulesNameOnlyVocabularyKinds(t *testing.T) {
	t.Parallel()
	for _, named := range []struct{ rule, kind string }{
		{`"task" and "what work" say work_item`, "work_item"},
		{`"company" and "across the organization" say organization`, "organization"},
		{`"how many repositories are in the organization" says repository`, "repository"},
		{`the repository is the anchor and member_kind is team`, "team"},
	} {
		if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(named.kind)) {
			t.Errorf("kind %q is not in the closed subject-kind vocabulary", named.kind)
		}
		if got := strings.Count(interpretationSystemPrompt, named.rule); got != 1 {
			t.Errorf("rule %q appears %d times, want 1", named.rule, got)
		}
	}
}
