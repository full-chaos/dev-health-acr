package config

import (
	"net"
	"strings"
	"testing"

	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// TestLoadProjectorDefaults binds the same class-sweep fix as acr-api's
// TestLoadDefaults (dictation 811): a process started with NO environment
// configured at all must fail closed, same as acr-api. Before this fix
// loadProjector(mapLookup(nil), requiredStoresAll) SUCCEEDED with RequireBackingStores false.
func TestLoadProjectorDefaults(t *testing.T) {
	_, err := loadProjector(mapLookup(nil), requiredStoresAll)
	if err == nil || !strings.Contains(err.Error(), "backing stores are required") {
		t.Fatalf("loadProjector() error = %v, want a backing-stores-required refusal with zero configuration", err)
	}
}

// TestLoadProjectorDefaults_bareOverrideAloneCannotDisableBackingStores is
// r1 P2 finding 1's class-sweep pin for the projector: a bare
// ACR_REQUIRE_BACKING_STORES=false, without the dev flag, must not disable
// the requirement here either.
func TestLoadProjectorDefaults_bareOverrideAloneCannotDisableBackingStores(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{"ACR_REQUIRE_BACKING_STORES": "false"}), requiredStoresAll)
	if err == nil || !strings.Contains(err.Error(), "backing stores are required") {
		t.Fatalf("loadProjector() error = %v, want a backing-stores-required refusal: a bare ACR_REQUIRE_BACKING_STORES=false without the dev flag must not disable the requirement", err)
	}
}

// TestLoadProjectorPriors_zeroEnvRefusesNamingPostgresOnly is r2 P1
// finding 1's own domain cell: the priors operator surface (CHAOS-3977 P5)
// is Postgres-only, so a zero-env start must refuse naming ONLY
// ACR_POSTGRES_DSN, never ACR_CLICKHOUSE_DSN (which this surface never
// opens). Reproduced live before the fix:
// `env -i acr-projector priors flip ...` -> "ACR_CLICKHOUSE_DSN is
// required when backing stores are required" (the wrong store named).
func TestLoadProjectorPriors_zeroEnvRefusesNamingPostgresOnly(t *testing.T) {
	_, err := loadProjector(mapLookup(nil), requiredStoresPostgresOnly)
	if err == nil || !strings.Contains(err.Error(), "ACR_POSTGRES_DSN") {
		t.Fatalf("loadProjector(requiredStoresPostgresOnly) error = %v, want an ACR_POSTGRES_DSN refusal", err)
	}
	if strings.Contains(err.Error(), "ACR_CLICKHOUSE_DSN") {
		t.Fatalf("loadProjector(requiredStoresPostgresOnly) error = %v, must never name ACR_CLICKHOUSE_DSN -- priors never opens it", err)
	}
}

// TestLoadProjectorPriors_postgresOnlyConfigurationSucceeds is r2 P1
// finding 1's second domain cell: a fully-valid Postgres-only environment
// (no ClickHouse DSN at all) must be ACCEPTED for the priors operator
// surface -- this is the exact scenario the review found refused.
func TestLoadProjectorPriors_postgresOnlyConfigurationSucceeds(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":             "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
	}), requiredStoresPostgresOnly)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cfg.ClickHouseDSN) != "" {
		t.Fatalf("ClickHouseDSN = %q, want empty (never configured, never required for priors)", cfg.ClickHouseDSN)
	}
}

