package modelruntimeresolver_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memorymodelconfig"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fakeRuntime is a named, distinguishable contextfabric.ModelRuntime so a
// test can assert exactly which runtime answered a call.
type fakeRuntime struct {
	name string
}

func (f *fakeRuntime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("fake runtime %s answered", f.name)
}

func (f *fakeRuntime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("fake runtime %s answered", f.name)
}

func writeRequest(model string) contractsv1.ContextFabricOrgModelConfigWriteRequest {
	return contractsv1.ContextFabricOrgModelConfigWriteRequest{
		SchemaVersion: contractsv1.ContextFabricOrgModelConfigWriteRequestSchema,
		Provider:      "acme-gateway",
		Model:         model,
		Credential:    "sk-acme-live-a1b2c3d4e5f6wxyz",
	}
}

func runtimeName(err error) string {
	// fakeRuntime methods always error with their own name embedded, so a
	// test can identify which runtime answered without a type assertion
	// through the interface indirection Resolver introduces.
	return err.Error()
}

// gatedConfigResolver wraps a real contextfabric.OrgModelConfigResolver but
// blocks the FIRST call to ResolveOrgModelConfig until release is closed,
// signaling resolveStarted first -- letting a test control exactly when
// "resolve is in flight" ends relative to some other event (an eviction),
// which is the precise window Codex round 3 found unfenced: the ticket must
// be claimed before this call, not after it returns. Every call after the
// first passes through immediately, since release is already closed by
// then (a closed channel always yields its zero value on receive without
// blocking).
type gatedConfigResolver struct {
	inner          contextfabric.OrgModelConfigResolver
	resolveStarted chan struct{}
	release        chan struct{}
	once           sync.Once
}

func (g *gatedConfigResolver) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	g.once.Do(func() { close(g.resolveStarted) })
	<-g.release
	return g.inner.ResolveOrgModelConfig(ctx, orgID)
}

// TestRuntimeFor_usesDeploymentDefault_whenOrgHasNoConfiguration is
// AC-3775-3's first half.
func TestRuntimeFor_usesDeploymentDefault_whenOrgHasNoConfiguration(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	deploymentDefault := &fakeRuntime{name: "default"}
	resolver := modelruntimeresolver.New(deploymentDefault, configs, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		t.Fatal("Build should not be called for an unconfigured organization")
		return nil, nil
	})
	_, _, err := resolver.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org-unconfigured"}, contextfabric.InvestigationRequest{})
	if err == nil || runtimeName(err) != "fake runtime default answered" {
		t.Fatalf("err = %v, want the deployment default to have answered", err)
	}
}

// TestRuntimeFor_usesOrgRuntime_whenOrgIsConfigured is AC-3775-1's core
// proof: two organizations with different configurations get different
// runtimes.
func TestRuntimeFor_usesOrgRuntime_whenOrgIsConfigured(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig org-a: %v", err)
	}
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-b"}, writeRequest("model-b")); err != nil {
		t.Fatalf("UpsertOrgModelConfig org-b: %v", err)
	}
	deploymentDefault := &fakeRuntime{name: "default"}
	resolver := modelruntimeresolver.New(deploymentDefault, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return &fakeRuntime{name: resolved.Model}, nil
	})
	_, _, errA := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	_, _, errB := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-b"}, contextfabric.InvestigationRequest{})
	if runtimeName(errA) != "fake runtime model-a answered" {
		t.Fatalf("org-a answered by %v, want model-a", errA)
	}
	if runtimeName(errB) != "fake runtime model-b answered" {
		t.Fatalf("org-b answered by %v, want model-b", errB)
	}
}

// TestRuntimeFor_cachesConstructedRuntime_acrossRequests is AC-3775-2/
// AC-3775-5's caching half: repeated requests for an unchanged
// configuration must not pay construction cost again.
func TestRuntimeFor_cachesConstructedRuntime_acrossRequests(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	var builds int32
	resolver := modelruntimeresolver.New(nil, configs, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		atomic.AddInt32(&builds, 1)
		return &fakeRuntime{name: "org-a-runtime"}, nil
	})
	for range 5 {
		if _, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{}); runtimeName(err) != "fake runtime org-a-runtime answered" {
			t.Fatalf("unexpected answer: %v", err)
		}
	}
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("Build called %d times, want exactly 1", got)
	}
}

