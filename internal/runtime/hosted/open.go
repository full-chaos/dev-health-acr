package hosted

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

func Open(ctx context.Context, cfg config.Config, options Options) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate hosted runtime configuration: %w", err)
	}
	return open(ctx, buildRequest{config: cfg, options: options, factories: productionFactories()})
}

func open(ctx context.Context, request buildRequest) (*Runtime, error) {
	if err := validateBuildRequest(request); err != nil {
		return nil, err
	}
	if request.options.Now == nil {
		request.options.Now = time.Now
	}
	hooks := observability.NewHooks(observability.NewSlogSink(request.options.Logger), nil)
	assemblyObserver := observability.NewAssemblyObserver(hooks)
	expansionObserver := observability.NewEvidenceExpansionObserver(hooks)

	postgres, err := request.factories.openPostgres(ctx, request.config, request.options.Logger)
	if err != nil {
		return nil, fmt.Errorf("initialize PostgreSQL runtime: %w", err)
	}
	runtime := &Runtime{postgresClose: postgres.close}
	usageTelemetry, err := auth.NewUsageTelemetry(postgres.credentials, postgres.audit, auth.UsageTelemetryOptions{Logger: request.options.Logger})
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize credential usage telemetry: %w", err))
	}
	runtime.usageTelemetry = usageTelemetry
	clickhouse, err := request.factories.openClickHouse(ctx, clickHouseOpenRequest{
		config: request.config, assemblyObserver: assemblyObserver, expansionObserver: expansionObserver,
	})
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize ClickHouse runtime: %w", err))
	}
	runtime.closers = []func() error{clickhouse.close}
	entitlement, err := request.factories.newEntitlement(request.config)
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize entitlement runtime: %w", err))
	}
	runtime.closers = []func() error{entitlement.Close, clickhouse.close}

	var episodeCreator api.EpisodeCreator
	if request.config.EnableEpisodeWriteback {
		episodeCreator, err = request.factories.newEpisode(episodeServiceRequest{postgres: postgres, options: request.options, hooks: hooks})
		if err != nil {
			return nil, closeAfterError(runtime, fmt.Errorf("initialize episode runtime: %w", err))
		}
	}
	checks := []api.ReadinessCheck{
		api.CheckFunc{CheckName: "postgres", Fn: postgres.check},
		api.CheckFunc{CheckName: "clickhouse", Fn: clickhouse.check},
		api.CheckFunc{CheckName: "entitlement", Fn: entitlement.Check},
	}
	for _, check := range checks {
		if err := check.Check(ctx); err != nil {
			return nil, closeAfterError(runtime, fmt.Errorf("initial %s readiness check: %w", check.Name(), err))
		}
	}

	manager, err := limits.NewManager(request.config.LimitOptions())
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize request controls: %w", err))
	}
	clientIP, err := auth.NewTrustedProxyClientIPResolver(request.config.TrustedProxyCIDRs)
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize client address resolver: %w", err))
	}
	assembler := contextpacket.NewAssembler(clickhouse.evidence, contextpacket.Options{
		Now: request.options.Now, ServiceVersion: request.options.ServiceVersion,
		MinimumSidecarVersion: request.config.MinimumSidecarVersion, SnapshotStore: postgres.packets,
		Observer: assemblyObserver, StoreBackend: contextpacket.StoreBackendClickHouse,
		Tracer: observability.NewAssemblyTraceBoundary(hooks, nil),
	})
	capabilities := api.StaticCapabilitiesProvider{Value: contractsv1.Capabilities{
		SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: request.options.ServiceVersion,
		MinimumSidecarVersion: request.config.MinimumSidecarVersion, SupportedSchemaVersions: contractsv1.AllSchemaVersions,
		EnabledTools: []string{}, Entitlements: contractsv1.CapabilityEntitlements{}, Permissions: contractsv1.CapabilityPermissions{},
		Limits: contractsv1.CapabilityLimits{MaxItems: request.config.MaxItems, MaxOutputTokens: request.config.MaxOutputTokens,
			MaxSerializedBytes: request.config.MaxSerializedBytes, RequestsPerMinute: request.config.ContextRequestsPerMinute()},
	}, Now: request.options.Now}
	authAttempts := auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{
		Window: request.config.RequestControls.Auth.Window, AttemptLimit: request.config.RequestControls.Auth.Requests,
		FailureLimit: request.config.RequestControls.AuthFailures, MaxTrackedKeys: request.config.RequestControls.AuthTrackedKeys,
	})
	runtime.Dependencies = api.Dependencies{
		Capabilities: capabilities, Now: request.options.Now, Observability: &hooks, Limits: manager, AuthAttempts: authAttempts,
		EvidenceStoreFactory: clickhouse.factory, ClientIP: clientIP, UsageTelemetry: usageTelemetry,
		Runtime: &api.RuntimeDependencies{
			Credentials: postgres.credentials, Audit: postgres.audit, Entitlements: entitlement, Assembler: assembler,
			Evidence: clickhouse.evidence, Episodes: episodeCreator, ReadinessChecks: checks,
			DeviceAuthorizations: postgres.devices, DeviceVerificationURL: request.config.DeviceVerificationURL,
			DeviceAuthorizationLimiter: api.NewDeviceAuthorizationLimiter(api.ClockFunc(request.options.Now)),
		},
	}
	return runtime, nil
}

func validateBuildRequest(request buildRequest) error {
	if request.options.Logger == nil || request.options.ServiceVersion == "" {
		return errors.New("hosted runtime options are incomplete")
	}
	if request.factories.openPostgres == nil || request.factories.openClickHouse == nil || request.factories.newEntitlement == nil || request.factories.newEpisode == nil {
		return errors.New("hosted runtime factories are incomplete")
	}
	return nil
}

func closeAfterError(runtime *Runtime, cause error) error {
	if closeErr := runtime.Close(); closeErr != nil {
		return errors.Join(cause, fmt.Errorf("close hosted runtime: %w", closeErr))
	}
	return cause
}
