package limits

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// trackedManager is a manager on the default options with one policy, on a
// clock the test moves.
func trackedManager(t *testing.T, now *time.Time, options Options) *Manager {
	t.Helper()
	options.Now = func() time.Time { return *now }
	manager, err := NewManager(options)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func trackedCall(t *testing.T, manager *Manager, subject string, class RequestClass) Decision {
	t.Helper()
	claim, decision, err := manager.Claim(context.Background(), Subject{OrgID: "org-a", CredentialID: subject}, class)
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		claim.DoneClaim()
	}
	return decision
}

// The cap counts live subjects, not subjects seen since the last sweep. One
// steady caller stops the sweep, so without an expiry per subject, 128 owners
// over days refused every new one with only 2 active at any time.
func TestAnIdleSubjectLeavesTheSetWhileTheOrganizationStaysActive(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	manager := trackedManager(t, &now, Options{})
	newcomers := 0
	for hour := range 24 * 14 {
		for tick := range 6 {
			if decision := trackedCall(t, manager, "steady-owner", RequestClassAuth); !decision.Allowed {
				t.Fatalf("hour %d: the steady owner was refused: %s", hour, decision.Reason)
			}
			if tick == 0 {
				newcomers++
				if decision := trackedCall(t, manager, fmt.Sprintf("newcomer-%d", hour), RequestClassAuth); !decision.Allowed {
					t.Fatalf("hour %d: a new owner was refused with 2 active: %s", hour, decision.Reason)
				}
			}
			now = now.Add(10 * time.Minute)
		}
	}
	if newcomers != 24*14 {
		t.Fatalf("%d newcomers, want %d: the repro measured less than its run", newcomers, 24*14)
	}
	// The state stays bounded, in every map of a subject.
	window := manager.windows[RequestClassAuth]["org-a"]
	if len(window.credentials) > 128 || len(window.touched) != len(window.credentials) || len(window.quotaCredential) != len(window.credentials) || len(window.running) != 0 {
		t.Fatalf("tracked state after 14 days: %d subjects, %d last-seen times, %d quota keys, %d running", len(window.credentials), len(window.touched), len(window.quotaCredential), len(window.running))
	}
}

// The set stays bounded: more live subjects than the cap are refused, as before.
func TestLiveSubjectsPastTheCapAreStillRefused(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	manager := trackedManager(t, &now, Options{MaxCredentialsPerOrganization: 3})
	for step := range 12 {
		for owner := range 3 {
			if decision := trackedCall(t, manager, fmt.Sprintf("owner-%d", owner), RequestClassAuth); !decision.Allowed {
				t.Fatalf("step %d: live owner %d refused: %s", step, owner, decision.Reason)
			}
		}
		if decision := trackedCall(t, manager, "one-too-many", RequestClassAuth); decision.Reason != DenialTrackingCapacity {
			t.Fatalf("step %d: a fourth live owner = %+v, want tracking_capacity", step, decision)
		}
		now = now.Add(10 * time.Minute)
	}
}

// An idle subject that comes back inside its quota window keeps its count, so
// waiting is no way around a quota.
func TestAnIdleSubjectKeepsItsQuotaInsideItsWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	manager := trackedManager(t, &now, Options{MaxCredentialsPerOrganization: 2, StateRetention: time.Minute, Policies: PolicySet{
		Context: ContextPolicy{Window: 2 * time.Hour, PerOrgLimit: 1 << 20, PerCredentialLimit: 1},
	}})
	if decision := trackedCall(t, manager, "quota-used", RequestClassContext); !decision.Allowed {
		t.Fatalf("first request refused: %s", decision.Reason)
	}
	if decision := trackedCall(t, manager, "steady", RequestClassContext); !decision.Allowed {
		t.Fatalf("the second subject refused: %s", decision.Reason)
	}
	// Past the retention, inside the quota window, with a newcomer at the cap.
	now = now.Add(time.Hour)
	if decision := trackedCall(t, manager, "newcomer", RequestClassContext); decision.Reason != DenialTrackingCapacity {
		t.Fatalf("a newcomer at the cap = %+v, want tracking_capacity: the used quota must not leave its window", decision)
	}
	if decision := trackedCall(t, manager, "quota-used", RequestClassContext); decision.Reason != DenialCredentialQuota {
		t.Fatalf("the subject back inside its window = %+v, want its quota", decision)
	}
}

// A request in flight holds its subject, whatever its age: the claim completes
// against the entry it began with.
func TestARunningSubjectNeverLeavesTheSet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	manager := trackedManager(t, &now, Options{MaxCredentialsPerOrganization: 2})
	subject := Subject{OrgID: "org-a", CredentialID: "running"}
	// Seen once before, so it holds a last-seen time that goes stale while the
	// long claim runs.
	if decision := trackedCall(t, manager, "running", RequestClassAuth); !decision.Allowed {
		t.Fatalf("first claim refused: %s", decision.Reason)
	}
	claim, decision, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !decision.Allowed {
		t.Fatalf("claim = %+v, %v", decision, err)
	}
	if decision := trackedCall(t, manager, "steady", RequestClassAuth); !decision.Allowed {
		t.Fatalf("the second subject refused: %s", decision.Reason)
	}
	// The steady caller keeps the organization active past the retention.
	for range 8 {
		now = now.Add(10 * time.Minute)
		if decision := trackedCall(t, manager, "steady", RequestClassAuth); !decision.Allowed {
			t.Fatalf("the steady subject refused: %s", decision.Reason)
		}
	}
	if decision := trackedCall(t, manager, "newcomer", RequestClassAuth); decision.Reason != DenialTrackingCapacity {
		t.Fatalf("a newcomer at the cap = %+v, want tracking_capacity: a running subject stays", decision)
	}
	claim.DoneClaim()
	totals, err := manager.Usage(subject, RequestClassAuth)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Credential.Completed != 2 || totals.Credential.Admitted != 2 {
		t.Fatalf("the running subject's totals = %+v, want its two claims admitted and completed", totals.Credential)
	}
}

