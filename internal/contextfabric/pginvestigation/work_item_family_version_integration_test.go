package pginvestigation_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// This is a real PostgreSQL version-fence proof using the established store
// fixture. The answer is controlled test input, not a fresh tuple producer.
func TestWorkItemFamilyVersionFencesReuseWithoutChangingStoredResult(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	principal := storage.Principal{OrgID: "org-work-item-family-version"}
	setCheckpointWatermark(t, ctx, db, principal.OrgID, "linear", "wm-1")
	store := mustReuseStore(t, db, time.Hour)
	legacy := reusableResult("result_family_version_legacy", principal.OrgID, "How is the scoped cohort doing?")
	snapshot, err := store.SnapshotSourceWatermarks(ctx, principal.OrgID)
	require.NoError(t, err)
	epoch, err := store.SnapshotRebuildEpoch(ctx, principal.OrgID)
	require.NoError(t, err)
	versions := testReuseVersionAuthorities
	versions.QuestionFamilyVersion = "question-family.v2"
	require.NoError(t, store.Save(ctx, principal, legacy, snapshot, &epoch,
		contextfabric.TimeAxisKeyFor(legacy.Interpretation.TimeContext), testReuseRetrievalIdentity,
		testReusePromptVersions, versions, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))
	before, err := store.Get(ctx, principal, legacy.ResultID)
	require.NoError(t, err)

	currentKey := reuseKeyFor(legacy)
	require.Equal(t, "question-family.v3", currentKey.QuestionFamilyVersion)
	_, found, _, err := store.FindReusable(ctx, principal, currentKey)
	require.NoError(t, err)
	require.False(t, found, "v3 lookup must decline the stored v2 answer")
	legacyKey := currentKey
	legacyKey.QuestionFamilyVersion = "question-family.v2"
	matched, found, _, err := store.FindReusable(ctx, principal, legacyKey)
	require.NoError(t, err)
	require.True(t, found, "matching v2 control must establish the row is reuse eligible")
	require.Equal(t, legacy.ResultID, matched.Result.ResultID)

	current := legacy
	current.ResultID = "result_family_version_current"
	saveWithReuseSnapshot(t, ctx, store, principal, current)
	matched, found, _, err = store.FindReusable(ctx, principal, currentKey)
	require.NoError(t, err)
	require.True(t, found, "current v3 row must remain reusable")
	require.Equal(t, current.ResultID, matched.Result.ResultID)
	after, err := store.Get(ctx, principal, legacy.ResultID)
	require.NoError(t, err)
	require.Equal(t, before, after, "version fencing must leave the legacy stored carrier unchanged")
}
