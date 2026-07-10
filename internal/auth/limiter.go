package auth

import (
	"sync"
	"time"
)

type AttemptLimiter interface {
	AllowAttempt(key string, now time.Time) bool
	FailureBlocked(key string, now time.Time) bool
	RecordFailure(key string, now time.Time)
}

type NoopLimiter struct{}

func (NoopLimiter) AllowAttempt(string, time.Time) bool   { return true }
func (NoopLimiter) FailureBlocked(string, time.Time) bool { return false }
func (NoopLimiter) RecordFailure(string, time.Time)       {}

type fixedWindow struct {
	Started time.Time
	Count   int
}

type MemoryLimiter struct {
	mu           sync.Mutex
	Window       time.Duration
	AttemptLimit int
	FailureLimit int
	attempts     map[string]fixedWindow
	failures     map[string]fixedWindow
}

func NewMemoryLimiter(window time.Duration, attemptLimit, failureLimit int) *MemoryLimiter {
	return &MemoryLimiter{
		Window: window, AttemptLimit: attemptLimit, FailureLimit: failureLimit,
		attempts: make(map[string]fixedWindow), failures: make(map[string]fixedWindow),
	}
}

func (l *MemoryLimiter) AllowAttempt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.current(l.attempts[key], now)
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
	window := l.current(l.failures[key], now)
	l.failures[key] = window
	return l.FailureLimit > 0 && window.Count >= l.FailureLimit
}

func (l *MemoryLimiter) RecordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.current(l.failures[key], now)
	window.Count++
	l.failures[key] = window
}

func (l *MemoryLimiter) current(window fixedWindow, now time.Time) fixedWindow {
	if l.Window <= 0 || window.Started.IsZero() || !now.Before(window.Started.Add(l.Window)) {
		return fixedWindow{Started: now}
	}
	return window
}
