package api

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestDeviceAuthorizationLimiter_enforcesOperationThresholdsPerSubject(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	limiter := NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now }))
	userCode := storage.HashUserCode("ABCD-EFGH")

	// When
	for range 10 {
		if !limiter.AllowDeviceCreation("192.0.2.10").Allowed {
			t.Fatal("device creation was denied before the tenth request")
		}
	}
	creationDenied := limiter.AllowDeviceCreation("192.0.2.10")
	for range 60 {
		if !limiter.AllowTokenRequest("192.0.2.10").Allowed {
			t.Fatal("token request was denied before the sixtieth request")
		}
	}
	tokenDenied := limiter.AllowTokenRequest("192.0.2.10")
	for range 5 {
		if !limiter.AllowApprovalAttempt("192.0.2.10", userCode).Allowed {
			t.Fatal("approval attempt was denied before the fifth request")
		}
	}
	approvalDenied := limiter.AllowApprovalAttempt("192.0.2.10", userCode)

	// Then
	if creationDenied.Allowed || tokenDenied.Allowed || approvalDenied.Allowed {
		t.Fatalf("denial decisions = %#v, %#v, %#v", creationDenied, tokenDenied, approvalDenied)
	}
	if creationDenied.RetryAfter != time.Minute || tokenDenied.RetryAfter != time.Minute || approvalDenied.RetryAfter != time.Minute {
		t.Fatalf("retry values = %#v, %#v, %#v", creationDenied, tokenDenied, approvalDenied)
	}
}

func TestDeviceAuthorizationLimiter_keepsApprovalSubjectsIndependent(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	limiter := NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now }))
	first := storage.HashUserCode("ABCD-EFGH")
	second := storage.HashUserCode("IJKL-MNOP")
	for range 5 {
		if !limiter.AllowApprovalAttempt("192.0.2.10", first).Allowed {
			t.Fatal("first approval subject was denied before its limit")
		}
	}

	// When
	decision := limiter.AllowApprovalAttempt("192.0.2.10", second)

	// Then
	if !decision.Allowed {
		t.Fatalf("independent approval subject was denied: %#v", decision)
	}
}

func TestDeviceAuthorizationLimiter_isConcurrencySafeAtDeviceCreationBoundary(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	limiter := NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now }))
	var allowed atomic.Int64
	var group sync.WaitGroup

	// When
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			if limiter.AllowDeviceCreation("192.0.2.10").Allowed {
				allowed.Add(1)
			}
		}()
	}
	group.Wait()

	// Then
	if allowed.Load() != 10 {
		t.Fatalf("allowed device creations = %d, want 10", allowed.Load())
	}
}

func TestDeviceAuthorizationLimiter_reclaimsExpiredSubjectsBeforeAdmittingNewOnes(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	limiter := NewBoundedDeviceAuthorizationLimiter(DeviceAuthorizationLimiterOptions{
		Clock: ClockFunc(func() time.Time { return now }), MaxTrackedKeys: 2,
	})
	if !limiter.AllowTokenRequest("192.0.2.1").Allowed || !limiter.AllowTokenRequest("192.0.2.2").Allowed {
		t.Fatal("initial subjects were unexpectedly denied")
	}

	// When
	blocked := limiter.AllowTokenRequest("192.0.2.3")
	now = now.Add(time.Minute)
	admitted := limiter.AllowTokenRequest("192.0.2.3")

	// Then
	if blocked.Allowed || !admitted.Allowed {
		t.Fatalf("bounded cleanup decisions = %#v, %#v", blocked, admitted)
	}
}
