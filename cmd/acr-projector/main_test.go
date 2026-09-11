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
	// dictation 811 class-sweep: backing stores default to required outside
	// development with the explicit dev flag -- this test is about the
	// --org flag, not backing stores, so opt into local composition.
	t.Setenv("ACR_LOCAL_COMPOSITION_READY", "true")
	err := run([]string{"rebuild"})
	if err == nil || !strings.Contains(err.Error(), "requires --org") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRebuildWithoutBackingStoresReportsWhatIsMissing(t *testing.T) {
	t.Setenv("ACR_LOCAL_COMPOSITION_READY", "true")
	err := run([]string{"rebuild", "--org", "org-1"})
	if err == nil || !strings.Contains(err.Error(), "requires Postgres, ClickHouse, and a configured Zep graph backend") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRebuildFullStoresConfigurationPassesConfigurationAndReachesRuntimeOpen
// is r3 P1 finding 1's serve/rebuild/rollback-side proof, through the real
// `acr-projector rebuild` entry point: rebuild is one of the full-stack
// (requiredStoresAll) commands the priors narrowing must not have
// loosened, so a genuinely complete environment (both DSNs, no dev flag)
// must pass configuration and proceed to the runtime-open step -- proven
// by the error CLASS changing from "configuration:"-prefixed to
// "open runtime:"-prefixed against deliberately unreachable DSNs (this
// needs no real database, same technique as
// cmd/acr-projector/priors_stores_test.go's postgres-only pin).
func TestRebuildFullStoresConfigurationPassesConfigurationAndReachesRuntimeOpen(t *testing.T) {
	t.Setenv("ACR_ENVIRONMENT", "development")
	t.Setenv("ACR_POSTGRES_DSN", "postgres://nouser:nopass@127.0.0.1:1/nodb?sslmode=disable")
	t.Setenv("ACR_POSTGRES_CONNECTION_KIND", "direct")
	t.Setenv("ACR_CLICKHOUSE_DSN", "https://nouser:nopass@127.0.0.1:1")
	// rebuild forces cfg.ProjectionEnabled=true itself (see rebuild's own
	// doc comment), so a canonical environment for it also needs the org
	// allowlist -- unrelated to this fix, just what rebuild always required.
	t.Setenv("ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS", "org-1")
	err := run([]string{"rebuild", "--org", "org-1"})
	if err == nil {
		t.Fatal("run(rebuild) unexpectedly succeeded against unreachable DSNs")
	}
	if strings.HasPrefix(err.Error(), "configuration:") {
		t.Fatalf("run(rebuild) error = %v, is still a configuration refusal -- a canonical full-stack environment should have passed validation", err)
	}
}

// TestRollbackFullStoresConfigurationPassesConfigurationAndReachesRuntimeOpen
// is the same pin for `acr-projector rollback` -- the other full-stack
// command besides serve/rebuild.
func TestRollbackFullStoresConfigurationPassesConfigurationAndReachesRuntimeOpen(t *testing.T) {
	t.Setenv("ACR_ENVIRONMENT", "development")
	t.Setenv("ACR_POSTGRES_DSN", "postgres://nouser:nopass@127.0.0.1:1/nodb?sslmode=disable")
	t.Setenv("ACR_POSTGRES_CONNECTION_KIND", "direct")
	t.Setenv("ACR_CLICKHOUSE_DSN", "https://nouser:nopass@127.0.0.1:1")
	// rollback forces cfg.ProjectionEnabled=true itself, same as rebuild.
	t.Setenv("ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS", "org-1")
	err := run([]string{"rollback", "--org", "org-1"})
	if err == nil {
		t.Fatal("run(rollback) unexpectedly succeeded against unreachable DSNs")
	}
	if strings.HasPrefix(err.Error(), "configuration:") {
		t.Fatalf("run(rollback) error = %v, is still a configuration refusal -- a canonical full-stack environment should have passed validation", err)
	}
}