// TestRuntimeFor_invalidatesCache_whenConfigurationChanges is AC-3775-5:
// a configuration change takes effect on the very next request, with no
// restart.
func TestRuntimeFor_invalidatesCache_whenConfigurationChanges(t *testing.T) {
	tick := 0
	configs := memorymodelconfig.NewStore(func() time.Time {
		tick++
		return time.Date(2026, 8, 13, 9, tick, 0, 0, time.UTC)
	})
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return &fakeRuntime{name: resolved.Model}, nil
	})
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("first answer = %v, want model-a", err)
	}
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a-v2")); err != nil {
		t.Fatalf("UpsertOrgModelConfig update: %v", err)
	}
	_, _, err = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a-v2 answered" {
		t.Fatalf("second answer = %v, want model-a-v2 (cache should have invalidated)", err)
	}
}

// TestRuntimeFor_brokenCredential_neverFallsBackToDeploymentDefault is
// AC-3775-2 and AC-3775-3's explicit no-silent-fallback prohibition: a
// resolve/build failure for a configured organization must surface as
// ErrModelUnavailable, and must never route to Default.
func TestRuntimeFor_brokenCredential_neverFallsBackToDeploymentDefault(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-broken"}, writeRequest("model-broken")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	deploymentDefault := &fakeRuntime{name: "default"}
	buildErr := errors.New("provider rejected the credential: 401")
	resolver := modelruntimeresolver.New(deploymentDefault, configs, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return nil, buildErr
	})
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-broken"}, contextfabric.InvestigationRequest{})
	if !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("err = %v, want ErrModelUnavailable", err)
	}
	if runtimeName(err) == "fake runtime default answered" {
		t.Fatal("a broken org credential silently fell back to the deployment default")
	}
}

// TestRuntimeFor_orgIsolation_oneOrgsFailureNeverAffectsAnother is
// AC-3775-2's headline claim, exercised end to end through the resolver:
// org-broken's construction failure must not prevent org-healthy from
// answering, before or after org-broken is resolved.
func TestRuntimeFor_orgIsolation_oneOrgsFailureNeverAffectsAnother(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-broken"}, writeRequest("model-broken")); err != nil {
		t.Fatalf("UpsertOrgModelConfig org-broken: %v", err)
	}
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-healthy"}, writeRequest("model-healthy")); err != nil {
		t.Fatalf("UpsertOrgModelConfig org-healthy: %v", err)
	}
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		if resolved.Model == "model-broken" {
			return nil, errors.New("provider unavailable")
		}
		return &fakeRuntime{name: resolved.Model}, nil
	})
	if _, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-broken"}, contextfabric.InvestigationRequest{}); !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("org-broken err = %v, want ErrModelUnavailable", err)
	}
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-healthy"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-healthy answered" {
		t.Fatalf("org-healthy err = %v, want model-healthy to have answered", err)
	}
}

// TestRuntimeFor_returnsErrModelUnavailable_whenNoRuntimeAtAll matches the
// pre-CHAOS-3775 nil-runtime contract: no organization, no per-org
// support, no deployment default -> ErrModelUnavailable, same as a nil
// ModelRuntime always has.
func TestRuntimeFor_returnsErrModelUnavailable_whenNoRuntimeAtAll(t *testing.T) {
	resolver := modelruntimeresolver.New(nil, nil, nil)
	_, _, err := resolver.InterpretQuestion(context.Background(), storage.Principal{}, contextfabric.InvestigationRequest{})
	if !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("err = %v, want ErrModelUnavailable", err)
	}
}

// TestRuntimeFor_invalidatesCache_evenWhenBothWritesLandInTheSameClockTick
// is the Codex round-1 F3 probe: memorymodelconfig's Store is given a `now`
// function that always returns the IDENTICAL timestamp, reproducing exactly
// what a wall-clock cache key would collide on (two upserts in the same
// tick, or a clock stepping backward). The fix -- Generation, a
// monotonically-incrementing counter independent of the clock -- must
// still detect the second write as a change and rebuild.
func TestRuntimeFor_invalidatesCache_evenWhenBothWritesLandInTheSameClockTick(t *testing.T) {
	frozen := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	configs := memorymodelconfig.NewStore(func() time.Time { return frozen })
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return &fakeRuntime{name: resolved.Model}, nil
	})
	if _, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{}); runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("first answer = %v, want model-a", err)
	}
	// Second write, IDENTICAL timestamp (frozen clock) -- a timestamp-keyed
	// cache would see entry.updatedAt.Equal(resolved.UpdatedAt) == true and
	// incorrectly keep serving model-a.
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a-v2")); err != nil {
		t.Fatalf("UpsertOrgModelConfig update: %v", err)
	}
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a-v2 answered" {
		t.Fatalf("second answer = %v, want model-a-v2 (same-tick write must still invalidate the cache)", err)
	}
}

