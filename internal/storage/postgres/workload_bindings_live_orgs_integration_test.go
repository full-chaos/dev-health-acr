package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// insertLiveOrgsRow writes a migration-0072 row: org_scope, role and scopes as given, org_id as given (nil = NULL).
func insertLiveOrgsRow(ctx context.Context, db *sql.DB, bindingID, scope, role, scopesJSON string, orgID any, disabled bool) error {
	var disabledAt any
	if disabled {
		disabledAt = time.Now().UTC()
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, org_scope, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by, disabled_at)
VALUES ($1, $2, $3, 'cluster.local', 'ns', 'sa', 'uid', $4, $5::jsonb, now(), 'test_actor', $6)`,
		bindingID, orgID, scope, role, scopesJSON, disabledAt)
	return err
}

func requirePgCode(t *testing.T, err error, code, constraint string) {
	t.Helper()
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "error = %v", err)
	require.Equal(t, code, pgErr.Code, "error = %v", err)
	require.Equal(t, constraint, pgErr.ConstraintName, "error = %v", err)
}

func TestWorkloadBindingsLiveOrgsConstraints(t *testing.T) {
	ctx := context.Background()
	for name, cell := range map[string]struct {
		scope, role, scopes string
		orgID               any
		code, constraint    string
	}{
		"a live_orgs row with the read role":      {"live_orgs", "read", `[]`, nil, "23514", "ck_acr_workload_bindings_live_orgs_revoke"},
		"a live_orgs row with the reconcile role": {"live_orgs", "reconcile", `[]`, nil, "23514", "ck_acr_workload_bindings_live_orgs_revoke"},
		"a live_orgs revoke row with a wildcard":  {"live_orgs", "revoke", `["*"]`, nil, "23514", "ck_acr_workload_bindings_live_orgs_revoke"},
		"a live_orgs row with an org_id":          {"live_orgs", "revoke", `[]`, workloadBindingTestOrgID, "23514", "ck_acr_workload_bindings_org_scope"},
		"an org row with no org_id":               {"org", "revoke", `[]`, nil, "23514", "ck_acr_workload_bindings_org_scope"},
		"an unknown org_scope":                    {"any_org", "revoke", `[]`, nil, "23514", "ck_acr_workload_bindings_org_scope"},
	} {
		t.Run(name, func(t *testing.T) {
			db := newCredentialStoreDatabase(t, ctx)
			err := insertLiveOrgsRow(ctx, db, "wlb_refused", cell.scope, cell.role, cell.scopes, cell.orgID, false)
			requirePgCode(t, err, cell.code, cell.constraint)
		})
	}
	t.Run("the revoke row with no scope is accepted", func(t *testing.T) {
		db := newCredentialStoreDatabase(t, ctx)
		require.NoError(t, insertLiveOrgsRow(ctx, db, "wlb_live", "live_orgs", "revoke", `[]`, nil, false))
	})
}

func TestWorkloadBindingsAtMostOneEnabledLiveOrgsRowPerIdentity(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	require.NoError(t, insertLiveOrgsRow(ctx, db, "wlb_live_1", "live_orgs", "revoke", `[]`, nil, false))
	err := insertLiveOrgsRow(ctx, db, "wlb_live_2", "live_orgs", "revoke", `[]`, nil, false)
	requirePgCode(t, err, "23505", "uq_acr_workload_bindings_live_orgs_identity")
	require.NoError(t, insertLiveOrgsRow(ctx, db, "wlb_live_3", "live_orgs", "revoke", `[]`, nil, true), "a disabled second row is outside the index")
	_, err = db.ExecContext(ctx, `UPDATE acr.workload_bindings SET disabled_at = now() WHERE binding_id = 'wlb_live_1'`)
	require.NoError(t, err)
	require.NoError(t, insertLiveOrgsRow(ctx, db, "wlb_live_4", "live_orgs", "revoke", `[]`, nil, false), "after the disable a new enabled row is accepted")
	_, err = db.ExecContext(ctx, `UPDATE acr.workload_bindings SET disabled_at = NULL WHERE binding_id = 'wlb_live_1'`)
	requirePgCode(t, err, "23505", "uq_acr_workload_bindings_live_orgs_identity")
	// The per-org key of migration 0070 still holds beside the live_orgs rows.
	require.NoError(t, insertWorkloadBindingRow(ctx, db, "wlb_org_a", workloadBindingTestOrgID))
	err = insertWorkloadBindingRow(ctx, db, "wlb_org_a_dup", workloadBindingTestOrgID)
	requirePgCode(t, err, "23505", "uq_acr_workload_bindings_identity_org")
}

func TestWorkloadBindingStore_lookupReadsTheOrgScope(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	require.NoError(t, insertLiveOrgsRow(ctx, db, "wlb_live", "live_orgs", "revoke", `[]`, nil, false))
	require.NoError(t, insertWorkloadBindingRow(ctx, db, "wlb_org_b", workloadBindingSecondOrgID))
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_org_a', $1, 'cluster.local', 'ns', 'sa', 'uid', 'revoke', '[]'::jsonb, now(), 'test_actor')`, workloadBindingTestOrgID)
	require.NoError(t, err, "a row written without org_scope gets 'org'")
	store, err := NewWorkloadBindingStore(db)
	require.NoError(t, err)
	bindings, err := store.Lookup(ctx, storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa", ServiceAccountUID: "uid"})
	require.NoError(t, err)
	require.Len(t, bindings, 3)
	got := [][3]string{}
	for _, b := range bindings {
		got = append(got, [3]string{b.BindingID, b.OrgID, b.OrgScope})
	}
	require.Equal(t, [][3]string{
		{"wlb_org_a", workloadBindingTestOrgID, storage.WorkloadOrgScopeOrg},
		{"wlb_org_b", workloadBindingSecondOrgID, storage.WorkloadOrgScopeOrg},
		{"wlb_live", "", storage.WorkloadOrgScopeLiveOrgs},
	}, got, "org rows by org_id, the live_orgs row last")
}

