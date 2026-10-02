package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// differentialRequest is fictional and exercises every rendered field.
const differentialRequest = `{"question":"Why is the Harbor Lights team slower than last quarter?","conversation":[{"turn_id":"t1","role":"user","content":"How is the Harbor Lights team doing?","created_at":"2026-09-01T10:00:00Z"},{"turn_id":"t2","role":"assistant","content":"Harbor Lights appears to have more open blockers than usual.","created_at":"2026-09-01T10:00:05Z"}],"requested_scope":{"subject_hints":[{"kind":"team","label":"Harbor Lights","source":"ui_selection"}]},"time_context":{"axis":"current"},"prior_subject_receipts":[{"result_id":"res_0000demo01","receipt_id":"rcpt_0000demo01"}]}`

type wireCapture struct {
	mu     sync.Mutex
	paths  []string
	bodies [][]byte
}

// loopbackProvider is an OpenAI-compatible endpoint on httptest's loopback
// listener. It records every request body and answers with one fixed,
// schema-valid chat completion.
func loopbackProvider(t *testing.T, capture *wireCapture) *httptest.Server {
	t.Helper()
	answer, err := json.Marshal(validTarget)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.paths = append(capture.paths, r.Method+" "+r.URL.Path)
		capture.bodies = append(capture.bodies, body)
		capture.mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-offline","object":"chat.completion","created":1767225600,"model":"offline-model","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, answer)
	}))
}

// userContentText returns the text of an OpenAI user message whose content
// is either a string or an array of content parts.
func userContentText(t *testing.T, raw json.RawMessage) (string, string) {
	t.Helper()
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, "string"
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("user content is neither a string nor a part array: %s", raw)
	}
	var text strings.Builder
	for _, part := range parts {
		if part["type"] != "text" {
			t.Fatalf("unexpected user content part %v", part)
		}
		text.WriteString(part["text"].(string))
	}
	return text.String(), fmt.Sprintf("array of %d text part(s)", len(parts))
}

// TestSystemMessageMatchesProductionWireBody is the differential oracle for
// the training SYSTEM message. It builds the production model runtime with
// the production constructor (modelprovider.NewGenkitRuntime: genkit +
// compat_oai + openai-go, the BYO OpenAI-compatible path), points it at a
// loopback httptest server, runs one real InterpretQuestion and compares
// the HTTP body compat_oai sends with the helper's system-message and
// render output. This test constructs a model client, but only against
// the loopback listener.
func TestSystemMessageMatchesProductionWireBody(t *testing.T) {
	capture := &wireCapture{}
	server := loopbackProvider(t, capture)
	defer server.Close()

	ctx := context.Background()
	runtime, err := modelprovider.NewGenkitRuntime(ctx, modelprovider.Config{
		Provider:             modelprovider.DefaultProvider,
		BaseURL:              server.URL + "/v1/",
		Model:                "offline-model",
		APIKey:               "loopback-test-key",
		Timeout:              10 * time.Second,
		MaxAttempts:          1,
		MaxTransportRetries:  0,
		AllowInsecureBaseURL: true,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("production constructor: %v", err)
	}
	decoded, err := decodeRequest(json.RawMessage(differentialRequest))
	if err != nil || decoded.ValidateErr != nil {
		t.Fatalf("request: %v %v", err, decoded.ValidateErr)
	}
	principal := storage.Principal{OrgID: "offline-test", Subject: "offline-test"}
	interpreted, _, err := runtime.InterpretQuestion(ctx, principal, decoded.Request)
	if err != nil {
		t.Fatalf("production InterpretQuestion over loopback: %v", err)
	}
	if interpreted.Shape != "single_subject" {
		t.Fatalf("loopback answer was not parsed by production: %+v", interpreted)
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.bodies) != 1 || !strings.HasSuffix(capture.paths[0], "/v1/chat/completions") {
		t.Fatalf("want exactly one chat completion call, got %v", capture.paths)
	}
	var wire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		ResponseFormat json.RawMessage `json:"response_format"`
	}
	if err := json.Unmarshal(capture.bodies[0], &wire); err != nil {
		t.Fatal(err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(capture.bodies[0], &topLevel); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(topLevel))
	for key := range topLevel {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	t.Logf("wire body top-level keys: %v", keys)
	t.Logf("wire response_format: %s", wire.ResponseFormat)

	if len(wire.Messages) != 2 || wire.Messages[0].Role != "system" || wire.Messages[1].Role != "user" {
		t.Fatalf("unexpected wire messages: %s", capture.bodies[0])
	}
	var wireSystem string
	if err := json.Unmarshal(wire.Messages[0].Content, &wireSystem); err != nil {
		t.Fatalf("system content is not a string: %s", wire.Messages[0].Content)
	}
	helperSystem, err := systemMessageInfo()
	if err != nil {
		t.Fatal(err)
	}
	if wireSystem != helperSystem.Text {
		t.Fatalf("system-message differs from the production wire body (%d vs %d bytes, sha %s vs %s)",
			len(helperSystem.Text), len(wireSystem), helperSystem.SHA256, sha256Hex([]byte(wireSystem)))
	}
	wireUser, userShape := userContentText(t, wire.Messages[1].Content)
	t.Logf("wire user content shape: %s", userShape)
	rendered, err := render(json.RawMessage(differentialRequest))
	if err != nil {
		t.Fatal(err)
	}
	if wireUser != rendered.UserPayload {
		t.Fatalf("render differs from the production wire user content\n render: %s\n wire:   %s", rendered.UserPayload, wireUser)
	}

	// The constraint the provider receives is a response_format of
	// json_object, not json_schema: WithCustomConstrainedOutput turns native
	// constraint off, genkit drops the schema from the output config, and
	// compat_oai maps format=json without a schema to json_object.
	var format map[string]any
	if err := json.Unmarshal(wire.ResponseFormat, &format); err != nil || format["type"] != "json_object" {
		t.Fatalf("response_format = %s, want {\"type\":\"json_object\"}", wire.ResponseFormat)
	}
	if _, hasSchema := format["json_schema"]; hasSchema {
		t.Fatal("unexpected json_schema constraint on the wire")
	}
}

// TestSystemMessageIsRequestIndependentAndUserMatchesRender checks the
// recorder path on its own: the system text does not change with the
// request, and the recorded user text is the render payload.
func TestSystemMessageIsRequestIndependentAndUserMatchesRender(t *testing.T) {
	var texts []string
	for _, request := range []string{systemMessageRequest, differentialRequest} {
		decoded, err := decodeRequest(json.RawMessage(request))
		if err != nil {
			t.Fatal(err)
		}
		recorded, err := recordProductionModelRequest(decoded.Request)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := render(json.RawMessage(request))
		if err != nil {
			t.Fatal(err)
		}
		if recorded.UserContent != rendered.UserPayload {
			t.Fatal("recorded user content differs from render")
		}
		if recorded.Parts != 2 || recorded.InstructionBytes == 0 {
			t.Fatalf("expected prompt + injected output instruction, got %d parts, %d instruction bytes", recorded.Parts, recorded.InstructionBytes)
		}
		if !strings.HasPrefix(recorded.Text[len(systemPromptInfo().Text):], "Output should be in JSON format and conform to the following schema:\n\n```") {
			t.Fatal("injected instruction does not follow the prompt directly")
		}
		texts = append(texts, recorded.Text)
	}
	if texts[0] != texts[1] {
		t.Fatal("system message depends on the request")
	}
}
