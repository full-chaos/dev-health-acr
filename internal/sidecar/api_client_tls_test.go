package sidecar

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientRejectsUntrustedTLSAuthority(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached a server whose certificate should not be trusted")
	}))
	defer server.Close()
	cfg := newFixtureConfig(t, server)
	cfg.CACertPath = writeTLSFixtureFile(t, "unrelated-ca.pem", generateTestCACertPEM(t))
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, callErr := client.Capabilities(context.Background())

	// Then
	assertSanitizedTLSFailure(t, callErr, cfg)
}

func TestClientRejectsTLSHostnameMismatch(t *testing.T) {
	// Given
	server, caPEM := newTLSFixtureServer(t, "not-the-listener.example.test")
	defer server.Close()
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		APIBaseURL:          baseURL,
		CACertPath:          writeTLSFixtureFile(t, "trusted-ca.pem", caPEM),
		Timeout:             5 * time.Second,
		MaxResponseBytes:    defaultMaxResponseBytes,
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
		ClientName:          "test-sidecar",
		ClientVersion:       "1.0.0",
		SidecarVersion:      "1.0.0",
	}
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	// When
	_, callErr := client.Capabilities(context.Background())

	// Then
	assertSanitizedTLSFailure(t, callErr, cfg)
}

func assertSanitizedTLSFailure(t *testing.T, callErr error, cfg Config) {
	t.Helper()
	if callErr == nil {
		t.Fatal("expected TLS verification to reject the hosted API certificate")
	}
	if !errors.Is(callErr, ErrTransportUnavailable) {
		t.Fatalf("expected errors.Is to match ErrTransportUnavailable, got %v", callErr)
	}
	var apiErr *APIError
	if !errors.As(callErr, &apiErr) {
		t.Fatalf("expected an *APIError, got %v (%T)", callErr, callErr)
	}
	if strings.Contains(callErr.Error(), cfg.APIBaseURL.Host) || strings.Contains(callErr.Error(), cfg.CACertPath) || strings.Contains(callErr.Error(), "x509") {
		t.Fatalf("TLS failure exposed host, certificate path, or raw transport details: %v", callErr)
	}
}

func newTLSFixtureServer(t *testing.T, dnsName string) (*httptest.Server, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "acr-sidecar-tls-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached a server whose hostname should not verify")
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func writeTLSFixtureFile(t *testing.T, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
