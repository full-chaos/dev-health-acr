package limits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestManagerUsesSeparatePoliciesAndTracksUsage(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, func() time.Time { return now }, PolicySet{
		Auth:    AuthPolicy{Window: time.Minute, PerOrgLimit: 2, PerCredentialLimit: 1},
		Context: ContextPolicy{Window: time.Minute, PerOrgLimit: 1, PerCredentialLimit: 1},
	})
	subjectA := Subject{OrgID: "org-a", CredentialID: "credential-a"}

	contextClaim, contextDecision, err := manager.Claim(context.Background(), subjectA, RequestClassContext)
	if err != nil || !contextDecision.Allowed {
		t.Fatalf("context claim = (%v, %#v, %v), want allowed", contextClaim, contextDecision, err)
	}
	contextClaim.DoneClaim()

	authClaim, authDecision, err := manager.Claim(context.Background(), subjectA, RequestClassAuth)
	if err != nil || !authDecision.Allowed {
		t.Fatalf("auth claim = (%v, %#v, %v), want allowed", authClaim, authDecision, err)
	}
	if err := authClaim.Complete(ResourceUsage{Items: 2, Tokens: 3, Bytes: 4}); err != nil {
		t.Fatalf("complete auth claim: %v", err)
	}
	if err := authClaim.Complete(ResourceUsage{Items: 99}); err != nil {
		t.Fatalf("repeat complete auth claim: %v", err)
	}

	_, denied, err := manager.Claim(context.Background(), subjectA, RequestClassAuth)
	if err != nil || denied.Allowed || denied.Reason != DenialCredentialQuota || denied.RetryAfter != time.Minute {
		t.Fatalf("credential quota decision = %#v, err %v", denied, err)
	}

	secondCredential := Subject{OrgID: "org-a", CredentialID: "credential-b"}
	claim, allowed, err := manager.Claim(context.Background(), secondCredential, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("second credential claim = (%v, %#v, %v), want allowed", claim, allowed, err)
	}
	claim.DoneClaim()

	_, denied, err = manager.Claim(context.Background(), Subject{OrgID: "org-a", CredentialID: "credential-c"}, RequestClassAuth)
	if err != nil || denied.Allowed || denied.Reason != DenialOrgQuota || denied.RetryAfter != time.Minute {
		t.Fatalf("org quota decision = %#v, err %v", denied, err)
	}

	usage, err := manager.Usage(subjectA, RequestClassAuth)
	if err != nil || usage.Org != (UsageCounters{Admitted: 2, Denied: 2, Completed: 2, Items: 2, Tokens: 3, Bytes: 4}) || usage.Credential != (UsageCounters{Admitted: 1, Denied: 1, Completed: 1, Items: 2, Tokens: 3, Bytes: 4}) || !usage.WindowStarted.Equal(now) {
		t.Fatalf("usage = %#v, err %v", usage, err)
	}

	now = now.Add(time.Minute)
	claim, allowed, err = manager.Claim(context.Background(), subjectA, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("window reset claim = (%v, %#v, %v), want allowed", claim, allowed, err)
	}
	claim.DoneClaim()
}

func TestManagerConcurrencyCancellationAndIdempotentRelease(t *testing.T) {
	manager := newTestManager(t, time.Now, PolicySet{Auth: AuthPolicy{Window: time.Minute}}, 1)
	subject := Subject{OrgID: "org-a", CredentialID: "credential-a"}

	claim, allowed, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("first claim = (%v, %#v, %v), want allowed", claim, allowed, err)
	}
	_, denied, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || denied.Allowed || denied.Reason != DenialConcurrency || denied.RetryAfter != defaultConcurrencyRetry {
		t.Fatalf("concurrency decision = %#v, err %v", denied, err)
	}
	if err := claim.Complete(ResourceUsage{Items: -1}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("negative completion error = %v, want ErrInvalidUsage", err)
	}
	_, denied, err = manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || denied.Reason != DenialConcurrency {
		t.Fatalf("claim after invalid completion = %#v, err %v", denied, err)
	}

	claim.DoneClaim()
	claim.DoneClaim()
	second, allowed, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("claim after release = (%v, %#v, %v), want allowed", second, allowed, err)
	}
	second.DoneClaim()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := manager.Claim(canceled, subject, RequestClassAuth); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled claim error = %v, want context canceled", err)
	}
	if _, _, err := manager.Claim(context.Background(), Subject{OrgID: " \t", CredentialID: "credential-a"}, RequestClassAuth); !errors.Is(err, ErrInvalidSubject) {
		t.Fatalf("malformed subject error = %v, want ErrInvalidSubject", err)
	}
}

func TestManagerDoesNotConsumeStateAfterCanceledLockWait(t *testing.T) {
	manager := newTestManager(t, time.Now, PolicySet{Auth: AuthPolicy{Window: time.Minute, PerOrgLimit: 1}})
	subject := Subject{OrgID: "org-a", CredentialID: "credential-a"}
	parent, cancel := context.WithCancel(context.Background())
	ctx := &firstPassContext{Context: parent, first: make(chan struct{})}
	manager.mu.Lock()
	result := make(chan error, 1)
	go func() {
		_, _, err := manager.Claim(ctx, subject, RequestClassAuth)
		result <- err
	}()
	<-ctx.first
	cancel()
	manager.mu.Unlock()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock-wait claim error = %v, want context canceled", err)
	}
	claim, allowed, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("claim after canceled wait = (%v, %#v, %v), want allowed", claim, allowed, err)
	}
	claim.DoneClaim()
}

