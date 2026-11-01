package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

const fakeWorkloadToken = "workload-token-not-for-production"

type fakeTokenSource struct {
	mu        sync.Mutex
	token     string
	err       error
	audiences []string
	scopes    [][]string
	deadlines []bool
}

func (f *fakeTokenSource) Token(ctx context.Context, audience string, scope ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audiences = append(f.audiences, audience)
	f.scopes = append(f.scopes, scope)
	_, hasDeadline := ctx.Deadline()
	f.deadlines = append(f.deadlines, hasDeadline)
	return f.token, f.err
}

type seenRequest struct{ path, authorization string }

func allRoutesServer(t *testing.T) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, seenRequest{r.URL.Path, r.Header.Get("Authorization")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/health"):
			_, _ = w.Write([]byte(`{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}`))
		case strings.Contains(r.URL.Path, "/entitlements/"):
			_, _ = w.Write([]byte(`{"schema_version":"acr_entitlement.v1","org_id":"` + accessOrg + `","agent_context_runtime":true}`))
		case strings.HasSuffix(r.URL.Path, "/llm-config"):
			_, _ = w.Write([]byte(llmBody(nil)))
		case strings.HasSuffix(r.URL.Path, "/access"):
			_, _ = w.Write([]byte(accessDocument(true, `{"kind":"none"}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), seen...)
	}
}

func callAllFive(t *testing.T, client *entitlements.Client) []error {
	t.Helper()
	ctx := context.Background()
	var errs []error
	errs = append(errs, client.Check(ctx))
	_, err := client.HasEntitlement(ctx, accessOrg, "agent_context_runtime")
	errs = append(errs, err)
	_, err = client.OrganizationUnknown(ctx, accessOrg)
	errs = append(errs, err)
	_, _, err = client.OrgLLMConfig(ctx, llmOrg)
	errs = append(errs, err)
	_, err = client.PrincipalAccess(ctx, accessOrg, accessUser, accessSession)
	errs = append(errs, err)
	return errs
}

func TestWorkloadCredentialSendsTheMintedTokenOnEveryCall(t *testing.T) {
	server, seen := allRoutesServer(t)
	source := &fakeTokenSource{token: fakeWorkloadToken}
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{Workload: source}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for i, err := range callAllFive(t, client) {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	got := seen()
	if len(got) != 5 {
		t.Fatalf("requests = %d, want 5", len(got))
	}
	for _, request := range got {
		if request.authorization != "Bearer "+fakeWorkloadToken {
			t.Fatalf("%s sent %q, want the workload token", request.path, request.authorization)
		}
	}
	for i, bounded := range source.deadlines {
		if !bounded {
			t.Fatalf("token request %d ran without the call's timeout: a slow mint would outlive the entitlement timeout", i)
		}
	}
	for i, audience := range source.audiences {
		if audience != "dev-health-api" || len(source.scopes[i]) != 0 {
			t.Fatalf("token request %d = (%q, %v), want (dev-health-api, no scope)", i, audience, source.scopes[i])
		}
	}
}

func TestWorkloadCredentialNeverOpensTheTokenFile(t *testing.T) {
	server, seen := allRoutesServer(t)
	source := &fakeTokenSource{token: fakeWorkloadToken}
	// A path that does not exist: a read would fail New, and a read of a real file would leak its value.
	missing := filepath.Join(t.TempDir(), "no-such-svc-token")
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{Workload: source, TokenFile: missing}))
	if err != nil {
		t.Fatalf("New with a token file set in workload mode: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the test file exists")
	}
	file := writeToken(t, testToken)
	client2, err := entitlements.New(clientConfig(t, server, entitlements.Config{Workload: source, TokenFile: file}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client2.Close() })
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := client2.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, request := range seen() {
		if strings.Contains(request.authorization, testToken) {
			t.Fatalf("the service token reached the server: %q", request.authorization)
		}
	}
}

func TestWorkloadCredentialFailsClosedWhenNoTokenCanBeMinted(t *testing.T) {
	server, seen := allRoutesServer(t)
	source := &fakeTokenSource{err: errors.New("exchange unavailable")}
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{Workload: source, TokenFile: writeToken(t, testToken)}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for i, err := range callAllFive(t, client) {
		if err == nil {
			t.Fatalf("call %d succeeded with no token", i)
		}
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("%d requests reached the server with no token, first %+v", len(got), got[0])
	}
}

func TestWorkloadCredentialObservesEachCall(t *testing.T) {
	server, _ := allRoutesServer(t)
	var mu sync.Mutex
	counts := map[string]int{}
	observe := func(_ context.Context, credential, outcome string) {
		mu.Lock()
		counts[credential+"/"+outcome]++
		mu.Unlock()
	}
	source := &fakeTokenSource{token: fakeWorkloadToken}
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{Workload: source, Observe: observe}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	callAllFive(t, client)
	// The entitlement and llm-config answers are cached: the second round sends three calls, not five.
	source.mu.Lock()
	source.err = errors.New("down")
	source.mu.Unlock()
	callAllFive(t, client)
	legacy, err := entitlements.New(clientConfig(t, server, entitlements.Config{Observe: observe}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	if err := legacy.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["workload/sent"] != 5 || counts["workload/token_unavailable"] != 3 || counts["service_token/sent"] != 1 || len(counts) != 3 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestServiceTokenCredentialIsUnchangedWithoutAWorkloadSource(t *testing.T) {
	server, seen := allRoutesServer(t)
	client, err := entitlements.New(clientConfig(t, server, entitlements.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for i, err := range callAllFive(t, client) {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	for _, request := range seen() {
		if request.authorization != "Bearer "+testToken {
			t.Fatalf("%s sent %q", request.path, request.authorization)
		}
	}
}
