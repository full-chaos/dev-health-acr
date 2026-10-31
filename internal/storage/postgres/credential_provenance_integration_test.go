package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	migrationfiles "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// CHAOS-12083: credential provenance in Postgres -- written at issuance,
// copied by rotation, immutable once set, and backfilled from the issuance
// records (migration 0058).

type provenanceFixture struct {
	db      *sql.DB
	now     time.Time
	service *auth.Service
	devices *DeviceAuthorizationStore
	creds   *storage.CredentialLifecycle
}

func newProvenanceFixture(t *testing.T, ctx context.Context) provenanceFixture {
	t.Helper()
	return newProvenanceFixtureOn(t, ctx, newCredentialStoreDatabase(t, ctx))
}

// newProvenanceFixtureOn builds the fixture over a given database pool.
func newProvenanceFixtureOn(t *testing.T, ctx context.Context, db *sql.DB) provenanceFixture {
	t.Helper()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	credentials, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	devices, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	return provenanceFixture{db: db, now: now, service: service, devices: devices, creds: credentials}
}

// redeemDevice approves and redeems one device authorization; a non-empty
// resource and client make it an OAuth exchange.
func (f provenanceFixture) redeemDevice(t *testing.T, ctx context.Context, suffix, resource, clientID string) contractsv1.ClientCredential {
	t.Helper()
	created, err := f.devices.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("provenance-device-" + suffix),
		UserCodeHash:   storage.HashUserCode("PROV" + suffix),
	})
	require.NoError(t, err)
	grant := postgresDeviceAuthorizationGrant()
	_, err = f.devices.Approve(ctx, created.UserCodeHash, grant)
	require.NoError(t, err)
	prepared, err := f.service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject,
		ExpiresAt: postgresPointerTime(f.now.Add(30 * 24 * time.Hour)),
		Resource:  resource, ClientID: clientID,
	})
	require.NoError(t, err)
	credential, err := f.devices.Redeem(ctx, created.DeviceCodeHash, prepared.StorageInput())
	require.NoError(t, err)
	return credential
}

type provenanceRow struct {
	class, approver, client sql.NullString
}

