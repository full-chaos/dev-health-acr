package hosted

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelconfigcrypto"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// unreachablePostgres builds postgresComponents around a *sql.DB that never
// actually dials -- pgx's stdlib.OpenDB is lazy -- safe for composition-gate
// tests that only need NewStore's own db-not-nil check to pass, never a
// live query.
func unreachablePostgres(t *testing.T) postgresComponents {
	t.Helper()
	cfg, err := pgx.ParseConfig("postgres://unreachable:5432/db")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return postgresComponents{db: stdlib.OpenDB(*cfg)}
}

func encryptionKeyLookup() func(string) (string, bool) {
	return envLookup(map[string]string{
		modelconfigcrypto.EnvKeys:      "k1=" + testEncryptionKeyBase64,
		modelconfigcrypto.EnvActiveKID: "k1",
	})
}

// testEncryptionKeyBase64 is 32 zero bytes, base64-encoded -- a fixed,
// obviously-fake key for composition-gate tests. It is never used to
// encrypt anything real; these tests never construct a live database
// connection.
const testEncryptionKeyBase64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// TestBuildOrgModelConfigStore_returnsNilWithoutAnEncryptionKey is the
// CHAOS-3775 analog of TestNewContextFabricModelRuntime_keepsTheCleanFiveOhThreeWithoutACredential:
// composition never fails closed over this unconfigured optional
// dependency, and the model-config routes degrade to a clean 503 rather
// than the deployment failing to start.
func TestBuildOrgModelConfigStore_returnsNilWithoutAnEncryptionKey(t *testing.T) {
	store, err := buildOrgModelConfigStore(unreachablePostgres(t), envLookup(nil))
	if err != nil {
		t.Fatalf("buildOrgModelConfigStore() = %v, want no failure over an unconfigured optional dependency", err)
	}
	if store != nil {
		t.Fatalf("store = %#v, want nil so the model-config routes keep their clean 503", store)
	}
}

// TestBuildOrgModelConfigStore_failsClosed_onAMisconfiguredKey mirrors
// AC-3770-2's "opted in but mis-specified fails startup" behavior for the
// encryption key surface: a deployment that sets
// ACR_CONTEXT_FABRIC_CREDENTIAL_ENCRYPTION_KEYS to something that does not
// decode to a valid 32-byte key must find out at startup, not have the
// per-organization surface silently disabled.
func TestBuildOrgModelConfigStore_failsClosed_onAMisconfiguredKey(t *testing.T) {
	lookup := envLookup(map[string]string{
		modelconfigcrypto.EnvKeys:      "k1=not-valid-base64!!!",
		modelconfigcrypto.EnvActiveKID: "k1",
	})
	store, err := buildOrgModelConfigStore(unreachablePostgres(t), lookup)
	if err == nil {
		t.Fatal("buildOrgModelConfigStore() = nil error for an invalid encryption key")
	}
	if store != nil {
		t.Fatal("buildOrgModelConfigStore() returned a store alongside an error")
	}
}

// TestBuildOrgModelConfigStore_succeeds_whenKeyIsConfigured proves
// composition succeeds (a non-nil store, no error) once a valid key is
// configured, independent of any graph-backend or investigations flag --
// buildOrgModelConfigStore takes no such flag as input.
func TestBuildOrgModelConfigStore_succeeds_whenKeyIsConfigured(t *testing.T) {
	store, err := buildOrgModelConfigStore(unreachablePostgres(t), encryptionKeyLookup())
	if err != nil {
		t.Fatalf("buildOrgModelConfigStore() = %v, want success", err)
	}
	if store == nil {
		t.Fatal("buildOrgModelConfigStore() = nil store, want a composed store")
	}
}

// fakeRuntime is a minimal, named contextfabric.ModelRuntime so a test can
// assert exactly which runtime a wrapping decision produced.
type fakeRuntime struct{ name string }

