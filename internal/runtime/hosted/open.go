package hosted

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedcache"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgclarification"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelconfig"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgstructurepriors"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgstructureselection"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	investigator, investigationResultStore, runtimeEvictor, resultReuseInvalidator, clarificationSink, structureSelectionSink, err := buildContextFabricInvestigator(ctx, request, postgres, clickhouse, orgModelConfigStore)
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize context fabric investigator: %w", err))
	}
	workloadTokenExchange, err := buildWorkloadTokenExchange(postgres, request.options.Now, os.LookupEnv)
	if err != nil {
		return nil, closeAfterError(runtime, fmt.Errorf("initialize workload token exchange: %w", err))
	}
	// CHAOS-3859: clarificationSink owns a background worker goroutine
	// that must stop BEFORE postgres.close() tears down the pool it
	// writes through -- same ordering Runtime.Close already gives
	// usageTelemetry, and for the identical reason (a worker still
	// draining when the pool closes underneath it would just add
	// DeliveryFailures for writes that were never going to land). nil
	// whenever the investigator itself was not composed; Sink.Close
	// nil-guards itself, so Runtime.Close can call it unconditionally.
	runtime.clarificationSink = clarificationSink
	// CHAOS-3927 P4: structureSelectionSink gets the identical treatment,
	// same reasoning, same nil-guarded unconditional Close.
	runtime.structureSelectionSink = structureSelectionSink
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
	// Same typed-nil guard: workloadTokenExchange is a concrete
	// *auth.WorkloadTokenExchangeService, nil whenever CHAOS-4013 is
	// unconfigured (see buildWorkloadTokenExchange's doc comment).
	var workloadTokenExchanger api.WorkloadTokenExchanger
	if workloadTokenExchange != nil {
		workloadTokenExchanger = workloadTokenExchange
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
			// CHAOS-4013: nil unless ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE
			// and ACR_WORKLOAD_TRUST_DOMAIN are both set -- see
			// buildWorkloadTokenExchange's own doc comment.
			WorkloadTokenExchange: workloadTokenExchanger,
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
// falkorGraphTelemetry builds the falkorgraph.GraphTelemetry sink this
// package wires into every graph adapter it constructs. Factored out to a
// named, directly-testable function (CHAOS-3835 round-4 finding 3) rather
// than an inline SlogTelemetry{Logger: ...} literal, so a unit test can
// assert the wiring -- "the constructed sink carries the passed-in logger"
// -- without needing a graph backend configured or a live connection.
func falkorGraphTelemetry(logger *slog.Logger) falkorgraph.GraphTelemetry {
	return falkorgraph.SlogTelemetry{Logger: logger}
}

// graphLifecycleTelemetry builds the CHAOS-3898 S2a
// contextfabric.GraphLifecycleTelemetry sink, same factoring reasoning as
// falkorGraphTelemetry above. Wired unconditionally (design brief v4.1 F4's
// instrument-before-flip sub-order): cf_resolved_graph_key already fires on
// every graph call this process makes, at epoch 0, today -- proving the
// signal pipeline live in production before any organization's first real
// build/flip (a follow-up slice; Config.EpochResolver stays nil here).
func graphLifecycleTelemetry(logger *slog.Logger) contextfabric.GraphLifecycleTelemetry {
	return contextfabric.SlogGraphLifecycleTelemetry{Logger: logger}
}

// defaultRawSignalObserver is CHAOS-3890's default for
// graphConfig.RawSignalObserver: override, when non-nil, wins unchanged --
// this is the CHAOS-3858 measurement-harness escape hatch (the generative
// trial harness sets its own trialRawSignalCollector), and it must keep
// taking priority exactly as before. A nil override (every real deployment,
// per Options.RawSignalObserver's own doc comment) now falls through to
// graphrank.NewSlogRawSignalObserver rather than staying nil -- the SAME
// "wired unconditionally, gated by log level, no separate config knob"
// convention graphConfig.ResolutionTracer below already uses. Factored out
// to a named, directly-testable function for the same reason
// falkorGraphTelemetry above is: asserting this wiring should not require a
// graph backend configured or a live connection.
func defaultRawSignalObserver(override graphrank.RawSignalObserver, logger *slog.Logger) graphrank.RawSignalObserver {
	if override != nil {
		return override
	}
	return graphrank.NewSlogRawSignalObserver(logger)
}

// contextFabricEngineTelemetry is CHAOS-4103's default for
// EngineDependencies.Telemetry: options.EngineTelemetry, when non-nil, WINS
// unchanged -- the CHAOS-3742 generative trial harness sets its own
// capturing implementation so a synthesis-status override's outcome reaches
// the trial report row instead of only a slog WARN line (see
// Options.EngineTelemetry's own doc comment). A nil override (every real
// deployment) falls through to contextfabric.NewSlogEngineTelemetry, exactly
// the AC-3782-8 wiring this replaces -- byte-identical production behavior.
// Factored out to a named, directly-testable function for the same reason
// defaultRawSignalObserver above is.
func contextFabricEngineTelemetry(options Options) contextfabric.EngineTelemetry {
	if options.EngineTelemetry != nil {
		return options.EngineTelemetry
	}
	return contextfabric.NewSlogEngineTelemetry(options.Logger)
}

// buildGraphLifecycleResolver returns the CHAOS-3898 S2a-2 read-side
// OrgEpochResolver, or nil when pglifecycle.EnvConfig.Enabled is false (the
// default) -- see that type's own doc comment for why this master switch
// exists and why acr-api and acr-projector MUST be configured with the
// SAME value. db is postgresComponents.db, the one PostgreSQL pool this
// package ever opens.
func buildGraphLifecycleResolver(db *sql.DB, telemetry contextfabric.GraphLifecycleTelemetry) (contextfabric.OrgEpochResolver, error) {
	envCfg, err := pglifecycle.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	if !envCfg.Enabled {
		return nil, nil
	}
	store, err := pglifecycle.NewStore(db)
	if err != nil {
		return nil, err
	}
	store.Telemetry = telemetry
	resolver, err := pglifecycle.NewResolver(store)
	if err != nil {
		return nil, err
	}
	return pglifecycle.NewCachedResolver(resolver, envCfg.Lease, pglifecycle.CachedResolverOptions{})
}

// buildContextFabricGraphReader composes the falkorgraph.Adapter (plus its
// paired embed-retrieval identity, which callers of buildContextFabricInvestigator
// still need downstream for EngineOptions.ReuseVersionAuthorities) --
// factored out of buildContextFabricInvestigator (CHAOS-3884 replay
// harness, team-lead ruling 2026-08-17 option (c)) so a SECOND graph reader
// pointed at the SAME live graph, differing ONLY in wireIdentityUniverse,
// can be built without duplicating this composition logic. Production
// (buildContextFabricInvestigator) always passes wireIdentityUniverse=true
// -- this parameter exists for the replay harness alone; no other caller
// should ever pass false.
//
// wireIdentityUniverse=false leaves graphConfig.IdentityUniverse unset,
// which is EXACTLY pre-CHAOS-3884 behavior (Config.IdentityUniverse's own
// doc comment: nil-safe, degrades to no identity fast path). This is the
// replay harness's "arm A" / baseline: the OLD resolver behavior recovered
// inside the NEW binary, by construction, because CHAOS-3884's entire
// commit-path change sits behind this one nil-checked dependency.
// Returns *falkorgraph.Adapter, not the narrower contextfabric.GraphReader
// interface (CHAOS-4042 PR3): buildContextFabricInvestigator's own
// anchorMembershipVerifier wiring needs the concrete adapter's AnchorMember
// method, which is deliberately NOT part of the GraphReader interface (see
// graphrank.GraphAnchorMemberFunc's own doc comment for why -- a new
// interface method would ripple through every fake GraphReader
// implementation in the test suite for a capability only one caller needs).
// Every other caller of this return value already only needs
// contextfabric.GraphReader's own methods, which *Adapter satisfies
// implicitly.
func buildContextFabricGraphReader(request buildRequest, postgres postgresComponents, clickhouse clickHouseComponents, wireIdentityUniverse bool) (*falkorgraph.Adapter, string, error) {
	graphConfig, err := falkorgraph.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, "", fmt.Errorf("load context fabric graph configuration: %w", err)
	}
	// Codex round-3 F2: supply a real telemetry sink. Left nil, every graph
	// signal -- including the per-request vector-degradation signal -- was
	// discarded, while documentation claimed operators could observe it.
	//
	// CHAOS-3835 round-4 finding 3: the sink must carry THIS process's
	// configured logger (request.options.Logger -- the same one every
	// other sink in this function already uses, e.g. NewSlogEngineTelemetry
	// below), not slog.Default() -- SlogTelemetry{} (no Logger) falls back
	// to slog.Default(), which ignores ACR_LOG_LEVEL and whatever handler
	// main.go actually configured. Every signal this package emits --
	// including the CHAOS-3835 id-only skip counts, whose entire purpose is
	// being visible at the operator's configured level -- was reaching a
	// DIFFERENT, unconfigured logger instead, satisfying
	// internal/contextfabric/AGENTS.md's "reported, never inferred"
	// invariant only cosmetically.
	graphConfig.Telemetry = falkorGraphTelemetry(request.options.Logger)
	// CHAOS-3898 S2a (design brief §2.0): startup/config assertion, and the
	// §5b signal sink wired unconditionally.
	graphConfig.LifecycleTelemetry = graphLifecycleTelemetry(request.options.Logger)
	if err := falkorgraph.AssertResolvedPrefix(request.options.Logger, graphConfig.LifecycleTelemetry, graphConfig.GraphPrefix); err != nil {
		return nil, "", fmt.Errorf("context fabric graph key prefix: %w", err)
	}
	// CHAOS-3898 S2a-2 (design brief §3.1): non-nil ONLY when an operator
	// has set pglifecycle.EnvEnabled -- see buildGraphLifecycleResolver's
	// own doc comment. This is the READ side of the same KeyResolver
	// acr-projector's write side must agree with: once ANY organization
	// has actually flipped, investigation reads MUST resolve the new
	// active epoch too, or they would keep reading the OLD epoch's key --
	// which, once retired, no longer exists and would silently
	// auto-recreate as an EMPTY graph (FalkorDB's own auto-create-on-read
	// behavior, identity.go:99). Every production deployment stays
	// byte-identical to pre-CHAOS-3898 output until this flag is
	// explicitly set (and stays a correctness no-op even then, until some
	// organization's first BeginBuild).
	epochResolver, err := buildGraphLifecycleResolver(postgres.db, graphConfig.LifecycleTelemetry)
	if err != nil {
		return nil, "", fmt.Errorf("context fabric graph lifecycle resolver: %w", err)
	}
	graphConfig.EpochResolver = epochResolver
	// CHAOS-3890: see defaultRawSignalObserver's own doc comment -- a nil
	// Options.RawSignalObserver (every real deployment) now defaults to a
	// debug-gated slog sink instead of staying nil forever; an explicit
	// override (the generative-trial harness) still takes priority
	// unchanged.
	graphConfig.RawSignalObserver = defaultRawSignalObserver(request.options.RawSignalObserver, request.options.Logger)
	// CHAOS-3884 (team-lead ruling, 2026-08-17): wired unconditionally,
	// the SAME "always on, gated by log level not by a boolean toggle"
	// convention Telemetry above already uses -- SlogResolutionTracer logs
	// at Debug, so it is silent for any deployment running at its usual
	// Info/Warn level and available the moment an operator raises theirs,
	// with no separate config knob to remember to flip.
	// CHAOS-3742 acceptance debt follow-up: request.options.ResolutionTracer
	// overrides this default when set (test-only hook, nil for every real
	// caller -- see Options.ResolutionTracer's own doc comment) so an
	// in-process caller can capture trace events directly instead of only
	// reaching them by parsing Debug-level slog output.
	if request.options.ResolutionTracer != nil {
		graphConfig.ResolutionTracer = request.options.ResolutionTracer
	} else {
		graphConfig.ResolutionTracer = graphrank.NewSlogResolutionTracer(request.options.Logger)
	}
	// CHAOS-3972 P3: wired UNCONDITIONALLY, not gated alongside
	// wireIdentityUniverse below -- graphrank.ValidateHandleGrammar/
	// HandleSourceColumn are pure, no-I/O registry lookups (no ClickHouse
	// client to share or withhold), so there is no "arm A baseline" reason
	// to leave this off the way CensusFunc/IdentityUniverse are gated.
	graphConfig.HandleGrammarChecker = func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
		if !graphrank.ValidateHandleGrammar(graphrank.CensusKind(kind), patternID, value) {
			return "", false
		}
		return graphrank.HandleSourceColumn(graphrank.CensusKind(kind), patternID)
	}
	// CHAOS-4042 (team-lead ruling): default false -- see
	// config.Config.AnchorMembershipOffersEnabled's own doc comment for
	// why this stays off until a follow-up ships pinned-epoch
	// reconciliation and redemption-time re-authorization.
	graphConfig.AnchorMembershipOffersEnabled = request.config.AnchorMembershipOffersEnabled
	if wireIdentityUniverse {
		// CHAOS-3884 (Option C): closes over the SAME ClickHouse query client
		// devhealthfacts.NewProviders already uses below, so the identity
		// universe read shares the deployment's one live ClickHouse connection
		// rather than opening a second one. Nil-safe by construction --
		// falkorgraph.Config.IdentityUniverse's doc comment -- so leaving this
		// unset (e.g. in a future caller) degrades to no identity fast path,
		// never a startup failure.
		graphConfig.IdentityUniverse = func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
			return devhealthsource.IdentityUniverse(ctx, clickhouse.queryClient, orgID)
		}
		// CHAOS-3896 Slice C (design brief v6 §1.3/§1.4): the source census
		// -- shares the SAME live ClickHouse query client IdentityUniverse
		// above already uses, rather than opening a second connection.
		// Nil-safe by construction (graphrank.ResolveDeps.CensusFunc's own
		// doc comment: nil means the round never runs, at zero cost, for
		// every caller that leaves this unset) -- gated alongside
		// IdentityUniverse, not unconditionally, because the census round's
		// own anchor binding (BindAnchor) consumes the SAME AliasLookup
		// completeness signal IdentityUniverse feeds (reader.go); leaving
		// both off together keeps the replay harness's wireIdentityUniverse=false
		// "arm A" baseline a clean, fully pre-CHAOS-3884/3896/3899 comparison,
		// exactly as this function's own doc comment describes that arm.
		graphConfig.CensusFunc = devhealthsource.NewCensusFunc(clickhouse.queryClient)
	}
	// CHAOS-3778: vector retrieval is optional. An unconfigured embedder
	// leaves the lexical retrieval path exactly as it was.
	embedderOptions, err := falkorgraph.EmbedderFromEnv(os.LookupEnv)
	if err != nil {
		return nil, "", fmt.Errorf("load context fabric embedder configuration: %w", err)
	}
	// CHAOS-3841: an optional LRU over the READ path's single-text (query)
	// Embed calls only -- see embedcache's package doc. Wired here, not in
	// falkorgraph.EmbedderFromEnv, so acr-projector's batch-embedding writer
	// (cmd/acr-projector/runtime.go) never carries this cache: it is a
	// read-path concern, and EmbedderFromEnv is the one construction point
	// both binaries share. Wrap is a no-op (returns embedderOptions.Embedder
	// unchanged) when the cache is not enabled or no embedder is configured.
	embedCacheConfig, err := embedcache.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, "", fmt.Errorf("load context fabric embed query cache configuration: %w", err)
	}
	embedderOptions.Embedder = embedcache.Wrap(embedderOptions.Embedder, embedCacheConfig)
	// CHAOS-3833: the deployment-current embed retrieval identity for
	// answer reuse, derived from the SAME embedprovider configuration the
	// embedder above was built from ("none" when vector retrieval is off).
	// Threaded into EngineOptions below so every Save persists it and
	// every reuse lookup compares it conjunctively -- see
	// contextfabric.ReuseKey's CHAOS-3833 doc comment.
	embedRetrievalIdentity, err := falkorgraph.EmbedRetrievalIdentityFromEnv(os.LookupEnv)
	if err != nil {
		return nil, "", fmt.Errorf("load context fabric embed retrieval identity: %w", err)
	}
	graphReader, err := falkorgraph.NewWithEmbedder(graphConfig, embedderOptions)
	if err != nil {
		return nil, "", fmt.Errorf("initialize context fabric graph adapter: %w", err)
	}
	return graphReader, embedRetrievalIdentity, nil
}

