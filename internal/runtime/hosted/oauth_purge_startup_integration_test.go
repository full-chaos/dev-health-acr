package hosted

import (
	"bytes"
	"context"
	"database/sql"
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
// oauthPurgeStartupHarness is a fresh migrated Postgres plus a runtime role
// (acr_purge_rt) holding everything except DELETE on the three tables the OAuth
// purge deletes from: what a deployment looks like before the DELETE grants have
// been applied.
type oauthPurgeStartupHarness struct {
	ctx        context.Context
	owner      *sql.DB
	runtimeDSN string
}

func newOAuthPurgeStartupHarness(t *testing.T) oauthPurgeStartupHarness {
	t.Helper()
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

	for _, statement := range []string{
		`CREATE ROLE acr_purge_rt LOGIN PASSWORD 'rt'`,
		`GRANT USAGE ON SCHEMA acr TO acr_purge_rt`,
		`GRANT ALL ON ALL TABLES IN SCHEMA acr TO acr_purge_rt`,
		`REVOKE DELETE ON acr.oauth_clients, acr.oauth_authorization_requests, acr.device_authorizations FROM acr_purge_rt`,
	} {
		_, err = owner.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	parsed, err := url.Parse(ownerDSN)
	require.NoError(t, err)
	parsed.User = url.UserPassword("acr_purge_rt", "rt")
	return oauthPurgeStartupHarness{ctx: ctx, owner: owner, runtimeDSN: parsed.String()}
}

func (h oauthPurgeStartupHarness) config() config.Config {
	return config.Config{
		PostgresDSN: h.runtimeDSN,
		OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp"},
		OAuthConsentURL:        "https://www.example.test/acr/authorize",
		OAuthRequestPurgeGrace: 2 * time.Hour, OAuthClientIdleTTL: time.Hour,
	}
}

func (h oauthPurgeStartupHarness) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, h.owner.QueryRowContext(h.ctx, fmt.Sprintf(`SELECT count(*) FROM acr.%s`, table)).Scan(&n))
	return n
}

func newOAuthPurgeStartupLogger() (*slog.Logger, *bytes.Buffer) {
	var logs bytes.Buffer
	return slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})), &logs
}

func TestOpenPostgres_oauthPurgeAtStartupThroughTheRealConstructor(t *testing.T) {
	h := newOAuthPurgeStartupHarness(t)
	ctx, owner := h.ctx, h.owner

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
	count := func(table string) int { return h.count(t, table) }
	require.Equal(t, 1, count("oauth_clients"))
	require.Equal(t, 1, count("oauth_authorization_requests"))

	cfg := h.config()
	newLogger := newOAuthPurgeStartupLogger

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
	_, err = owner.ExecContext(ctx, `GRANT DELETE ON acr.oauth_clients, acr.oauth_authorization_requests, acr.device_authorizations TO acr_purge_rt`)
	require.NoError(t, err)
	logger, logs = newLogger()
	components, err = openPostgres(ctx, cfg, logger)
	require.NoError(t, err)
	require.NoError(t, components.close())
	require.Equal(t, 0, count("oauth_authorization_requests"), "the startup purge must delete the expired request")
	require.Equal(t, 0, count("oauth_clients"), "and, in the same call, the client whose last request just aged out")
	require.Equal(t, 0, count("device_authorizations"), "and the expired device authorization behind the request")
	require.Contains(t, logs.String(), `msg="oauth purge"`)
	require.Contains(t, logs.String(), "requests=1")
	require.Contains(t, logs.String(), "clients=1")
	require.Contains(t, logs.String(), "device_authorizations=1")
	require.Contains(t, logs.String(), "requests_remaining=0", "nothing eligible is left after the purge")
	require.Contains(t, logs.String(), "clients_remaining=0")
	require.Contains(t, logs.String(), "device_authorizations_remaining=0")
}

// The startup purge is the DELETE-privilege proof: a runtime role missing
// DELETE on ANY table the purge deletes from must fail startup, on a database
// that has no row to delete yet as well (the first tick that finds a candidate
// is far too late to learn the grant is missing).
func TestOpenPostgres_oauthPurgeStartupProvesDeleteOnEveryPurgedTable(t *testing.T) {
	h := newOAuthPurgeStartupHarness(t)
	ctx, owner := h.ctx, h.owner
	cfg := h.config()
	tables := []string{"acr.oauth_authorization_requests", "acr.oauth_clients", "acr.device_authorizations"}
	for _, missing := range tables {
		for _, table := range tables {
			verb := "GRANT DELETE ON " + table + " TO"
			if table == missing {
				verb = "REVOKE DELETE ON " + table + " FROM"
			}
			_, err := owner.ExecContext(ctx, verb+" acr_purge_rt")
			require.NoError(t, err)
		}
		logger, _ := newOAuthPurgeStartupLogger()
		_, err := openPostgres(ctx, cfg, logger)
		require.Errorf(t, err, "DELETE missing on %s: startup must fail even with nothing to delete", missing)
		require.Contains(t, err.Error(), "purge expired oauth rows")
	}

	_, err := owner.ExecContext(ctx, `GRANT DELETE ON acr.oauth_authorization_requests, acr.oauth_clients, acr.device_authorizations TO acr_purge_rt`)
	require.NoError(t, err)
	logger, _ := newOAuthPurgeStartupLogger()
	components, err := openPostgres(ctx, cfg, logger)
	require.NoError(t, err, "with every DELETE granted an empty database starts")
	require.NoError(t, components.close())
}

