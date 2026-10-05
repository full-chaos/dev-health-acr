package auth

import (
	"sync"
	"time"
)

type AttemptLimiter interface {
	AllowAttempt(key string, now time.Time) bool
	FailureBlocked(key string, now time.Time) bool
	// BeginAttempt atomically admits one authentication attempt: it refuses
	// when the address has reached its failure limit, or when too many
	// attempts from it are still undecided (so a burst of concurrent guesses
	// cannot all pass the check before the first failure is recorded; the
	// overshoot is bounded by the in-flight cap). The returned release must be
	// called once the attempt is decided; it spends nothing, so a successful
	// attempt leaves the failure budget untouched.
	BeginAttempt(key string, now time.Time) (release func(), ok bool)
	RecordFailure(key string, now time.Time)
	RetryAfter(key string, now time.Time) time.Duration
}

// AttemptRefusal names which bound refused an attempt. The empty value is an
// admitted attempt.
type AttemptRefusal string

const (
	// RefusalNone: the attempt was admitted.
	RefusalNone AttemptRefusal = ""
	// RefusalFailureBudget: the address reached its failed-authentication limit.
	RefusalFailureBudget AttemptRefusal = "failure_budget"
	// RefusalInFlight: too many attempts from the address are still undecided.
	RefusalInFlight AttemptRefusal = "in_flight"
	// RefusalTrackedKeys: the tracked-address table (failure window or
	// in-flight table) is full and the address is not already in it.
	RefusalTrackedKeys AttemptRefusal = "tracked_keys"
	// RefusalUnspecified: a limiter that cannot say which bound refused.
	RefusalUnspecified AttemptRefusal = "unspecified"
	// RefusalVerificationSlot: the address is over its failure limit and its
	// one over-budget verification slot is held by another attempt.
	RefusalVerificationSlot AttemptRefusal = "verification_slot"
)

// OverBudgetVerificationSlots is how many undecided attempts an address that
// has reached its failure limit may have at once, per process, counting the
// attempts it started while still under the limit. Verifying is the only way
// to tell a valid credential from a guess, so an over-budget address keeps
// this one slot: a credential that verifies in it is served, and the
// credential-store work of the address stays bounded by one lookup at a time.
const OverBudgetVerificationSlots = 1

// AttemptDecision is what a limiter decided for one attempt: the refusal (if
// any) and how many attempts from the address were undecided at that moment
// (including this one when admitted).
type AttemptDecision struct {
	Refusal  AttemptRefusal
	InFlight int
	// FirstRefusal is true for every refusal except a repeat failure_budget
	// refusal of the same address in the same window: once an address is
	// locked out every retry is refused, and a caller that logs each refusal
	// at full level would let the retry rate set the log volume. The flag lives
	// on the limiter's existing per-address window entry (no second map).
	FirstRefusal bool
	// OverBudget is set on an attempt admitted by BeginVerification while
	// the address is at or over its failure limit (it holds the slot); its
	// rejection is answered as a refusal even if the window rolls over first.
	OverBudget bool
}

// Admitted reports whether the attempt was admitted.
func (d AttemptDecision) Admitted() bool { return d.Refusal == RefusalNone }

// DecisionLimiter is an AttemptLimiter that reports WHICH bound refused.
type DecisionLimiter interface {
	AttemptLimiter
	BeginAttemptDecision(key string, now time.Time) (release func(), decision AttemptDecision)
}

// BeginAttemptDecision admits one attempt on any limiter, naming the refusal
// when the limiter can (DecisionLimiter). A limiter that cannot say is
// reported as RefusalUnspecified: FailureBlocked is also true for an address
// the failure table cannot track, so it is never used to guess the bound.
func BeginAttemptDecision(limiter AttemptLimiter, key string, now time.Time) (func(), AttemptDecision) {
	if decider, ok := limiter.(DecisionLimiter); ok {
		return decider.BeginAttemptDecision(key, now)
	}
	release, admitted := limiter.BeginAttempt(key, now)
	if admitted {
		return release, AttemptDecision{}
	}
	return nil, AttemptDecision{Refusal: RefusalUnspecified, FirstRefusal: true}
}

// VerificationLimiter admits attempts that present a well-formed credential
// without refusing them on the failure budget alone: an over-budget address
// is admitted to OverBudgetVerificationSlots concurrent verifications.
type VerificationLimiter interface {
	AttemptLimiter
	BeginVerification(key string, now time.Time) (release func(), decision AttemptDecision)
	// RecordRejection records one failed authentication and reports whether
	// the address was already at or over its failure limit, and whether this
	// is the first such report for the address in the window.
	RecordRejection(key string, now time.Time) (overBudget, firstRefusal bool)
}

