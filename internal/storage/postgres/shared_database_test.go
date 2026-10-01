package postgres

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// One PostgreSQL container serves the whole package. A container per test put
// the package at 404 s of its 420 s -timeout on a passing hosted race run, and
// over it on the next two: 97 serial container starts were 273 s of that wall.
// The migrated database is the template; each caller gets its own copy.
const (
	sharedPostgresTemplate    = "acr"
	sharedPostgresMaintenance = "postgres"
	packageBudgetPercent      = 85
)

type sharedPostgresServer struct {
	admin     *sql.DB
	dsn       url.URL
	terminate func() error
}

var (
	sharedPostgresOnce      sync.Once
	sharedPostgres          *sharedPostgresServer
	sharedPostgresErr       error
	sharedPostgresDatabases atomic.Int64
)

func TestMain(m *testing.M) {
	started := time.Now()
	code := m.Run()
	if sharedPostgres != nil {
		if err := sharedPostgres.close(); err != nil {
			log.Printf("storage/postgres tests: stop the shared PostgreSQL container: %v", err)
			code = max(code, 1)
		}
	}
	elapsed := time.Since(started)
	if timeout := packageTestTimeout(); packageBudgetExceeded(elapsed, timeout) {
		log.Printf("storage/postgres tests: package wall %s is over %d%% of its -timeout %s (%d test databases on one container); the next slow run hits the alarm, which blames whichever test is running: shrink the suite or split the package",
			elapsed.Round(time.Millisecond), packageBudgetPercent, timeout, sharedPostgresDatabases.Load())
		code = max(code, 1)
	}
	os.Exit(code)
}

func packageTestTimeout() time.Duration {
	timeout := flag.Lookup("test.timeout")
	if timeout == nil {
		return 0
	}
	getter, ok := timeout.Value.(flag.Getter)
	if !ok {
		return 0
	}
	duration, _ := getter.Get().(time.Duration)
	return duration
}

func packageBudgetExceeded(elapsed, timeout time.Duration) bool {
	return timeout > 0 && elapsed*100 > timeout*packageBudgetPercent
}

func sharedPostgresFixture(t *testing.T) *sharedPostgresServer {
	t.Helper()
	sharedPostgresOnce.Do(func() { sharedPostgres, sharedPostgresErr = startSharedPostgres(context.Background()) })
	require.NoError(t, sharedPostgresErr, "start the shared PostgreSQL container")
	return sharedPostgres
}

func startSharedPostgres(ctx context.Context) (*sharedPostgresServer, error) {
	// Pinned by digest so TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this
	// to the ghcr.io mirror by digest, same as every other postgres:18-alpine
	// pull in this module.
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
		tcpostgres.WithDatabase(sharedPostgresTemplate), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}
	terminate := func() error { return container.Terminate(context.Background()) }
	fail := func(step string, err error) (*sharedPostgresServer, error) {
		return nil, errors.Join(fmt.Errorf("%s: %w", step, err), terminate())
	}
	raw, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fail("read connection string", err)
	}
	dsn, err := url.Parse(raw)
	if err != nil {
		return fail("parse connection string", err)
	}
	server := &sharedPostgresServer{dsn: *dsn, terminate: terminate}
	template, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: server.dsnFor(sharedPostgresTemplate)})
	if err != nil {
		return fail("open template database", err)
	}
	runner, err := migrations.Embedded()
	if err == nil {
		_, err = runner.Apply(ctx, template)
	}
	if err = errors.Join(err, template.Close()); err != nil {
		return fail("migrate template database", err)
	}
	server.admin, err = runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: server.dsnFor(sharedPostgresMaintenance)})
	if err != nil {
		return fail("open maintenance database", err)
	}
	return server, nil
}

func (s *sharedPostgresServer) dsnFor(database string) string {
	dsn := s.dsn
	dsn.Path = "/" + database
	return dsn.String()
}

func (s *sharedPostgresServer) createDatabase(t *testing.T, ctx context.Context) string {
	t.Helper()
	name := fmt.Sprintf("acr_test_%d", sharedPostgresDatabases.Add(1))
	_, err := s.admin.ExecContext(ctx, "CREATE DATABASE "+name+" TEMPLATE "+sharedPostgresTemplate)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := s.admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		require.NoError(t, err)
	})
	return name
}

func (s *sharedPostgresServer) close() error {
	return errors.Join(s.admin.Close(), s.terminate())
}

func TestCredentialStoreDatabase_sharesOneServerAndIsolatesEachDatabase(t *testing.T) {
	ctx := context.Background()
	first := newCredentialStoreDatabase(t, ctx)
	second := newCredentialStoreDatabase(t, ctx)
	identify := func(db *sql.DB) (server, database string) {
		t.Helper()
		require.NoError(t, db.QueryRowContext(ctx, `SELECT system_identifier::text, current_database() FROM pg_control_system()`).Scan(&server, &database))
		return server, database
	}
	relationExists := func(db *sql.DB, relation string) bool {
		t.Helper()
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, relation).Scan(&exists))
		return exists
	}

	_, err := first.ExecContext(ctx, `CREATE TABLE public.isolation_probe (id integer)`)
	require.NoError(t, err)
	third := newCredentialStoreDatabase(t, ctx)

	firstServer, firstDatabase := identify(first)
	secondServer, secondDatabase := identify(second)
	thirdServer, thirdDatabase := identify(third)
	require.Equal(t, firstServer, secondServer, "every test database comes from the one package container")
	require.Equal(t, firstServer, thirdServer, "every test database comes from the one package container")
	require.Len(t, map[string]struct{}{firstDatabase: {}, secondDatabase: {}, thirdDatabase: {}}, 3, "each caller owns a separate database")
	for _, db := range []*sql.DB{first, second, third} {
		require.True(t, relationExists(db, "acr.client_credentials"), "each database carries the migrated schema")
	}
	require.True(t, relationExists(first, "public.isolation_probe"))
	require.False(t, relationExists(second, "public.isolation_probe"), "a write in one test database is not visible in another")
	require.False(t, relationExists(third, "public.isolation_probe"), "a write in one test database does not reach the template")
}

func TestPackageBudgetExceeded(t *testing.T) {
	budget := 420 * time.Second
	for name, tc := range map[string]struct {
		elapsed, timeout time.Duration
		exceeded         bool
	}{
		"hosted pass of record at 96 percent": {elapsed: 403717 * time.Millisecond, timeout: budget, exceeded: true},
		"one millisecond over the share":      {elapsed: 357*time.Second + time.Millisecond, timeout: budget, exceeded: true},
		"exactly at the share":                {elapsed: 357 * time.Second, timeout: budget, exceeded: false},
		"well inside the budget":              {elapsed: 90 * time.Second, timeout: budget, exceeded: false},
		"no timeout set":                      {elapsed: time.Hour, timeout: 0, exceeded: false},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.exceeded, packageBudgetExceeded(tc.elapsed, tc.timeout))
		})
	}
}
