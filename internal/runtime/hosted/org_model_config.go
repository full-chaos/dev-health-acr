package hosted

import (
	"context"
	"fmt"
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
func wrapWithOrgModelRuntimeResolver(deploymentDefault contextfabric.ModelRuntime, orgConfigs *pgmodelconfig.Store, lookup func(string) (string, bool)) (contextfabric.ModelRuntime, *modelruntimeresolver.Resolver, error) {
	if orgConfigs == nil {
		return deploymentDefault, nil, nil
	}
	defaults, err := contextFabricModelDefaults(lookup)
	if err != nil {
		return nil, nil, fmt.Errorf("load context fabric model defaults for per-organization runtimes: %w", err)
	}
	resolver := modelruntimeresolver.New(deploymentDefault, orgConfigs, modelruntimeresolver.NewModelProviderBuild(defaults))
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
		identities = append(identities, provider+"/"+fallbackModel)
	}
	return identities, nil
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
