package hosted

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// CHAOS-12094: buildMCPHopPolicy turns on acr-api's hop check only with
// ACR_MCP_HOP_REQUIRED, over Kubernetes TokenReview with the hop audience
// and acr-api's own reviewer token, and accepts only the configured acr-mcp
// ServiceAccount.

func hopJWT(t *testing.T, expiry time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"exp": expiry.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestBuildMCPHopPolicyIsOffUnlessRequired(t *testing.T) {
	policy, err := buildMCPHopPolicy(config.Config{OAuthIssuer: "https://acr.example.test"}, time.Now, func(string) (string, bool) { return "", false })
	if err != nil || policy != nil {
		t.Fatalf("hop not required: policy=%v err=%v, want nil, nil", policy, err)
	}
}

func TestBuildMCPHopPolicyChecksTheServiceAccountThroughTokenReview(t *testing.T) {
	now := time.Now().UTC()
	valid := hopJWT(t, now.Add(10*time.Minute))
	other := hopJWT(t, now.Add(9*time.Minute))
	var reviews atomic.Int64
	var gotAudience, gotReviewer atomic.Value
	review := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reviews.Add(1)
		if r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			http.NotFound(w, r)
			return
		}
		gotReviewer.Store(r.Header.Get("Authorization"))
		var body struct {
			Spec struct {
				Token     string   `json:"token"`
				Audiences []string `json:"audiences"`
			} `json:"spec"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotAudience.Store(fmt.Sprint(body.Spec.Audiences))
		username := map[string]string{valid: "system:serviceaccount:acr:acr-mcp", other: "system:serviceaccount:acr:acr-api"}[body.Spec.Token]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{
			"authenticated": username != "", "audiences": body.Spec.Audiences,
			"user": map[string]any{"username": username, "uid": "uid-1"},
		}})
	}))
	t.Cleanup(review.Close)
	reviewerToken := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(reviewerToken, []byte("reviewer-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		envKubernetesAPIServerURL:      review.URL,
		envKubernetesReviewerTokenPath: reviewerToken,
		envKubernetesCACertPath:        filepath.Join(t.TempDir(), "absent-ca.crt"),
	}
	cfg := config.Config{OAuthIssuer: "https://acr.example.test", MCPHop: config.MCPHopConfig{
		Required: true, Audience: config.DefaultMCPHopAudience, Namespace: "acr", ServiceAccount: "acr-mcp",
	}}
	policy, err := buildMCPHopPolicy(cfg, time.Now, func(key string) (string, bool) { value, ok := env[key]; return value, ok })
	if err != nil || policy == nil {
		t.Fatalf("hop required: policy=%v err=%v", policy, err)
	}

	audit := memory.NewAuditStore()
	store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(store, auth.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(t.Context(), auth.CreateCredentialRequest{OrgID: "org_1", Name: "mcp", RepositoryScopes: []string{"o/r"}, Scopes: []string{auth.ScopeContextRead}, CreatedBy: "test", Resource: "https://mcp.example.test/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewAuthenticator(store, audit, auth.AuthenticatorOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), MCPHop: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func(hop string) int {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
		request.Header.Set("Authorization", "Bearer "+issued.Token)
		request.Header.Set(auth.ResourceHeader, "https://mcp.example.test/mcp")
		if hop != "" {
			request.Header.Set(auth.MCPHopHeader, hop)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if got := call(""); got != http.StatusUnauthorized {
		t.Fatalf("no hop = %d, want 401", got)
	}
	if got := call(other); got != http.StatusUnauthorized {
		t.Fatalf("acr-api's own ServiceAccount token as the hop = %d, want 401", got)
	}
	if got := call(valid); got != http.StatusOK {
		t.Fatalf("acr-mcp's token = %d, want 200", got)
	}
	if got := gotAudience.Load(); got != "["+config.DefaultMCPHopAudience+"]" {
		t.Fatalf("TokenReview audiences = %v, want [%s]", got, config.DefaultMCPHopAudience)
	}
	if got := gotReviewer.Load(); got != "Bearer reviewer-token" {
		t.Fatalf("TokenReview caller = %v, want acr-api's reviewer token", got)
	}
	before := reviews.Load()
	if got := call(valid); got != http.StatusOK || reviews.Load() != before {
		t.Fatalf("second call with the same live token: %d, reviews %d -> %d; want 200 from the cache", got, before, reviews.Load())
	}
}
