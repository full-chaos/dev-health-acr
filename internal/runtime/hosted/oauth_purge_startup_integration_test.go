package hosted

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestOpenPostgres_oauthPurgeAtStartupThroughTheRealConstructor drives the
// REAL openPostgres (the constructor acr-api uses) against a fresh migrated
// Postgres, connected as a runtime role, and proves the startup contract of
// the OAuth purge end to end through the wiring, not a fake: unconfigured OAuth
// never needs DELETE; configured OAuth with the DELETE grant missing fails
// startup; with the grant the initial purge runs with the configured windows,
// deletes the expired rows through that role, logs its heartbeat line, and the
// runtime closes cleanly.
func TestOpenPostgres_oauthPurgeAtStartupThroughTheRealConstructor(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	ownerDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	owner, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: ownerDSN})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, owner)
	require.NoError(t, err)

	// A runtime role holding everything except DELETE on the two OAuth tables:
	// what a deployment looks like before the DELETE grant has been applied.
	for _, statement := range []string{
		`CREATE ROLE acr_purge_rt LOGIN PASSWORD 'rt'`,
		`GRANT USAGE ON SCHEMA acr TO acr_purge_rt`,
		`GRANT ALL ON ALL TABLES IN SCHEMA acr TO acr_purge_rt`,
		`REVOKE DELETE ON acr.oauth_clients, acr.oauth_authorization_requests FROM acr_purge_rt`,
	} {
		_, err = owner.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	parsed, err := url.Parse(ownerDSN)
	require.NoError(t, err)
	parsed.User = url.UserPassword("acr_purge_rt", "rt")
	runtimeDSN := parsed.String()

	// Rows that are long past any window (2026-09-01 against a real clock).
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	audit, err := storagepostgres.NewAuditStore(owner)
	require.NoError(t, err)
	devices, err := storagepostgres.NewDeviceAuthorizationStoreWithOptions(owner, audit, storagepostgres.DeviceAuthorizationStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)
	seed, err := storagepostgres.NewOAuthStoreWithOptions(owner, storagepostgres.OAuthStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)
	var random [16]byte
	clientID := storage.NewDynamicOAuthClientID(random)
	_, err = seed.RegisterClient(ctx, storage.OAuthClient{ClientID: clientID, ClientName: "startup purge", RedirectURIs: []string{"https://example.com/callback"}, CreatedAt: t0})
	require.NoError(t, err)
	device, err := devices.Create(ctx, storage.DeviceAuthorizationCreateInput{DeviceCodeHash: storage.HashDeviceCode("startup-purge-device"), UserCodeHash: storage.HashUserCode("STARTPRG")})
	require.NoError(t, err)
	_, err = seed.CreateAuthorizationRequest(ctx, storage.OAuthAuthorizationRequest{
		HandleHash: storage.HashOAuthSecret("startup-purge-handle"), DeviceCodeHash: device.DeviceCodeHash, ClientID: clientID,
		ClientKind: storage.OAuthClientKindDynamic, RedirectURI: "https://example.com/callback", CodeChallenge: strings.Repeat("A", 43),
		Resource: "https://example.com/resource", Scope: "context:read", State: "s", CreatedAt: t0, ExpiresAt: t0.Add(storage.DeviceAuthorizationTTL),
	})
	require.NoError(t, err)
	count := func(table string) int {
		var n int
		require.NoError(t, owner.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM acr.%s`, table)).Scan(&n))
		return n
	}
	require.Equal(t, 1, count("oauth_clients"))
	require.Equal(t, 1, count("oauth_authorization_requests"))

	cfg := config.Config{
		PostgresDSN: runtimeDSN,
		OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp"},
		OAuthConsentURL:        "https://www.example.test/acr/authorize",
		OAuthRequestPurgeGrace: time.Hour, OAuthClientIdleTTL: 2 * time.Hour,
	}
	newLogger := func() (*slog.Logger, *bytes.Buffer) {
		var logs bytes.Buffer
		return slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})), &logs
	}

	// OAuth unconfigured: no purge runs, so a role without the OAuth DELETE grant starts, rows untouched.
	logger, logs := newLogger()
	unconfigured := cfg
	unconfigured.OAuthIssuer, unconfigured.OAuthResources, unconfigured.OAuthConsentURL = "", nil, ""
	components, err := openPostgres(ctx, unconfigured, logger)
	require.NoError(t, err, "a deployment without OAuth must start without DELETE on the OAuth tables")
	require.NoError(t, components.close())
	require.NotContains(t, logs.String(), "oauth purge")
	require.Equal(t, 1, count("oauth_authorization_requests"))

	// OAuth configured, DELETE not granted: startup fails loudly, naming the purge, deleting nothing.
	logger, _ = newLogger()
	_, err = openPostgres(ctx, cfg, logger)
	require.Error(t, err, "OAuth configured with the DELETE grant missing must fail startup")
	require.Contains(t, err.Error(), "purge expired oauth rows")
	require.Equal(t, 1, count("oauth_authorization_requests"))
	require.Equal(t, 1, count("oauth_clients"))

	// DELETE granted (what the runtime-acl step / Helm hook does): the initial purge runs through the real wiring.
	_, err = owner.ExecContext(ctx, `GRANT DELETE ON acr.oauth_clients, acr.oauth_authorization_requests TO acr_purge_rt`)
	require.NoError(t, err)
	logger, logs = newLogger()
	components, err = openPostgres(ctx, cfg, logger)
	require.NoError(t, err)
	require.NoError(t, components.close())
	require.Equal(t, 0, count("oauth_authorization_requests"), "the startup purge must delete the expired request")
	require.Equal(t, 0, count("oauth_clients"), "and, in the same call, the client whose last request just aged out")
	require.Contains(t, logs.String(), `msg="oauth purge"`)
	require.Contains(t, logs.String(), "requests=1")
	require.Contains(t, logs.String(), "clients=1")
}
