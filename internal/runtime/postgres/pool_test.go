package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestOpen_pingsPostgresAndAppliesBoundedPoolSettings(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := newTestPostgresDSN(t, ctx)
	config := Config{
		DSN:             dsn,
		AllowInsecure:   true,
		MaxOpenConns:    2,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
		ConnMaxIdleTime: time.Second,
		PingTimeout:     time.Second,
	}

	// When
	db, err := Open(ctx, config)

	// Then
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.Equal(t, 2, db.Stats().MaxOpenConnections)
	require.NoError(t, db.PingContext(ctx))
}

func TestConfig_validateDoesNotTrustClientPoolModeQuery(t *testing.T) {
	// Given
	config := Config{DSN: "postgres://acr:secret@db.example/acr?pool_mode=transaction&sslmode=verify-full"}

	// When
	err := config.validate()

	// Then
	require.NoError(t, err)
}

func TestConfig_validateSetsBoundedLifecycleDefaults_whenUnset(t *testing.T) {
	// Given
	config := Config{DSN: "postgres://acr:secret@db.example/acr?sslmode=verify-full"}

	// When
	err := config.validate()

	// Then
	require.NoError(t, err)
	require.Positive(t, config.ConnMaxLifetime)
	require.Positive(t, config.ConnMaxIdleTime)
}

func TestConfig_validateDerivesIdleDefaultWithinOpenLimit(t *testing.T) {
	config := Config{DSN: "postgres://acr:secret@db.example/acr?sslmode=verify-full", MaxOpenConns: 1}
	require.NoError(t, config.validate())
	require.Equal(t, 1, config.MaxIdleConns)
}

func TestConfig_validatePreservesExplicitZeroIdleConnections(t *testing.T) {
	config := Config{DSN: "postgres://acr:secret@db.example/acr?sslmode=verify-full", MaxOpenConns: 2, MaxIdleConnsSet: true}
	require.NoError(t, config.validate())
	require.Zero(t, config.MaxIdleConns)
}

func TestOpen_redactsDSN_whenConnectionCannotBeEstablished(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	config := Config{DSN: "postgres://acr:do-not-leak@127.0.0.1:1/acr?sslmode=disable", PingTimeout: time.Second, AllowInsecure: true}

	// When
	_, err := Open(ctx, config)

	// Then
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "do-not-leak"))
	require.False(t, strings.Contains(err.Error(), "127.0.0.1:1"))
}

func TestOpen_rejectsUnverifiedTLSWithoutLeakingDSN(t *testing.T) {
	// Given
	config := Config{DSN: "postgres://user:sentinel-secret@db.example/acr?sslmode=require"}

	// When
	_, err := Open(context.Background(), config)

	// Then
	require.ErrorContains(t, err, "verified TLS")
	require.NotContains(t, err.Error(), "sentinel-secret")
}

func TestValidateDSNTransport_acceptsUnixSocketWithoutTLS(t *testing.T) {
	err := ValidateDSNTransport("postgres:///acr?host=/var/run/postgresql", false)
	require.NoError(t, err)
}

func newTestPostgresDSN(t *testing.T, ctx context.Context) string {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"),
		tcpostgres.WithUsername("acr"),
		tcpostgres.WithPassword("acr"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}
