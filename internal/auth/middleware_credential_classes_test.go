package auth

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func callWithoutCredential(handler http.Handler) int {
	return callFrom(handler, "192.0.2.10:4000", "").Code
}

func callFrom(handler http.Handler, remote, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.RemoteAddr = remote
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestValidTokenAfterNoCredentialRequestsFromItsAddressIsServed(t *testing.T) {
	handler, token, _, _ := failureBudgetFixture(t, 100, 3)
	for i := 0; i < 25; i++ {
		if code := callWithoutCredential(handler); code != http.StatusUnauthorized {
			t.Fatalf("no-credential request %d = %d, want 401 (never counted)", i, code)
		}
	}
	if code := callWithToken(handler, token); code != http.StatusOK {
		t.Fatalf("valid token after 25 no-credential requests from its address = %d, want 200", code)
	}
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for i, want := range []int{401, 401, 401, 429} {
		if code := callWithToken(handler, bad); code != want {
			t.Fatalf("rejected credential %d after the no-credential requests = %d, want %d", i, code, want)
		}
	}
}

func TestValidTokenAfterRejectedCredentialsFromItsAddressIsServed(t *testing.T) {
	handler, token, _, _ := failureBudgetFixture(t, 100, 3)
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for i := 0; i < 25; i++ {
		callWithToken(handler, bad)
	}
	if code := callWithToken(handler, token); code != http.StatusOK {
		t.Fatalf("valid token after 25 rejected credentials from its address = %d, want 200", code)
	}
}

func TestNoCredentialAnswerIsTheSameUnderAndOverTheFailureBudget(t *testing.T) {
	handler, _, _, _ := failureBudgetFixture(t, 100, 3)
	before := callFrom(handler, "192.0.2.10:4000", "")
	for i := 0; i < 5; i++ {
		callWithToken(handler, "junk")
	}
	during := callFrom(handler, "192.0.2.10:4000", "")
	if before.Code != http.StatusUnauthorized || during.Code != before.Code || !bytes.Equal(during.Body.Bytes(), before.Body.Bytes()) || !sameHeaders(before.Header(), during.Header()) {
		t.Fatalf("no-credential answer over budget = %d %v %q, want %d %v %q", during.Code, during.Header(), during.Body.String(), before.Code, before.Header(), before.Body.String())
	}
}

// Unknown, revoked and expired credentials get the same answer, under the
// failure budget (401) and over it (429): the answer never says whether a
// credential exists.
func TestRejectedCredentialClassesAreAnsweredAlike(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	store := newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore())
	expiredAt := now.Add(-time.Minute)
	revoked := issueForMiddleware(t, store, nil, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, nil)
	if _, err := store.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: "org_1", CredentialID: revoked.Credential.CredentialID, ActorID: "admin"}); err != nil {
		t.Fatal(err)
	}
	expired := issueForMiddleware(t, store, nil, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, &expiredAt)
	unknown := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, phase := range []struct {
		name    string
		prefill int
		want    int
	}{{"under budget", 0, http.StatusUnauthorized}, {"over budget", 3, http.StatusTooManyRequests}} {
		answers := map[string]*httptest.ResponseRecorder{}
		for name, token := range map[string]string{"unknown": unknown, "revoked": revoked.Token, "expired": expired.Token} {
			authenticator := newTestAuthenticator(t, store, memory.NewAuditStore(), now, NewMemoryLimiter(time.Minute, 100, 3))
			handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			for i := 0; i < phase.prefill; i++ {
				callWithToken(handler, "junk")
			}
			answers[name] = callFrom(handler, "192.0.2.10:4000", "Bearer "+token)
		}
		reference := answers["unknown"]
		if reference.Code != phase.want {
			t.Fatalf("%s: unknown credential = %d, want %d", phase.name, reference.Code, phase.want)
		}
		for name, got := range answers {
			if got.Code != reference.Code || !bytes.Equal(got.Body.Bytes(), reference.Body.Bytes()) || !sameHeaders(got.Header(), reference.Header()) {
				t.Fatalf("%s: %s answer = %d %v %q, unknown = %d %v %q", phase.name, name, got.Code, got.Header(), got.Body.String(), reference.Code, reference.Header(), reference.Body.String())
			}
		}
	}
}

