package auth

import (
	"testing"
	"time"
)

func TestMemoryLimiterAttemptAndFailureWindows(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	limiter := NewMemoryLimiter(time.Minute, 2, 2)
	if !limiter.AllowAttempt("ip", now) {
		t.Fatal("first attempt was not admitted")
	}
	if !limiter.AllowAttempt("ip", now) {
		t.Fatal("second attempt was not admitted")
	}
	if limiter.AllowAttempt("ip", now) {
		t.Fatal("attempt ceiling was not enforced")
	}
	if limiter.FailureBlocked("ip", now) {
		t.Fatal("failure bucket began blocked")
	}
	limiter.RecordFailure("ip", now)
	limiter.RecordFailure("ip", now)
	if !limiter.FailureBlocked("ip", now) {
		t.Fatal("failure ceiling was not enforced")
	}
	if retryAfter := limiter.RetryAfter("ip", now); retryAfter != time.Minute {
		t.Fatalf("retry after = %s", retryAfter)
	}
	later := now.Add(time.Minute)
	if !limiter.AllowAttempt("ip", later) || limiter.FailureBlocked("ip", later) {
		t.Fatal("fixed window did not reset")
	}
}

func TestMemoryLimiterBoundsTrackedKeysAndReclaimsExpiredWindows(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	limiter := NewBoundedMemoryLimiter(MemoryLimiterOptions{Window: time.Minute, AttemptLimit: 2, FailureLimit: 2, MaxTrackedKeys: 1})

	if !limiter.AllowAttempt("first", now) {
		t.Fatal("first key was not admitted")
	}
	if limiter.AllowAttempt("second", now) {
		t.Fatal("second key bypassed the tracking cap")
	}
	if !limiter.AllowAttempt("second", now.Add(time.Minute)) {
		t.Fatal("expired key was not reclaimed")
	}
}
