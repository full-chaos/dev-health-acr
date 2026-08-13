package pgmodelconfig_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelconfigcrypto"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelconfig"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// Requires Docker; run explicitly (not part of `go test ./...` in this
// session's gate policy):
//
//	go test ./internal/contextfabric/pgmodelconfig -run TestStore -v
func newModelConfigTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	return db
}

func testCipher(t *testing.T) *modelconfigcrypto.Cipher {
	t.Helper()
	key := make([]byte, modelconfigcrypto.KeyLength)
	for i := range key {
		key[i] = byte(i)
	}
	cipher, err := modelconfigcrypto.New(map[string][]byte{"k1": key}, "k1")
	require.NoError(t, err)
	return cipher
}

func testWriteRequest(model string) contractsv1.ContextFabricOrgModelConfigWriteRequest {
	return contractsv1.ContextFabricOrgModelConfigWriteRequest{
		SchemaVersion: contractsv1.ContextFabricOrgModelConfigWriteRequestSchema,
		Provider:      "acme-gateway",
		BaseURL:       "https://llm.acme-gateway.example/v1/",
		Model:         model,
		FallbackModel: "acme-large-fallback",
		Credential:    "sk-acme-live-a1b2c3d4e5f6wxyz",
	}
}

// TestStore_upsertThenGetRoundTripsMaskedCredential is AC-3775-4's
// end-to-end proof against real Postgres: the credential written on Upsert
// never comes back through Get.
func TestStore_upsertThenGetRoundTripsMaskedCredential(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	written := testWriteRequest("model-a")
	saved, err := store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, written)
	require.NoError(t, err)
	require.NotEqual(t, written.Credential, saved.CredentialMasked)

	got, err := store.GetOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"})
	require.NoError(t, err)
	require.Equal(t, saved.CredentialMasked, got.CredentialMasked)
	require.Equal(t, written.Provider, got.Provider)
	require.Equal(t, written.Model, got.Model)
}

// TestStore_credentialIsEncryptedAtRest is the direct, unambiguous
// AC-3775-4 proof: the raw database column never contains the plaintext
// credential, byte for byte.
func TestStore_credentialIsEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	written := testWriteRequest("model-a")
	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, written)
	require.NoError(t, err)

	var ciphertext []byte
	row := db.QueryRowContext(ctx, `SELECT credential_ciphertext FROM acr.context_fabric_org_model_config WHERE org_id = $1`, "org-a")
	require.NoError(t, row.Scan(&ciphertext))
	require.NotContains(t, string(ciphertext), written.Credential)
}

// TestStore_resolveReturnsDecryptedCredential proves the ONE seam that DOES
// return plaintext -- internal/contextfabric/modelruntimeresolver's only
// consumer -- actually decrypts correctly against a real round trip through
// Postgres (not just the in-memory Cipher unit tests).
func TestStore_resolveReturnsDecryptedCredential(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	written := testWriteRequest("model-a")
	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, written)
	require.NoError(t, err)

	resolved, ok, err := store.ResolveOrgModelConfig(ctx, "org-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, written.Credential, resolved.Credential)
}

// TestStore_orgIsolation is AC-3775-1/AC-3775-2's storage-level proof
// against real Postgres: two organizations' rows are fully independent.
func TestStore_orgIsolation(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, testWriteRequest("model-a"))
	require.NoError(t, err)
	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-b"}, testWriteRequest("model-b"))
	require.NoError(t, err)

	require.NoError(t, store.DeleteOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}))

	_, err = store.GetOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"})
	require.ErrorIs(t, err, contextfabric.ErrOrgModelConfigNotFound)

	gotB, err := store.GetOrgModelConfig(ctx, storage.Principal{OrgID: "org-b"})
	require.NoError(t, err)
	require.Equal(t, "model-b", gotB.Model)
}