func sameHeaders(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for name, values := range a {
		if strings.Join(values, "\x00") != strings.Join(b.Values(name), "\x00") {
			return false
		}
	}
	return true
}

// heldLookupStore parks every lookup of one token hash until released (then
// answers it from the inner store) or until the request is abandoned, and
// records how many were parked at once; other lookups pass through.
type heldLookupStore struct {
	storage.CredentialStore
	held     string
	release  chan struct{}
	mu       sync.Mutex
	parked   int
	maxSeen  int
	arrivals int
}

func (s *heldLookupStore) FindByTokenHash(ctx context.Context, hash string) (contractsv1.ClientCredential, error) {
	if hash != s.held {
		return s.CredentialStore.FindByTokenHash(ctx, hash)
	}
	s.mu.Lock()
	s.parked++
	s.arrivals++
	s.maxSeen = max(s.maxSeen, s.parked)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.parked--
		s.mu.Unlock()
	}()
	select {
	case <-s.release:
	case <-ctx.Done():
		return contractsv1.ClientCredential{}, ctx.Err()
	case <-time.After(5 * time.Second):
	}
	return s.CredentialStore.FindByTokenHash(ctx, hash)
}

func (s *heldLookupStore) counts() (parked, maxSeen, arrivals int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parked, s.maxSeen, s.arrivals
}

// An address over its failure limit verifies at most one well-formed
// credential at a time: a concurrent flood of guesses causes one store
// lookup, the rest are refused unverified, and a valid credential from
// another address is served while the slot is held.
func TestOverBudgetAddressVerifiesOneCredentialAtATime(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	inner := newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore())
	valid := issueForMiddleware(t, inner, nil, now.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, nil)
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	store := &heldLookupStore{CredentialStore: inner, release: make(chan struct{})}
	var logs bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewJSONHandler(lockedWriter{&logs, &logMu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: NewMemoryLimiter(time.Minute, 100, 3), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	const attacker = "192.0.2.50:4000"
	for i := 0; i < 3; i++ {
		callFrom(handler, attacker, "Bearer junk")
	}
	store.held = HashToken(bad)

	const flood = 40
	var refused atomic.Int64
	var wg sync.WaitGroup
	codes := make([]int, flood)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = callFrom(handler, attacker, "Bearer "+bad).Code
			if codes[i] == http.StatusTooManyRequests {
				refused.Add(1)
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		parked, _, _ := store.counts()
		if parked == 1 && refused.Load() == flood-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("flood did not settle: parked=%d refused=%d", parked, refused.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if code := callFrom(handler, "192.0.2.51:4000", "Bearer "+valid.Token).Code; code != http.StatusOK {
		t.Fatalf("valid credential from another address while the slot is held = %d, want 200", code)
	}
	if code := callFrom(handler, attacker, "Bearer "+valid.Token).Code; code != http.StatusTooManyRequests {
		t.Fatalf("valid credential from the flooding address while its slot is held = %d, want 429", code)
	}
	close(store.release)
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusTooManyRequests {
			t.Fatalf("over-budget guess %d = %d, want 429", i, code)
		}
	}
	if _, maxSeen, arrivals := store.counts(); maxSeen != 1 || arrivals != 1 {
		t.Fatalf("over-budget flood: lookups %d, most at once %d; want 1 and 1", arrivals, maxSeen)
	}
	if code := callFrom(handler, attacker, "Bearer "+valid.Token).Code; code != http.StatusOK {
		t.Fatalf("valid credential from the flooding address after the slot is free = %d, want 200", code)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if n := strings.Count(logs.String(), `"level":"INFO","msg":"ACR authentication attempt refused","reason":"verification_slot"`); n != 1 {
		t.Fatalf("Info verification_slot refusal lines = %d, want 1 (the rest Debug):\n%s", n, logs.String())
	}
}

type lockedWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
