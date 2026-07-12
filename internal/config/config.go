package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

const (
	defaultListenAddress      = ":8080"
	defaultEnvironment        = "development"
	defaultMinimumSidecar     = "0.1.0"
	defaultEntitlementKey     = "agent_context_runtime"
	defaultRequestTimeout     = 15 * time.Second
	defaultReadHeaderTimeout  = 5 * time.Second
	defaultReadTimeout        = 20 * time.Second
	defaultWriteTimeout       = 20 * time.Second
	defaultIdleTimeout        = 60 * time.Second
	defaultShutdownTimeout    = 10 * time.Second
	defaultMaxItems           = 30
	defaultMaxOutputTokens    = 4000
	defaultMaxSerializedBytes = 262144
	defaultRequestsPerMinute  = 60
)

// Config contains only process-level configuration. Credentials and request
// identity are resolved by dedicated services and must never be stored here.
type Config struct {
	Environment           string
	ListenAddress         string
	LogLevel              slog.Level
	RequestTimeout        time.Duration
	ReadHeaderTimeout     time.Duration
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	ShutdownTimeout       time.Duration
	ClickHouseDSN         string
	PostgresDSN           string
	RequireBackingStores  bool
	MinimumSidecarVersion string
	RevokedClientVersions []string
	EntitlementKey        string
	EvidenceIDActiveKID   string
	EvidenceIDKeys        map[string][]byte
	MaxItems              int
	MaxOutputTokens       int
	MaxSerializedBytes    int
	RequestsPerMinute     int
	RequestControls       RequestControlsConfig
	TrustedProxyCIDRs     []string
}

type lookupEnv func(string) (string, bool)

