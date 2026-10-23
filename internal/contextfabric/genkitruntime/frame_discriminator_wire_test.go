package genkitruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func wireChild(t *testing.T, node map[string]any, name string) map[string]any {
	t.Helper()
	properties, _ := node["properties"].(map[string]any)
	child, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("wire has no property %q", name)
	}
	return child
}

func wireRequired(node map[string]any, name string) bool {
	list, _ := node["required"].([]any)
	return slices.Contains(list, any(name))
}

func TestStrictInterpretationWireForcesTheFrameDiscriminator(t *testing.T) {
	t.Parallel()
	schema := strictSchemas(t)["interpret"]
	root := schema.strict
	frame := wireChild(t, root, "question_frame")
	if frame["type"] != "object" {
		t.Errorf("question_frame type = %v, want object (not nullable)", frame["type"])
	}
	expression := wireChild(t, frame, "subject_expression")
	if expression["type"] != "object" {
		t.Errorf("subject_expression type = %v, want object (not nullable)", expression["type"])
	}
	kind := wireChild(t, expression, "kind")
	var want []any
	for _, member := range contextfabric.SubjectExpressionKindVocabulary() {
		want = append(want, string(member))
	}
	got, _ := kind["enum"].([]any)
	if !slices.Equal(got, want) {
		t.Errorf("kind enum = %v, want exactly the six kinds %v with no empty member", got, want)
	}
	if kind["type"] != "string" {
		t.Errorf("kind type = %v, want string", kind["type"])
	}
}

func TestLenientInterpretationWireIsUnchangedByTheStrictFrameRule(t *testing.T) {
	t.Parallel()
	schema := strictSchemas(t)["interpret"]
	raw, err := json.Marshal(schema.wire)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	const pinned = "e63131537213348b7a9de423badacfccdaf1d8a23c532ed9b7d032b70e65a27b"
	if got := hex.EncodeToString(sum[:]); got != pinned {
		t.Fatalf("lenient interpretation wire digest = %s, want %s -- the OpenAI/luna wire and the local check must not change", got, pinned)
	}
}

func TestFrameWithoutAKindStillRefusesAtTheGateWhateverShapeTheModelSent(t *testing.T) {
	t.Parallel()
	shapes := map[string]*subjectExpressionOutput{
		"subject_expression null":     nil,
		"kind empty":                  {Kind: "", Terms: []string{"x"}},
		"kind outside the closed set": {Kind: "team", Terms: []string{"x"}},
	}
	for name, expression := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			output := validInterpretationOutput()
			output.QuestionFrame = &questionFrameOutput{Goals: []string{"explain_drivers"}, SubjectExpression: expression, Temporal: "current"}
			runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
			_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
			if err != nil {
				t.Fatalf("InterpretQuestion() error = %v", err)
			}
			if receipt.QuestionFrame == nil {
				t.Fatal("receipt.QuestionFrame = nil, want the proposal")
			}
			if kind := receipt.QuestionFrame.SubjectExpression.Kind; kind != "" {
				t.Fatalf("sanitized kind = %q, want empty: nothing may guess a kind", kind)
			}
			result := contextfabric.ValidateFrame(*receipt.QuestionFrame, nil, contextfabric.ShapeSingleSubject)
			if result.Outcome != contextfabric.FrameValidationOutcomeRefusedInvalid || result.Failure.Detail != contextfabric.FrameFailureKindUnset {
				t.Fatalf("ValidateFrame = %v / %v, want refused_invalid / kind_unset", result.Outcome, result.Failure.Detail)
			}
		})
	}
}
