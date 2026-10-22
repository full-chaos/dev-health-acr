package entitlements_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// withRawMembers puts raw JSON members first in the body, so a test can send
// what a map cannot: a duplicate key or a literal null.
func withRawMembers(members string, overrides map[string]any) string {
	return "{" + members + "," + strings.TrimPrefix(llmBody(overrides), "{")
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

func TestOrgLLMConfigCarriesThePromptCacheOptIn(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]any
		want      bool
		wantFound bool
	}{
		{"opted in", map[string]any{"prompt_cache": true}, true, true},
		{"explicitly off", map[string]any{"prompt_cache": false}, false, true},
		{"field omitted", nil, false, true},
		{"a string is a contract break: unavailable", map[string]any{"prompt_cache": "yes"}, false, false},
		{"a number is a contract break: unavailable", map[string]any{"prompt_cache": 1}, false, false},
	}
	raw := []struct {
		name string
		body string
	}{
		{"null is a contract break: unavailable", withRawMembers(`"prompt_cache":null`, nil)},
		{"a duplicate key is a contract break even when both say off", withRawMembers(`"prompt_cache":false`, map[string]any{"prompt_cache": false})},
		{"a duplicate key that turns the cache on later is a contract break", withRawMembers(`"prompt_cache":false`, map[string]any{"prompt_cache": true})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := llmConfigServer(t, http.StatusOK, llmBody(tc.overrides))
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
			if err != nil {
				t.Fatal(err)
			}
			config, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
			if found != tc.wantFound || (err != nil) == tc.wantFound {
				t.Fatalf("found = %v err = %v", found, err)
			}
			if config.PromptCache != tc.want {
				t.Fatalf("PromptCache = %v, want %v", config.PromptCache, tc.want)
			}
		})
	}
	for _, tc := range raw {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := llmConfigServer(t, http.StatusOK, tc.body)
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := client.OrgLLMConfig(context.Background(), llmOrg); found || err == nil {
				t.Fatalf("found = %v err = %v, want unavailable", found, err)
			}
		})
	}
}

func TestOrgLLMConfigShowsThePromptCacheOptInAndStillHidesTheKey(t *testing.T) {
	config := entitlements.OrgLLMConfig{Provider: "anthropic", Model: "m", APIKey: llm.NewSecret("sk-ant-SECRET-VALUE"), PromptCache: true, Version: "v"}
	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("m", "config", config)
	encoded, _ := json.Marshal(config)
	for form, want := range map[string]string{
		fmt.Sprintf("%v", config):  "PromptCache:true",
		fmt.Sprintf("%#v", config): "PromptCache:true",
		logs.String():              `"prompt_cache":true`,
		string(encoded):            `"prompt_cache":true`,
	} {
		if !strings.Contains(form, want) {
			t.Fatalf("the opt-in %q is not shown: %s", want, form)
		}
		if strings.Contains(form, "SECRET") {
			t.Fatalf("the key was printed: %s", form)
		}
	}
}

func TestOrgLLMConfigRefusesABaseURLThatCarriesTheKey(t *testing.T) {
	const key = "sk-ant-SECRET-VALUE"
	text := base64.RawStdEncoding.EncodeToString([]byte(key))
	escaped := func(s string) string {
		var out strings.Builder
		for i := 0; i < len(s); i++ {
			out.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{s[i]})))
		}
		return out.String()
	}
	cases := []struct {
		name    string
		baseURL string
		reason  string
	}{
		{"key in the query", "https://h.example.invalid/v1?token=" + key, "base_url_carries_key"},
		{"key in the path", "https://h.example.invalid/v1/" + key, "base_url_carries_key"},
		{"key in the fragment", "https://h.example.invalid/v1#" + key, "base_url_carries_key"},
		{"key in a host label", "https://" + strings.ToLower(key) + ".h.example.invalid/v1", "base_url_carries_key"},
		{"key as the userinfo password", "https://user:" + key + "@h.example.invalid/v1", "base_url_carries_key"},
		{"query-escaped key", "https://h.example.invalid/v1?token=" + url.QueryEscape(key), "base_url_carries_key"},
		{"hex key", "https://h.example.invalid/v1/" + hex.EncodeToString([]byte(key)), "base_url_carries_key"},
		{"base64 key", "https://h.example.invalid/v1/" + text[:len(text)-2], "base_url_carries_key"},
		{"key escaped twice", "https://h.example.invalid/v1/" + escaped(escaped(key)), "base_url_carries_key"},
		{"userinfo that is not the key", "https://user:pass@h.example.invalid/v1", "base_url_carries_userinfo"},
		{"protocol-relative userinfo", "//user:pass@h.example.invalid/v1", "base_url_carries_userinfo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := llmConfigServer(t, http.StatusOK, llmBody(map[string]any{"base_url": tc.baseURL}))
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 8192}))
			if err != nil {
				t.Fatal(err)
			}
			config, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
			var refused *entitlements.BaseURLRefusedError
			if found || !errors.Is(err, entitlements.ErrLLMConfigUnusable) || !errors.As(err, &refused) || refused.Reason != tc.reason {
				t.Fatalf("found = %v err = %v, want an unusable configuration refused for %s", found, err, tc.reason)
			}
			if config != (entitlements.OrgLLMConfig{}) {
				t.Fatalf("a refused configuration was returned: %v", config)
			}
			for _, secret := range []string{key, "h.example.invalid", "user:pass"} {
				if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%#v %+v", err, err), secret) {
					t.Fatalf("the refusal holds %q: %v", secret, err)
				}
			}
		})
	}
}

