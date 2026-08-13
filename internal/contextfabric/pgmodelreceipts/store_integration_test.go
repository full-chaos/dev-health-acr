package pgmodelreceipts_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelreceipts"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// Requires Docker; run explicitly (not part of `go test ./...` in this
// session's gate policy):
//
//	go test ./internal/contextfabric/pgmodelreceipts -run TestStore -v
func newReceiptsTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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

func testValidReceipt() contextfabric.ModelExecutionReceipt {
	now := time.Now().UTC()
	return contextfabric.ModelExecutionReceipt{
		Operation:        contextfabric.ModelOperationSynthesize,
		Provider:         "acme-gateway",
		Model:            "acme-large",
		ModelVersion:     "v1",
		PromptVersion:    "v5",
		SchemaVersion:    "v1",
		EvaluatorVersion: "v1",
		StartedAt:        now,
		CompletedAt:      now.Add(2 * time.Second),
		Attempts:         2,
		InputDigest:      "ae1608896372720b6ebb58261e0c0092c608324b0804bc99267c1753990faaa8",
		OutputDigest:     "29d61df009bd41b58a672932d8003edcdd299644c3276b2f77885bdbe3c9bf59",
		Usage:            contextfabric.ModelUsage{InputTokens: 120, OutputTokens: 45, TotalTokens: 165},
		FallbackUsed:     true,
		Outcome:          "fallback",
	}
}

// TestStore_recordModelExecutionPersistsEveryField is AC-3775-6's direct
// proof: a receipt is durably recorded with its organization, provider,
// model, outcome, fallback_used, and token usage all intact.
func TestStore_recordModelExecutionPersistsEveryField(t *testing.T) {
	ctx := context.Background()
	db := newReceiptsTestDatabase(t, ctx)
	store, err := pgmodelreceipts.NewStore(db)
	require.NoError(t, err)

	receipt := testValidReceipt()
	require.NoError(t, store.RecordModelExecution(ctx, storage.Principal{OrgID: "org-a"}, receipt))

	var orgID, provider, outcome string
	var fallbackUsed bool
	var payload []byte
	row := db.QueryRowContext(ctx, `
SELECT org_id, provider, outcome, fallback_used, payload
FROM acr.context_fabric_model_execution_receipts
WHERE org_id = $1`, "org-a")
	require.NoError(t, row.Scan(&orgID, &provider, &outcome, &fallbackUsed, &payload))
	require.Equal(t, "org-a", orgID)
	require.Equal(t, receipt.Provider, provider)
	require.Equal(t, receipt.Outcome, outcome)
	require.True(t, fallbackUsed)

	var decoded contextfabric.ModelExecutionReceipt
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Equal(t, receipt.Model, decoded.Model)
	require.Equal(t, receipt.Usage, decoded.Usage)
}

// TestStore_recordModelExecutionNeverLeaksACredentialField locks that the
// stored payload -- ModelExecutionReceipt marshaled verbatim -- structurally
// contains no credential field at all (AC-3775-4/AC-3770-5): there is
// nothing to redact because the contract never carries one.
func TestStore_recordModelExecutionNeverLeaksACredentialField(t *testing.T) {
	ctx := context.Background()
	db := newReceiptsTestDatabase(t, ctx)
	store, err := pgmodelreceipts.NewStore(db)
	require.NoError(t, err)

	require.NoError(t, store.RecordModelExecution(ctx, storage.Principal{OrgID: "org-a"}, testValidReceipt()))

	var payload []byte
	row := db.QueryRowContext(ctx, `SELECT payload FROM acr.context_fabric_model_execution_receipts WHERE org_id = $1`, "org-a")
	require.NoError(t, row.Scan(&payload))

	var fields map[string]any
	require.NoError(t, json.Unmarshal(payload, &fields))
	for key := range fields {
		require.NotContains(t, key, "credential")
		require.NotContains(t, key, "api_key")
		require.NotContains(t, key, "secret")
	}
}

// TestStore_orgIsolation proves two organizations' receipts are
// independently scoped and both durably recorded.
func TestStore_orgIsolation(t *testing.T) {
	ctx := context.Background()
	db := newReceiptsTestDatabase(t, ctx)
	store, err := pgmodelreceipts.NewStore(db)
	require.NoError(t, err)

	require.NoError(t, store.RecordModelExecution(ctx, storage.Principal{OrgID: "org-a"}, testValidReceipt()))
	require.NoError(t, store.RecordModelExecution(ctx, storage.Principal{OrgID: "org-b"}, testValidReceipt()))

	var countA, countB int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_model_execution_receipts WHERE org_id = $1`, "org-a").Scan(&countA))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_model_execution_receipts WHERE org_id = $1`, "org-b").Scan(&countB))
	require.Equal(t, 1, countA)
	require.Equal(t, 1, countB)
}
