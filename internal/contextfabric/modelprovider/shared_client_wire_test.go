package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
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

// TestModelRequestsAreTheGenkitRequests drives the real entry point for
// interpretation and synthesis and compares each request with the one the
// Genkit transport sent for the same input. The golden bodies were captured
// from the real Genkit path at the commit before the shared client replaced
// it (testdata/genkit_wire): response_format json_object, the output schema
// as text at the end of the system message, the sample seed on
// interpretation only, and no other field.
func TestModelRequestsAreTheGenkitRequests(t *testing.T) {
	recorder := &bodyRecorder{}
	cfg := testConfig(&providerServer{baseURL: recorder.server(t, validInterpretationJSON)})
	runtime, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())
	_, _, _ = runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_test"}, liveSynthesisInput())
	currentSystem := []string{interpretprompt.System(), synthesisprompt.System()}
	for i, name := range []string{"interpret_request.json", "synthesize_request.json"} {
		raw, err := os.ReadFile("testdata/genkit_wire/" + name)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var want map[string]any
		if err := decoder.Decode(&want); err != nil {
			t.Fatal(err)
		}
		got := recorder.body(t, i)
		if !reflect.DeepEqual(sortedKeys(got), sortedKeys(want)) {
			t.Errorf("%s: fields %v, Genkit sent %v", name, sortedKeys(got), sortedKeys(want))
		}
		for _, field := range []string{"model", "response_format", "seed"} {
			if !reflect.DeepEqual(got[field], want[field]) {
				t.Errorf("%s: %s = %v, Genkit sent %v", name, field, got[field], want[field])
			}
		}
		gotMessages, wantMessages := got["messages"].([]any), want["messages"].([]any)
		if len(gotMessages) != 2 || len(wantMessages) != 2 {
			t.Fatalf("%s: messages %d, Genkit sent %d", name, len(gotMessages), len(wantMessages))
		}
		// The prompt text itself may have changed on main since the capture;
		// what Genkit added is the instruction suffix, compared byte for byte,
		// after the current system prompt.
		gotSystem, wantSystem := gotMessages[0].(map[string]any)["content"].(string), wantMessages[0].(map[string]any)["content"].(string)
		wantSuffix := wantSystem[strings.Index(wantSystem, genkitInstructionLead):]
		if !strings.HasSuffix(gotSystem, wantSuffix) || strings.TrimSuffix(gotSystem, wantSuffix) != currentSystem[i] {
			t.Errorf("%s: the system message is not the current prompt plus Genkit's instruction (%d vs %d bytes)", name, len(gotSystem), len(currentSystem[i])+len(wantSuffix))
		}
		if gotUser, wantUser := gotMessages[1].(map[string]any)["content"], messageText(wantMessages[1]); gotUser != wantUser {
			t.Errorf("%s: the user message text differs from Genkit's", name)
		}
	}
}

const genkitInstructionLead = "Output should be in JSON format and conform to the following schema:"

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// messageText is a message's text whether it was sent as a string or, as
// openai-go sent Genkit's user message, as text parts.
func messageText(message any) string {
	content := message.(map[string]any)["content"]
	if text, ok := content.(string); ok {
		return text
	}
	var out strings.Builder
	for _, part := range content.([]any) {
		out.WriteString(part.(map[string]any)["text"].(string))
	}
	return out.String()
}
