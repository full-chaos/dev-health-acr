package auth

import (
	"sync"
	"time"
)

type AttemptLimiter interface {
	AllowAttempt(key string, now time.Time) bool
	FailureBlocked(key string, now time.Time) bool
	RecordFailure(key string, now time.Time)
	RetryAfter(key string, now time.Time) time.Duration
}

type NoopLimiter struct{}

func (NoopLimiter) AllowAttempt(string, time.Time) bool        { return true }
func (NoopLimiter) FailureBlocked(string, time.Time) bool      { return false }
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
	attempts     map[string]fixedWindow
	failures     map[string]fixedWindow
}

type MemoryLimiterOptions struct {
	Window         time.Duration
	AttemptLimit   int
	FailureLimit   int
	MaxTrackedKeys int
}

func NewMemoryLimiter(window time.Duration, attemptLimit, failureLimit int) *MemoryLimiter {
	return NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: window, AttemptLimit: attemptLimit, FailureLimit: failureLimit, MaxTrackedKeys: 4096})
}

func NewBoundedMemoryLimiter(options MemoryLimiterOptions) *MemoryLimiter {
	if options.MaxTrackedKeys < 1 {
		options.MaxTrackedKeys = 1
	}
	return &MemoryLimiter{
		Window: options.Window, AttemptLimit: options.AttemptLimit, FailureLimit: options.FailureLimit,
		maxKeys:  options.MaxTrackedKeys,
		attempts: make(map[string]fixedWindow), failures: make(map[string]fixedWindow),
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
