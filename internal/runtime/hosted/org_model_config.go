package hosted

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelconfigcrypto"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelconfig"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelreceipts"
)

// buildOrgModelConfigStore composes the per-organization BYO LLM
// configuration store (CHAOS-3775). It returns (nil, nil, nil) -- the same
// "unconfigured optional dependency never fails composition closed"
// convention buildContextFabricInvestigator and newContextFabricModelRuntime
// already use -- when no credential encryption key is configured: the
// api.App model-config routes stay registered, authorized, and audited, and
// answer a clean 503 for every request (api.App's nil-store handling,
// mirroring the nil-Investigator case), and every organization uses the
// deployment-default runtime unchanged.
//
// Composed independently of ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED /
// falkorgraph.Configured (unlike the investigator): a customer must be able
// to enter and save their organization's BYO LLM configuration before or
// independently of the graph backend being wired, since the product-facing
// configuration surface has no dependency on graph readiness.
func buildOrgModelConfigStore(postgres postgresComponents, lookup func(string) (string, bool)) (*pgmodelconfig.Store, error) {
	if !modelconfigcrypto.Configured(lookup) {
		return nil, nil
	}
	cipher, err := modelconfigcrypto.NewFromEnv(lookup)
	if err != nil {
		return nil, fmt.Errorf("load context fabric credential encryption keys: %w", err)
	}
	store, err := pgmodelconfig.NewStore(postgres.db, cipher)
	if err != nil {
		return nil, fmt.Errorf("initialize context fabric org model config store: %w", err)
	}
	return store, nil
}

// wrapWithOrgModelRuntimeResolver wraps deploymentDefault (which may itself
// be nil -- no deployment-default provider configured) with per-organization
// resolution (CHAOS-3775) when orgConfigs is non-nil. When orgConfigs is
// nil, it returns deploymentDefault completely unchanged, and a nil
// evictor: pre-CHAOS-3775 behavior, bit for bit, is what a deployment with
// no encryption key configured gets.
//
// The second return value is the SAME *modelruntimeresolver.Resolver
// wrapped into the first, exposed as contextfabric.OrgModelRuntimeEvictor
// so internal/api's DELETE model-config handler can purge a cached runtime
// immediately after a successful delete (Codex round-1 finding F4) --
// callers must apply the typed-nil-interface guard this package's other
// optional-dependency wiring already uses (see open.go's orgModelConfigs
// conversion) before assigning it into an interface-typed field.
//
// telemetry (CHAOS-4355 follow-up) is stamped onto defaults before
// NewModelProviderBuild closes over it, so every per-organization runtime
// this resolver builds reports RecordModelRowsStripped through the SAME
// sink the deployment-default runtime uses (open()'s own engineTelemetry),
// never a second, independently-resolved instance.
func wrapWithOrgModelRuntimeResolver(deploymentDefault contextfabric.ModelRuntime, orgConfigs *pgmodelconfig.Store, lookup func(string) (string, bool), telemetry contextfabric.EngineTelemetry, logger *slog.Logger) (contextfabric.ModelRuntime, *modelruntimeresolver.Resolver, error) {
	if orgConfigs == nil {
		return deploymentDefault, nil, nil
	}
	defaults, err := contextFabricModelDefaults(lookup)
	if err != nil {
		return nil, nil, fmt.Errorf("load context fabric model defaults for per-organization runtimes: %w", err)
	}
	defaults.Telemetry = telemetry
	// CHAOS-5380: stamped alongside Telemetry, for the same reason -- a
	// per-organization BYO runtime's decision line (and its attempt fields)
	// must reach the same collected sink the deployment default's does.
	defaults.Logger = logger
	resolver := newOrgModelRuntimeResolver(deploymentDefault, orgConfigs, modelruntimeresolver.NewModelProviderBuild(defaults), logger)
	return resolver, resolver, nil
}

// contextFabricReuseModelIdentityResolver implements
// contextfabric.ReuseModelIdentityResolver (CHAOS-3782, Codex round-2
// finding #3; chain-widened by CHAOS-3786) by resolving the SAME
// organization-effective configuration modelruntimeresolver.Resolver.
// runtimeFor would (configs, falling through to fallback when the
// organization has none) -- without building a contextfabric.ModelRuntime,
// since a reuse lookup only needs the identity strings, never a genkit
// instance. configs may be nil (no per-organization support wired at
// all), in which case every organization's resolved chain is simply
// fallback, matching pre-CHAOS-3782 (and pre-CHAOS-3775) behavior.
//
// fallback (CHAOS-3786) is itself already a full chain -- the
// deployment-default's own [primary, fallback-if-configured], computed by
// contextFabricReuseModelIdentities -- not a single identity, so an
// organization with no BYO configuration of its own still gets the
// deployment default's FALLBACK model as a valid reuse match, exactly
// mirroring what modelruntimeresolver actually runs for it.
type contextFabricReuseModelIdentityResolver struct {
	configs  contextfabric.OrgModelConfigResolver
	fallback []string
}

