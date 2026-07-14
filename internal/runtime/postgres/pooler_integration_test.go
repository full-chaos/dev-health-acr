package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestOpen_rejectsActualPgBouncerTransactionAndStatementModes(t *testing.T) {
	for _, mode := range []string{"transaction", "statement"} {
		t.Run(mode, func(t *testing.T) {
			// Given
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			migrationDSN, adminDSN := newPgBouncerDSNs(t, ctx, mode)
			require.NotContains(t, migrationDSN, "pool_mode")

			// When
			_, err := Open(ctx, Config{DSN: migrationDSN, PoolerAdminDSN: adminDSN, PingTimeout: 30 * time.Second})

			// Then
			require.ErrorIs(t, err, ErrTransactionPooler)
		})
	}
}

func TestOpen_acceptsActualPgBouncerSessionMode(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	migrationDSN, adminDSN := newPgBouncerDSNs(t, ctx, "session")

	// When
	db, err := Open(ctx, Config{DSN: migrationDSN, PoolerAdminDSN: adminDSN, PingTimeout: 30 * time.Second})

	// Then
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.PingContext(ctx))
}

func newPgBouncerDSNs(t *testing.T, ctx context.Context, mode string) (string, string) {
	t.Helper()
	dockerNetwork, err := network.New(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { removeTestNetwork(t, dockerNetwork) })

	postgres, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"),
		tcpostgres.WithUsername("acr"),
		tcpostgres.WithPassword("acr"),
		tcpostgres.BasicWaitStrategies(),
		network.WithNetwork([]string{"postgres"}, dockerNetwork),
	)
	require.NoError(t, err)
	t.Cleanup(func() { terminateTestContainer(t, postgres) })

	configPath, userListPath := writePgBouncerConfig(t, mode)
	pooler, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "edoburu/pgbouncer:latest",
			ExposedPorts: []string{"5432/tcp"},
			Cmd:          []string{"pgbouncer", "/etc/pgbouncer/pgbouncer.ini"},
			Networks:     []string{dockerNetwork.Name},
			NetworkAliases: map[string][]string{
				dockerNetwork.Name: {"pgbouncer"},
			},
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      configPath,
				ContainerFilePath: "/etc/pgbouncer/pgbouncer.ini",
				FileMode:          0o644,
			}, {
				HostFilePath:      userListPath,
				ContainerFilePath: "/etc/pgbouncer/userlist.txt",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForListeningPort("5432/tcp"),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { terminateTestContainer(t, pooler) })

	host, err := pooler.Host(ctx)
	require.NoError(t, err)
	port, err := pooler.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	base := fmt.Sprintf("postgres://acr:acr@%s:%s", host, port.Port())
	migrationDSN, adminDSN := base+"/acr?sslmode=disable", base+"/pgbouncer?sslmode=disable"
	return migrationDSN, adminDSN
}

func writePgBouncerConfig(t *testing.T, mode string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pgbouncer.ini")
	contents := fmt.Sprintf(`[databases]
acr = host=postgres port=5432 dbname=acr

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 5432
auth_type = plain
auth_file = /etc/pgbouncer/userlist.txt
admin_users = acr
pool_mode = %s
`, mode)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	userListPath := filepath.Join(filepath.Dir(path), "userlist.txt")
	require.NoError(t, os.WriteFile(userListPath, []byte("\"acr\" \"acr\"\n"), 0o644))
	return path, userListPath
}

func terminateTestContainer(t *testing.T, container testcontainers.Container) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, container.Terminate(ctx))
}

func removeTestNetwork(t *testing.T, dockerNetwork *testcontainers.DockerNetwork) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, dockerNetwork.Remove(ctx))
}