func TestManagerLimitsConcurrentClaims(t *testing.T) {
	manager := newTestManager(t, time.Now, PolicySet{Auth: AuthPolicy{Window: time.Minute}}, 3)
	subject := Subject{OrgID: "org-a", CredentialID: "credential-a"}
	start := make(chan struct{})
	claims := make(chan *Claim, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			claim, decision, err := manager.Claim(context.Background(), subject, RequestClassAuth)
			if err != nil {
				t.Errorf("concurrent claim: %v", err)
				return
			}
			if decision.Allowed {
				claims <- claim
			}
		}()
	}
	close(start)
	group.Wait()
	close(claims)

	count := 0
	for claim := range claims {
		count++
		claim.DoneClaim()
	}
	if count != 3 {
		t.Fatalf("allowed concurrent claims = %d, want 3", count)
	}
}

func TestManagerEnforcesPerClassResourceBudget(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, func() time.Time { return now }, PolicySet{
		Context: ContextPolicy{Window: time.Minute, Resources: ResourceBudget{MaxItems: 1, MaxTokens: 2, MaxBytes: 3}},
	})
	subject := Subject{OrgID: "org-a", CredentialID: "credential-a"}

	overBudget, allowed, err := manager.Claim(context.Background(), subject, RequestClassContext)
	if err != nil || !allowed.Allowed {
		t.Fatalf("over-budget claim = (%v, %#v, %v), want allowed", overBudget, allowed, err)
	}
	if err := overBudget.Complete(ResourceUsage{Items: 2, Tokens: 2, Bytes: 3}); !errors.Is(err, ErrResourceBudgetExceeded) {
		t.Fatalf("over-budget completion error = %v, want ErrResourceBudgetExceeded", err)
	}
	if err := overBudget.Complete(ResourceUsage{Items: 1, Tokens: 2, Bytes: 3}); !errors.Is(err, ErrResourceBudgetExceeded) {
		t.Fatalf("repeat rejected completion error = %v, want ErrResourceBudgetExceeded", err)
	}

	withinBudget, allowed, err := manager.Claim(context.Background(), subject, RequestClassContext)
	if err != nil || !allowed.Allowed {
		t.Fatalf("claim after budget rejection = (%v, %#v, %v), want allowed", withinBudget, allowed, err)
	}
	if err := withinBudget.Complete(ResourceUsage{Items: 1, Tokens: 2, Bytes: 3}); err != nil {
		t.Fatalf("within-budget completion: %v", err)
	}
	if err := withinBudget.Complete(ResourceUsage{Items: 2, Tokens: 2, Bytes: 3}); err != nil {
		t.Fatalf("repeat accepted completion error = %v, want nil", err)
	}
	usage, err := manager.Usage(subject, RequestClassContext)
	want := UsageCounters{Admitted: 2, Denied: 1, Completed: 2, Items: 1, Tokens: 2, Bytes: 3}
	if err != nil || usage.Org != want || usage.Credential != want {
		t.Fatalf("resource-budget usage = %#v, err %v, want %#v", usage, err, want)
	}
}

func TestManagerUsesQuotaEpochForRolloverRetry(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, func() time.Time { return now }, PolicySet{
		Auth: AuthPolicy{Window: time.Minute, PerOrgLimit: 1},
	})
	first := Subject{OrgID: "org", CredentialID: "credential-a"}
	claim, allowed, err := manager.Claim(context.Background(), first, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("initial claim = (%v, %#v, %v)", claim, allowed, err)
	}
	claim.DoneClaim()
	now = now.Add(time.Minute)
	claim, allowed, err = manager.Claim(context.Background(), first, RequestClassAuth)
	if err != nil || !allowed.Allowed {
		t.Fatalf("rollover claim = (%v, %#v, %v)", claim, allowed, err)
	}
	claim.DoneClaim()
	_, denied, err := manager.Claim(context.Background(), Subject{OrgID: "org", CredentialID: "credential-b"}, RequestClassAuth)
	if err != nil || denied.Reason != DenialOrgQuota || denied.RetryAfter != time.Minute {
		t.Fatalf("rollover quota decision = %#v, err %v", denied, err)
	}
}

func newTestManager(t *testing.T, now func() time.Time, policies PolicySet, concurrency ...int) *Manager {
	t.Helper()
	limit := 0
	if len(concurrency) > 0 {
		limit = concurrency[0]
	}
	manager, err := NewManager(Options{Now: now, Policies: policies, PerOrgConcurrency: limit})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

type firstPassContext struct {
	context.Context
	first chan struct{}
	once  sync.Once
}

func (ctx *firstPassContext) Err() error {
	ctx.once.Do(func() { close(ctx.first) })
	return ctx.Context.Err()
}
