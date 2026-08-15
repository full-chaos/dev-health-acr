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
	// minAnswerReuseMaxAge and maxAnswerReuseMaxAge bound
	// ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE when it is set at all --
	// see Config.AnswerReuseMaxAge's doc comment. There is deliberately
	// no default duration: answer reuse is OFF (AnswerReuseMaxAge == 0)
	// unless an operator explicitly opts in by setting this variable.
	minAnswerReuseMaxAge = time.Minute
	maxAnswerReuseMaxAge = 24 * time.Hour
)

// Config contains only process-level configuration. Credentials and request
// identity are resolved by dedicated services and must never be stored here.
type Config struct {
	Environment          string
	ListenAddress        string
	LogLevel             slog.Level
	RequestTimeout       time.Duration
	ReadHeaderTimeout    time.Duration
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	ShutdownTimeout      time.Duration
	ClickHouseDSN        string
	ClickHouseCACertPath string
	// ClickHouseMaxBytesToRead (CHAOS-3848, ACR_CLICKHOUSE_MAX_BYTES_TO_READ)
	// is the per-query max_bytes_to_read ceiling handed to every production
	// ClickHouse client (internal/runtime/clickhouse.Options.MaxBytesToRead).
	// Falls back to runtimeclickhouse.DefaultMaxBytesToRead when unset; an
	// explicitly configured zero is rejected by Validate rather than silently
	// reinterpreted as "unset".
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
	LocalCompositionReady          bool
	EnableEpisodeWriteback         bool
	// EnableContextFabricInvestigations gates hosted composition attempting
	// to construct a real contextfabric.Investigator (CHAOS-3755). Reads
	// GraphReadsEnabledEnvVar ("ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED"),
	// the flag reserved for exactly this since CHAOS-3753's design. Even
	// when true, the investigator is only wired if the graph backend is
	// separately configured (falkorgraph.Configured) -- composition never
	// fails closed over an unconfigured optional dependency, matching the
	// convention ADR 0007 established (and ADR 0009 carries forward) for
	// the projection worker.
	EnableContextFabricInvestigations bool
	// AnswerReuseMaxAge (CHAOS-3782, ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE)
	// is the staleness window TRD §19.7.3 condition 4 enforces: a stored
	// investigation result older than this is never reused, regardless of
	// whether every other reuse condition holds.
	//
	// ZERO MEANS DISABLED. Unlike every other duration in this Config,
	// there is no default: leaving the environment variable unset leaves
	// this at its zero value, and hosted composition (internal/runtime/hosted)
	// treats that as "do not turn on answer reuse at all" -- an operator
	// must explicitly opt in by setting a window. Validate() below allows
	// exactly zero through unchecked; any NON-zero value must fall inside
	// [minAnswerReuseMaxAge, maxAnswerReuseMaxAge].
	//
	// D15 HAZARD (TRD §19.2/§19.7.3), read before setting this: the
	// projection cursor is event-time based, so a backfilled or corrected
	// source row does NOT advance backend_watermark and is not
	// re-observed until a full rebuild. Watermark equality (condition 3)
	// therefore CANNOT by itself prove a stored answer is still accurate
	// -- a source row could change underneath an unchanged watermark.
	// This window is the second, independent bound that limits how long
	// a result can be served without that guarantee, and rebuild
	// invalidation (AC-3782-4, wired through
	// contextfabric.ReuseInvalidator) is the other one. Set this
	// conservatively: it must be short enough that a plausible backfill
	// lag in your deployment's canonical sources is very unlikely to fall
	// entirely inside one window, because nothing else catches that case
	// between rebuilds.
	AnswerReuseMaxAge                    time.Duration
	MinimumSidecarVersion                string
	RevokedClientVersions                []string
	EntitlementKey                       string
	EvidenceIDActiveKID                  string
	EvidenceIDKeys                       map[string][]byte
	MaxItems                             int
	MaxOutputTokens                      int
	MaxSerializedBytes                   int
	RequestsPerMinute                    int
	RequestControls                      RequestControlsConfig
	TrustedProxyCIDRs                    []string
	DevHealthEntitlementURL              string
	DevHealthEntitlementTokenFile        string
	DevHealthEntitlementTimeout          time.Duration
	DevHealthEntitlementMaxResponseBytes int64
	DevHealthEntitlementProxyURL         string
	DevHealthEntitlementCACertPath       string
	WebAssertionIssuer                   string
	WebAssertionAudience                 string
	WebAssertionJWKSFile                 string
	DeviceVerificationURL                string
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
	evidenceIDActiveKID, err := SecretValue(lookup, "ACR_EVIDENCE_ID_ACTIVE_KID")
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment:                    environment,
		ListenAddress:                  stringValue(lookup, "ACR_ADDR", defaultListenAddress),
		LogLevel:                       logLevel,
		MinimumSidecarVersion:          stringValue(lookup, "ACR_MINIMUM_SIDECAR_VERSION", defaultMinimumSidecar),
		RevokedClientVersions:          stringListValue(lookup, "ACR_REVOKED_CLIENT_VERSIONS"),
		EntitlementKey:                 stringValue(lookup, "ACR_ENTITLEMENT_KEY", defaultEntitlementKey),
		EvidenceIDActiveKID:            evidenceIDActiveKID,
		TrustedProxyCIDRs:              stringListValue(lookup, "ACR_TRUSTED_PROXY_CIDRS"),
		DevHealthEntitlementURL:        stringValue(lookup, "ACR_DEV_HEALTH_ENTITLEMENT_URL", ""),
		DevHealthEntitlementTokenFile:  stringValue(lookup, "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE", ""),
		DevHealthEntitlementProxyURL:   stringValue(lookup, "ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL", ""),
		DevHealthEntitlementCACertPath: stringValue(lookup, "ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE", ""),
		WebAssertionIssuer:             stringValue(lookup, "ACR_WEB_ASSERTION_ISSUER", ""),
		WebAssertionAudience:           stringValue(lookup, "ACR_WEB_ASSERTION_AUDIENCE", ""),
		WebAssertionJWKSFile:           stringValue(lookup, "ACR_WEB_ASSERTION_JWKS_FILE", ""),
		DeviceVerificationURL:          stringValue(lookup, "ACR_DEVICE_VERIFICATION_URL", ""),
	}
	if err := loadHostedRuntimeValues(lookup, &cfg, requireStoresDefault); err != nil {
		return Config{}, err
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
	if cfg.DevHealthEntitlementTimeout, err = durationValue(lookup, "ACR_DEV_HEALTH_ENTITLEMENT_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}
	// No default: an unset variable leaves AnswerReuseMaxAge at zero,
	// which means "answer reuse disabled" -- see the field's doc comment.
	if cfg.AnswerReuseMaxAge, err = durationValue(lookup, "ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE", 0); err != nil {
		return Config{}, err
	}
	devHealthEntitlementMaxResponseBytes, err := intValue(lookup, "ACR_DEV_HEALTH_ENTITLEMENT_MAX_RESPONSE_BYTES", 16<<10)
	if err != nil {
		return Config{}, err
	}
	cfg.DevHealthEntitlementMaxResponseBytes = int64(devHealthEntitlementMaxResponseBytes)
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
	if c.EntitlementKey != defaultEntitlementKey {
		return errors.New("ACR_ENTITLEMENT_KEY must be agent_context_runtime")
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
	if c.ClickHouseMaxBytesToRead == 0 {
		return errors.New("ACR_CLICKHOUSE_MAX_BYTES_TO_READ must be positive")
	}
	// Zero (unset) means answer reuse is disabled -- a valid, deliberate
	// state, not a bounds violation. Any other value must be a sane
	// window: this also rejects a negative duration, which
	// time.ParseDuration accepts syntactically ("-5m") but which would
	// mean every candidate row is already "in the future" and always
	// rejected -- a silent, confusing way to disable reuse that the
	// explicit zero-means-disabled convention exists to avoid.
	if c.AnswerReuseMaxAge != 0 && (c.AnswerReuseMaxAge < minAnswerReuseMaxAge || c.AnswerReuseMaxAge > maxAnswerReuseMaxAge) {
		return fmt.Errorf("ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE must be unset (disabled) or between %s and %s", minAnswerReuseMaxAge, maxAnswerReuseMaxAge)
	}
	if err := c.RequestControls.validate(); err != nil {
		return err
	}
	if err := validateTrustedProxyCIDRs(c.TrustedProxyCIDRs); err != nil {
		return err
	}
	if err := validateEntitlementConfiguration(c); err != nil {
		return err
	}
	if c.LocalCompositionReady && (c.Environment != "development" || c.RequireBackingStores) {
		return errors.New("ACR_LOCAL_COMPOSITION_READY requires development with backing stores disabled")
	}
	if c.RequireBackingStores {
		if err := validateHostedRuntime(c); err != nil {
			return err
		}
	}
	if err := validateWebAssertionConfig(c); err != nil {
		return err
	}
	return nil
}