func (f *fakeRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, errors.New("fake runtime " + f.name + " answered")
}

func (f *fakeRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, errors.New("fake runtime " + f.name + " answered")
}

// TestWrapWithOrgModelRuntimeResolver_passesThroughUnchanged_withNoOrgStore
// locks that a deployment with no encryption key configured gets the
// deployment-default runtime completely unwrapped -- pre-CHAOS-3775
// behavior, bit for bit, including the nil case.
func TestWrapWithOrgModelRuntimeResolver_passesThroughUnchanged_withNoOrgStore(t *testing.T) {
	deploymentDefault := &fakeRuntime{name: "default"}
	runtime, evictor, err := wrapWithOrgModelRuntimeResolver(deploymentDefault, nil, envLookup(nil), nil)
	if err != nil {
		t.Fatalf("wrapWithOrgModelRuntimeResolver() = %v, want success", err)
	}
	if runtime != contextfabric.ModelRuntime(deploymentDefault) {
		t.Fatal("wrapWithOrgModelRuntimeResolver() wrapped the runtime despite a nil org store")
	}
	if evictor != nil {
		t.Fatalf("evictor = %#v, want nil when no org store is configured", evictor)
	}

	nilRuntime, nilEvictor, err := wrapWithOrgModelRuntimeResolver(nil, nil, envLookup(nil), nil)
	if err != nil {
		t.Fatalf("wrapWithOrgModelRuntimeResolver() = %v, want success", err)
	}
	if nilRuntime != nil {
		t.Fatalf("runtime = %#v, want nil unchanged", nilRuntime)
	}
	if nilEvictor != nil {
		t.Fatalf("evictor = %#v, want nil unchanged", nilEvictor)
	}
}

// TestWrapWithOrgModelRuntimeResolver_returnsAUsableEvictor_whenOrgStoreIsConfigured
// is the composition-level half of Codex round-1 finding F4: when an org
// config store IS wired, wrapWithOrgModelRuntimeResolver must return a
// non-nil evictor whose EvictOrgModelRuntime is safe to call -- the exact
// value open() wires into api.RuntimeDependencies.OrgModelRuntimeEvictor
// for the DELETE route to use.
func TestWrapWithOrgModelRuntimeResolver_returnsAUsableEvictor_whenOrgStoreIsConfigured(t *testing.T) {
	orgConfigs, err := buildOrgModelConfigStore(unreachablePostgres(t), encryptionKeyLookup())
	if err != nil {
		t.Fatalf("buildOrgModelConfigStore: %v", err)
	}
	_, evictor, err := wrapWithOrgModelRuntimeResolver(nil, orgConfigs, envLookup(nil), nil)
	if err != nil {
		t.Fatalf("wrapWithOrgModelRuntimeResolver() = %v, want success", err)
	}
	if evictor == nil {
		t.Fatal("evictor = nil, want a usable OrgModelRuntimeEvictor when an org store is configured")
	}
	// Must not panic for an organization with nothing cached -- the DELETE
	// handler calls this unconditionally after every successful delete,
	// including the common case of an organization that was never cached
	// in the first place.
	evictor.EvictOrgModelRuntime("org-never-cached")
}

// TestContextFabricModelDefaults_fallsBackToPackageDefaults_whenUnconfigured
// covers the "per-organization BYO LLM with no deployment default" shape:
// tuning defaults must still resolve to sane values, not an error, when the
// deployment-default provider itself was never configured.
func TestContextFabricModelDefaults_fallsBackToPackageDefaults_whenUnconfigured(t *testing.T) {
	defaults, err := contextFabricModelDefaults(envLookup(nil))
	if err != nil {
		t.Fatalf("contextFabricModelDefaults() = %v, want success", err)
	}
	if defaults.Timeout != modelprovider.DefaultTimeout || defaults.MaxAttempts != modelprovider.DefaultMaxAttempts || defaults.MaxTransportRetries != modelprovider.DefaultMaxTransportRetries {
		t.Fatalf("defaults = %+v, want the package defaults", defaults)
	}
}

