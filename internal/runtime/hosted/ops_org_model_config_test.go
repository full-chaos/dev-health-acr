package hosted

import (
	"context"
	"errors"
	"github.com/full-chaos/dev-health-go/llm"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type fakeOrgLLMSource struct {
	config entitlements.OrgLLMConfig
	found  bool
	err    error
	calls  int
}

func (f *fakeOrgLLMSource) OrgLLMConfig(context.Context, string) (entitlements.OrgLLMConfig, bool, error) {
	f.calls++
	return f.config, f.found, f.err
}

type entitlementWithLLMConfig struct {
	localEntitlement
	*fakeOrgLLMSource
}

func TestBuildOrgModelConfigResolverPicksTheOpsSource(t *testing.T) {
	source := &fakeOrgLLMSource{}
	withOps := entitlementWithLLMConfig{fakeOrgLLMSource: source}
	env := func(value string) func(string) (string, bool) {
		return envLookup(map[string]string{EnvOrgLLMConfigSource: value})
	}
	got, err := buildOrgModelConfigResolver(withOps, env(""), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(opsOrgModelConfigs); !ok {
		t.Fatalf("unset must default to the ops source, got %T", got)
	}
	if got, err := buildOrgModelConfigResolver(localEntitlement{}, env(""), discardLogger()); err != nil || got != nil {
		t.Fatalf("unset without the Dev Health client: %v %v, want a nil interface", got, err)
	}
	if got, err := buildOrgModelConfigResolver(withOps, env("off"), discardLogger()); err != nil || got != nil {
		t.Fatalf("off: %v %v, want a nil interface", got, err)
	}
	got, err = buildOrgModelConfigResolver(withOps, env("ops"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(opsOrgModelConfigs); !ok {
		t.Fatalf("ops source = %T", got)
	}
	if _, err := buildOrgModelConfigResolver(localEntitlement{}, env("ops"), discardLogger()); err == nil {
		t.Fatal("ops source without the Dev Health entitlement client was accepted")
	}
	if _, err := buildOrgModelConfigResolver(withOps, env("bogus"), discardLogger()); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

func TestOpsOrgModelConfigsMapsTheOpsAnswer(t *testing.T) {
	const digest = "9f2c4e5d0b7a61c3e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b"
	source := &fakeOrgLLMSource{found: true, config: entitlements.OrgLLMConfig{
		Provider: "anthropic", Model: "claude-haiku-5-5", APIKey: llm.NewSecret("sk-ant-x"), Version: digest,
	}}
	configs := opsOrgModelConfigs{source: source, generations: &versionGenerations{}}
	resolved, ok, err := configs.ResolveOrgModelConfig(context.Background(), "org")
	if err != nil || !ok {
		t.Fatalf("ok = %v err = %v", ok, err)
	}
	if resolved.Provider != "anthropic" || resolved.Model != "claude-haiku-5-5" || resolved.Credential.Reveal() != "sk-ant-x" || resolved.Generation < 1 {
		t.Fatalf("resolved = %s generation %d", resolved, resolved.Generation)
	}
	bad := *source
	bad.config.APIKey = llm.Secret{}
	if _, ok, err := (opsOrgModelConfigs{source: &bad, generations: &versionGenerations{}}).ResolveOrgModelConfig(context.Background(), "org"); ok || !errors.Is(err, entitlements.ErrLLMConfigUnusable) {
		t.Errorf("no key and no base URL: ok = %v err = %v, want unusable", ok, err)
	}
	none := &fakeOrgLLMSource{}
	if _, ok, err := (opsOrgModelConfigs{source: none, generations: &versionGenerations{}}).ResolveOrgModelConfig(context.Background(), "org"); ok || err != nil {
		t.Fatalf("no BYO: ok = %v err = %v, want ok=false and no error", ok, err)
	}
}

// TestGenerationMovesOnlyWhenOpsVersionChanges pins the cache key: ops'
// version is an opaque digest, so the generation is a per-organization
// counter that keeps its value for the same version and only grows.
func TestGenerationMovesOnlyWhenOpsVersionChanges(t *testing.T) {
	g := &versionGenerations{}
	first := g.generationFor("org_a", "digest-1")
	if again := g.generationFor("org_a", "digest-1"); again != first {
		t.Fatalf("same version: %d then %d", first, again)
	}
	changed := g.generationFor("org_a", "digest-2")
	if changed <= first {
		t.Fatalf("a new version got %d, not above %d", changed, first)
	}
	if back := g.generationFor("org_a", "digest-1"); back <= changed {
		t.Fatalf("a version that differs from the last one seen must move up: got %d after %d", back, changed)
	}
	if other := g.generationFor("org_b", "digest-2"); other == changed {
		t.Fatalf("another org shared a generation: %d", other)
	}
}

type countingRuntime struct {
	contextfabric.ModelRuntime
	calls int
}

func (c *countingRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	c.calls++
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, nil
}

// TestAnUnusableOpsConfigNeverFallsBackToThePlatformModel drives the
// resolver the hosted runtime builds: an org whose ops config is unusable
// gets an unavailable model, never the deployment default; an org with no
// BYO config gets the deployment default.
func TestAnUnusableOpsConfigNeverFallsBackToThePlatformModel(t *testing.T) {
	platform := &countingRuntime{}
	built := 0
	build := func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		built++
		return &countingRuntime{}, nil
	}
	unusable := &fakeOrgLLMSource{err: errors.Join(entitlements.ErrLLMConfigUnusable, errors.New("ops answered 409"))}
	resolver := modelruntimeresolver.New(platform, opsOrgModelConfigs{source: unusable, generations: &versionGenerations{}}, build)
	_, _, err := resolver.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_a"}, contextfabric.InvestigationRequest{})
	if !errors.Is(err, contextfabric.ErrModelUnavailable) || platform.calls != 0 || built != 0 {
		t.Fatalf("err = %v platform calls = %d built = %d, want unavailable with no platform call", err, platform.calls, built)
	}
	none := &fakeOrgLLMSource{}
	resolver = modelruntimeresolver.New(platform, opsOrgModelConfigs{source: none, generations: &versionGenerations{}}, build)
	if _, _, err := resolver.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_b"}, contextfabric.InvestigationRequest{}); err != nil || platform.calls != 1 {
		t.Fatalf("no BYO: err = %v platform calls = %d, want the platform model", err, platform.calls)
	}
}

func TestOrgModelConfigSourceOffIsDetected(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		want  bool
	}{{"off", true, true}, {" off ", true, true}, {"ops", true, false}, {"", true, false}, {"", false, false}} {
		lookup := func(string) (string, bool) { return tc.value, tc.set }
		if got := orgModelConfigSourceOff(lookup); got != tc.want {
			t.Fatalf("orgModelConfigSourceOff(%q set=%t) = %t, want %t", tc.value, tc.set, got, tc.want)
		}
	}
}
