package hosted

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelconfig"
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
	// CHAOS-3775: composed independently of the investigator/graph gating
	// below -- see buildOrgModelConfigStore's doc comment -- so a customer
	// can save their organization's BYO LLM configuration whether or not
	// the graph backend is separately wired.
	orgModelConfigStore, err := buildOrgModelConfigStore(postgres, os.LookupEnv)
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize context fabric org model config store: %w", err))
	}
	investigator, investigationResultStore, runtimeEvictor, resultReuseInvalidator, err := buildContextFabricInvestigator(ctx, request, postgres, clickhouse, orgModelConfigStore)
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize context fabric investigator: %w", err))
	}
	// orgModelConfigStore is a concrete *pgmodelconfig.Store, possibly nil;
	// runtimeEvictor is a concrete *modelruntimeresolver.Resolver, possibly
	// nil. Assigning either nil pointer directly into its interface-typed
	// field below would produce a non-nil interface wrapping a nil value
	// (the classic typed-nil trap -- see (*App).orgModelConfigs' and
	// (*App).investigator's matching doc comments), so both nil checks
	// happen here, before the interface conversion, not after.
	var orgModelConfigs contextfabric.OrgModelConfigStore
	if orgModelConfigStore != nil {
		orgModelConfigs = orgModelConfigStore
	}
	var orgModelRuntimeEvictor contextfabric.OrgModelRuntimeEvictor
	if runtimeEvictor != nil {
		orgModelRuntimeEvictor = runtimeEvictor
	}
	// Same typed-nil guard: investigationResultStore is a concrete
	// *pginvestigation.Store and is nil whenever the investigator itself
	// was not composed.
	var investigationResults contextfabric.InvestigationResultStore
	if investigationResultStore != nil {
		investigationResults = investigationResultStore
	}
	runtime.Dependencies = api.Dependencies{
		Capabilities: capabilities, Now: request.options.Now, Observability: &hooks, Limits: manager, AuthAttempts: authAttempts,
		EvidenceStoreFactory: clickhouse.factory, ClientIP: clientIP, UsageTelemetry: usageTelemetry,
		Runtime: &api.RuntimeDependencies{
			Credentials: postgres.credentials, Audit: postgres.audit, Entitlements: entitlement, Assembler: assembler,
			Evidence: clickhouse.evidence, Episodes: episodeCreator, ReadinessChecks: checks,
			DeviceAuthorizations: postgres.devices, DeviceVerificationURL: request.config.DeviceVerificationURL,
			DeviceAuthorizationLimiter: api.NewDeviceAuthorizationLimiter(api.ClockFunc(request.options.Now)),
			Investigator:               investigator,
			InvestigationResults:       investigationResults,
			OrgModelConfigs:            orgModelConfigs,
			OrgModelRuntimeEvictor:     orgModelRuntimeEvictor,
			// CHAOS-3786, codex round-1 P1(b): resultReuseInvalidator is
			// already nil-or-a-real-*pginvestigation.Store as an interface
			// value (buildContextFabricInvestigator's own reuseEnabled
			// guard decides that before returning it), so it needs no
			// second typed-nil check here.
			ReuseInvalidator: resultReuseInvalidator,
		},
	}
	return runtime, nil
}

