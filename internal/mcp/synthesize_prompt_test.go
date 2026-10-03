package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

const (
	synthesizeAnswerPromptName = "synthesize_answer"
	synthesisOutputURI         = "acr://contract/synthesis-output"
)

func getSynthesizePrompt(t *testing.T, client *mcpsdk.ClientSession) (*mcpsdk.GetPromptResult, error) {
	t.Helper()
	return client.GetPrompt(context.Background(), &mcpsdk.GetPromptParams{Name: synthesizeAnswerPromptName})
}

func TestSynthesizePromptSystemBytesEqualTheRuntimeAssembly(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	res, err := getSynthesizePrompt(t, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("got %d messages, want the system message alone", len(res.Messages))
	}
	system := messageText(t, res.Messages[0])
	want := genkitruntime.SynthesisSystemPrompt()
	if system != want {
		t.Fatalf("system message differs from the runtime assembly (%d vs %d bytes)", len(system), len(want))
	}
	if got := res.Meta["system_sha256"]; got != sha256Hex(want) {
		t.Errorf("_meta system_sha256 = %v, want %s", got, sha256Hex(want))
	}
	if res.Meta["prompt_version"] != genkitruntime.DefaultSynthesisPromptVersion ||
		res.Meta["model_output_version"] != genkitruntime.DefaultSchemaVersion {
		t.Errorf("_meta versions = %v", res.Meta)
	}
}

func TestSynthesizePromptListShape(t *testing.T) {
	client, closeFn := connectedClient(t, investigateBootstrap(t))
	defer closeFn()
	listed, err := client.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var p *mcpsdk.Prompt
	for _, c := range listed.Prompts {
		if c.Name == synthesizeAnswerPromptName {
			p = c
		}
	}
	if p == nil {
		t.Fatalf("%s not listed", synthesizeAnswerPromptName)
	}
	if len(p.Arguments) != 0 {
		t.Errorf("arguments = %+v", p.Arguments)
	}
	keys := make([]string, 0, len(p.Meta))
	for k := range p.Meta {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"model_output_version", "prompt_version", "service_version", "system_sha256"}) {
		t.Fatalf("_meta keys = %v", keys)
	}
	if p.Meta["system_sha256"] != sha256Hex(genkitruntime.SynthesisSystemPrompt()) {
		t.Errorf("listed system_sha256 = %v", p.Meta["system_sha256"])
	}
	for _, v := range []string{genkitruntime.DefaultSynthesisPromptVersion, genkitruntime.DefaultSchemaVersion, p.Meta["system_sha256"].(string), synthesisOutputURI} {
		if !strings.Contains(p.Description, v) {
			t.Errorf("description lacks %q", v)
		}
	}
}

func TestSynthesizePromptRefusedWithoutInvestigateTool(t *testing.T) {
	client, closeFn := connectedClient(t, newFixtureBootstrap(t, newFixtureServer(t)))
	defer closeFn()
	if slices.Contains(promptNames(t, client), synthesizeAnswerPromptName) {
		t.Fatalf("%s listed for a credential without investigate_question", synthesizeAnswerPromptName)
	}
	if _, err := getSynthesizePrompt(t, client); err == nil {
		t.Fatalf("%s gettable without investigate_question", synthesizeAnswerPromptName)
	}
}

func TestSynthesizePromptIsRefusedAfterTheToolIsRevoked(t *testing.T) {
	fx := investigateFixture(t)
	boot := newFixtureBootstrap(t, fx)
	boot.Capabilities.EnabledTools = append(boot.Capabilities.EnabledTools, toolInvestigateQuestion)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()
	if _, err := getSynthesizePrompt(t, client); err != nil {
		t.Fatalf("get before revocation: %v", err)
	}
	fx.CapabilitiesHandler = func(w http.ResponseWriter, r *http.Request) {
		caps := validCapabilitiesFixture()
		caps.EnabledTools = slices.DeleteFunc(slices.Clone(caps.EnabledTools), func(n string) bool { return n == toolInvestigateQuestion })
		writeJSONFixture(t, w, http.StatusOK, caps)
	}
	if _, err := getSynthesizePrompt(t, client); err == nil {
		t.Fatalf("%s served after the hosted API revoked investigate_question", synthesizeAnswerPromptName)
	}
}

