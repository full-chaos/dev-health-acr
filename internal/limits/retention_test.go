package limits

import (
	"context"
	"testing"
	"time"
)

func TestManagerBoundsAndSweepsTrackedState(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(Options{
		Now: nowFunc(&now), MaxTrackedOrganizations: 2, MaxCredentialsPerOrganization: 1,
		StateRetention: time.Minute, MaxRetryAfter: 7 * time.Second,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	claimAndDone(t, manager, Subject{OrgID: "org-a", CredentialID: "credential-a"})
	claimAndDone(t, manager, Subject{OrgID: "org-b", CredentialID: "credential-b"})
	_, denied, err := manager.Claim(context.Background(), Subject{OrgID: "org-c", CredentialID: "credential-c"}, RequestClassAuth)
	if err != nil || denied.Reason != DenialTrackingCapacity || denied.RetryAfter != 7*time.Second {
		t.Fatalf("organization capacity decision = %#v, err %v", denied, err)
	}
	if got := len(manager.windows[RequestClassAuth]); got != 2 {
		t.Fatalf("tracked organizations = %d, want 2", got)
	}
	now = now.Add(time.Minute)
	claimAndDone(t, manager, Subject{OrgID: "org-c", CredentialID: "credential-c"})
	if got := len(manager.windows[RequestClassAuth]); got != 1 {
		t.Fatalf("organizations after sweep = %d, want 1", got)
	}

	_, denied, err = manager.Claim(context.Background(), Subject{OrgID: "org-c", CredentialID: "credential-other"}, RequestClassAuth)
	if err != nil || denied.Reason != DenialTrackingCapacity {
		t.Fatalf("credential capacity decision = %#v, err %v", denied, err)
	}
}

func TestManagerDoesNotSweepQuotaBeforeLongerPolicyWindow(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(Options{
		Now:            func() time.Time { return now },
		Policies:       PolicySet{Context: ContextPolicy{Window: 10 * time.Minute, PerOrgLimit: 1}},
		StateRetention: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	subject := Subject{OrgID: "org", CredentialID: "credential"}
	claim, decision, err := manager.Claim(context.Background(), subject, RequestClassContext)
	if err != nil || !decision.Allowed {
		t.Fatalf("first claim = (%#v, %#v, %v)", claim, decision, err)
	}
	claim.DoneClaim()
	now = now.Add(2 * time.Minute)

	_, decision, err = manager.Claim(context.Background(), subject, RequestClassContext)
	if err != nil || decision.Allowed || decision.Reason != DenialOrgQuota {
		t.Fatalf("quota was swept before policy window: (%#v, %v)", decision, err)
	}
}

func TestManagerCapsQuotaAndConcurrencyRetryHints(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(Options{
		Now: nowFunc(&now), PerOrgConcurrency: 1, MaxRetryAfter: 3 * time.Second, ConcurrencyRetryAfter: 10 * time.Second,
		Policies: PolicySet{Auth: AuthPolicy{Window: time.Hour, PerOrgLimit: 1}},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	subject := Subject{OrgID: "org", CredentialID: "credential"}
	claim, allowed, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("initial claim = (%v, %#v, %v)", claim, allowed, err)
	}
	_, concurrent, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || concurrent.Reason != DenialConcurrency || concurrent.RetryAfter != 3*time.Second {
		t.Fatalf("concurrency retry = %#v, err %v", concurrent, err)
	}
	claim.DoneClaim()
	_, quota, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || quota.Reason != DenialOrgQuota || quota.RetryAfter != 3*time.Second {
		t.Fatalf("quota retry = %#v, err %v", quota, err)
	}
}

func TestManagerRetainsRolloverWindowUntilClaimCompletes(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, nowFunc(&now), PolicySet{
		Evidence: EvidencePolicy{Window: time.Minute},
	})
	subject := Subject{OrgID: "org", CredentialID: "credential"}
	claim, allowed, err := manager.Claim(context.Background(), subject, RequestClassEvidence)
	if err != nil || !allowed.Allowed {
		t.Fatalf("initial claim = (%v, %#v, %v), want allowed", claim, allowed, err)
	}
	now = now.Add(time.Minute)
	if err := claim.Complete(ResourceUsage{Items: 1, Tokens: 2, Bytes: 3}); err != nil {
		t.Fatalf("completion after rollover: %v", err)
	}
	usage, err := manager.Usage(subject, RequestClassEvidence)
	want := UsageCounters{Admitted: 1, Completed: 1, Items: 1, Tokens: 2, Bytes: 3}
	if err != nil || usage.Org != want || usage.Credential != want {
		t.Fatalf("rollover usage = %#v, err %v, want %#v", usage, err, want)
	}
	claimAndDoneForClass(t, manager, subject, RequestClassEvidence)
}

func claimAndDone(t *testing.T, manager *Manager, subject Subject) {
	claimAndDoneForClass(t, manager, subject, RequestClassAuth)
}

func claimAndDoneForClass(t *testing.T, manager *Manager, subject Subject, class RequestClass) {
	t.Helper()
	claim, decision, err := manager.Claim(context.Background(), subject, class)
	if err != nil || !decision.Allowed {
		t.Fatalf("claim = (%v, %#v, %v), want allowed", claim, decision, err)
	}
	claim.DoneClaim()
}

func nowFunc(now *time.Time) func() time.Time { return func() time.Time { return *now } }