// CHAOS-7249: through the real constructor and a real database, a purge tick
// that skipped every eligible row logs a different line from a tick that found
// nothing, though both delete zero rows. The skip here is a real one: another
// transaction holds the eligible rows, and the purge skips locked rows instead
// of waiting for them.
func TestOpenPostgres_oauthPurgeTickLineSeparatesSkippedFromEmpty(t *testing.T) {
	h := newOAuthPurgeStartupHarness(t)
	ctx, owner := h.ctx, h.owner
	_, err := owner.ExecContext(ctx, `GRANT DELETE ON acr.oauth_clients, acr.oauth_authorization_requests, acr.device_authorizations TO acr_purge_rt`)
	require.NoError(t, err)
	cfg := h.config()

	// An empty database: nothing deleted, nothing eligible.
	logger, logs := newOAuthPurgeStartupLogger()
	components, err := openPostgres(ctx, cfg, logger)
	require.NoError(t, err)
	require.NoError(t, components.close())
	require.Contains(t, logs.String(), "requests=0 clients=0 device_authorizations=0 requests_remaining=0 clients_remaining=0 device_authorizations_remaining=0")

	// One expired request (its client is young, so the client stays) with its device
	// authorization, and one idle client, all locked by another transaction for the
	// whole startup purge.
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	audit, err := storagepostgres.NewAuditStore(owner)
	require.NoError(t, err)
	devices, err := storagepostgres.NewDeviceAuthorizationStoreWithOptions(owner, audit, storagepostgres.DeviceAuthorizationStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)
	seed, err := storagepostgres.NewOAuthStoreWithOptions(owner, storagepostgres.OAuthStoreOptions{Now: func() time.Time { return t0 }})
	require.NoError(t, err)
	clientID := func(seedByte byte) string {
		var random [16]byte
		for i := range random {
			random[i] = seedByte
		}
		return storage.NewDynamicOAuthClientID(random)
	}
	// a client registered "now" is young against the 1h idle window, whatever the clock is
	youngClient, idleClient := clientID(0x71), clientID(0x72)
	_, err = seed.RegisterClient(ctx, storage.OAuthClient{ClientID: youngClient, ClientName: "young", RedirectURIs: []string{"https://example.com/callback"}, CreatedAt: time.Now().UTC()})
	require.NoError(t, err)
	_, err = seed.RegisterClient(ctx, storage.OAuthClient{ClientID: idleClient, ClientName: "idle", RedirectURIs: []string{"https://example.com/callback"}, CreatedAt: t0})
	require.NoError(t, err)
	device, err := devices.Create(ctx, storage.DeviceAuthorizationCreateInput{DeviceCodeHash: storage.HashDeviceCode("skipped-tick-device"), UserCodeHash: storage.HashUserCode("SKIPTICK")})
	require.NoError(t, err)
	_, err = seed.CreateAuthorizationRequest(ctx, storage.OAuthAuthorizationRequest{
		HandleHash: storage.HashOAuthSecret("skipped-tick-handle"), DeviceCodeHash: device.DeviceCodeHash, ClientID: youngClient,
		ClientKind: storage.OAuthClientKindDynamic, RedirectURI: "https://example.com/callback", CodeChallenge: strings.Repeat("A", 43),
		Resource: "https://example.com/resource", Scope: "context:read", State: "s", CreatedAt: t0, ExpiresAt: t0.Add(storage.DeviceAuthorizationTTL),
	})
	require.NoError(t, err)

	tx, err := owner.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM acr.oauth_authorization_requests FOR UPDATE`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM acr.device_authorizations FOR UPDATE`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM acr.oauth_clients WHERE client_id = $1 FOR KEY SHARE`, idleClient)
	require.NoError(t, err)

	logger, logs = newOAuthPurgeStartupLogger()
	components, err = openPostgres(ctx, cfg, logger)
	require.NoError(t, err, "the startup purge must skip locked rows, not wait for them")
	require.NoError(t, components.close())
	require.Equal(t, 1, h.count(t, "oauth_authorization_requests"))
	require.Equal(t, 2, h.count(t, "oauth_clients"))
	require.Contains(t, logs.String(), "requests=0 clients=0 device_authorizations=0 requests_remaining=1 clients_remaining=1 device_authorizations_remaining=1",
		"skipped-everything must not read like the empty tick above")

	// Released, the next purge takes them and the line says nothing is left.
	require.NoError(t, tx.Commit())
	logger, logs = newOAuthPurgeStartupLogger()
	components, err = openPostgres(ctx, cfg, logger)
	require.NoError(t, err)
	require.NoError(t, components.close())
	require.Equal(t, 0, h.count(t, "oauth_authorization_requests"))
	require.Contains(t, logs.String(), "requests=1 clients=1 device_authorizations=1 requests_remaining=0 clients_remaining=0 device_authorizations_remaining=0")
	require.Equal(t, 1, h.count(t, "oauth_clients"), "the young client stays")
}
