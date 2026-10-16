package hosted

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelconfig"
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

func TestBuildOrgModelConfigResolverPicksOneStore(t *testing.T) {
	source := &fakeOrgLLMSource{}
	withOps := entitlementWithLLMConfig{fakeOrgLLMSource: source}
	env := func(value string) func(string) (string, bool) {
		return envLookup(map[string]string{EnvOrgLLMConfigSource: value})
	}
	if got, err := buildOrgModelConfigResolver(nil, withOps, env(""), discardLogger()); err != nil || got != nil {
		t.Fatalf("unset with no acr store: %v %v, want a nil interface", got, err)
	}
	got, err := buildOrgModelConfigResolver(nil, withOps, env("ops"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(opsOrgModelConfigs); !ok {
		t.Fatalf("ops source = %T", got)
	}
	if _, err := buildOrgModelConfigResolver(nil, localEntitlement{}, env("ops"), discardLogger()); err == nil {
		t.Fatal("ops source without the Dev Health entitlement client was accepted")
	}
	if _, err := buildOrgModelConfigResolver(&pgmodelconfig.Store{}, withOps, env("ops"), discardLogger()); err == nil || !strings.Contains(err.Error(), "one BYO store") {
		t.Fatalf("ops source beside acr's store: err = %v", err)
	}
	if _, err := buildOrgModelConfigResolver(nil, withOps, env("acr"), discardLogger()); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

func TestOpsOrgModelConfigsMapsTheOpsAnswer(t *testing.T) {
	source := &fakeOrgLLMSource{found: true, config: entitlements.OrgLLMConfig{
		Provider: "anthropic", Model: "claude-haiku-5-5", APIKey: "sk-ant-x", Version: "2026-10-16T18:00:00.123456789Z",
	}}
	resolved, ok, err := opsOrgModelConfigs{source: source}.ResolveOrgModelConfig(context.Background(), "org")
	if err != nil || !ok {
		t.Fatalf("ok = %v err = %v", ok, err)
	}
	if resolved.Provider != "anthropic" || resolved.Model != "claude-haiku-5-5" || resolved.Credential != "sk-ant-x" || resolved.Generation != 1792173600123456789 {
		t.Fatalf("resolved = %s generation %d", resolved, resolved.Generation)
	}
	for name, mutate := range map[string]func(*entitlements.OrgLLMConfig){
		"a version that is not RFC 3339": func(c *entitlements.OrgLLMConfig) { c.Version = "yesterday" },
		"no key and no base URL":         func(c *entitlements.OrgLLMConfig) { c.APIKey = "" },
	} {
		bad := *source
		mutate(&bad.config)
		if _, ok, err := (opsOrgModelConfigs{source: &bad}).ResolveOrgModelConfig(context.Background(), "org"); ok || !errors.Is(err, entitlements.ErrLLMConfigUnusable) {
			t.Errorf("%s: ok = %v err = %v, want unusable", name, ok, err)
		}
	}
	none := &fakeOrgLLMSource{}
	if _, ok, err := (opsOrgModelConfigs{source: none}).ResolveOrgModelConfig(context.Background(), "org"); ok || err != nil {
		t.Fatalf("no BYO: ok = %v err = %v, want ok=false and no error", ok, err)
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
	resolver := modelruntimeresolver.New(platform, opsOrgModelConfigs{source: unusable}, build)
	_, _, err := resolver.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_a"}, contextfabric.InvestigationRequest{})
	if !errors.Is(err, contextfabric.ErrModelUnavailable) || platform.calls != 0 || built != 0 {
		t.Fatalf("err = %v platform calls = %d built = %d, want unavailable with no platform call", err, platform.calls, built)
	}
	none := &fakeOrgLLMSource{}
	resolver = modelruntimeresolver.New(platform, opsOrgModelConfigs{source: none}, build)
	if _, _, err := resolver.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_b"}, contextfabric.InvestigationRequest{}); err != nil || platform.calls != 1 {
		t.Fatalf("no BYO: err = %v platform calls = %d, want the platform model", err, platform.calls)
	}
}
