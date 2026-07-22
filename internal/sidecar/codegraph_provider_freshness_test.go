package sidecar

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphProvider_ContextForTask_degradesForDecodedWorktreeMismatch(t *testing.T) {
	// Given
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	writeFixtureStatusField(t, commandLog, "worktreeMismatch", []byte(`"index built for another worktree"`))
	request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace}

	// When
	bundle, err := provider.ContextForTask(context.Background(), request)

	// Then
	require.NoError(t, err)
	require.NotEmpty(t, bundle.Evidence)
	require.Equal(t, LocalIndexStatusDegraded, bundle.Status)
	require.Equal(t, LocalIndexFreshnessStale, bundle.Freshness)
	require.Equal(t, []string{"local_worktree_mismatch", "local_query_budget_exhausted", "indexed_commit_unknown"}, bundle.Warnings)
	require.NotContains(t, bundle.Warnings, "index built for another worktree")
}

func TestCodeGraphProvider_StrictPolicyOmitsDecodedWorktreeMismatch(t *testing.T) {
	// Given
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	provider.runner.Config.StalePolicy = LocalIndexStaleStrict
	writeFixtureStatusField(t, commandLog, "worktreeMismatch", []byte(`"index built for another worktree"`))
	request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe local context", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace}

	// When
	_, capabilitiesErr := provider.Capabilities(context.Background())
	_, contextErr := provider.ContextForTask(context.Background(), request)

	// Then
	for _, err := range []error{capabilitiesErr, contextErr} {
		var localErr *LocalIndexError
		require.ErrorAs(t, err, &localErr)
		require.Equal(t, LocalIndexErrorStale, localErr.Code())
		require.Equal(t, LocalIndexFreshnessStale, localErr.Freshness())
		require.Equal(t, []string{"local_worktree_mismatch", "indexed_commit_unknown"}, localErr.Warnings())
		require.NotContains(t, localErr.Error(), "index built for another worktree")
	}
}

func TestCodeGraphProvider_StrictStaleCauseReflectsActualCondition(t *testing.T) {
	for _, test := range []struct {
		name         string
		configure    func(*CodeGraphLocalIndexProvider, *LocalWorkspaceSnapshot, string)
		wantMismatch bool
		wantStale    bool
	}{
		{name: "worktree mismatch", configure: func(_ *CodeGraphLocalIndexProvider, _ *LocalWorkspaceSnapshot, log string) {
			writeFixtureStatusField(t, log, "worktreeMismatch", []byte(`"other worktree"`))
		}, wantMismatch: true},
		{name: "pending", configure: func(_ *CodeGraphLocalIndexProvider, _ *LocalWorkspaceSnapshot, log string) {
			writeFixtureStatusField(t, log, "pendingChanges", []byte(`{"added":1,"modified":0,"removed":0}`))
		}, wantStale: true},
		{name: "reindex", configure: func(_ *CodeGraphLocalIndexProvider, _ *LocalWorkspaceSnapshot, log string) {
			writeFixtureStatusField(t, log, "index", []byte(`{"builtWithVersion":"1.2.0","reindexRecommended":true,"builtWithExtractionVersion":1,"currentExtractionVersion":1}`))
		}, wantStale: true},
		{name: "extraction", configure: func(_ *CodeGraphLocalIndexProvider, _ *LocalWorkspaceSnapshot, log string) {
			writeFixtureStatusField(t, log, "index", []byte(`{"builtWithVersion":"1.2.0","reindexRecommended":false,"builtWithExtractionVersion":1,"currentExtractionVersion":2}`))
		}, wantStale: true},
		{name: "workspace dirty and truncated", configure: func(_ *CodeGraphLocalIndexProvider, workspace *LocalWorkspaceSnapshot, log string) {
			workspace.ChangedFilesState = LocalChangedFilesTruncated
			writeFixtureStatusField(t, log, "pendingChanges", []byte(`{"added":1,"modified":0,"removed":0}`))
		}, wantStale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, _, commandLog := newFixtureCodeGraphProvider(t)
			provider.runner.Config.StalePolicy = LocalIndexStaleStrict
			test.configure(provider, &provider.workspace, commandLog)

			_, err := provider.Capabilities(context.Background())

			var localErr *LocalIndexError
			require.ErrorAs(t, err, &localErr)
			require.Equal(t, LocalIndexErrorStale, localErr.Code())
			require.Equal(t, LocalIndexFreshnessStale, localErr.Freshness())
			if test.wantMismatch {
				require.ErrorIs(t, err, errCodeGraphMismatch)
			} else {
				require.NotErrorIs(t, err, errCodeGraphMismatch)
			}
			if test.wantStale {
				require.ErrorIs(t, err, errCodeGraphStale)
			}
		})
	}
}

