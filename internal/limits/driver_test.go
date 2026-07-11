package limits

import (
	"context"
	"testing"
	"time"
)

func TestDriverQuotaConcurrencyAndRetryHints(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	subject := Subject{OrgID: "driver-org", CredentialID: "driver-credential"}
	quotaManager := newTestManager(t, func() time.Time { return now }, PolicySet{
		Auth: AuthPolicy{Window: time.Minute, PerCredentialLimit: 1},
	})
	quotaClaim, allowed, err := quotaManager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("quota driver initial claim = (%v, %#v, %v)", quotaClaim, allowed, err)
	}
	quotaClaim.DoneClaim()
	_, quotaDenied, err := quotaManager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || quotaDenied.Reason != DenialCredentialQuota || quotaDenied.RetryAfter != time.Minute {
		t.Fatalf("quota driver decision = %#v, err %v", quotaDenied, err)
	}
	t.Logf("quota denied: reason=%s retry_after=%s", quotaDenied.Reason, quotaDenied.RetryAfter)

	concurrencyManager := newTestManager(t, func() time.Time { return now }, PolicySet{Auth: AuthPolicy{}}, 1)
	lease, allowed, err := concurrencyManager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("concurrency driver initial claim = (%v, %#v, %v)", lease, allowed, err)
	}
	defer lease.DoneClaim()
	_, concurrencyDenied, err := concurrencyManager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || concurrencyDenied.Reason != DenialConcurrency || concurrencyDenied.RetryAfter != defaultConcurrencyRetry {
		t.Fatalf("concurrency driver decision = %#v, err %v", concurrencyDenied, err)
	}
	t.Logf("concurrency denied: reason=%s retry_after=%s", concurrencyDenied.Reason, concurrencyDenied.RetryAfter)
}
