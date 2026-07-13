package entitlements_test

import (
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func clientConfig(t *testing.T, server *httptest.Server, overrides entitlements.Config) entitlements.Config {
	t.Helper()
	certificate, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	caFile := filepath.Join(t.TempDir(), "server.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return mergeConfig(entitlements.Config{
		BaseURL:          mustURL(t, server.URL),
		TokenFile:        writeToken(t, testToken),
		CACertPath:       caFile,
		Timeout:          time.Second,
		MaxResponseBytes: 2048,
		PositiveCacheTTL: time.Minute,
		NegativeCacheTTL: time.Second,
		CacheCapacity:    2,
	}, overrides)
}

func mergeConfig(base, overrides entitlements.Config) entitlements.Config {
	if overrides.BaseURL != nil {
		base.BaseURL = overrides.BaseURL
	}
	if overrides.TokenFile != "" {
		base.TokenFile = overrides.TokenFile
	}
	if overrides.Timeout != 0 {
		base.Timeout = overrides.Timeout
	}
	if overrides.MaxResponseBytes != 0 {
		base.MaxResponseBytes = overrides.MaxResponseBytes
	}
	if overrides.ProxyURL != nil {
		base.ProxyURL = overrides.ProxyURL
	}
	if overrides.CACertPath != "" {
		base.CACertPath = overrides.CACertPath
	}
	if overrides.PositiveCacheTTL != 0 {
		base.PositiveCacheTTL = overrides.PositiveCacheTTL
	}
	if overrides.NegativeCacheTTL != 0 {
		base.NegativeCacheTTL = overrides.NegativeCacheTTL
	}
	if overrides.CacheCapacity != 0 {
		base.CacheCapacity = overrides.CacheCapacity
	}
	return base
}

func writeToken(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	return parsed
}
