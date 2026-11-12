package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/full-chaos/dev-health-go/pep"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/authworkload"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
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
	// wrapHandler, when set, wraps the application handler before the server
	// is built (the OTel server span and metrics). nil serves the handler
	// unwrapped.
	wrapHandler func(http.Handler) http.Handler
	// metrics counts the outcomes the investigation route and the engine
	// certify. nil records nothing.
	metrics *hostedmetrics.Instruments
}

func prepareServer(ctx context.Context, request serverBuildRequest) (serverRunner, func() error, error) {
	if request.logger == nil || request.newServer == nil {
		return nil, nil, errors.New("server construction dependencies are incomplete")
	}
	if request.serviceVersion == "" {
		request.serviceVersion = version.Current().Version
	}
	// CHAOS-11195: start-up proof only. The client is built, not used: no exchange runs and no readiness check
	// waits on it, so an auth-service outage never stops acr-api. A bad config stops the start; there is no fallback.
	if _, err := authworkload.Build(request.config.AuthWorkload, request.logger); err != nil {
		return nil, nil, fmt.Errorf("initialize auth workload client: %w", err)
	}
	dependencies, closeRuntime, err := applicationDependencies(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	webAssertions, err := webAssertionVerifier(request.config, dependencies.WebAssertionReplays, request.logger)
	if err != nil {
		return nil, nil, closeBuildFailure(closeRuntime, fmt.Errorf("initialize web assertions: %w", err))
	}
	dependencies.WebAssertions = webAssertions
	approvals, err := approvalDecisions(request.config, request.logger)
	if err != nil {
		return nil, nil, closeBuildFailure(closeRuntime, fmt.Errorf("initialize device approval decisions: %w", err))
	}
	dependencies.ApprovalDecisions = approvals
	app, err := api.NewApp(appConfig(request.config, request.serviceVersion), dependencies, request.logger)
	if err != nil {
		return nil, nil, closeBuildFailure(closeRuntime, fmt.Errorf("initialize application: %w", err))
	}
	closeApplication := func() error {
		if closeRuntime == nil {
			return app.Close()
		}
		return errors.Join(app.Close(), closeRuntime())
	}
	handler := app.Handler()
	if request.wrapHandler != nil {
		handler = request.wrapHandler(handler)
	}
	server, err := request.newServer(serverConfig(request.config), handler, request.logger)
	if err != nil {
		return nil, nil, closeBuildFailure(closeApplication, fmt.Errorf("initialize server: %w", err))
	}
	return server, closeApplication, nil
}

func webAssertionVerifier(cfg config.Config, replays auth.WebAssertionReplayStore, logger *slog.Logger) (*auth.WebAssertionVerifier, error) {
	if cfg.WebAssertionJWKSFile == "" {
		return nil, nil
	}
	if replays == nil && cfg.RequireBackingStores {
		return nil, errors.New("web assertions require the shared replay store")
	}
	return auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: cfg.WebAssertionIssuer, Audience: cfg.WebAssertionAudience, JWKSPath: cfg.WebAssertionJWKSFile,
		MaxBodyBytes: int64(cfg.MaxSerializedBytes),
		Replays:      replays, Logger: logger,
	})
}

// approvalDecisions builds the approver check of an approval (CHAOS-13586): nil when ACR_DEVICE_APPROVAL_PDP is off.
// It never stops the start (product rule 3): an unknown mode is off with one ERROR line; shadow or enforce without a
// well-formed ACR_DEVICE_APPROVAL_PDP_URL or the workload client has no PDP client, one ERROR line, and enforce then
// refuses every approval (fail closed). One INFO line names the effective mode, for the roll read-back.
func approvalDecisions(cfg config.Config, logger *slog.Logger) (*auth.ApprovalDecisions, error) {
	mode, err := auth.ParseApprovalDecisionMode(cfg.DeviceApprovalPDP.Mode)
	if err != nil {
		logger.Error("device approval decision mode unknown", "key", "ACR_DEVICE_APPROVAL_PDP", "allowed", "off,shadow,enforce")
	}
	logger.Info("device approval decision mode", "mode", string(mode))
	if mode == auth.ApprovalDecisionOff {
		return nil, nil
	}
	var decider pep.Decider
	if cfg.DeviceApprovalPDP.Configured(cfg.AuthWorkload) {
		client, fault := approvalDecisionClient(cfg, logger)
		if fault != "" {
			logger.Error("device approval decision client invalid", "key", "ACR_DEVICE_APPROVAL_PDP_URL", "fault", fault)
		} else {
			decider = client
		}
	}
	return auth.NewApprovalDecisions(mode, decider, logger)
}