func TestSynthesisOutputResourceEqualsTheRuntimeSchema(t *testing.T) {
	h := newDTHosted(t)
	client, closeFn := connectedClient(t, h.boot(t, toolInvestigateQuestion))
	defer closeFn()

	listed, err := client.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var resource *mcpsdk.Resource
	for _, r := range listed.Resources {
		if r.URI == synthesisOutputURI {
			resource = r
		}
	}
	if resource == nil {
		t.Fatalf("%s not listed", synthesisOutputURI)
	}
	if resource.MIMEType != "application/schema+json" {
		t.Errorf("mime = %q", resource.MIMEType)
	}

	read, err := readResource(t, client, synthesisOutputURI)
	if err != nil {
		t.Fatal(err)
	}
	got := read.Contents[0].Text
	schemaWant, err := genkitruntime.SynthesisOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schemaWant, &wantMap); err != nil {
		t.Fatal(err)
	}
	if gotMap["$id"] != synthesisOutputURI+"/"+genkitruntime.DefaultSchemaVersion || !strings.Contains(gotMap["title"].(string), genkitruntime.DefaultSchemaVersion) {
		t.Errorf("schema $id/title = %v / %v", gotMap["$id"], gotMap["title"])
	}
	delete(gotMap, "$id")
	delete(gotMap, "title")
	delete(wantMap, "$id")
	delete(wantMap, "title")
	a, _ := json.Marshal(gotMap)
	b, _ := json.Marshal(wantMap)
	if string(a) != string(b) {
		t.Error("served schema differs from the runtime's SynthesisOutputSchema")
	}
	meta := read.Contents[0].Meta
	if meta["sha256"] != sha256Hex(got) || resource.Meta["sha256"] != sha256Hex(got) {
		t.Errorf("schema sha256: read %v, listed %v, want %s", meta["sha256"], resource.Meta["sha256"], sha256Hex(got))
	}
	if meta["model_output_version"] != genkitruntime.DefaultSchemaVersion || meta["prompt_version"] != genkitruntime.DefaultSynthesisPromptVersion {
		t.Errorf("schema _meta = %v", meta)
	}
}

func TestInterpretationResourcesKeepTheInterpretationPromptVersion(t *testing.T) {
	h := newDTHosted(t)
	client, closeFn := connectedClient(t, h.boot(t, toolInvestigateQuestion))
	defer closeFn()
	for _, uri := range []string{uriInterpretationOutput, uriFactKinds} {
		read, err := readResource(t, client, uri)
		if err != nil {
			t.Fatal(err)
		}
		if got := read.Contents[0].Meta["prompt_version"]; got != genkitruntime.DefaultInterpretationPromptVersion {
			t.Errorf("%s prompt_version = %v", uri, got)
		}
	}
}

func TestSynthesisOutputResourceFollowsTheInvestigateTool(t *testing.T) {
	h := newDTHosted(t)
	none, closeNone := connectedClient(t, h.boot(t, toolDataCatalog))
	defer closeNone()
	if slices.Contains(resourceURIs(t, none), synthesisOutputURI) {
		t.Errorf("%s listed without investigate_question", synthesisOutputURI)
	}
	if _, err := readResource(t, none, synthesisOutputURI); err == nil {
		t.Errorf("%s readable without investigate_question", synthesisOutputURI)
	}
}

func TestSynthesisOutputResourceIsRefusedAfterTheToolIsRevoked(t *testing.T) {
	fx := investigateFixture(t)
	boot := newFixtureBootstrap(t, fx)
	boot.Capabilities.EnabledTools = append(boot.Capabilities.EnabledTools, toolInvestigateQuestion)
	client, closeFn := connectedClient(t, boot)
	defer closeFn()
	if _, err := readResource(t, client, synthesisOutputURI); err != nil {
		t.Fatalf("read before revocation: %v", err)
	}
	fx.CapabilitiesHandler = func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
	}
	if _, err := readResource(t, client, synthesisOutputURI); err == nil {
		t.Errorf("%s served after the hosted API revoked investigate_question", synthesisOutputURI)
	}
}
