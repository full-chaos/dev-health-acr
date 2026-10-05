package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// lifecycleFixture is one acr-api authenticator (failure limit 3, one-minute
// window) on a settable clock, whose store parks the lookups of one bearer.
type lifecycleFixture struct {
	t       *testing.T
	handler http.Handler
	store   *heldLookupStore
	valid   string
	clockMu sync.Mutex
	clock   time.Time
}

const lifecycleAddress = "192.0.2.80:4000"

func newLifecycleFixture(t *testing.T, held func(valid string) string) *lifecycleFixture {
	t.Helper()
	f := &lifecycleFixture{t: t, clock: time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)}
	inner := newMemoryCredentialStoreAt(t, f.clock.Add(-time.Hour), memory.NewAuditStore())
	f.valid = issueForMiddleware(t, inner, nil, f.clock.Add(-time.Hour), []string{ScopeContextRead}, []string{"owner/repo"}, nil).Token
	f.store = &heldLookupStore{CredentialStore: inner, release: make(chan struct{}), held: HashToken(held(f.valid))}
	authenticator, err := NewAuthenticator(f.store, memory.NewAuditStore(), AuthenticatorOptions{Now: f.now, Limiter: NewMemoryLimiter(time.Minute, 100, 3)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	f.handler = authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	return f
}

func (f *lifecycleFixture) now() time.Time {
	f.clockMu.Lock()
	defer f.clockMu.Unlock()
	return f.clock
}

func (f *lifecycleFixture) advance(d time.Duration) {
	f.clockMu.Lock()
	defer f.clockMu.Unlock()
	f.clock = f.clock.Add(d)
}

func (f *lifecycleFixture) call(bearer string) int {
	return callFrom(f.handler, lifecycleAddress, "Bearer "+bearer).Code
}

// failures sends malformed bearers until the gate refuses one and returns
// how many more failures the window accepted (0 when already over budget).
func (f *lifecycleFixture) remainingBudget() int {
	for n := 0; n <= 3; n++ {
		if f.call("junk") == http.StatusTooManyRequests {
			return n
		}
	}
	f.t.Fatal("the failure budget never refused")
	return 0
}

// start sends the held bearer in the background, with a context the test can
// cancel, and waits until its lookup is parked.
func (f *lifecycleFixture) start(bearer string) (result chan int, abandon context.CancelFunc) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result = make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		request.RemoteAddr = lifecycleAddress
		request.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		result <- response.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if parked, _, _ := f.store.counts(); parked == 1 {
			return result, cancel
		}
		if time.Now().After(deadline) {
			f.t.Fatal("lookup never parked")
		}
		time.Sleep(time.Millisecond)
	}
}

const lifecycleGuess = TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func guessIsHeld(string) string       { return lifecycleGuess }
func validIsHeld(valid string) string { return valid }

func TestAttemptAdmittedUnderBudgetAndVerifiedIsServedAndNotCounted(t *testing.T) {
	f := newLifecycleFixture(t, validIsHeld)
	result, _ := f.start(f.valid)
	close(f.store.release)
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified = %d, want 200", code)
	}
	if n := f.remainingBudget(); n != 3 {
		t.Fatalf("failures accepted after a verified attempt = %d, want 3", n)
	}
}

func TestAttemptAdmittedUnderBudgetAndRejectedIsCountedAnd401(t *testing.T) {
	f := newLifecycleFixture(t, guessIsHeld)
	result, _ := f.start(lifecycleGuess)
	close(f.store.release)
	if code := <-result; code != http.StatusUnauthorized {
		t.Fatalf("rejected = %d, want 401", code)
	}
	if n := f.remainingBudget(); n != 2 {
		t.Fatalf("failures accepted after a rejected attempt = %d, want 2", n)
	}
}

func TestAttemptAdmittedUnderBudgetAndAbandonedIsNotCountedAndReleased(t *testing.T) {
	f := newLifecycleFixture(t, guessIsHeld)
	result, abandon := f.start(lifecycleGuess)
	abandon()
	if code := <-result; code != http.StatusServiceUnavailable {
		t.Fatalf("abandoned = %d, want 503", code)
	}
	if n := f.remainingBudget(); n != 3 {
		t.Fatalf("failures accepted after an abandoned attempt = %d, want 3", n)
	}
}

