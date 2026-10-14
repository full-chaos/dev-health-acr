package main

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/stretchr/testify/require"
)

// A refused grant is planted for real: the runtime role is created and the
// declaration is NOT applied. The credential lookup then fails with Postgres's
// own refusal, and the error that reaches the log carries the class and the
// table name from the server's real message.
func TestARefusedGrantIsNamedByClassAndTableInTheCredentialLookupError(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_refused_grant_test", SkipGrant: true})
	audit, err := storagepostgres.NewAuditStore(database.Runtime)
	require.NoError(t, err)
	store, err := storagepostgres.NewCredentialStore(database.Runtime, audit)
	require.NoError(t, err)

	raw := make([]byte, 32)
	_, err = store.FindByTokenHash(ctx, auth.HashToken(auth.TokenPrefix+base64.RawURLEncoding.EncodeToString(raw)))
	require.Error(t, err)
	var class *storage.DependencyErrorClass
	require.True(t, errors.As(err, &class), "the lookup error carries a classification: %v", err)
	require.Equal(t, "42501", class.SQLState)
	require.Equal(t, "insufficient_privilege", class.Class)
	require.Equal(t, "client_credentials", class.Table)
}