// TestStore_ciphertextSwappedBetweenOrgRowsFailsToDecrypt is team-lead
// review requirement 2's exact probe, run against the real store and a real
// database: physically move org-a's stored ciphertext into org-b's row
// (what a bad migration or a hand-edited UPDATE would do) and prove
// ResolveOrgModelConfig refuses to decrypt it, rather than silently opening
// org-a's credential under org-b's identity.
func TestStore_ciphertextSwappedBetweenOrgRowsFailsToDecrypt(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, testWriteRequest("model-a"))
	require.NoError(t, err)
	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-b"}, testWriteRequest("model-b"))
	require.NoError(t, err)

	var ciphertextA []byte
	row := db.QueryRowContext(ctx, `SELECT credential_ciphertext FROM acr.context_fabric_org_model_config WHERE org_id = $1`, "org-a")
	require.NoError(t, row.Scan(&ciphertextA))

	_, err = db.ExecContext(ctx, `UPDATE acr.context_fabric_org_model_config SET credential_ciphertext = $1 WHERE org_id = $2`, ciphertextA, "org-b")
	require.NoError(t, err)

	_, _, resolveErr := store.ResolveOrgModelConfig(ctx, "org-b")
	require.Error(t, resolveErr, "a ciphertext swapped into a different organization's row must fail to decrypt")

	gotB, getErr := store.GetOrgModelConfig(ctx, storage.Principal{OrgID: "org-b"})
	require.NoError(t, getErr, "Get degrades to a masked placeholder rather than erroring the whole read")
	require.Equal(t, "unavailable", gotB.CredentialMasked)
}

// TestStore_upsertPreservesCreatedAtAdvancesUpdatedAt is AC-3775-5's
// storage-level timestamp proof: a second Upsert for the same org is a full
// replace that keeps the original created_at but advances updated_at.
// updated_at is display/informational only now -- see
// TestStore_upsertAdvancesGeneration for the actual cache-invalidation key
// internal/contextfabric/modelruntimeresolver reads (Codex round-2 nit: this
// doc comment and docs/operations.md previously still called updated_at the
// cache key, which stopped being true once generation (Codex round-1 finding
// F3) replaced it).
func TestStore_upsertPreservesCreatedAtAdvancesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	first, err := store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, testWriteRequest("model-a"))
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)
	second, err := store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, testWriteRequest("model-a-v2"))
	require.NoError(t, err)

	require.Equal(t, first.CreatedAt, second.CreatedAt)
	require.True(t, second.UpdatedAt.After(first.UpdatedAt))
	require.Equal(t, "model-a-v2", second.Model)
}

// TestStore_upsertAdvancesGeneration is the direct generation proof Codex
// round-2 asked for (previously only exercised indirectly, through the
// resolver's own tests against memorymodelconfig's counter, never against
// the real Postgres sequence-backed column). ResolveOrgModelConfig is the
// only method that surfaces Generation (contractsv1.ContextFabricOrgModelConfig,
// UpsertOrgModelConfig's return shape, deliberately does not -- it is an
// internal cache-key detail, not part of the public contract).
func TestStore_upsertAdvancesGeneration(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, testWriteRequest("model-a"))
	require.NoError(t, err)
	first, ok, err := store.ResolveOrgModelConfig(ctx, "org-a")
	require.NoError(t, err)
	require.True(t, ok)

	_, err = store.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, testWriteRequest("model-a-v2"))
	require.NoError(t, err)
	second, ok, err := store.ResolveOrgModelConfig(ctx, "org-a")
	require.NoError(t, err)
	require.True(t, ok)

	require.Greater(t, second.Generation, first.Generation, "a second upsert for the same org must strictly advance Generation")
}

// TestStore_getOrgWithNoConfiguration is AC-3775-3's storage-level proof:
// absence is ErrOrgModelConfigNotFound, never a generic error.
func TestStore_getOrgWithNoConfiguration(t *testing.T) {
	ctx := context.Background()
	db := newModelConfigTestDatabase(t, ctx)
	store, err := pgmodelconfig.NewStore(db, testCipher(t))
	require.NoError(t, err)

	_, err = store.GetOrgModelConfig(ctx, storage.Principal{OrgID: "org-never-configured"})
	require.ErrorIs(t, err, contextfabric.ErrOrgModelConfigNotFound)

	resolved, ok, err := store.ResolveOrgModelConfig(ctx, "org-never-configured")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, contextfabric.ResolvedOrgModelConfig{}, resolved)
}
