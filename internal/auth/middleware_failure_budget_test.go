package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func failureBudgetFixture(t *testing.T, attemptLimit, failureLimit int) (http.Handler, string, *atomic.Int64, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	credentialStore := newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore())
	auditStore := memory.NewAuditStore()
	issued := issueForMiddleware(t, credentialStore, auditStore, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, nil)
	limiter := NewMemoryLimiter(time.Minute, attemptLimit, failureLimit)
	authenticator := newTestAuthenticator(t, credentialStore, auditStore, now, limiter)
	reached := &atomic.Int64{}
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	return handler, issued.Token, reached, now
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
	if reached.Load() != 25 {
		t.Fatalf("handler reached %d times, want 25", reached.Load())
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

func TestLockedOutAddressIsRefusedBeforeAnyCredentialLookup(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	inner := newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore())
	issued := issueForMiddleware(t, inner, memory.NewAuditStore(), now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, nil)
	store := &countingCredentialStore{CredentialStore: inner}
	authenticator := newTestAuthenticator(t, store, memory.NewAuditStore(), now, NewMemoryLimiter(time.Minute, 100, 2))
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	callWithToken(handler, bad)
	callWithToken(handler, bad)
	before := store.lookups
	if code := callWithToken(handler, issued.Token); code != http.StatusTooManyRequests {
		t.Fatalf("locked-out address = %d, want 429", code)
	}
	if store.lookups != before {
		t.Fatalf("locked-out request hit the credential store: %d -> %d", before, store.lookups)
	}
}

// gatedCredentialStore holds every lookup until `want` lookups are in flight
// (or a timeout), so concurrent guesses all pass the address gate before any
// of them records its failure.
type gatedCredentialStore struct {
	storage.CredentialStore
	mu      sync.Mutex
	arrived int
	want    int
	release chan struct{}
	once    sync.Once
}

func (s *gatedCredentialStore) FindByTokenHash(_ context.Context, _ string) (contractsv1.ClientCredential, error) {
	s.mu.Lock()
	s.arrived++
	if s.arrived >= s.want {
		s.once.Do(func() { close(s.release) })
	}
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-time.After(300 * time.Millisecond):
	}
	return contractsv1.ClientCredential{}, storage.ErrNotFound
}

func TestConcurrentGuessesCannotCrossTheFailureCeiling(t *testing.T) {
	const limit, burst = 3, 25
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	store := &gatedCredentialStore{CredentialStore: newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore()), want: burst, release: make(chan struct{})}
	authenticator := newTestAuthenticator(t, store, memory.NewAuditStore(), now, NewMemoryLimiter(time.Minute, 1000, limit))
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var wg sync.WaitGroup
	codes := make([]int, burst)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = callWithToken(handler, bad)
		}()
	}
	wg.Wait()
	unauthorized, limited := 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if unauthorized != limit || limited != burst-limit {
		t.Fatalf("concurrent guesses admitted to lookup = %d (limit %d), 429 = %d", unauthorized, limit, limited)
	}
}

func TestConcurrentValidRequestsBelowTheBudgetAreNotRefused(t *testing.T) {
	handler, token, _, _ := failureBudgetFixture(t, 1000, 20)
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = callWithToken(handler, token)
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("concurrent valid request %d = %d, want 200", i, code)
		}
	}
}

func TestSuccessDoesNotResetPriorFailures(t *testing.T) {
	handler, token, _, _ := failureBudgetFixture(t, 1000, 3)
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	steps := []struct {
		token string
		want  int
	}{{bad, 401}, {bad, 401}, {token, 200}, {bad, 401}, {token, 429}, {bad, 429}}
	for i, step := range steps {
		if code := callWithToken(handler, step.token); code != step.want {
			t.Fatalf("step %d = %d, want %d", i, code, step.want)
		}
	}
}
