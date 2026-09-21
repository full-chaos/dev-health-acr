package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

func investigateBootstrap(t *testing.T) *Bootstrap {
	t.Helper()
	boot := newFixtureBootstrap(t, newFixtureServer(t))
	boot.Capabilities.EnabledTools = append(boot.Capabilities.EnabledTools, toolInvestigateQuestion, toolInvestigationResult)
	return boot
}

func promptNames(t *testing.T, client *mcpsdk.ClientSession) []string {
	t.Helper()
	listed, err := client.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range listed.Prompts {
		names = append(names, p.Name)
	}
	return names
}

func TestServerListsInvestigatePromptsWithVocabularyArguments(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	listed, err := client.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	vocab, err := guide.LoadPromptVocab()
	if err != nil {
		t.Fatal(err)
	}
	want := guide.PromptDefs(vocab)
	if len(want) != 3 || len(listed.Prompts) != len(want) {
		t.Fatalf("listed %d prompts, defined %d", len(listed.Prompts), len(want))
	}
	got := map[string]*mcpsdk.Prompt{}
	for _, p := range listed.Prompts {
		got[p.Name] = p
	}
	for _, def := range want {
		p := got[def.Name]
		if p == nil {
			t.Fatalf("prompt %s not listed", def.Name)
		}
		if p.Description == "" || p.Title == "" || len(p.Arguments) != len(def.Args) {
			t.Fatalf("prompt %s listed incompletely: %+v", def.Name, p)
		}
		for i, arg := range def.Args {
			if p.Arguments[i].Name != arg.Name || p.Arguments[i].Required != arg.Required || p.Arguments[i].Description != arg.Description {
				t.Errorf("prompt %s argument %d differs: %+v vs %+v", def.Name, i, p.Arguments[i], arg)
			}
		}
	}
	// Every registry kind and window is in the argument text the client sees.
	investigate := got[guide.PromptInvestigate]
	var text string
	for _, a := range investigate.Arguments {
		text += a.Description
	}
	for _, k := range vocab.SubjectKinds {
		if !strings.Contains(text, "`"+k+"`") {
			t.Errorf("investigate arguments do not list kind %s", k)
		}
	}
	for _, w := range vocab.Windows {
		if !strings.Contains(text, "`"+w+"`") {
			t.Errorf("investigate arguments do not list window %s", w)
		}
	}
}

// A prompt never names a tool that is absent: investigate and continue follow
// the investigate_question tool; expand_evidence follows source_evidence.
func TestInvestigatePromptsFollowToolAvailability(t *testing.T) {
	plain, closePlain := connectedClient(t, newFixtureBootstrap(t, newFixtureServer(t)))
	defer closePlain()
	if names := promptNames(t, plain); len(names) != 1 || names[0] != guide.PromptExpand {
		t.Fatalf("without investigate_question the only prompt is %s, got %v", guide.PromptExpand, names)
	}
	full, closeFull := connectedClient(t, investigateBootstrap(t))
	defer closeFull()
	if names := promptNames(t, full); len(names) != 3 {
		t.Fatalf("with investigate_question expected 3 prompts, got %v", names)
	}
	if _, err := plain.GetPrompt(context.Background(), &mcpsdk.GetPromptParams{
		Name: guide.PromptInvestigate, Arguments: map[string]string{"question": "q"},
	}); err == nil {
		t.Fatal("investigate must not be gettable when its tool is absent")
	}
}

var promptCallBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

func getPromptText(t *testing.T, client *mcpsdk.ClientSession, name string, args map[string]string) (string, error) {
	t.Helper()
	res, err := client.GetPrompt(context.Background(), &mcpsdk.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("prompt %s returned %d messages", name, len(res.Messages))
	}
	text, ok := res.Messages[0].Content.(*mcpsdk.TextContent)
	if !ok || text.Text == "" {
		t.Fatalf("prompt %s content is %T", name, res.Messages[0].Content)
	}
	return text.Text, nil
}

func promptCall(t *testing.T, text string) []byte {
	t.Helper()
	m := promptCallBlock.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no call block in prompt:\n%s", text)
	}
	return []byte(m[1])
}

func strictDecode(t *testing.T, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
}

// The prompt's arguments decode strictly through the tool's own request type
// and pass its validator. The published request schemas are checked against
// the same calls in the guide package tests.
func TestRenderedPromptCallsAreAcceptedByTheTools(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	listedTools := map[string]bool{}
	for _, tool := range tools.Tools {
		listedTools[tool.Name] = true
	}
	for _, name := range []string{toolInvestigateQuestion, toolSourceEvidence} {
		if !listedTools[name] {
			t.Fatalf("tool %s not listed", name)
		}
	}

	check := func(prompt string, args map[string]string, tool string) {
		t.Helper()
		text, err := getPromptText(t, client, prompt, args)
		if err != nil {
			t.Fatalf("%s: %v", prompt, err)
		}
		call := promptCall(t, text)
		switch tool {
		case toolInvestigateQuestion:
			var req contractsv1.MCPInvestigateQuestionRequest
			strictDecode(t, call, &req)
			if err := req.Validate(); err != nil {
				t.Fatalf("%s: request does not validate: %v\n%s", prompt, err, call)
			}
		case toolSourceEvidence:
			var req contractsv1.MCPSourceEvidenceRequest
			strictDecode(t, call, &req)
			if err := req.Validate(); err != nil {
				t.Fatalf("%s: request does not validate: %v\n%s", prompt, err, call)
			}
		}
		if !strings.Contains(text, "`"+tool+"`") {
			t.Errorf("%s does not name tool %s", prompt, tool)
		}
	}
	check(guide.PromptInvestigate, map[string]string{
		"question": "Which teams need attention?", "repository": "full-chaos/acr", "project": "p1", "team": "t1",
		"expected_kinds": "team,project", "window": "trailing_90d",
	}, toolInvestigateQuestion)
	check(guide.PromptContinue, map[string]string{
		"question": "and the api team?", "parent_result_id": "result_0001",
		"receipts": "kindr_00000001,handr_00000002,winr_00000003,ancr_00000004,candr_00000005,subj_00000006",
	}, toolInvestigateQuestion)
	check(guide.PromptExpand, map[string]string{"evidence_ref_id": "evidence_0001"}, toolSourceEvidence)
}

