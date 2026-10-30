package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
	"github.com/stretchr/testify/require"
)

const workloadBindingTestOrgID = "33333333-3333-3333-3333-333333333333"

// TestCredentialStore_createCredentialRecordsIssuanceProvenanceInPostgresAudit
// is codex round 1's finding: acr.client_credentials.createCredential built
// its audit record without ever threading IssuanceProvenance through, so a
// workload_exchange (or device_authorization) credential's audit_events row
// silently lost that attribution in production, unlike the memory store's
// equivalent path.
func TestCredentialStore_createCredentialRecordsIssuanceProvenanceInPostgresAudit(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{})
	require.NoError(t, err)

	// workload_binding_id is a foreign key onto acr.workload_bindings.
	_, err = db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_audit_test', $1, 'cluster.local', 'ns', 'sa', 'uid', 'read', '["*"]'::jsonb, now(), 'test_actor')`, workloadBindingTestOrgID)
	require.NoError(t, err)

	issued, err := service.Create(ctx, auth.CreateCredentialRequest{
		OrgID: workloadBindingTestOrgID, Name: "workload:wlb_audit_test", RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: "wlb_audit_test", IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: "wlb_audit_test",
	})
	require.NoError(t, err)

	var metadataJSON []byte
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT metadata FROM acr.audit_events
WHERE action = 'credential_created' AND resource_id = $1`, issued.Credential.CredentialID).Scan(&metadataJSON))
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(metadataJSON, &metadata))
	require.Equal(t, "workload_exchange", metadata["issuance_provenance"])
}

func TestWorkloadBindingStore_lookupResolvesAnExactSeededRow(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_test_1', $1, 'cluster.local', 'panel-ns', 'panel-read', 'sa-uid-1', 'read', '["*"]'::jsonb, now(), 'test_actor')`, workloadBindingTestOrgID)
	require.NoError(t, err)

	store, err := NewWorkloadBindingStore(db)
	require.NoError(t, err)
	binding, err := store.Lookup(ctx, storage.WorkloadBindingKey{
		TrustDomain: "cluster.local", Namespace: "panel-ns", ServiceAccountName: "panel-read", ServiceAccountUID: "sa-uid-1",
	})
	require.NoError(t, err)
	require.Equal(t, "wlb_test_1", binding.BindingID)
	require.Equal(t, workloadBindingTestOrgID, binding.OrgID)
	require.Equal(t, "read", binding.Role)
	require.Equal(t, []string{"*"}, binding.RepositoryScopes)
	require.Nil(t, binding.DisabledAt)
}

func TestWorkloadBindingStore_lookupMissReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewWorkloadBindingStore(db)
	require.NoError(t, err)
	_, err = store.Lookup(ctx, storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa", ServiceAccountUID: "uid"})
	require.True(t, errors.Is(err, storage.ErrNotFound))
}

