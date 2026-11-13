package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

func purgeWorkloadToken(audience string) string {
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	claims := fmt.Sprintf(`{"iss":"https://auth.example.test","aud":%q,"sub":"prn_AAAAAAAAAAAAAAAAAAAAAA","principal_type":"workload","jti":"%024d","iat":%d,"nbf":%d,"exp":%d}`, audience, 1, now-5, now-5, now+295)
	return enc.EncodeToString([]byte(`{"alg":"EdDSA","typ":"workload+jwt","kid":"test-key"}`)) + "." + enc.EncodeToString([]byte(claims)) + "." + enc.EncodeToString(make([]byte, 64))
}

func purgeFakes(t *testing.T) (opsURL, exchangeURL string, authz func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:jwt","expires_in":300}`, purgeWorkloadToken(form.Get("audience")))
	}))
	t.Cleanup(exchange.Close)
	ops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ops.Close)
	return ops.URL, exchange.URL, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestPurgeEntitlementClientSendsTheWorkloadToken(t *testing.T) {
	opsURL, exchangeURL, authz := purgeFakes(t)
	dir := t.TempDir()
	subject := filepath.Join(dir, "subject")
	enc := base64.RawURLEncoding
	if err := os.WriteFile(subject, []byte(enc.EncodeToString([]byte(`{"alg":"RS256"}`))+"."+enc.EncodeToString([]byte(`{"sub":"x"}`))+"."+enc.EncodeToString([]byte("sig"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        opsURL,
		"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload",
		"ACR_AUTH_WORKLOAD_EXCHANGE_URL":        exchangeURL,
		"ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP":    "true",
		"ACR_AUTH_WORKLOAD_ISSUER":              "https://auth.example.test",
		"ACR_AUTH_WORKLOAD_SUBJECT_FILE":        subject,
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	client, err := purgeEntitlementClientFrom(lookup, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Check(context.Background()); err == nil {
		t.Log("health body is not the contract: Check is expected to fail after the request was sent")
	}
	got := authz()
	if len(got) != 1 || !strings.HasPrefix(got[0], "Bearer ey") {
		t.Fatalf("ops saw %q, want one workload token", got)
	}
}

func TestPurgeEntitlementClientRefusesWorkloadWithoutTheAuthClient(t *testing.T) {
	env := map[string]string{
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "https://ops.example.test",
		"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	if _, err := purgeEntitlementClientFrom(lookup, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("workload mode built with no ACR_AUTH_WORKLOAD_*")
	}
	delete(env, "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL")
	if _, err := purgeEntitlementClientFrom(lookup, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("an unset credential built with no ACR_AUTH_WORKLOAD_*")
	}
	env["ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL"] = "svc_acr"
	if _, err := purgeEntitlementClientFrom(lookup, slog.New(slog.DiscardHandler)); err == nil || strings.Contains(err.Error(), "svc_acr") {
		t.Fatalf("a bad flag value must be refused without echoing it: %v", err)
	}
}

// The purge refuses the retired credential value as acr-api does, before it builds anything, and names neither the
// ignored token file setting nor a value.
func TestPurgeEntitlementClientRefusesTheRetiredCredential(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"retired credential value":                 {"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": config.RetiredEntitlementServiceToken},
		"retired credential value with token file": {"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": config.RetiredEntitlementServiceToken, config.RetiredEntitlementTokenFileEnv: "/run/secrets/ops-token"},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_URL": "https://ops.example.test"}
			for k, v := range extra {
				env[k] = v
			}
			_, err := purgeEntitlementClientFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), "is retired") || strings.Contains(err.Error(), "/run/secrets/ops-token") || strings.Contains(err.Error(), config.RetiredEntitlementTokenFileEnv) {
				t.Fatalf("purge error = %v, want the retired credential refused without a value", err)
			}
		})
	}
}

// The retired token file setting does not stop the purge (product rule 3): with the workload settings the client is
// built and one Warn line names the setting; without the auth workload client the missing-config check refuses.
func TestPurgeEntitlementClientWarnsOnTheRetiredTokenFile(t *testing.T) {
	opsURL, exchangeURL, _ := purgeFakes(t)
	subject := filepath.Join(t.TempDir(), "subject")
	if err := os.WriteFile(subject, []byte("x.y.z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workload := map[string]string{
		"ACR_AUTH_WORKLOAD_EXCHANGE_URL":     exchangeURL,
		"ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP": "true",
		"ACR_AUTH_WORKLOAD_ISSUER":           "https://auth.example.test",
		"ACR_AUTH_WORKLOAD_SUBJECT_FILE":     subject,
	}
	for name, test := range map[string]struct {
		credential string
		workload   bool
		token      string
		refused    string // "" = the client is built
	}{
		"token file with workload":                   {"workload", true, "/run/secrets/ops-token", ""},
		"token file with an unset credential":        {"", true, "/run/secrets/ops-token", ""},
		"token file empty":                           {"", true, "", ""},
		"token file without the auth client":         {"workload", false, "/run/secrets/ops-token", "auth workload client"},
		"token file, unset, without the auth client": {"", false, "/run/secrets/ops-token", "auth workload client"},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"ACR_DEV_HEALTH_ENTITLEMENT_URL": opsURL, config.RetiredEntitlementTokenFileEnv: test.token}
			if test.credential != "" {
				env["ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL"] = test.credential
			}
			if test.workload {
				for k, v := range workload {
					env[k] = v
				}
			}
			var out bytes.Buffer
			client, err := purgeEntitlementClientFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, slog.New(slog.NewJSONHandler(&out, nil)))
			if test.refused == "" {
				if err != nil {
					t.Fatalf("purge refused a complete workload configuration: %v", err)
				}
				client.Close()
			} else if err == nil || !strings.Contains(err.Error(), test.refused) || strings.Contains(err.Error(), "is retired") {
				t.Fatalf("purge error = %v, want the missing-config refusal %q", err, test.refused)
			}
			warns := 0
			for _, raw := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				var line map[string]any
				if err := json.Unmarshal([]byte(raw), &line); err != nil {
					t.Fatalf("log line is not JSON: %v: %s", err, raw)
				}
				if line["level"] == "WARN" && line["msg"] == config.RetiredEntitlementTokenFileWarning && line["setting"] == config.RetiredEntitlementTokenFileEnv {
					warns++
				}
			}
			if warns != 1 || strings.Contains(out.String(), "/run/secrets/ops-token") {
				t.Fatalf("%d retired-setting Warn lines, want 1 without the value: %s", warns, out.String())
			}
		})
	}
}
