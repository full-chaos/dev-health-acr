package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func failureBudgetFixture(t *testing.T, attemptLimit, failureLimit int) (http.Handler, string, *int, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	credentialStore := newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore())
	auditStore := memory.NewAuditStore()
	issued := issueForMiddleware(t, credentialStore, auditStore, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, nil)
	limiter := NewMemoryLimiter(time.Minute, attemptLimit, failureLimit)
	authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, limiter)
	reached := 0
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	return handler, issued.Token, &reached, now
}

func callWithToken(handler http.Handler, token string) int {
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.RemoteAddr = "192.0.2.10:4000"
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code
}

func TestValidTokenRequestsDoNotConsumePerAddressBudget(t *testing.T) {
	handler, token, reached, _ := failureBudgetFixture(t, 3, 3)
	for i := 0; i < 25; i++ {
		if code := callWithToken(handler, token); code != http.StatusOK {
			t.Fatalf("valid request %d from one address = %d, want 200", i, code)
		}
	}
	if *reached != 25 {
		t.Fatalf("handler reached %d times, want 25", *reached)
	}
}

func TestInvalidTokenAttemptsAreLimitedPerAddress(t *testing.T) {
	handler, token, _, _ := failureBudgetFixture(t, 100, 3)
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var codes []int
	for i := 0; i < 5; i++ {
		codes = append(codes, callWithToken(handler, bad))
	}
	for i, want := range []int{401, 401, 401, 429, 429} {
		if codes[i] != want {
			t.Fatalf("invalid attempt %d = %d, want %d (all %v)", i, codes[i], want, codes)
		}
	}
	// Budget is per address and failure-only: it is not reset by a success,
	// so a guessing burst cannot be laundered through one valid token.
	if code := callWithToken(handler, token); code != http.StatusTooManyRequests {
		t.Fatalf("valid token after exhausted failure budget = %d, want 429", code)
	}
}
