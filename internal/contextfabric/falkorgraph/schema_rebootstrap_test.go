package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type rebootstrapSink struct {
	NoopTelemetry
	attempts []int
}

func (s *rebootstrapSink) RecordGraphSchemaRebootstrap(_ context.Context, _ string, attempt int) {
	s.attempts = append(s.attempts, attempt)
}

// purgeRig is a graph whose schema exists only after a CREATE VECTOR INDEX and
// can be purged "from another process" at any time.
type purgeRig struct {
	schemaPresent bool
	createCalls   int
	embeddings    int
	watermarks    int
	createFixes   bool
	// vanishOnWrite drops the schema at the first graph write after a
	// bootstrap, as something that keeps purging the key would.
	vanishOnWrite bool
}

func newPurgeRig(createFixes bool) *purgeRig { return &purgeRig{createFixes: createFixes} }

func (r *purgeRig) conn() *fakeConn {
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, _ map[string]interface{}, readOnly bool) ([]row, error) {
		if r.vanishOnWrite && !readOnly && !strings.Contains(cypher, "CREATE VECTOR INDEX") {
			r.schemaPresent = false
		}
		switch {
		case strings.Contains(cypher, "CREATE VECTOR INDEX"):
			r.createCalls++
			if r.createFixes {
				r.schemaPresent = true
			}
		case strings.Contains(cypher, labelWatermark):
			r.watermarks++
		case strings.Contains(cypher, "vecf32($vec)"):
			r.embeddings++
		}
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		if r.schemaPresent {
			return []indexStatus{operationalVectorIndex(8)}, nil
		}
		return nil, nil
	}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	return fake
}

func (r *purgeRig) purge() { r.schemaPresent = false }

func bootstrapCached(a *Adapter, key string) bool {
	a.bootstrapMu.RLock()
	defer a.bootstrapMu.RUnlock()
	return a.bootstrapDone[key]
}

func writeKeyOf(t *testing.T, a *Adapter) string {
	t.Helper()
	key, err := a.resolveWriteKey(context.Background(), "org")
	if err != nil {
		t.Fatalf("resolveWriteKey: %v", err)
	}
	return key
}

func apply(t *testing.T, a *Adapter) error {
	t.Helper()
	_, err := a.ApplyProjectionBatch(context.Background(), vectorProbeBatch("org"))
	return err
}

// The wedge: bootstrap, a purge by another process, then every tick failed
// with errVectorIndexNotReady until a restart.
func TestAPurgeFromAnotherProcessIsHealedByTheNextTick(t *testing.T) {
	rig := newPurgeRig(true)
	sink := &rebootstrapSink{}
	adapter := vectorAdapterWithTelemetry(t, rig.conn(), &stubEmbedder{vector: make([]float32, 8)}, sink)
	key := writeKeyOf(t, adapter)

	if err := apply(t, adapter); err != nil {
		t.Fatalf("first batch (bootstraps the schema): %v", err)
	}
	if !bootstrapCached(adapter, key) || rig.embeddings != 1 {
		t.Fatalf("premise: bootstrap cached = %v, embeddings = %d, want true/1", bootstrapCached(adapter, key), rig.embeddings)
	}
	watermarksBefore := rig.watermarks

	rig.purge()
	if err := apply(t, adapter); !errors.Is(err, errVectorIndexNotReady) {
		t.Fatalf("the batch that finds the index gone = %v, want errVectorIndexNotReady (checkpoint held)", err)
	}
	if bootstrapCached(adapter, key) {
		t.Fatal("bootstrapDone must be dropped when the vector index is absent after bootstrap")
	}
	if rig.watermarks != watermarksBefore {
		t.Fatalf("watermark writes %d -> %d: the batch that finds the index gone must not advance the checkpoint", watermarksBefore, rig.watermarks)
	}
	if len(sink.attempts) != 1 || sink.attempts[0] != 1 {
		t.Fatalf("rebootstrap telemetry = %v, want one event with attempt 1", sink.attempts)
	}

	if err := apply(t, adapter); err != nil {
		t.Fatalf("the next tick must bootstrap again and land the batch: %v", err)
	}
	if rig.watermarks != watermarksBefore+1 {
		t.Fatalf("watermark writes = %d, want %d: the healed batch advances the checkpoint once", rig.watermarks, watermarksBefore+1)
	}
	if !rig.schemaPresent || rig.embeddings != 2 || !bootstrapCached(adapter, key) {
		t.Fatalf("schema/embeddings/cached = %v/%d/%v, want true/2/true", rig.schemaPresent, rig.embeddings, bootstrapCached(adapter, key))
	}
}

