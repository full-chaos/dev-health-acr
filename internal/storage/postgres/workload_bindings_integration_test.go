package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

const workloadBindingTestOrgID = "33333333-3333-3333-3333-333333333333"

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
	// auth.GrantResolver, not here (see storageGrantResolver.Resolve).
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
