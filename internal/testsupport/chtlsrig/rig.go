// Package chtlsrig starts a real ClickHouse server that only speaks native
// TLS (port 9440) behind an in-process latency proxy, so a test can count the
// TCP connections and TLS handshakes a client opens and time its statements at
// a chosen round-trip time. A container that does not start fails the test;
// nothing here skips.
package chtlsrig

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// ServerName is the name the rig's server certificate carries.
	ServerName   = "localhost"
	secureNative = "9440/tcp"
)

// Rig is a TLS ClickHouse behind a latency proxy.
type Rig struct {
	// DSN reaches the server through the proxy with certificate verification on.
	DSN string
	// CAPath is a PEM file holding the CA that signed the server certificate.
	CAPath string
	Proxy  *LatencyProxy
}

// Start runs the pinned ClickHouse image with a native TLS listener and puts a
// latency proxy of the given round-trip time in front of it.
func Start(tb testing.TB, ctx context.Context, rtt time.Duration) *Rig {
	tb.Helper()
	ca, certPEM, keyPEM := newCertificate(tb)
	directory := tb.TempDir()
	write := func(name string, contents []byte) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			tb.Fatal(err)
		}
		return path
	}
	certPath, keyPath := write("server.crt", certPEM), write("server.key", keyPEM)
	configPath := write("tls.xml", []byte(`<clickhouse>
  <tcp_port_secure>9440</tcp_port_secure>
  <openSSL>
    <server>
      <certificateFile>/etc/clickhouse-server/server.crt</certificateFile>
      <privateKeyFile>/etc/clickhouse-server/server.key</privateKeyFile>
      <verificationMode>none</verificationMode>
      <cacheSessions>true</cacheSessions>
      <disableProtocols>sslv2,sslv3</disableProtocols>
      <preferServerCiphers>true</preferServerCiphers>
    </server>
  </openSSL>
</clickhouse>`))
	password := randomHex(tb)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{secureNative},
			Env: map[string]string{"CLICKHOUSE_USER": "default", "CLICKHOUSE_PASSWORD": password},
			Files: []testcontainers.ContainerFile{
				{HostFilePath: certPath, ContainerFilePath: "/etc/clickhouse-server/server.crt", FileMode: 0o644},
				{HostFilePath: keyPath, ContainerFilePath: "/etc/clickhouse-server/server.key", FileMode: 0o644},
				{HostFilePath: configPath, ContainerFilePath: "/etc/clickhouse-server/config.d/tls.xml", FileMode: 0o644},
			},
			WaitingFor: wait.ForListeningPort(secureNative).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		if container != nil {
			_ = container.Terminate(context.Background())
		}
		tb.Fatalf("start TLS ClickHouse container: %v", err)
	}
	tb.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			tb.Errorf("terminate TLS ClickHouse container: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	port, err := container.MappedPort(ctx, secureNative)
	if err != nil {
		tb.Fatal(err)
	}
	backend := net.JoinHostPort(host, port.Port())
	waitReady(tb, ctx, backend, password, ca)
	proxy := NewLatencyProxy(tb, backend, rtt)
	dsn := (&url.URL{
		Scheme: "clickhouse", User: url.UserPassword("default", password),
		Host: proxy.Addr(), Path: "/default",
		RawQuery: url.Values{"secure": []string{"true"}, "tls_server_name": []string{ServerName}}.Encode(),
	}).String()
	return &Rig{DSN: dsn, CAPath: write("ca.pem", ca.pem), Proxy: proxy}
}

type certificateAuthority struct {
	pem   []byte
	roots *x509.CertPool
}

func waitReady(tb testing.TB, ctx context.Context, address, password string, ca certificateAuthority) {
	tb.Helper()
	connection, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{address}, Auth: clickhousedriver.Auth{Database: "default", Username: "default", Password: password},
		TLS: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: ca.roots, ServerName: ServerName}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pingErr := connection.Ping(ctx)
		if pingErr == nil {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("TLS ClickHouse not ready: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func randomHex(tb testing.TB) string {
	tb.Helper()
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func newCertificate(tb testing.TB) (certificateAuthority, []byte, []byte) {
	tb.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatal(err)
	}
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ACR rig CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		tb.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: ServerName},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{ServerName}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		tb.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		tb.Fatal("append rig CA")
	}
	return certificateAuthority{pem: caPEM, roots: roots},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
}
