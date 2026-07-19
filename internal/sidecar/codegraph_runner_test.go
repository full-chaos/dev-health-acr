package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphRunner_ReturnsStreamingJSONForFixedStatusCommand(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `printf '{"version":"1.2.0"}'`)

	// When
	result, err := runner.Status(context.Background())

	// Then
	require.NoError(t, err)
	require.JSONEq(t, `{"version":"1.2.0"}`, string(result))
}

func TestCodeGraphRunner_RejectsUntrustedExecutable(t *testing.T) {
	// Given
	runner := CodeGraphRunner{Config: LocalIndexConfig{Executable: filepath.Join(t.TempDir(), "codegraph")}}

	// When
	_, err := runner.Status(context.Background())

	// Then
	require.ErrorIs(t, err, ErrCodeGraphUnavailable)
	require.NotContains(t, err.Error(), runner.Config.Executable)
}

func TestCodeGraphRunner_RedactsPath(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "secret-codegraph")
	runner := CodeGraphRunner{Config: LocalIndexConfig{Executable: path}}

	// When
	_, err := runner.Status(context.Background())

	// Then
	require.ErrorIs(t, err, ErrCodeGraphUnavailable)
	require.NotContains(t, err.Error(), path)
}

func TestCodeGraphRunner_KillsOversizedOutput(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `printf '"'; head -c 1048577 /dev/zero | tr '\000' a; printf '"'`)

	// When
	_, err := runner.Status(context.Background())

	// Then
	require.ErrorIs(t, err, ErrCodeGraphOutputTooLarge)
}

func TestCodeGraphRunner_RejectsMalformedJSON(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `printf '{not-json}'`)

	// When
	_, err := runner.Status(context.Background())

	// Then
	require.ErrorIs(t, err, ErrCodeGraphUnavailable)
}

func TestCodeGraphRunner_KillsOnTimeout(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `sleep 10`)
	runner.Config.Timeout = 100 * time.Millisecond

	// When
	_, err := runner.Status(context.Background())

	// Then
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestCodeGraphRunner_DoesNotPassACRCredentialsToChild(t *testing.T) {
	// Given
	t.Setenv("ACR_API_TOKEN", "canary-secret")
	runner := newTestCodeGraphRunner(t, `test -z "$ACR_API_TOKEN" && printf '{}'`)

	// When
	_, err := runner.Status(context.Background())

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

func TestCodeGraphRunner_RejectsArbitraryArguments(t *testing.T) {
	// Given
	runner := newTestCodeGraphRunner(t, `printf '{}'`)

	// When
	_, err := runner.Run(context.Background(), "status", []string{"--unsafe"})

	// Then
	require.ErrorIs(t, err, ErrCodeGraphArgumentsRejected)
	require.False(t, errors.Is(err, context.Canceled))
	require.NotContains(t, err.Error(), strings.Join([]string{"--unsafe"}, ""))
}
