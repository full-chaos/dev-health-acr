package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

func TestKindForProviderNamespaces(t *testing.T) {
	for provider, want := range map[string]llm.Kind{
		"openai": llm.KindLocal, "vllm": llm.KindLocal, "local-llama": llm.KindLocal, "": llm.KindLocal,
		"anthropic": llm.KindAnthropic, "Anthropic": llm.KindAnthropic, "gemini": llm.KindGemini, " qwen ": llm.KindQwen,
	} {
		if got := kindFor(provider); got != want {
			t.Errorf("kindFor(%q) = %q, want %q", provider, got, want)
		}
	}
}

// providerKindServer answers a chat completion or a Messages call with the
// interpretation, and keeps each request's path and body.
type providerKindServer struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
}

func (p *providerKindServer) start(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		p.paths = append(p.paths, r.URL.Path)
		p.bodies = append(p.bodies, body)
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var compact bytes.Buffer
		_ = json.Compact(&compact, []byte(validInterpretationJSON))
		if r.URL.Path == "/v1/messages" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content":     []any{map[string]any{"type": "text", "text": compact.String()}},
				"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 40, "output_tokens": 18},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": compact.String()}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 40, "completion_tokens": 18},
		})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// TestProviderNamespacePicksTheClientAndItsSeedRule drives the real entry
// point per provider namespace and checks the API each one reaches, that the
// configured model is the one sent, and that a seed reaches only the
// providers that accept one.
func TestProviderNamespacePicksTheClientAndItsSeedRule(t *testing.T) {
	for _, tc := range []struct {
		provider string
		baseURL  func(string) string
		path     string
		wantSeed bool
	}{
		{provider: "openai", baseURL: func(u string) string { return u + "/v1" }, path: "/v1/chat/completions", wantSeed: true},
		{provider: "qwen", baseURL: func(u string) string { return u + "/v1" }, path: "/v1/chat/completions", wantSeed: true},
		{provider: "gemini", baseURL: func(u string) string { return u + "/v1beta/openai" }, path: "/v1beta/openai/chat/completions", wantSeed: false},
		{provider: "anthropic", baseURL: func(u string) string { return u }, path: "/v1/messages", wantSeed: false},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			server := &providerKindServer{}
			cfg := Config{
				Provider: tc.provider, BaseURL: tc.baseURL(server.start(t)), Model: "configured-" + tc.provider,
				APIKey: llm.NewSecret("sk-configured"), Timeout: DefaultTimeout, MaxAttempts: 1, AllowInsecureBaseURL: true,
			}
			runtime, err := New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_test"}, testRequest())
			if err != nil {
				t.Fatalf("InterpretQuestion() = %v", err)
			}
			if len(server.paths) != 1 || server.paths[0] != tc.path {
				t.Fatalf("paths = %v, want one call to %s", server.paths, tc.path)
			}
			body := server.bodies[0]
			if body["model"] != cfg.Model || receipt.Model != cfg.Model || receipt.Provider != tc.provider {
				t.Fatalf("model sent %v, receipt %s/%s", body["model"], receipt.Provider, receipt.Model)
			}
			if _, present := body["seed"]; present != tc.wantSeed {
				t.Fatalf("seed present = %v, want %v", present, tc.wantSeed)
			}
			if _, present := body["temperature"]; present {
				t.Fatalf("temperature = %v, want none", body["temperature"])
			}
		})
	}
}

func TestNativeKindsDefaultToTheirOwnEndpoint(t *testing.T) {
	for provider, wantHost := range map[string]string{
		"anthropic": "api.anthropic.com", "gemini": "generativelanguage.googleapis.com", "openai": "api.openai.com",
	} {
		clients, err := newLLMClients(Config{Provider: provider, Model: "m", APIKey: llm.NewSecret("sk-configured"), Timeout: DefaultTimeout}, "m")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(clients["m"])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(wantHost)) || bytes.Contains(raw, []byte("sk-configured")) {
			t.Errorf("%s client = %s, want base URL host %s and no key", provider, raw, wantHost)
		}
	}
}
