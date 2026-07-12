package sidecar

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This file covers ACR_API_CA_BUNDLE: the regular-file-only/no-symlink
// type check, and Config.Validate's parity check with loadCACertPool
// (api_client.go) -- the same bounded size ceiling and PEM-validity
// requirement, enforced without any network I/O, so acr-mcp doctor (which
// only ever calls LoadConfig) reports an unusable CA bundle instead of
// "ok".

// generateTestCACertPEM returns a minimal, real, self-signed certificate
// PEM block: not a placeholder string, so it exercises the actual
// AppendCertsFromPEM parity path Config.Validate now shares with
// loadCACertPool.
func generateTestCACertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "acr-config-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestLoadConfigCACertPathMustExist(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: "/does/not/exist/ca.pem",
	})); err == nil {
		t.Fatal("nonexistent CA bundle path was accepted")
	}
}

func TestLoadConfigCACertPathAcceptsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, generateTestCACertPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CACertPath != path {
		t.Fatalf("unexpected CA cert path: %q", cfg.CACertPath)
	}
}

func TestLoadConfigCACertPathRejectsDirectory(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: t.TempDir(),
	})); err == nil {
		t.Fatal("a directory CA bundle path was accepted")
	}
}

// TestLoadConfigCACertPathRejectsSymlink proves the CA bundle path check
// rejects a symlink even when it resolves to an otherwise-valid regular
// file: no symlink indirection is trusted for this security-sensitive
// path, closing off the class of TOCTOU attack where the link target is
// swapped between validation and the later read in loadCACertPool.
func TestLoadConfigCACertPathRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-ca.pem")
	if err := os.WriteFile(target, generateTestCACertPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ca-link.pem")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: link,
	})); err == nil {
		t.Fatal("a symlinked CA bundle path was accepted")
	}
}

// TestLoadConfigCACertPathRejectsOversizedBundle proves Config.Validate
// enforces the same maxCACertBundleBytes ceiling loadCACertPool does, so
// acr-mcp doctor (LoadConfig only, no Client) reports an oversized bundle
// as invalid instead of "ok".
func TestLoadConfigCACertPathRejectsOversizedBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	oversized := make([]byte, maxCACertBundleBytes+1)
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: path,
	})); err == nil {
		t.Fatal("an oversized CA bundle was accepted by Config.Validate")
	}
}

// TestLoadConfigCACertPathRejectsInvalidPEM proves Config.Validate
// performs the same AppendCertsFromPEM validity check loadCACertPool
// does: a regular file of the right size but unparseable content is
// rejected at LoadConfig time, not just when a Client is later
// constructed.
func TestLoadConfigCACertPathRejectsInvalidPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte("this is not PEM data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: path,
	})); err == nil {
		t.Fatal("a CA bundle with no valid PEM certificates was accepted by Config.Validate")
	}
}
