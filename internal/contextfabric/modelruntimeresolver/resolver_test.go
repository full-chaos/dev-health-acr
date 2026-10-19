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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgmodelconfigfake"
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

// TestRuntimeFor_usesDeploymentDefault_whenOrgHasNoConfiguration is
// AC-3775-3's first half.
func TestRuntimeFor_usesDeploymentDefault_whenOrgHasNoConfiguration(t *testing.T) {
	configs := orgmodelconfigfake.NewStore(nil)
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
	configs := orgmodelconfigfake.NewStore(nil)
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
	configs := orgmodelconfigfake.NewStore(nil)
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
	configs := orgmodelconfigfake.NewStore(func() time.Time {
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
	configs := orgmodelconfigfake.NewStore(nil)
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
	configs := orgmodelconfigfake.NewStore(nil)
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
// is the Codex round-1 F3 probe: orgmodelconfigfake's Store is given a `now`
// function that always returns the IDENTICAL timestamp, reproducing exactly
// what a wall-clock cache key would collide on (two upserts in the same
// tick, or a clock stepping backward). The fix -- Generation, a
// monotonically-incrementing counter independent of the clock -- must
// still detect the second write as a change and rebuild.
func TestRuntimeFor_invalidatesCache_evenWhenBothWritesLandInTheSameClockTick(t *testing.T) {
	frozen := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	configs := orgmodelconfigfake.NewStore(func() time.Time { return frozen })
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

// TestRuntimeFor_coalescesConcurrentColdRequestsIntoOneBuild is the Codex
// round-1 F6 probe: N concurrent first requests for the SAME newly
// configured (cold-cache) organization must trigger Build exactly once, not
// once per goroutine -- the thundering-herd this singleflight coalescing
// exists to prevent. buildStarted/buildRelease gate Build so every
// goroutine's request is guaranteed to observe a cold cache before any of
// them completes.
func TestRuntimeFor_coalescesConcurrentColdRequestsIntoOneBuild(t *testing.T) {
	configs := orgmodelconfigfake.NewStore(nil)
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
	configs := orgmodelconfigfake.NewStore(nil)
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

// TestRuntimeFor_newerGenerationBuildIsNeverClobberedByAnOlderInFlightBuild
// is the "benign sibling" Codex round-2 noted alongside M2: two builds for
// different generations of the SAME organization completing out of order
// (an UPDATE lands while an older build is still in flight) must never let
// the older, now-superseded build overwrite the newer one's already-cached
// result. This is the generation-monotonic write guard's job: a write is
// refused whenever the entry already cached for this organization has a
// Generation newer than the one this build was for, regardless of
// completion order.
func TestRuntimeFor_newerGenerationBuildIsNeverClobberedByAnOlderInFlightBuild(t *testing.T) {
	configs := orgmodelconfigfake.NewStore(nil)
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

	// A third request: the answer must still be model-a-v2, and the build
	// COUNT is what discriminates the guard: without it the stale build's
	// write clobbers the cache with generation 1, which mismatches the
	// current generation 2 and forces an avoidable third build. With it the
	// generation-2 entry survives and this request is a cache hit.
	_, _, err = resolver.InterpretQuestion(ctx, storage.Principal{OrgID: "org-a"}, contextfabric.InvestigationRequest{})
	if runtimeName(err) != "fake runtime model-a-v2 answered" {
		t.Fatalf("after the stale build finished, answer = %v, want model-a-v2 (must not be clobbered)", err)
	}
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("Build called %d times, want exactly 2 -- the generation guard should have discarded the stale write and avoided a third, wasted rebuild", got)
	}
}
