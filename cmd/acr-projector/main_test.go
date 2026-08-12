package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

func TestUnknownCommand(t *testing.T) {
	err := run([]string{"unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	fn()
	_ = writer.Close()
	var output bytes.Buffer
	_, _ = io.Copy(&output, reader)
	_ = reader.Close()
	return output.String()
}

func TestHelpCommand(t *testing.T) {
	var runErr error
	output := captureStdout(t, func() { runErr = run([]string{"--help"}) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(output, "Usage: acr-projector") {
		t.Fatalf("help output = %q", output)
	}
}

func TestVersionCommand(t *testing.T) {
	originalVersion, originalCommit, originalDate := version.Version, version.Commit, version.Date
	version.Version = "1.2.3-rc.1+build.7"
	version.Commit = "0123456789abcdef0123456789abcdef01234567"
	version.Date = "2026-07-12T15:04:05Z"
	t.Cleanup(func() { version.Version, version.Commit, version.Date = originalVersion, originalCommit, originalDate })

	var runErr error
	output := captureStdout(t, func() { runErr = run([]string{"version"}) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(output, "1.2.3-rc.1+build.7") || !strings.Contains(output, "0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("version output = %q", output)
	}
}

func TestVersionFlagShortcut(t *testing.T) {
	var runErr error
	output := captureStdout(t, func() { runErr = run([]string{"--version"}) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(output, version.Current().Version) {
		t.Fatalf("version output = %q", output)
	}
}

func TestRebuildRequiresOrgFlag(t *testing.T) {
	err := run([]string{"rebuild"})
	if err == nil || !strings.Contains(err.Error(), "requires --org") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRebuildWithoutBackingStoresReportsWhatIsMissing(t *testing.T) {
	err := run([]string{"rebuild", "--org", "org-1"})
	if err == nil || !strings.Contains(err.Error(), "requires Postgres, ClickHouse, and a configured Zep graph backend") {
		t.Fatalf("unexpected error: %v", err)
	}
}