// buildContextFabricInvestigator composes a real contextfabric.Investigator
// (CHAOS-3755) when the operator opted in AND the graph backend is
// separately configured. It never fails composition over an unconfigured
// optional dependency (ADR 0007's convention): if either condition is
// false, it returns all-nil and the investigations route degrades to a
// static 503 (see api.App.investigator / handleRuntimeUnavailable).
//
// The model runtime is a third, INDEPENDENT enablement (CHAOS-3770): it is
// constructed by newContextFabricModelRuntime only when a provider is
// configured, and stays nil otherwise. A nil model runtime does not stop
// the investigator from being composed -- the graph and canonical-fact
// layers are real and live either way, and every request degrades to a
// clean ErrModelUnavailable 503 instead. See newContextFabricModelRuntime.
//
// orgModelConfigStore is CHAOS-3775's per-organization layer over that same
// model runtime: when non-nil, the deployment-default runtime built here is
// wrapped in a modelruntimeresolver.Resolver so each request's actual
// runtime is chosen by the requesting organization's own stored
// configuration (falling through to this deployment default when the
// organization has none). When nil (no encryption key configured), the
// deployment-default runtime is used completely unwrapped, unchanged from
// pre-CHAOS-3775 behavior, and the returned evictor is nil too.
//
// The second return value is the immutable result store this engine already
// writes every answer through. open() wires it into
// api.RuntimeDependencies.InvestigationResults so the retrieval route
// (CHAOS-3746) reads back exactly what the investigation persisted, rather
// than composing a second store over the same table that could drift from
// it.
//
// The third return value is that same Resolver, exposed only as
// contextfabric.OrgModelRuntimeEvictor -- open() wires it into
// api.RuntimeDependencies.OrgModelRuntimeEvictor so the DELETE model-config
// route can purge a cached runtime immediately (Codex round-1 finding F4).
func buildContextFabricInvestigator(ctx context.Context, request buildRequest, postgres postgresComponents, clickhouse clickHouseComponents, orgModelConfigStore *pgmodelconfig.Store) (contextfabric.Investigator, *pginvestigation.Store, contextfabric.OrgModelRuntimeEvictor, contextfabric.ReuseInvalidator, error) {
	if !request.config.EnableContextFabricInvestigations || !falkorgraph.Configured(os.LookupEnv) {
		return nil, nil, nil, nil, nil
	}
	graphConfig, err := falkorgraph.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load context fabric graph configuration: %w", err)
	}
	// Codex round-3 F2: supply a real telemetry sink. Left nil, every graph
	// signal -- including the per-request vector-degradation signal -- was
	// discarded, while documentation claimed operators could observe it.
	graphConfig.Telemetry = falkorgraph.SlogTelemetry{}
	// CHAOS-3778: vector retrieval is optional. An unconfigured embedder
	// leaves the lexical retrieval path exactly as it was.
	embedderOptions, err := falkorgraph.EmbedderFromEnv(os.LookupEnv)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load context fabric embedder configuration: %w", err)
	}
	graphReader, err := falkorgraph.NewWithEmbedder(graphConfig, embedderOptions)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("initialize context fabric graph adapter: %w", err)
	}
	factRegistry, err := contextfabric.NewFactCapabilityRegistry(
		devhealthfacts.NewProviders(clickhouse.queryClient),
		contextfabric.FactRegistryOptions{Now: request.options.Now},
	)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("initialize canonical fact registry: %w", err)
	}
	// CHAOS-3782: WithAnswerReuse turns on Save's reuse-column bookkeeping
	// and FindReusable/InvalidateOrganizationReuse. request.config.AnswerReuseMaxAge
	// is condition 4's staleness window -- see its doc comment (D15
	// hazard) for why it must stay conservative. Zero (the unset default;
	// config.Config.Validate lets zero through as "disabled") means
	// answer reuse stays off entirely: WithAnswerReuse is not passed, so
	// Save never writes reuse columns and FindReusable always misses.
	reuseEnabled := request.config.AnswerReuseMaxAge > 0
	var storeOpts []pginvestigation.StoreOption
	if reuseEnabled {
		storeOpts = append(storeOpts, pginvestigation.WithAnswerReuse(request.config.AnswerReuseMaxAge))
	}
	investigationStore, err := pginvestigation.NewStore(postgres.db, storeOpts...)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("initialize investigation result store: %w", err)
	}
	// reuseSnapshotter, reuseEpochSnapshotter, and reuseInvalidator all
	// stay nil when reuse is disabled -- Engine then never queries
	// checkpoints for a snapshot it would immediately discard
	// (reuseColumnsFor short-circuits on !s.reuseEnabled before ever
	// consulting one), and the model-config PUT/DELETE routes
	// (CHAOS-3786, codex round-1 P1(b)) skip invalidation entirely --
	// there is nothing to invalidate for a store that never wrote a
	// reuse column in the first place.
	var reuseInvalidator contextfabric.ReuseInvalidator
	if reuseEnabled {
		reuseInvalidator = investigationStore
	}
	// CHAOS-3782 Codex round-3 finding 1: these two MUST be wired
	// together, from the same reuseEnabled switch -- the same
	// *pginvestigation.Store satisfies both SourceWatermarkSnapshotter
	// and RebuildEpochSnapshotter, and Engine treats a nil
	// ReuseEpochSnapshotter exactly like reuse being off entirely (see
	// EngineDependencies.ReuseEpochSnapshotter's doc comment): a saved
	// row with a nil invalidation_epoch never satisfies store.go's
	// FindReusable, so wiring only the watermark half silently disables
	// reuse for every new result while looking fully configured.
	var reuseSnapshotter contextfabric.SourceWatermarkSnapshotter
	var reuseEpochSnapshotter contextfabric.RebuildEpochSnapshotter
	if reuseEnabled {
		reuseSnapshotter = investigationStore
		reuseEpochSnapshotter = investigationStore
	}
	// nil when no model provider is configured; see the function doc
	// comment and newContextFabricModelRuntime.
	deploymentDefaultRuntime, err := newContextFabricModelRuntime(ctx, os.LookupEnv)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	modelRuntime, evictor, err := wrapWithOrgModelRuntimeResolver(deploymentDefaultRuntime, orgModelConfigStore, os.LookupEnv)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	receiptSink, err := buildModelReceiptSink(postgres)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// orgModelConfigStore is a concrete *pgmodelconfig.Store, possibly nil
	// -- the same typed-nil-interface trap open()'s own conversion guards
	// against (see its doc comment) applies here too: assigning a nil
	// *pgmodelconfig.Store directly into an interface-typed field would
	// produce a non-nil interface wrapping nil, so
	// contextFabricReuseModelIdentityResolver's own `configs == nil` check
	// would never see it as absent.
	var reuseModelIdentityConfigs contextfabric.OrgModelConfigResolver
	if orgModelConfigStore != nil {
		reuseModelIdentityConfigs = orgModelConfigStore
	}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: modelRuntime, Sink: receiptSink},
		Graph:       graphReader,
		Facts:       factRegistry,
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: modelRuntime, Sink: receiptSink, Options: contextfabric.RuntimeAnswerSynthesizerOptions{
			ServiceVersion:    request.options.ServiceVersion,
			Backend:           "graph",
			ProjectionVersion: contextFabricProjectionVersion,
		}},
		Results:   investigationStore,
		ReuseGate: investigationStore,
		// CHAOS-3782 Codex round-1 F1: same *pginvestigation.Store also
		// implements SourceWatermarkSnapshotter, so Engine can capture
		// the reuse snapshot itself, before the graph read, rather than
		// Save taking a later (too late) one.
		ReuseSnapshotter: reuseSnapshotter,
		// CHAOS-3782 Codex round-3 finding 1: wired from the same
		// reuseEnabled switch, on the same investigationStore, as
		// ReuseSnapshotter immediately above -- see that variable's
		// declaration for why these two must never be set independently.
		ReuseEpochSnapshotter: reuseEpochSnapshotter,
		// CHAOS-3782 Codex round-2 finding #3: resolve the reuse lookup's
		// model identity per-organization, from the SAME orgModelConfigStore
		// wrapWithOrgModelRuntimeResolver above already uses for the actual
		// model runtime, rather than binding every organization's lookups
		// to one static deployment-wide identity -- see
		// contextFabricReuseModelIdentityResolver's doc comment. orgModelConfigStore
		// may be nil (no per-organization support configured at all); the
		// resolver handles that by always returning fallback, so it is
		// still safe -- and still correct, since there is then no
		// per-organization divergence possible -- to wire unconditionally.
		ReuseModelIdentityResolver: contextFabricReuseModelIdentityResolver{
			configs:  reuseModelIdentityConfigs,
			fallback: contextFabricReuseModelIdentities(os.LookupEnv),
		},
		// CHAOS-3782 AC-3782-8: the first production EngineTelemetry
		// wiring (previously always nil -- see SlogEngineTelemetry's doc
		// comment). Also covers the pre-existing
		// RecordPriorSubjectReceiptsSkipped counter, which had a port and
		// a call site but no production implementation until now.
		Telemetry: contextfabric.NewSlogEngineTelemetry(request.options.Logger),
	}, contextfabric.EngineOptions{
		ServiceVersion: request.options.ServiceVersion, Now: request.options.Now, NewResultID: newInvestigationResultID,
		// ReuseProjectionVersion mirrors RuntimeAnswerSynthesizerOptions
		// above verbatim (contextFabricProjectionVersion, so a fresh
		// answer's Versions.ProjectionVersion and what reuse compares
		// against can never drift apart), and ReuseModelIdentities (CHAOS-3786)
		// mirrors the FULL chain (primary, then fallback) the configured
		// provider/model(s) can produce via model_runtime.go's
		// modelIdentity helper -- see contextFabricReuseModelIdentities'
		// doc comment for why this is a chain, not a single value.
		ReuseProjectionVersion: contextFabricProjectionVersion,
		ReuseModelIdentities:   contextFabricReuseModelIdentities(os.LookupEnv),
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("initialize context fabric engine: %w", err)
	}
	// evictor is a concrete *modelruntimeresolver.Resolver, possibly nil
	// (when orgModelConfigStore was nil) -- guard the typed-nil-interface
	// trap here too, same as open()'s conversion, so a nil *Resolver can
	// never reach the caller wrapped in a non-nil
	// contextfabric.OrgModelRuntimeEvictor. reuseInvalidator is already
	// either nil or a concrete *pginvestigation.Store assigned above
	// through the same interface-typed local, so it needs no separate
	// guard here.
	if evictor == nil {
		return engine, investigationStore, nil, reuseInvalidator, nil
	}
	return engine, investigationStore, evictor, reuseInvalidator, nil
}

