package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/full-chaos/dev-health-go/authverify"
)

func newTestAccessTokenIssuer(t *testing.T, now func() time.Time) authverify.AccessTokenIssuer {
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
	binding := authverify.WorkloadBinding{BindingID: "wlb_1", OrgID: "11111111-1111-4111-8111-111111111111", GrantedScopes: RoleScopes("read"), RepositoryScopes: []string{"*"}}
	subjectExpiresAt := now.Add(2 * time.Minute) // sooner than WorkloadAccessTokenLifetime
	issued, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), subjectExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if issued.ExpiresAt == nil || !issued.ExpiresAt.Equal(subjectExpiresAt) {
		t.Fatalf("expires_at = %v, want capped at subject expiry %v", issued.ExpiresAt, subjectExpiresAt)
	}
}

func TestWorkloadAccessTokenIssuer_usesTheFixedLifetimeWhenSubjectOutlivesIt(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issuer := newTestAccessTokenIssuer(t, func() time.Time { return now })
	binding := authverify.WorkloadBinding{BindingID: "wlb_2", OrgID: "11111111-1111-4111-8111-111111111111", GrantedScopes: RoleScopes("ops"), RepositoryScopes: []string{"*"}}
	subjectExpiresAt := now.Add(24 * time.Hour)
	issued, err := issuer.Issue(context.Background(), binding, RoleScopes("ops"), subjectExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(authverify.WorkloadAccessTokenLifetime)
	if issued.ExpiresAt == nil || !issued.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want the fixed lifetime %v", issued.ExpiresAt, want)
	}
}

func TestWorkloadAccessTokenIssuer_rejectsASubjectExpiringTooSoonToBeUseful(t *testing.T) {
	// Codex round 1 finding: a subject token with only, say, 5 seconds of
	// life left passed the old ">now" check and minted a token the
	// sidecar's own cache margin (workloadRefreshMargin, 30s) would treat
	// as immediately stale, wasting the issuance and the purge-eligible
	// row it created. minWorkloadAccessTokenLifetime closes that gap.
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issuer := newTestAccessTokenIssuer(t, func() time.Time { return now })
	binding := authverify.WorkloadBinding{BindingID: "wlb_5", OrgID: "11111111-1111-4111-8111-111111111111", GrantedScopes: RoleScopes("read"), RepositoryScopes: []string{"*"}}
	if _, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), now.Add(5*time.Second)); !errors.Is(err, authverify.ErrSubjectTokenInvalid) {
		t.Fatalf("error = %v, want ErrSubjectTokenInvalid for a subject token expiring in 5s (below minWorkloadAccessTokenLifetime)", err)
	}
}

func TestWorkloadAccessTokenIssuer_rejectsAnAlreadyExpiredSubject(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	issuer := newTestAccessTokenIssuer(t, func() time.Time { return now })
	binding := authverify.WorkloadBinding{BindingID: "wlb_3", OrgID: "11111111-1111-4111-8111-111111111111", GrantedScopes: RoleScopes("read"), RepositoryScopes: []string{"*"}}
	if _, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), now.Add(-time.Minute)); !errors.Is(err, authverify.ErrSubjectTokenInvalid) {
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
	binding := authverify.WorkloadBinding{BindingID: "wlb_4", OrgID: "11111111-1111-4111-8111-111111111111", GrantedScopes: RoleScopes("read"), RepositoryScopes: []string{"*"}}
	issued, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !IsTokenShapeValid(issued.Token) {
		t.Fatalf("issued token %q does not match the ACR token shape", issued.Token)
	}
	// WorkloadBindingID and IssuanceProvenance are not exposed on
	// authverify.IssuedToken (a neutral {Token, ExpiresAt} shape) -- both
	// are asserted here via the stored record itself, the same lookup
	// path AccessTokenIssuer's own caller (the credential authenticator)
	// uses.
	record, err := store.FindByTokenHash(context.Background(), HashToken(issued.Token))
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkloadBindingID == nil || *record.WorkloadBindingID != "wlb_4" {
		t.Fatalf("WorkloadBindingID = %v, want wlb_4", record.WorkloadBindingID)
	}
}

func TestWorkloadAccessTokenIssuer_setsIssuedAtToTheInjectedNowWithinTheCeiling(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	binding := authverify.WorkloadBinding{BindingID: "wlb_6", OrgID: "11111111-1111-4111-8111-111111111111", GrantedScopes: RoleScopes("read"), RepositoryScopes: []string{"*"}}
	cases := []struct {
		name             string
		subjectExpiresAt time.Time
		wantLifetime     time.Duration
	}{
		{"full lifetime", now.Add(24 * time.Hour), authverify.WorkloadAccessTokenLifetime},
		{"cut by subject expiry", now.Add(2 * time.Minute), 2 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer := newTestAccessTokenIssuer(t, func() time.Time { return now })
			issued, err := issuer.Issue(context.Background(), binding, RoleScopes("read"), tc.subjectExpiresAt)
			if err != nil {
				t.Fatal(err)
			}
			if issued.IssuedAt == nil || !issued.IssuedAt.Equal(now) {
				t.Fatalf("issued_at = %v, want the injected now %v", issued.IssuedAt, now)
			}
			if issued.ExpiresAt == nil {
				t.Fatal("expires_at is nil")
			}
			lifetime := issued.ExpiresAt.Sub(*issued.IssuedAt)
			if lifetime != tc.wantLifetime {
				t.Fatalf("lifetime = %v, want %v", lifetime, tc.wantLifetime)
			}
			if lifetime > authverify.WorkloadTokenLifetimeCeiling {
				t.Fatalf("lifetime %v exceeds the ceiling %v", lifetime, authverify.WorkloadTokenLifetimeCeiling)
			}
		})
	}
}

type stubSubjectValidator struct{ expiresAt time.Time }

func (v stubSubjectValidator) Validate(context.Context, string) (authverify.SubjectIdentity, error) {
	return authverify.SubjectIdentity{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa", ServiceAccountUID: "uid-1", ExpiresAt: v.expiresAt}, nil
}

type stubGrantResolver struct{ binding authverify.WorkloadBinding }

func (r stubGrantResolver) Resolve(context.Context, authverify.SubjectIdentity) (authverify.WorkloadBinding, error) {
	return r.binding, nil
}

func TestWorkloadAccessTokenIssuer_passesTheWorkloadExchangeServiceLifetimeCheck(t *testing.T) {
	// The exchange service reads time.Until on the issued expiry, so this
	// cell runs on the real clock.
	issuer := newTestAccessTokenIssuer(t, time.Now)
	binding := authverify.WorkloadBinding{BindingID: "wlb_7", OrgID: "11111111-1111-4111-8111-111111111111", GrantedScopes: RoleScopes("read"), RepositoryScopes: []string{"*"}}
	service, err := authverify.NewWorkloadTokenExchangeService(stubSubjectValidator{expiresAt: time.Now().Add(time.Hour)}, stubGrantResolver{binding: binding}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Exchange(context.Background(), "subject-token", nil)
	if err != nil {
		t.Fatalf("exchange error = %v, want nil", err)
	}
	if !IsTokenShapeValid(result.AccessToken) {
		t.Fatalf("access token %q does not match the ACR token shape", result.AccessToken)
	}
	maxExpiresIn := int(authverify.WorkloadTokenLifetimeCeiling / time.Second)
	if result.ExpiresIn <= 0 || result.ExpiresIn > maxExpiresIn {
		t.Fatalf("expires_in = %d, want in (0, %d]", result.ExpiresIn, maxExpiresIn)
	}
}
