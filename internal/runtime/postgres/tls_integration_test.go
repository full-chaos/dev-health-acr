package postgres

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestOpen_acceptsRealPostgreSQLOverVerifiedTLS proves that Open succeeds
// against a real PostgreSQL server presenting a certificate signed by the
// configured CA, connecting with sslmode=verify-full.
func TestOpen_acceptsRealPostgreSQLOverVerifiedTLS(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := newTLSPostgresFixture(t, ctx)
	dsn := fixture.dsn(t, fixture.caPath)

	// When
	db, err := Open(ctx, Config{DSN: dsn, PingTimeout: 30 * time.Second})

	// Then
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.PingContext(ctx))
}

// TestOpen_rejectsRealPostgreSQLWithWrongCA proves that Open refuses to
// connect (and therefore acr-api would exit before listening) when the
// configured CA does not match the server's actual certificate, even though
// the server is real and reachable.
func TestOpen_rejectsRealPostgreSQLWithWrongCA(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := newTLSPostgresFixture(t, ctx)
	wrongCAPath := writeUnrelatedCA(t)
	dsn := fixture.dsn(t, wrongCAPath)

	// When
	_, err := Open(ctx, Config{DSN: dsn, PingTimeout: 10 * time.Second})

	// Then
	require.Error(t, err)
	require.NotContains(t, err.Error(), fixture.password)
}

type tlsPostgresFixture struct {
	container testcontainers.Container
	host      string
	port      string
	user      string
	password  string
	database  string
	caPath    string
}

func (f tlsPostgresFixture) dsn(t *testing.T, caPath string) string {
	t.Helper()
	values := url.Values{"sslmode": []string{"verify-full"}, "sslrootcert": []string{caPath}}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?%s", f.user, f.password, f.host, f.port, f.database, values.Encode())
}

func newTLSPostgresFixture(t *testing.T, ctx context.Context) tlsPostgresFixture {
	t.Helper()
	certificate := newSelfSignedCertificate(t)
	directory := t.TempDir()
	certPath := filepath.Join(directory, "server.crt")
	keyPath := filepath.Join(directory, "server.key")
	for path, contents := range map[string][]byte{certPath: certificate.certPEM, keyPath: certificate.keyPEM} {
		require.NoError(t, os.WriteFile(path, contents, 0o644))
	}
	user, password, database := "acr", "acr-tls-password", "acr"
	startCmd := "cp /tls-src/server.crt /tls-src/server.key /var/lib/postgresql/ && " +
		"chown postgres:postgres /var/lib/postgresql/server.crt /var/lib/postgresql/server.key && " +
		"chmod 600 /var/lib/postgresql/server.key /var/lib/postgresql/server.crt && " +
		"exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/var/lib/postgresql/server.crt -c ssl_key_file=/var/lib/postgresql/server.key"
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:18-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env:          map[string]string{"POSTGRES_USER": user, "POSTGRES_PASSWORD": password, "POSTGRES_DB": database},
			Cmd:          []string{"sh", "-c", startCmd},
			Files: []testcontainers.ContainerFile{
				{HostFilePath: certPath, ContainerFilePath: "/tls-src/server.crt", FileMode: 0o644},
				{HostFilePath: keyPath, ContainerFilePath: "/tls-src/server.key", FileMode: 0o644},
			},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		require.NoError(t, container.Terminate(cleanupCtx))
	})
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	caPath := filepath.Join(directory, "ca.crt")
	require.NoError(t, os.WriteFile(caPath, certificate.caPEM, 0o644))
	return tlsPostgresFixture{
		container: container, host: host, port: port.Port(),
		user: user, password: password, database: database, caPath: caPath,
	}
}

type selfSignedCertificate struct {
	caPEM   []byte
	certPEM []byte
	keyPEM  []byte
}

func newSelfSignedCertificate(t *testing.T) selfSignedCertificate {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ACR PostgreSQL integration CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	require.NoError(t, err)
	return selfSignedCertificate{
		caPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}),
	}
}

func writeUnrelatedCA(t *testing.T) string {
	t.Helper()
	unrelated := newSelfSignedCertificate(t)
	path := filepath.Join(t.TempDir(), "wrong-ca.crt")
	require.NoError(t, os.WriteFile(path, unrelated.caPEM, 0o644))
	return path
}
