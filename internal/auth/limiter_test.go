package auth

import (
	"testing"
	"time"
)

func TestMemoryLimiterAttemptAndFailureWindows(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	limiter := NewMemoryLimiter(time.Minute, 2, 2)
	if !limiter.AllowAttempt("ip", now) || !limiter.AllowAttempt("ip", now) || limiter.AllowAttempt("ip", now) {
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
	later := now.Add(time.Minute)
	if !limiter.AllowAttempt("ip", later) || limiter.FailureBlocked("ip", later) {
		t.Fatal("fixed window did not reset")
	}
}