// A subject a quota refused has an entry and no claim. It leaves the set too.
func TestASubjectRefusedByAQuotaLeavesTheSetToo(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	manager := trackedManager(t, &now, Options{MaxCredentialsPerOrganization: 2, Policies: PolicySet{
		Context: ContextPolicy{Window: time.Minute, PerOrgLimit: 1},
	}})
	if decision := trackedCall(t, manager, "admitted", RequestClassContext); !decision.Allowed {
		t.Fatalf("first request refused: %s", decision.Reason)
	}
	if decision := trackedCall(t, manager, "refused", RequestClassContext); decision.Reason != DenialOrgQuota {
		t.Fatalf("second subject = %+v, want the organization's quota", decision)
	}
	// One subject keeps the window alive; the refused one goes quiet.
	for range 7 {
		now = now.Add(10 * time.Minute)
		if decision := trackedCall(t, manager, "admitted", RequestClassContext); !decision.Allowed {
			t.Fatalf("the steady subject refused: %s", decision.Reason)
		}
	}
	now = now.Add(time.Minute)
	if decision := trackedCall(t, manager, "newcomer", RequestClassContext); !decision.Allowed {
		t.Fatalf("a newcomer = %+v, want the refused subject's place", decision)
	}
}

// A full set of live subjects costs one scan, not one per refused newcomer: the
// scan holds the manager's lock and the cap is configurable. It runs again at
// the moment a subject can leave, not later.
func TestARefusedNewcomerDoesNotScanAgainBeforeASubjectCanLeave(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	now := start
	manager := trackedManager(t, &now, Options{MaxCredentialsPerOrganization: 2})
	if decision := trackedCall(t, manager, "first", RequestClassAuth); !decision.Allowed {
		t.Fatalf("first subject refused: %s", decision.Reason)
	}
	now = start.Add(30 * time.Minute)
	if decision := trackedCall(t, manager, "second", RequestClassAuth); !decision.Allowed {
		t.Fatalf("second subject refused: %s", decision.Reason)
	}
	now = start.Add(40 * time.Minute)
	for attempt := range 100 {
		if decision := trackedCall(t, manager, fmt.Sprintf("refused-%d", attempt), RequestClassAuth); decision.Reason != DenialTrackingCapacity {
			t.Fatalf("attempt %d = %+v, want tracking_capacity", attempt, decision)
		}
	}
	window := manager.windows[RequestClassAuth]["org-a"]
	if window.forgetPasses != 1 {
		t.Fatalf("%d scans for 100 refused newcomers, want 1", window.forgetPasses)
	}
	if want := start.Add(time.Hour); !window.nextForget.Equal(want) {
		t.Fatalf("the next scan is at %s, want the first subject's expiry %s", window.nextForget, want)
	}
	now = start.Add(time.Hour)
	if decision := trackedCall(t, manager, "newcomer", RequestClassAuth); !decision.Allowed {
		t.Fatalf("a newcomer at the first subject's expiry = %+v, want its place", decision)
	}
}

// A wall clock that steps back can give a finishing subject an expiry earlier
// than the moment the last scan counted on. The next refusal scans again.
func TestAClockStepBackDoesNotDelayAPlaceThatIsFree(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	now := start
	manager := trackedManager(t, &now, Options{MaxCredentialsPerOrganization: 2})
	subject := Subject{OrgID: "org-a", CredentialID: "running"}
	claim, decision, err := manager.Claim(context.Background(), subject, RequestClassAuth)
	if err != nil || !decision.Allowed {
		t.Fatalf("claim = %+v, %v", decision, err)
	}
	now = start.Add(40 * time.Minute)
	if decision := trackedCall(t, manager, "other", RequestClassAuth); !decision.Allowed {
		t.Fatalf("the second subject refused: %s", decision.Reason)
	}
	if decision := trackedCall(t, manager, "refused", RequestClassAuth); decision.Reason != DenialTrackingCapacity {
		t.Fatalf("a newcomer at the cap = %+v, want tracking_capacity", decision)
	}
	// The clock steps back 30 minutes, and the running claim finishes there.
	now = start.Add(10 * time.Minute)
	claim.DoneClaim()
	// A steady caller keeps the organization's window from being swept.
	now = start.Add(65 * time.Minute)
	if decision := trackedCall(t, manager, "other", RequestClassAuth); !decision.Allowed {
		t.Fatalf("the steady subject refused: %s", decision.Reason)
	}
	// The finished subject can leave at 10:10, before the scan's 10:40.
	now = start.Add(70 * time.Minute)
	if decision := trackedCall(t, manager, "newcomer", RequestClassAuth); !decision.Allowed {
		t.Fatalf("a newcomer after the finished subject's expiry = %+v, want its place", decision)
	}
}