func TestWorkloadBindingStore_disabledBindingIsStillReturnedForTheResolverToReject(t *testing.T) {
	// The store itself is a plain lookup -- disabling is enforced by
	// authverify.GrantResolver, not here (see storageGrantResolver.Resolve).
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by, disabled_at)
VALUES ('wlb_test_2', $1, 'cluster.local', 'ns', 'sa', 'uid', 'ops', '["*"]'::jsonb, now(), 'test_actor', now())`, workloadBindingTestOrgID)
	require.NoError(t, err)

	store, err := NewWorkloadBindingStore(db)
	require.NoError(t, err)
	binding, err := store.Lookup(ctx, storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa", ServiceAccountUID: "uid"})
	require.NoError(t, err)
	require.NotNil(t, binding.DisabledAt)
}

func TestNewWorkloadCredentialPurger_deletesOnlyExpiredWorkloadRows(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_test_3', $1, 'cluster.local', 'ns', 'sa', 'uid', 'read', '["*"]'::jsonb, now(), 'test_actor')`, workloadBindingTestOrgID)
	require.NoError(t, err)

	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{})
	require.NoError(t, err)

	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	live := now.Add(time.Hour)
	_, err = service.Create(ctx, auth.CreateCredentialRequest{
		OrgID: workloadBindingTestOrgID, Name: "workload:wlb_test_3", RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: "wlb_test_3", ExpiresAt: &live, IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: "wlb_test_3",
	})
	require.NoError(t, err)
	// A second, non-workload credential with a past expiry must never be
	// touched by this purger -- only workload_binding_id IS NOT NULL rows
	// are eligible.
	ordinaryExpired, err := service.Create(ctx, auth.CreateCredentialRequest{
		OrgID: workloadBindingTestOrgID, Name: "ordinary", RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead}, CreatedBy: "test_actor",
	})
	require.NoError(t, err)
	// Force the ordinary credential's expiry into the past directly (Create
	// itself rejects a past ExpiresAt), simulating a row that aged out.
	_, err = db.ExecContext(ctx, `UPDATE acr.client_credentials SET expires_at = $1 WHERE credential_id = $2`, expired, ordinaryExpired.Credential.CredentialID)
	require.NoError(t, err)
	// A separate, ALREADY-expired workload row via a second exchange.
	expiredWorkload, err := service.Create(ctx, auth.CreateCredentialRequest{
		OrgID: workloadBindingTestOrgID, Name: "workload:wlb_test_3", RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: "wlb_test_3", ExpiresAt: &live, IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: "wlb_test_3",
	})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE acr.client_credentials SET expires_at = $1 WHERE credential_id = $2`, expired, expiredWorkload.Credential.CredentialID)
	require.NoError(t, err)

	purge := NewWorkloadCredentialPurger(db)
	purged, err := purge(ctx, now, 500)
	require.NoError(t, err)
	require.Equal(t, 1, purged)

	_, err = credentials.GetByID(ctx, workloadBindingTestOrgID, expiredWorkload.Credential.CredentialID)
	require.True(t, errors.Is(err, storage.ErrNotFound), "the expired workload row must be gone")
	_, err = credentials.GetByID(ctx, workloadBindingTestOrgID, ordinaryExpired.Credential.CredentialID)
	require.NoError(t, err, "an ordinary (non-workload) expired credential must never be purged by this function")
}

type exchangeSubjectValidator struct{ expiresAt time.Time }

func (v exchangeSubjectValidator) Validate(context.Context, string) (authverify.SubjectIdentity, error) {
	return authverify.SubjectIdentity{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa", ServiceAccountUID: "uid", ExpiresAt: v.expiresAt}, nil
}

type exchangeGrantResolver struct{ binding authverify.WorkloadBinding }

func (r exchangeGrantResolver) Resolve(context.Context, authverify.SubjectIdentity) (authverify.WorkloadBinding, error) {
	return r.binding, nil
}

func TestWorkloadAccessTokenIssuer_overPostgresKeepsTheLifetimeWithinTheCeiling(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{})
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_lifetime_test', $1, 'cluster.local', 'ns', 'sa', 'uid', 'read', '["*"]'::jsonb, now(), 'test_actor')`, workloadBindingTestOrgID)
	require.NoError(t, err)

	// A clock reading with the worst-case sub-microsecond tail: timestamptz
	// keeps microseconds, so the stored expiry differs from the issued one
	// only if the driver rounds up.
	now := func() time.Time { return time.Now().Truncate(time.Second).Add(999_999_999 * time.Nanosecond) }
	issuer, err := auth.NewWorkloadAccessTokenIssuer(service, now)
	require.NoError(t, err)
	binding := authverify.WorkloadBinding{BindingID: "wlb_lifetime_test", OrgID: workloadBindingTestOrgID, GrantedScopes: auth.RoleScopes("read"), RepositoryScopes: []string{"*"}}

	issued, err := issuer.Issue(ctx, binding, auth.RoleScopes("read"), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, issued.IssuedAt)
	require.NotNil(t, issued.ExpiresAt)
	require.LessOrEqual(t, issued.ExpiresAt.Sub(*issued.IssuedAt), authverify.WorkloadTokenLifetimeCeiling)

	stored, err := credentials.FindByTokenHash(ctx, auth.HashToken(issued.Token))
	require.NoError(t, err)
	require.NotNil(t, stored.ExpiresAt)
	require.LessOrEqual(t, stored.ExpiresAt.Sub(*issued.IssuedAt), authverify.WorkloadTokenLifetimeCeiling, "the stored (microsecond) expiry must not lengthen the lifetime")

	exchange, err := authverify.NewWorkloadTokenExchangeService(exchangeSubjectValidator{expiresAt: time.Now().Add(time.Hour)}, exchangeGrantResolver{binding: binding}, issuer)
	require.NoError(t, err)
	result, err := exchange.Exchange(ctx, "subject-token", nil)
	require.NoError(t, err)
	require.Greater(t, result.ExpiresIn, 0)
	require.LessOrEqual(t, result.ExpiresIn, int(authverify.WorkloadTokenLifetimeCeiling/time.Second))
}