// BeginVerificationDecision admits an attempt that presents a well-formed
// credential. A limiter that is not a VerificationLimiter decides it as any
// other attempt.
func BeginVerificationDecision(limiter AttemptLimiter, key string, now time.Time) (func(), AttemptDecision) {
	if verifier, ok := limiter.(VerificationLimiter); ok {
		return verifier.BeginVerification(key, now)
	}
	return BeginAttemptDecision(limiter, key, now)
}

// RecordRejection records a rejected credential on any limiter. A limiter
// that is not a VerificationLimiter never reports over budget: it refused
// over-budget attempts before verification.
func RecordRejection(limiter AttemptLimiter, key string, now time.Time) (overBudget, firstRefusal bool) {
	if verifier, ok := limiter.(VerificationLimiter); ok {
		return verifier.RecordRejection(key, now)
	}
	limiter.RecordFailure(key, now)
	return false, false
}

type NoopLimiter struct{}

func (NoopLimiter) AllowAttempt(string, time.Time) bool   { return true }
func (NoopLimiter) FailureBlocked(string, time.Time) bool { return false }
func (NoopLimiter) BeginAttempt(string, time.Time) (func(), bool) {
	return func() {}, true
}
func (NoopLimiter) RecordFailure(string, time.Time)            {}
func (NoopLimiter) RetryAfter(string, time.Time) time.Duration { return 0 }

type fixedWindow struct {
	Started time.Time
	Count   int
	// RefusalLogged: a failure_budget refusal has been reported for this
	// address in this window (failures map only; a new window starts false).
	RefusalLogged bool
}

type MemoryLimiter struct {
	mu           sync.Mutex
	Window       time.Duration
	AttemptLimit int
	FailureLimit int
	maxKeys      int
	maxInflight  int
	attempts     map[string]fixedWindow
	failures     map[string]fixedWindow
	inflight     map[string]int
}

type MemoryLimiterOptions struct {
	Window         time.Duration
	AttemptLimit   int
	FailureLimit   int
	MaxTrackedKeys int
	// MaxInFlight bounds concurrent undecided attempts per address (default
	// DefaultMaxInFlight). Concurrent guesses can overshoot the failure limit
	// by at most this many; valid requests are never refused below it.
	MaxInFlight int
}

const DefaultMaxInFlight = 64

func NewMemoryLimiter(window time.Duration, attemptLimit, failureLimit int) *MemoryLimiter {
	return NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: window, AttemptLimit: attemptLimit, FailureLimit: failureLimit, MaxTrackedKeys: 4096})
}

func NewBoundedMemoryLimiter(options MemoryLimiterOptions) *MemoryLimiter {
	if options.MaxTrackedKeys < 1 {
		options.MaxTrackedKeys = 1
	}
	if options.MaxInFlight < 1 {
		options.MaxInFlight = DefaultMaxInFlight
	}
	return &MemoryLimiter{
		maxInflight: options.MaxInFlight,
		Window:      options.Window, AttemptLimit: options.AttemptLimit, FailureLimit: options.FailureLimit,
		maxKeys:  options.MaxTrackedKeys,
		attempts: make(map[string]fixedWindow), failures: make(map[string]fixedWindow), inflight: make(map[string]int),
	}
}

func (l *MemoryLimiter) AllowAttempt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, tracked := l.window(l.attempts, key, now)
	if !tracked {
		return false
	}
	if l.AttemptLimit > 0 && window.Count >= l.AttemptLimit {
		l.attempts[key] = window
		return false
	}
	window.Count++
	l.attempts[key] = window
	return true
}

func (l *MemoryLimiter) FailureBlocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, tracked := l.window(l.failures, key, now)
	if !tracked {
		return true
	}
	l.failures[key] = window
	return l.FailureLimit > 0 && window.Count >= l.FailureLimit
}

func (l *MemoryLimiter) BeginAttempt(key string, now time.Time) (func(), bool) {
	release, decision := l.BeginAttemptDecision(key, now)
	return release, decision.Admitted()
}

// BeginAttemptDecision is BeginAttempt that also says which bound refused.
func (l *MemoryLimiter) BeginAttemptDecision(key string, now time.Time) (func(), AttemptDecision) {
	return l.begin(key, now, false)
}