func (r contextFabricReuseModelIdentityResolver) ResolveReuseModelIdentity(ctx context.Context, orgID string) ([]string, error) {
	if r.configs == nil || strings.TrimSpace(orgID) == "" {
		return r.fallback, nil
	}
	resolved, ok, err := r.configs.ResolveOrgModelConfig(ctx, orgID)
	if err != nil {
		// AC-3775-3's prohibition applies here too: an organization whose
		// configuration exists but cannot be read must never fall back to
		// the deployment-default chain as a substitute -- that could
		// wrongly match (and reuse) a row this organization's ACTUAL
		// current configuration would never have produced. The caller
		// (Engine.tryReuse) treats this error as a plain cache miss.
		return nil, err
	}
	if !ok {
		return r.fallback, nil
	}
	provider := strings.TrimSpace(resolved.Provider)
	model := strings.TrimSpace(resolved.Model)
	if provider == "" || model == "" {
		return r.fallback, nil
	}
	// CHAOS-3786: include the org's OWN fallback model too, not only its
	// primary -- a candidate that organization's fallback actually
	// produced must be able to match.
	identities := []string{provider + "/" + model}
	if fallbackModel := strings.TrimSpace(resolved.FallbackModel); fallbackModel != "" {
		identities = appendReuseIdentity(identities, provider+"/"+fallbackModel)
	}
	return identities, nil
}

// appendReuseIdentity appends candidate to identities unless it is already
// present, preserving order (CHAOS-3786, codex round-1 P2). A configured
// FallbackModel equal to the primary Model is rejected by
// modelprovider.Config.Validate() on the request path, but a row already
// persisted in pgmodelconfig is read back WITHOUT revalidation
// (pgmodelconfig/store.go's decode path), so a stored row that predates a
// tightened validation rule -- or one written by a future/older binary --
// can still reach here with FallbackModel == Model. Without this guard,
// such a chain would carry the same identity twice; harmless for
// FindReusable's `= ANY(...)` correctness, but it wastes a comparison and
// makes a debug dump of the resolved chain misleadingly imply two distinct
// models are in play.
func appendReuseIdentity(identities []string, candidate string) []string {
	for _, existing := range identities {
		if existing == candidate {
			return identities
		}
	}
	return append(identities, candidate)
}

// contextFabricModelDefaults returns the Timeout/MaxAttempts/
// MaxTransportRetries tuning a per-organization BYO LLM runtime inherits
// from the deployment surface (§19.3.2: those knobs are not part of the
// per-organization contract, only Provider/BaseURL/Model/FallbackModel/
// Credential are). When the deployment-default provider itself is not
// configured -- an operator may support ONLY per-organization BYO LLM, with
// no deployment default -- this falls back to modelprovider's own package
// defaults rather than failing: those defaults are exactly what an
// unconfigured deployment-default Config would have used anyway.
func contextFabricModelDefaults(lookup func(string) (string, bool)) (modelprovider.Config, error) {
	if !modelprovider.Configured(lookup) {
		return modelprovider.Config{
			Timeout: modelprovider.DefaultTimeout, MaxAttempts: modelprovider.DefaultMaxAttempts,
			MaxTransportRetries: modelprovider.DefaultMaxTransportRetries,
		}, nil
	}
	// Configured() is already true, so newContextFabricModelRuntime (called
	// separately, before this) has already parsed and validated this same
	// environment successfully -- re-parsing here is a second cheap,
	// side-effect-free startup-time read, not a duplicated failure surface.
	return modelprovider.ConfigFromEnv(lookup)
}

// buildModelReceiptSink composes the durable ModelExecutionReceipt sink
// (CHAOS-3775, AC-3775-6; closes drift item D16, confirmed in §19.13: no
// non-test ModelReceiptSink implementation existed anywhere on main).
// Unconditional -- every hosted deployment has a Postgres connection, and a
// receipt is worth recording for the deployment-default runtime too, not
// only for per-organization runtimes.
func buildModelReceiptSink(postgres postgresComponents) (contextfabric.ModelReceiptSink, error) {
	store, err := pgmodelreceipts.NewStore(postgres.db)
	if err != nil {
		return nil, fmt.Errorf("initialize context fabric model receipt sink: %w", err)
	}
	return store, nil
}