// TestLoadProjectorPriors_clickHouseDSNFileUnreadableIsNeverRead is r3 P1
// finding 1's own repro made a domain cell: priors' Postgres-only
// requirement must mean ACR_CLICKHOUSE_DSN_FILE is never even READ, not
// merely unvalidated -- before this fix, loadHostedRuntimeValues called
// SecretValue("ACR_CLICKHOUSE_DSN") unconditionally, so an operator's
// shared environment (the same env a co-located `serve` process reads)
// naming an unreadable ClickHouse DSN file refused a priors start that
// never opens that file. Reproduced live before the fix:
//
//	configuration: ACR_CLICKHOUSE_DSN_FILE: secret file is unreadable
func TestLoadProjectorPriors_clickHouseDSNFileUnreadableIsNeverRead(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":             "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
		"ACR_CLICKHOUSE_DSN_FILE":      t.TempDir() + "/missing-clickhouse.dsn",
	}), requiredStoresPostgresOnly)
	if err != nil {
		t.Fatalf("loadProjector(requiredStoresPostgresOnly) error = %v, want success -- priors must never read ACR_CLICKHOUSE_DSN_FILE at all", err)
	}
	if strings.TrimSpace(cfg.ClickHouseDSN) != "" {
		t.Fatalf("ClickHouseDSN = %q, want empty -- the file was never opened", cfg.ClickHouseDSN)
	}
}

// TestLoadProjectorPriors_projectionEnabledWithoutOrgIDsDoesNotRefuse is r3
// P1 finding 1's second domain cell: priors never runs the projection
// coordinator loop, so ACR_CONTEXT_FABRIC_PROJECTION_ENABLED=true without
// an org allowlist (meaningful only to serve/rebuild/rollback, and plausible
// in a shared operator environment) must not refuse a priors start.
// Reproduced live before the fix:
//
//	configuration: ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS is required when ACR_CONTEXT_FABRIC_PROJECTION_ENABLED is true in an environment that requires backing stores
func TestLoadProjectorPriors_projectionEnabledWithoutOrgIDsDoesNotRefuse(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":                      "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "true",
	}), requiredStoresPostgresOnly)
	if err != nil {
		t.Fatalf("loadProjector(requiredStoresPostgresOnly) error = %v, want success -- priors does not run the projection loop", err)
	}
}

// TestLoadProjectorPriors_clickHouseMaxBytesZeroDoesNotRefuse is the
// symmetric class-sweep cell for ClickHouseMaxBytesToRead: priors never
// queries ClickHouse, so a shared environment's
// ACR_CLICKHOUSE_MAX_BYTES_TO_READ=0 (meaningless to priors) must not
// refuse it either.
func TestLoadProjectorPriors_clickHouseMaxBytesZeroDoesNotRefuse(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":                 "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND":     "direct",
		"ACR_CLICKHOUSE_MAX_BYTES_TO_READ": "0",
	}), requiredStoresPostgresOnly)
	if err != nil {
		t.Fatalf("loadProjector(requiredStoresPostgresOnly) error = %v, want success -- priors does not read ClickHouse", err)
	}
}

// TestLoadProjectorFullStores_postgresOnlyStillRefusesNamingClickHouse
// proves the OTHER half of the split: every command using the FULL
// requirement (serve, rebuild, rollback) must still refuse a Postgres-only
// environment, naming ClickHouse specifically -- the split must narrow
// priors alone, not silently loosen the requirement for everyone.
func TestLoadProjectorFullStores_postgresOnlyStillRefusesNamingClickHouse(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":             "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
	}), requiredStoresAll)
	if err == nil || !strings.Contains(err.Error(), "ACR_CLICKHOUSE_DSN") {
		t.Fatalf("loadProjector(requiredStoresAll) error = %v, want an ACR_CLICKHOUSE_DSN refusal", err)
	}
}

// TestLoadProjectorFullStores_clickHouseOnlyStillRefusesNamingPostgres is
// r3 P1 finding 1's serve/rebuild/rollback-side counterpart: the full-stack
// requirement narrowing that let priors stop reading ClickHouse must not
// have loosened the OTHER half -- a ClickHouse-only environment must still
// be refused, naming Postgres, for every requiredStoresAll caller.
func TestLoadProjectorFullStores_clickHouseOnlyStillRefusesNamingPostgres(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_CLICKHOUSE_DSN": "https://clickhouse.internal",
	}), requiredStoresAll)
	if err == nil || !strings.Contains(err.Error(), "ACR_POSTGRES_DSN") {
		t.Fatalf("loadProjector(requiredStoresAll) error = %v, want an ACR_POSTGRES_DSN refusal", err)
	}
}

