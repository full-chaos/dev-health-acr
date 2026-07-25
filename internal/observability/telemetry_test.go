package observability

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestRequestIDGeneratorUsesCanonicalPrefix(t *testing.T) {
	identifier, err := NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0xab}, 16))).New()
	if err != nil || identifier != RequestID("req_"+strings.Repeat("ab", 16)) {
		t.Fatalf("New() = %q, %v", identifier, err)
	}
}

func TestWithRequestIDRejectsUppercaseHex(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req_ABCDEFABCDEFABCDEFABCDEFABCDEFAB")
	if _, ok := RequestIDFromContext(ctx); ok {
		t.Fatal("uppercase request ID was accepted")
	}
}

func TestMemorySinkBoundsRetainedSnapshots(t *testing.T) {
	sink := NewMemorySink(2)
	for _, outcome := range []Outcome{OutcomeSuccess, OutcomeFailure, OutcomeDenied} {
		sink.Record(SupportSnapshot{Outcome: outcome})
	}
	snapshots := sink.Snapshots()
	if len(snapshots) != 2 || snapshots[0].Outcome != OutcomeFailure || snapshots[1].Outcome != OutcomeDenied {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestEvidenceExpansionAdapterEmitsSafeCoverage(t *testing.T) {
	sink := NewMemorySink(1)
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x71}, 16))))
	ctx, _, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	NewEvidenceExpansionObserver(hooks).ObserveEvidenceExpansion(ctx, contextpacket.EvidenceExpansionObservation{System: "Bearer raw-evidence-secret", Availability: contractsv1.EvidenceStale, Duration: time.Millisecond})
	snapshot := sink.Snapshots()[0]
	if snapshot.SourceFallback != SourceFallbackNone || snapshot.SourceCoverage != SourceCoveragePartial || strings.Contains(fmt.Sprintf("%#v", snapshot), "raw-evidence-secret") {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestHooksEmitCanonicalVersionsAndBoundedLifecycle(t *testing.T) {
	sink := NewMemorySink(2)
	hooks := NewHooks(sink, NewRequestIDGenerator(bytes.NewReader(bytes.Repeat([]byte{0x19}, 16))))
	ctx, _, err := hooks.EnsureRequestID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hooks.ObserveStore(ctx, StoreObservation{QueryClass: StoreQueryPacket, Backend: StoreBackendPostgres, TimedOut: true, Packet: PacketObservation{Status: PacketStatusDegraded, SchemaVersion: SchemaVersionContextPacket, BaselineVersion: SchemaVersionContextPacket, Compatibility: CompatibilityPartial, SourceCoverage: SourceCoveragePartial, Items: 3, StaleSources: 1, UnavailableSources: 2, VersionMismatch: true}})
	hooks.ObserveEpisode(ctx, EpisodeObservation{Outcome: OutcomeSuccess, EpisodeOutcome: EpisodeOutcomeDuplicate, AuditDelivery: AuditDeliveryDelivered})
	snapshots := sink.Snapshots()
	if snapshots[0].PacketSchemaVersion != SchemaVersionContextPacket || snapshots[0].StoreBackend != StoreBackendPostgres || !snapshots[0].QueryTimedOut || snapshots[0].PacketItems != 3 || snapshots[0].StaleSources != 1 || snapshots[0].UnavailableSources != 2 || !snapshots[0].VersionMismatch || snapshots[1].EpisodeOutcome != EpisodeOutcomeDuplicate || snapshots[1].AuditDelivery != AuditDeliveryDelivered {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestTraceCompletionNormalizesUntrustedOutcome(t *testing.T) {
	boundary := &capturingTraceBoundary{}
	_, complete := startTrace(context.Background(), boundary, TraceObservation{Name: TraceStore})

	complete(Outcome("Bearer secret-outcome"))

	if boundary.outcome != OutcomeUnknown {
		t.Fatalf("trace outcome = %q", boundary.outcome)
	}
}

func TestSlogSinkIncludesBoundedStoreQueryDetails(t *testing.T) {
	buffer := &bytes.Buffer{}
	sink := NewSlogSink(slog.New(slog.NewJSONHandler(buffer, nil)))

	sink.Record(SupportSnapshot{
		Kind:            KindStore,
		StoreQueryClass: StoreQueryEvidence,
		StoreSource:     "file_complexity.v1",
		StorePhase:      StorePhaseIteration,
	})

	if log := buffer.String(); !strings.Contains(log, `"store_query_class":"evidence"`) ||
		!strings.Contains(log, `"store_source":"file_complexity.v1"`) ||
		!strings.Contains(log, `"store_phase":"iteration"`) {
		t.Fatalf("log = %s", buffer.String())
	}
}

func TestAssemblyObserverIncludesBoundedSourceFailurePhase(t *testing.T) {
	// Given
	sink := NewMemorySink(4)
	observer := NewAssemblyObserver(NewHooks(sink, nil))

	// When
	observer.ObserveStoreQuery(context.Background(), contextpacket.StoreQueryObservation{
		Operation:   contextpacket.StoreOperationEvidence,
		Backend:     contextpacket.StoreBackendClickHouse,
		Outcome:     contextpacket.OperationFailure,
		SourceID:    "file_complexity.v1",
		SourcePhase: contextpacket.SourceQueryPhaseIteration,
	})

	// Then
	snapshot := sink.Snapshots()[0]
	if snapshot.StoreSource != "file_complexity.v1" || snapshot.StorePhase != StorePhaseIteration {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

type capturingTraceBoundary struct{ outcome Outcome }

func (b *capturingTraceBoundary) Start(ctx context.Context, _ TraceObservation) (context.Context, func(Outcome)) {
	return ctx, func(outcome Outcome) { b.outcome = outcome }
}
