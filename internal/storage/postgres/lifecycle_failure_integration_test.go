package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

const forcedLifecycleFailureName = "forced-lifecycle-failure"

func TestCredentialStore_MapsPostgresWriteFailuresThroughLifecycle(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(context.Context, *auth.Service) (string, error)
		run     func(context.Context, *auth.Service, string) error
		assert  func(*testing.T, context.Context, *sql.DB, string)
		want    int
	}{
		{
			name:    "create",
			prepare: func(context.Context, *auth.Service) (string, error) { return "", nil },
			run: func(ctx context.Context, service *auth.Service, _ string) error {
				_, err := service.Create(ctx, credentialCreateRequest(forcedLifecycleFailureName))
				return err
			},
			assert: func(*testing.T, context.Context, *sql.DB, string) {},
			want:   0,
		},
		{
			name: "rotate",
			prepare: func(ctx context.Context, service *auth.Service) (string, error) {
				source, err := service.Create(ctx, credentialCreateRequest("rotate-source"))
				return source.Credential.CredentialID, err
			},
			run: func(ctx context.Context, service *auth.Service, sourceID string) error {
				_, err := service.Rotate(ctx, auth.RotateCredentialRequest{
					OrgID: credentialTestOrgID, CredentialID: sourceID, Name: forcedLifecycleFailureName,
					CreatedBy: credentialTestActorID,
				})
				return err
			},
			assert: assertSourceIsNotRotated,
			want:   1,
		},
		{
			name: "revoke",
			prepare: func(ctx context.Context, service *auth.Service) (string, error) {
				source, err := service.Create(ctx, credentialCreateRequest(forcedLifecycleFailureName))
				return source.Credential.CredentialID, err
			},
			run: func(ctx context.Context, service *auth.Service, sourceID string) error {
				_, err := service.Revoke(ctx, credentialTestOrgID, sourceID, credentialTestActorID)
				return err
			},
			assert: assertSourceIsNotRevoked,
			want:   1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			ctx := context.Background()
			db := newCredentialStoreDatabase(t, ctx)
			audit, err := NewAuditStore(db)
			require.NoError(t, err)
			store, err := NewCredentialStore(db, audit)
			require.NoError(t, err)
			service, err := auth.NewService(store, auth.ServiceOptions{})
			require.NoError(t, err)
			sourceID, err := test.prepare(ctx, service)
			require.NoError(t, err)
			installLifecycleFailureTrigger(t, ctx, db)

			// When
			err = test.run(ctx, service, sourceID)

			// Then
			require.ErrorIs(t, err, storage.ErrUnavailable)
			require.NotContains(t, err.Error(), "forced database diagnostic")
			require.False(t, strings.Contains(err.Error(), "postgres"))
			assertCredentialAndAuditCounts(t, ctx, db, test.want, test.want)
			test.assert(t, ctx, db, sourceID)
		})
	}
}

func assertSourceIsNotRotated(t *testing.T, ctx context.Context, db *sql.DB, credentialID string) {
	t.Helper()
	var rotatedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, "SELECT rotated_at FROM acr.client_credentials WHERE credential_id = $1", credentialID).Scan(&rotatedAt))
	require.False(t, rotatedAt.Valid)
}

func assertSourceIsNotRevoked(t *testing.T, ctx context.Context, db *sql.DB, credentialID string) {
	t.Helper()
	var revokedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, "SELECT revoked_at FROM acr.client_credentials WHERE credential_id = $1", credentialID).Scan(&revokedAt))
	require.False(t, revokedAt.Valid)
}

func installLifecycleFailureTrigger(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
CREATE FUNCTION acr.fail_credential_lifecycle_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.name = 'forced-lifecycle-failure' THEN
    RAISE EXCEPTION 'forced database diagnostic';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER fail_credential_lifecycle_write
BEFORE INSERT OR UPDATE ON acr.client_credentials
FOR EACH ROW EXECUTE FUNCTION acr.fail_credential_lifecycle_write();`)
	require.NoError(t, err)
}
