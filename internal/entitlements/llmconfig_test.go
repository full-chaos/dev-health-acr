package entitlements_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

const llmOrg = "11111111-2222-4333-8444-555555555555"

func llmConfigServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int64, *atomic.Value) {
	t.Helper()
	calls := &atomic.Int64{}
	lastPath := &atomic.Value{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastPath.Store(r.URL.Path + " " + r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, calls, lastPath
}

func llmBody(overrides map[string]any) string {
	body := map[string]any{
		"schema_version": "acr_llm_config.v1", "org_id": llmOrg, "provider": " Anthropic ", "model": "claude-haiku-5-5",
		"base_url": "", "api_key": "sk-ant-SECRET-VALUE", "version": "2026-10-16T18:00:00.123456Z",
	}
	for key, value := range overrides {
		if value == nil {
			delete(body, key)
			continue
		}
		body[key] = value
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func TestOrgLLMConfigServesTheOpsConfig(t *testing.T) {
	server, calls, path := llmConfigServer(t, http.StatusOK, llmBody(nil))
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	config, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
	if err != nil || !found {
		t.Fatalf("found = %v err = %v", found, err)
	}
	if config.Provider != "anthropic" || config.Model != "claude-haiku-5-5" || config.APIKey.Reveal() != "sk-ant-SECRET-VALUE" || config.Version != "2026-10-16T18:00:00.123456Z" {
		t.Fatalf("config = %+v", config)
	}
	if got := path.Load().(string); got != "/api/v1/internal/acr/orgs/"+llmOrg+"/llm-config Bearer "+testToken {
		t.Fatalf("request = %q", got)
	}
	if _, _, err := client.OrgLLMConfig(context.Background(), llmOrg); err != nil || calls.Load() != 1 {
		t.Fatalf("second read: calls = %d err = %v, want the cached answer", calls.Load(), err)
	}
}

func TestOrgLLMConfigMapsEachStatus(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantFound  bool
		wantUnused bool
		wantErr    bool
	}{
		{"404 without the no-config marker is unavailable", http.StatusNotFound, `{"detail":"Not found"}`, false, false, true},
		{"401 is unavailable, not a saved config", http.StatusUnauthorized, `{"detail":"Unauthorized"}`, false, false, true},
		{"403 is unavailable, not a saved config", http.StatusForbidden, `{"detail":"Forbidden"}`, false, false, true},
		{"409 undecryptable is unusable", http.StatusConflict, `{"detail":"stored config unreadable"}`, false, true, true},
		{"503 is unavailable", http.StatusServiceUnavailable, `{"detail":"Service unavailable"}`, false, false, true},
		{"another org is unusable", http.StatusOK, llmBody(map[string]any{"org_id": "99999999-2222-4333-8444-555555555555"}), false, true, true},
		{"another schema is unusable", http.StatusOK, llmBody(map[string]any{"schema_version": "acr_llm_config.v2"}), false, true, true},
		{"no model is unusable", http.StatusOK, llmBody(map[string]any{"model": " "}), false, true, true},
		{"no version is unusable", http.StatusOK, llmBody(map[string]any{"version": ""}), false, true, true},
		{"an unknown field is unusable", http.StatusOK, llmBody(map[string]any{"extra": 1}), false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := llmConfigServer(t, tc.status, tc.body)
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
			if err != nil {
				t.Fatal(err)
			}
			_, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
			if found != tc.wantFound || (err != nil) != tc.wantErr || errors.Is(err, entitlements.ErrLLMConfigUnusable) != tc.wantUnused {
				t.Fatalf("found = %v err = %v", found, err)
			}
		})
	}
}

func TestOrgLLMConfigErrorsAreNotCached(t *testing.T) {
	server, calls, _ := llmConfigServer(t, http.StatusServiceUnavailable, `{"detail":"x"}`)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, _, _ = client.OrgLLMConfig(context.Background(), llmOrg)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want every failed read to go to ops", calls.Load())
	}
}

func TestOrgLLMConfigNeverPrintsTheKey(t *testing.T) {
	config := entitlements.OrgLLMConfig{Provider: "anthropic", Model: "m", APIKey: llm.NewSecret("sk-ant-SECRET-VALUE"), Version: "v"}
	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("m", "config", config)
	encoded, _ := json.Marshal(config)
	for _, form := range []string{fmt.Sprintf("%v", config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config), fmt.Sprintf("%s", config), logs.String(), string(encoded)} {
		if strings.Contains(form, "SECRET") {
			t.Fatalf("the key was printed: %s", form)
		}
	}
}
