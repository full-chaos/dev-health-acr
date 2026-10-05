package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// Attempts admitted while the address was under its budget can still be
// verifying when it goes over: no over-budget attempt is admitted until they
// are all decided, so an over-budget address never has a second lookup in
// flight beside its slot.
func TestOverBudgetSlotWaitsForAttemptsAdmittedUnderBudget(t *testing.T) {
	now := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 3, MaxTrackedKeys: 16, MaxInFlight: 64})
	var releases []func()
	for i := 0; i < 10; i++ {
		release, decision := limiter.BeginVerification("a", now)
		if !decision.Admitted() || decision.OverBudget {
			t.Fatalf("attempt %d under budget = %+v, want admitted under budget", i, decision)
		}
		releases = append(releases, release)
	}
	for i := 0; i < 3; i++ {
		limiter.RecordRejection("a", now, false)
		releases[i]()
	}
	if _, decision := limiter.BeginVerification("a", now); decision.Refusal != RefusalVerificationSlot {
		t.Fatalf("over-budget attempt beside 7 undecided attempts = %+v, want verification_slot refusal", decision)
	}
	for _, release := range releases[3:] {
		release()
	}
	release, decision := limiter.BeginVerification("a", now)
	if !decision.Admitted() || !decision.OverBudget {
		t.Fatalf("over-budget attempt with nothing in flight = %+v, want admitted to the slot", decision)
	}
	if _, second := limiter.BeginVerification("a", now); second.Refusal != RefusalVerificationSlot {
		t.Fatalf("second over-budget attempt while the slot is held = %+v, want verification_slot refusal", second)
	}
	release()
}

// With an in-flight cap of 1, a busy slot is still reported as the slot, and
// only its first refusal in the window is reported as first.
func TestBusySlotIsReportedAsTheSlotWhateverTheInFlightCap(t *testing.T) {
	now := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 1, MaxTrackedKeys: 16, MaxInFlight: 1})
	limiter.RecordFailure("a", now)
	release, decision := limiter.BeginVerification("a", now)
	if !decision.Admitted() || !decision.OverBudget {
		t.Fatalf("slot attempt = %+v, want admitted over budget", decision)
	}
	defer release()
	_, first := limiter.BeginVerification("a", now)
	_, second := limiter.BeginVerification("a", now)
	if first.Refusal != RefusalVerificationSlot || !first.FirstRefusal || second.Refusal != RefusalVerificationSlot || second.FirstRefusal {
		t.Fatalf("busy-slot refusals = %+v, %+v; want verification_slot, first then not first", first, second)
	}
}

// A credential verified in the over-budget slot and rejected after the
// window rolled over is still answered as an over-budget rejection (429), and
// its failure is counted in the window where it was decided.
func TestSlotRejectionDecidedAfterWindowRolloverIsStillRefused(t *testing.T) {
	start := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := start
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	inner := newMemoryCredentialStoreAt(t, start.Add(-time.Hour), memory.NewAuditStore())
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	store := &heldLookupStore{CredentialStore: inner, release: make(chan struct{}), held: HashToken(bad)}
	limiter := NewMemoryLimiter(time.Minute, 100, 3)
	authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{Now: now, Limiter: limiter})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	const address = "192.0.2.70:4000"
	for i := 0; i < 3; i++ {
		callFrom(handler, address, "Bearer junk")
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- callFrom(handler, address, "Bearer "+bad) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if parked, _, _ := store.counts(); parked == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot lookup never started")
		}
		time.Sleep(time.Millisecond)
	}
	clockMu.Lock()
	clock = start.Add(2 * time.Minute)
	clockMu.Unlock()
	if code := callFrom(handler, address, "Bearer junk").Code; code != http.StatusUnauthorized {
		t.Fatalf("malformed bearer in the new window = %d, want 401", code)
	}
	close(store.release)
	if response := <-done; response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "60" {
		t.Fatalf("slot rejection decided after the window rolled over = %d Retry-After %q, want 429 with 60 (the rest of the window it was decided in)", response.Code, response.Header().Get("Retry-After"))
	}
	if code := callFrom(handler, address, "Bearer junk").Code; code != http.StatusUnauthorized {
		t.Fatalf("third failure of the new window = %d, want 401", code)
	}
	if code := callFrom(handler, address, "Bearer junk").Code; code != http.StatusTooManyRequests {
		t.Fatalf("fourth failure of the new window = %d, want 429 (the slot rejection counted in the new window)", code)
	}
}

// An address with an undecided attempt keeps a failure entry even when the
// tracked-address table filled up while it was verifying: its rejection is
// counted, so it cannot start a fresh, empty window later.
func TestRejectionOfAnUndecidedAttemptIsCountedWhenTheTableIsFull(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return t0.Add(time.Duration(seconds) * time.Second) }
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 1, MaxTrackedKeys: 2, MaxInFlight: 4})
	limiter.RecordFailure("a", at(0))
	release, slot := limiter.BeginVerification("a", at(10))
	if !slot.Admitted() || !slot.OverBudget {
		t.Fatalf("a in the over-budget slot = %+v", slot)
	}
	for _, other := range []string{"b", "c"} {
		otherRelease, decision := limiter.BeginAttemptDecision(other, at(61))
		if !decision.Admitted() {
			t.Fatalf("%s admitted = %+v", other, decision)
		}
		limiter.RecordFailure(other, at(61))
		otherRelease()
	}
	if refused, _ := limiter.RecordRejection("a", at(65), false); refused {
		t.Fatalf("a's rejection in its new window reported over budget before counting")
	}
	release()
	if _, decision := limiter.BeginVerification("a", at(122)); !decision.OverBudget && decision.Admitted() {
		t.Fatalf("a after b and c expired = %+v, want over budget (its rejection at 65 counts until 125)", decision)
	}
}

