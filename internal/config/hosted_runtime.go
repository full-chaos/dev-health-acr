package config

import (
	"errors"
	"net/url"
	"strings"
	"time"

	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
)

const defaultHostedPostgresPingTimeout = 5 * time.Second

func loadHostedRuntimeValues(lookup lookupEnv, cfg *Config, requiredByEnvironment bool) error {
	var err error
	if cfg.ClickHouseDSN, err = SecretValue(lookup, "ACR_CLICKHOUSE_DSN"); err != nil {
		return err
	}
	cfg.ClickHouseCACertPath = stringValue(lookup, "ACR_CLICKHOUSE_CA_BUNDLE", "")
	if cfg.PostgresDSN, err = SecretValue(lookup, "ACR_POSTGRES_DSN"); err != nil {
		return err
	}
	if cfg.PostgresPoolerAdminDSN, err = SecretValue(lookup, "ACR_POSTGRES_POOLER_ADMIN_DSN"); err != nil {
		return err
	}
	cfg.PostgresConnectionKind = stringValue(lookup, "ACR_POSTGRES_CONNECTION_KIND", "")
	if cfg.PostgresMaxOpenConns, err = intValue(lookup, "ACR_POSTGRES_MAX_OPEN_CONNS", 0); err != nil {
		return err
	}
	if cfg.PostgresMaxIdleConns, err = intValue(lookup, "ACR_POSTGRES_MAX_IDLE_CONNS", 0); err != nil {
		return err
	}
	if raw, ok := lookup("ACR_POSTGRES_MAX_IDLE_CONNS"); ok && strings.TrimSpace(raw) != "" {
		cfg.PostgresMaxIdleConnsConfigured = true
	}
	if cfg.PostgresConnMaxLifetime, err = durationValue(lookup, "ACR_POSTGRES_CONN_MAX_LIFETIME", 0); err != nil {
		return err
	}
	if cfg.PostgresConnMaxIdleTime, err = durationValue(lookup, "ACR_POSTGRES_CONN_MAX_IDLE_TIME", 0); err != nil {
		return err
	}
	if cfg.PostgresPingTimeout, err = durationValue(lookup, "ACR_POSTGRES_PING_TIMEOUT", defaultHostedPostgresPingTimeout); err != nil {
		return err
	}
	if cfg.RequireBackingStores, err = boolValue(lookup, "ACR_REQUIRE_BACKING_STORES", requiredByEnvironment); err != nil {
		return err
	}
	cfg.RequireBackingStores = cfg.RequireBackingStores || requiredByEnvironment
	if cfg.LocalCompositionReady, err = boolValue(lookup, "ACR_LOCAL_COMPOSITION_READY", false); err != nil {
		return err
	}
	if cfg.EnableEpisodeWriteback, err = boolValue(lookup, "ACR_ENABLE_EPISODE_WRITEBACK", false); err != nil {
		return err
	}
	// GraphReadsEnabledEnvVar ("ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED") is
	// the flag CHAOS-3753's design reserved for exactly this: the
	// independent read-side (investigation endpoint) enablement, distinct
	// from ACR_CONTEXT_FABRIC_PROJECTION_ENABLED's write-side. Reusing it
	// here (rather than inventing a second flag) is what CHAOS-3755 must
	// do per that reservation.
	if cfg.EnableContextFabricInvestigations, err = boolValue(lookup, GraphReadsEnabledEnvVar, false); err != nil {
		return err
	}
	return nil
}