// BeginVerification admits an attempt that presents a well-formed
// credential. Below the failure limit it is BeginAttemptDecision. At or over
// it the attempt is admitted only while the address has fewer than
// OverBudgetVerificationSlots undecided attempts (including those admitted
// under the limit), so an over-budget address never verifies more than that
// many credentials at once; the tracked-key bound applies as for every
// attempt.
func (l *MemoryLimiter) BeginVerification(key string, now time.Time) (func(), AttemptDecision) {
	return l.begin(key, now, true)
}

func (l *MemoryLimiter) begin(key string, now time.Time, verification bool) (func(), AttemptDecision) {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, tracked := l.window(l.failures, key, now)
	if !tracked {
		return nil, AttemptDecision{Refusal: RefusalTrackedKeys, InFlight: l.inflight[key], FirstRefusal: true}
	}
	l.failures[key] = window
	overBudget := l.FailureLimit > 0 && window.Count >= l.FailureLimit
	if overBudget && !verification {
		first := !window.RefusalLogged
		window.RefusalLogged = true
		l.failures[key] = window
		return nil, AttemptDecision{Refusal: RefusalFailureBudget, InFlight: l.inflight[key], FirstRefusal: first}
	}
	// Undecided attempts are bounded per address, and the tracked addresses
	// are bounded like every other limiter map. Below the per-address bound a
	// valid request is never refused, whatever the failure count.
	current, tracking := l.inflight[key]
	if overBudget && current >= OverBudgetVerificationSlots {
		// A busy slot is refused at the retry rate of the address, so only
		// the first refusal of the window is reported as first.
		first := !window.RefusalLogged
		window.RefusalLogged = true
		l.failures[key] = window
		return nil, AttemptDecision{Refusal: RefusalVerificationSlot, InFlight: current, FirstRefusal: first}
	}
	if current >= l.maxInflight {
		return nil, AttemptDecision{Refusal: RefusalInFlight, InFlight: current, FirstRefusal: true}
	}
	if !tracking && len(l.inflight) >= l.maxKeys {
		return nil, AttemptDecision{Refusal: RefusalTrackedKeys, InFlight: current, FirstRefusal: true}
	}
	l.inflight[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.inflight[key] <= 1 {
				delete(l.inflight, key)
				return
			}
			l.inflight[key]--
		})
	}, AttemptDecision{InFlight: current + 1, OverBudget: overBudget}
}

// RecordRejection records one failed authentication of a verified-and-
// rejected credential in the window where it was decided, and reports whether
// the address was already at or over its failure limit there. The caller also
// answers it as a refusal when the attempt was admitted over the limit.
func (l *MemoryLimiter) RecordRejection(key string, now time.Time) (bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, tracked := l.window(l.failures, key, now)
	if !tracked {
		return true, true
	}
	overBudget := l.FailureLimit > 0 && window.Count >= l.FailureLimit
	first := false
	if overBudget {
		first = !window.RefusalLogged
		window.RefusalLogged = true
	}
	window.Count++
	l.failures[key] = window
	return overBudget, first
}

func (l *MemoryLimiter) RecordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, tracked := l.window(l.failures, key, now)
	if !tracked {
		return
	}
	window.Count++
	l.failures[key] = window
}

func (l *MemoryLimiter) RetryAfter(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	retryAfter := time.Duration(0)
	for _, windows := range []map[string]fixedWindow{l.attempts, l.failures} {
		window, ok := windows[key]
		if !ok {
			continue
		}
		remaining := window.Started.Add(l.Window).Sub(now)
		if remaining > retryAfter {
			retryAfter = remaining
		}
	}
	if retryAfter <= 0 && (len(l.attempts) >= l.maxKeys || len(l.failures) >= l.maxKeys) {
		return l.Window
	}
	return retryAfter
}

func (l *MemoryLimiter) current(window fixedWindow, now time.Time) fixedWindow {
	if l.Window <= 0 || window.Started.IsZero() || !now.Before(window.Started.Add(l.Window)) {
		return fixedWindow{Started: now}
	}
	return window
}

func (l *MemoryLimiter) window(windows map[string]fixedWindow, key string, now time.Time) (fixedWindow, bool) {
	for trackedKey, window := range windows {
		if !now.Before(window.Started.Add(l.Window)) {
			delete(windows, trackedKey)
		}
	}
	if window, ok := windows[key]; ok {
		return l.current(window, now), true
	}
	if len(windows) >= l.maxKeys {
		return fixedWindow{}, false
	}
	return fixedWindow{Started: now}, true
}