// TestLoadProjectorFullStores_canonicalConfigurationSucceeds is r3 P1
// finding 1's full-stack canonical cell: a genuinely complete environment
// (both DSNs, no dev flag) must pass requiredStoresAll validation -- the
// narrowing for priors must not have tightened anything for the callers
// that still need everything.
func TestLoadProjectorFullStores_canonicalConfigurationSucceeds(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":             "postgres://configured",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
		"ACR_CLICKHOUSE_DSN":           "https://clickhouse.internal",
	}), requiredStoresAll)
	if err != nil {
		t.Fatalf("loadProjector(requiredStoresAll) error = %v, want a canonical full-stack configuration to succeed", err)
	}
	if cfg.PostgresDSN == "" || cfg.ClickHouseDSN == "" {
		t.Fatalf("cfg = %#v, want both DSNs populated", cfg)
	}
}

// TestLoadProjectorDefaults_developmentWithLocalCompositionReady isolates
// every other default TestLoadProjectorDefaults itself can no longer
// observe on the (now-erroring) bare path, using the same dev-flag
// exemption acr-api's Config supports.
func TestLoadProjectorDefaults_developmentWithLocalCompositionReady(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true"}), requiredStoresAll)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != defaultProjectorListenAddress {
		t.Fatalf("listen address = %q", cfg.ListenAddress)
	}
	if cfg.ProjectionEnabled {
		t.Fatal("projection must be disabled by default")
	}
	if cfg.PollInterval != defaultProjectionPollInterval || cfg.Concurrency != defaultProjectionConcurrency {
		t.Fatalf("unexpected scheduling defaults: %#v", cfg)
	}
	if cfg.DrainBatchBudget != defaultProjectionDrainBudget {
		t.Fatalf("DrainBatchBudget = %d, want default %d", cfg.DrainBatchBudget, defaultProjectionDrainBudget)
	}
	if cfg.RequireBackingStores {
		t.Fatal("ACR_LOCAL_COMPOSITION_READY=true in development must default backing stores to NOT required")
	}
	// CHAOS-3848: acr-projector is the binary that was actually wedged --
	// it must inherit the same raised default acr-api does, via the shared
	// loadHostedRuntimeValues path.
	if cfg.ClickHouseMaxBytesToRead != runtimeclickhouse.DefaultMaxBytesToRead {
		t.Fatalf("ClickHouseMaxBytesToRead = %d, want default %d", cfg.ClickHouseMaxBytesToRead, runtimeclickhouse.DefaultMaxBytesToRead)
	}
}

// TestLoadProjectorDefaults_listenAddressLoopback pins the loopback-only
// listen default directly (dictation 811 -- was ":8090", every interface).
func TestLoadProjectorDefaults_listenAddressLoopback(t *testing.T) {
	if defaultProjectorListenAddress != "127.0.0.1:8090" {
		t.Fatalf("defaultProjectorListenAddress = %q, want loopback-only", defaultProjectorListenAddress)
	}
}

// TestDefaultProjectorListenAddressActuallyBindsLoopbackOnly is r2 P3
// finding 3's class sweep to acr-projector: executes a REAL net.Listen
// against defaultProjectorListenAddress's own host (port swapped for an
// ephemeral 0) and asserts the address the OS actually bound -- read back
// from the live listener, never the config string -- is loopback. See
// TestDefaultListenAddressActuallyBindsLoopbackOnly (config_test.go) for
// why a string-only assertion cannot catch a wiring regression here.
func TestDefaultProjectorListenAddressActuallyBindsLoopbackOnly(t *testing.T) {
	host, _, err := net.SplitHostPort(defaultProjectorListenAddress)
	if err != nil {
		t.Fatalf("defaultProjectorListenAddress = %q is not host:port: %v", defaultProjectorListenAddress, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("defaultProjectorListenAddress host = %q, want 127.0.0.1", host)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("net.Listen(%q) = %v, want a successful loopback bind", net.JoinHostPort(host, "0"), err)
	}
	defer listener.Close()
	boundHost, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("listener.Addr() = %q is not host:port: %v", listener.Addr().String(), err)
	}
	if boundHost != "127.0.0.1" {
		t.Fatalf("listener actually bound host = %q (from the live OS-assigned address, not the config string), want 127.0.0.1", boundHost)
	}
}

