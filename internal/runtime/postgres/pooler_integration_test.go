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
			_, err := Open(ctx, Config{DSN: migrationDSN, PoolerAdminDSN: adminDSN, PingTimeout: 30 * time.Second, AllowInsecure: true})

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
	db, err := Open(ctx, Config{DSN: migrationDSN, PoolerAdminDSN: adminDSN, PingTimeout: 30 * time.Second, AllowInsecure: true})

	// Then
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.PingContext(ctx))
}

func TestOpen_rejectsActualPgBouncerDatabasePoolModeOverride(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	migrationDSN, adminDSN := newPgBouncerDSNsWithModes(t, ctx, pgBouncerModes{global: "session", database: "transaction"})

	// When
	_, err := Open(ctx, Config{DSN: migrationDSN, PoolerAdminDSN: adminDSN, PingTimeout: 30 * time.Second, AllowInsecure: true})

	// Then
	require.ErrorIs(t, err, ErrTransactionPooler)
}

func TestOpen_resolvesActualPgBouncerForcedUserPoolMode(t *testing.T) {
	for _, test := range []struct {
		mode    string
		wantErr error
	}{
		{mode: "session"},
		{mode: "transaction", wantErr: ErrTransactionPooler},
	} {
		t.Run(test.mode, func(t *testing.T) {
			// Given
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			modes := pgBouncerModes{global: "session", database: test.mode, forcedUser: true, clientUser: "client"}
			migrationDSN, adminDSN := newPgBouncerDSNsWithModes(t, ctx, modes)

			// When
			database, err := Open(ctx, Config{DSN: migrationDSN, PoolerAdminDSN: adminDSN, PingTimeout: 30 * time.Second, AllowInsecure: true})

			// Then
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, database.Close())
		})
	}
}

type pgBouncerModes struct {
	global     string
	database   string
	forcedUser bool
	clientUser string
}

func newPgBouncerDSNs(t *testing.T, ctx context.Context, mode string) (string, string) {
	return newPgBouncerDSNsWithModes(t, ctx, pgBouncerModes{global: mode})
}

func newPgBouncerDSNsWithModes(t *testing.T, ctx context.Context, modes pgBouncerModes) (string, string) {
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

	configPath, userListPath := writePgBouncerConfig(t, modes)
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
	clientUser := modes.clientUser
	clientPassword := "acr"
	if clientUser == "" {
		clientUser = "acr"
	}
	if clientUser == "client" {
		clientPassword = "client"
	}
	targetBase := fmt.Sprintf("postgres://%s:%s@%s:%s", clientUser, clientPassword, host, port.Port())
	adminBase := fmt.Sprintf("postgres://acr:acr@%s:%s", host, port.Port())
	migrationDSN, adminDSN := targetBase+"/acr?sslmode=disable", adminBase+"/pgbouncer?sslmode=disable"
	return migrationDSN, adminDSN
}

func writePgBouncerConfig(t *testing.T, modes pgBouncerModes) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pgbouncer.ini")
	databaseOptions := ""
	if modes.database != "" {
		databaseOptions = " pool_mode=" + modes.database
	}
	if modes.forcedUser {
		databaseOptions += " user=acr password=acr"
	}
	contents := fmt.Sprintf(`[databases]
acr = host=postgres port=5432 dbname=acr%s

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 5432
auth_type = plain
auth_file = /etc/pgbouncer/userlist.txt
admin_users = acr
pool_mode = %s
`, databaseOptions, modes.global)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	userListPath := filepath.Join(filepath.Dir(path), "userlist.txt")
	require.NoError(t, os.WriteFile(userListPath, []byte("\"acr\" \"acr\"\n\"client\" \"client\"\n"), 0o644))
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