func readProvenanceRow(t *testing.T, ctx context.Context, db *sql.DB, credentialID string) provenanceRow {
	t.Helper()
	var row provenanceRow
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT client_class, approving_user_id, client_id FROM acr.client_credentials WHERE credential_id = $1`, credentialID).
		Scan(&row.class, &row.approver, &row.client))
	return row
}

func TestCredentialProvenance_issuanceWritesTheClassAndRotationCopiesIt(t *testing.T) {
	ctx := context.Background()
	f := newProvenanceFixture(t, ctx)
	const client = "acrc_0123456789abcdef0123456789abcdef"

	cli, err := f.service.Create(ctx, credentialCreateRequest("cli"))
	require.NoError(t, err)
	native := f.redeemDevice(t, ctx, "N1", "", "")
	oauth := f.redeemDevice(t, ctx, "O1", "https://mcp.example.test/mcp", client)
	rotated, err := f.service.Rotate(ctx, auth.RotateCredentialRequest{
		OrgID: oauth.OrgID, CredentialID: oauth.CredentialID, CreatedBy: oauth.CredentialID, Overlap: time.Minute,
	})
	require.NoError(t, err)

	require.Equal(t, provenanceRow{class: sql.NullString{String: "org_bearer", Valid: true}}, readProvenanceRow(t, ctx, f.db, cli.Credential.CredentialID),
		"a credential from the operator CLI is org_bearer with no approver and no client (D6530)")
	require.Equal(t, provenanceRow{
		class:    sql.NullString{String: "native_device", Valid: true},
		approver: sql.NullString{String: credentialTestActorID, Valid: true},
	}, readProvenanceRow(t, ctx, f.db, native.CredentialID))
	wantOAuth := provenanceRow{
		class:    sql.NullString{String: "oauth_user", Valid: true},
		approver: sql.NullString{String: credentialTestActorID, Valid: true},
		client:   sql.NullString{String: client, Valid: true},
	}
	require.Equal(t, wantOAuth, readProvenanceRow(t, ctx, f.db, oauth.CredentialID))
	require.Equal(t, wantOAuth, readProvenanceRow(t, ctx, f.db, rotated.Credential.CredentialID),
		"a rotation successor keeps its source's provenance, not the rotating actor")
	found, err := f.creds.FindByTokenHash(ctx, auth.HashToken(rotated.Token))
	require.NoError(t, err)
	require.Equal(t, oauth.Provenance, found.Provenance)
	// The class decision and its inputs are on the credential_created audit row.
	var class, approver, clientID sql.NullString
	require.NoError(t, f.db.QueryRowContext(ctx, `
SELECT metadata->>'client_class', metadata->>'approving_user_id', metadata->>'client_id' FROM acr.audit_events
WHERE action = 'credential_created' AND resource_id = $1`, oauth.CredentialID).Scan(&class, &approver, &clientID))
	require.Equal(t, "oauth_user", class.String)
	require.Equal(t, credentialTestActorID, approver.String)
	require.Equal(t, client, clientID.String)
}

func TestCredentialProvenance_isImmutableOnceSet(t *testing.T) {
	ctx := context.Background()
	f := newProvenanceFixture(t, ctx)
	native := f.redeemDevice(t, ctx, "I1", "", "")

	_, err := f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET approving_user_id = 'someone_else' WHERE credential_id = $1`, native.CredentialID)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "error = %v", err)
	require.Equal(t, "23514", pgErr.Code, "check_violation")
	_, err = f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET client_class = NULL WHERE credential_id = $1`, native.CredentialID)
	require.Error(t, err)

	// Lifecycle columns stay writable.
	_, err = f.service.Revoke(ctx, native.OrgID, native.CredentialID, credentialTestActorID)
	require.NoError(t, err)
	require.Equal(t, "native_device", readProvenanceRow(t, ctx, f.db, native.CredentialID).class.String)
}

func TestCredentialProvenance_backfillClassifiesOnlyProvenRows(t *testing.T) {
	ctx := context.Background()
	f := newProvenanceFixture(t, ctx)
	const client = "acrc_0123456789abcdef0123456789abcdef"
	_, err := f.db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_provenance', $1, 'cluster.local', 'ns', 'sa', 'uid', 'read', '["*"]'::jsonb, now(), 'test_actor')`, credentialTestOrgID)
	require.NoError(t, err)
	workload, err := f.service.Create(ctx, auth.CreateCredentialRequest{
		OrgID: credentialTestOrgID, Name: "workload:wlb_provenance", RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: "wlb_provenance", IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: "wlb_provenance",
	})
	require.NoError(t, err)
	native := f.redeemDevice(t, ctx, "B1", "", "")
	nativeSuccessor, err := f.service.Rotate(ctx, auth.RotateCredentialRequest{
		OrgID: native.OrgID, CredentialID: native.CredentialID, CreatedBy: native.CredentialID, Overlap: time.Minute,
	})
	require.NoError(t, err)
	oauth := f.redeemDevice(t, ctx, "B2", "https://mcp.example.test/mcp", client)
	cli, err := f.service.Create(ctx, credentialCreateRequest("legacy cli"))
	require.NoError(t, err)

	// Make every row a pre-0058 row: no provenance at all.
	_, err = f.db.ExecContext(ctx, `ALTER TABLE acr.client_credentials DISABLE TRIGGER trg_client_credentials_provenance_immutable`)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET client_class = NULL, approving_user_id = NULL, client_id = NULL`)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `ALTER TABLE acr.client_credentials ENABLE TRIGGER trg_client_credentials_provenance_immutable`)
	require.NoError(t, err)

	migration, err := migrationfiles.Files.ReadFile("0058_credential_provenance.sql")
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, string(migration))
	require.NoError(t, err, "the migration is idempotent")

	require.Equal(t, provenanceRow{class: sql.NullString{String: "workload", Valid: true}}, readProvenanceRow(t, ctx, f.db, workload.Credential.CredentialID))
	wantNative := provenanceRow{
		class:    sql.NullString{String: "native_device", Valid: true},
		approver: sql.NullString{String: credentialTestActorID, Valid: true},
	}
	require.Equal(t, wantNative, readProvenanceRow(t, ctx, f.db, native.CredentialID))
	require.Equal(t, wantNative, readProvenanceRow(t, ctx, f.db, nativeSuccessor.Credential.CredentialID), "a rotation successor inherits through the audit edge")
	gotOAuth := readProvenanceRow(t, ctx, f.db, oauth.CredentialID)
	require.Equal(t, "oauth_user", gotOAuth.class.String)
	require.Equal(t, credentialTestActorID, gotOAuth.approver.String)
	require.Equal(t, provenanceRow{}, readProvenanceRow(t, ctx, f.db, cli.Credential.CredentialID),
		"a CLI row has no issuance record to prove its class: 0058 leaves it unclassified")
}

// Migration 0063 adds org_bearer (D6530) and relabels the rows the CLI path
// wrote as operator before it: 0058 never backfills operator, and the CLI
// create is the only writer of it, so every operator row is a CLI credential.
func TestCredentialProvenance_orgBearerMigrationRelabelsCLIRowsAndKeepsImmutability(t *testing.T) {
	ctx := context.Background()
	f := newProvenanceFixture(t, ctx)
	cli, err := f.service.Create(ctx, credentialCreateRequest("pre-0063 cli"))
	require.NoError(t, err)
	cliSuccessor, err := f.service.Rotate(ctx, auth.RotateCredentialRequest{
		OrgID: cli.Credential.OrgID, CredentialID: cli.Credential.CredentialID, CreatedBy: "operator@example.test", Overlap: time.Minute,
	})
	require.NoError(t, err)
	native := f.redeemDevice(t, ctx, "M1", "", "")

	// Make the CLI rows pre-0063 rows: the class the 0058 code wrote.
	_, err = f.db.ExecContext(ctx, `ALTER TABLE acr.client_credentials DISABLE TRIGGER trg_client_credentials_provenance_immutable`)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET client_class = 'operator' WHERE credential_id = ANY($1)`,
		[]string{cli.Credential.CredentialID, cliSuccessor.Credential.CredentialID})
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `ALTER TABLE acr.client_credentials ENABLE TRIGGER trg_client_credentials_provenance_immutable`)
	require.NoError(t, err)

	migration, err := migrationfiles.Files.ReadFile("0063_credential_org_bearer.sql")
	require.NoError(t, err)
	for run := 1; run <= 2; run++ {
		_, err = f.db.ExecContext(ctx, string(migration))
		require.NoError(t, err, "run %d: the migration is idempotent", run)
	}

	wantOrgBearer := provenanceRow{class: sql.NullString{String: "org_bearer", Valid: true}}
	require.Equal(t, wantOrgBearer, readProvenanceRow(t, ctx, f.db, cli.Credential.CredentialID))
	require.Equal(t, wantOrgBearer, readProvenanceRow(t, ctx, f.db, cliSuccessor.Credential.CredentialID))
	require.Equal(t, "native_device", readProvenanceRow(t, ctx, f.db, native.CredentialID).class.String, "other classes are not touched")
	var operatorRows int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM acr.client_credentials WHERE client_class = 'operator'`).Scan(&operatorRows))
	require.Zero(t, operatorRows)

	var triggerState string
	require.NoError(t, f.db.QueryRowContext(ctx, `
