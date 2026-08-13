package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type clickHouseFixture struct {
	container testcontainers.Container
	dsn       string
	caPath    string
	stopOnce  sync.Once
	stopErr   error
}

type clickHouseSeedRequest struct {
	connection   driver.Conn
	readPassword string
}

type clickHouseScopeRequest struct {
	dsn   string
	roots *x509.CertPool
}

func newClickHouseFixture(t *testing.T, ctx context.Context) *clickHouseFixture {
	t.Helper()
	certificate := newTestCertificate(t)
	directory := t.TempDir()
	certPath := filepath.Join(directory, "server.crt")
	keyPath := filepath.Join(directory, "server.key")
	configPath := filepath.Join(directory, "tls.xml")
	for path, contents := range map[string][]byte{certPath: certificate.certPEM, keyPath: certificate.keyPEM} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := `<clickhouse>
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
</clickhouse>`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	adminPassword := randomFixtureSecret(t)
	readPassword := randomFixtureSecret(t)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "clickhouse/clickhouse-server:24.8", ExposedPorts: []string{"9440/tcp"},
			Env: map[string]string{"CLICKHOUSE_USER": "default", "CLICKHOUSE_PASSWORD": adminPassword, "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1"},
			Files: []testcontainers.ContainerFile{
				{HostFilePath: certPath, ContainerFilePath: "/etc/clickhouse-server/server.crt", FileMode: 0o644},
				{HostFilePath: keyPath, ContainerFilePath: "/etc/clickhouse-server/server.key", FileMode: 0o644},
				{HostFilePath: configPath, ContainerFilePath: "/etc/clickhouse-server/config.d/tls.xml", FileMode: 0o644},
			},
			WaitingFor: wait.ForListeningPort("9440/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		if container != nil {
			if terminateErr := container.Terminate(ctx); terminateErr != nil {
				t.Logf("terminate ClickHouse after startup failure: %v", terminateErr)
			}
		}
		t.Fatal(err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9440/tcp")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate.caPEM) {
		t.Fatal("append ClickHouse integration CA")
	}
	connection, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{net.JoinHostPort(host, port.Port())}, Auth: clickhousedriver.Auth{Database: "default", Username: "default", Password: adminPassword},
		TLS: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	// The port-listening wait strategy only proves the TLS socket is open; the
	// secure ClickHouse server can still reset the first handshake. Retry the
	// initial ping until the server accepts connections or the deadline elapses.
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		pingErr := connection.Ping(ctx)
		if pingErr == nil {
			break
		}
		if time.Now().After(pingDeadline) {
			t.Fatalf("clickhouse not ready for connections: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	seedClickHouse(t, ctx, clickHouseSeedRequest{connection: connection, readPassword: readPassword})
	var serverVersion string
	if err := connection.QueryRow(ctx, "SELECT version()").Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(serverVersion, "24.8.") {
		t.Fatalf("ClickHouse version = %q, want 24.8.x", serverVersion)
	}
	dsn := (&url.URL{
		Scheme: "clickhouse", User: url.UserPassword("acr_readonly", readPassword),
		Host: net.JoinHostPort(host, port.Port()), Path: "/default",
		RawQuery: url.Values{"secure": []string{"true"}, "tls_server_name": []string{"localhost"}}.Encode(),
	}).String()
	fixture := &clickHouseFixture{container: container, dsn: dsn, caPath: writeRestrictedFixture(t, "clickhouse-ca.pem", certificate.caPEM)}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := fixture.Stop(cleanupCtx); err != nil {
			t.Error(err)
		}
	})
	assertClickHouseScopeQuery(t, ctx, clickHouseScopeRequest{dsn: dsn, roots: roots})
	assertClickHouseServerRejectsMutation(t, ctx, clickHouseScopeRequest{dsn: dsn, roots: roots})
	return fixture
}

