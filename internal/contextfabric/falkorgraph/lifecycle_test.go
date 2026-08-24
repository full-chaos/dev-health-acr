package falkorgraph

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// fakeEpochResolver is a minimal contextfabric.OrgEpochResolver test double:
// a fixed active epoch, optionally overridden per call via activeFunc/
// buildFunc for tests that need the resolved epoch to change between calls
// (simulating a live flip or a live config change).
type fakeEpochResolver struct {
	active     int64
	activeFunc func() int64
	buildEpoch int64
	buildOK    bool
}

func (f *fakeEpochResolver) ResolveActiveEpoch(context.Context, string) (int64, error) {
	if f.activeFunc != nil {
		return f.activeFunc(), nil
	}
	return f.active, nil
}

func (f *fakeEpochResolver) ResolveBuildEpoch(context.Context, string) (int64, bool, error) {
	return f.buildEpoch, f.buildOK, nil
}

// fakeLifecycleTelemetry records cf_resolved_graph_key/cf_graph_key_divergence
// calls only -- the two signals falkorgraph/lifecycle.go actually emits.
type fakeLifecycleTelemetry struct {
	mu          sync.Mutex
	stamps      []string // "orgID\x00epoch\x00role\x00key"
	divergences []string // "orgID\x00epoch\x00role"
}

func (f *fakeLifecycleTelemetry) RecordResolvedGraphKey(_ context.Context, orgID string, epoch int64, role contextfabric.GraphKeyRole, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stamps = append(f.stamps, orgID+"\x00"+strconv.FormatInt(epoch, 10)+"\x00"+string(role)+"\x00"+key)
}

func (f *fakeLifecycleTelemetry) RecordGraphKeyDivergence(_ context.Context, orgID string, epoch int64, role contextfabric.GraphKeyRole) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.divergences = append(f.divergences, orgID+"\x00"+strconv.FormatInt(epoch, 10)+"\x00"+string(role))
}

func (f *fakeLifecycleTelemetry) RecordStartupPrefixAssertion(context.Context, bool) {}
func (f *fakeLifecycleTelemetry) RecordEpochFlip(context.Context, string, int64, int64, time.Duration, int) {
}
func (f *fakeLifecycleTelemetry) RecordEpochRollback(context.Context, string, int64, int64, time.Duration) {
}
func (f *fakeLifecycleTelemetry) RecordEpochRetire(context.Context, string, int64, contextfabric.RetireGuardVerdict, time.Duration) {
}
func (f *fakeLifecycleTelemetry) RecordLifecycleCASConflict(context.Context, string, contextfabric.LifecycleTransition, contextfabric.LifecycleStatus) {
}
func (f *fakeLifecycleTelemetry) RecordCheckpointEpochState(context.Context, string, int64, contextfabric.CheckpointEpochState, time.Duration) {
}
func (f *fakeLifecycleTelemetry) RecordEpochResolverInvalidation(context.Context, string, contextfabric.LifecycleTransition) {
}

func (f *fakeLifecycleTelemetry) RecordBuildSourceProgress(context.Context, string, int64, string, contextfabric.BuildCompletionMode, int64) {
}

func (f *fakeLifecycleTelemetry) divergenceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.divergences)
}

var _ contextfabric.GraphLifecycleTelemetry = (*fakeLifecycleTelemetry)(nil)

func TestResolveReadKey_NilEpochResolverDegradesToEpochZero(t *testing.T) {
	telemetry := &fakeLifecycleTelemetry{}
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.config.LifecycleTelemetry = telemetry

	key, err := adapter.resolveReadKey(context.Background(), "org-1", contextfabric.GraphKeyRoleInvestigationRead)
	if err != nil {
		t.Fatalf("resolveReadKey() error = %v", err)
	}
	if key != graphKey(adapter.config.GraphPrefix, "org-1") {
		t.Fatalf("resolveReadKey() = %q, want the byte-identical pre-CHAOS-3898 legacy key", key)
	}
	if telemetry.divergenceCount() != 0 {
		t.Fatalf("a single resolution must never fire divergence")
	}
}