func TestOrgLLMConfigAcceptsABaseURLThatCarriesNoKey(t *testing.T) {
	for _, baseURL := range []string{
		"",
		"https://api.anthropic.com",
		"https://h.example.invalid/openai/deployments/d?api-version=2024-02-01",
		"https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1",
		"https://h.example.invalid/v1?next=a@b",
	} {
		server, _, _ := llmConfigServer(t, http.StatusOK, llmBody(map[string]any{"base_url": baseURL}))
		client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 8192}))
		if err != nil {
			t.Fatal(err)
		}
		config, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
		if err != nil || !found || config.BaseURL != baseURL {
			t.Fatalf("base_url %q: found = %v err = %v config base_url = %q, want it served", baseURL, found, err, config.BaseURL)
		}
	}
}

// A refusal is read again on the next turn: it is neither a served config nor
// one of the refusals the client remembers for 15 s.
func TestARefusedBaseURLIsNotCached(t *testing.T) {
	server, calls, _ := llmConfigServer(t, http.StatusOK, llmBody(map[string]any{"base_url": "https://h.example.invalid/v1?token=sk-ant-SECRET-VALUE"}))
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 8192}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, found, err := client.OrgLLMConfig(context.Background(), llmOrg); found || !errors.Is(err, entitlements.ErrLLMConfigUnusable) {
			t.Fatalf("read %d: found = %v err = %v", i, found, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want every refused read to go to ops", calls.Load())
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
		{"a body that is not JSON is a contract break: unavailable", http.StatusOK, `not json`, false, false, true},
		{"another org is a contract break: unavailable", http.StatusOK, llmBody(map[string]any{"org_id": "99999999-2222-4333-8444-555555555555"}), false, false, true},
		{"another schema is a contract break: unavailable", http.StatusOK, llmBody(map[string]any{"schema_version": "acr_llm_config.v2"}), false, false, true},
		{"no model is a contract break: unavailable", http.StatusOK, llmBody(map[string]any{"model": " "}), false, false, true},
		{"no version is a contract break: unavailable", http.StatusOK, llmBody(map[string]any{"version": ""}), false, false, true},
		{"an unknown field is a contract break: unavailable", http.StatusOK, llmBody(map[string]any{"extra": 1}), false, false, true},
		{"a duplicate key is a contract break: unavailable", http.StatusOK, withRawMembers(`"model":"other-model"`, nil), false, false, true},
		{"a null key is a contract break: unavailable", http.StatusOK, llmBody(map[string]any{"api_key": json.RawMessage("null")}), false, false, true},
		{"trailing JSON is a contract break: unavailable", http.StatusOK, llmBody(nil) + ` {}`, false, false, true},
		{"a truncated body is a contract break: unavailable", http.StatusOK, strings.TrimSuffix(llmBody(nil), "}"), false, false, true},
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

// ops refuses to serve a stored base_url that carries the org's own key: it
// answers the marked 404 with reason byo_base_url_carries_key. acr reads it as
// the same refusal as a served url that carries the key: an unusable
// configuration, never "no BYO" and never the platform model.
func TestOrgLLMConfigReasonBaseURLCarriesKeyIsAnUnusableRefusalNotCached(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Org-Llm-Config", "none")
		w.Header().Set("X-Org-Llm-Config-Reason", "byo_base_url_carries_key")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not found"}`))
	}))
	t.Cleanup(server.Close)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 8192}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		config, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
		var refused *entitlements.BaseURLRefusedError
		if found || !errors.Is(err, entitlements.ErrLLMConfigUnusable) || !errors.As(err, &refused) || refused.Reason != "base_url_carries_key" {
			t.Fatalf("read %d: found = %v err = %v, want an unusable configuration refused for base_url_carries_key", i, found, err)
		}
		if errors.Is(err, entitlements.ErrLLMConfigGateClosed) || errors.Is(err, entitlements.ErrLLMConfigNoInvestigateConfig) || errors.Is(err, entitlements.ErrLLMConfigTurnedOff) {
			t.Fatalf("read %d: err = %v, want none of the remembered refusals", i, err)
		}
		if config != (entitlements.OrgLLMConfig{}) {
			t.Fatalf("read %d: a refused configuration was returned: %v", i, config)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("ops calls = %d, want 2 (a refusal is read again on every turn)", got)
	}
}