func validateHostedRuntime(cfg Config) error {
	connectionKindErr := validatePostgresConnectionKind(cfg)
	switch {
	case strings.TrimSpace(cfg.ClickHouseDSN) == "":
		return errors.New("ACR_CLICKHOUSE_DSN is required when backing stores are required")
	case strings.TrimSpace(cfg.PostgresDSN) == "":
		return errors.New("ACR_POSTGRES_DSN is required when backing stores are required")
	case !hasActiveEvidenceIDKey(cfg):
		return errors.New("ACR_EVIDENCE_ID_ACTIVE_KID and ACR_EVIDENCE_ID_KEYS must configure an active evidence key when backing stores are required")
	case strings.TrimSpace(cfg.DeviceVerificationURL) == "":
		return errors.New("ACR_DEVICE_VERIFICATION_URL is required when backing stores are required")
	case !isAbsoluteDeviceVerificationURL(cfg.DeviceVerificationURL):
		return errors.New("ACR_DEVICE_VERIFICATION_URL must be an absolute URL")
	case cfg.PostgresMaxOpenConns < 0 || cfg.PostgresMaxIdleConns < 0:
		return errors.New("ACR PostgreSQL pool bounds must not be negative")
	case cfg.PostgresMaxOpenConns > 0 && cfg.PostgresMaxIdleConns > cfg.PostgresMaxOpenConns:
		return errors.New("ACR_POSTGRES_MAX_IDLE_CONNS must not exceed ACR_POSTGRES_MAX_OPEN_CONNS")
	case cfg.PostgresConnMaxLifetime < 0 || cfg.PostgresConnMaxIdleTime < 0 || cfg.PostgresPingTimeout < 0:
		return errors.New("ACR PostgreSQL pool durations must not be negative")
	case connectionKindErr != nil:
		return connectionKindErr
	default:
		return nil
	}
}

func isAbsoluteDeviceVerificationURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.IsAbs() && parsed.Host != ""
}

// validatePostgresConnectionKind requires an explicit ACR_POSTGRES_CONNECTION_KIND
// for the hosted runtime and rejects configurations where the declared kind
// contradicts the presence of a PgBouncer administration DSN.
func validatePostgresConnectionKind(cfg Config) error {
	kind, err := runtimepostgres.ParseConnectionKind(cfg.PostgresConnectionKind)
	if err != nil {
		return errors.New("ACR_POSTGRES_CONNECTION_KIND is required when backing stores are required and must be direct or pgbouncer")
	}
	return runtimepostgres.ValidateConnectionKind(kind, cfg.PostgresPoolerAdminDSN)
}

func hasActiveEvidenceIDKey(c Config) bool {
	return validEvidenceKID(c.EvidenceIDActiveKID) && len(c.EvidenceIDKeys[c.EvidenceIDActiveKID]) >= evidenceIDKeyMinimumBytes
}

// SafeAttributes returns operational configuration without secret-bearing DSNs.
func (c Config) SafeAttributes() []any {
	return []any{
		"environment", c.Environment,
		"listen_address", c.ListenAddress,
		"backing_stores_required", c.RequireBackingStores,
		"local_composition_ready", c.LocalCompositionReady,
		"clickhouse_configured", c.ClickHouseDSN != "",
		"clickhouse_ca_bundle_configured", c.ClickHouseCACertPath != "",
		"postgres_configured", c.PostgresDSN != "",
		"postgres_pooler_admin_configured", c.PostgresPoolerAdminDSN != "",
		"postgres_max_idle_conns_configured", c.PostgresMaxIdleConnsConfigured,
		"episode_writeback_enabled", c.EnableEpisodeWriteback,
		"context_fabric_investigations_enabled", c.EnableContextFabricInvestigations,
		"minimum_sidecar_version", c.MinimumSidecarVersion,
		"entitlement_key", c.EntitlementKey,
		"entitlement_mode", string(c.EntitlementMode()),
		"evidence_id_active_kid", c.EvidenceIDActiveKID,
		"evidence_id_key_count", len(c.EvidenceIDKeys),
		"trusted_proxy_count", len(c.TrustedProxyCIDRs),
		"dev_health_entitlement_configured", c.DevHealthEntitlementURL != "",
		"dev_health_entitlement_token_file_configured", c.DevHealthEntitlementTokenFile != "",
		"web_assertions_configured", c.WebAssertionJWKSFile != "",
		"device_verification_configured", c.DeviceVerificationURL != "",
	}
}
