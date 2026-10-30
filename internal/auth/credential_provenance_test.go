package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12083: an OAuth exchange records its client and the approving user as
// the credential's provenance, and a self-rotation keeps it.

func oauthIssuedCredential(t *testing.T, h *oauthHarness) (IssuedCredential, string) {
	t.Helper()
	clientID := h.register(t)
	verifier, challenge := pkce(t)
	token, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{
		GrantType: "authorization_code", Code: h.login(t, clientID, challenge, testResource),
		RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier, Resource: testResource,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token.Issued, clientID
}

func TestOAuthExchangeRecordsTheClientAndApproverProvenance(t *testing.T) {
	h := newOAuthHarness(t)

	issued, clientID := oauthIssuedCredential(t, h)

	want := contractsv1.CredentialProvenance{
		ClientClass: string(storage.CredentialClientClassOAuthUser), ApprovingUserID: "user_1", ClientID: clientID,
	}
	if issued.Credential.Provenance != want {
		t.Fatalf("issued provenance = %+v, want %+v", issued.Credential.Provenance, want)
	}
	stored, err := h.creds.FindByTokenHash(context.Background(), HashToken(issued.Token))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Provenance != want {
		t.Fatalf("stored provenance = %+v, want %+v", stored.Provenance, want)
	}
}

func TestOAuthRedeemRefusesAnEmptyClientID(t *testing.T) {
	h := newOAuthHarness(t)
	started, err := h.devices.StartForOAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.approveDevice(t, started.DeviceCodeHash, []string{"org/repo"})

	if _, err := h.devices.RedeemForResource(context.Background(), started.DeviceCodeHash, "", testResource, []string{ScopeContextRead}); !errors.Is(err, ErrInvalidDeviceFlow) {
		t.Fatalf("RedeemForResource without a client id error = %v, want ErrInvalidDeviceFlow", err)
	}
	if _, err := h.devices.PollDeviceGrant(context.Background(), started.DeviceCodeHash, "", testResource, []string{ScopeContextRead}); !errors.Is(err, ErrInvalidDeviceFlow) {
		t.Fatalf("PollDeviceGrant without a client id error = %v, want ErrInvalidDeviceFlow", err)
	}
}

func TestRotateSelfKeepsTheIssuanceProvenance(t *testing.T) {
	h := newOAuthHarness(t)
	issued, _ := oauthIssuedCredential(t, h)
	source := issued.Credential
	service, err := NewService(h.creds, ServiceOptions{Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: source.CredentialID,
		OrgID: source.OrgID, CredentialID: source.CredentialID,
		RepositoryScopes: source.RepositoryScopes, Permissions: source.Scopes,
	}

	rotation, err := service.RotateSelf(context.Background(), principal)

	if err != nil {
		t.Fatal(err)
	}
	if rotation.Issued.Credential.Provenance != source.Provenance {
		t.Fatalf("successor provenance = %+v, want the source's %+v", rotation.Issued.Credential.Provenance, source.Provenance)
	}
	stored, err := h.creds.FindByTokenHash(context.Background(), HashToken(rotation.Issued.Token))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Provenance.ApprovingUserID != "user_1" {
		t.Fatalf("stored successor approving user = %q, want user_1 (not the rotating credential %q)", stored.Provenance.ApprovingUserID, source.CredentialID)
	}
}

func TestAuthenticatedPrincipalCarriesTheOAuthProvenance(t *testing.T) {
	h := newOAuthHarness(t)
	issued, _ := oauthIssuedCredential(t, h)
	authenticator := newTestAuthenticator(t, h.creds, nil, h.now, NewMemoryLimiter(time.Minute, 10, 5))
	var principal storage.Principal
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/context", nil)
	request.RemoteAddr = "192.0.2.10:4242"
	request.Header.Set("Authorization", "Bearer "+issued.Token)
	request.Header.Set(ResourceHeader, testResource)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
	if principal.ClientClass != storage.CredentialClientClassOAuthUser || principal.ApprovingUserID != "user_1" {
		t.Fatalf("principal provenance = class %q approver %q, want oauth_user and user_1", principal.ClientClass, principal.ApprovingUserID)
	}
	if principal.Subject != issued.Credential.CredentialID {
		t.Fatalf("principal subject = %q, want the credential id (owner keys are CHAOS-12091)", principal.Subject)
	}
}