func buildContextFabricInvestigator(ctx context.Context, request buildRequest, postgres postgresComponents, clickhouse clickHouseComponents, orgModelConfigStore *pgmodelconfig.Store) (contextfabric.Investigator, *pginvestigation.Store, contextfabric.OrgModelRuntimeEvictor, contextfabric.ReuseInvalidator, *pgclarification.Sink, *pgstructureselection.Sink, error) {
	if !request.config.EnableContextFabricInvestigations || !falkorgraph.Configured(os.LookupEnv) {
		return nil, nil, nil, nil, nil, nil, nil
	}
	graphReader, embedRetrievalIdentity, err := buildContextFabricGraphReader(request, postgres, clickhouse, true)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	// handleVerifier (CHAOS-3900 P1.E) adapts graphrank.VerifyHandle over
	// the SAME devhealthsource.NewCensusFunc(clickhouse.queryClient)
	// construction buildContextFabricGraphReader above already wires onto
	// graphConfig.CensusFunc (when wireIdentityUniverse=true, which this
	// caller always passes -- see that function's own doc comment) --
	// wrapping the client a second time here is cheap (NewCensusFunc is a
	// stateless closure constructor, not a new connection) and keeps this
	// dependency visible at Engine's own construction site rather than
	// threading it out of buildContextFabricGraphReader's unrelated return
	// signature. contextfabric.Engine fails a handr_ redemption CLOSED
	// when this is nil (HandleVerifier's own doc comment); it is never
	// nil here because request.config.EnableContextFabricInvestigations
	// gating above already requires falkorgraph.Configured, and this
	// function's own ClickHouse component is always live by the time it
	// is called.
	censusFunc := devhealthsource.NewCensusFunc(clickhouse.queryClient)
	handleVerifier := contextfabric.HandleVerifier(func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, contextfabric.HandleVerificationReason) {
		ok, reason := graphrank.VerifyHandle(ctx, orgID, kind, patternID, value, censusFunc)
		return ok, contextfabric.HandleVerificationReason(reason)
	})
	// anchorVerifier (CHAOS-3900 P1.E, team-lead ruling) adapts
	// graphrank.VerifyAnchorClaimantUnique over the SAME
	// devhealthsource.IdentityUniverse construction
	// buildContextFabricGraphReader above already wires onto
	// graphConfig.IdentityUniverse -- same "wrap the client a second time,
	// cheaply" reasoning as handleVerifier above. contextfabric.Engine
	// fails an ancr_ redemption CLOSED when this is nil (AnchorVerifier's
	// own doc comment).
	identityUniverse := graphrank.IdentityUniverseFunc(func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return devhealthsource.IdentityUniverse(ctx, clickhouse.queryClient, orgID)
	})
	anchorVerifier := contextfabric.AnchorVerifier(func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, contextfabric.AnchorVerificationReason) {
		ok, reason := graphrank.VerifyAnchorClaimantUnique(ctx, orgID, kind, canonicalID, matchedTermHash, identityUniverse)
		return ok, contextfabric.AnchorVerificationReason(reason)
	})
	// anchorMembershipVerifier (CHAOS-4042, sol-max ruling) adapts
	// graphrank.VerifyAnchorClaimantMembership over the SAME identityUniverse
	// construction anchorVerifier above uses, PLUS (PR3) graphReader's own
	// AnchorMember method for the graph-side pinned-epoch reconciliation +
	// redemption re-authorization half -- binding/scope now actually reach
	// the graph read they were always threaded here for.
	anchorMembershipVerifier := contextfabric.AnchorMembershipVerifier(func(ctx context.Context, principal storage.Principal, scope contractsv1.ContextFabricRequestedScope, binding contextfabric.ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, contextfabric.AnchorVerificationReason) {
		ok, reason := graphrank.VerifyAnchorClaimantMembership(ctx, principal, scope, binding, kind, canonicalID, matchedTermHash, identityUniverse, graphReader.AnchorMember)
		return ok, contextfabric.AnchorVerificationReason(reason)
	})
	// candidateVerifier (CHAOS-4012) adapts the SAME graphReader.AnchorMember
	// construction anchorMembershipVerifier above uses -- the graph-side
	// existence+re-authorization half ALONE, no identityUniverse layer:
	// see CandidateVerifier's own doc comment (structure.go) for why a
	// candidate offer's redemption proof stops at "does this (kind,
	// canonical_id) still exist and remain authorized," never a term-
	// uniqueness claim. No new graph backend method, no new composition-
	// root dependency.
	candidateVerifier := contextfabric.CandidateVerifier(func(ctx context.Context, principal storage.Principal, scope contractsv1.ContextFabricRequestedScope, binding contextfabric.ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID string) (bool, contextfabric.CandidateVerificationReason) {
		result, err := graphReader.AnchorMember(ctx, principal, scope, binding, kind, canonicalID)
		if err != nil {
			return false, contextfabric.CandidateVerificationGraphUnverifiable
		}
		if result.Unverifiable {
			return false, contextfabric.CandidateVerificationGraphUnverifiable
		}
		if !result.Exists || !result.Authorized {
			return false, contextfabric.CandidateVerificationClaimLost
		}
		return true, contextfabric.CandidateVerificationValid
	})
	// priorConsultant (CHAOS-3977 P5, design brief §3.4) is nil unless
	// ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_ENABLED is set -- the deployment-
	// wide half of the "flag-gated per org, default OFF" gate; the per-org
	// half is the active-version pointer itself (pgstructurepriors.Store.GetActive
	// degrades to "no active version" for any org that was never flipped,
	// DP8(a)). Shares postgres.db with every other Postgres-backed
	// dependency in this function -- read-only, no separate connection.
	var priorConsultant contextfabric.PriorConsultant
	var priorHandleGrammarChecker contextfabric.HandleGrammarChecker
	if request.config.StructurePriorsEnabled {
		priorStore, err := pgstructurepriors.NewStore(postgres.db)
		if err != nil {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("initialize structure prior store: %w", err)
		}
		priorConsultant = contextfabric.NewPriorConsultant(priorStore)
		// priorHandleGrammarChecker mirrors graphConfig.HandleGrammarChecker
		// (buildContextFabricGraphReader, above) exactly -- the SAME pure,
		// no-I/O registry lookup, duplicated here because Engine's own prior
		// consultation (priors_consult.go) merges AFTER ResolveSubjects
		// returns, outside that function's own call boundary -- see
		// EngineDependencies.PriorHandleGrammarChecker's own doc comment.
		priorHandleGrammarChecker = func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
			if !graphrank.ValidateHandleGrammar(graphrank.CensusKind(kind), patternID, value) {
				return "", false
			}
			return graphrank.HandleSourceColumn(graphrank.CensusKind(kind), patternID)
		}
	}
	factRegistry, err := contextfabric.NewFactCapabilityRegistry(
		devhealthfacts.NewProviders(clickhouse.queryClient),
		// CHAOS-4099 stage 2: the real ScopeExpander over the SAME
		// ClickHouse client every FactProvider above shares -- activating
		// the 3 ratified project-origin policies (fact_scope.go's own
		// Enabled flip). No control-flow change: an unwired ScopeExpander
		// would have made every enabled policy fail closed to
		// policy_unavailable exactly as stage 1 did (NewFactReadScopeResolver's
		// own doc comment), so this line is the ENTIRE activation.
		contextfabric.FactRegistryOptions{Now: request.options.Now, ScopeExpander: devhealthfacts.NewScopeExpander(clickhouse.queryClient)},
	)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("initialize canonical fact registry: %w", err)
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
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("initialize investigation result store: %w", err)
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
	// comment and newContextFabricModelRuntime. CHAOS-3742: an explicit
	// ModelRuntimeOverride takes priority over the env-driven deployment
	// runtime -- see Options.ModelRuntimeOverride's doc comment. Every
	// real caller leaves this nil, so behavior is unchanged unless a
	// caller opts in.
	deploymentDefaultRuntime := request.options.ModelRuntimeOverride
	if deploymentDefaultRuntime == nil {
		deploymentDefaultRuntime, err = newContextFabricModelRuntime(ctx, os.LookupEnv)
		if err != nil {
			return nil, nil, nil, nil, nil, nil, err
		}
	}
	modelRuntime, evictor, err := wrapWithOrgModelRuntimeResolver(deploymentDefaultRuntime, orgModelConfigStore, os.LookupEnv)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	receiptSink, err := buildModelReceiptSink(postgres)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
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
	// CHAOS-3859 (capture-only phase): unconditional, independent of
	// reuseEnabled -- capture only needs PriorSubjectReceipts resolution
	// (e.results != nil, i.e. the investigator itself being composed),
	// which does not require answer reuse to be turned on. Constructed
	// here, as the LAST fallible step before NewEngine, deliberately: an
	// earlier construction point would need its own Close() on every
	// subsequent error return in this function to avoid leaking its
	// background worker goroutine, and this is the only remaining one.
	// See pgclarification.Sink's own doc comment for the fail-open,
	// bounded-queue contract it upholds; open() below is responsible for
	// Close()-ing it before the Postgres pool it shares with everything
	// else in this function.
	clarificationSink, err := pgclarification.NewSink(postgres.db, pgclarification.SinkOptions{Logger: request.options.Logger})
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("initialize clarification selection sink: %w", err)
	}
	// CHAOS-3927 P4: clarificationSink's own structure-offer twin, SAME
	// unconditional/reuseEnabled-independent construction reasoning as
	// clarificationSink immediately above -- capture only needs
	// e.results != nil, not answer reuse.
	structureSelectionSink, err := pgstructureselection.NewSink(postgres.db, pgstructureselection.SinkOptions{Logger: request.options.Logger})
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = clarificationSink.Close(closeCtx)
		cancel()
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("initialize structure selection sink: %w", err)
	}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: modelRuntime, Sink: receiptSink},
		Graph:       graphReader,
		Facts:       factRegistry,
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: modelRuntime, Sink: receiptSink, Options: contextFabricSynthesizerOptions(request.options.ServiceVersion)},
		Results:     investigationStore,
		ReuseGate:   investigationStore,
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
		//
		// CHAOS-4103: request.options.EngineTelemetry, when set, REPLACES
		// this default -- same convention graphConfig.ResolutionTracer
		// below already uses (see Options.EngineTelemetry's own doc
		// comment) -- letting a caller capture events (e.g. the synthesis-
		// status override's outcome) in-process instead of only reaching
		// them by parsing slog output.
		Telemetry: contextFabricEngineTelemetry(request.options),
		// CHAOS-3859 (capture-only phase): unconditional -- see
		// clarificationSink's own construction comment above for why this
		// does not depend on reuseEnabled.
		ClarificationSelectionSink: clarificationSink,
		// CHAOS-3927 P4: see structureSelectionSink's own construction
		// comment above for why this does not depend on reuseEnabled.
		StructureSelectionSink: structureSelectionSink,
		// CHAOS-3900 P1.E: see handleVerifier's own construction comment
		// above.
		HandleVerifier: handleVerifier,
		// CHAOS-3900 P1.E: see anchorVerifier's own construction comment
		// above.
		AnchorVerifier: anchorVerifier,
		// CHAOS-4042: see anchorMembershipVerifier's own construction
		// comment above.
		AnchorMembershipVerifier: anchorMembershipVerifier,
		// CHAOS-4012: see candidateVerifier's own construction comment above.
		CandidateVerifier: candidateVerifier,
		// CHAOS-3977 P5: nil unless ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_ENABLED
		// -- see priorConsultant's own construction comment above.
		PriorConsultant:           priorConsultant,
		PriorHandleGrammarChecker: priorHandleGrammarChecker,
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
		// CHAOS-3833: one options field carries both retrieval
		// discriminators to BOTH sides (every Save and every lookup key),
		// so the persisted columns and the compared predicates cannot
		// drift within this process.
		ReuseRetrievalIdentity: contextfabric.ReuseRetrievalIdentity{
			EmbedRetrievalIdentity: embedRetrievalIdentity,
			RetrievalPolicyVersion: falkorgraph.RetrievalPolicyVersion,
		},
		// CHAOS-3862: the deployment-current interpretation/synthesis
		// prompt versions, read from the SAME genkitruntime defaulting
		// newContextFabricModelRuntime's underlying genkitruntime.New call
		// uses whenever Config leaves either field unset (which every
		// production caller does today -- see modelprovider.runtimeConfig)
		// -- so a prompt bump can never drift from what this value reports.
		// Wired unconditionally, the same way ReuseProjectionVersion and
		// ReuseModelIdentities above are: independent of whether
		// Options.ModelRuntimeOverride is set for a diagnostic trial run,
		// mirroring how those two dimensions are already deployment-static
		// rather than override-aware.
		ReusePromptVersions: contextfabric.ReusePromptVersions{
			InterpretationPromptVersion: genkitruntime.DefaultInterpretationPromptVersion,
			SynthesisPromptVersion:      genkitruntime.DefaultSynthesisPromptVersion,
		},
		// CHAOS-3862 round 2 (sol review class-close): three MORE
		// deployment-current version authorities, read from the SAME
		// constants contextFabricSynthesizerOptions above already stamps
		// on every fresh result's Versions.QueryVersion/
		// CanonicalServiceVersion (devhealthfacts.QueryVersion,
		// contextfabric.CanonicalFactRegistryVersion), plus the same
		// genkitruntime schema-version default the prompt-version pair
		// above already reads from. Wired unconditionally for the same
		// reason as ReusePromptVersions immediately above.
		ReuseVersionAuthorities: contextfabric.ReuseVersionAuthorities{
			QueryVersion:             devhealthfacts.QueryVersion,
			CanonicalServiceVersion:  contextfabric.CanonicalFactRegistryVersion,
			ModelOutputSchemaVersion: genkitruntime.DefaultSchemaVersion,
			// CHAOS-3884: one more deployment-current version authority --
			// graphrank's own NormalizeAliasTerm definition. Wired
			// unconditionally, same reasoning as its three siblings.
			IdentityNormalizationVersion: graphrank.IdentityNormalizationVersion,
			// CHAOS-3900 W1: one more deployment-current version authority --
			// contextfabric's own window class/default-table/binder rules.
			// Wired unconditionally, same reasoning as its four siblings.
			WindowInferenceVersion: contextfabric.WindowInferenceVersion,
			// CHAOS-4085: one more deployment-current version authority --
			// contextfabric's own commit-gate rules. Wired unconditionally,
			// same reasoning as its five siblings; see
			// ReuseKey.CommitGateVersion for why this dimension's absence
			// would be a safety hole rather than a staleness one.
			CommitGateVersion: contextfabric.CommitGateVersion,
		},
	})
	if err != nil {
		// clarificationSink/structureSelectionSink's workers must not
		// outlive this function on a failed composition -- NewEngine is
		// the only fallible step after both are constructed, so this is
		// the only cleanup site that needs them. A short bounded context
		// is enough: both queues are guaranteed empty (nothing has called
		// RecordSelection on an engine that was never returned), so Close
		// only needs to signal each worker to exit.
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = clarificationSink.Close(closeCtx)
		_ = structureSelectionSink.Close(closeCtx)
		cancel()
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("initialize context fabric engine: %w", err)
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
		return engine, investigationStore, nil, reuseInvalidator, clarificationSink, structureSelectionSink, nil
	}
	return engine, investigationStore, evictor, reuseInvalidator, clarificationSink, structureSelectionSink, nil
}

