package limits

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewManagerRejectsInvalidPolicies(t *testing.T) {
	tests := []struct {
		name    string
		options Options
	}{
		{
			name:    "quota without window",
			options: Options{Policies: PolicySet{Evidence: EvidencePolicy{PerOrgLimit: 1}}},
		},
		{
			name:    "negative credential quota",
			options: Options{Policies: PolicySet{Snapshot: SnapshotPolicy{Window: time.Minute, PerCredentialLimit: -1}}},
		},
		{
			name:    "negative resource budget",
			options: Options{Policies: PolicySet{Episode: EpisodePolicy{Resources: ResourceBudget{MaxBytes: -1}}}},
		},
		{
			name:    "negative concurrency",
			options: Options{PerOrgConcurrency: -1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewManager(test.options); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("NewManager() error = %v, want ErrInvalidPolicy", err)
			}
		})
	}
}

func TestManagerRejectsMalformedSubjectsAndRequestClasses(t *testing.T) {
	manager := newTestManager(t, time.Now, PolicySet{Episode: EpisodePolicy{Window: time.Minute, PerOrgLimit: 1}})
	malformed := []Subject{
		{OrgID: "", CredentialID: "credential"},
		{OrgID: "org", CredentialID: ""},
		{OrgID: "org\nnext", CredentialID: "credential"},
		{OrgID: "org", CredentialID: "credential\tother"},
		{OrgID: string([]byte{0xff}), CredentialID: "credential"},
	}
	for _, subject := range malformed {
		if _, _, err := manager.Claim(context.Background(), subject, RequestClassEpisode); !errors.Is(err, ErrInvalidSubject) {
			t.Fatalf("Claim(%#v) error = %v, want ErrInvalidSubject", subject, err)
		}
	}
	valid := Subject{OrgID: "org", CredentialID: "credential"}
	if _, _, err := manager.Claim(context.Background(), valid, RequestClass(99)); !errors.Is(err, ErrInvalidRequestClass) {
		t.Fatalf("invalid request class error = %v, want ErrInvalidRequestClass", err)
	}
}

func TestManagerRoundsRetryHintsUpToSafeSecond(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, func() time.Time { return now }, PolicySet{
		Episode: EpisodePolicy{Window: 1500 * time.Millisecond, PerOrgLimit: 1},
	})
	subject := Subject{OrgID: "org", CredentialID: "credential"}
	claim, allowed, err := manager.Claim(context.Background(), subject, RequestClassEpisode)
	if err != nil || !allowed.Allowed {
		t.Fatalf("initial claim = (%v, %#v, %v)", claim, allowed, err)
	}
	claim.DoneClaim()
	now = now.Add(time.Millisecond)
	_, denied, err := manager.Claim(context.Background(), subject, RequestClassEpisode)
	if err != nil || denied.Allowed || denied.RetryAfter != 2*time.Second {
		t.Fatalf("retry decision = %#v, err %v, want 2s", denied, err)
	}
}

func TestManagerHonorsQuotasWithZeroClock(t *testing.T) {
	manager := newTestManager(t, func() time.Time { return time.Time{} }, PolicySet{
		Snapshot: SnapshotPolicy{Window: time.Minute, PerOrgLimit: 1},
	})
	subject := Subject{OrgID: "org", CredentialID: "credential"}
	claim, allowed, err := manager.Claim(context.Background(), subject, RequestClassSnapshot)
	if err != nil || !allowed.Allowed {
		t.Fatalf("initial zero-clock claim = (%v, %#v, %v)", claim, allowed, err)
	}
	claim.DoneClaim()
	_, denied, err := manager.Claim(context.Background(), subject, RequestClassSnapshot)
	if err != nil || denied.Allowed || denied.Reason != DenialOrgQuota {
		t.Fatalf("zero-clock quota decision = %#v, err %v", denied, err)
	}
}
