package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

// TestVersionCommandPrintsFullBuildIdentity is the CHAOS-2926 regression
// lock for the release workflow's smoke-test expectation ($VERSION
// commit=$COMMIT built=$DATE, see .github/workflows/release.yml): plain
// `acr-mcp version` -- and its `--version`/`-version` aliases -- must print
// the exact same full build identity line `metadata` and `doctor` already
// expose as separate `version`/`commit`/`build_date` fields, not the bare
// version string alone.
func TestVersionCommandPrintsFullBuildIdentity(t *testing.T) {
	// Given
	originalVersion, originalCommit, originalDate := version.Version, version.Commit, version.Date
	version.Version = "1.2.3-rc.1+build.7"
	version.Commit = "0123456789abcdef0123456789abcdef01234567"
	version.Date = "2026-07-12T15:04:05Z"
	t.Cleanup(func() { version.Version, version.Commit, version.Date = originalVersion, originalCommit, originalDate })
	want := "1.2.3-rc.1+build.7 commit=0123456789abcdef0123456789abcdef01234567 built=2026-07-12T15:04:05Z"

	for _, alias := range []string{"version", "--version", "-version"} {
		t.Run(alias, func(t *testing.T) {
			// When
			code, output := captureStdout(t, func() int { return runCLI([]string{alias}) })

			// Then
			if code != 0 {
				t.Fatalf("runCLI([%q]) code = %d, want 0", alias, code)
			}
			if got := strings.TrimSpace(output); got != want {
				t.Fatalf("runCLI([%q]) output = %q, want %q", alias, got, want)
			}
		})
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// fn's own return value alongside everything it wrote to stdout.
func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	code := fn()
	_ = writer.Close()
	var output bytes.Buffer
	_, _ = io.Copy(&output, reader)
	_ = reader.Close()
	return code, output.String()
}