func assertClickHouseScopeQuery(t *testing.T, ctx context.Context, request clickHouseScopeRequest) {
	t.Helper()
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: request.dsn, TLS: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: request.roots},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := client.Query(ctx, `SELECT toString(id), repo, ifNull(ref, '') FROM repos FINAL WHERE org_id = {org_id:String} AND repo = {repo_slug:String} LIMIT 2`, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: hostedIntegrationOrg}, {Name: "repo_slug", Value: hostedIntegrationRepository},
	})
	if err != nil {
		t.Fatalf("runtime scope query: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	if !rows.Next() {
		t.Fatalf("runtime scope query returned no row: %v", rows.Err())
	}
	var repositoryID, repository, branch string
	if err := rows.Scan(&repositoryID, &repository, &branch); err != nil {
		t.Fatal(err)
	}
	if repositoryID == "" || repository != hostedIntegrationRepository || branch != "main" {
		t.Fatalf("runtime scope row = %q %q %q", repositoryID, repository, branch)
	}
}

func assertClickHouseServerRejectsMutation(t *testing.T, ctx context.Context, request clickHouseScopeRequest) {
	t.Helper()
	options, err := clickhousedriver.ParseDSN(request.dsn)
	if err != nil {
		t.Fatalf("parse readonly ClickHouse DSN: %v", err)
	}
	options.TLS = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: request.roots, ServerName: "localhost"}
	connection, err := clickhousedriver.Open(options)
	if err != nil {
		t.Fatalf("open readonly ClickHouse connection: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := connection.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	var readonly uint8
	if err := connection.QueryRow(ctx, "SELECT getSetting('readonly')").Scan(&readonly); err != nil {
		t.Fatalf("read configured ClickHouse readonly setting: %v", err)
	}
	if readonly != 2 {
		t.Fatalf("getSetting('readonly') = %d, want server profile 2", readonly)
	}
	err = connection.Exec(ctx, "INSERT INTO ci_pipeline_runs (run_id, repo_id, branch, status, started_at, finished_at) VALUES ('forbidden', '00000000-0000-0000-0000-000000000001', 'main', 'failure', now64(3), now64(3))")
	var exception *clickhousedriver.Exception
	if err == nil || !errors.As(err, &exception) || exception.Code != 164 {
		t.Fatalf("readonly ClickHouse mutation error = %v, want ClickHouse READONLY code 164", err)
	}
}

func seedClickHouse(t *testing.T, ctx context.Context, request clickHouseSeedRequest) {
	t.Helper()
	// Rendered from the shared declaration (CHAOS-3781 round-4 R4-3).
	// These two were hand-written and diverged from production -- repos
	// omitted most of its columns and both dropped the ReplacingMergeTree
	// VERSION column, so FINAL here deduped differently than it does in
	// production. The closure sweep in devhealthschema now fails the build
	// if any test re-authors DDL for a declared table.
	for _, statement := range devhealthschema.DDL("repos", "ci_pipeline_runs") {
		if err := request.connection.Exec(ctx, statement); err != nil {
			t.Fatalf("execute ClickHouse fixture statement: %v", err)
		}
	}
	if err := request.connection.Exec(ctx, `INSERT INTO repos (id, org_id, repo, ref) VALUES (?, ?, ?, ?)`, "00000000-0000-0000-0000-000000000001", hostedIntegrationOrg, hostedIntegrationRepository, "main"); err != nil {
		t.Fatal(err)
	}
	if err := request.connection.Exec(ctx, `INSERT INTO ci_pipeline_runs (run_id, repo_id, branch, status, started_at, finished_at) VALUES (?, ?, ?, ?, now64(3), now64(3))`, "run-4821", "00000000-0000-0000-0000-000000000001", "main", "failure"); err != nil {
		t.Fatal(err)
	}
	if err := request.connection.Exec(ctx, `CREATE USER acr_readonly IDENTIFIED WITH plaintext_password BY {password:String}`, clickhousedriver.Named("password", request.readPassword)); err != nil {
		t.Fatal(err)
	}
	if err := request.connection.Exec(ctx, `GRANT SELECT ON default.* TO acr_readonly`); err != nil {
		t.Fatal(err)
	}
	if err := request.connection.Exec(ctx, `GRANT INSERT ON default.ci_pipeline_runs TO acr_readonly`); err != nil {
		t.Fatal(err)
	}
	if err := request.connection.Exec(ctx, `CREATE SETTINGS PROFILE acr_fixture_readonly SETTINGS readonly = 2 TO acr_readonly`); err != nil {
		t.Fatal(err)
	}
}

func (f *clickHouseFixture) Stop(ctx context.Context) error {
	f.stopOnce.Do(func() { f.stopErr = f.container.Terminate(ctx) })
	return f.stopErr
}
