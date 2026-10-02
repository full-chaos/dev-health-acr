package genkitruntime

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The subject-expression, shape and requested-kind sections of
// interpretationSystemPrompt. prompts.go splices them in.

// interpretationSubjectExpressionRules decides the cases where two
// subject_expression kinds seem to fit one question.
const interpretationSubjectExpressionRules = `Subject expression rules:
- A count with organization wording ("in the organization", "organization-wide", "our organization") is organization_scope with member_kind set to the counted kind; the organization is the scope bound, not the counted subject. A count with no organization wording and no named parent is discovered_kind. Do not swap one for the other.
- A share or amount of one named subject's members (what fraction of a named project's work items are completed) is named_subject with the goal assess_state. Never make it children_of_scope, and never a count over the members of the named subject.
- A trend of a measure over the members of a named parent, where the question names the member noun, is children_of_scope. A count of the members of a named parent is children_of_scope too.
- "who owns repository X" is children_of_scope, not named_subject: the repository is the anchor and member_kind is team.
- member_qualifier is "status" when the question puts a state condition on members that are objects (issues, projects, incidents, work items), not people: open, in progress, unfinished, blocked, stuck, active, "remain open", reversed. A state verb with a window on the members of a named parent ("merged", "closed", "stay unresolved" over a stated window) is a state condition too. It applies to children_of_scope and to a children_of_scope operand of explicit_set. Team members of a scope also take "status" when the question selects a subset of them by state. A health or pressure condition used as the ranking basis (strained, at risk, need attention) is not a state filter.`

// interpretationFlatFieldRows says, per subject_expression kind, how the flat
// fields restate the expression. Data, so the test checks each row against
// the expression's own derivations. An empty SubjectTerms means the kind has
// no subject_terms.
var interpretationFlatFieldRows = []struct {
	Kind         contextfabric.SubjectExpressionKind
	SubjectTerms string
	Also         string
}{
	{contextfabric.SubjectExpressionNamed, "subject_terms holds its terms", ""},
	{contextfabric.SubjectExpressionExplicitSet, "subject_terms and comparison_terms both list the terms of every operand, in operand order (for a children_of_scope operand, its anchor_terms)", ""},
	{contextfabric.SubjectExpressionDiscoveredKind, "", ""},
	{contextfabric.SubjectExpressionChildrenOfScope, "subject_terms holds its anchor_terms", "scope_anchor_term is the first anchor term and scope_anchor_kind is the anchor's kind"},
	{contextfabric.SubjectExpressionGroupedMembers, "", "the flat group_kind restates its group_kind"},
	{contextfabric.SubjectExpressionOrganizationScope, "", ""},
}

// interpretationShapeRules renders the shape of each kind from
// contextfabric.DeriveShape, the projection the service itself computes.
var interpretationShapeRules = func() string {
	var b strings.Builder
	b.WriteString("Shape and flat fields: shape, subject_terms, comparison_terms, scope_anchor_term, scope_anchor_kind and the flat group_kind restate question_frame.subject_expression and must agree with it. By subject_expression kind:")
	for _, row := range interpretationFlatFieldRows {
		shape := contextfabric.DeriveShape(contextfabric.SubjectExpression{Kind: row.Kind})
		fmt.Fprintf(&b, "\n- %s: shape %s", row.Kind, shape)
		if shape == contextfabric.ShapeExplicitCohort {
			fmt.Fprintf(&b, ", never %s", contextfabric.ShapeDiscoveredCohort)
		}
		b.WriteString(". ")
		if row.SubjectTerms == "" {
			b.WriteString("No subject_terms")
		} else {
			b.WriteString(row.SubjectTerms)
		}
		if row.Also != "" {
			b.WriteString("; " + row.Also)
		}
		b.WriteString(".")
	}
	b.WriteString("\ncomparison_terms is used for explicit_set only, scope_anchor_term and scope_anchor_kind for children_of_scope only, and the flat group_kind for grouped_members only.")
	return b.String()
}()

// interpretationRequestedSubjectKindRule replaces "emit it whenever the
// question makes it clear": the field follows a kind word that is said.
const interpretationRequestedSubjectKindRule = `requested_subject_kind: the kind of thing the ANSWER is about, from the same closed set as group_kind. It is the counterpart to scope_anchor_kind: in "the fullchaos team's projects" the anchor kind is "team" and requested_subject_kind is "project", and it is that DIFFERENCE that identifies the question as being about a scoped group of members rather than about the named subject itself.
- Emit it only when a kind word is said in the question ("projects", "teams", "repositories"). Plain nouns count as kind words: "task" and "what work" say work_item; "company" and "across the organization" say organization.
- Omit it for a bare name, a bare ticket key and a bare "our".
- When the question refers back to an earlier subject ("it", "that", "which", "its", "the same", or an elided reference such as a bare "Why?"), take the kind word from the conversation turn that names that subject.
- In a count, the counted kind word decides it, also when organization wording is present: "how many repositories are in the organization" says repository. Never organization there, and never omit it there.
- Omit it for an explicit_set whose operands state different kinds: the field holds one kind.`
