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

func TestAnswerOneSendsOneSystemMessageStrictSchemaAndNoSamplingFields(t *testing.T) {
	body, resp, logs := answerOnceAgainst(t, map[string]any{"role": "assistant", "content": `{"answer":"ok"}`}, "xhigh")
	if resp.Error != "" {
		t.Fatalf("response error = %q", resp.Error)
	}
	if !strings.Contains(logs, "status=ok") || !strings.Contains(logs, "model=gpt-5.6-luna") {
		t.Errorf("the ok log line must carry the model: %q", logs)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want one system message (System, blank line, Instructions) and one user message", messages)
	}
	system, _ := messages[0].(map[string]any)
	user, _ := messages[1].(map[string]any)
	wantSystem := "system prompt\n\nproduce JSON matching output_schema; echo session_nonce"
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
	format, _ := body["response_format"].(map[string]any)
	schema, _ := format["json_schema"].(map[string]any)
	if format["type"] != "json_schema" || schema["strict"] != true || schema["name"] != "interpret" {
		t.Errorf("response_format = %v, want strict json_schema named after the operation", format)
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
		t.Fatalf("checked %d non-test files, want main.go and strictschema.go at least", checked)
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
