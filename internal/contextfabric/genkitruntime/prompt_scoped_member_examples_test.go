package genkitruntime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The goals and the temporal intent that each example question takes. The
// prompt shows the subject expression only; the validator needs the whole
// frame.
var scopedMemberExampleFrames = map[string]struct {
	goals    string
	temporal string
}{
	"Over the past quarter, how many incidents were there in the relay-gateway repository?":   {`["count_or_aggregate"]`, "bounded_window"},
	"What fraction of the Brackwater project's work items are done?":                          {`["assess_state"]`, "current"},
	"What explains the many open pull requests of the Marram team?":                           {`["explain_drivers"]`, "current"},
	"Why does the ledger-api repository keep having incidents?":                               {`["explain_drivers"]`, "current"},
	"Show the Marram team's projects that are blocked.":                                       {`["rank_or_survey"]`, "current"},
	"Which of the Marram team's projects are most at risk?":                                   {`["rank_or_survey"]`, "current"},
	"Why do deployments keep failing in the Brackwater project's repositories?":               {`["explain_drivers"]`, "current"},
	"In the past month, how many pull requests has the Vesper team merged?":                   {`["count_or_aggregate"]`, "bounded_window"},
	"List the work items assigned to the Vesper team.":                                        {`["rank_or_survey"]`, "current"},
	"Which Brackwater project work items are still in progress?":                              {`["rank_or_survey"]`, "current"},
	"Compare the open projects of the Marram team with the open projects of the Vesper team.": {`["compare"]`, "current"},
	"Which of the Vesper team's projects are open?":                                           {`["rank_or_survey"]`, "current"},
}

// DecideFrameGate refuses these two examples and passes every other one.
// It is the gate before the work-item arm that the engine applies after it;
// that arm admits no work-item frame with a qualifier, so both stay refused.
var scopedMemberExamplesRefusedByTheGate = map[string]bool{
	"List the work items assigned to the Vesper team.":           true,
	"Which Brackwater project work items are still in progress?": true,
}

func sanitizedExampleFrame(t *testing.T, example scopedMemberExample) interpretationFrameCapture {
	t.Helper()
	frame, ok := scopedMemberExampleFrames[example.Question]
	if !ok {
		t.Fatalf("example %q has no goals and temporal in scopedMemberExampleFrames", example.Question)
	}
	raw := fmt.Sprintf(`{"question_frame":{"goals":%s,"subject_expression":%s,"temporal":%q}}`, frame.goals, example.Expression, frame.temporal)
	var output interpretationOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("example %q does not decode as model output: %v", example.Question, err)
	}
	return sanitizeFrameOutput(output)
}

// Every example the model reads is a frame the service accepts: it goes
// through the sanitizer with nothing dropped and through the validator.
func TestScopedMemberExamplesAreFramesTheServiceAccepts(t *testing.T) {
	t.Parallel()
	if len(interpretationScopedMemberExamples) != len(scopedMemberExampleFrames) {
		t.Fatalf("%d examples, %d frames in the test table", len(interpretationScopedMemberExamples), len(scopedMemberExampleFrames))
	}
	for _, example := range interpretationScopedMemberExamples {
		capture := sanitizedExampleFrame(t, example)
		if !capture.Present || capture.KindUnrecognized || capture.MemberKindUnrecognized || capture.MemberQualifierUnrecognized || capture.GroupKindUnrecognized || capture.TemporalUnrecognized || capture.GoalsDropped != 0 || capture.TermsTruncated != 0 {
			t.Errorf("example %q: the sanitizer changed the frame: %+v", example.Question, capture)
			continue
		}
		expression := capture.Frame.SubjectExpression
		result := contextfabric.ValidateFrame(capture.Frame, nil, contextfabric.DeriveShape(expression))
		if !result.Outcome.Accepted() {
			t.Errorf("example %q: the validator refuses the frame: %+v", example.Question, result.Failure)
			continue
		}
		gate := contextfabric.DecideFrameGate(result, true)
		if got, want := gate.Refuses(), scopedMemberExamplesRefusedByTheGate[example.Question]; got != want {
			t.Errorf("example %q: frame gate refuses = %v (%s), want %v", example.Question, got, gate.Observable(), want)
		}
	}
}

// The anchor fields of an example restate its expression: every term is in
// the question as written, no term carries the kind word, and the anchor
// kind is one that retrieval keeps for this expression.
func TestScopedMemberExamplesRestateTheQuestion(t *testing.T) {
	t.Parallel()
	for _, example := range interpretationScopedMemberExamples {
		capture := sanitizedExampleFrame(t, example)
		expression := capture.Frame.SubjectExpression
		terms := expression.SubjectTerms()
		if len(terms) == 0 {
			t.Errorf("example %q has no terms", example.Question)
		}
		for _, term := range terms {
			if !strings.Contains(example.Question, term) {
				t.Errorf("example %q: term %q is not in the question as written", example.Question, term)
			}
			for _, kindWord := range []string{"team", "project", "repository"} {
				if strings.Contains(strings.ToLower(term), kindWord) {
					t.Errorf("example %q: term %q carries the kind word %q", example.Question, term, kindWord)
				}
			}
		}
		scoped := expression.Kind == contextfabric.SubjectExpressionChildrenOfScope
		if scoped != (example.AnchorKind != "") {
			t.Errorf("example %q: AnchorKind %q on a %s expression", example.Question, example.AnchorKind, expression.Kind)
			continue
		}
		if !scoped {
			continue
		}
		anchorKind, unrecognized := contextfabric.SanitizeSubjectKind(example.AnchorKind)
		if unrecognized {
			t.Errorf("example %q: anchor kind %q is not a subject kind", example.Question, example.AnchorKind)
		}
		if kept := contextfabric.ScopeAnchorRetrievalKind(&capture.Frame, anchorKind); kept != anchorKind {
			t.Errorf("example %q: retrieval keeps anchor kind %q, the example says %q", example.Question, kept, anchorKind)
		}
		if !strings.Contains(example.Question, terms[0]+" "+example.AnchorKind) {
			t.Errorf("example %q: the question does not say %q", example.Question, terms[0]+" "+example.AnchorKind)
		}
	}
}

// The examples are pairs, each line is in the prompt once, and no question
// is used twice.
func TestScopedMemberExamplesRenderOncePerPair(t *testing.T) {
	t.Parallel()
	if len(interpretationScopedMemberExamples)%2 != 0 {
		t.Fatalf("%d examples, want pairs", len(interpretationScopedMemberExamples))
	}
	for i, example := range interpretationScopedMemberExamples {
		line := fmt.Sprintf("\n- %q -> %s", example.Question, example.Expression)
		if got := strings.Count(interpretationSystemPrompt, line); got != 1 {
			t.Errorf("example line %q appears %d times, want 1", line, got)
		}
		if i%2 == 1 && example.Expression == interpretationScopedMemberExamples[i-1].Expression {
			t.Errorf("pair %d has the same expression twice: it shows no contrast", i/2)
		}
	}
}
