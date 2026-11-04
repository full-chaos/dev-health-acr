package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"strings"
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

func TestBuildOrgModelConfigResolverIsOpsWhenTheEntitlementClientExists(t *testing.T) {
	source := &fakeOrgLLMSource{}
	withOps := entitlementWithLLMConfig{fakeOrgLLMSource: source}
	got := buildOrgModelConfigResolver(withOps, envLookup(nil), discardLogger())
	if _, ok := got.(opsOrgModelConfigs); !ok {
		t.Fatalf("with the Dev Health client the source must be ops, got %T", got)
	}
	if got := buildOrgModelConfigResolver(localEntitlement{}, envLookup(nil), discardLogger()); got != nil {
		t.Fatalf("without the Dev Health client: %T, want a nil interface", got)
	}
}

// CHAOS-12997: the setting is gone. A deployment that still sets it, to any
// value, gets the same source as one that does not, and is told once.
func TestAStaleOrgLLMConfigSourceSettingChangesNothing(t *testing.T) {
	withOps := entitlementWithLLMConfig{fakeOrgLLMSource: &fakeOrgLLMSource{}}
	for _, value := range []string{"off", "ops", "bogus", ""} {
		var logs strings.Builder
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		lookup := envLookup(map[string]string{removedEnvOrgLLMConfigSource: value})
		if got := buildOrgModelConfigResolver(withOps, lookup, logger); func() bool { _, ok := got.(opsOrgModelConfigs); return !ok }() {
			t.Fatalf("stale value %q built %T, want the ops source", value, buildOrgModelConfigResolver(withOps, lookup, logger))
		}
		if !strings.Contains(logs.String(), "removed and ignored") {
			t.Fatalf("stale value %q was not reported: %s", value, logs.String())
		}
		if got := buildOrgModelConfigResolver(localEntitlement{}, lookup, logger); got != nil {
			t.Fatalf("stale value %q with no Dev Health client built %T, want nil", value, got)
		}
	}
	var quiet strings.Builder
	buildOrgModelConfigResolver(withOps, envLookup(nil), slog.New(slog.NewTextHandler(&quiet, nil)))
	if strings.Contains(quiet.String(), "removed and ignored") {
		t.Fatalf("an unset setting was reported: %s", quiet.String())
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

func TestOpsOrgModelConfigsCarryThePromptCacheOptIn(t *testing.T) {
	for _, optedIn := range []bool{true, false} {
		source := &fakeOrgLLMSource{found: true, config: entitlements.OrgLLMConfig{
			Provider: "anthropic", Model: "claude-haiku-5-5", APIKey: llm.NewSecret("sk-ant-x"), PromptCache: optedIn, Version: "v1",
		}}
		resolved, ok, err := (opsOrgModelConfigs{source: source, generations: &versionGenerations{}}).ResolveOrgModelConfig(context.Background(), "org")
		if err != nil || !ok {
			t.Fatalf("ok = %v err = %v", ok, err)
		}
		if resolved.PromptCache != optedIn {
			t.Errorf("opted in = %v, resolved PromptCache = %v", optedIn, resolved.PromptCache)
		}
	}
}

func TestARefusedBaseURLIsLoggedByReasonOnly(t *testing.T) {
	for _, reason := range []string{"base_url_carries_key", "base_url_carries_userinfo"} {
		var logs bytes.Buffer
		source := &fakeOrgLLMSource{err: &entitlements.BaseURLRefusedError{Reason: reason}}
		configs := opsOrgModelConfigs{source: source, generations: &versionGenerations{}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
		_, ok, err := configs.ResolveOrgModelConfig(context.Background(), "org_a")
		if ok || !errors.Is(err, entitlements.ErrLLMConfigUnusable) {
			t.Fatalf("%s: ok = %v err = %v, want an unusable configuration", reason, ok, err)
		}
		var line map[string]any
		if json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &line) != nil {
			t.Fatalf("%s: want exactly one log line, got %q", reason, logs.String())
		}
		if line["msg"] != "context fabric org model config refused" || line["org_id"] != "org_a" || line["reason"] != reason || line["level"] != "WARN" {
			t.Fatalf("%s: log line = %v", reason, line)
		}
		for key := range line {
			if key != "time" && key != "level" && key != "msg" && key != "org_id" && key != "reason" {
				t.Fatalf("%s: unexpected log field %q", reason, key)
			}
		}
	}
	var logs bytes.Buffer
	other := &fakeOrgLLMSource{err: errors.Join(entitlements.ErrLLMConfigUnusable, errors.New("ops answered 409"))}
	configs := opsOrgModelConfigs{source: other, generations: &versionGenerations{}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	if _, _, err := configs.ResolveOrgModelConfig(context.Background(), "org_a"); !errors.Is(err, entitlements.ErrLLMConfigUnusable) || logs.Len() != 0 {
		t.Fatalf("a configuration that is not a refused base_url: err = %v log = %q, want the error and no log line", err, logs.String())
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

// CHAOS-12997: ACR_MCP_SERVICE_MODEL_DEFAULT is gone too. Any value is reported
// once and changes nothing: the platform model needs the two feature flags.
func TestAStaleServiceModelDefaultSettingIsReportedAndIgnored(t *testing.T) {
	withOps := entitlementWithLLMConfig{fakeOrgLLMSource: &fakeOrgLLMSource{}}
	for _, value := range []string{"on", "off", "bogus"} {
		var logs strings.Builder
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		got := buildOrgModelConfigResolver(withOps, envLookup(map[string]string{removedEnvServiceModelDefault: value}), logger)
		if _, ok := got.(opsOrgModelConfigs); !ok {
			t.Fatalf("stale value %q built %T, want the ops source", value, got)
		}
		if !strings.Contains(logs.String(), removedEnvServiceModelDefault) || !strings.Contains(logs.String(), "removed and ignored") {
			t.Fatalf("stale value %q was not reported: %s", value, logs.String())
		}
	}
}
