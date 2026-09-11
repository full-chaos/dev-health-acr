package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	// defaultProjectorListenAddress: loopback-only, same class fix and same
	// reason as acr-api's defaultListenAddress in config.go -- an
	// unauthenticated readiness HTTP server started with no ACR_PROJECTOR_ADDR
	// configured must not be reachable from the network by default. Every
	// real deployment surface in this repo sets ACR_PROJECTOR_ADDR explicitly
	// to a 0.0.0.0-bound address.
	defaultProjectorListenAddress = "127.0.0.1:8090"
	defaultProjectionPollInterval = 15 * time.Second
	defaultProjectionConcurrency  = 4
	defaultProjectionDrainBudget  = 500
	defaultProjectorPingTimeout   = 5 * time.Second
	envContextFabricProjection    = "ACR_CONTEXT_FABRIC_PROJECTION_ENABLED"
	envContextFabricProjectorOrgs = "ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS"
	envContextFabricPollInterval  = "ACR_CONTEXT_FABRIC_PROJECTION_POLL_INTERVAL"
	envContextFabricConcurrency   = "ACR_CONTEXT_FABRIC_PROJECTION_CONCURRENCY"
	envContextFabricDrainBudget   = "ACR_CONTEXT_FABRIC_PROJECTION_DRAIN_BATCH_BUDGET"
	envContextFabricTeamsProjects = "ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED"
	envContextFabricGraphReads    = "ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED"
	envProjectorListenAddress     = "ACR_PROJECTOR_ADDR"
	envProjectorEnvironment       = "ACR_ENVIRONMENT"
)

// ProjectorConfig is cmd/acr-projector's process configuration. It shares
// the Postgres/ClickHouse connection knobs and env var names with the
// hosted acr-api Config (loadHostedRuntimeValues) -- both binaries reach the
// same PostgreSQL and ClickHouse instances -- but it is independently
// loaded and validated, not a shared Config, because acr-projector has none
// of acr-api's HTTP-serving, sidecar-compatibility, or entitlement
// invariants and must not be forced to satisfy them.
type ProjectorConfig struct {
	Environment   string
	LogLevel      slog.Level
	ListenAddress string // readiness/health HTTP server

	ClickHouseDSN                  string
	ClickHouseCACertPath           string
	ClickHouseMaxBytesToRead       uint64
	PostgresDSN                    string
	PostgresPoolerAdminDSN         string
	PostgresConnectionKind         string
	PostgresMaxOpenConns           int
	PostgresMaxIdleConns           int
	PostgresMaxIdleConnsConfigured bool
	PostgresConnMaxLifetime        time.Duration
	PostgresConnMaxIdleTime        time.Duration
	PostgresPingTimeout            time.Duration
	RequireBackingStores           bool
	// LocalCompositionReady mirrors Config.LocalCompositionReady (dictation
	// 811): the explicit dev opt-out allowing RequireBackingStores to
	// default false, restricted to ACR_ENVIRONMENT=development by Validate
	// below.
	LocalCompositionReady bool

	// ProjectionEnabled is this binary's own master switch (independent of
	// ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED, which gates graph reads in
	// acr-api/hosted composition -- Reset 1B/1C scope, reserved here so the
	// two lanes don't collide on the name). When false the coordinator loop
	// never starts; the readiness server still runs and reports disabled.
	ProjectionEnabled bool
	// OrgIDs is the explicit allowlist of organizations to project. See
	// docs/design/context-fabric-projection-worker.md for why this starts
	// as an explicit list rather than auto-discovery.
	OrgIDs       []string
	PollInterval time.Duration
	Concurrency  int
	// DrainBatchBudget (CHAOS-3826) bounds how many extra batches, beyond
	// the one every configured source always attempts, one organization's
	// Tick may pull across all its sources combined before yielding to the
	// next poll -- see projectionrun.Config.DrainBatchBudget's doc comment.
	DrainBatchBudget     int
	TeamsProjectsEnabled bool
}

// requiredStores names the specific backing stores ONE CALLER of
// ProjectorConfig actually opens -- r2 P1 finding: acr-projector's
// commands do not all open the same stores (serve/rebuild/rollback run the
// full projection composition, Postgres AND ClickHouse; the priors
// operator surface, CHAOS-3977 P5, is deliberately Postgres-only, see
// openPriorsDB's own doc comment in cmd/acr-projector/priors.go), so a
// single binary-wide "backing stores required" boolean cannot validate
// both correctly -- requiring ClickHouse for a command that never opens it
// would refuse a legitimately-configured priors deployment, and NOT
// requiring it for serve would reintroduce the original defect this whole
// change exists to close. RequireBackingStores itself still answers "is
// validation active at all" (the dev-flag exemption); requiredStores
// answers "which DSNs does THIS validation enforce".
type requiredStores struct {
	postgres   bool
	clickhouse bool
}