// orgModelConfigResolverFunc is a fake contextfabric.OrgModelConfigResolver
// driven by a plain function.
type orgModelConfigResolverFunc func(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error)

func (f orgModelConfigResolverFunc) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	return f(ctx, orgID)
}

// TestContextFabricReuseModelIdentityResolver_nilConfigsAlwaysUsesFallback
// covers the "no per-organization support wired at all" composition shape
// (buildOrgModelConfigStore returned nil): every organization's resolved
// identity must be the deployment-default fallback, matching pre-CHAOS-3775
// behavior exactly.
func TestContextFabricReuseModelIdentityResolver_nilConfigsAlwaysUsesFallback(t *testing.T) {
	resolver := contextFabricReuseModelIdentityResolver{fallback: []string{"deployment/default"}}
	got, err := resolver.ResolveReuseModelIdentity(context.Background(), "org_1")
	if err != nil {
		t.Fatalf("ResolveReuseModelIdentity() error = %v", err)
	}
	if len(got) != 1 || got[0] != "deployment/default" {
		t.Fatalf("ResolveReuseModelIdentity() = %v, want the fallback chain", got)
	}
}

// TestContextFabricReuseModelIdentityResolver_noConfigurationUsesFallback
// covers the AC-3775-3 "no configuration at all" shape (Configs is wired,
// but this specific organization has never set one): fallback, not an
// error.
func TestContextFabricReuseModelIdentityResolver_noConfigurationUsesFallback(t *testing.T) {
	resolver := contextFabricReuseModelIdentityResolver{
		configs: orgModelConfigResolverFunc(func(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
			return contextfabric.ResolvedOrgModelConfig{}, false, nil
		}),
		fallback: []string{"deployment/default"},
	}
	got, err := resolver.ResolveReuseModelIdentity(context.Background(), "org_1")
	if err != nil {
		t.Fatalf("ResolveReuseModelIdentity() error = %v", err)
	}
	if len(got) != 1 || got[0] != "deployment/default" {
		t.Fatalf("ResolveReuseModelIdentity() = %v, want the fallback chain", got)
	}
}

// TestContextFabricReuseModelIdentityResolver_configuredOrgUsesItsOwnIdentity
// is the CHAOS-3782 finding #3 fix's core assertion: an organization WITH
// a BYO configuration resolves to ITS OWN provider/model, not the
// deployment fallback -- proving the reuse lookup key can diverge
// per-organization, which a static identity never could.
func TestContextFabricReuseModelIdentityResolver_configuredOrgUsesItsOwnIdentity(t *testing.T) {
	resolver := contextFabricReuseModelIdentityResolver{
		configs: orgModelConfigResolverFunc(func(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
			return contextfabric.ResolvedOrgModelConfig{Provider: "anthropic", Model: "claude-x"}, true, nil
		}),
		fallback: []string{"deployment/default"},
	}
	got, err := resolver.ResolveReuseModelIdentity(context.Background(), "org_1")
	if err != nil {
		t.Fatalf("ResolveReuseModelIdentity() error = %v", err)
	}
	if len(got) != 1 || got[0] != "anthropic/claude-x" {
		t.Fatalf("ResolveReuseModelIdentity() = %v, want [%q]", got, "anthropic/claude-x")
	}
}

