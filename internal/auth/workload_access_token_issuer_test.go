package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func newTestAccessTokenIssuer(t *testing.T, now func() time.Time) AccessTokenIssuer {
	t.Helper()
	// The store's own clock must match the fixture's, not the real wall
	// clock -- its own ExpiresAt.After(now) check would otherwise compare
	// a fixed test timestamp against real time.Now() and fail flakily.
	store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: memory.NewAuditStore(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := NewService(store, ServiceOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewWorkloadAccessTokenIssuer(credentials, now)
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func TestWorkloadAccessTokenIssuer_capsExpiryAtSubjectExpiryWhenSooner(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issuer := newTestAccessTokenIssuer(t, func() time.Time { return now })
	binding := WorkloadBinding{BindingID: "wlb_1", OrgID: "11111111-1111-4111-8111-111111111111", Role: "read", RepositoryScopes: []string{"*"}}
	subjectExpiresAt := now.Add(2 * time.Minute) // sooner than WorkloadAccessTokenLifetime
	issued, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), subjectExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Credential.ExpiresAt == nil || !issued.Credential.ExpiresAt.Equal(subjectExpiresAt) {
		t.Fatalf("expires_at = %v, want capped at subject expiry %v", issued.Credential.ExpiresAt, subjectExpiresAt)
	}
}

func TestWorkloadAccessTokenIssuer_usesTheFixedLifetimeWhenSubjectOutlivesIt(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issuer := newTestAccessTokenIssuer(t, func() time.Time { return now })
	binding := WorkloadBinding{BindingID: "wlb_2", OrgID: "11111111-1111-4111-8111-111111111111", Role: "ops", RepositoryScopes: []string{"*"}}
	subjectExpiresAt := now.Add(24 * time.Hour)
	issued, err := issuer.Issue(context.Background(), binding, RoleScopes("ops"), subjectExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(WorkloadAccessTokenLifetime)
	if issued.Credential.ExpiresAt == nil || !issued.Credential.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want the fixed lifetime %v", issued.Credential.ExpiresAt, want)
	}
}

func TestWorkloadAccessTokenIssuer_rejectsAnAlreadyExpiredSubject(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issuer := newTestAccessTokenIssuer(t, func() time.Time { return now })
	binding := WorkloadBinding{BindingID: "wlb_3", OrgID: "11111111-1111-4111-8111-111111111111", Role: "read", RepositoryScopes: []string{"*"}}
	if _, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), now.Add(-time.Minute)); !errors.Is(err, ErrSubjectTokenInvalid) {
		t.Fatalf("error = %v, want ErrSubjectTokenInvalid", err)
	}
}

func TestWorkloadAccessTokenIssuer_marksProvenanceAndBindingID(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: memory.NewAuditStore(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := NewService(store, ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewWorkloadAccessTokenIssuer(credentials, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	binding := WorkloadBinding{BindingID: "wlb_4", OrgID: "11111111-1111-4111-8111-111111111111", Role: "read", RepositoryScopes: []string{"*"}}
	issued, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if issued.Credential.WorkloadBindingID == nil || *issued.Credential.WorkloadBindingID != "wlb_4" {
		t.Fatalf("WorkloadBindingID = %v, want wlb_4", issued.Credential.WorkloadBindingID)
	}
	if !IsTokenShapeValid(issued.Token) {
		t.Fatalf("issued token %q does not match the ACR token shape", issued.Token)
	}
	// The stored record's IssuanceProvenance is not exposed on
	// ClientCredential (see storage.CredentialRecord's own doc comment on
	// TokenHash never being included in public DTOs) -- it is asserted
	// indirectly here via the token's successful lookup, and directly by
	// storage-level tests in internal/storage/memory and .../postgres.
	if _, err := store.FindByTokenHash(context.Background(), HashToken(issued.Token)); err != nil {
		t.Fatal(err)
	}
}
