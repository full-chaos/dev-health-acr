package main

import (
	"context"
	"database/sql"
	"testing"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/stretchr/testify/require"
)

func TestCredentialCLIRejectsRotationSourcesWithoutReplacementOverlapOrSuccessAudit(t *testing.T) {
	tests := []struct {
		name         string
		rotationOrg  string
		credentialID func(string) string
		revoke       bool
		wantAudits   int
	}{
		{
			name:         "revoked source",
			rotationOrg:  "11111111-1111-1111-1111-111111111111",
			credentialID: func(sourceID string) string { return sourceID },
			revoke:       true,
			wantAudits:   2,
		},
		{
			name:         "cross organization source",
			rotationOrg:  "33333333-3333-3333-3333-333333333333",
			credentialID: func(sourceID string) string { return sourceID },
			wantAudits:   1,
		},
		{
			name:         "unknown source",
			rotationOrg:  "11111111-1111-1111-1111-111111111111",
			credentialID: func(string) string { return "cred_unknown" },
			wantAudits:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			ctx := context.Background()
			dsn := newCredentialTestDatabase(t, ctx)
			environment := []string{"ACR_POSTGRES_DSN=" + dsn}
			created := runCredentialCLIProcess(t, environment, "credentials", "create",
				"--org-id", "11111111-1111-1111-1111-111111111111",
				"--repository-scope", "acme/widgets",
				"--scope", "context:read",
				"--name", "rotation-source",
				"--actor", "22222222-2222-2222-2222-222222222222")
			require.Zero(t, created.exitCode, created.stderr)
			sourceID := credentialIDFromList(t, environment)
			if test.revoke {
				revoked := runCredentialCLIProcess(t, environment, "credentials", "revoke",
					"--org-id", "11111111-1111-1111-1111-111111111111",
					"--credential-id", sourceID,
					"--actor", "22222222-2222-2222-2222-222222222222")
				require.Zero(t, revoked.exitCode, revoked.stderr)
			}
			db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn, AllowInsecure: true})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			var beforeExpires, beforeRevoked sql.NullTime
			require.NoError(t, db.QueryRowContext(ctx, "SELECT expires_at, revoked_at FROM acr.client_credentials WHERE credential_id = $1", sourceID).Scan(&beforeExpires, &beforeRevoked))

			// When
			result := runCredentialCLIProcess(t, environment, "credentials", "rotate",
				"--org-id", test.rotationOrg,
				"--credential-id", test.credentialID(sourceID),
				"--repository-scope", "acme/widgets",
				"--scope", "context:read",
				"--name", "unexpected-replacement",
				"--actor", "22222222-2222-2222-2222-222222222222",
				"--overlap", "1m")

			// Then
			require.NotZero(t, result.exitCode)
			require.Empty(t, result.stdout)
			require.NotContains(t, result.stderr, "fcacr_")
			require.NotContains(t, result.stderr, sourceID)
			var credentialCount, auditCount, rotationAudits int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.client_credentials WHERE org_id = $1", "11111111-1111-1111-1111-111111111111").Scan(&credentialCount))
			require.Equal(t, 1, credentialCount)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1", "11111111-1111-1111-1111-111111111111").Scan(&auditCount))
			require.Equal(t, test.wantAudits, auditCount)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_rotated'", "11111111-1111-1111-1111-111111111111").Scan(&rotationAudits))
			require.Zero(t, rotationAudits)
			var afterExpires, afterRevoked sql.NullTime
			require.NoError(t, db.QueryRowContext(ctx, "SELECT expires_at, revoked_at FROM acr.client_credentials WHERE credential_id = $1", sourceID).Scan(&afterExpires, &afterRevoked))
			require.Equal(t, beforeExpires, afterExpires)
			require.Equal(t, beforeRevoked, afterRevoked)
		})
	}
}