// TestEvictOrgModelRuntime_removesCachedEntry is the Codex round-1 F4
// probe's cache-level half: after eviction, the org has no cache entry at
// all -- proven indirectly by observing that resolution runs Build again
// (a cache hit would never call Build a second time for an unchanged
// configuration).
func TestEvictOrgModelRuntime_removesCachedEntry(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	var builds int32
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		atomic.AddInt32(&builds, 1)
		return &fakeRuntime{name: resolved.Model}, nil
	})
	if _, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{}); runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("first InterpretQuestion: %v", err)
	}
	if _, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{}); runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("second InterpretQuestion: %v", err)
	}
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("Build called %d times before eviction, want exactly 1 (cache should have hit)", got)
	}

	resolver.EvictOrgModelRuntime("org-a")

	if _, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{}); runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("third InterpretQuestion: %v", err)
	}
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("Build called %d times after eviction, want exactly 2 (eviction should have forced a rebuild)", got)
	}
}

// TestEvictOrgModelRuntime_deleteThenRecreateWithoutAnIdenticalGenerationLeak
// is the Codex round-1 F4 probe's full end-to-end shape: delete an
// organization's configuration, evict, then recreate it with a DIFFERENT
// credential/runtime. The resolver must serve the NEW runtime, never a
// resurrected stale one -- proving eviction plus the Generation fix
// together close the "decrypted credential stays resident" and
// "resurrected after re-add" risks F4 raised.
func TestEvictOrgModelRuntime_deleteThenRecreateNeverResurrectsTheOldRuntime(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a-old")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return &fakeRuntime{name: resolved.Model}, nil
	})
	if _, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{}); runtimeName(err) != "fake runtime model-a-old answered" {
		t.Fatalf("first answer = %v, want model-a-old", err)
	}

	if err := configs.DeleteOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}); err != nil {
		t.Fatalf("DeleteOrgModelConfig: %v", err)
	}
	resolver.EvictOrgModelRuntime("org-a")

	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a-new")); err != nil {
		t.Fatalf("re-create UpsertOrgModelConfig: %v", err)
	}
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a-new answered" {
		t.Fatalf("answer after recreate = %v, want model-a-new, never the resurrected model-a-old", err)
	}
}

// TestRuntimeFor_coalescesConcurrentColdRequestsIntoOneBuild is the Codex
// round-1 F6 probe: N concurrent first requests for the SAME newly
// configured (cold-cache) organization must trigger Build exactly once, not
// once per goroutine -- the thundering-herd this singleflight coalescing
// exists to prevent. buildStarted/buildRelease gate Build so every
// goroutine's request is guaranteed to observe a cold cache before any of
// them completes.
func TestRuntimeFor_coalescesConcurrentColdRequestsIntoOneBuild(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	var builds int32
	buildStarted := make(chan struct{})
	release := make(chan struct{})
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		if atomic.AddInt32(&builds, 1) == 1 {
			close(buildStarted)
		}
		<-release
		return &fakeRuntime{name: resolved.Model}, nil
	})

	const concurrency = 10
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			_, _, _ = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
		}()
	}
	<-buildStarted
	// Give every goroutine a chance to reach and block inside Build (or
	// singleflight's wait path) before releasing, so a real implementation
	// bug (no coalescing) has every opportunity to show up as builds > 1.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("Build called %d times for %d concurrent cold requests, want exactly 1", got, concurrency)
	}
}

// TestRuntimeFor_doesNotCacheACanceledBuild is the Codex round-2 finding M1
// probe: a build that fails because the CALLER's context was canceled must
// never be cached as a negative entry -- doing so would poison every later
// request at this generation with a cancellation error that has nothing to
// do with whether the organization's provider actually works, and could
// otherwise make the route return an empty response for callers who never
// canceled anything themselves.
func TestRuntimeFor_doesNotCacheACanceledBuild(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	var builds int32
	buildStarted := make(chan struct{})
	resolver := modelruntimeresolver.New(nil, configs, func(buildCtx context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		if atomic.AddInt32(&builds, 1) == 1 {
			close(buildStarted)
			<-buildCtx.Done()
			return nil, buildCtx.Err()
		}
		return &fakeRuntime{name: resolved.Model}, nil
	})

	cancelCtx, cancel := context.WithCancel(ctx)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _, _ = resolver.InterpretQuestion(cancelCtx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	}()
	<-buildStarted
	cancel()
	<-firstDone

	// A fresh request, fresh (non-canceled) context -- must trigger a real
	// second build, not serve a cached cancellation.
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("second request = %v, want a fresh successful build, not a cached cancellation", err)
	}
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("Build called %d times, want exactly 2 (a canceled build must never be cached)", got)
	}
}

