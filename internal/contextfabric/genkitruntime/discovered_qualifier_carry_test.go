package genkitruntime

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// A status stated by a question that names no parent is carried by the
// discovered_kind and organization_scope frames, as it is by children_of_scope.
func TestParseInterpretationOutputSignalsCarriesTheStatusOfAnUnanchoredQuestion(t *testing.T) {
	for _, kind := range []string{"discovered_kind", "organization_scope"} {
		output := validInterpretationOutput()
		output.QuestionFrame = &questionFrameOutput{
			Goals: []string{"rank_or_survey"},
			SubjectExpression: &subjectExpressionOutput{
				Kind: kind, MemberKind: "pull_request", MemberQualifier: "status", MemberQualifierValue: "awaiting_review",
			},
			Temporal: "current",
		}
		raw, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		_, capture, err := ParseInterpretationOutputSignals(raw, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		frame := capture.Frame.Frame
		if got := contextfabric.PullRequestStatusFilterOf(&frame); got != "awaiting_review" {
			t.Fatalf("%s: status filter = %q, want awaiting_review", kind, got)
		}
		if capture.Frame.MemberQualifierUnrecognized {
			t.Fatalf("%s: qualifier flagged unrecognized", kind)
		}
	}
}