SELECT tgenabled::text FROM pg_trigger WHERE tgname = 'trg_client_credentials_provenance_immutable'`).Scan(&triggerState))
	require.Equal(t, "O", triggerState, "the trigger is enabled again after the relabel")

	var pgErr *pgconn.PgError
	_, err = f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET client_class = 'workload' WHERE credential_id = $1`, cli.Credential.CredentialID)
	require.True(t, errors.As(err, &pgErr), "error = %v", err)
	require.Equal(t, "23514", pgErr.Code, "the class stays immutable once set")
	_, err = f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET client_class = NULL WHERE credential_id = $1`, cli.Credential.CredentialID)
	require.Error(t, err)

	// org_bearer behaves like workload: no approver and no client.
	_, err = f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET approving_user_id = 'someone' WHERE credential_id = $1`, cli.Credential.CredentialID)
	require.True(t, errors.As(err, &pgErr), "error = %v", err)
	require.Equal(t, "23514", pgErr.Code)
	_, err = f.db.ExecContext(ctx, `UPDATE acr.client_credentials SET client_id = 'acrc_0123456789abcdef0123456789abcdef' WHERE credential_id = $1`, cli.Credential.CredentialID)
	require.True(t, errors.As(err, &pgErr), "error = %v", err)
	require.Equal(t, "23514", pgErr.Code)
}
