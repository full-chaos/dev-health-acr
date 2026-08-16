package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphRunner_ReturnsStreamingJSONForFixedStatusCommand(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `printf '{"version":"1.2.0"}'`)

	// When
	result, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.NoError(t, err)
	require.JSONEq(t, `{"version":"1.2.0"}`, string(result))
}

func TestCodeGraphRunner_RejectsUntrustedExecutable(t *testing.T) {
	// Given
	runner := CodeGraphRunner{Config: LocalIndexConfig{Executable: filepath.Join(t.TempDir(), "codegraph")}}

	// When
	_, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.ErrorIs(t, err, errCodeGraphExecutableAbsent)
	require.NotContains(t, err.Error(), runner.Config.Executable)
}

func TestCodeGraphRunner_RedactsPath(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "secret-codegraph")
	runner := CodeGraphRunner{Config: LocalIndexConfig{Executable: path}}

	// When
	_, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.ErrorIs(t, err, ErrCodeGraphUnavailable)
	require.NotContains(t, err.Error(), path)
}

// TestCodeGraphRunner_ClassifiesAGenuineENOENTAsExecutableAbsent proves the
// CHAOS-3861 fix's wiring end to end, not just classifyCodeGraphSpawnError
// in isolation: unlike TestCodeGraphRunner_RejectsUntrustedExecutable and
// TestCodeGraphRunner_RedactsPath above (which fail inside
// CodeGraphRunner.executable()'s own pre-flight resolution, before ever
// reaching runCodeGraphJSON), this uses the resolveExecutable test seam to
// bypass that pre-flight check the way newTestCodeGraphRunner does, so the
// failure genuinely occurs at cmd.Start() inside runCodeGraphJSON -- the
// exact call site this fix changed. The path really does not exist, so
// this is real ENOENT, not a synthetic error, deterministic and safe to
// run in any CI environment.
func TestCodeGraphRunner_ClassifiesAGenuineENOENTAsExecutableAbsent(t *testing.T) {
	// Given
	missing := filepath.Join(t.TempDir(), "codegraph-does-not-exist")
	runner := CodeGraphRunner{
		Config:            LocalIndexConfig{Executable: missing, Timeout: 3 * time.Second},
		resolveExecutable: func(string) (string, error) { return missing, nil },
	}

	// When
	_, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.ErrorIs(t, err, errCodeGraphExecutableAbsent)
	require.ErrorIs(t, err, ErrCodeGraphUnavailable)
	require.NotErrorIs(t, err, errCodeGraphSpawnUnavailable, "a genuinely missing file must never be misclassified as a transient spawn failure")
}

func TestCodeGraphRunner_KillsOversizedOutput(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `printf '"'; head -c 1048577 /dev/zero | tr '\000' a; printf '"'`)

	// When
	_, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.ErrorIs(t, err, ErrCodeGraphOutputTooLarge)
}

func TestCodeGraphRunner_IgnoresWaitDelayAfterSuccessfulJSONDecode(t *testing.T) {
	// Given: the command emits valid JSON, then leaves a descendant running in
	// the same process group with stderr still attached so os/exec must wait
	// out cleanup and may return ErrWaitDelay even though the payload itself is
	// complete.
	runner := newTestCodeGraphRunner(t, `printf '{}'; sh -c 'exec sleep 30' >/dev/null &`)

	// When
	result, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(result))
}

func TestCodeGraphRunner_RejectsMalformedJSON(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `printf '{not-json}'`)

	// When
	_, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.ErrorIs(t, err, errCodeGraphDecode)
}

func TestCodeGraphRunner_KillsOnTimeout(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `sleep 10`)
	runner.Config.Timeout = 100 * time.Millisecond

	// When
	_, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestCodeGraphRunner_classifiesCommandExitWithoutLeakingCommandOutput(t *testing.T) {
	tests := []struct {
		name    string
		command func(CodeGraphRunner, context.Context, string) error
		wantErr error
	}{
		{
			name: "status missing",
			command: func(runner CodeGraphRunner, ctx context.Context, root string) error {
				_, err := runner.Status(ctx, root)
				return err
			},
			wantErr: errCodeGraphMissing,
		},
		{
			name: "query unsupported",
			command: func(runner CodeGraphRunner, ctx context.Context, root string) error {
				_, err := runner.Query(ctx, codeGraphQueryRequest{GitRoot: root, Search: "safe", Limit: 1})
				return err
			},
			wantErr: errCodeGraphUnsupported,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			runner := newTestCodeGraphRunner(t, `printf '/private/path' >&2; exit 7`)
			root := directGuardFixture(t)

			// When
			err := test.command(runner, t.Context(), root)

			// Then
			require.ErrorIs(t, err, test.wantErr)
			require.NotContains(t, err.Error(), "/private/path")
		})
	}
}