func TestAttemptAdmittedUnderBudgetAndRejectedAfterRolloverIsCountedInTheNewWindow(t *testing.T) {
	f := newLifecycleFixture(t, guessIsHeld)
	f.call("junk")
	f.call("junk")
	result, _ := f.start(lifecycleGuess)
	f.advance(2 * time.Minute)
	close(f.store.release)
	if code := <-result; code != http.StatusUnauthorized {
		t.Fatalf("rejected after rollover = %d, want 401 (admitted under budget, new window under budget)", code)
	}
	if n := f.remainingBudget(); n != 2 {
		t.Fatalf("failures accepted in the new window = %d, want 2 (the rejection counted there)", n)
	}
}

func TestAttemptAdmittedUnderBudgetAndVerifiedAfterRolloverIsServed(t *testing.T) {
	f := newLifecycleFixture(t, validIsHeld)
	result, _ := f.start(f.valid)
	f.advance(2 * time.Minute)
	close(f.store.release)
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified after rollover = %d, want 200", code)
	}
}

func TestAttemptInTheOverBudgetSlotAndVerifiedIsServedAndNotCounted(t *testing.T) {
	f := newLifecycleFixture(t, validIsHeld)
	if n := f.remainingBudget(); n != 3 {
		t.Fatalf("setup: %d", n)
	}
	result, _ := f.start(f.valid)
	close(f.store.release)
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified in the slot = %d, want 200", code)
	}
	if code := f.call(lifecycleGuess); code != http.StatusTooManyRequests {
		t.Fatalf("guess after the slot is free = %d, want 429 (still over budget)", code)
	}
}

func TestAttemptInTheOverBudgetSlotAndRejectedIsCountedAnd429(t *testing.T) {
	f := newLifecycleFixture(t, guessIsHeld)
	f.remainingBudget()
	result, _ := f.start(lifecycleGuess)
	close(f.store.release)
	if code := <-result; code != http.StatusTooManyRequests {
		t.Fatalf("rejected in the slot = %d, want 429", code)
	}
	if code := f.call(f.valid); code != http.StatusOK {
		t.Fatalf("valid bearer after the slot is free = %d, want 200", code)
	}
}

func TestAttemptInTheOverBudgetSlotAndAbandonedIsNotCountedAndFreesTheSlot(t *testing.T) {
	f := newLifecycleFixture(t, guessIsHeld)
	f.remainingBudget()
	result, abandon := f.start(lifecycleGuess)
	if code := f.call(f.valid); code != http.StatusTooManyRequests {
		t.Fatalf("valid bearer while the slot is held = %d, want 429", code)
	}
	abandon()
	if code := <-result; code != http.StatusServiceUnavailable {
		t.Fatalf("abandoned in the slot = %d, want 503", code)
	}
	if code := f.call(f.valid); code != http.StatusOK {
		t.Fatalf("valid bearer after the slot was abandoned = %d, want 200", code)
	}
}

func TestAttemptInTheOverBudgetSlotAndVerifiedAfterRolloverIsServed(t *testing.T) {
	f := newLifecycleFixture(t, validIsHeld)
	f.remainingBudget()
	result, _ := f.start(f.valid)
	f.advance(2 * time.Minute)
	close(f.store.release)
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified in the slot after rollover = %d, want 200", code)
	}
}

func TestAttemptAbandonedByTimeoutIsNotCountedAndReleased(t *testing.T) {
	f := newLifecycleFixture(t, guessIsHeld)
	f.remainingBudget()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	request.RemoteAddr = lifecycleAddress
	request.Header.Set("Authorization", "Bearer "+lifecycleGuess)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("lookup timed out in the slot = %d, want 503", response.Code)
	}
	if code := f.call(f.valid); code != http.StatusOK {
		t.Fatalf("valid bearer after the timed-out slot attempt = %d, want 200 (slot released, nothing counted)", code)
	}
}
