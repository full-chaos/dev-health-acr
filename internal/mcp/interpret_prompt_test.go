package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func getInterpretPrompt(t *testing.T, client *mcpsdk.ClientSession, question string) (*mcpsdk.GetPromptResult, error) {
	t.Helper()
	return client.GetPrompt(context.Background(), &mcpsdk.GetPromptParams{
		Name: promptInterpretQuestion, Arguments: map[string]string{interpretQuestionArg: question},
	})
}

func messageText(t *testing.T, m *mcpsdk.PromptMessage) string {
	t.Helper()
	text, ok := m.Content.(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("message content is %T", m.Content)
	}
	return text.Text
}

func TestInterpretPromptSystemBytesEqualTheRuntimeAssembly(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	const question = "how is the payments team doing"
	res, err := getInterpretPrompt(t, client, question)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("got %d messages, want system + question", len(res.Messages))
	}
	system := messageText(t, res.Messages[0])
	want := genkitruntime.InterpretationSystemPrompt()
	if system != want {
		t.Fatalf("system message differs from the runtime assembly (%d vs %d bytes)", len(system), len(want))
	}
	sum := sha256.Sum256([]byte(want))
	if got := res.Meta["system_sha256"]; got != hex.EncodeToString(sum[:]) {
		t.Errorf("_meta system_sha256 = %v, want %x", got, sum)
	}
	wantUser, err := genkitruntime.BuildInterpretationPrompt(contextfabric.InvestigationRequest{
		Question:    question,
		TimeContext: contextfabric.TimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
	}, genkitruntime.DefaultExchangeMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageText(t, res.Messages[1]); got != wantUser || !strings.Contains(got, question) {
		t.Errorf("user message = %q, want %q", got, wantUser)
	}
	if res.Meta["prompt_version"] != genkitruntime.DefaultInterpretationPromptVersion ||
		res.Meta["model_output_version"] != genkitruntime.DefaultSchemaVersion {
		t.Errorf("_meta versions = %v", res.Meta)
	}
}

func TestInterpretPromptListShape(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	listed, err := client.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var p *mcpsdk.Prompt
	for _, c := range listed.Prompts {
		if c.Name == promptInterpretQuestion {
			p = c
		}
	}
	if p == nil {
		t.Fatalf("interpret_question not listed")
	}
	if len(p.Arguments) != 1 || p.Arguments[0].Name != "question" || !p.Arguments[0].Required {
		t.Errorf("arguments = %+v", p.Arguments)
	}
	keys := make([]string, 0, len(p.Meta))
	for k := range p.Meta {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"model_output_version", "prompt_version", "service_version", "system_sha256"}) {
		t.Errorf("_meta keys = %v", keys)
	}
	for _, v := range []string{genkitruntime.DefaultInterpretationPromptVersion, genkitruntime.DefaultSchemaVersion, p.Meta["system_sha256"].(string)} {
		if !strings.Contains(p.Description, v) {
			t.Errorf("description lacks %q", v)
		}
	}
}

func TestInterpretPromptRefusedWithoutInvestigateTool(t *testing.T) {
	client, closeFn := connectedClient(t, newFixtureBootstrap(t, newFixtureServer(t)))
	defer closeFn()
	if slices.Contains(promptNames(t, client), promptInterpretQuestion) {
		t.Fatal("interpret_question listed for a credential without investigate_question")
	}
	if _, err := getInterpretPrompt(t, client, "q"); err == nil {
		t.Fatal("interpret_question gettable without investigate_question")
	}
}

func TestInterpretPromptRequiresQuestion(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	if _, err := getInterpretPrompt(t, client, "  "); err == nil {
		t.Fatal("blank question accepted")
	}
}
