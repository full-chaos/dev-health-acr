package mcp

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"testing"
)

func TestClientFlowVocabularyNamesRegisteredSurfaces(t *testing.T) {
	raw, err := os.ReadFile("schemas/tools.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range manifest.Tools {
		names = append(names, tool.Name)
	}
	vocab := ClientFlow()
	for _, tool := range []string{vocab.InterpretTool, vocab.ServerSideTool, vocab.ResultTool} {
		if !slices.Contains(names, tool) {
			t.Errorf("tool %q is not in the tool manifest %v", tool, names)
		}
	}
	for _, key := range []string{"model_output_version", "prompt_version", "system_sha256"} {
		if !slices.Contains(vocab.PromptMetaKeys, key) {
			t.Errorf("prompt _meta keys %v lack %q", vocab.PromptMetaKeys, key)
		}
	}
}

func TestInterpretPromptResultHasTwoUserMessages(t *testing.T) {
	result, err := interpretPromptResult("Is pull request 532 ready to merge?", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(result.Messages))
	}
	for i, message := range result.Messages {
		if message.Role != "user" {
			t.Errorf("message %d role = %q, want user: the guide says both messages carry the role user", i+1, message.Role)
		}
	}
}

func TestInterpretPromptUserPayloadHoldsOnlyTheDocumentedKeys(t *testing.T) {
	result, err := interpretPromptResult("Is pull request 532 ready to merge?", "test")
	if err != nil {
		t.Fatal(err)
	}
	text := messageText(t, result.Messages[1])
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !slices.Equal(keys, []string{"question", "requested_scope", "time_context"}) || string(payload["requested_scope"]) != "{}" {
		t.Fatalf("payload %s: the guide says question, time context and an empty requested_scope", text)
	}
}

func TestClientFlowVocabularyTiesTheSynthesisSurfaces(t *testing.T) {
	vocab := ClientFlow()
	if vocab.SynthesisPrompt != promptSynthesizeAnswer || vocab.SynthesisPrompt == "" {
		t.Errorf("synthesis prompt = %q", vocab.SynthesisPrompt)
	}
	if vocab.SynthesisOutputURI != uriSynthesisOutput || vocab.SynthesisOutputURI == "" {
		t.Errorf("synthesis output URI = %q", vocab.SynthesisOutputURI)
	}
}
