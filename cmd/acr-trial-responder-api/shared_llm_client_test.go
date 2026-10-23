package main

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

func exchangeDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func answerOnceAgainst(t *testing.T, message map[string]any, effort string) (map[string]any, exchangeResponse, string) {
	t.Helper()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("request = %s %s, want POST /chat/completions", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion", "created": 0, "model": "gpt-5.6-luna",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": message}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
		})
	}))
	defer srv.Close()
	dir := exchangeDirForTest(t)
	r := newTestResponder(t, dir, srv.URL)
	r.effort = effort
	writeExchangeRequest(t, dir, 1, "n1")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), filepath.Join(dir, "requests", "000001-interpret.json"), respPath, filepath.Join(dir, "_responder_logs"))
	raw, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("no response written: %v", err)
	}
	var resp exchangeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	logs, _ := os.ReadFile(filepath.Join(dir, "_responder_logs", "000001-interpret.json.log"))
	return captured, resp, string(logs)
}

func TestAnswerOneSendsOneSystemMessageAndNoSamplingFields(t *testing.T) {
	body, resp, logs := answerOnceAgainst(t, map[string]any{"role": "assistant", "content": `{"answer":"ok"}`}, "xhigh")
	if resp.Error != "" {
		t.Fatalf("response error = %q", resp.Error)
	}
	if !strings.Contains(logs, "status=ok") || !strings.Contains(logs, "model=gpt-5.6-luna") {
		t.Errorf("the ok log line must carry the model: %q", logs)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want one system message and one user message", messages)
	}
	system, _ := messages[0].(map[string]any)
	user, _ := messages[1].(map[string]any)
	wantSystem := "system prompt\n\nproduce JSON matching output_schema; echo session_nonce" + genkitruntime.OutputFormatInstructions([]byte(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`))
	if system["role"] != "system" || system["content"] != wantSystem || user["role"] != "user" || user["content"] != "user prompt" {
		t.Errorf("messages = %v, want system %q then user", messages, wantSystem)
	}
	for _, field := range []string{"temperature", "max_tokens", "max_completion_tokens"} {
		if _, present := body[field]; present {
			t.Errorf("request carries %s; the responder has never sent it", field)
		}
	}
	if body["reasoning_effort"] != "xhigh" || body["model"] != "gpt-5.6-luna" {
		t.Errorf("reasoning_effort = %v model = %v", body["reasoning_effort"], body["model"])
	}
}

// The responder must put on the wire what production puts on it for a Chat
// Completions model (genkitruntime llm_generator.go complete): a JSON object
// answer and the schema as text, for every operation. Built from the real
// production schemas so a change on either side fails here.
func TestAnswerOneSendsTheProductionChatCompletionsWireForEveryOperation(t *testing.T) {
	schemas := map[string]func() ([]byte, error){
		"interpret":  genkitruntime.InterpretationOutputSchema,
		"synthesize": genkitruntime.SynthesisOutputSchema,
	}
	for operation, build := range schemas {
		t.Run(operation, func(t *testing.T) {
			schema, err := build()
			if err != nil {
				t.Fatal(err)
			}
			dir := exchangeDirForTest(t)
			var captured map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&captured)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":0,"model":"gpt-5.6-luna","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}]}`))
			}))
			defer srv.Close()
			r := newTestResponder(t, dir, srv.URL)
			req := exchangeRequest{Operation: operation, Seq: 1, SessionNonce: "n1", System: "S", Prompt: "P", OutputSchema: schema}
			body, _ := json.Marshal(req)
			name := "000001-" + operation + ".json"
			if err := os.WriteFile(filepath.Join(dir, "requests", name), body, 0o644); err != nil {
				t.Fatal(err)
			}
			r.answerOne(t.Context(), filepath.Join(dir, "requests", name), filepath.Join(dir, "responses", name), filepath.Join(dir, "_responder_logs"))

			format, _ := captured["response_format"].(map[string]any)
			if format["type"] != "json_object" || format["json_schema"] != nil {
				t.Fatalf("response_format = %v, want json_object with no schema (production's Chat Completions wire)", format)
			}
			messages, _ := captured["messages"].([]any)
			system, _ := messages[0].(map[string]any)
			content, _ := system["content"].(string)
			if !strings.HasSuffix(content, genkitruntime.OutputFormatInstructions(schema)) {
				t.Errorf("system message does not end with production's schema instructions text")
			}
		})
	}
}

// Replays the live failure: a provider that refuses any json_schema request
// (HTTP 400 on the synthesize schema) answers a json_object request. The
// responder must publish a validated answer, not a terminal invalid_request.
func TestAnswerOneDoesNotSendASchemaOnTheWireSoAProviderThatRefusesOneStillAnswers(t *testing.T) {
	dir := exchangeDirForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if format, _ := body["response_format"].(map[string]any); format["type"] == "json_schema" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid schema for response_format: schema must have a 'type' key.","type":"invalid_request_error","param":"response_format","code":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":0,"model":"gpt-5.6-luna","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"answer\":\"ok\"}"}}]}`))
	}))
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	writeExchangeRequest(t, dir, 1, "n1")
	name := "000001-interpret.json"
	r.answerOne(t.Context(), filepath.Join(dir, "requests", name), filepath.Join(dir, "responses", name), filepath.Join(dir, "_responder_logs"))
	raw, err := os.ReadFile(filepath.Join(dir, "responses", name))
	if err != nil {
		t.Fatalf("no response: %v", err)
	}
	var resp exchangeResponse
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Error != "" || len(resp.Output) == 0 {
		t.Fatalf("response = %s (%v), want a published output", raw, err)
	}
}

func TestAnswerOneWritesModelRefusedAndNeverTheRefusalText(t *testing.T) {
	_, resp, logs := answerOnceAgainst(t, map[string]any{"role": "assistant", "content": nil, "refusal": "secret refusal words"}, "")
	if !strings.Contains(logs, "class=model_refused") || strings.Contains(logs, "secret refusal words") {
		t.Errorf("the log must name the class and never the refusal text: %q", logs)
	}
	if resp.Error != classModelRefused || len(resp.Output) != 0 {
		t.Fatalf("response = %+v, want only the %s class", resp, classModelRefused)
	}
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "secret refusal words") {
		t.Error("the refusal's own words reached the response file")
	}
}

func TestResponderDoesNotImportOpenAIGo(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found: %v", err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range parsed.Imports {
			if strings.Contains(imp.Path.Value, "openai/openai-go") {
				t.Errorf("%s imports %s; the responder uses the shared llm client", name, imp.Path.Value)
			}
		}
	}
	if checked < 2 {
		t.Fatalf("checked %d non-test files, want main.go and errorreason.go at least", checked)
	}
}

func TestAnswerOneUnlistedClientStatusIsTerminalInvalidRequestAndLogsTheLLMClass(t *testing.T) {
	dir := exchangeDirForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	writeExchangeRequest(t, dir, 1, "n1")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), filepath.Join(dir, "requests", "000001-interpret.json"), respPath, filepath.Join(dir, "_responder_logs"))
	raw, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("no terminal response for an unlisted 4xx: %v", err)
	}
	var resp exchangeResponse
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Error != classInvalidRequest {
		t.Fatalf("response = %s (%v), want error %q", raw, err, classInvalidRequest)
	}
	logs, _ := os.ReadFile(filepath.Join(dir, "_responder_logs", "000001-interpret.json.log"))
	if !strings.Contains(string(logs), "class=invalid_request terminal=true llm_class=generic") {
		t.Errorf("the error line must carry the closed class and the llm class: %q", logs)
	}
}