type liveOrgsPresence struct{ gone map[string]bool }

func (p liveOrgsPresence) OrganizationKnown(ctx context.Context, orgID string) error {
	return p.OrganizationKnownNow(ctx, orgID)
}

func (p liveOrgsPresence) OrganizationKnownNow(_ context.Context, orgID string) error {
	if p.gone[orgID] {
		return auth.ErrOrganizationGone
	}
	return nil
}

// The whole exchange over postgres: a live_orgs row issues a revoke-only
// credential in the selected organization, whose row names the binding.
func TestWorkloadExchange_liveOrgsBindingIssuesARevokeCredentialInTheSelectedOrg(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	require.NoError(t, insertLiveOrgsRow(ctx, db, "wlb_live", "live_orgs", "revoke", `[]`, nil, false))
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{})
	require.NoError(t, err)
	issuer, err := auth.NewWorkloadAccessTokenIssuer(service, time.Now)
	require.NoError(t, err)
	store, err := NewWorkloadBindingStore(db)
	require.NoError(t, err)
	resolver, err := auth.NewGrantResolver(store, liveOrgsPresence{gone: map[string]bool{workloadBindingSecondOrgID: true}})
	require.NoError(t, err)
	exchange, err := authverify.NewWorkloadTokenExchangeService(exchangeSubjectValidator{expiresAt: time.Now().Add(time.Hour)}, resolver, issuer)
	require.NoError(t, err)

	result, err := exchange.Exchange(auth.WithWorkloadOrgSelector(ctx, workloadBindingTestOrgID), "subject-token", nil)
	require.NoError(t, err)
	require.Equal(t, auth.RoleScopes("revoke"), result.Scope)
	stored, err := credentials.FindByTokenHash(ctx, auth.HashToken(result.AccessToken))
	require.NoError(t, err)
	require.Equal(t, workloadBindingTestOrgID, stored.OrgID)
	require.Equal(t, auth.RoleScopes("revoke"), stored.Scopes)
	require.Equal(t, []string{"*"}, stored.RepositoryScopes, "the fixed repository scope, not the row's")
	var bindingID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT workload_binding_id FROM acr.client_credentials WHERE credential_id = $1`, stored.CredentialID).Scan(&bindingID))
	require.Equal(t, "wlb_live", bindingID)

	var before int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.client_credentials`).Scan(&before))
	_, err = exchange.Exchange(auth.WithWorkloadOrgSelector(ctx, workloadBindingSecondOrgID), "subject-token", nil)
	require.ErrorIs(t, err, auth.ErrWorkloadOrgNotBound, "a gone organization is not bound")
	var after int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.client_credentials`).Scan(&after))
	require.Equal(t, before, after, "a refused exchange writes no credential row")
}
