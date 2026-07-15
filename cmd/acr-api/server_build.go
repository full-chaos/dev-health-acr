package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/runtime/hosted"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

type serverRunner interface {
	Run(context.Context) error
}

type serverBuildRequest struct {
	config         config.Config
	logger         *slog.Logger
	serviceVersion string
	openRuntime    func(context.Context, hosted.Options) (*hosted.Runtime, error)
	newServer      func(api.ServerConfig, http.Handler, *slog.Logger) (serverRunner, error)
}

func prepareServer(ctx context.Context, request serverBuildRequest) (serverRunner, func() error, error) {
	if request.logger == nil || request.newServer == nil {
		return nil, nil, errors.New("server construction dependencies are incomplete")
	}
	if request.serviceVersion == "" {
		request.serviceVersion = version.Current().Version
	}
	dependencies, closeRuntime, err := applicationDependencies(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	webAssertions, err := webAssertionVerifier(request.config)
	if err != nil {
		return nil, nil, closeBuildFailure(closeRuntime, fmt.Errorf("initialize web assertions: %w", err))
	}
	dependencies.WebAssertions = webAssertions
	app, err := api.NewApp(appConfig(request.config, request.serviceVersion), dependencies, request.logger)
	if err != nil {
		return nil, nil, closeBuildFailure(closeRuntime, fmt.Errorf("initialize application: %w", err))
	}
	server, err := request.newServer(serverConfig(request.config), app.Handler(), request.logger)
	if err != nil {
		return nil, nil, closeBuildFailure(closeRuntime, fmt.Errorf("initialize server: %w", err))
	}
	return server, closeRuntime, nil
}

func webAssertionVerifier(cfg config.Config) (*auth.WebAssertionVerifier, error) {
	if cfg.WebAssertionJWKSFile == "" {
		return nil, nil
	}
	return auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: cfg.WebAssertionIssuer, Audience: cfg.WebAssertionAudience, JWKSPath: cfg.WebAssertionJWKSFile,
		MaxBodyBytes: int64(cfg.MaxSerializedBytes),
	})
}

func applicationDependencies(ctx context.Context, request serverBuildRequest) (api.Dependencies, func() error, error) {
	if request.config.RequireBackingStores {
		if request.openRuntime == nil {
			return api.Dependencies{}, nil, errors.New("hosted runtime opener is required")
		}
		runtime, err := request.openRuntime(ctx, hosted.Options{ServiceVersion: request.serviceVersion, Logger: request.logger})
		if err != nil {
			return api.Dependencies{}, nil, fmt.Errorf("initialize hosted runtime: %w", err)
		}
		if runtime == nil || runtime.Dependencies.Runtime == nil {
			incomplete := errors.New("hosted runtime is incomplete")
			if runtime != nil {
				return api.Dependencies{}, nil, errors.Join(incomplete, runtime.Close())
			}
			return api.Dependencies{}, nil, incomplete
		}
		return runtime.Dependencies, runtime.Close, nil
	}
	dependencies, err := developmentDependencies(request.config, request.serviceVersion, request.logger)
	return dependencies, nil, err
}

func developmentDependencies(cfg config.Config, serviceVersion string, logger *slog.Logger) (api.Dependencies, error) {
	clientIP, err := auth.NewTrustedProxyClientIPResolver(cfg.TrustedProxyCIDRs)
	if err != nil {
		return api.Dependencies{}, fmt.Errorf("configuration: %w", err)
	}
	manager, err := limits.NewManager(cfg.LimitOptions())
	if err != nil {
		return api.Dependencies{}, fmt.Errorf("initialize request controls: %w", err)
	}
	authAttempts := auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{
		Window: cfg.RequestControls.Auth.Window, AttemptLimit: cfg.RequestControls.Auth.Requests,
		FailureLimit: cfg.RequestControls.AuthFailures, MaxTrackedKeys: cfg.RequestControls.AuthTrackedKeys,
	})
	hooks := observability.NewHooks(observability.NewSlogSink(logger), nil)
	capabilities := api.StaticCapabilitiesProvider{Value: contractsv1.Capabilities{
		SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: serviceVersion,
		MinimumSidecarVersion: cfg.MinimumSidecarVersion, SupportedSchemaVersions: contractsv1.AllSchemaVersions,
		EnabledTools: []string{}, Entitlements: contractsv1.CapabilityEntitlements{}, Permissions: contractsv1.CapabilityPermissions{},
		Limits: contractsv1.CapabilityLimits{MaxItems: cfg.MaxItems, MaxOutputTokens: cfg.MaxOutputTokens,
			MaxSerializedBytes: cfg.MaxSerializedBytes, RequestsPerMinute: cfg.ContextRequestsPerMinute()},
	}}
	checks := []api.ReadinessCheck{
		api.CheckFunc{CheckName: "configuration", Fn: func(context.Context) error { return cfg.Validate() }},
		api.CheckFunc{CheckName: "runtime_dependencies", Fn: func(context.Context) error { return errors.New("hosted read runtime is not configured") }},
	}
	return api.Dependencies{
		Capabilities: capabilities, ReadinessChecks: checks, Limits: manager, AuthAttempts: authAttempts,
		Observability: &hooks, ClientIP: clientIP,
	}, nil
}

func appConfig(cfg config.Config, serviceVersion string) api.AppConfig {
	return api.AppConfig{
		ServiceName: "dev-health-acr", ServiceVersion: serviceVersion, RequestTimeout: cfg.RequestTimeout,
		MaxRequestBodyBytes: int64(cfg.MaxSerializedBytes), MaxEvidenceResponseBytes: int64(cfg.MaxSerializedBytes),
		MaxItems: cfg.MaxItems, MaxOutputTokens: cfg.MaxOutputTokens, MaxSerializedBytes: cfg.MaxSerializedBytes,
		RevokedClientVersions: cfg.RevokedClientVersions,
	}
}

func serverConfig(cfg config.Config) api.ServerConfig {
	return api.ServerConfig{
		ListenAddress: cfg.ListenAddress, ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, ShutdownTimeout: cfg.ShutdownTimeout,
	}
}

func newAPIServer(cfg api.ServerConfig, handler http.Handler, logger *slog.Logger) (serverRunner, error) {
	return api.NewServer(cfg, handler, logger)
}

func closeBuildFailure(closeRuntime func() error, cause error) error {
	if closeRuntime == nil {
		return cause
	}
	return errors.Join(cause, closeRuntime())
}