// TestEvictOrgModelRuntime_fencesAnInFlightBuildFromResurrectingAfterEviction
// is the Codex round-2 finding M2 probe: a build already in flight when
// EvictOrgModelRuntime runs for the same organization must not be allowed
// to write its result afterward -- mirroring the real DELETE handler's
// sequence (delete the row, then evict), a build gated mid-flight must
// never resurrect a runtime for a configuration that no longer exists.
func TestEvictOrgModelRuntime_fencesAnInFlightBuildFromResurrectingAfterEviction(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	buildStarted := make(chan struct{})
	release := make(chan struct{})
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		close(buildStarted)
		<-release
		return &fakeRuntime{name: resolved.Model}, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	}()
	<-buildStarted

	// Mirrors ContextFabricOrgModelConfigDeleteHandler: delete the row,
	// then evict the cache, WHILE the gated build above is still running.
	if err := configs.DeleteOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}); err != nil {
		t.Fatalf("DeleteOrgModelConfig: %v", err)
	}
	resolver.EvictOrgModelRuntime("org-a")

	close(release)
	<-done

	// The raced build must not have resurrected anything: the org has no
	// configuration anymore, so the next request must fall straight
	// through to Default (nil in this test) -- ErrModelUnavailable, never
	// the raced build's runtime answering.
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if !errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatalf("err = %v, want ErrModelUnavailable -- a build racing eviction must never resurrect a runtime", err)
	}
}

// TestEvictOrgModelRuntime_fenceIsObservable_whenTheConfigurationRowSurvivesEviction
// isolates the M2 fence from the delete-based test above, which (by
// design, correctly) can't actually tell fenced from unfenced: once the
// row is gone, ResolveOrgModelConfig's ok=false short-circuits BEFORE the
// cache is ever consulted, so that test would pass even without the fence.
// This test evicts WITHOUT deleting the row, so the configuration is
// unchanged (same generation) when the raced build finishes -- if the
// fence did not exist, the raced build's write would produce a cache HIT
// on the next request (same generation, silently served, no rebuild); with
// the fence, the write is discarded, so the next request is a genuine
// cache MISS and Build runs again. The build count is what makes the two
// outcomes observably different.
func TestEvictOrgModelRuntime_fenceIsObservable_whenTheConfigurationRowSurvivesEviction(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	var builds int32
	buildStarted := make(chan struct{})
	release := make(chan struct{})
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		if atomic.AddInt32(&builds, 1) == 1 {
			close(buildStarted)
			<-release
		}
		return &fakeRuntime{name: resolved.Model}, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	}()
	<-buildStarted

	// Evict alone -- the configuration row is untouched, still the same
	// generation the gated build is building for.
	resolver.EvictOrgModelRuntime("org-a")

	close(release)
	<-done

	// A fresh request: the configuration is unchanged (ok=true, same
	// generation), so this DOES consult the cache. If the raced build's
	// write had been allowed through, this would be a cache hit (builds
	// stays at 1); the fence must force a second build instead.
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("post-eviction answer = %v, want model-a", err)
	}
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("Build called %d times, want exactly 2 -- the raced build's write must have been discarded by the eviction fence, forcing a fresh build", got)
	}
}