// Load reads configuration from the process environment and validates it.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup lookupEnv) (Config, error) {
	environment := stringValue(lookup, "ACR_ENVIRONMENT", defaultEnvironment)
	requireStoresDefault := environment == "staging" || environment == "production"

	logLevel, err := parseLogLevel(stringValue(lookup, "ACR_LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment:           environment,
		ListenAddress:         stringValue(lookup, "ACR_ADDR", defaultListenAddress),
		LogLevel:              logLevel,
		ClickHouseDSN:         stringValue(lookup, "ACR_CLICKHOUSE_DSN", ""),
		PostgresDSN:           stringValue(lookup, "ACR_POSTGRES_DSN", ""),
		MinimumSidecarVersion: stringValue(lookup, "ACR_MINIMUM_SIDECAR_VERSION", defaultMinimumSidecar),
		RevokedClientVersions: stringListValue(lookup, "ACR_REVOKED_CLIENT_VERSIONS"),
		EntitlementKey:        stringValue(lookup, "ACR_ENTITLEMENT_KEY", defaultEntitlementKey),
		EvidenceIDActiveKID:   stringValue(lookup, "ACR_EVIDENCE_ID_ACTIVE_KID", ""),
		TrustedProxyCIDRs:     stringListValue(lookup, "ACR_TRUSTED_PROXY_CIDRS"),
	}
	if cfg.EvidenceIDKeys, err = evidenceIDKeysValue(lookup); err != nil {
		return Config{}, err
	}

	if cfg.RequestTimeout, err = durationValue(lookup, "ACR_REQUEST_TIMEOUT", defaultRequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadHeaderTimeout, err = durationValue(lookup, "ACR_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadTimeout, err = durationValue(lookup, "ACR_READ_TIMEOUT", defaultReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WriteTimeout, err = durationValue(lookup, "ACR_WRITE_TIMEOUT", defaultWriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = durationValue(lookup, "ACR_IDLE_TIMEOUT", defaultIdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = durationValue(lookup, "ACR_SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RequireBackingStores, err = boolValue(lookup, "ACR_REQUIRE_BACKING_STORES", requireStoresDefault); err != nil {
		return Config{}, err
	}
	if cfg.MaxItems, err = intValue(lookup, "ACR_MAX_ITEMS", defaultMaxItems); err != nil {
		return Config{}, err
	}
	if cfg.MaxOutputTokens, err = intValue(lookup, "ACR_MAX_OUTPUT_TOKENS", defaultMaxOutputTokens); err != nil {
		return Config{}, err
	}
	if cfg.MaxSerializedBytes, err = intValue(lookup, "ACR_MAX_SERIALIZED_BYTES", defaultMaxSerializedBytes); err != nil {
		return Config{}, err
	}
	if cfg.RequestsPerMinute, err = intValue(lookup, "ACR_REQUESTS_PER_MINUTE", defaultRequestsPerMinute); err != nil {
		return Config{}, err
	}
	if cfg.RequestControls, err = requestControlsValue(lookup, cfg.RequestsPerMinute); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.Environment {
	case "development", "test", "staging", "production":
	default:
		return fmt.Errorf("ACR_ENVIRONMENT must be development, test, staging, or production")
	}
	if strings.TrimSpace(c.ListenAddress) == "" {
		return errors.New("ACR_ADDR is required")
	}
	for name, value := range map[string]time.Duration{
		"ACR_REQUEST_TIMEOUT":     c.RequestTimeout,
		"ACR_READ_HEADER_TIMEOUT": c.ReadHeaderTimeout,
		"ACR_READ_TIMEOUT":        c.ReadTimeout,
		"ACR_WRITE_TIMEOUT":       c.WriteTimeout,
		"ACR_IDLE_TIMEOUT":        c.IdleTimeout,
		"ACR_SHUTDOWN_TIMEOUT":    c.ShutdownTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if strings.TrimSpace(c.MinimumSidecarVersion) == "" {
		return errors.New("ACR_MINIMUM_SIDECAR_VERSION is required")
	}
	if !version.IsCanonical(c.MinimumSidecarVersion) {
		return errors.New("ACR_MINIMUM_SIDECAR_VERSION must be canonical SemVer")
	}
	for _, revoked := range c.RevokedClientVersions {
		if !version.IsCanonical(revoked) {
			return errors.New("ACR_REVOKED_CLIENT_VERSIONS must contain canonical SemVer versions")
		}
	}
	if strings.TrimSpace(c.EntitlementKey) == "" {
		return errors.New("ACR_ENTITLEMENT_KEY is required")
	}
	if c.MaxItems < 1 || c.MaxItems > 50 {
		return errors.New("ACR_MAX_ITEMS must be between 1 and 50")
	}
	if c.MaxOutputTokens < 500 || c.MaxOutputTokens > 16000 {
		return errors.New("ACR_MAX_OUTPUT_TOKENS must be between 500 and 16000")
	}
	if c.MaxSerializedBytes < 8192 || c.MaxSerializedBytes > 1048576 {
		return errors.New("ACR_MAX_SERIALIZED_BYTES must be between 8192 and 1048576")
	}
	if c.RequestsPerMinute < 1 {
		return errors.New("ACR_REQUESTS_PER_MINUTE must be positive")
	}
	if err := c.RequestControls.validate(); err != nil {
		return err
	}
	if err := validateTrustedProxyCIDRs(c.TrustedProxyCIDRs); err != nil {
		return err
	}
	if c.Environment == "production" && !hasActiveEvidenceIDKey(c) {
		return errors.New("ACR_EVIDENCE_ID_ACTIVE_KID and ACR_EVIDENCE_ID_KEYS must configure an active evidence key in production")
	}
	if c.RequireBackingStores {
		if strings.TrimSpace(c.ClickHouseDSN) == "" {
			return errors.New("ACR_CLICKHOUSE_DSN is required when backing stores are required")
		}
		if strings.TrimSpace(c.PostgresDSN) == "" {
			return errors.New("ACR_POSTGRES_DSN is required when backing stores are required")
		}
	}
	return nil
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
		"clickhouse_configured", c.ClickHouseDSN != "",
		"postgres_configured", c.PostgresDSN != "",
		"minimum_sidecar_version", c.MinimumSidecarVersion,
		"entitlement_key", c.EntitlementKey,
		"evidence_id_active_kid", c.EvidenceIDActiveKID,
		"evidence_id_key_count", len(c.EvidenceIDKeys),
		"trusted_proxy_count", len(c.TrustedProxyCIDRs),
	}
}
