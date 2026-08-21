package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// TestAuthenticator_workloadTokenSetsSubjectToTheBindingID is CHAOS-4013's
// central Principal derivation contract: a workload-exchanged token's
// Principal.Subject is the STABLE binding_id (so quotas -- which key on
// Subject, see internal/api/limits_middleware.go -- survive a workload's
// ~10-minute re-exchange cycle), while Principal.CredentialID stays the
// churning per-exchange row id. An ordinary credential's Subject is
// unaffected (see TestAuthenticatorAllowsAuthorizedReadAndTracksUsage,
// which asserts the pre-existing Subject == CredentialID case).
func TestAuthenticator_workloadTokenSetsSubjectToTheBindingID(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentialStore, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := NewService(credentialStore, ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewWorkloadAccessTokenIssuer(credentials, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	binding := WorkloadBinding{BindingID: "wlb_panel_read_1", OrgID: "org_1", Role: "read", RepositoryScopes: []string{"*"}}
	issued, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	authenticator := newTestAuthenticator(t, credentialStore, audit, now, NewMemoryLimiter(time.Minute, 10, 5))
	var capturedSubject, capturedCredentialID string
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("expected a principal in context")
		}
		capturedSubject, capturedCredentialID = principal.Subject, principal.CredentialID
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/context", nil)
	request.RemoteAddr = "192.0.2.20:4242"
	request.Header.Set("Authorization", "Bearer "+issued.Token)
	response := httptest.NewRecorder()
	authenticator.Middleware(terminal).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if capturedSubject != "wlb_panel_read_1" {
		t.Fatalf("Subject = %q, want the binding_id", capturedSubject)
	}
	if capturedCredentialID != issued.Credential.CredentialID {
		t.Fatalf("CredentialID = %q, want the issued row's own id %q", capturedCredentialID, issued.Credential.CredentialID)
	}
	if capturedSubject == capturedCredentialID {
		t.Fatal("Subject and CredentialID must differ for a workload-exchanged token")
	}
}
