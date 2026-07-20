package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/stretchr/testify/require"
)

func TestCodeGraphProvider_ContextForTask_truncatedWorkspaceReturnsDegradedEvidence(t *testing.T) {
	for _, policy := range []LocalIndexStalePolicy{LocalIndexStaleGraceful, LocalIndexStaleStrict} {
		t.Run(string(policy), func(t *testing.T) {
			// Given
			provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
			provider.runner.Config.MaxItems = 12
			provider.runner.Config.MaxOutputTokens = 4000
			provider.runner.Config.StalePolicy = policy
			workspace.ChangedFilesState = LocalChangedFilesTruncated
			workspace.ChangedFiles = []string{"internal/sidecar/local_index.go", "internal/sidecar/codegraph_provider.go"}
			request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", RequestedCategories: []contractsv1.PacketCategory{contractsv1.CategoryState}, MaxItems: 12, MaxOutputTokens: 4000, Workspace: &workspace}

			// When
			bundle, err := provider.ContextForTask(context.Background(), request)

			// Then
			require.NoError(t, err)
			require.Equal(t, LocalIndexStatusDegraded, bundle.Status)
			require.Equal(t, LocalIndexFreshnessStale, bundle.Freshness)
			require.Contains(t, bundle.Warnings, "changed_files_truncated")
			require.False(t, bundle.Truncated)
			commands, readErr := os.ReadFile(commandLog)
			require.NoError(t, readErr)
			require.NotContains(t, string(commands), "affected --json")
			require.NotContains(t, string(commands), "files --json --filter")
		})
	}
}

func TestCodeGraphProvider_ContextForTask_notRequestedEmptyFilesSkipsAffected(t *testing.T) {
	// Given
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	workspace.ChangedFilesState = LocalChangedFilesNotRequested
	workspace.ChangedFiles = nil
	request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace}

	// When
	_, err := provider.ContextForTask(context.Background(), request)

	// Then
	require.NoError(t, err)
	commands, readErr := os.ReadFile(commandLog)
	require.NoError(t, readErr)
	require.NotContains(t, string(commands), "affected --json")
	require.NotContains(t, string(commands), "files --json --filter")
}

func TestNormalizeCodeGraphWorkspace_rejectsNotRequestedFilesWithoutExecutableError(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	workspace.ChangedFilesState = LocalChangedFilesNotRequested
	workspace.ChangedFiles = []string{"internal/sidecar/local_index.go"}

	// When
	_, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})

	// Then
	require.ErrorIs(t, err, ErrInvalidLocalContextRequest)
	var localErr *LocalIndexError
	require.True(t, errors.As(err, &localErr))
	require.NotEqual(t, LocalIndexErrorExecutableAbsent, localErr.Code())
}

func TestCodeGraphProvider_ContextForTask_worktreeMismatchStringHonorsPolicies(t *testing.T) {
	// Given
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	statusPath := filepath.Join(filepath.Dir(commandLog), "fixtures", "status.json")
	status, readErr := os.ReadFile(statusPath)
	require.NoError(t, readErr)
	require.NoError(t, os.WriteFile(statusPath, []byte(strings.Replace(string(status), `"worktreeMismatch": null`, `"worktreeMismatch": "different worktree"`, 1)), 0o600))
	request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace}

	// When
	graceful, gracefulErr := provider.ContextForTask(context.Background(), request)
	provider.runner.Config.StalePolicy = LocalIndexStaleStrict
	_, strictErr := provider.ContextForTask(context.Background(), request)

	// Then
	require.NoError(t, gracefulErr)
	require.Equal(t, LocalIndexStatusDegraded, graceful.Status)
	require.Equal(t, LocalIndexFreshnessStale, graceful.Freshness)
	require.Equal(t, []string{"local_worktree_mismatch", "local_query_budget_exhausted", "indexed_commit_unknown"}, graceful.Warnings)
	var localErr *LocalIndexError
	require.True(t, errors.As(strictErr, &localErr))
	require.Equal(t, LocalIndexErrorStale, localErr.Code())
	require.Equal(t, []string{"local_worktree_mismatch", "indexed_commit_unknown"}, localErr.Warnings())
}
