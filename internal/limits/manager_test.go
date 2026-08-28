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

// TestClaimCompleteWithBudgetOverridesTheCeilingButRecordsRealUsage is the
// CHAOS-4355 unit-level guard for Claim.CompleteWithBudget: an override
// budget can admit usage the class's OWN configured budget would reject
// along one dimension (here, Tokens), but the accepted usage recorded into
// Manager.Usage()'s org/credential window totals is exactly what the
// caller passed -- never a false or zeroed record, and never the ceiling
// itself.
func TestClaimCompleteWithBudgetOverridesTheCeilingButRecordsRealUsage(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, func() time.Time { return now }, PolicySet{
		Context: ContextPolicy{Window: time.Minute, PerOrgLimit: 10, PerCredentialLimit: 10, Resources: ResourceBudget{MaxTokens: 4000, MaxBytes: 262144}},
	})
	subject := Subject{OrgID: "org-a", CredentialID: "credential-a"}

	claim, decision, err := manager.Claim(context.Background(), subject, RequestClassContext)
	if err != nil || !decision.Allowed {
		t.Fatalf("claim = (%v, %#v, %v), want allowed", claim, decision, err)
	}
	// 17700 exceeds the class's own MaxTokens (4000, set above) -- Complete
	// would reject this. CompleteWithBudget must not.
	usage := ResourceUsage{Items: 3, Tokens: 17700, Bytes: 70797}
	override := ResourceBudget{MaxItems: 30, MaxTokens: 0, MaxBytes: 262144}
	if err := claim.CompleteWithBudget(usage, override); err != nil {
		t.Fatalf("CompleteWithBudget with MaxTokens=0 override = %v, want nil (unlimited Tokens must admit 17700)", err)
	}

	got, err := manager.Usage(subject, RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
	want := UsageCounters{Admitted: 1, Completed: 1, Items: 3, Tokens: 17700, Bytes: 70797}
	if got.Org != want {
		t.Fatalf("org usage = %#v, want %#v -- CompleteWithBudget must record the REAL usage, not the override or zero", got.Org, want)
	}
	if got.Credential != want {
		t.Fatalf("credential usage = %#v, want %#v", got.Credential, want)
	}
}

// TestClaimCompleteWithBudgetStillRejectsOverTheOverrideCeiling proves the
// override is a real budget, not a bypass: a dimension the override DOES
// bound (Bytes here) still rejects, and the rejection is still recorded
// (Denied, not Completed).
func TestClaimCompleteWithBudgetStillRejectsOverTheOverrideCeiling(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, func() time.Time { return now }, PolicySet{
		Context: ContextPolicy{Window: time.Minute, PerOrgLimit: 10, PerCredentialLimit: 10, Resources: ResourceBudget{MaxTokens: 4000, MaxBytes: 262144}},
	})
	subject := Subject{OrgID: "org-a", CredentialID: "credential-a"}

	claim, decision, err := manager.Claim(context.Background(), subject, RequestClassContext)
	if err != nil || !decision.Allowed {
		t.Fatalf("claim = (%v, %#v, %v), want allowed", claim, decision, err)
	}
	usage := ResourceUsage{Items: 1, Tokens: 100, Bytes: 300000}
	override := ResourceBudget{MaxItems: 30, MaxTokens: 0, MaxBytes: 262144}
	if err := claim.CompleteWithBudget(usage, override); !errors.Is(err, ErrResourceBudgetExceeded) {
		t.Fatalf("CompleteWithBudget over MaxBytes = %v, want ErrResourceBudgetExceeded", err)
	}

	got, err := manager.Usage(subject, RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
	// Completed tracks lifecycle completion (a Complete/CompleteWithBudget
	// call was made) regardless of accept/deny -- see manager.go's
	// complete(), which increments it unconditionally before branching on
	// accepted. Denied and the resource counters are what distinguish a
	// rejected claim: Denied increments, Items/Tokens/Bytes do not.
	if got.Org.Completed != 1 || got.Org.Denied != 1 || got.Org.Items != 0 || got.Org.Tokens != 0 || got.Org.Bytes != 0 {
		t.Fatalf("org usage = %#v, want Completed=1 Denied=1 and zero resource counters -- a rejected claim must not record accepted usage", got.Org)
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