// TestRuntimeFor_newerGenerationBuildIsNeverClobberedByAnOlderInFlightBuild
// is the "benign sibling" Codex round-2 noted alongside M2: two builds for
// different generations of the SAME organization completing out of order
// (an UPDATE lands while an older build is still in flight) must never let
// the older, now-superseded build overwrite the newer one's already-cached
// result. This is the generation-monotonic write guard's job, not epoch's
// (see runtimeFor's package-doc comment, guard #2): epoch is bumped ONLY by
// eviction as of the round-3 fix, so it plays no part here -- a write is
// refused whenever the entry already cached for this organization has a
// Generation newer than the one this build was for, regardless of
// completion order.
func TestRuntimeFor_newerGenerationBuildIsNeverClobberedByAnOlderInFlightBuild(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	var builds int32
	firstBuildStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstBuildGated int32
	resolver := modelruntimeresolver.New(nil, configs, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		atomic.AddInt32(&builds, 1)
		if resolved.Model == "model-a" && atomic.CompareAndSwapInt32(&firstBuildGated, 0, 1) {
			close(firstBuildStarted)
			<-releaseFirst
		}
		return &fakeRuntime{name: resolved.Model}, nil
	})

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _, _ = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	}()
	<-firstBuildStarted

	// Configuration changes to a new generation while the old build for
	// the OLD generation is still blocked.
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a-v2")); err != nil {
		t.Fatalf("UpsertOrgModelConfig update: %v", err)
	}

	// A request for the NEW generation runs its own build to completion
	// (unblocked -- only the "model-a" build gates on releaseFirst) and
	// caches it. This is build #2.
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a-v2 answered" {
		t.Fatalf("new-generation answer = %v, want model-a-v2", err)
	}

	// NOW let the stale old-generation build finish; its attempt to write
	// must be discarded.
	close(releaseFirst)
	<-firstDone

	// A third request: the correctness half (answer must still be
	// model-a-v2) is guaranteed by the generation mismatch alone, with or
	// without the epoch fence -- see the fence-isolating test above for why
	// that alone can't discriminate a working fence from a missing one.
	// The build COUNT is what does: without the fence, the stale build's
	// unconditional write clobbers the cache with generation 1, which
	// mismatches the still-current generation 2 and forces an AVOIDABLE
	// third build (the "benign sibling" Codex round-2 noted -- wasteful,
	// not wrong). With the fence, the stale write is discarded, the
	// generation-2 entry survives untouched, and this request is a cache
	// hit: build count stays at 2.
	_, _, err = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a-v2 answered" {
		t.Fatalf("after the stale build finished, answer = %v, want model-a-v2 (must not be clobbered)", err)
	}
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("Build called %d times, want exactly 2 -- the fence should have discarded the stale write and avoided a third, wasted rebuild", got)
	}
}

// TestRuntimeFor_evictionDuringResolveStillFencesTheLateWrite is the Codex
// round-3 probe: the exact interleaving round 3 found unfenced --
//
//	resolve starts (reads a pre-eviction ticket) -> [paused inside
//	ResolveOrgModelConfig] -> eviction bumps the epoch -> resolve completes,
//	returning the (now stale) pre-eviction configuration -> build runs and
//	succeeds -> the write must be REFUSED.
//
// A prior revision claimed the epoch ticket INSIDE the build closure,
// after resolve had already returned -- so this exact schedule was
// invisible to it: the closure's own ticket claim happened after the
// eviction, making the build look "fresh" regardless of how stale the
// configuration it was built from actually was. gatedConfigResolver
// controls the pause precisely inside ResolveOrgModelConfig, which a test
// gating only on Build (as every round-1/round-2 test here does) cannot
// reach -- by the time Build even starts, resolve has already returned and
// the (broken) prior design had already claimed its ticket too late.
func TestRuntimeFor_evictionDuringResolveStillFencesTheLateWrite(t *testing.T) {
	configs := memorymodelconfig.NewStore(nil)
	ctx := context.Background()
	if _, err := configs.UpsertOrgModelConfig(ctx, storage.Principal{OrgID: "org-a"}, writeRequest("model-a")); err != nil {
		t.Fatalf("UpsertOrgModelConfig: %v", err)
	}
	gated := &gatedConfigResolver{inner: configs, resolveStarted: make(chan struct{}), release: make(chan struct{})}
	var builds int32
	resolver := modelruntimeresolver.New(nil, gated, func(_ context.Context, resolved contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		atomic.AddInt32(&builds, 1)
		return &fakeRuntime{name: resolved.Model}, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	}()
	<-gated.resolveStarted // ticket has necessarily already been claimed -- it happens before this call.

	// Evict WHILE resolve is still blocked -- the row is left untouched
	// (see TestEvictOrgModelRuntime_fenceIsObservable_...'s comment for why
	// that matters: only an evict-without-delete leaves the read path
	// still consulting the cache afterward, which is what makes the race's
	// outcome observable at all).
	resolver.EvictOrgModelRuntime("org-a")

	close(gated.release) // resolve now completes, returning the pre-eviction configuration.
	<-done

	// The raced build (from stale, pre-eviction data) must not have been
	// written. Prove it the same way as the other fence-observability
	// test: a fresh request against the SAME (unchanged, un-deleted)
	// configuration must trigger a genuine second build, not a cache hit
	// serving the resurrected runtime.
	_, _, err := resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a answered" {
		t.Fatalf("post-eviction answer = %v, want model-a", err)
	}
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("Build called %d times, want exactly 2 -- a build racing eviction during resolve must never have its write survive", got)
	}
}
