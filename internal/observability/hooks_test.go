package observability

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type snapshotSink struct {
	mu        sync.Mutex
	snapshots []SupportSnapshot
}

func (s *snapshotSink) Record(snapshot SupportSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, snapshot)
}

func (s *snapshotSink) all() []SupportSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SupportSnapshot(nil), s.snapshots...)
}

func TestHooksEmitCorrelatedSafeSnapshots(t *testing.T) {
	t.Parallel()

	// Given: a deterministic request ID generator and a collector.
	sink := &snapshotSink{}
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x42}, 16))))

	// When: independently-owned request, store, ranking, evidence, and episode stages finish.
	ctx, requestID, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatalf("EnsureRequestID() error = %v", err)
	}
	hooks.ObserveRequest(ctx, RequestObservation{Outcome: OutcomeDenied, Duration: 12 * time.Millisecond, Denial: DenialRepositoryScope})
	hooks.ObserveStore(ctx, StoreObservation{Outcome: OutcomeSuccess, Duration: 3 * time.Millisecond, Packet: PacketObservation{Status: PacketStatusComplete, Bytes: 128, Tokens: 16}})
	hooks.ObserveRanking(ctx, RankingObservation{Outcome: OutcomeSuccess, Duration: 8 * time.Millisecond, QueryVersion: QueryVersionV1, RankingVersion: RankingVersionV1})
	hooks.ObserveEvidence(ctx, EvidenceObservation{Outcome: OutcomeSuccess, Duration: 2 * time.Millisecond, SourceFallback: SourceFallbackCatalog})
	hooks.ObserveEpisode(ctx, EpisodeObservation{Outcome: OutcomeCanceled, Duration: time.Millisecond})

	// Then: every snapshot has the request correlation and only bounded dimensions.
	snapshots := sink.all()
	if got, want := len(snapshots), 5; got != want {
		t.Fatalf("snapshot count = %d, want %d", got, want)
	}
	for _, snapshot := range snapshots {
		if snapshot.RequestID != requestID {
			t.Errorf("snapshot RequestID = %q, want %q", snapshot.RequestID, requestID)
		}
	}
	if got := snapshots[0]; got.Kind != KindRequest || got.Denial != DenialRepositoryScope || got.Outcome != OutcomeDenied {
		t.Errorf("request snapshot = %#v, want denied request with repository scope denial", got)
	}
	if got := snapshots[1]; got.PacketStatus != PacketStatusComplete || got.PacketBytes != 128 || got.PacketTokens != 16 {
		t.Errorf("store snapshot = %#v, want current packet dimensions", got)
	}
	if got := snapshots[2]; got.QueryVersion != QueryVersionV1 || got.RankingVersion != RankingVersionV1 {
		t.Errorf("ranking snapshot = %#v, want version dimensions", got)
	}
	if got := snapshots[3]; got.SourceFallback != SourceFallbackCatalog {
		t.Errorf("evidence snapshot = %#v, want catalog fallback", got)
	}
}

func TestHooksScrubUnknownHighCardinalityDimensions(t *testing.T) {
	t.Parallel()

	// Given: attacker-controlled strings masquerading as telemetry dimensions.
	const secret = "Bearer raw-license-and-evidence-secret"
	sink := &snapshotSink{}
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x11}, 16))))
	ctx, _, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatalf("EnsureRequestID() error = %v", err)
	}

	// When: an observation includes unsupported arbitrary values.
	hooks.ObserveRanking(ctx, RankingObservation{
		Outcome:        Outcome("debug-" + strings.Repeat("x", 80)),
		QueryVersion:   QueryVersion(secret),
		RankingVersion: RankingVersion("ranking-" + strings.Repeat("x", 80)),
	})

	// Then: support output contains only the bounded unknown values, never raw input.
	snapshots := sink.all()
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("snapshot count = %d, want %d", got, want)
	}
	snapshot := snapshots[0]
	if snapshot.Outcome != OutcomeUnknown || snapshot.QueryVersion != QueryVersionUnknown || snapshot.RankingVersion != RankingVersionUnknown {
		t.Errorf("snapshot = %#v, want unknown bounded dimensions", snapshot)
	}
	if serialized := fmt.Sprintf("%#v", snapshot); strings.Contains(serialized, secret) || strings.Contains(serialized, "debug-") {
		t.Errorf("safe snapshot leaked untrusted value: %q", serialized)
	}
}

