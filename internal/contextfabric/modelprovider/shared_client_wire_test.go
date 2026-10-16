package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

// bodyRecorder answers every chat completion with content and keeps each
// request body as sent.
type bodyRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (b *bodyRecorder) server(t *testing.T, content string) string {
	t.Helper()
	answer := chatCompletion(t, content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.bodies = append(b.bodies, raw)
		b.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		answer(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func (b *bodyRecorder) body(t *testing.T, i int) map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if i >= len(b.bodies) {
		t.Fatalf("request %d was never sent (%d sent)", i, len(b.bodies))
	}
	decoder := json.NewDecoder(bytes.NewReader(b.bodies[i]))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

// TestConfiguredModelWinsOverThePackageDefault pins chris's settled rule for
// acr: the configured model (gpt-5.6-luna) is what every request carries,
// never the shared package's own default for any kind.
func TestConfiguredModelWinsOverThePackageDefault(t *testing.T) {
	if DefaultModel != "gpt-5.6-luna" {
		t.Fatalf("DefaultModel = %q, want gpt-5.6-luna", DefaultModel)
	}
	recorder := &bodyRecorder{}
	cfg := Config{
		Provider: DefaultProvider, BaseURL: recorder.server(t, validInterpretationJSON), Model: DefaultModel,
		APIKey: "sk-configured", Timeout: DefaultTimeout, MaxAttempts: 1, AllowInsecureBaseURL: true,
	}
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	sent := recorder.body(t, 0)["model"]
	for _, kind := range llm.Kinds() {
		if sent == llm.DefaultModel(kind) {
			t.Fatalf("request model = %v, the package default for %s", sent, kind)
		}
	}
	if sent != DefaultModel || receipt.Model != DefaultModel {
		t.Fatalf("request model %v, receipt model %q, want %q", sent, receipt.Model, DefaultModel)
	}

	clients, err := newLLMClients(Config{Model: "gpt-5.6-luna", FallbackModel: "gpt-5.6-sol", PhrasingModel: "gpt-5.6-luna-phrase", APIKey: "k", Timeout: DefaultTimeout},
		"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-luna-phrase", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 3 {
		t.Fatalf("clients = %d, want one per configured model", len(clients))
	}
	for model, client := range clients {
		if client.Model() != model {
			t.Errorf("client keyed %q calls %q", model, client.Model())
		}
	}
}

// TestInterpretRequestKeepsTheGenkitWireShape drives the real entry point
// and pins the Chat Completions body the Genkit compat_oai plugin sent, plus
// what the shared client changes on purpose: no temperature (GPT-5 models
// refuse one), the sample seed as an exact integer, and no output cap.
func TestInterpretRequestKeepsTheGenkitWireShape(t *testing.T) {
	recorder := &bodyRecorder{}
	cfg := testConfig(&providerServer{baseURL: recorder.server(t, validInterpretationJSON)})
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest()); err != nil {
		t.Fatal(err)
	}
	body := recorder.body(t, 0)

	if _, present := body["temperature"]; present {
		t.Errorf("temperature = %v, want none", body["temperature"])
	}
	seed, ok := body["seed"].(json.Number)
	if !ok {
		t.Fatalf("seed = %#v, want a JSON integer", body["seed"])
	}
	if _, err := seed.Int64(); err != nil {
		t.Fatalf("seed %s is not an int64: %v", seed, err)
	}
	if _, present := body["max_completion_tokens"]; present {
		t.Errorf("max_completion_tokens = %v, want none (the Genkit transport sent no cap)", body["max_completion_tokens"])
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %v, want system then user", messages)
	}
	format, _ := body["response_format"].(map[string]any)
	schemaFormat, _ := format["json_schema"].(map[string]any)
	if format["type"] != "json_schema" || schemaFormat["name"] != "output" || schemaFormat["strict"] != true {
		t.Fatalf("response_format = %v, want strict json_schema named output", format)
	}
	wantSchema, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(wantSchema, &want); err != nil {
		t.Fatal(err)
	}
	gotRaw, _ := json.Marshal(schemaFormat["schema"])
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema sent differs from InterpretationOutputSchema()")
	}
}