func TestResolveReadKey_NonZeroEpochAppendsSuffix(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.config.EpochResolver = &fakeEpochResolver{active: 3}

	key, err := adapter.resolveReadKey(context.Background(), "org-1", contextfabric.GraphKeyRoleInvestigationRead)
	if err != nil {
		t.Fatalf("resolveReadKey() error = %v", err)
	}
	want := graphKeyForEpoch(adapter.config.GraphPrefix, "org-1", 3)
	if key != want {
		t.Fatalf("resolveReadKey() = %q, want %q", key, want)
	}
}

func TestResolveWriteKey_ResolvesBuildEpochWhileOpenElseActive(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	resolver := &fakeEpochResolver{active: 1, buildEpoch: 2, buildOK: true}
	adapter.config.EpochResolver = resolver

	key, err := adapter.resolveWriteKey(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("resolveWriteKey() error = %v", err)
	}
	if want := graphKeyForEpoch(adapter.config.GraphPrefix, "org-1", 2); key != want {
		t.Fatalf("resolveWriteKey() with an open build = %q, want the TARGET epoch's key %q", key, want)
	}

	resolver.buildOK = false // build closed -- falls back to active
	key, err = adapter.resolveWriteKey(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("resolveWriteKey() error = %v", err)
	}
	if want := graphKeyForEpoch(adapter.config.GraphPrefix, "org-1", 1); key != want {
		t.Fatalf("resolveWriteKey() with no open build = %q, want the ACTIVE epoch's key %q", key, want)
	}
}

// TestStampResolvedKey_FiresDivergenceOnlyWhenTheSameKeyChangesUnderneath
// pins the in-process half of cf_graph_key_divergence (design brief §2.0,
// v4.1 F2): repeated resolutions for the SAME (org, epoch, role) that keep
// producing the SAME key never fire divergence; a resolution that changes
// mid-process (simulating a live GraphPrefix config edit) does.
func TestStampResolvedKey_FiresDivergenceOnlyWhenTheSameKeyChangesUnderneath(t *testing.T) {
	telemetry := &fakeLifecycleTelemetry{}
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.config.LifecycleTelemetry = telemetry
	adapter.config.EpochResolver = &fakeEpochResolver{active: 1}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := adapter.resolveReadKey(ctx, "org-1", contextfabric.GraphKeyRoleInvestigationRead); err != nil {
			t.Fatalf("resolveReadKey() error = %v", err)
		}
	}
	if got := telemetry.divergenceCount(); got != 0 {
		t.Fatalf("stable repeated resolutions fired %d divergences, want 0", got)
	}

	// Simulate the prefix changing live (a bad rolling deploy / reloaded env):
	// the SAME (org, epoch, role) now derives a DIFFERENT key.
	adapter.config.GraphPrefix = adapter.config.GraphPrefix + "-changed"
	if _, err := adapter.resolveReadKey(ctx, "org-1", contextfabric.GraphKeyRoleInvestigationRead); err != nil {
		t.Fatalf("resolveReadKey() error = %v", err)
	}
	if got := telemetry.divergenceCount(); got != 1 {
		t.Fatalf("divergenceCount() after the prefix changed = %d, want 1", got)
	}

	// A DIFFERENT epoch (a healthy build in progress) must NOT be treated
	// as divergence -- design brief v4.1 F2: "two keys for one org is the
	// NORMAL healthy-build shape". Divergence is keyed (org, epoch, role).
	if _, err := adapter.resolveReadKey(ctx, "org-1", contextfabric.GraphKeyRoleReuseRecheck); err != nil {
		t.Fatalf("resolveReadKey() error = %v", err)
	}
	if got := telemetry.divergenceCount(); got != 1 {
		t.Fatalf("a different ROLE must not itself fire divergence; divergenceCount() = %d, want 1", got)
	}
}
