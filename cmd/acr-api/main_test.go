package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestUnknownCommand(t *testing.T) {
	err := run([]string{"unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVersionCommand(t *testing.T) {
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
	if strings.TrimSpace(output.String()) == "" {
		t.Fatal("version output was empty")
	}
}

func TestServeFailsClosedWithoutHostedRuntimeAdapters(t *testing.T) {
	t.Setenv("ACR_ENVIRONMENT", "production")
	t.Setenv("ACR_CLICKHOUSE_DSN", "clickhouse://configured")
	t.Setenv("ACR_POSTGRES_DSN", "postgres://configured")
	t.Setenv("ACR_EVIDENCE_ID_ACTIVE_KID", "current")
	t.Setenv("ACR_EVIDENCE_ID_KEYS", "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")

	err := serve(nil)

	if err == nil || !strings.Contains(err.Error(), "runtime adapters") {
		t.Fatalf("error = %v", err)
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
