package auth

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// A reservation held PAST the failure window's expiry must still bound new
// addresses through the in-flight table's own key cap. The failure map's entry
// for the held address ages out with the window, so a test that rejects the
// second address through the failure map (the two tests above) passes even
// with the separate in-flight key check removed (CHAOS-7196 item 1).
func TestReservationHeldPastWindowExpiryStillBoundsNewAddresses(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 3, MaxTrackedKeys: 1, MaxInFlight: 4})
	release, decision := limiter.BeginAttemptDecision("a", now)
	if !decision.Admitted() {
		t.Fatalf("first address refused: %+v", decision)
	}
	later := now.Add(3 * time.Minute) // "a"'s failure-window entry has expired; its reservation has not been released
	for _, address := range []string{"b", "c", "d"} {
		if _, decision := limiter.BeginAttemptDecision(address, later); decision.Refusal != RefusalTrackedKeys {
			t.Fatalf("address %s admitted or mislabelled while %q holds the only in-flight key past window expiry: %+v", address, "a", decision)
		}
	}
	release()
	if _, decision := limiter.BeginAttemptDecision("b", later); !decision.Admitted() {
		t.Fatalf("address refused after the reservation was released: %+v", decision)
	}
}

func TestBeginAttemptDecisionNamesTheBoundThatRefused(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)

	budget := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 2, MaxTrackedKeys: 8})
	budget.RecordFailure("a", now)
	budget.RecordFailure("a", now)
	if _, d := budget.BeginAttemptDecision("a", now); d.Refusal != RefusalFailureBudget {
		t.Fatalf("budget exhausted: %+v", d)
	}

	inflight := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 9, MaxTrackedKeys: 8, MaxInFlight: 2})
	for i := 1; i <= 2; i++ {
		if _, d := inflight.BeginAttemptDecision("a", now); !d.Admitted() || d.InFlight != i {
			t.Fatalf("admission %d: %+v, want admitted with in_flight=%d", i, d, i)
		}
	}
	if _, d := inflight.BeginAttemptDecision("a", now); d.Refusal != RefusalInFlight || d.InFlight != 2 {
		t.Fatalf("per-address in-flight cap: %+v, want in_flight refusal with in_flight=2", d)
	}

	failureTable := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 9, MaxTrackedKeys: 1})
	failureTable.RecordFailure("a", now)
	if _, d := failureTable.BeginAttemptDecision("b", now); d.Refusal != RefusalTrackedKeys {
		t.Fatalf("failure table full: %+v, want tracked_keys (not failure_budget)", d)
	}
}

// A limiter that cannot say which bound refused is reported as unspecified,
// never guessed: FailureBlocked is also true for an address the failure table
// cannot track, so a full table must not read as an exhausted budget.
type plainLimiter struct{ blocked, budget bool }

func (plainLimiter) AllowAttempt(string, time.Time) bool     { return true }
func (p plainLimiter) FailureBlocked(string, time.Time) bool { return p.budget }
func (p plainLimiter) BeginAttempt(string, time.Time) (func(), bool) {
	return func() {}, !p.blocked
}
func (plainLimiter) RecordFailure(string, time.Time)            {}
func (plainLimiter) RetryAfter(string, time.Time) time.Duration { return 0 }

func TestBeginAttemptDecisionNeverGuessesForLimitersWithoutReasons(t *testing.T) {
	now := time.Now()
	if _, d := BeginAttemptDecision(plainLimiter{}, "a", now); !d.Admitted() {
		t.Fatalf("%+v", d)
	}
	// FailureBlocked true (as for a full failure table) must NOT read as failure_budget.
	for _, l := range []plainLimiter{{blocked: true, budget: true}, {blocked: true}} {
		if _, d := BeginAttemptDecision(l, "a", now); d.Refusal != RefusalUnspecified || !d.FirstRefusal {
			t.Fatalf("%+v -> %+v, want unspecified", l, d)
		}
	}
}

