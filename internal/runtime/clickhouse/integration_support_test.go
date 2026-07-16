package clickhouse

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
)

func integrationClient(t *testing.T) (*Client, Options) {
	t.Helper()
	dsn := os.Getenv("ACR_CLICKHOUSE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("ACR_CLICKHOUSE_INTEGRATION_DSN is required for the real ClickHouse integration test")
	}
	if os.Getenv("ACR_CLICKHOUSE_INTEGRATION_ISOLATED") != "1" {
		t.Skip("ACR_CLICKHOUSE_INTEGRATION_ISOLATED=1 is required before the integration test can target seeded data")
	}
	options := Options{DSN: dsn, TLS: integrationTLSConfig(t), QueryTimeout: 10 * time.Second}
	client, err := NewClickHouseQueryClientWithOptions(options)
	if err != nil {
		t.Fatalf("NewClickHouseQueryClientWithOptions() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})
	return client, options
}

func integrationTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	certificatePath := os.Getenv("ACR_CLICKHOUSE_INTEGRATION_CA_FILE")
	if certificatePath == "" {
		t.Fatal("ACR_CLICKHOUSE_INTEGRATION_CA_FILE is required to verify the real ClickHouse TLS certificate")
	}
	certificate, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatalf("read ClickHouse CA file: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certificate) {
		t.Fatal("parse ClickHouse CA file")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
}

func assertIntegrationServerRejectsMutation(t *testing.T, options Options) {
	t.Helper()

	// Given
	configured, err := clickhousedriver.ParseDSN(options.DSN)
	if err != nil {
		t.Fatalf("parse direct ClickHouse DSN: %v", err)
	}
	applyOptions(configured, options)
	connection, err := clickhousedriver.Open(configured)
	if err != nil {
		t.Fatalf("open direct ClickHouse connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	// When
	rows, err := connection.Query(context.Background(), "SELECT getSetting('readonly')")
	if err != nil {
		t.Fatalf("read enforced readonly setting: %v", err)
	}
	t.Cleanup(func() { _ = rows.Close() })
	if !rows.Next() {
		t.Fatalf("enforced readonly setting returned no rows: %v", rows.Err())
	}
	var readonly uint64
	if err := rows.Scan(&readonly); err != nil {
		t.Fatalf("scan enforced readonly setting: %v", err)
	}
	if readonly != 2 {
		t.Fatalf("getSetting('readonly') = %d, want server profile 2", readonly)
	}
	err = connection.Exec(context.Background(), "INSERT INTO ci_pipeline_runs (org_id) VALUES ('forbidden')")

	// Then
	if err == nil {
		t.Fatal("server accepted INSERT for the configured read-only user")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("server mutation error was cancellation: %v", err)
	}
	if fmt.Sprint(err) == "" {
		t.Fatal("server mutation returned an empty error")
	}
}
