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

func TestHelpCommand(t *testing.T) {
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })

	if err := run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	var output bytes.Buffer
	_, _ = io.Copy(&output, reader)
	_ = reader.Close()
	if got := output.String(); !strings.Contains(got, "Usage: acr-api") {
		t.Fatalf("help output = %q", got)
	}
}

func TestVersionCommand(t *testing.T) {
	originalVersion, originalCommit, originalDate := version.Version, version.Commit, version.Date
	version.Version = "1.2.3-rc.1+build.7"
	version.Commit = "0123456789abcdef0123456789abcdef01234567"
	version.Date = "2026-07-12T15:04:05Z"
	t.Cleanup(func() { version.Version, version.Commit, version.Date = originalVersion, originalCommit, originalDate })
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	var output bytes.Buffer
	_, _ = io.Copy(&output, reader)
	_ = reader.Close()
	if got, want := strings.TrimSpace(output.String()), "1.2.3-rc.1+build.7 commit=0123456789abcdef0123456789abcdef01234567 built=2026-07-12T15:04:05Z"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestServeFailsClosedWhenPostgresIsUnavailable(t *testing.T) {
	t.Setenv("ACR_ENVIRONMENT", "production")
	t.Setenv("ACR_CLICKHOUSE_DSN", "clickhouse://configured")
	t.Setenv("ACR_POSTGRES_DSN", "postgres://configured?sslmode=verify-full")
	t.Setenv("ACR_EVIDENCE_ID_ACTIVE_KID", "current")
	t.Setenv("ACR_EVIDENCE_ID_KEYS", "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
	t.Setenv("ACR_DEV_HEALTH_ENTITLEMENT_URL", "https://ops.example.test")
	t.Setenv("ACR_DEVICE_VERIFICATION_URL", "https://verify.example.test/device")
	t.Setenv("ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE", "/run/secrets/ops-token")
	t.Setenv("ACR_POSTGRES_CONNECTION_KIND", "direct")

	err := serve(nil)

	if err == nil || !strings.Contains(err.Error(), "initialize PostgreSQL runtime") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "postgres://configured") {
		t.Fatalf("error leaked PostgreSQL DSN: %v", err)
	}
}

func TestServeRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	t.Setenv("ACR_ENVIRONMENT", "development")
	t.Setenv("ACR_TRUSTED_PROXY_CIDRS", "not-a-cidr")

	err := serve(nil)

	if err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
		t.Fatalf("error = %v", err)
	}
}