// The first failure_budget refusal of an address in a window is reported as
// first; the retries of the locked-out address are not; a new window is first
// again. Other bounds are always first.
func TestFailureBudgetRefusalIsFirstOncePerWindow(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 1, MaxTrackedKeys: 8})
	limiter.RecordFailure("a", now)
	for i := 0; i < 5; i++ {
		_, d := limiter.BeginAttemptDecision("a", now.Add(time.Duration(i)*time.Second))
		if d.Refusal != RefusalFailureBudget || d.FirstRefusal != (i == 0) {
			t.Fatalf("refusal %d = %+v, want failure_budget with FirstRefusal=%v", i, d, i == 0)
		}
	}
	// A different address has its own flag.
	limiter.RecordFailure("b", now)
	if _, d := limiter.BeginAttemptDecision("b", now); !d.FirstRefusal {
		t.Fatalf("second address's first refusal = %+v", d)
	}
	// New window: the address is admitted again, locks out again, and its first refusal is first.
	later := now.Add(2 * time.Minute)
	if _, d := limiter.BeginAttemptDecision("a", later); !d.Admitted() {
		t.Fatalf("after the window: %+v", d)
	}
	limiter.RecordFailure("a", later)
	if _, d := limiter.BeginAttemptDecision("a", later); d.Refusal != RefusalFailureBudget || !d.FirstRefusal {
		t.Fatalf("first refusal of the new window = %+v", d)
	}
	// in_flight is never suppressed.
	inflight := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 9, MaxTrackedKeys: 8, MaxInFlight: 1})
	inflight.BeginAttemptDecision("a", now)
	for i := 0; i < 3; i++ {
		if _, d := inflight.BeginAttemptDecision("a", now); d.Refusal != RefusalInFlight || !d.FirstRefusal {
			t.Fatalf("in_flight refusal %d = %+v", i, d)
		}
	}
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuf) lines() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(s.buf.Bytes()), []byte("\n")) {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func refusalLines(b *syncBuf) []map[string]any {
	var out []map[string]any
	for _, m := range b.lines() {
		if m["msg"] == "ACR authentication attempt refused" {
			out = append(out, m)
		}
	}
	return out
}

// The 429 is one response for three different bounds; the Info line tells them
// apart, with the in-flight count (CHAOS-7196 item 2).
func TestAuthenticatorLogsWhichBoundRefusedTheAttempt(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	build := func(t *testing.T, limiter AttemptLimiter, store *gatedCredentialStore) (http.Handler, *syncBuf) {
		logs := &syncBuf{}
		var credentialStore = newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore())
		options := AuthenticatorOptions{Now: func() time.Time { return now }, Limiter: limiter, Logger: slog.New(slog.NewJSONHandler(logs, nil))}
		var a *Authenticator
		var err error
		if store != nil {
			a, err = NewAuthenticator(store, memory.NewAuditStore(), options)
		} else {
			a, err = NewAuthenticator(credentialStore, memory.NewAuditStore(), options)
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close() })
		return a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })), logs
	}
	from := func(handler http.Handler, address string) int {
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		request.RemoteAddr = address + ":4000"
		request.Header.Set("Authorization", "Bearer "+bad)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}

	t.Run("failure_budget", func(t *testing.T) {
		handler, logs := build(t, NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 1, MaxTrackedKeys: 8}), nil)
		from(handler, "192.0.2.10")
		if code := from(handler, "192.0.2.10"); code != http.StatusTooManyRequests {
			t.Fatalf("status %d", code)
		}
		got := refusalLines(logs)
		if len(got) != 1 || got[0]["reason"] != "failure_budget" || got[0]["remote_ip"] != "192.0.2.10" || got[0]["level"] != "INFO" {
			t.Fatalf("lines = %v", got)
		}
	})
	t.Run("tracked_keys", func(t *testing.T) {
		handler, logs := build(t, NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 5, MaxTrackedKeys: 1}), nil)
		from(handler, "192.0.2.10")
		if code := from(handler, "192.0.2.11"); code != http.StatusTooManyRequests {
			t.Fatalf("status %d", code)
		}
		if got := refusalLines(logs); len(got) != 1 || got[0]["reason"] != "tracked_keys" {
			t.Fatalf("lines = %v", got)
		}
	})
	t.Run("in_flight", func(t *testing.T) {
		store := &gatedCredentialStore{CredentialStore: newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore()), release: make(chan struct{})}
		handler, logs := build(t, NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 5, MaxTrackedKeys: 8, MaxInFlight: 1}), store)
		done := make(chan int, 1)
		go func() { done <- from(handler, "192.0.2.10") }()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
			store.mu.Lock()
			parked := store.arrived
			store.mu.Unlock()
			if parked == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("first attempt never reached the store")
			}
		}
		if code := from(handler, "192.0.2.10"); code != http.StatusTooManyRequests {
			t.Fatalf("status %d", code)
		}
		close(store.release)
		<-done
		got := refusalLines(logs)
		if len(got) != 1 || got[0]["reason"] != "in_flight" || got[0]["in_flight"] != float64(1) {
			t.Fatalf("lines = %v", got)
		}
	})
}

// 25 refusals of one locked-out address in one window: one Info line, 24
// Debug; a new window logs Info again (P2-4: the retry rate must not set the
// Info volume).
func TestRefusalLineIsInfoOncePerWindowThenDebug(t *testing.T) {
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)
	bad := TokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	logs := &syncBuf{}
	clock := now
	store := newMemoryCredentialStoreAt(t, now.Add(-time.Hour), memory.NewAuditStore())
	a, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{
		Now: func() time.Time { return clock }, Limiter: NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, FailureLimit: 1, MaxTrackedKeys: 8}),
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func() int {
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		request.RemoteAddr = "192.0.2.10:4000"
		request.Header.Set("Authorization", "Bearer "+bad)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	call() // the one failure that uses the budget
	for i := 0; i < 25; i++ {
		if code := call(); code != http.StatusTooManyRequests {
			t.Fatalf("refusal %d = %d", i, code)
		}
	}
	count := func() (info, debug int) {
		for _, m := range refusalLines(logs) {
			switch m["level"] {
			case "INFO":
				info++
			case "DEBUG":
				debug++
			}
		}
		return
	}
	if info, debug := count(); info != 1 || debug != 24 {
		t.Fatalf("25 refusals in one window: info=%d debug=%d, want 1 and 24", info, debug)
	}
	clock = now.Add(2 * time.Minute)
	call() // uses the new window's budget
	call() // refused
	if info, debug := count(); info != 2 || debug != 24 {
		t.Fatalf("after a new window: info=%d debug=%d, want 2 and 24", info, debug)
	}
}
