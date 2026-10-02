package genkitruntime

import (
	"fmt"
	"strings"
)

// The worked examples of interpretationSystemPrompt for the members of a
// named parent. prompts.go splices them in after the subject expression
// rules. Data, so the test runs every expression through the frame
// sanitizer and validator the service uses.

// scopedMemberExample is one question and the subject_expression it takes.
// AnchorKind is set for a single children_of_scope only.
type scopedMemberExample struct {
	Question   string
	Expression string
	AnchorKind string
	Note       string
}

// interpretationScopedMemberExamples holds contrast pairs: rows 2n and 2n+1
// are two readings that are easy to confuse.
var interpretationScopedMemberExamples = []scopedMemberExample{
	{
		Question:   "Over the past quarter, how many incidents were there in the relay-gateway repository?",
		Expression: `{"kind":"children_of_scope","anchor_terms":["relay-gateway"],"member_kind":"incident"}`,
		AnchorKind: "repository",
		Note:       "a count of the members",
	},
	{
		Question:   "What fraction of the Brackwater project's work items are done?",
		Expression: `{"kind":"named_subject","terms":["Brackwater"]}`,
		Note:       "a share: goal assess_state, no scope anchor",
	},
	{
		Question:   "What explains the many open pull requests of the Marram team?",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Marram"],"member_kind":"pull_request","member_qualifier":"status"}`,
		AnchorKind: "team",
		Note:       "the members are filtered by a state",
	},
	{
		Question:   "Why does the ledger-api repository keep having incidents?",
		Expression: `{"kind":"named_subject","terms":["ledger-api"]}`,
		Note:       "the question is about the repository itself; no scope anchor",
	},
	{
		Question:   "Show the Marram team's projects that are blocked.",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Marram"],"member_kind":"project","member_qualifier":"status"}`,
		AnchorKind: "team",
		Note:       "blocked is a state",
	},
	{
		Question:   "Which of the Marram team's projects are most at risk?",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Marram"],"member_kind":"project"}`,
		AnchorKind: "team",
		Note:       "at risk is the ranking basis: no member_qualifier",
	},
	{
		Question:   "Why do deployments keep failing in the Brackwater project's repositories?",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Brackwater"],"member_kind":"repository"}`,
		AnchorKind: "project",
		Note:       "deployments is the topic and names fact kinds",
	},
	{
		Question:   "In the past month, how many pull requests has the Vesper team merged?",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Vesper"],"member_kind":"pull_request","member_qualifier":"status"}`,
		AnchorKind: "team",
		Note:       "pull requests are counted; merged over a window is a state",
	},
	{
		Question:   "List the work items assigned to the Vesper team.",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Vesper"],"member_kind":"work_item","member_qualifier":"assignee"}`,
		AnchorKind: "team",
	},
	{
		Question:   "Which Brackwater project work items are still in progress?",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Brackwater"],"member_kind":"work_item","member_qualifier":"status"}`,
		AnchorKind: "project",
	},
	{
		Question:   "Compare the open projects of the Marram team with the open projects of the Vesper team.",
		Expression: `{"kind":"explicit_set","operands":[{"kind":"children_of_scope","anchor_terms":["Marram"],"member_kind":"project","member_qualifier":"status"},{"kind":"children_of_scope","anchor_terms":["Vesper"],"member_kind":"project","member_qualifier":"status"}]}`,
		Note:       "one operand for each parent, never one children_of_scope with two anchor terms; no scope anchor",
	},
	{
		Question:   "Which of the Vesper team's projects are open?",
		Expression: `{"kind":"children_of_scope","anchor_terms":["Vesper"],"member_kind":"project","member_qualifier":"status"}`,
		AnchorKind: "team",
	},
}

var interpretationScopedMemberExampleLines = func() string {
	var b strings.Builder
	b.WriteString("Worked examples for the members of a named parent, as question -> question_frame.subject_expression. Each pair of lines shows two readings that are easy to confuse:")
	for _, example := range interpretationScopedMemberExamples {
		fmt.Fprintf(&b, "\n- %q -> %s", example.Question, example.Expression)
		var notes []string
		if example.AnchorKind != "" {
			notes = append(notes, "scope_anchor_kind "+example.AnchorKind)
		}
		if example.Note != "" {
			notes = append(notes, example.Note)
		}
		if len(notes) > 0 {
			b.WriteString(" (" + strings.Join(notes, "; ") + ")")
		}
	}
	return b.String()
}()
