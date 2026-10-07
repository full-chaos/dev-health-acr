package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type AppConfig struct {
	ServiceName              string
	ServiceVersion           string
	RequestTimeout           time.Duration
	MaxRequestBodyBytes      int64
	MaxEvidenceResponseBytes int64
	MaxItems                 int
	MaxOutputTokens          int
	MaxSerializedBytes       int
	RevokedClientVersions    []string
	// ServerCompletenessAuthorityEnabled mirrors
	// config.Config.ServerCompletenessAuthorityEnabled's own doc comment --
	// read live by the by-id investigation-result route, since a stored
	// row never reaches the engine's own gated flip (finalizeServed).
	ServerCompletenessAuthorityEnabled bool
	// ServerCompletenessAuthoritySymmetricEnabled (CHAOS-5743) mirrors
	// config.Config.ServerCompletenessAuthoritySymmetricEnabled's own doc
	// comment, for the same by-id route reason immediately above.
	ServerCompletenessAuthoritySymmetricEnabled bool
}

type Dependencies struct {
	Capabilities         CapabilitiesProvider
	ReadinessChecks      []ReadinessCheck
	Now                  func() time.Time
	RequestID            func() string
	Observability        *observability.Hooks
	Limits               *limits.Manager
	AuthAttempts         auth.AttemptLimiter
	EvidenceStoreFactory contextpacket.EvidenceStoreFactory
	Runtime              *RuntimeDependencies
	ClientIP             auth.ClientIPResolver
	WebAssertions        *auth.WebAssertionVerifier
	// WebAssertionReplays is the shared used-id record the hosted runtime
	// provides; the web assertion verifier fails closed without it.
	WebAssertionReplays auth.WebAssertionReplayStore
	UsageTelemetry      *auth.UsageTelemetry
	// Metrics counts the investigation outcomes the route certifies. nil
	// records nothing.
	Metrics *hostedmetrics.Instruments
}

type App struct {
	config          AppConfig
	capabilities    CapabilitiesProvider
	readinessChecks []ReadinessCheck
	// dataStoreChecks (CHAOS-6745) are the ClickHouse-backed checks that
	// gate the ClickHouse-dependent routes ONLY -- never /readyz. See
	// RuntimeDependencies.DataStoreChecks and requireDataStoresReady.
	dataStoreChecks      []ReadinessCheck
	now                  func() time.Time
	requestID            func() string
	logger               *slog.Logger
	observability        observability.Hooks
	limits               *limits.Manager
	authAttempts         auth.AttemptLimiter
	evidenceStoreFactory contextpacket.EvidenceStoreFactory
	runtime              *RuntimeDependencies
	authenticator        *auth.Authenticator
	credentialService    *auth.Service
	deviceFlow           *auth.DeviceFlowService
	oauth                *auth.OAuthService
	oauthConsentURL      string
	clientIP             auth.ClientIPResolver
	usageTelemetry       *auth.UsageTelemetry
	metrics              *hostedmetrics.Instruments
	closers              appClosers
	// readinessTransitions (dictation 811) tracks the last OBSERVED /readyz
	// outcome -- aggregate AND per-check -- so handleReady can log a
	// transition line whenever either actually changes, instead of one
	// line per poll. See ReadinessTransitionLogger's own doc comment.
	readinessTransitions *ReadinessTransitionLogger
}