// A reason this build does not know stays unavailable, never an unusable
// refusal: only the closed reason words map to a refusal.
func TestOrgLLMConfigUnknownBaseURLReasonStaysUnavailable(t *testing.T) {
	for _, reason := range []string{"byo_base_url_carries_userinfo", "byo_base_url_carries_key_x", "BYO_BASE_URL_CARRIES_KEY"} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Org-Llm-Config", "none")
			w.Header().Set("X-Org-Llm-Config-Reason", reason)
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)
		client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 8192}))
		if err != nil {
			t.Fatal(err)
		}
		_, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
		var refused *entitlements.BaseURLRefusedError
		if found || err == nil || errors.Is(err, entitlements.ErrLLMConfigUnusable) || errors.As(err, &refused) {
			t.Fatalf("reason %q: found = %v err = %v, want unavailable and not a refusal", reason, found, err)
		}
	}
}

// The marker and the reason are each ONE value. A repeated header is not read
// by its first value: which of two conflicting reasons an intermediary
// forwarded first must never choose between a refusal and a platform-model
// fallback, so any repeat is an unknown answer and fails closed.
func TestOrgLLMConfigRepeatedMarkerOrReasonHeaderFailsClosed(t *testing.T) {
	for name, headers := range map[string][][2]string{
		"turned off then carries key":      {{"X-Org-Llm-Config", "none"}, {"X-Org-Llm-Config-Reason", "byo_llm_turned_off"}, {"X-Org-Llm-Config-Reason", "byo_base_url_carries_key"}},
		"carries key then turned off":      {{"X-Org-Llm-Config", "none"}, {"X-Org-Llm-Config-Reason", "byo_base_url_carries_key"}, {"X-Org-Llm-Config-Reason", "byo_llm_turned_off"}},
		"the same reason twice":            {{"X-Org-Llm-Config", "none"}, {"X-Org-Llm-Config-Reason", "byo_llm_gate_closed"}, {"X-Org-Llm-Config-Reason", "byo_llm_gate_closed"}},
		"marker twice":                     {{"X-Org-Llm-Config", "none"}, {"X-Org-Llm-Config", "none"}},
		"marker none then another value":   {{"X-Org-Llm-Config", "none"}, {"X-Org-Llm-Config", "saved"}},
		"marker twice with a valid reason": {{"X-Org-Llm-Config", "none"}, {"X-Org-Llm-Config", "none"}, {"X-Org-Llm-Config-Reason", "byo_base_url_carries_key"}},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, header := range headers {
					w.Header().Add(header[0], header[1])
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			t.Cleanup(server.Close)
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 8192}))
			if err != nil {
				t.Fatal(err)
			}
			_, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
			var refused *entitlements.BaseURLRefusedError
			if found || err == nil || errors.Is(err, entitlements.ErrLLMConfigUnusable) || errors.As(err, &refused) ||
				errors.Is(err, entitlements.ErrLLMConfigTurnedOff) || errors.Is(err, entitlements.ErrLLMConfigGateClosed) || errors.Is(err, entitlements.ErrLLMConfigNoInvestigateConfig) {
				t.Fatalf("found = %v err = %v, want unavailable and no remembered or refusal class", found, err)
			}
		})
	}
}

// A short key is held to the same rule as a long one for every form of eight
// characters or more: a 4-byte key echoed in hex is refused through the real
// client, a 3-byte key (no derived form that long) is served.
func TestOrgLLMConfigRefusesAShortKeyEchoedInHex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		baseURL string
		refused bool
	}{
		{"4-byte key in hex", "abcd", "https://h.example.invalid/v1/" + hex.EncodeToString([]byte("abcd")), true},
		{"4-byte key in upper-case hex, escaped", "abcd", "https://h.example.invalid/v1/" + strings.ToUpper(hex.EncodeToString([]byte("abcd"))), true},
		{"7-byte key in base64", "abcdefg", "https://h.example.invalid/v1/YWJjZGVmZw", true},
		{"3-byte key in hex", "abc", "https://h.example.invalid/v1/" + hex.EncodeToString([]byte("abc")), false},
		{"4-byte key, another key's hex", "abcd", "https://h.example.invalid/v1/" + hex.EncodeToString([]byte("abce")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _ := llmConfigServer(t, http.StatusOK, llmBody(map[string]any{"api_key": tc.key, "base_url": tc.baseURL}))
			client, err := entitlements.New(clientConfig(t, server, entitlements.Config{MaxResponseBytes: 8192}))
			if err != nil {
				t.Fatal(err)
			}
			_, found, err := client.OrgLLMConfig(context.Background(), llmOrg)
			var refused *entitlements.BaseURLRefusedError
			if tc.refused {
				if found || !errors.As(err, &refused) || refused.Reason != "base_url_carries_key" || !errors.Is(err, entitlements.ErrLLMConfigUnusable) {
					t.Fatalf("found = %v err = %v, want an unusable configuration refused for base_url_carries_key", found, err)
				}
				return
			}
			if err != nil || !found {
				t.Fatalf("found = %v err = %v, want the configuration served", found, err)
			}
		})
	}
}