func TestCodeGraphStatus_decodesOnlyNullableStringWorktreeMismatch(t *testing.T) {
	for _, test := range []struct {
		name         string
		value        []byte
		wantMismatch bool
		wantErr      error
	}{
		{name: "null is not mismatched", value: []byte("null")},
		{name: "empty text is not mismatched", value: []byte(`""`)},
		{name: "nonempty text is mismatched", value: []byte(`"explanation"`), wantMismatch: true},
		{name: "boolean is malformed", value: []byte("true"), wantErr: errCodeGraphDecode},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			object := decodeJSONObject(t, localStatusPayload(t, readCodeGraphFixture(t, "status")))
			payload := appendStatusField(t, object, "worktreeMismatch", test.value)

			// When
			status, err := decodeCodeGraphStatus(payload)

			// Then
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantMismatch, status.WorktreeMismatch)
		})
	}
}

func TestCodeGraphStatus_marksAnyValidatedPendingChangeAsDirty(t *testing.T) {
	for _, test := range []struct {
		name    string
		pending []byte
		wantErr error
	}{
		{name: "added", pending: []byte(`{"added":1,"modified":0,"removed":0}`)},
		{name: "modified", pending: []byte(`{"added":0,"modified":1,"removed":0}`)},
		{name: "removed", pending: []byte(`{"added":0,"modified":0,"removed":1}`)},
		{name: "multiple maximum integers", pending: []byte(`{"added":` + strconv.Itoa(math.MaxInt) + `,"modified":` + strconv.Itoa(math.MaxInt) + `,"removed":0}`)},
		{name: "negative is malformed", pending: []byte(`{"added":-1,"modified":0,"removed":0}`), wantErr: errCodeGraphDecode},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			object := decodeJSONObject(t, localStatusPayload(t, readCodeGraphFixture(t, "status")))
			payload := appendStatusField(t, object, "pendingChanges", test.pending)

			// When
			status, err := decodeCodeGraphStatus(payload)

			// Then
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			classification := classifyCodeGraphStatus(status)
			require.Equal(t, LocalIndexFreshnessStale, classification.Freshness)
			require.Contains(t, classification.Warnings, "local_workspace_dirty")
		})
	}
}

func TestNormalizeCodeGraphWorkspace_truncatedPathsAreSortedAndDeduplicated(t *testing.T) {
	// Given
	_, workspace, _ := newFixtureCodeGraphProvider(t)
	workspace.ChangedFilesState = LocalChangedFilesTruncated
	workspace.ChangedFiles = []string{"internal/sidecar/z.go", "internal/sidecar/a.go", "internal/sidecar/z.go"}

	// When
	normalized, err := normalizeCodeGraphWorkspace(&workspace)

	// Then
	require.NoError(t, err)
	require.Equal(t, LocalChangedFilesTruncated, normalized.ChangedFilesState)
	require.Equal(t, []string{"internal/sidecar/a.go", "internal/sidecar/z.go"}, normalized.ChangedFiles)
}

func TestNormalizeCodeGraphWorkspace_completePathsRejectDuplicates(t *testing.T) {
	// Given
	_, workspace, _ := newFixtureCodeGraphProvider(t)
	workspace.ChangedFiles = []string{"internal/sidecar/a.go", "internal/sidecar/a.go"}

	// When
	_, err := normalizeCodeGraphWorkspace(&workspace)

	// Then
	require.ErrorIs(t, err, ErrInvalidLocalContextRequest)
}

func writeFixtureStatusField(t *testing.T, commandLog, field string, value []byte) {
	t.Helper()
	statusPath := filepath.Join(filepath.Dir(commandLog), "fixtures", "status.json")
	payload, err := os.ReadFile(statusPath)
	require.NoError(t, err)
	var status map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &status))
	status[field] = value
	payload, err = json.Marshal(status)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statusPath, payload, 0o600))
}