func TestCodeGraphRunner_preservesContextErrorWhenProcessCannotStart(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "cancelled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				return ctx, cancel
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			runner := newTestCodeGraphRunner(t, `printf '{}'`)
			ctx, cancel := test.context()
			defer cancel()

			// When
			_, err := runner.Status(ctx, directGuardFixture(t))

			// Then
			require.ErrorIs(t, err, test.wantErr)
			require.NotErrorIs(t, err, ErrCodeGraphUnavailable)
		})
	}
}

func TestCodeGraphRunner_DoesNotPassACRCredentialsToChild(t *testing.T) {
	// Given
	t.Setenv("ACR_API_TOKEN", "canary-secret")
	runner := newTestCodeGraphRunner(t, `test -z "$ACR_API_TOKEN" && printf '{}'`)

	// When
	_, err := runner.Status(context.Background(), directGuardFixture(t))

	// Then
	require.NoError(t, err)
}

func newTestCodeGraphRunner(t *testing.T, body string) CodeGraphRunner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codegraph")
	script := "#!/bin/sh\n" + body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return CodeGraphRunner{
		Config:            LocalIndexConfig{Executable: path, Timeout: 3 * time.Second},
		resolveExecutable: func(string) (string, error) { return path, nil },
	}
}

func TestCodeGraphRunner_TypedCommands_useFixedJSONArgvAndTrustedRoot(t *testing.T) {
	// Given
	runner, root, commandLog := newRecordingCodeGraphRunner(t)
	query := codeGraphQueryRequest{GitRoot: root, Search: "Assemble", Limit: 3}

	// When
	_, statusErr := runner.Status(t.Context(), root)
	_, queryErr := runner.Query(t.Context(), query)
	_, callersErr := runner.Callers(t.Context(), query)
	_, calleesErr := runner.Callees(t.Context(), query)
	_, impactErr := runner.Impact(t.Context(), query)
	_, affectedErr := runner.Affected(t.Context(), codeGraphAffectedRequest{GitRoot: root, Files: []string{"internal/sidecar/local_index.go"}})
	_, filesErr := runner.Files(t.Context(), codeGraphFilesRequest{GitRoot: root, Filter: "internal/sidecar", Pattern: "*.go"})

	// Then
	for _, err := range []error{statusErr, queryErr, callersErr, calleesErr, impactErr, affectedErr, filesErr} {
		require.NoError(t, err)
	}
	commands, err := os.ReadFile(commandLog)
	require.NoError(t, err)
	require.Equal(t, []string{
		root + "|status --json",
		root + "|query --json Assemble --limit 3",
		root + "|callers --json Assemble --limit 3",
		root + "|callees --json Assemble --limit 3",
		root + "|impact --json Assemble --depth 2",
		root + "|affected --json --stdin --depth 2",
		root + "|files --json --filter internal/sidecar --pattern *.go --max-depth 2 --no-metadata",
	}, strings.Split(strings.TrimSpace(string(commands)), "\n"))
}

func TestCodeGraphRunner_TypedCommands_rejectControlsAndUnsafePaths(t *testing.T) {
	// Given
	runner, root, commandLog := newRecordingCodeGraphRunner(t)

	// When
	_, queryErr := runner.Query(t.Context(), codeGraphQueryRequest{GitRoot: root, Search: "unsafe\nquery", Limit: 1})
	_, affectedErr := runner.Affected(t.Context(), codeGraphAffectedRequest{GitRoot: root, Files: []string{"/private/local.go"}})
	_, filesErr := runner.Files(t.Context(), codeGraphFilesRequest{GitRoot: root, Filter: "../unsafe"})

	// Then
	require.ErrorIs(t, queryErr, ErrCodeGraphArgumentsRejected)
	require.ErrorIs(t, affectedErr, ErrCodeGraphArgumentsRejected)
	require.ErrorIs(t, filesErr, ErrCodeGraphArgumentsRejected)
	_, err := os.Stat(commandLog)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func newRecordingCodeGraphRunner(t *testing.T) (CodeGraphRunner, string, string) {
	t.Helper()
	root, err := canonicalCodeGraphRoot(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".codegraph"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codegraph", "codegraph.db"), []byte("index"), 0o600))
	commandLog := filepath.Join(root, "commands.log")
	executable := filepath.Join(root, "codegraph")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\" >> " + strconv.Quote(commandLog) + "\nprintf '{}'\n"
	require.NoError(t, os.WriteFile(executable, []byte(script), 0o700))
	return CodeGraphRunner{Config: LocalIndexConfig{Executable: executable, Timeout: 3 * time.Second}, resolveExecutable: func(string) (string, error) { return executable, nil }}, root, commandLog
}