// TestLoadProjectorStagingCannotDisableBackingStoresOverride mirrors
// TestStagingCannotDisableBackingStoresOverride (config_test.go) for the
// projector: asserts the FORCED value directly rather than merely
// "loadProjector() errors" (an under-configured staging environment can
// error for unrelated reasons too).
func TestLoadProjectorStagingCannotDisableBackingStoresOverride(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_ENVIRONMENT":              "staging",
		"ACR_REQUIRE_BACKING_STORES":   "false",
		"ACR_CLICKHOUSE_DSN":           "clickhouse://redacted",
		"ACR_POSTGRES_DSN":             "postgres://redacted?sslmode=verify-full",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
	}), requiredStoresAll)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireBackingStores {
		t.Fatal("staging must force RequireBackingStores=true even over an explicit ACR_REQUIRE_BACKING_STORES=false")
	}
}

// TestLoadProjectorRejectsLocalCompositionReadyOutsideDevelopment mirrors
// cmd/acr-api's TestConfig_rejects_local_composition_in_production for the
// projector's own (new) interlock.
func TestLoadProjectorRejectsLocalCompositionReadyOutsideDevelopment(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_ENVIRONMENT":             "production",
		"ACR_LOCAL_COMPOSITION_READY": "true",
	}), requiredStoresAll)
	if err == nil {
		t.Fatal("local composition was accepted in production")
	}
}

func TestLoadProjector_appliesConfiguredClickHouseMaxBytesToRead(t *testing.T) {
	// Given
	// When
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_CLICKHOUSE_MAX_BYTES_TO_READ": "33554432",
		"ACR_LOCAL_COMPOSITION_READY":      "true",
	}), requiredStoresAll)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClickHouseMaxBytesToRead != 32<<20 {
		t.Fatalf("ClickHouseMaxBytesToRead = %d, want %d", cfg.ClickHouseMaxBytesToRead, uint64(32<<20))
	}
}

func TestLoadProjector_rejectsInvalidClickHouseMaxBytesToRead(t *testing.T) {
	for _, value := range []string{"0", "-1", "garbage"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadProjector(mapLookup(map[string]string{"ACR_CLICKHOUSE_MAX_BYTES_TO_READ": value}), requiredStoresAll)
			if err == nil || !strings.Contains(err.Error(), "ACR_CLICKHOUSE_MAX_BYTES_TO_READ") {
				t.Fatalf("loadProjector() error = %v, want ACR_CLICKHOUSE_MAX_BYTES_TO_READ rejection", err)
			}
		})
	}
}

func TestLoadProjectorProductionRequiresBackingStores(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{"ACR_ENVIRONMENT": "production"}), requiredStoresAll)
	if err == nil {
		t.Fatal("expected an error: production requires ACR_CLICKHOUSE_DSN/ACR_POSTGRES_DSN")
	}
}

func TestLoadProjectorEnabledProductionRequiresOrgAllowlist(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{
		"ACR_ENVIRONMENT": "production", "ACR_CLICKHOUSE_DSN": "https://clickhouse.internal", "ACR_POSTGRES_DSN": "postgres://db/acr",
		"ACR_POSTGRES_CONNECTION_KIND": "direct", "ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "true",
	}), requiredStoresAll)
	if err == nil {
		t.Fatal("expected an error: enabling projection without an organization allowlist")
	}
}

