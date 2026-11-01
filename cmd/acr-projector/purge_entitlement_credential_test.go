package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestPurgeEntitlementClientHonorsTheWorkloadCredentialFlag(t *testing.T) {
	opsURL, exchangeURL, authz := purgeFakes(t)
	dir := t.TempDir()
	subject := filepath.Join(dir, "subject")
	enc := base64.RawURLEncoding
	if err := os.WriteFile(subject, []byte(enc.EncodeToString([]byte(`{"alg":"RS256"}`))+"."+enc.EncodeToString([]byte(`{"sub":"x"}`))+"."+enc.EncodeToString([]byte("sig"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        opsURL,
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": filepath.Join(dir, "svc-token-never-created"),
		"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload",
		"ACR_AUTH_WORKLOAD_EXCHANGE_URL":        exchangeURL,
		"ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP":    "true",
		"ACR_AUTH_WORKLOAD_ISSUER":              "https://auth.example.test",
		"ACR_AUTH_WORKLOAD_SUBJECT_FILE":        subject,
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	client, err := purgeEntitlementClientFrom(lookup)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Check(context.Background()); err == nil {
		t.Log("health body is not the contract: Check is expected to fail after the request was sent")
	}
	got := authz()
	if len(got) != 1 || !strings.HasPrefix(got[0], "Bearer ey") || strings.Contains(got[0], "svc_acr_") {
		t.Fatalf("ops saw %q, want one workload token", got)
	}
}

func TestPurgeEntitlementClientRefusesWorkloadWithoutTheAuthClient(t *testing.T) {
	env := map[string]string{
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "https://ops.example.test",
		"ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL": "workload",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	if _, err := purgeEntitlementClientFrom(lookup); err == nil {
		t.Fatal("workload mode built with no ACR_AUTH_WORKLOAD_*")
	}
	env["ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL"] = "svc_acr"
	if _, err := purgeEntitlementClientFrom(lookup); err == nil || strings.Contains(err.Error(), "svc_acr") {
		t.Fatalf("a bad flag value must be refused without echoing it: %v", err)
	}
}
