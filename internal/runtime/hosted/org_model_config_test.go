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
	runtime, evictor, err := wrapWithOrgModelRuntimeResolver(deploymentDefault, nil, envLookup(nil))
	if err != nil {
		t.Fatalf("wrapWithOrgModelRuntimeResolver() = %v, want success", err)
	}
	if runtime != contextfabric.ModelRuntime(deploymentDefault) {
		t.Fatal("wrapWithOrgModelRuntimeResolver() wrapped the runtime despite a nil org store")
	}
	if evictor != nil {
		t.Fatalf("evictor = %#v, want nil when no org store is configured", evictor)
	}

	nilRuntime, nilEvictor, err := wrapWithOrgModelRuntimeResolver(nil, nil, envLookup(nil))
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
	_, evictor, err := wrapWithOrgModelRuntimeResolver(nil, orgConfigs, envLookup(nil))
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