// contextFabricSynthesizerOptions is the complete static version identity
// hosted composition stamps on every InvestigationResult.
//
// It is a named function, not an inline literal, because CHAOS-3810 codex
// round-1 P1 was exactly a half-filled literal: Backend and
// ProjectionVersion were supplied while QueryVersion and
// CanonicalServiceVersion were left to the "unwired" placeholder even though
// both have real authorities in this repository. A field omitted from a
// literal is invisible; a field omitted from this function is a test failure
// (see the hosted composition test that asserts every field against its
// authority).
//
// Why this matters most on the terminal path: a synthesized result could
// still recover CanonicalServiceVersion from the fact bundle it read
// (Engine falls back to facts.Version), but Engine's terminal
// clarification_required/no_match result reads NO bundle -- it takes its
// static versions from this struct alone, through
// RuntimeAnswerSynthesizer.StaticResultVersions -- so an unset field there
// is permanently unattributable across a rebuild.
func contextFabricSynthesizerOptions(serviceVersion string) contextfabric.RuntimeAnswerSynthesizerOptions {
	return contextfabric.RuntimeAnswerSynthesizerOptions{
		ServiceVersion:          serviceVersion,
		Backend:                 "graph",
		ProjectionVersion:       contextFabricProjectionVersion,
		QueryVersion:            devhealthfacts.QueryVersion,
		CanonicalServiceVersion: contextfabric.CanonicalFactRegistryVersion,
	}
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
// devhealthsource.ClickHouseSourceVersion, .EpisodesSourceVersion, and
// .TeamsProjectsSourceVersion are the real, deliberately-bumped identities
// devhealthsource itself already uses to force a rebuild when its own
// projection mapping changes (see ClickHouseSourceVersion's doc comment) --
// composing them is the most direct, already-established authority
// available, short of this binary (acr-api, the READ side) somehow tracking
// every possible acr-projector (the WRITE side, a different binary) source
// version live, which nothing here has a channel for.
//
// ALL source versions compose here (CHAOS-3833, embed-text spec v2 §4
// P1-2). The previous form omitted TeamsProjectsSourceVersion -- the
// omission comment ("still a stub returning zero rows") had gone stale the
// moment CHAOS-3802 made it a real producer -- so a teams/projects-only
// producer change would not have moved the reuse key at all, and stored
// answers would have kept reusing across a semantic change to those
// subjects' projection.
const contextFabricProjectionVersion = devhealthsource.ClickHouseSourceVersion + "+" + devhealthsource.EpisodesSourceVersion + "+" + devhealthsource.TeamsProjectsSourceVersion

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
