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
	ServiceName    string
	ServiceVersion string
	RequestTimeout time.Duration
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
	for _, check := range deps.ReadinessChecks {
		if check == nil || strings.TrimSpace(check.Name()) == "" {
			return nil, errors.New("readiness checks require a name")
		}
	}
	return &App{
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
	}, nil
}

func (a *App) ProtectedHandler(class limits.RequestClass, next http.Handler) http.Handler {
	return LimitMiddleware(a.limits, class, next)
}

func (a *App) AuthenticatedHandler(credentials storage.CredentialStore, audit storage.AuditStore, class limits.RequestClass, next http.Handler) (http.Handler, error) {
	authenticator, err := auth.NewAuthenticator(credentials, audit, auth.AuthenticatorOptions{Now: a.now, Limiter: a.authAttempts, Logger: a.logger})
	if err != nil {
		return nil, err
	}
	return authenticator.Middleware(a.ProtectedHandler(class, next)), nil
}

func (a *App) NewEvidenceStore(rows contextpacket.ClickHouseRows) (*contextpacket.ClickHouseEvidenceStore, error) {
	if a.evidenceStoreFactory == nil {
		return nil, errors.New("evidence store factory is not configured")
	}
	return a.evidenceStoreFactory(rows)
}