func NewApp(cfg AppConfig, deps Dependencies, logger *slog.Logger) (*App, error) {
	if strings.TrimSpace(cfg.ServiceName) == "" {
		return nil, errors.New("service name is required")
	}
	if strings.TrimSpace(cfg.ServiceVersion) == "" {
		return nil, errors.New("service version is required")
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("request timeout must be positive")
	}
	if cfg.MaxRequestBodyBytes == 0 {
		cfg.MaxRequestBodyBytes = 1 << 20
	}
	if cfg.MaxEvidenceResponseBytes == 0 {
		cfg.MaxEvidenceResponseBytes = 1 << 20
	}
	if cfg.MaxItems == 0 {
		cfg.MaxItems = 50
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 16_000
	}
	if cfg.MaxSerializedBytes == 0 {
		cfg.MaxSerializedBytes = 1 << 20
	}
	if cfg.MaxRequestBodyBytes < 1 || cfg.MaxEvidenceResponseBytes < 1 || cfg.MaxItems < 1 || cfg.MaxItems > 50 || cfg.MaxOutputTokens < 500 || cfg.MaxOutputTokens > 16_000 || cfg.MaxSerializedBytes < 8_192 || cfg.MaxSerializedBytes > 1<<20 {
		return nil, errors.New("hosted read limits are invalid")
	}
	if deps.Capabilities == nil {
		return nil, errors.New("capabilities provider is required")
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.RequestID == nil {
		deps.RequestID = newRequestID
	}
	if deps.Observability == nil {
		hooks := observability.NewHooks(nil, nil)
		deps.Observability = &hooks
	}
	if deps.AuthAttempts == nil {
		deps.AuthAttempts = auth.NoopLimiter{}
	}
	var authenticator *auth.Authenticator
	var credentialService *auth.Service
	var deviceFlow *auth.DeviceFlowService
	var oauth *auth.OAuthService
	if deps.Runtime != nil {
		if err := deps.Runtime.validate(); err != nil {
			return nil, err
		}
		if deps.Limits == nil {
			return nil, errors.New("hosted read runtime requires request controls")
		}
		var err error
		authenticator, err = auth.NewAuthenticator(deps.Runtime.Credentials, deps.Runtime.Audit, auth.AuthenticatorOptions{Now: deps.Now, Limiter: deps.AuthAttempts, Logger: logger, ClientIP: deps.ClientIP, WebAssertions: deps.WebAssertions, UsageTelemetry: deps.UsageTelemetry})
		if err != nil {
			return nil, err
		}
		if deps.UsageTelemetry == nil {
			deps.UsageTelemetry = authenticator.UsageTelemetry()
		}
		credentialService, err = auth.NewService(deps.Runtime.Credentials, auth.ServiceOptions{Now: deps.Now})
		if err != nil {
			return nil, err
		}
		// Wired here, before newOAuthService below constructs anything from it,
		// so Poll can refuse an RFC 8628 device code the moment OAuth login is
		// configured (deps.Runtime.OAuth is config data present on deps
		// already, not something derived from deviceFlow or oauth).
		// NewDeviceFlowService below REFUSES a nil OAuthDeviceGrants -- so a
		// future edit here that fails to wire the real store when OAuth IS
		// configured fails App construction loudly, never silently, in
		// contrast to a nil that Poll used to treat as "OAuth is off".
		oauthDeviceGrants := auth.OAuthDeviceGrantLookup(auth.NoOAuthDeviceGrants{})
		if deps.Runtime.OAuth != nil {
			oauthDeviceGrants = deps.Runtime.OAuth.Store
		}
		deviceFlow, err = auth.NewDeviceFlowService(deps.Runtime.DeviceAuthorizations, credentialService, auth.DeviceFlowOptions{Now: deps.Now, OAuthDeviceGrants: oauthDeviceGrants})
		if err != nil {
			return nil, err
		}
		deps.ReadinessChecks = append(deps.ReadinessChecks, deps.Runtime.ReadinessChecks...)
		oauth, err = newOAuthService(deps, deviceFlow)
		if err != nil {
			return nil, err
		}
	}
	for _, check := range deps.ReadinessChecks {
		if check == nil || strings.TrimSpace(check.Name()) == "" {
			return nil, errors.New("readiness checks require a name")
		}
	}
	// CHAOS-6745: dataStoreChecks travels separately from ReadinessChecks --
	// see RuntimeDependencies.DataStoreChecks's doc comment for why it must
	// never reach /readyz.
	var dataStoreChecks []ReadinessCheck
	if deps.Runtime != nil {
		for _, check := range deps.Runtime.DataStoreChecks {
			if check == nil || strings.TrimSpace(check.Name()) == "" {
				return nil, errors.New("data store readiness checks require a name")
			}
		}
		dataStoreChecks = append([]ReadinessCheck(nil), deps.Runtime.DataStoreChecks...)
	}
	app := &App{
		config:               cfg,
		capabilities:         deps.Capabilities,
		readinessChecks:      append([]ReadinessCheck(nil), deps.ReadinessChecks...),
		dataStoreChecks:      dataStoreChecks,
		now:                  deps.Now,
		requestID:            deps.RequestID,
		logger:               logger,
		observability:        *deps.Observability,
		limits:               deps.Limits,
		authAttempts:         deps.AuthAttempts,
		evidenceStoreFactory: deps.EvidenceStoreFactory,
		runtime:              deps.Runtime,
		authenticator:        authenticator,
		clientIP:             deps.ClientIP,
		usageTelemetry:       deps.UsageTelemetry,
		metrics:              deps.Metrics,
		credentialService:    credentialService,
		deviceFlow:           deviceFlow,
		oauth:                oauth,
		oauthConsentURL:      oauthConsentURL(deps),
		readinessTransitions: NewReadinessTransitionLogger(),
	}
	if app.clientIP == nil {
		app.clientIP = auth.RemoteAddressClientIP
	}
	app.trackAuthenticator(authenticator)
	return app, nil
}

func (a *App) ProtectedHandler(class limits.RequestClass, next http.Handler) http.Handler {
	return LimitMiddleware(a.limits, class, next)
}

func (a *App) AuthenticatedHandler(credentials storage.CredentialStore, audit storage.AuditStore, class limits.RequestClass, next http.Handler) (http.Handler, error) {
	authenticator, err := auth.NewAuthenticator(credentials, audit, auth.AuthenticatorOptions{Now: a.now, Limiter: a.authAttempts, Logger: a.logger, ClientIP: a.clientIP, UsageTelemetry: a.usageTelemetry})
	if err != nil {
		return nil, err
	}
	a.trackAuthenticator(authenticator)
	return authenticator.Middleware(a.ProtectedHandler(class, next)), nil
}

func (a *App) NewEvidenceStore(rows contextpacket.ClickHouseRows) (*contextpacket.ClickHouseEvidenceStore, error) {
	if a.evidenceStoreFactory == nil {
		return nil, errors.New("evidence store factory is not configured")
	}
	return a.evidenceStoreFactory(rows)
}

func oauthConsentURL(deps Dependencies) string {
	if deps.Runtime == nil || deps.Runtime.OAuth == nil {
		return ""
	}
	return deps.Runtime.OAuth.ConsentURL
}