// TestChaos3786_ContextFabricReuseModelIdentityResolver_configuredOrgIncludesItsOwnFallback
// is the CHAOS-3786 fix's core assertion: an organization with BOTH a
// primary AND a FallbackModel configured resolves to a TWO-entry chain,
// primary first -- the reuse lookup must be able to match a candidate the
// fallback produced, not only one the primary produced.
func TestChaos3786_ContextFabricReuseModelIdentityResolver_configuredOrgIncludesItsOwnFallback(t *testing.T) {
	resolver := contextFabricReuseModelIdentityResolver{
		configs: orgModelConfigResolverFunc(func(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
			return contextfabric.ResolvedOrgModelConfig{Provider: "anthropic", Model: "claude-x", FallbackModel: "claude-y"}, true, nil
		}),
		fallback: []string{"deployment/default"},
	}
	got, err := resolver.ResolveReuseModelIdentity(context.Background(), "org_1")
	if err != nil {
		t.Fatalf("ResolveReuseModelIdentity() error = %v", err)
	}
	want := []string{"anthropic/claude-x", "anthropic/claude-y"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ResolveReuseModelIdentity() = %v, want %v", got, want)
	}
}

// TestContextFabricReuseModelIdentityResolver_resolveErrorNeverFallsBack is
// the AC-3775-3 prohibition applied to reuse: an organization whose
// configuration exists but cannot be read (e.g. an undecryptable
// credential) must get an error, never a silent fallback to the
// deployment-default identity -- falling back could wrongly match (and
// reuse) a stored row this organization's actual current configuration
// would never have produced.
func TestContextFabricReuseModelIdentityResolver_resolveErrorNeverFallsBack(t *testing.T) {
	sentinel := errors.New("credential no longer decrypts")
	resolver := contextFabricReuseModelIdentityResolver{
		configs: orgModelConfigResolverFunc(func(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
			return contextfabric.ResolvedOrgModelConfig{}, false, sentinel
		}),
		fallback: []string{"deployment/default"},
	}
	got, err := resolver.ResolveReuseModelIdentity(context.Background(), "org_1")
	if !errors.Is(err, sentinel) {
		t.Fatalf("ResolveReuseModelIdentity() error = %v, want %v", err, sentinel)
	}
	if len(got) != 0 {
		t.Fatalf("ResolveReuseModelIdentity() = %v, want empty on error (never the fallback)", got)
	}
}

// TestChaos3786_ContextFabricReuseModelIdentityResolver_dedupesEqualFallback
// is the codex round-1 P2 probe: pgmodelconfig reads a stored row back
// WITHOUT revalidation (see pgmodelconfig/store.go's decode path), so a
// row with FallbackModel == Model -- a shape the request-time
// modelprovider.Config.Validate() rejects, but that could still reach
// storage from a row written before that rule existed, or by a
// future/older binary -- can still reach this resolver. The resolved
// chain must contain the identity exactly once, not twice.
func TestChaos3786_ContextFabricReuseModelIdentityResolver_dedupesEqualFallback(t *testing.T) {
	resolver := contextFabricReuseModelIdentityResolver{
		configs: orgModelConfigResolverFunc(func(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
			return contextfabric.ResolvedOrgModelConfig{Provider: "anthropic", Model: "claude-x", FallbackModel: "claude-x"}, true, nil
		}),
		fallback: []string{"deployment/default"},
	}
	got, err := resolver.ResolveReuseModelIdentity(context.Background(), "org_1")
	if err != nil {
		t.Fatalf("ResolveReuseModelIdentity() error = %v", err)
	}
	want := []string{"anthropic/claude-x"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("ResolveReuseModelIdentity() = %v, want %v (deduplicated)", got, want)
	}
}

// TestAppendReuseIdentity_dedupesPreservingOrder is the direct unit probe
// for the codex round-1 P2 helper both contextFabricReuseModelIdentities
// (open.go) and contextFabricReuseModelIdentityResolver (this file) share.
func TestAppendReuseIdentity_dedupesPreservingOrder(t *testing.T) {
	got := appendReuseIdentity([]string{"a", "b"}, "a")
	want := []string{"a", "b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("appendReuseIdentity() = %v, want %v (no duplicate appended)", got, want)
	}
	got = appendReuseIdentity([]string{"a"}, "b")
	want = []string{"a", "b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("appendReuseIdentity() = %v, want %v (distinct candidate appended)", got, want)
	}
}
