package pgstructurepriors

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// newPriorsTestDatabase mirrors pgclarification's own
// newClarificationTestDatabase exactly: a fresh testcontainers Postgres,
// migrated to head, scoped to one test via t.Cleanup.
func newPriorsTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
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
	require.NoError(t, runner.Up(ctx, db))
	return db
}

func oneEntry(value string) []contextfabric.StructurePriorEntry {
	return []contextfabric.StructurePriorEntry{
		{
			EntryID:      contextfabric.DeriveStructurePriorEntryID("org-priors-01", contractsv1.ContextFabricStructureNeedExpectedKind, "hash-0001", "", "", value),
			QuestionHash: "hash-0001", Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: value,
			SupportHumanPanel: 1, Rank: 0,
		},
	}
}

func TestGetActive_ColdStart_NoActiveVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)

	set, found, state, err := store.GetActive(ctx, "org-priors-cold")
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, contextfabric.PriorDegradationNone, state)
	require.Empty(t, set.Entries)
}

func TestPublishVersion_ThenFlip_ThenGetActive_ReturnsEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-01"

	entries := oneEntry("pull_request")
	version, err := store.PublishVersion(ctx, orgID, entries, "watermark-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.Equal(t, int64(1), version)

	// Not active yet -- publishing never flips (DP8(a)).
	_, found, _, err := store.GetActive(ctx, orgID)
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, store.FlipActiveVersion(ctx, orgID, nil, &version, "chris"))

	set, found, state, err := store.GetActive(ctx, orgID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contextfabric.PriorDegradationNone, state)
	require.Equal(t, version, set.Version)
	require.Len(t, set.Entries, 1)
	require.Equal(t, "pull_request", set.Entries[0].Value)
	require.False(t, set.Entries[0].Revoked)
	require.Equal(t, version, set.Entries[0].Version)
}

func TestPublishVersion_Twice_Increments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-increment"

	v1, err := store.PublishVersion(ctx, orgID, oneEntry("pull_request"), "wm-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	v2, err := store.PublishVersion(ctx, orgID, oneEntry("work_item"), "wm-2", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.Equal(t, v1+1, v2)
}

func TestFlipActiveVersion_WrongExpectedCurrent_Conflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-conflict"

	v1, err := store.PublishVersion(ctx, orgID, oneEntry("pull_request"), "wm-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.NoError(t, store.FlipActiveVersion(ctx, orgID, nil, &v1, "chris"))

	v2, err := store.PublishVersion(ctx, orgID, oneEntry("work_item"), "wm-2", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)

	wrongExpected := int64(999)
	err = store.FlipActiveVersion(ctx, orgID, &wrongExpected, &v2, "chris")
	require.Error(t, err)
	require.True(t, errors.Is(err, contextfabric.ErrPriorPointerConflict), "want ErrPriorPointerConflict, got %v", err)

	// Active version is UNCHANGED after the failed flip.
	set, found, _, err := store.GetActive(ctx, orgID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, v1, set.Version)
}

func TestFlipActiveVersion_NonexistentVersion_Refuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-nonexistent"

	ghost := int64(404)
	err = store.FlipActiveVersion(ctx, orgID, nil, &ghost, "chris")
	require.Error(t, err)
	require.True(t, errors.Is(err, contextfabric.ErrPriorVersionNotFound))
}

func TestFlipActiveVersion_RequiresRatifiedBy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-noratify"

	v1, err := store.PublishVersion(ctx, orgID, oneEntry("pull_request"), "wm-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	err = store.FlipActiveVersion(ctx, orgID, nil, &v1, "")
	require.Error(t, err, "flip with empty ratified-by must be refused (DP8(a))")
}

func TestRollbackActiveVersion_RestoresPrevious(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-rollback"

	v1, err := store.PublishVersion(ctx, orgID, oneEntry("pull_request"), "wm-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.NoError(t, store.FlipActiveVersion(ctx, orgID, nil, &v1, "chris"))
	v2, err := store.PublishVersion(ctx, orgID, oneEntry("work_item"), "wm-2", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.NoError(t, store.FlipActiveVersion(ctx, orgID, &v1, &v2, "chris"))

	set, found, _, err := store.GetActive(ctx, orgID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, v2, set.Version)

	require.NoError(t, store.RollbackActiveVersion(ctx, orgID, "chris"))

	set, found, _, err = store.GetActive(ctx, orgID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, v1, set.Version)
}

func TestRollbackActiveVersion_NoPrevious_Refuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-no-previous"

	v1, err := store.PublishVersion(ctx, orgID, oneEntry("pull_request"), "wm-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.NoError(t, store.FlipActiveVersion(ctx, orgID, nil, &v1, "chris"))

	err = store.RollbackActiveVersion(ctx, orgID, "chris")
	require.Error(t, err)
	require.True(t, errors.Is(err, contextfabric.ErrPriorVersionNotFound))
}

func TestRevokeEntry_MarksRevokedAcrossVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)
	orgID := "org-priors-revoke"

	entries := oneEntry("pull_request")
	entryID := entries[0].EntryID
	v1, err := store.PublishVersion(ctx, orgID, entries, "wm-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.NoError(t, store.FlipActiveVersion(ctx, orgID, nil, &v1, "chris"))

	require.NoError(t, store.RevokeEntry(ctx, orgID, entryID, "chris"))
	// Idempotent: revoking twice is not an error.
	require.NoError(t, store.RevokeEntry(ctx, orgID, entryID, "chris"))

	set, found, _, err := store.GetActive(ctx, orgID)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, set.Entries, 1)
	require.True(t, set.Entries[0].Revoked, "revoked entry must still be returned by GetActive, flagged Revoked=true")

	// A LATER version that re-proposes the SAME (org, member, question,
	// value) triple gets the SAME EntryID (deterministic derivation) and
	// stays revoked -- design brief §3.3's "targeted kills between
	// versions."
	v2, err := store.PublishVersion(ctx, orgID, entries, "wm-2", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.NoError(t, store.FlipActiveVersion(ctx, orgID, &v1, &v2, "chris"))
	set, found, _, err = store.GetActive(ctx, orgID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, set.Entries[0].Revoked, "the SAME entry_id must stay revoked in a NEWER version that re-proposes it")
}

func TestGetActive_OrgIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newPriorsTestDatabase(t, ctx)
	store, err := NewStore(db)
	require.NoError(t, err)

	v1, err := store.PublishVersion(ctx, "org-a", oneEntry("pull_request"), "wm-1", contextfabric.CurationRuleVersionV1)
	require.NoError(t, err)
	require.NoError(t, store.FlipActiveVersion(ctx, "org-a", nil, &v1, "chris"))

	_, found, _, err := store.GetActive(ctx, "org-b")
	require.NoError(t, err)
	require.False(t, found, "org-b must see NOTHING from org-a's own priors -- absolute org isolation")
}
