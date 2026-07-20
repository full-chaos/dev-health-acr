package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphDegradation_Absent(t *testing.T) {
	provider, _, _ := newFixtureCodeGraphProvider(t)
	provider.runner.resolveExecutable = func(string) (string, error) { return "", errors.New("/private/codegraph") }
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorExecutableAbsent, LocalIndexFreshnessUnknown, "local_index_executable_absent")
}

func TestCodeGraphDegradation_MissingIndex(t *testing.T) {
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	require.NoError(t, os.Remove(filepath.Join(workspace.GitRoot, ".codegraph")))
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorMissing, LocalIndexFreshnessUnknown, "local_index_missing")
}

func TestCodeGraphDegradation_Mismatch(t *testing.T) {
	provider, _, commandLog := newFixtureCodeGraphProvider(t)
	provider.runner.Config.StalePolicy = LocalIndexStaleStrict
	writeFixtureStatusField(t, commandLog, "worktreeMismatch", []byte(`"other worktree"`))
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorStale, LocalIndexFreshnessStale, "local_worktree_mismatch")
}

func TestCodeGraphDegradation_Timeout(t *testing.T) {
	provider, _, _ := newFixtureCodeGraphProvider(t)
	setCodeGraphProviderScript(t, provider, "sleep 30")
	provider.runner.Config.Timeout = 100 * time.Millisecond
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorTimeout, LocalIndexFreshnessUnknown, "local_index_timeout")
}

func TestCodeGraphDegradation_Malformed(t *testing.T) {
	provider, _, _ := newFixtureCodeGraphProvider(t)
	setCodeGraphProviderScript(t, provider, "printf '{not-json}'")
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorMalformed, LocalIndexFreshnessUnknown, "local_index_malformed")
}

func TestCodeGraphDegradation_Oversized(t *testing.T) {
	provider, _, _ := newFixtureCodeGraphProvider(t)
	setCodeGraphProviderScript(t, provider, `printf '"'; head -c 1048577 /dev/zero | tr '\000' x; printf '"'`)
	assertCodeGraphCapabilitiesFailure(t, provider, LocalIndexErrorOversized, LocalIndexFreshnessUnknown, "local_index_oversized")
}

func TestCodeGraphDegradation_Unsupported(t *testing.T) {
	provider, workspace, commandLog := newFixtureCodeGraphProvider(t)
	setCodeGraphProviderScript(t, provider, "case \"$1\" in status) /bin/cat "+shellQuote(filepath.Join(filepath.Dir(commandLog), "fixtures", "status.json"))+" ;; *) exit 7 ;; esac")
	_, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})
	assertCodeGraphFailure(t, err, LocalIndexErrorUnsupportedCapability, LocalIndexFreshnessUnknown, "local_index_unsupported_capability")
}

func TestCodeGraphDegradation_ChangedFilesTruncated(t *testing.T) {
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	provider.workspace.ChangedFilesState = LocalChangedFilesTruncated
	workspace.ChangedFilesState = LocalChangedFilesTruncated
	capabilities, err := provider.Capabilities(context.Background())
	require.NoError(t, err)
	require.True(t, capabilities.Available)
	require.Equal(t, LocalIndexStatusDegraded, capabilities.Status)
	require.Equal(t, LocalIndexFreshnessStale, capabilities.Freshness)
	bundle, err := provider.ContextForTask(context.Background(), LocalContextRequest{TaskID: "CHAOS-3007", Goal: "safe", MaxItems: 1, MaxOutputTokens: 125, Workspace: &workspace})
	require.NoError(t, err)
	require.Contains(t, bundle.Warnings, "changed_files_truncated")
}

func assertCodeGraphCapabilitiesFailure(t *testing.T, provider *CodeGraphLocalIndexProvider, code LocalIndexErrorCode, freshness LocalIndexFreshness, warning string) {
	t.Helper()
	_, err := provider.Capabilities(context.Background())
	assertCodeGraphFailure(t, err, code, freshness, warning)
}

func assertCodeGraphFailure(t *testing.T, err error, code LocalIndexErrorCode, freshness LocalIndexFreshness, warning string) {
	t.Helper()
	var localErr *LocalIndexError
	require.ErrorAs(t, err, &localErr)
	require.Equal(t, code, localErr.Code())
	require.Equal(t, LocalIndexStatusUnavailable, localErr.Status())
	require.Equal(t, freshness, localErr.Freshness())
	require.Contains(t, localErr.Warnings(), warning)
	require.NotContains(t, localErr.Error(), "/private/")
}

func setCodeGraphProviderScript(t *testing.T, provider *CodeGraphLocalIndexProvider, body string) {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "codegraph")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\n"+body+"\n"), 0o700))
	provider.runner.Config.Executable = executable
	provider.runner.resolveExecutable = func(string) (string, error) { return executable, nil }
}
