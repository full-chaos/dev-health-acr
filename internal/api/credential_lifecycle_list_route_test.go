package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// countingClassStore authenticates every bearer as one credential row and
// counts the lookups, so a cell can tell a refusal before the credential
// lookup from one after it.
type countingClassStore struct {
	classCredentialStore
	lookups *atomic.Int64
}

func (s countingClassStore) FindByTokenHash(ctx context.Context, hash string) (contractsv1.ClientCredential, error) {
	s.lookups.Add(1)
	return s.classCredentialStore.FindByTokenHash(ctx, hash)
}

type listerFunc func(ctx context.Context, orgID, after string, limit int) ([]contractsv1.CredentialLifecycleItem, error)

func (f listerFunc) ListCredentialLifecycle(ctx context.Context, orgID, after string, limit int) ([]contractsv1.CredentialLifecycleItem, error) {
	return f(ctx, orgID, after, limit)
}

type lifecycleListRig struct {
	app     *App
	token   string
	lookups *atomic.Int64
	reads   *atomic.Int64
	audit   *memory.AuditStore
	failure *error
}

func newLifecycleListRig(t *testing.T, class storage.CredentialClientClass, scopes []string) lifecycleListRig {
	t.Helper()
	app, _ := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	audit, ok := app.runtime.Audit.(*memory.AuditStore)
	if !ok {
		t.Fatalf("test app audit store is %T", app.runtime.Audit)
	}
	rig := lifecycleListRig{app: app, lookups: &atomic.Int64{}, reads: &atomic.Int64{}, audit: audit, failure: new(error)}
	app.runtime.CredentialLifecycle = listerFunc(func(ctx context.Context, orgID, after string, limit int) ([]contractsv1.CredentialLifecycleItem, error) {
		rig.reads.Add(1)
		if *rig.failure != nil {
			return nil, *rig.failure
		}
		return audit.ListCredentialLifecycle(ctx, orgID, after, limit)
	})
	authenticator, err := auth.NewAuthenticator(countingClassStore{
		lookups: rig.lookups,
		classCredentialStore: classCredentialStore{row: contractsv1.ClientCredential{
			CredentialID: "list_caller", OrgID: "org_1", Scopes: scopes, RepositoryScopes: []string{hostedTestRepository},
			Provenance: contractsv1.CredentialProvenance{ClientClass: string(class)},
		}},
	}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	app.authenticator = authenticator
	rig.token, err = auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return rig
}

func (r lifecycleListRig) seed(t *testing.T, org string, count int) []string {
	t.Helper()
	service, err := auth.NewService(r.app.runtime.Credentials, auth.ServiceOptions{Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range count {
		issued, err := service.Create(context.Background(), auth.CreateCredentialRequest{OrgID: org, Name: "seed", RepositoryScopes: []string{hostedTestRepository}, Scopes: []string{auth.ScopeContextRead}, CreatedBy: "actor"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, issued.Credential.CredentialID)
	}
	return ids
}

func (r lifecycleListRig) get(query string, bearer *string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, CredentialLifecycleListPath+query, nil)
	request.RemoteAddr = "192.0.2.10:4000"
	if bearer != nil {
		request.Header.Set("Authorization", "Bearer "+*bearer)
	}
	response := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(response, request)
	return response
}

func reconcileScopes() []string { return []string{auth.ScopeCredentialLifecycleRead} }

func TestCredentialLifecycleListServesOnlyAWorkloadClassCredentialWithTheScope(t *testing.T) {
	for _, class := range []storage.CredentialClientClass{
		storage.CredentialClientClassBrowser, storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice,
		storage.CredentialClientClassOrgBearer, storage.CredentialClientClassOperator, "",
	} {
		rig := newLifecycleListRig(t, class, reconcileScopes())
		rig.seed(t, "org_1", 2)
		response := rig.get("", &rig.token)
		assertErrorResponse(t, response, http.StatusForbidden, "credential_class_refused")
		if rig.reads.Load() != 0 || rig.lookups.Load() != 1 {
			t.Errorf("class %q: listing reads = %d (want 0), credential lookups = %d (want 1: the class is known only from the row)", class, rig.reads.Load(), rig.lookups.Load())
		}
		if strings.Contains(response.Body.String(), "cred_") {
			t.Errorf("class %q: the refusal names a credential: %s", class, response.Body.String())
		}
	}
}

func TestCredentialLifecycleListRefusesAWorkloadWithoutTheScopeBeforeTheListing(t *testing.T) {
	for _, scopes := range [][]string{
		{auth.ScopeContextRead, auth.ScopeEvidenceRead}, {auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite}, {auth.ScopeDataRead},
	} {
		rig := newLifecycleListRig(t, storage.CredentialClientClassWorkload, scopes)
		response := rig.get("", &rig.token)
		assertErrorResponse(t, response, http.StatusForbidden, "insufficient_scope")
		if rig.reads.Load() != 0 {
			t.Errorf("scopes %v: listing reads = %d, want 0", scopes, rig.reads.Load())
		}
	}
}

func TestCredentialLifecycleListRefusesBeforeAnyLookupWhenTheBearerIsAbsentOrMalformed(t *testing.T) {
	rig := newLifecycleListRig(t, storage.CredentialClientClassWorkload, reconcileScopes())
	malformed := "fcacr_not-a-token"
	jwt := "eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJ4In0.c2ln"
	for name, bearer := range map[string]*string{"absent": nil, "malformed": &malformed, "jwt": &jwt} {
		response := rig.get("", bearer)
		assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
		if rig.lookups.Load() != 0 || rig.reads.Load() != 0 {
			t.Fatalf("%s: credential lookups = %d, listing reads = %d, want 0 and 0", name, rig.lookups.Load(), rig.reads.Load())
		}
	}
	request := httptest.NewRequest(http.MethodGet, CredentialLifecycleListPath, nil)
	request.Header.Set(auth.WebAssertionHeader, "assertion")
	response := httptest.NewRecorder()
	rig.app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || rig.lookups.Load() != 0 || rig.reads.Load() != 0 {
		t.Fatalf("web assertion: status %d, lookups %d, reads %d; want 401, 0, 0", response.Code, rig.lookups.Load(), rig.reads.Load())
	}
}

func TestCredentialLifecycleListPagesTheCallersOrganizationWithThreeKeysOnly(t *testing.T) {
	rig := newLifecycleListRig(t, storage.CredentialClientClassWorkload, reconcileScopes())
	ours := rig.seed(t, "org_1", 5)
	theirs := rig.seed(t, "org_2", 3)
	seen := map[string]bool{}
	after := ""
	pages := 0
	for {
		query := "?limit=2"
		if after != "" {
			query += "&after=" + after
		}
		response := rig.get(query, &rig.token)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
		}
		var raw struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		for _, item := range raw.Items {
			if len(item) != 3 {
				t.Fatalf("item has keys %v, want credential_id, revision, status", item)
			}
		}
		var page contractsv1.CredentialLifecycleList
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if err := page.Validate(); err != nil {
			t.Fatalf("page invalid: %v: %s", err, response.Body.String())
		}
		for _, item := range page.Items {
			if seen[item.CredentialID] {
				t.Fatalf("%s listed twice", item.CredentialID)
			}
			seen[item.CredentialID] = true
		}
		pages++
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
		if pages > 10 {
			t.Fatal("walk does not end")
		}
	}
	for _, id := range ours {
		if !seen[id] {
			t.Errorf("%s missing from the walk", id)
		}
	}
	for _, id := range theirs {
		if seen[id] {
			t.Errorf("%s belongs to another organization", id)
		}
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3 for 5 items at limit 2", pages)
	}
}

func TestCredentialLifecycleListRefusesBadQueriesBeforeTheListing(t *testing.T) {
	rig := newLifecycleListRig(t, storage.CredentialClientClassWorkload, reconcileScopes())
	for _, query := range []string{
		"?limit=0", "?limit=501", "?limit=x", "?limit=-1", "?limit=2&limit=3", "?after=", "?after=a%20b", "?after=" + strings.Repeat("a", 129),
		"?after=a&after=b", "?org_id=org_2", "?sort=desc", "?limit=",
	} {
		assertErrorResponse(t, rig.get(query, &rig.token), http.StatusBadRequest, "invalid_request")
		if rig.reads.Load() != 0 {
			t.Fatalf("query %q reached the listing", query)
		}
	}
}

func TestCredentialLifecycleListDefaultLimitAndStoreFailure(t *testing.T) {
	rig := newLifecycleListRig(t, storage.CredentialClientClassWorkload, reconcileScopes())
	var asked int
	rig.app.runtime.CredentialLifecycle = listerFunc(func(_ context.Context, orgID, _ string, limit int) ([]contractsv1.CredentialLifecycleItem, error) {
		asked = limit
		if orgID != "org_1" {
			t.Errorf("org = %q, want the credential's org_1", orgID)
		}
		return nil, nil
	})
	response := rig.get("", &rig.token)
	if response.Code != http.StatusOK || asked != contractsv1.CredentialLifecycleListDefaultLimit+1 {
		t.Fatalf("status %d, store asked for %d; want 200 and default+1 to detect a next page", response.Code, asked)
	}
	rig.get("?limit=500", &rig.token)
	if asked != contractsv1.CredentialLifecycleListMaxLimit+1 {
		t.Fatalf("store asked for %d at limit 500", asked)
	}

	rig.app.runtime.CredentialLifecycle = listerFunc(func(context.Context, string, string, int) ([]contractsv1.CredentialLifecycleItem, error) {
		return nil, errors.New("database down")
	})
	failed := rig.get("", &rig.token)
	assertErrorResponse(t, failed, http.StatusServiceUnavailable, "upstream_unavailable")
	if strings.Contains(failed.Body.String(), "database down") {
		t.Fatal("the store error text reached the response")
	}

	rig.app.runtime.CredentialLifecycle = nil
	assertErrorResponse(t, rig.get("", &rig.token), http.StatusServiceUnavailable, "upstream_unavailable")
}

func TestCredentialLifecycleListRefusesAValidWebAssertion(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	var reads atomic.Int64
	app.runtime.CredentialLifecycle = listerFunc(func(context.Context, string, string, int) ([]contractsv1.CredentialLifecycleItem, error) {
		reads.Add(1)
		return nil, nil
	})
	request := httptest.NewRequest(http.MethodGet, CredentialLifecycleListPath, nil)
	request.Header.Set(auth.WebAssertionHeader, signAPIAssertionAt(t, private, request, nil, "list_1", now))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
	if reads.Load() != 0 {
		t.Fatalf("listing reads = %d, want 0", reads.Load())
	}
}
