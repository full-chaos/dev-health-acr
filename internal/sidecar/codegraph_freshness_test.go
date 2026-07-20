package sidecar

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/stretchr/testify/require"
)

func TestCodeGraphFreshness_Fresh(t *testing.T) {
	provider, _, _ := newFixtureCodeGraphProvider(t)
	capabilities, err := provider.Capabilities(context.Background())
	require.NoError(t, err)
	require.Equal(t, LocalIndexStatusAvailable, capabilities.Status)
	require.Equal(t, LocalIndexFreshnessFresh, capabilities.Freshness)
}

func TestCodeGraphFreshness_GracefulStale(t *testing.T) {
	provider, _, commandLog := newFixtureCodeGraphProvider(t)
	writeFixtureStatusField(t, commandLog, "pendingChanges", []byte(`{"added":1,"modified":0,"removed":0}`))
	capabilities, err := provider.Capabilities(context.Background())
	require.NoError(t, err)
	require.Equal(t, LocalIndexStatusDegraded, capabilities.Status)
	require.Equal(t, LocalIndexFreshnessStale, capabilities.Freshness)
}

func TestCodeGraphFreshness_StrictStale(t *testing.T) {
	provider, _, commandLog := newFixtureCodeGraphProvider(t)
	provider.runner.Config.StalePolicy = LocalIndexStaleStrict
	writeFixtureStatusField(t, commandLog, "pendingChanges", []byte(`{"added":1,"modified":0,"removed":0}`))
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorStale, LocalIndexFreshnessStale, "local_index_stale")
}

func TestCodeGraphFreshness_Dirty(t *testing.T) {
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	writeFixtureStatusField(t, commandLog, "pendingChanges", []byte(`{"added":1,"modified":0,"removed":0}`))
	bundle, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})
	require.NoError(t, err)
	require.Contains(t, bundle.Warnings, "local_workspace_dirty")
}

func TestCodeGraphFreshness_UnknownCommit(t *testing.T) {
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	bundle, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})
	require.NoError(t, err)
	require.Contains(t, bundle.Warnings, "indexed_commit_unknown")
}

func TestLocalIndexError_accessorsAndCancellation(t *testing.T) {
	cause := context.Canceled
	err := newLocalIndexError(LocalIndexErrorCancelled, LocalIndexStatusUnavailable, LocalIndexFreshnessUnknown, []string{"local_index_cancelled"}, cause)
	require.Equal(t, LocalIndexErrorCancelled, err.Code())
	require.Equal(t, LocalIndexStatusUnavailable, err.Status())
	require.Equal(t, LocalIndexFreshnessUnknown, err.Freshness())
	require.Equal(t, []string{"local_index_cancelled"}, err.Warnings())
	require.ErrorIs(t, err, cause)
	require.NotContains(t, err.Error(), cause.Error())
}

func TestLocalIndexError_unwrapsCause(t *testing.T) {
	err := newLocalIndexError(LocalIndexErrorTimeout, LocalIndexStatusUnavailable, LocalIndexFreshnessUnknown, nil, context.DeadlineExceeded)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
}

func TestCodeGraphFreshness_ordersWarningsByMismatchStaleDirtyThenUnknownCommit(t *testing.T) {
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	writeFixtureStatusField(t, commandLog, "worktreeMismatch", []byte(`"other worktree"`))
	writeFixtureStatusField(t, commandLog, "pendingChanges", []byte(`{"added":1,"modified":0,"removed":0}`))
	bundle, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})
	require.NoError(t, err)
	require.Equal(t, []string{"local_worktree_mismatch", "local_index_stale", "local_workspace_dirty", "local_query_budget_exhausted", "indexed_commit_unknown"}, bundle.Warnings)
}

func TestCodeGraphFreshness_strictOmitsMismatchAndDirty(t *testing.T) {
	provider, _, commandLog := newFixtureCodeGraphProvider(t)
	provider.runner.Config.StalePolicy = LocalIndexStaleStrict
	writeFixtureStatusField(t, commandLog, "worktreeMismatch", []byte(`"other worktree"`))
	writeFixtureStatusField(t, commandLog, "pendingChanges", []byte(`{"added":1,"modified":0,"removed":0}`))
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorStale, LocalIndexFreshnessStale, "local_worktree_mismatch")
}

func TestCodeGraphFreshness_workspaceTruncationDoesNotMarkBundleTruncated(t *testing.T) {
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	provider.runner.Config.MaxItems = 12
	provider.runner.Config.MaxOutputTokens = 4000
	provider.workspace.ChangedFilesState = LocalChangedFilesTruncated
	workspace.ChangedFilesState = LocalChangedFilesTruncated
	bundle, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe", RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryState}, MaxItems: 12, MaxOutputTokens: 4000, Workspace: &workspace})
	require.NoError(t, err)
	require.Equal(t, LocalIndexStatusDegraded, bundle.Status)
	require.False(t, bundle.Truncated)
}

func TestLocalIndexError_redactsUnderlyingErrorText(t *testing.T) {
	err := newLocalIndexError(LocalIndexErrorMalformed, LocalIndexStatusUnavailable, LocalIndexFreshnessUnknown, nil, errors.New("/private/secret"))
	require.NotContains(t, err.Error(), "/private/secret")
}

func TestCodeGraphDegradation_postStatusCommandFailureIsUnsupported(t *testing.T) {
	err := localIndexFailure(errCodeGraphUnsupported)
	var localErr *LocalIndexError
	require.ErrorAs(t, err, &localErr)
	require.Equal(t, LocalIndexErrorUnsupportedCapability, localErr.Code())
}
