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
)

// AttemptDecision is what a limiter decided for one attempt: the refusal (if
// any) and how many attempts from the address were undecided at that moment
// (including this one when admitted).
type AttemptDecision struct {
	Refusal  AttemptRefusal
	InFlight int
}

// Admitted reports whether the attempt was admitted.
func (d AttemptDecision) Admitted() bool { return d.Refusal == RefusalNone }

// DecisionLimiter is an AttemptLimiter that reports WHICH bound refused.
type DecisionLimiter interface {
	AttemptLimiter
	BeginAttemptDecision(key string, now time.Time) (release func(), decision AttemptDecision)
}

// BeginAttemptDecision admits one attempt on any limiter, naming the refusal
// when the limiter can (DecisionLimiter) and RefusalUnspecified when it
// cannot, except that a refusal with the failure budget reached is always
// named as such.
func BeginAttemptDecision(limiter AttemptLimiter, key string, now time.Time) (func(), AttemptDecision) {
	if decider, ok := limiter.(DecisionLimiter); ok {
		return decider.BeginAttemptDecision(key, now)
	}
	release, admitted := limiter.BeginAttempt(key, now)
	if admitted {
		return release, AttemptDecision{}
	}
	if limiter.FailureBlocked(key, now) {
		return nil, AttemptDecision{Refusal: RefusalFailureBudget}
	}
	return nil, AttemptDecision{Refusal: RefusalUnspecified}
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
	l.mu.Lock()
	defer l.mu.Unlock()
	window, tracked := l.window(l.failures, key, now)
	if !tracked {
		return nil, AttemptDecision{Refusal: RefusalTrackedKeys, InFlight: l.inflight[key]}
	}
	l.failures[key] = window
	if l.FailureLimit > 0 && window.Count >= l.FailureLimit {
		return nil, AttemptDecision{Refusal: RefusalFailureBudget, InFlight: l.inflight[key]}
	}
	// Undecided attempts are bounded per address, and the tracked addresses
	// are bounded like every other limiter map. Below the per-address bound a
	// valid request is never refused, whatever the failure count.
	current, tracking := l.inflight[key]
	if current >= l.maxInflight {
		return nil, AttemptDecision{Refusal: RefusalInFlight, InFlight: current}
	}
	if !tracking && len(l.inflight) >= l.maxKeys {
		return nil, AttemptDecision{Refusal: RefusalTrackedKeys, InFlight: current}
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
	}, AttemptDecision{InFlight: current + 1}
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