var (
	// requiredStoresAll is every acr-projector command except priors:
	// serve, rebuild, rollback.
	requiredStoresAll = requiredStores{postgres: true, clickhouse: true}
	// requiredStoresPostgresOnly is the priors operator surface's own
	// requirement (curate, flip, rollback, revoke) -- never ClickHouse.
	requiredStoresPostgresOnly = requiredStores{postgres: true}
)

// LoadProjector reads cmd/acr-projector's configuration from the process
// environment and validates it against the FULL backing-store requirement
// (Postgres AND ClickHouse) -- the requirement every acr-projector command
// EXCEPT priors actually has. Use LoadProjectorPriors for the priors
// operator surface.
func LoadProjector() (ProjectorConfig, error) {
	return loadProjector(os.LookupEnv, requiredStoresAll)
}

// LoadProjectorPriors reads cmd/acr-projector's configuration for the
// priors operator surface (CHAOS-3977 P5) -- Postgres-only, never
// ClickHouse, unlike every other acr-projector command. Validating against
// the full requirement here would refuse a legitimately-configured
// Postgres-only priors deployment for a store it never opens (r2 P1
// finding, reproduced live: a fully-valid Postgres-only environment was
// refused with "ACR_CLICKHOUSE_DSN is required...").
func LoadProjectorPriors() (ProjectorConfig, error) {
	return loadProjector(os.LookupEnv, requiredStoresPostgresOnly)
}

func loadProjector(lookup lookupEnv, required requiredStores) (ProjectorConfig, error) {
	environment := stringValue(lookup, envProjectorEnvironment, defaultEnvironment)
	// requireStoresDefault: same class fix as acr-api's load() in config.go
	// -- backing stores are required by default in every environment now,
	// exempted only by ACR_ENVIRONMENT=development with the explicit
	// ACR_LOCAL_COMPOSITION_READY dev flag; this is ALSO the force argument
	// passed to loadHostedRuntimeValues below (r1 P2 finding 1 class sweep:
	// a bare ACR_REQUIRE_BACKING_STORES=false must not, by itself, disable
	// the requirement). A separate "staging || production" disjunct would
	// be dead here: for any environment other than "development" the
	// negated clause below is unconditionally true regardless, so
	// staging/production's force-to-true falls entirely out of "not
	// (development with the dev flag)".
	localCompositionReadyRequested, _ := boolValue(lookup, "ACR_LOCAL_COMPOSITION_READY", false)
	requireStoresDefault := !(environment == "development" && localCompositionReadyRequested)
	logLevel, err := parseLogLevel(stringValue(lookup, "ACR_LOG_LEVEL", "info"))
	if err != nil {
		return ProjectorConfig{}, err
	}
	cfg := ProjectorConfig{
		Environment: environment, LogLevel: logLevel,
		ListenAddress: stringValue(lookup, envProjectorListenAddress, defaultProjectorListenAddress),
	}
	// Reuses the exact acr-api Postgres/ClickHouse env var names and
	// loading/validation (ACR_POSTGRES_DSN, ACR_CLICKHOUSE_DSN, pool knobs,
	// ACR_REQUIRE_BACKING_STORES): both binaries are configured against the
	// same instances, and this keeps that one loading path authoritative.
	var hosted Config
	// r1 P2 finding 1 (class sweep): same fix as acr-api's load() in
	// config.go -- the dev opt-out flag is the ONLY way to turn backing
	// stores off for the projector too.
	if err := loadHostedRuntimeValues(lookup, &hosted, requireStoresDefault, requireStoresDefault); err != nil {
		return ProjectorConfig{}, err
	}
	cfg.ClickHouseDSN, cfg.ClickHouseCACertPath = hosted.ClickHouseDSN, hosted.ClickHouseCACertPath
	cfg.ClickHouseMaxBytesToRead = hosted.ClickHouseMaxBytesToRead
	cfg.PostgresDSN, cfg.PostgresPoolerAdminDSN = hosted.PostgresDSN, hosted.PostgresPoolerAdminDSN
	cfg.PostgresConnectionKind = hosted.PostgresConnectionKind
	cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns = hosted.PostgresMaxOpenConns, hosted.PostgresMaxIdleConns
	cfg.PostgresMaxIdleConnsConfigured = hosted.PostgresMaxIdleConnsConfigured
	cfg.PostgresConnMaxLifetime, cfg.PostgresConnMaxIdleTime = hosted.PostgresConnMaxLifetime, hosted.PostgresConnMaxIdleTime
	cfg.PostgresPingTimeout = hosted.PostgresPingTimeout
	if cfg.PostgresPingTimeout <= 0 {
		cfg.PostgresPingTimeout = defaultProjectorPingTimeout
	}
	cfg.RequireBackingStores = hosted.RequireBackingStores
	cfg.LocalCompositionReady = hosted.LocalCompositionReady

	if cfg.ProjectionEnabled, err = boolValue(lookup, envContextFabricProjection, false); err != nil {
		return ProjectorConfig{}, err
	}
	cfg.OrgIDs = stringListValue(lookup, envContextFabricProjectorOrgs)
	if cfg.PollInterval, err = durationValue(lookup, envContextFabricPollInterval, defaultProjectionPollInterval); err != nil {
		return ProjectorConfig{}, err
	}
	if cfg.Concurrency, err = intValue(lookup, envContextFabricConcurrency, defaultProjectionConcurrency); err != nil {
		return ProjectorConfig{}, err
	}
	if cfg.DrainBatchBudget, err = intValue(lookup, envContextFabricDrainBudget, defaultProjectionDrainBudget); err != nil {
		return ProjectorConfig{}, err
	}
	// Defaults to true as of CHAOS-3802 (was false). The old default existed
	// only because devhealthsource.TeamsProjectsSource was a stub that failed
	// loudly when switched on; now that it is a real canonical source, a
	// default-off feature whose acceptance criterion ("an org rebuild picks
	// the new kinds up with no second migration") requires it on would be a
	// dead guard. Still explicitly disableable -- this gates ONLY the
	// teams/projects source, never the repository/work-item projection that
	// ACR_CONTEXT_FABRIC_PROJECTION_ENABLED above governs.
	if cfg.TeamsProjectsEnabled, err = boolValue(lookup, envContextFabricTeamsProjects, true); err != nil {
		return ProjectorConfig{}, err
	}

	if err := cfg.validate(required); err != nil {
		return ProjectorConfig{}, err
	}
	return cfg, nil
}