// sampledModelRuntime returns runtime as a contextfabric.SampledModelRuntime
// when it can produce per-sample interpretations, and nil when it cannot
// (CHAOS-5638).
//
// A NIL RETURN IS NOT A DEGRADE. The interpreter pairs this with
// EnsembleSize: at the production default of 1 the field is never read, and
// at N>1 a nil here surfaces as ErrEnsembleRuntimeMissing rather than as a
// quiet fall back to one sample. So a composition can only be wrong LOUDLY --
// which is the property the whole seam is built around.
//
// The assertion lives here, at the composition, rather than inside the
// interpreter: this is the one place that knows what was actually built.
//
// NO `if !ok` BRANCH, deliberately. A failed type assertion to an INTERFACE
// type already yields that interface's zero value, which is nil, so an
// explicit branch returning nil would be a second way of saying the same
// thing -- and one no test can distinguish from its absence, which a mutation
// battery proved by surviving it. The comma-ok form stays because dropping it
// entirely would panic instead of returning.
func sampledModelRuntime(runtime contextfabric.ModelRuntime) contextfabric.SampledModelRuntime {
	// TYPED NIL IS NOT NIL, and a plain `== nil` does not catch it: an
	// interface holding a (*T)(nil) is non-nil, satisfies the assertion, and
	// panics on the first call. This package already had isNilRuntime for
	// exactly that shape and this function was written without it, so a
	// reviewer reached the panic.
	//
	// ONE CHECK, AFTER the assertion, and the placement is the whole of it.
	// A pre-assertion guard reads as more careful and is redundant: whatever
	// the input, `sampled` here is either a nil interface (the assertion
	// failed) or an interface holding the same nil pointer (it succeeded),
	// and this catches both. A mutation battery proved the pre-guard
	// unkillable, which is the same thing said in evidence.
	sampled, _ := runtime.(contextfabric.SampledModelRuntime)
	if sampled == nil || isNilRuntime(sampled) {
		return nil
	}
	return sampled
}

// interpretationEnsembleSize normalises the composition's requested N.
//
// Zero -- the field's own zero value, and every production caller today --
// means 1: one interpret sample, the pre-ensemble path, unchanged. A negative
// value means the same rather than an error, matching BoundEnsembleSize's own
// treatment of a nonsensical configuration as "take the safe default" instead
// of failing a composition over a number.
func interpretationEnsembleSize(configured int) int {
	if configured < 1 {
		return 1
	}
	return configured
}

// newOrgModelRuntimeResolver constructs the resolver AND gives it everything
// it needs of its own.
//
// EXTRACTED (CHAOS-5638) because the logger assignment below is the kind of
// line that is invisible when it is missing. `defaults.Logger` and
// `resolver.Logger` are different wires -- the first reaches the
// per-organization runtime this resolver BUILDS, the second reaches the
// resolver itself -- and the first was set while the second was not, which
// sent the per-sample warning to slog.Default() where the service's collected
// sink never sees it. A caller reading wrapWithOrgModelRuntimeResolver saw one
// `Logger =` line and no reason to look for a second. One function now owns
// both halves, and a test can drive it without a live Postgres store.
func newOrgModelRuntimeResolver(
	deploymentDefault contextfabric.ModelRuntime,
	orgConfigs contextfabric.OrgModelConfigResolver,
	build modelruntimeresolver.Build,
	logger *slog.Logger,
) *modelruntimeresolver.Resolver {
	resolver := modelruntimeresolver.New(deploymentDefault, orgConfigs, build)
	resolver.Logger = logger
	return resolver
}

// newContextFabricQuestionInterpreter builds the interpreter the engine uses.
//
// A NAMED CONSTRUCTOR rather than a struct literal inline in the composition
// (CHAOS-5638), so the ensemble wiring is reachable by a test. As a literal,
// deleting `SampledRuntime` left every test green while making the ensemble
// permanently unreachable in the built product -- the composition is exactly
// where that kind of omission hides, because nothing downstream of it can tell
// "not configured" from "configured and dropped on the floor".
func newContextFabricQuestionInterpreter(
	modelRuntime contextfabric.ModelRuntime,
	receiptSink contextfabric.ModelReceiptSink,
	engineTelemetry contextfabric.EngineTelemetry,
	factRegistry contextfabric.RequirementDeriver,
	configuredEnsembleSize int,
) contextfabric.RuntimeQuestionInterpreter {
	return contextfabric.RuntimeQuestionInterpreter{
		Runtime:         modelRuntime,
		SampledRuntime:  sampledModelRuntime(modelRuntime),
		EnsembleSize:    interpretationEnsembleSize(configuredEnsembleSize),
		Sink:            receiptSink,
		FamilyTelemetry: engineTelemetry,
		FrameTelemetry:  engineTelemetry,
		Requirements:    factRegistry,
	}
}
