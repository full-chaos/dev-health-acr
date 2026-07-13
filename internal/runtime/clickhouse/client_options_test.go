package clickhouse

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
)

func TestNewClickHouseQueryClientWithOptions_requires_verified_TLS(t *testing.T) {
	for _, dsn := range []string{
		"clickhouse://readonly@example.invalid:9000/default",
		"clickhouse://readonly@example.invalid:9440/default?secure=true&skip_verify=true",
	} {
		t.Run(dsn, func(t *testing.T) {
			// When
			_, err := NewClickHouseQueryClientWithOptions(Options{DSN: dsn})

			// Then
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("NewClickHouseQueryClientWithOptions() error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func TestApplyOptions_applies_default_execution_limits(t *testing.T) {
	// Given
	configured := &clickhousedriver.Options{}

	// When
	applyOptions(configured, Options{})

	// Then
	if configured.Settings["readonly"] != 1 || configured.Settings["max_execution_time"] != uint(10) || configured.Settings["max_result_rows"] != uint(1_000) || configured.Settings["max_bytes_to_read"] != uint64(16<<20) {
		t.Fatalf("query settings = %#v, want read-only execution limits", configured.Settings)
	}
}

func TestApplyOptions_preserves_DSN_settings_and_TLS_server_name(t *testing.T) {
	// Given
	dsnRoots := x509.NewCertPool()
	runtimeRoots := x509.NewCertPool()
	callerSettings := clickhousedriver.Settings{
		"custom_setting":     "preserve",
		"readonly":           0,
		"max_execution_time": uint(999),
	}
	configured := &clickhousedriver.Options{
		TLS:             &tls.Config{ServerName: "clickhouse.internal", RootCAs: dsnRoots},
		DialTimeout:     3 * time.Second,
		ReadTimeout:     4 * time.Second,
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 6 * time.Minute,
		Settings:        callerSettings,
	}

	// When
	applyOptions(configured, Options{TLS: &tls.Config{RootCAs: runtimeRoots}})

	// Then
	if configured.TLS.ServerName != "clickhouse.internal" || configured.TLS.RootCAs != runtimeRoots {
		t.Fatalf("TLS = %#v, want preserved DSN server name and runtime roots", configured.TLS)
	}
	if configured.DialTimeout != 3*time.Second || configured.ReadTimeout != 4*time.Second || configured.MaxOpenConns != 5 || configured.MaxIdleConns != 2 || configured.ConnMaxLifetime != 6*time.Minute {
		t.Fatalf("DSN connection settings were overwritten: %#v", configured)
	}
	if configured.Settings["custom_setting"] != "preserve" || configured.Settings["readonly"] != 1 || configured.Settings["max_execution_time"] != uint(10) {
		t.Fatalf("settings = %#v, want preserved caller settings plus enforced query limits", configured.Settings)
	}
	if callerSettings["readonly"] != 0 || callerSettings["max_execution_time"] != uint(999) {
		t.Fatalf("caller settings were mutated: %#v", callerSettings)
	}
}