func TestHooksReplaceUnsafeRequestID(t *testing.T) {
	t.Parallel()

	// Given: an inbound request ID that contains a bearer credential.
	const secret = "Bearer raw-license-and-evidence-secret"
	sink := &snapshotSink{}
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x51}, 16))))

	// When: the hook is given the untrusted correlation value.
	ctx := WithRequestID(context.Background(), secret)
	ctx, requestID, err := hooks.EnsureRequestID(ctx)
	if err != nil {
		t.Fatalf("EnsureRequestID() error = %v", err)
	}
	hooks.ObserveEpisode(ctx, EpisodeObservation{Outcome: OutcomeSuccess})

	// Then: the generated replacement correlates the event without leaking the input.
	snapshots := sink.all()
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("snapshot count = %d, want %d", got, want)
	}
	if got := snapshots[0].RequestID; got != requestID || strings.Contains(string(got), secret) {
		t.Errorf("snapshot RequestID = %q, want generated non-secret ID", got)
	}
}

func TestHooksPreserveCorrelationWhenContextCanceled(t *testing.T) {
	t.Parallel()

	// Given: a correlated request context that has been canceled.
	sink := &snapshotSink{}
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x23}, 16))))
	ctx, requestID, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatalf("EnsureRequestID() error = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	// When: completion is observed after the caller canceled.
	hooks.ObserveRequest(canceled, RequestObservation{Outcome: OutcomeCanceled})

	// Then: the terminal event is retained with its request ID.
	snapshots := sink.all()
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("snapshot count = %d, want %d", got, want)
	}
	if got := snapshots[0]; got.RequestID != requestID || got.Outcome != OutcomeCanceled {
		t.Errorf("snapshot = %#v, want correlated canceled outcome", got)
	}
}

func TestHooksKeepPartialStateWithoutNegativeSizes(t *testing.T) {
	t.Parallel()

	// Given: a stale packet whose malformed counters are negative.
	sink := &snapshotSink{}
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x36}, 16))))
	ctx, _, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatalf("EnsureRequestID() error = %v", err)
	}

	// When: the store completion is observed.
	hooks.ObserveStore(ctx, StoreObservation{Packet: PacketObservation{Status: PacketStatusPartial, Bytes: -1, Tokens: -1}})

	// Then: staleness remains visible while invalid numeric dimensions are bounded.
	snapshots := sink.all()
	if got, want := len(snapshots), 1; got != want {
		t.Fatalf("snapshot count = %d, want %d", got, want)
	}
	if got := snapshots[0]; got.PacketStatus != PacketStatusPartial || got.PacketBytes != 0 || got.PacketTokens != 0 {
		t.Errorf("snapshot = %#v, want partial status and zeroed counters", got)
	}
}

func TestHooksUseSemanticDefaults(t *testing.T) {
	t.Parallel()

	sink := &snapshotSink{}
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x64}, 16))))
	ctx, _, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatalf("EnsureRequestID() error = %v", err)
	}
	hooks.ObserveRequest(ctx, RequestObservation{Outcome: OutcomeSuccess})
	hooks.ObserveEvidence(ctx, EvidenceObservation{Outcome: OutcomeSuccess})

	snapshots := sink.all()
	if got, want := len(snapshots), 2; got != want {
		t.Fatalf("snapshot count = %d, want %d", got, want)
	}
	if got := snapshots[0]; got.Denial != DenialNone || got.PacketStatus != PacketStatusUnknown || got.PacketSchemaVersion != SchemaVersionUnknown || got.PacketBaselineVersion != SchemaVersionUnknown || got.SourceFallback != SourceFallbackUnknown || got.QueryVersion != QueryVersionUnknown || got.RankingVersion != RankingVersionUnknown {
		t.Errorf("request snapshot defaults = %#v", got)
	}
	if got := snapshots[1]; got.SourceFallback != SourceFallbackNone {
		t.Errorf("evidence snapshot fallback = %q, want %q", got.SourceFallback, SourceFallbackNone)
	}
}

func TestHooksRecordConcurrently(t *testing.T) {
	t.Parallel()

	// Given: one shared hook instance and a correlated context.
	sink := &snapshotSink{}
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x77}, 16))))
	ctx, _, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatalf("EnsureRequestID() error = %v", err)
	}

	// When: several independent store completions record concurrently.
	const workers = 64
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			hooks.ObserveStore(ctx, StoreObservation{Outcome: OutcomeSuccess})
		}()
	}
	group.Wait()

	// Then: no observations are lost.
	if got := len(sink.all()); got != workers {
		t.Errorf("snapshot count = %d, want %d", got, workers)
	}
}
