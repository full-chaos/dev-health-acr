package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
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
	UsageTelemetry       *auth.UsageTelemetry
}

type App struct {
	config               AppConfig
	capabilities         CapabilitiesProvider
	readinessChecks      []ReadinessCheck
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
	clientIP             auth.ClientIPResolver
	usageTelemetry       *auth.UsageTelemetry
	closers              appClosers
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
		deviceFlow, err = auth.NewDeviceFlowService(deps.Runtime.DeviceAuthorizations, credentialService, auth.DeviceFlowOptions{Now: deps.Now})
		if err != nil {
			return nil, err
		}
		deps.ReadinessChecks = append(deps.ReadinessChecks, deps.Runtime.ReadinessChecks...)
	}
	for _, check := range deps.ReadinessChecks {
		if check == nil || strings.TrimSpace(check.Name()) == "" {
			return nil, errors.New("readiness checks require a name")
		}
	}
	app := &App{
		config:               cfg,
		capabilities:         deps.Capabilities,
		readinessChecks:      append([]ReadinessCheck(nil), deps.ReadinessChecks...),
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
		credentialService:    credentialService,
		deviceFlow:           deviceFlow,
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