// approvalDecisionClient builds the PDP client, or names the step that failed with a closed word (never an error's
// text, which can hold a configured value).
func approvalDecisionClient(cfg config.Config, logger *slog.Logger) (pep.Decider, string) {
	tokens, err := authworkload.Build(cfg.AuthWorkload, logger)
	if err != nil || tokens == nil {
		return nil, "workload_client"
	}
	httpClient, err := tokens.HTTPClient(outboundhttp.Transport(), cfg.DeviceApprovalPDP.URL, "auth-service")
	if err != nil {
		return nil, "workload_transport"
	}
	client, err := pep.New(pep.Config{BaseURL: cfg.DeviceApprovalPDP.URL, AllowPlainHTTP: cfg.AuthWorkload.AllowPlainHTTP,
		HTTPClient: httpClient, ClientService: "dev-health-acr-api"})
	if err != nil {
		return nil, "pep_client"
	}
	return client, ""
}

func applicationDependencies(ctx context.Context, request serverBuildRequest) (api.Dependencies, func() error, error) {
	if request.config.RequireBackingStores {
		if request.openRuntime == nil {
			return api.Dependencies{}, nil, errors.New("hosted runtime opener is required")
		}
		// CHAOS-5638: read at the composition root, beside every other
		// option this command hands the hosted runtime. Unset -- every
		// deployment today -- reads as 1, which is the pre-ensemble path.
		runtime, err := request.openRuntime(ctx, hosted.Options{
			ServiceVersion:             request.serviceVersion,
			Logger:                     request.logger,
			InterpretationEnsembleSize: hosted.InterpretationEnsembleSizeFromEnv(os.LookupEnv, request.logger),
			Metrics:                    request.metrics,
		})
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
		runtime.Dependencies.Metrics = request.metrics
		return runtime.Dependencies, runtime.Close, nil
	}
	dependencies, err := developmentDependencies(request.config, request.serviceVersion, request.logger)
	dependencies.Metrics = request.metrics
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
		FailureLimit: cfg.RequestControls.AuthFailures, MaxTrackedKeys: cfg.RequestControls.AuthTrackedKeys, MaxInFlight: cfg.RequestControls.AuthMaxInFlight,
	})
	hooks := observability.NewHooks(observability.NewSlogSink(logger), nil)
	capabilities := api.StaticCapabilitiesProvider{Value: contractsv1.Capabilities{
		SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: serviceVersion,
		MinimumSidecarVersion: cfg.MinimumSidecarVersion, SupportedSchemaVersions: contractsv1.AllSchemaVersions,
		EnabledTools: []string{}, Entitlements: contractsv1.CapabilityEntitlements{}, Permissions: contractsv1.CapabilityPermissions{},
		Limits: contractsv1.CapabilityLimits{MaxItems: cfg.MaxItems, MaxOutputTokens: cfg.MaxOutputTokens,
			MaxSerializedBytes: cfg.MaxSerializedBytes, RequestsPerMinute: cfg.ContextRequestsPerMinute()},
	}}
	runtimeReadiness := api.CheckFunc{CheckName: "runtime_dependencies", Fn: func(context.Context) error { return errors.New("hosted read runtime is not configured") }}
	if cfg.LocalCompositionReady {
		runtimeReadiness.Fn = func(context.Context) error { return nil }
	}
	checks := []api.ReadinessCheck{
		api.CheckFunc{CheckName: "configuration", Fn: func(context.Context) error { return cfg.Validate() }},
		runtimeReadiness,
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
		RevokedClientVersions:                       cfg.RevokedClientVersions,
		CORSAllowedOrigins:                          cfg.CORSAllowedOrigins,
		ServerCompletenessAuthorityEnabled:          cfg.ServerCompletenessAuthorityEnabled,
		ServerCompletenessAuthoritySymmetricEnabled: cfg.ServerCompletenessAuthoritySymmetricEnabled,
		ContextFabricVocabularyLabelsEnabled:        cfg.ContextFabricVocabularyLabelsEnabled,
		ContextFabricInvestmentTreemapEnabled:       cfg.ContextFabricInvestmentTreemapEnabled,
		ContextFabricInvestmentSunburstEnabled:      cfg.ContextFabricInvestmentSunburstEnabled,
		ContextFabricInvestmentSankeyEnabled:        cfg.ContextFabricInvestmentSankeyEnabled,
		ContextFabricComparisonTableEnabled:         cfg.ContextFabricComparisonTableEnabled,
		ContextFabricLandscapeQuadrantEnabled:       cfg.ContextFabricLandscapeQuadrantEnabled,
		ContextFabricCompletionDistributionEnabled:  cfg.CompletionDistributionEnabled,
		ContextFabricEventStreamEnabled:             cfg.ContextFabricEventStreamEnabled,
		ContextFabricRunLeaseEnabled:                cfg.ContextFabricRunLeaseEnabled,
		AskDevLimits: contextfabric.SubmissionLimits{
			MaxRunningPerPrincipal: cfg.AskDevMaxRunningPerPrincipal, MaxRunningPerOrg: cfg.AskDevMaxRunningPerOrg,
			MonthlyPlatformRunAllowance: int64(cfg.AskDevMonthlyPlatformRunAllowance),
		},
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
