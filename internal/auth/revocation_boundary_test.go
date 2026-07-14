package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestAuthenticator_AllowsAuthenticatedRequestToFinishAfterRevoke(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	credentialStore := newMemoryCredentialStore(t)
	auditStore := memory.NewAuditStore()
	issued := issueForMiddleware(t, credentialStore, auditStore, now, []string{ScopeContextRead}, []string{"owner/repo"}, nil)
	authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, NoopLimiter{})
	service, err := NewService(credentialStore, ServiceOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	authenticated := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(authenticated)
		<-release
		w.WriteHeader(http.StatusNoContent)
		close(completed)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+issued.Token)
	response := httptest.NewRecorder()
	go handler.ServeHTTP(response, request)
	<-authenticated

	// When
	_, err = service.Revoke(context.Background(), issued.Credential.OrgID, issued.Credential.CredentialID, "admin")
	require.NoError(t, err)
	close(release)
	<-completed

	// Then
	require.Equal(t, http.StatusNoContent, response.Code)
	denied := httptest.NewRecorder()
	authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("lookup after revocation reached the handler")
	})).ServeHTTP(denied, request)
	assertContractError(t, denied, http.StatusUnauthorized, "invalid_token")
}
