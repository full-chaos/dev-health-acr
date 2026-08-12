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
	defaultProjectorListenAddress = ":8090"
	defaultProjectionPollInterval = 15 * time.Second
	defaultProjectionConcurrency  = 4
	defaultProjectorPingTimeout   = 5 * time.Second
	envContextFabricProjection    = "ACR_CONTEXT_FABRIC_PROJECTION_ENABLED"
	envContextFabricProjectorOrgs = "ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS"
	envContextFabricPollInterval  = "ACR_CONTEXT_FABRIC_PROJECTION_POLL_INTERVAL"
	envContextFabricConcurrency   = "ACR_CONTEXT_FABRIC_PROJECTION_CONCURRENCY"
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

	// ProjectionEnabled is this binary's own master switch (independent of
	// ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED, which gates graph reads in
	// acr-api/hosted composition -- Reset 1B/1C scope, reserved here so the
	// two lanes don't collide on the name). When false the coordinator loop
	// never starts; the readiness server still runs and reports disabled.
	ProjectionEnabled bool
	// OrgIDs is the explicit allowlist of organizations to project. See
	// docs/design/context-fabric-projection-worker.md for why this starts
	// as an explicit list rather than auto-discovery.
	OrgIDs               []string
	PollInterval         time.Duration
	Concurrency          int
	TeamsProjectsEnabled bool
}

// LoadProjector reads cmd/acr-projector's configuration from the process
// environment and validates it.
func LoadProjector() (ProjectorConfig, error) {
	return loadProjector(os.LookupEnv)
}

func loadProjector(lookup lookupEnv) (ProjectorConfig, error) {
	environment := stringValue(lookup, envProjectorEnvironment, defaultEnvironment)
	requireStoresDefault := environment == "staging" || environment == "production"
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
	if err := loadHostedRuntimeValues(lookup, &hosted, requireStoresDefault); err != nil {
		return ProjectorConfig{}, err
	}
	cfg.ClickHouseDSN, cfg.ClickHouseCACertPath = hosted.ClickHouseDSN, hosted.ClickHouseCACertPath
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
	if cfg.TeamsProjectsEnabled, err = boolValue(lookup, envContextFabricTeamsProjects, false); err != nil {
		return ProjectorConfig{}, err
	}

	if err := cfg.Validate(); err != nil {
		return ProjectorConfig{}, err
	}
	return cfg, nil
}

func (c ProjectorConfig) Validate() error {
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
	if c.RequireBackingStores {
		if strings.TrimSpace(c.ClickHouseDSN) == "" {
			return errors.New("ACR_CLICKHOUSE_DSN is required when backing stores are required")
		}
		if strings.TrimSpace(c.PostgresDSN) == "" {
			return errors.New("ACR_POSTGRES_DSN is required when backing stores are required")
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
		"environment", c.Environment, "projection_enabled", c.ProjectionEnabled,
		"organization_count", len(c.OrgIDs), "poll_interval", c.PollInterval.String(),
		"concurrency", c.Concurrency, "teams_projects_enabled", c.TeamsProjectsEnabled,
		"require_backing_stores", c.RequireBackingStores,
	}
}

// GraphReadsEnabledEnvVar is the reserved name for the independent
// graph-read enablement flag Reset 1B/1C's hosted composition owns. It is
// not read here; it exists so both lanes agree on the exact spelling.
const GraphReadsEnabledEnvVar = envContextFabricGraphReads