// Validate checks cfg against the FULL backing-store requirement
// (Postgres AND ClickHouse) -- every acr-projector command except priors.
// cmd/acr-projector/main.go's rebuild/rollback re-validate after forcing
// ProjectionEnabled=true using this same zero-argument form, since they
// too need the full set.
func (c ProjectorConfig) Validate() error {
	return c.validate(requiredStoresAll)
}

func (c ProjectorConfig) validate(required requiredStores) error {
	switch c.Environment {
	case "development", "test", "staging", "production":
	default:
		return fmt.Errorf("%s must be development, test, staging, or production", envProjectorEnvironment)
	}
	if strings.TrimSpace(c.ListenAddress) == "" {
		return fmt.Errorf("%s is required", envProjectorListenAddress)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("%s must be positive", envContextFabricPollInterval)
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("%s must be at least 1", envContextFabricConcurrency)
	}
	if c.ClickHouseMaxBytesToRead == 0 {
		return errors.New("ACR_CLICKHOUSE_MAX_BYTES_TO_READ must be positive")
	}
	if c.LocalCompositionReady && (c.Environment != "development" || c.RequireBackingStores) {
		return errors.New("ACR_LOCAL_COMPOSITION_READY requires development with backing stores disabled")
	}
	if c.RequireBackingStores {
		// r2 P1 finding: required is per-CALLER (see requiredStores' own
		// doc comment) -- a Postgres-only caller (priors) must never be
		// refused for a ClickHouse DSN it never opens, and a full-stack
		// caller (serve/rebuild/rollback) must still require both.
		if required.postgres && strings.TrimSpace(c.PostgresDSN) == "" {
			return errors.New("ACR_POSTGRES_DSN is required when backing stores are required")
		}
		if required.clickhouse && strings.TrimSpace(c.ClickHouseDSN) == "" {
			return errors.New("ACR_CLICKHOUSE_DSN is required when backing stores are required")
		}
	}
	if c.ProjectionEnabled && c.RequireBackingStores && len(c.OrgIDs) == 0 {
		return fmt.Errorf("%s is required when %s is true in an environment that requires backing stores", envContextFabricProjectorOrgs, envContextFabricProjection)
	}
	return nil
}

// SafeAttributes returns content-safe structured-logging fields: counts and
// booleans only, never DSNs or org identifiers as free text at startup.
func (c ProjectorConfig) SafeAttributes() []any {
	return []any{
		"environment", c.Environment, "listen_address", c.ListenAddress,
		"projection_enabled", c.ProjectionEnabled,
		"organization_count", len(c.OrgIDs), "poll_interval", c.PollInterval.String(),
		"concurrency", c.Concurrency, "drain_batch_budget", c.DrainBatchBudget, "teams_projects_enabled", c.TeamsProjectsEnabled,
		"require_backing_stores", c.RequireBackingStores, "local_composition_ready", c.LocalCompositionReady,
	}
}

// GraphReadsEnabledEnvVar is the independent graph-read enablement flag
// CHAOS-3755's hosted composition reads (see loadHostedRuntimeValues in
// hosted_runtime.go, which sets Config.EnableContextFabricInvestigations
// from it) -- not read in this file, which owns the write-side projector
// config instead. Exported so both lanes, and Helm/Compose docs, agree on
// the exact spelling.
const GraphReadsEnabledEnvVar = envContextFabricGraphReads
