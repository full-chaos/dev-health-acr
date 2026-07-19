package sidecar

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphFreshness_Fresh(t *testing.T) {
	classification := classifyCodeGraphStatus(codeGraphStatus{})
	require.Equal(t, LocalIndexStatusAvailable, classification.Status)
	require.Equal(t, LocalIndexFreshnessFresh, classification.Freshness)
	require.Equal(t, []string{"indexed_commit_unknown"}, classification.Warnings)
}

func TestCodeGraphFreshness_GracefulStale(t *testing.T) {
	classification := classifyCodeGraphStatus(codeGraphStatus{PendingChanges: 1})
	require.Equal(t, LocalIndexStatusDegraded, classification.Status)
	require.Equal(t, LocalIndexFreshnessStale, classification.Freshness)
	require.Equal(t, []string{"local_index_stale", "local_workspace_dirty", "indexed_commit_unknown"}, classification.Warnings)
}

func TestCodeGraphFreshness_StrictStale(t *testing.T) {
	classification := classifyCodeGraphStatus(codeGraphStatus{PendingChanges: 1})
	require.True(t, classification.omit(LocalIndexStaleStrict))
}

func TestCodeGraphFreshness_Dirty(t *testing.T) {
	classification := classifyCodeGraphStatus(codeGraphStatus{PendingChanges: 1})
	require.Contains(t, classification.Warnings, "local_workspace_dirty")
}

func TestCodeGraphFreshness_UnknownCommit(t *testing.T) {
	classification := classifyCodeGraphStatus(codeGraphStatus{})
	require.Equal(t, []string{"indexed_commit_unknown"}, classification.Warnings)
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

func TestCodeGraphDegradation_Absent(t *testing.T) {
	require.Equal(t, LocalIndexErrorExecutableAbsent, localIndexErrorCodeFor(ErrCodeGraphUnavailable))
}
func TestCodeGraphDegradation_MissingIndex(t *testing.T) {
	require.Equal(t, LocalIndexErrorMissing, localIndexErrorCodeFor(errCodeGraphMissing))
}
func TestCodeGraphDegradation_Mismatch(t *testing.T) {
	require.Equal(t, LocalIndexErrorWorktreeMismatch, localIndexErrorCodeFor(errCodeGraphMismatch))
}
func TestCodeGraphDegradation_Timeout(t *testing.T) {
	require.Equal(t, LocalIndexErrorTimeout, localIndexErrorCodeFor(context.DeadlineExceeded))
}
func TestCodeGraphDegradation_Malformed(t *testing.T) {
	require.Equal(t, LocalIndexErrorMalformed, localIndexErrorCodeFor(errCodeGraphDecode))
}
func TestCodeGraphDegradation_Oversized(t *testing.T) {
	require.Equal(t, LocalIndexErrorOversized, localIndexErrorCodeFor(ErrCodeGraphOutputTooLarge))
}
func TestCodeGraphDegradation_Unsupported(t *testing.T) {
	require.Equal(t, LocalIndexErrorUnsupportedCapability, localIndexErrorCodeFor(errCodeGraphUnsupported))
}
func TestCodeGraphDegradation_ChangedFilesTruncated(t *testing.T) {
	classification := classifyCodeGraphWorkspace(LocalWorkspaceSnapshot{ChangedFilesState: LocalChangedFilesTruncated})
	require.Equal(t, []string{"changed_files_truncated"}, classification.Warnings)
}

func TestLocalIndexError_unwrapsCause(t *testing.T) {
	err := newLocalIndexError(LocalIndexErrorTimeout, LocalIndexStatusUnavailable, LocalIndexFreshnessUnknown, nil, context.DeadlineExceeded)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
}

func TestCodeGraphFreshness_ordersWarningsByMismatchStaleDirtyThenUnknownCommit(t *testing.T) {
	classification := classifyCodeGraphStatus(codeGraphStatus{WorktreeMismatch: true, ReindexRecommended: true, PendingChanges: 2})
	require.Equal(t, []string{"local_worktree_mismatch", "local_index_stale", "local_workspace_dirty", "indexed_commit_unknown"}, classification.Warnings)
}

func TestCodeGraphFreshness_strictOmitsMismatchAndDirty(t *testing.T) {
	classification := classifyCodeGraphStatus(codeGraphStatus{WorktreeMismatch: true, PendingChanges: 1})
	require.True(t, classification.omit(LocalIndexStaleStrict))
}

func TestCodeGraphFreshness_workspaceTruncationDoesNotMarkBundleTruncated(t *testing.T) {
	classification := classifyCodeGraphWorkspace(LocalWorkspaceSnapshot{ChangedFilesState: LocalChangedFilesTruncated})
	require.Equal(t, LocalIndexStatusDegraded, classification.Status)
	require.NotContains(t, classification.Warnings, string(LocalIndexErrorQueryBudgetExhausted))
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
