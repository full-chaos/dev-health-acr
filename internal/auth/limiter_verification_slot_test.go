package auth

import (
	"net/http"
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
		limiter.RecordRejection("a", now)
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
	if code := callFrom(handler, address, "Bearer junk").Code; code != http.StatusUnauthorized {
		t.Fatalf("malformed bearer in the new window = %d, want 401", code)
	}
	close(store.release)
	if code := <-done; code != http.StatusTooManyRequests {
		t.Fatalf("slot rejection decided after the window rolled over = %d, want 429", code)
	}
	if code := callFrom(handler, address, "Bearer junk").Code; code != http.StatusUnauthorized {
		t.Fatalf("third failure of the new window = %d, want 401", code)
	}
	if code := callFrom(handler, address, "Bearer junk").Code; code != http.StatusTooManyRequests {
		t.Fatalf("fourth failure of the new window = %d, want 429 (the slot rejection counted in the new window)", code)
	}
}