// contextFabricProjectionVersion is Versions.ProjectionVersion for every
// investigation this composition produces, and (CHAOS-3782 Codex round-1
// F9) EngineOptions.ReuseProjectionVersion's value too -- the same
// constant on both sides so they cannot drift.
//
// Before this fix neither call site set a real ProjectionVersion at all
// (both defaulted to the literal "unwired" via nonEmptyVersion), so this
// dimension of the reuse key never actually distinguished anything: a
// projection shape change (e.g. CHAOS-3779's edge vocabulary expansion)
// would NOT have forced already-stored results to be treated as stale,
// defeating AC-3782-7's version-mismatch guarantee for this specific
// dimension.
//
// devhealthsource.ClickHouseSourceVersion and .EpisodesSourceVersion are
// the two real, deliberately-bumped identities devhealthsource itself
// already uses to force a rebuild when its own projection mapping
// changes (see ClickHouseSourceVersion's doc comment) -- composing them
// is the most direct, already-established authority available, short of
// this binary (acr-api, the READ side) somehow tracking every
// possible acr-projector (the WRITE side, a different binary) source
// version live, which nothing here has a channel for. TeamsProjectsSource
// has no version constant (CHAOS-3779: still a stub returning zero rows)
// and is intentionally omitted.
const contextFabricProjectionVersion = devhealthsource.ClickHouseSourceVersion + "+" + devhealthsource.EpisodesSourceVersion

