package hosted

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

	"github.com/full-chaos/dev-health-go/authverify"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

const workloadTestIssuer = "https://auth.example.test"

func mintedWorkloadToken(audience string) string {
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	header := `{"alg":"EdDSA","typ":"workload+jwt","kid":"test-key"}`
	claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"prn_AAAAAAAAAAAAAAAAAAAAAA","principal_type":"workload","jti":"%024d","iat":%d,"nbf":%d,"exp":%d}`,
		workloadTestIssuer, audience, 1, now-5, now-5, now+295)
	return enc.EncodeToString([]byte(header)) + "." + enc.EncodeToString([]byte(claims)) + "." + enc.EncodeToString(make([]byte, 64))
}

type workloadStack struct {
	cfg          config.Config
	audiences    func() []string
	opsAuthz     func() []string
	tokenFile    string
	subjectToken string
}

func newWorkloadStack(t *testing.T, credential string) workloadStack {
	t.Helper()
	var mu sync.Mutex
	var audiences, authz []string
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		mu.Lock()
		audiences = append(audiences, form.Get("audience"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:jwt","expires_in":300}`, mintedWorkloadToken(form.Get("audience")))
	}))
	t.Cleanup(exchange.Close)
	ops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authz = append(authz, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}`))
	}))
	t.Cleanup(ops.Close)
	dir := t.TempDir()
	subject := filepath.Join(dir, "subject")
	enc := base64.RawURLEncoding
	if err := os.WriteFile(subject, []byte(enc.EncodeToString([]byte(`{"alg":"RS256"}`))+"."+enc.EncodeToString([]byte(`{"sub":"x"}`))+"."+enc.EncodeToString([]byte("sig"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "svc-token-never-created")
	return workloadStack{
		cfg: config.Config{
			Environment: "production", DevHealthEntitlementURL: ops.URL, DevHealthEntitlementTokenFile: tokenFile,
			DevHealthEntitlementCredential: credential,
			AuthWorkload:                   config.AuthWorkloadConfig{ExchangeURL: exchange.URL, AllowPlainHTTP: true, Issuer: workloadTestIssuer, SubjectFile: subject},
		},
		audiences: func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), audiences...) },
		opsAuthz:  func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), authz...) },
		tokenFile: tokenFile,
	}
}

func TestNewEntitlementWorkloadSendsAnExchangedTokenAndNeverOpensTheTokenFile(t *testing.T) {
	stack := newWorkloadStack(t, "workload")
	provider, err := newEntitlement(stack.cfg, entitlementDeps{})
	if err != nil {
		t.Fatalf("newEntitlement with a token file path that does not exist: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if err := provider.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	authz := stack.opsAuthz()
	if len(authz) != 1 || !strings.HasPrefix(authz[0], "Bearer ey") || strings.Count(authz[0], ".") != 2 {
		t.Fatalf("ops saw %q, want one workload token", authz)
	}
	if got := stack.audiences(); len(got) != 1 || got[0] != "dev-health-api" {
		t.Fatalf("exchange audiences = %v, want [dev-health-api]", got)
	}
}

func TestNewEntitlementServiceTokenDefaultReadsTheTokenFileAndNeverExchanges(t *testing.T) {
	stack := newWorkloadStack(t, "")
	if err := os.WriteFile(stack.tokenFile, []byte("svc_acr_test-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := newEntitlement(stack.cfg, entitlementDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if err := provider.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stack.opsAuthz(); len(got) != 1 || got[0] != "Bearer svc_acr_test-value" {
		t.Fatalf("ops saw %q", got)
	}
	if got := stack.audiences(); len(got) != 0 {
		t.Fatalf("the exchange was called in service_token mode: %v", got)
	}
}

func TestNewEntitlementWorkloadWithoutTheAuthClientRefusesToBuild(t *testing.T) {
	stack := newWorkloadStack(t, "workload")
	stack.cfg.AuthWorkload = config.AuthWorkloadConfig{}
	if _, err := newEntitlement(stack.cfg, entitlementDeps{}); err == nil {
		t.Fatal("workload mode built with no auth workload client")
	}
}

func TestNewEntitlementWorkloadCountsEveryCallByCredential(t *testing.T) {
	stack := newWorkloadStack(t, "workload")
	metrics, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	provider, err := newEntitlement(stack.cfg, entitlementDeps{metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if err := provider.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	cell := hostedmetrics.EntitlementCredentialName + "{class=workload,outcome=sent}"
	if got := read()[cell]; got != 1 {
		t.Fatalf("%s = %d, want 1 (cells %v)", cell, got, read())
	}
}

type staticKeys map[string]ed25519.PublicKey

func (k staticKeys) PublicKey(kid string) (ed25519.PublicKey, bool) {
	key, ok := k[kid]
	return key, ok
}

// The token the workload credential sends passes the verifier go-api runs on its internal acr routes: the real
// authverify.WorkloadVerifier, audience dev-health-api, then the sub pin. A second subject and a wrong-audience token are
// refused, so the cell can fail.
func TestNewEntitlementWorkloadTokenPassesTheGoAPIWorkloadVerifier(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const pinnedSubject = "prn_AAAAAAAAAAAAAAAAAAAAAA"
	sign := func(audience, subject string) string {
		enc := base64.RawURLEncoding
		now := time.Now().Unix()
		input := enc.EncodeToString([]byte(`{"alg":"EdDSA","typ":"workload+jwt","kid":"k1"}`)) + "." +
			enc.EncodeToString([]byte(fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":%q,"principal_type":"workload","jti":"%024d","iat":%d,"nbf":%d,"exp":%d}`,
				workloadTestIssuer, audience, subject, 7, now-5, now-5, now+295)))
		return input + "." + enc.EncodeToString(ed25519.Sign(private, []byte(input)))
	}
	var mu sync.Mutex
	exchangeSubject, exchangeAudience := pinnedSubject, ""
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		mu.Lock()
		subject, audience := exchangeSubject, form.Get("audience")
		if exchangeAudience != "" {
			audience = exchangeAudience
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:jwt","expires_in":300}`, sign(audience, subject))
	}))
	t.Cleanup(exchange.Close)
	verifier, err := authverify.NewWorkloadVerifier(authverify.WorkloadVerifierConfig{Issuer: workloadTestIssuer, Audience: "dev-health-api", Keys: staticKeys{"k1": public}})
	if err != nil {
		t.Fatal(err)
	}
	ops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), time.Now())
		if err != nil || claims.Subject != pinnedSubject {
			http.Error(w, "refused", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}`))
	}))
	t.Cleanup(ops.Close)
	dir := t.TempDir()
	subjectFile := filepath.Join(dir, "subject")
	enc := base64.RawURLEncoding
	if err := os.WriteFile(subjectFile, []byte(enc.EncodeToString([]byte(`{"alg":"RS256"}`))+"."+enc.EncodeToString([]byte(`{"sub":"x"}`))+"."+enc.EncodeToString([]byte("sig"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := func() entitlementChecker {
		provider, err := newEntitlement(config.Config{
			Environment: "production", DevHealthEntitlementURL: ops.URL, DevHealthEntitlementCredential: "workload",
			AuthWorkload: config.AuthWorkloadConfig{ExchangeURL: exchange.URL, AllowPlainHTTP: true, Issuer: workloadTestIssuer, SubjectFile: subjectFile},
		}, entitlementDeps{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = provider.Close() })
		return provider
	}
	if err := build().Check(context.Background()); err != nil {
		t.Fatalf("the verified workload token was refused: %v", err)
	}
	mu.Lock()
	exchangeSubject = "prn_BBBBBBBBBBBBBBBBBBBBBB"
	mu.Unlock()
	if err := build().Check(context.Background()); err == nil {
		t.Fatal("a token for another subject was accepted")
	}
	mu.Lock()
	exchangeSubject, exchangeAudience = pinnedSubject, "acr-api"
	mu.Unlock()
	if err := build().Check(context.Background()); err == nil {
		t.Fatal("a token for another audience was accepted")
	}
}