func TestLoadProjectorParsesOrgAllowlistAndScheduling(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED":            "true",
		"ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS":             "org-1, org-2 ,org-3",
		"ACR_CONTEXT_FABRIC_PROJECTION_POLL_INTERVAL":      "30s",
		"ACR_CONTEXT_FABRIC_PROJECTION_CONCURRENCY":        "10",
		"ACR_CONTEXT_FABRIC_PROJECTION_DRAIN_BATCH_BUDGET": "50",
		"ACR_LOCAL_COMPOSITION_READY":                      "true",
	}), requiredStoresAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OrgIDs) != 3 || cfg.OrgIDs[0] != "org-1" || cfg.OrgIDs[2] != "org-3" {
		t.Fatalf("org ids = %#v", cfg.OrgIDs)
	}
	if cfg.PollInterval.String() != "30s" || cfg.Concurrency != 10 || cfg.DrainBatchBudget != 50 {
		t.Fatalf("scheduling = %#v", cfg)
	}
}

func TestLoadProjectorRejectsInvalidEnvironment(t *testing.T) {
	_, err := loadProjector(mapLookup(map[string]string{"ACR_ENVIRONMENT": "sandbox"}), requiredStoresAll)
	if err == nil {
		t.Fatal("expected an error for an invalid environment")
	}
}

// TestProjectionEnablementIsIndependentOfTheGraphReadsFlag proves the two
// enablement levers required by the ticket (independent disablement of
// projection and reads) don't interact on the projector side: toggling
// ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED -- the flag Reset 1B/1C's
// GraphReader/hosted composition owns, reserved here as
// GraphReadsEnabledEnvVar -- has zero effect on ProjectionEnabled in either
// direction. This package intentionally never reads that variable itself;
// the assertion is that setting it can't accidentally flip projection on
// or off. The read side's own independent enablement is Reset 1B/1C's to
// prove once GraphReader exists.
func TestProjectionEnablementIsIndependentOfTheGraphReadsFlag(t *testing.T) {
	enabled, err := loadProjector(mapLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "true", "ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS": "org-1",
		GraphReadsEnabledEnvVar:       "false",
		"ACR_LOCAL_COMPOSITION_READY": "true",
	}), requiredStoresAll)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.ProjectionEnabled {
		t.Fatal("projection must stay enabled regardless of the graph-reads flag's value")
	}

	disabled, err := loadProjector(mapLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": "false",
		GraphReadsEnabledEnvVar:                 "true",
		"ACR_LOCAL_COMPOSITION_READY":           "true",
	}), requiredStoresAll)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.ProjectionEnabled {
		t.Fatal("the graph-reads flag must never turn projection on")
	}
}

func TestProjectorConfigSafeAttributesOmitDSNs(t *testing.T) {
	cfg, err := loadProjector(mapLookup(map[string]string{
		"ACR_POSTGRES_DSN":            "postgres://secret@db/acr",
		"ACR_LOCAL_COMPOSITION_READY": "true",
	}), requiredStoresAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, attr := range cfg.SafeAttributes() {
		if text, ok := attr.(string); ok && text == "postgres://secret@db/acr" {
			t.Fatal("SafeAttributes leaked a DSN")
		}
	}
}

// TestTeamsProjectsDefaultsToEnabled is CHAOS-3802 D2. The flag defaulted to
// false only because teams_projects.go was a stub that failed loudly when
// switched on; with a real source behind it, a default-off feature whose
// acceptance criterion ("an org rebuild picks the new kinds up") requires it
// on is a dead guard, not a safety measure. An operator can still turn it off
// explicitly -- which the second half of this test proves is a real, reachable
// choice and not a value the loader ignores.
func TestTeamsProjectsDefaultsToEnabled(t *testing.T) {
	t.Parallel()
	cfg, err := loadProjector(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true"}), requiredStoresAll)
	if err != nil {
		t.Fatalf("loadProjector: %v", err)
	}
	if !cfg.TeamsProjectsEnabled {
		t.Fatal("ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED must default to true now that the source is implemented")
	}
	off, err := loadProjector(mapLookup(map[string]string{
		envContextFabricTeamsProjects: "false", "ACR_LOCAL_COMPOSITION_READY": "true",
	}), requiredStoresAll)
	if err != nil {
		t.Fatalf("loadProjector: %v", err)
	}
	if off.TeamsProjectsEnabled {
		t.Fatal("an explicit false must still disable team/project projection")
	}
}