// contextFabricReuseModelIdentities computes the CURRENT deployment-default
// model CHAIN's identities, primary first and then the fallback (if
// modelprovider.EnvFallbackModel is configured), each in the exact
// "<provider>/<model>" shape model_runtime.go's modelIdentity helper
// produces from a receipt, for EngineOptions.ReuseProjectionVersion's
// sibling ReuseModelIdentities. It reads modelprovider.ConfigFromEnv a
// second time (newContextFabricModelRuntime already read it once, to
// build the runtime itself) rather than threading the config out through
// that function's return value, to avoid changing a signature other call
// sites and tests depend on for what is a purely additive, optional reuse
// concern.
//
// CHAOS-3786: previously returned only the primary's identity, so a
// result actually synthesized by the fallback model (§19.3.4 records this
// happening often -- 16 of 17 successful investigations needed it in the
// measured batch) computed a ReuseKey that never matched it -- reuse
// simply missed for every fallback-produced answer. Returning the whole
// chain and matching on chain MEMBERSHIP (see
// ReuseKey.ModelIdentities' doc comment) fixes that without needing to
// predict ahead of a model call which of the two will answer.
func contextFabricReuseModelIdentities(lookup func(string) (string, bool)) []string {
	if !modelprovider.Configured(lookup) {
		return nil
	}
	modelConfig, err := modelprovider.ConfigFromEnv(lookup)
	if err != nil {
		return nil
	}
	provider := strings.TrimSpace(modelConfig.Provider)
	model := strings.TrimSpace(modelConfig.Model)
	if provider == "" || model == "" {
		return nil
	}
	identities := []string{provider + "/" + model}
	if fallbackModel := strings.TrimSpace(modelConfig.FallbackModel); fallbackModel != "" {
		identities = appendReuseIdentity(identities, provider+"/"+fallbackModel)
	}
	return identities
}

func newInvestigationResultID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("result_fallback_%d", time.Now().UnixNano())
	}
	return "result_" + hex.EncodeToString(buf)
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