func TestPromptRefusalIsAnInvalidParamsErrorThatDoesNotEchoInput(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	_, err := getPromptText(t, client, guide.PromptInvestigate, map[string]string{"question": "q", "window": "SECRETVALUE"})
	if err == nil {
		t.Fatal("an invalid window must be refused")
	}
	if strings.Contains(err.Error(), "SECRETVALUE") || !strings.Contains(err.Error(), "trailing_30d") {
		t.Fatalf("refusal must list the vocabulary and not echo the value: %v", err)
	}
	_, err = getPromptText(t, client, guide.PromptInvestigate, map[string]string{"question": "q", "expected_kindz": "team"})
	if err == nil || strings.Contains(err.Error(), "expected_kindz") || !strings.Contains(err.Error(), "expected_kinds") {
		t.Fatalf("an undeclared argument must be refused, listing declared names and not echoing the input: %v", err)
	}
	if _, err := getPromptText(t, client, guide.PromptExpand, nil); err == nil {
		t.Fatal("expand_evidence without evidence_ref_id must be refused")
	}
}

// Prompts read no caller state: two callers with different capabilities and
// credentials get byte-identical text for the same arguments.
func TestPromptsAreIdenticalForEveryCaller(t *testing.T) {
	fx := newFixtureServer(t)
	withWriteback := newWritebackFixtureBootstrap(t, fx)
	withWriteback.Capabilities.EnabledTools = append(withWriteback.Capabilities.EnabledTools, toolInvestigateQuestion, toolInvestigationResult)
	render := func(boot *Bootstrap) []string {
		client, closeFn := connectedClient(t, boot)
		defer closeFn()
		var out []string
		for _, c := range []struct {
			name string
			args map[string]string
		}{
			{guide.PromptInvestigate, map[string]string{"question": "q", "team": "t1", "window": "all_time"}},
			{guide.PromptContinue, map[string]string{"question": "q", "parent_result_id": "result_0001", "receipts": "kindr_00000001"}},
			{guide.PromptExpand, map[string]string{"evidence_ref_id": "e1"}},
		} {
			text, err := getPromptText(t, client, c.name, c.args)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, text)
		}
		return out
	}
	a, b := render(investigateBootstrap(t)), render(withWriteback)
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("prompt %d differs between callers", i)
		}
	}
}

func TestPromptArgumentCompletionOverMCP(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	complete := func(prompt, arg, value string) []string {
		t.Helper()
		res, err := client.Complete(context.Background(), &mcpsdk.CompleteParams{
			Ref:      &mcpsdk.CompleteReference{Type: "ref/prompt", Name: prompt},
			Argument: mcpsdk.CompleteParamsArgument{Name: arg, Value: value},
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.Completion.Values
	}
	vocab, err := guide.LoadPromptVocab()
	if err != nil {
		t.Fatal(err)
	}
	if got := complete(guide.PromptInvestigate, "window", ""); strings.Join(got, ",") != strings.Join(vocab.Windows, ",") {
		t.Errorf("window completion = %v, registry windows = %v", got, vocab.Windows)
	}
	if got := complete(guide.PromptInvestigate, "expected_kinds", "team,proj"); len(got) != 1 || got[0] != "team,project" {
		t.Errorf("kind completion = %v", got)
	}
	if got := complete(guide.PromptInvestigate, "question", "x"); len(got) != 0 {
		t.Errorf("free-text argument must not complete: %v", got)
	}
	res, err := client.Complete(context.Background(), &mcpsdk.CompleteParams{
		Ref:      &mcpsdk.CompleteReference{Type: "ref/resource", URI: "acr://guide/questions"},
		Argument: mcpsdk.CompleteParamsArgument{Name: "window", Value: ""},
	})
	if err != nil || len(res.Completion.Values) != 0 {
		t.Errorf("resource references must complete to nothing: %v %v", res, err)
	}
}

// In one process two callers with different capability sets list different
// prompt catalogues, the same way they list different tools: a prompt is
// listed exactly when the tool it builds a call for is.
func TestPromptCatalogueFollowsTheCallersOwnCapabilities(t *testing.T) {
	_, cfg, callerA, callerB, _, _ := twoCallerProcess(t)
	sessionA := connectedClientForCaller(t, cfg, callerA)
	sessionB := connectedClientForCaller(t, cfg, callerB)

	namesA := promptNames(t, sessionA)
	namesB := promptNames(t, sessionB)

	if len(namesA) != 3 || len(namesB) != 1 || namesB[0] != guide.PromptExpand {
		t.Fatalf("caller A prompts %v, caller B prompts %v", namesA, namesB)
	}
	args := map[string]string{"evidence_ref_id": "e1"}
	a, err := getPromptText(t, sessionA, guide.PromptExpand, args)
	if err != nil {
		t.Fatal(err)
	}
	b, err := getPromptText(t, sessionB, guide.PromptExpand, args)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("expand_evidence text differs between callers")
	}
	if _, err := getPromptText(t, sessionB, guide.PromptInvestigate, map[string]string{"question": "q"}); err == nil {
		t.Fatal("caller B has no investigate_question, so investigate must not be gettable")
	}
}
