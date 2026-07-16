package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/stretchr/testify/require"
)

func TestRotateCredential_PreservesEarliestRevocationAndExpiry(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	occurredAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	earliest := occurredAt.Add(-time.Minute)
	later := occurredAt.Add(5 * time.Minute)

	for _, test := range []struct {
		name               string
		column             string
		previousValidUntil *time.Time
	}{
		{name: "revocation", column: "revoked_at", previousValidUntil: nil},
		{name: "expiry", column: "expires_at", previousValidUntil: &later},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			source, createErr := service.Create(ctx, credentialCreateRequest("case-"+test.name))
			require.NoError(t, createErr)
			_, err = db.ExecContext(ctx, "UPDATE acr.client_credentials SET "+test.column+" = $2 WHERE credential_id = $1", source.Credential.CredentialID, earliest)
			require.NoError(t, err)
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })
			replacement := rotationCaseReplacement(source.Credential, test.name, occurredAt)

			// When
			err = rotateCredential(ctx, tx, credentialTestOrgID, source.Credential.CredentialID, replacement, "hash-"+test.name, credentialTestActorID, test.previousValidUntil, occurredAt)
			require.NoError(t, err)
			require.NoError(t, tx.Commit())

			// Then
			var persisted time.Time
			require.NoError(t, db.QueryRowContext(ctx, "SELECT "+test.column+" FROM acr.client_credentials WHERE credential_id = $1", source.Credential.CredentialID).Scan(&persisted))
			require.True(t, earliest.Equal(persisted))
		})
	}
}

func rotationCaseReplacement(source contractsv1.ClientCredential, suffix string, createdAt time.Time) contractsv1.ClientCredential {
	return contractsv1.ClientCredential{
		SchemaVersion: contractsv1.ClientCredentialSchema, CredentialID: source.CredentialID + "-replacement-" + suffix,
		OrgID: source.OrgID, Name: "replacement-" + suffix, TokenPrefix: source.TokenPrefix,
		RepositoryScopes: source.RepositoryScopes, Scopes: source.Scopes, CreatedAt: createdAt,
	}
}