// The 429 of a slot rejection decided after a window rollover is the first
// refusal of the new window, so it is logged at Info.
func TestSlotRejectionAfterRolloverIsLoggedAtInfoOncePerWindow(t *testing.T) {
	start := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := start
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	inner := newMemoryCredentialStoreAt(t, start.Add(-time.Hour), memory.NewAuditStore())
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	store := &heldLookupStore{CredentialStore: inner, release: make(chan struct{}), held: HashToken(bad)}
	var logs bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewJSONHandler(lockedWriter{&logs, &logMu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{Now: now, Limiter: NewMemoryLimiter(time.Minute, 100, 3), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	const address = "192.0.2.71:4000"
	for i := 0; i < 3; i++ {
		callFrom(handler, address, "Bearer junk")
	}
	done := make(chan int)
	go func() { done <- callFrom(handler, address, "Bearer "+bad).Code }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if parked, _, _ := store.counts(); parked == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot lookup never started")
		}
		time.Sleep(time.Millisecond)
	}
	clockMu.Lock()
	clock = start.Add(2 * time.Minute)
	clockMu.Unlock()
	logMu.Lock()
	logs.Reset()
	logMu.Unlock()
	close(store.release)
	if code := <-done; code != http.StatusTooManyRequests {
		t.Fatalf("slot rejection after rollover = %d, want 429", code)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if n := strings.Count(logs.String(), `"level":"INFO","msg":"ACR authentication attempt refused","reason":"failure_budget"`); n != 1 {
		t.Fatalf("Info failure_budget refusal lines for the first refusal of the new window = %d, want 1:\n%s", n, logs.String())
	}
}

// An invalid web assertion decided after a window rollover is counted in the
// window where it was decided.
func TestWebAssertionFailureDecidedAfterRolloverIsCountedInTheNewWindow(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	// The authenticator reads the clock at each request start and at each
	// decision: the web-assertion request starts at t0 and is decided after
	// the window rolled over.
	var clockMu sync.Mutex
	times := []time.Time{t0, t0, t0, t0.Add(2 * time.Minute)}
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		next := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return next
	}
	authenticator, err := NewAuthenticator(newMemoryCredentialStore(t), memory.NewAuditStore(), AuthenticatorOptions{Now: now, Limiter: NewMemoryLimiter(time.Minute, 100, 3), WebAssertions: verifier})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	handler := authenticator.MiddlewareFor(true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	const address = "192.0.2.72:4000"
	callFrom(handler, address, "Bearer junk")
	callFrom(handler, address, "Bearer junk")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = address
	request.Header.Set(WebAssertionHeader, "not-a-web-assertion")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid web assertion = %d, want 401", response.Code)
	}
	var codes []int
	for i := 0; i < 3; i++ {
		codes = append(codes, callFrom(handler, address, "Bearer junk").Code)
	}
	if codes[0] != http.StatusUnauthorized || codes[1] != http.StatusUnauthorized || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("failures in the new window after the web-assertion failure = %v, want 401, 401, 429 (the web-assertion failure counted there)", codes)
	}
}

// A replayed web assertion decided after a window rollover is counted in the
// window where it was decided.
func TestWebAssertionReplayDecidedAfterRolloverIsCountedInTheNewWindow(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	times := []time.Time{t0, t0, t0, t0, t0.Add(2 * time.Minute)}
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		next := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return next
	}
	authenticator, err := NewAuthenticator(newMemoryCredentialStore(t), memory.NewAuditStore(), AuthenticatorOptions{Now: now, Limiter: NewMemoryLimiter(time.Minute, 100, 3), WebAssertions: verifier})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	handler := authenticator.MiddlewareFor(true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	const address = "192.0.2.73:4000"
	callFrom(handler, address, "Bearer junk")
	callFrom(handler, address, "Bearer junk")
	body := []byte(`{}`)
	assertion := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
		request.RemoteAddr = address
		return request
	}
	first := assertion()
	signed := signTestWebAssertion(t, private, "current", webAssertionClaims(t0, first, body))
	first.Header.Set(WebAssertionHeader, signed)
	if response := httptest.NewRecorder(); func() int { handler.ServeHTTP(response, first); return response.Code }() != http.StatusNoContent {
		t.Fatalf("first use of the web assertion = %d, want 204", response.Code)
	}
	replay := assertion()
	replay.Header.Set(WebAssertionHeader, signed)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, replay)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("replayed web assertion = %d, want 429", response.Code)
	}
	var codes []int
	for i := 0; i < 3; i++ {
		codes = append(codes, callFrom(handler, address, "Bearer junk").Code)
	}
	if codes[0] != http.StatusUnauthorized || codes[1] != http.StatusUnauthorized || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("failures in the new window after the replay = %v, want 401, 401, 429 (the replay counted there)", codes)
	}
}