// A graph that keeps losing its schema right after each bootstrap must not
// turn every tick into a bootstrap: one re-bootstrap per backoff window,
// doubling.
func TestARepeatedAbsentIndexBacksOffInsteadOfLooping(t *testing.T) {
	rig := newPurgeRig(true)
	rig.vanishOnWrite = true
	sink := &rebootstrapSink{}
	adapter := vectorAdapterWithTelemetry(t, rig.conn(), &stubEmbedder{vector: make([]float32, 8)}, sink)
	clock := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	adapter.now = func() time.Time { return clock }

	tick := func(advance time.Duration) {
		clock = clock.Add(advance)
		if err := apply(t, adapter); err == nil {
			t.Fatal("a graph that loses its schema on every write must keep failing the batch")
		}
	}
	tick(0)
	if len(sink.attempts) != 1 || rig.createCalls != 1 {
		t.Fatalf("first tick: attempts/bootstraps = %v/%d, want [1]/1", sink.attempts, rig.createCalls)
	}
	for i := 0; i < 6; i++ {
		tick(time.Second)
	}
	if len(sink.attempts) != 1 || rig.createCalls != 2 {
		t.Fatalf("ticks inside the backoff window: attempts/bootstraps = %v/%d, want [1]/2: one bootstrap after the drop, then none", sink.attempts, rig.createCalls)
	}
	tick(schemaRebootstrapBackoffBase)
	tick(time.Second)
	if len(sink.attempts) != 2 || sink.attempts[1] != 2 || rig.createCalls != 3 {
		t.Fatalf("after the first window: attempts/bootstraps = %v/%d, want [1 2]/3", sink.attempts, rig.createCalls)
	}
	tick(schemaRebootstrapBackoffBase)
	tick(time.Second)
	if len(sink.attempts) != 2 {
		t.Fatalf("the second window doubles: attempts = %v, want still [1 2]", sink.attempts)
	}
	tick(schemaRebootstrapBackoffBase)
	tick(time.Second)
	if len(sink.attempts) != 3 || rig.createCalls != 4 {
		t.Fatalf("after the doubled window: attempts/bootstraps = %v/%d, want 3 events/4 bootstraps", sink.attempts, rig.createCalls)
	}
}

// An index that is still building is not a vanished one.
func TestAnIndexUnderConstructionDoesNotDropTheBootstrapCache(t *testing.T) {
	sink := &rebootstrapSink{}
	fake := &fakeConn{}
	fake.constraintsFunc = func(context.Context, string) ([]constraintStatus, error) {
		return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
	}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{{
			Label: labelSubject, EntityType: "NODE", Status: "UNDER CONSTRUCTION",
			Types:   map[string][]string{propEmbedding: {"VECTOR"}},
			Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(8)}},
		}}, nil
	}
	adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 8)}, sink)
	adapter.config.RequestTimeout = 20 * time.Millisecond
	key := writeKeyOf(t, adapter)
	adapter.bootstrapMu.Lock()
	adapter.bootstrapDone[key] = true
	adapter.bootstrapMu.Unlock()

	if err := apply(t, adapter); !errors.Is(err, errVectorIndexNotReady) {
		t.Fatalf("batch = %v, want errVectorIndexNotReady", err)
	}
	if !bootstrapCached(adapter, key) || len(sink.attempts) != 0 {
		t.Fatalf("cached = %v, attempts = %v, want true and none: only an ABSENT index drops the cache", bootstrapCached(adapter, key), sink.attempts)
	}
}

// Finding the index present clears the throttle, so a later purge heals at
// once instead of waiting out an old window.
func TestFindingTheIndexResetsTheRebootstrapThrottle(t *testing.T) {
	rig := newPurgeRig(true)
	sink := &rebootstrapSink{}
	adapter := vectorAdapterWithTelemetry(t, rig.conn(), &stubEmbedder{vector: make([]float32, 8)}, sink)
	clock := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	adapter.now = func() time.Time { return clock }

	for round := 1; round <= 2; round++ {
		if err := apply(t, adapter); err != nil {
			t.Fatalf("round %d: batch: %v", round, err)
		}
		rig.purge()
		if err := apply(t, adapter); !errors.Is(err, errVectorIndexNotReady) {
			t.Fatalf("round %d: purged batch = %v, want errVectorIndexNotReady", round, err)
		}
		if err := apply(t, adapter); err != nil {
			t.Fatalf("round %d: healing batch: %v", round, err)
		}
		clock = clock.Add(time.Second)
	}
	if len(sink.attempts) != 2 || sink.attempts[0] != 1 || sink.attempts[1] != 1 {
		t.Fatalf("attempts = %v, want [1 1]: a healed schema resets the counter and the wait", sink.attempts)
	}
}

// Telemetry without the optional method, or none at all, still heals.
func TestRebootstrapNeedsNoOptionalTelemetry(t *testing.T) {
	for name, telemetry := range map[string]GraphTelemetry{"noop": NoopTelemetry{}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			rig := newPurgeRig(true)
			adapter := vectorAdapterWithTelemetry(t, rig.conn(), &stubEmbedder{vector: make([]float32, 8)}, telemetry)
			if err := apply(t, adapter); err != nil {
				t.Fatalf("first batch: %v", err)
			}
			rig.purge()
			_ = apply(t, adapter)
			if err := apply(t, adapter); err != nil {
				t.Fatalf("healing batch with telemetry %s: %v", name, err)
			}
		})
	}
}
