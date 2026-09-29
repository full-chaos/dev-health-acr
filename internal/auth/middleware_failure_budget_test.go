package auth

import (
	"context"
	"fmt"
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

// gatedCredentialStore holds every lookup until the test releases it, so
// concurrent guesses are all undecided at the same time.
type gatedCredentialStore struct {
	storage.CredentialStore
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func (s *gatedCredentialStore) FindByTokenHash(_ context.Context, _ string) (contractsv1.ClientCredential, error) {
	s.mu.Lock()
	s.arrived++
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-time.After(5 * time.Second):
	}
	return contractsv1.ClientCredential{}, storage.ErrNotFound
}

func TestConcurrentGuessesCannotCrossTheFailureCeiling(t *testing.T) {
	const limit, burst, inflightCap = 3, 100, 64
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	store := &gatedCredentialStore{CredentialStore: newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore()), release: make(chan struct{})}
	authenticator := newTestAuthenticator(t, store, memory.NewAuditStore(), now, NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, AttemptLimit: 1000, FailureLimit: limit, MaxTrackedKeys: 16}))
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var wg sync.WaitGroup
	var refused atomic.Int64
	codes := make([]int, burst)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = callWithToken(handler, bad)
			if codes[i] == http.StatusTooManyRequests {
				refused.Add(1)
			}
		}()
	}
	// Wait until every request is either parked in the store or refused, then
	// let the parked lookups resolve.
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		parked := store.arrived
		store.mu.Unlock()
		if parked == inflightCap && refused.Load() == burst-inflightCap {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("burst did not settle: parked=%d refused=%d", parked, refused.Load())
		}
		time.Sleep(time.Millisecond)
	}
	close(store.release)
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
	if unauthorized != inflightCap || limited != burst-inflightCap {
		t.Fatalf("concurrent guesses admitted to lookup = %d (in-flight cap %d), 429 = %d", unauthorized, inflightCap, limited)
	}
	// Once the burst has resolved the address is over its failure limit: the
	// next guess is refused before any credential lookup.
	store.mu.Lock()
	before := store.arrived
	store.mu.Unlock()
	if code := callWithToken(handler, bad); code != http.StatusTooManyRequests {
		t.Fatalf("guess after the burst = %d, want 429", code)
	}
	store.mu.Lock()
	after := store.arrived
	store.mu.Unlock()
	if after != before {
		t.Fatalf("locked-out guess reached the credential store: %d -> %d", before, after)
	}
}

func TestConcurrentValidRequestsAboveTheFailureLimitAreNotRefused(t *testing.T) {
	// Five simultaneous valid requests with a failure limit of 3: valid
	// requests are not failures, so none may be refused.
	handler, token, _, _ := failureBudgetFixture(t, 1000, 3)
	var wg sync.WaitGroup
	codes := make([]int, 5)
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

func TestManyAddressesInFlightAreBoundedByTheTrackedKeyCap(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 3, MaxTrackedKeys: 1})
	admitted := 0
	for i := 0; i < 25; i++ {
		if _, ok := limiter.BeginAttempt(fmt.Sprintf("192.0.2.%d", i), now); ok {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("addresses in flight with tracked-key cap 1 = %d, want 1", admitted)
	}
}

func TestInFlightReservationsAreBoundedByTrackedKeys(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 3, MaxTrackedKeys: 1, MaxInFlight: 4})
	release, ok := limiter.BeginAttempt("a", now)
	if !ok {
		t.Fatal("first address refused")
	}
	if _, ok := limiter.BeginAttempt("b", now); ok {
		t.Fatal("second address admitted past the tracked-key cap while the first is in flight")
	}
	release()
	// The failure map's own window entry for "a" ages out with the window.
	now = now.Add(time.Minute)
	if _, ok := limiter.BeginAttempt("b", now); !ok {
		t.Fatal("address refused after the reservation was released")
	}
}
