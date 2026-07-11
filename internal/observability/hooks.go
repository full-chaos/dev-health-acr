package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"sync"
	"time"
)

type Sink interface {
	Record(SupportSnapshot)
}

type Hooks struct {
	sink Sink
	ids  *RequestIDGenerator
}

type RequestIDGenerator struct {
	reader io.Reader
	mu     sync.Mutex
}

type requestIDKey struct{}

type discardSink struct{}

func (discardSink) Record(SupportSnapshot) {}

func NewHooks(sink Sink, ids *RequestIDGenerator) Hooks {
	if sink == nil {
		sink = discardSink{}
	}
	if ids == nil {
		ids = NewRequestIDGenerator(rand.Reader)
	}
	return Hooks{sink: sink, ids: ids}
}

func NewRequestIDGenerator(reader io.Reader) *RequestIDGenerator {
	return &RequestIDGenerator{reader: reader}
}

func (g *RequestIDGenerator) New() (RequestID, error) {
	var raw [16]byte
	g.mu.Lock()
	_, err := io.ReadFull(g.reader, raw[:])
	g.mu.Unlock()
	if err != nil {
		return "", err
	}
	return RequestID("req_" + hex.EncodeToString(raw[:])), nil
}

func WithRequestID(ctx context.Context, value string) context.Context {
	if requestID, ok := parseRequestID(value); ok {
		return context.WithValue(ctx, requestIDKey{}, requestID)
	}
	return ctx
}

func RequestIDFromContext(ctx context.Context) (RequestID, bool) {
	requestID, ok := ctx.Value(requestIDKey{}).(RequestID)
	return requestID, ok && requestID != ""
}

func (h Hooks) EnsureRequestID(ctx context.Context) (context.Context, RequestID, error) {
	if requestID, ok := RequestIDFromContext(ctx); ok {
		return ctx, requestID, nil
	}
	requestID, err := h.ids.New()
	if err != nil {
		return ctx, "", err
	}
	return context.WithValue(ctx, requestIDKey{}, requestID), requestID, nil
}

func (h Hooks) ObserveRequest(ctx context.Context, observation RequestObservation) {
	snapshot := baseSnapshot(KindRequest, observation.Outcome, observation.Duration)
	snapshot.Operation = normalizeOperation(observation.Operation)
	snapshot.HTTPStatusClass = normalizeHTTPStatusClass(observation.StatusClass)
	snapshot.Denial = normalizeDenial(observation.Denial)
	h.record(ctx, snapshot)
}

func (h Hooks) ObserveStore(ctx context.Context, observation StoreObservation) {
	snapshot := baseSnapshot(KindStore, observation.Outcome, observation.Duration)
	snapshot.PacketStatus = normalizePacketStatus(observation.Packet.Status)
	snapshot.PacketBytes = nonNegative(observation.Packet.Bytes)
	snapshot.PacketTokens = nonNegative(observation.Packet.Tokens)
	snapshot.PacketItems = nonNegative(observation.Packet.Items)
	snapshot.StaleSources = nonNegative(observation.Packet.StaleSources)
	snapshot.UnavailableSources = nonNegative(observation.Packet.UnavailableSources)
	snapshot.VersionMismatch = observation.Packet.VersionMismatch
	snapshot.PacketSchemaVersion = normalizeSchemaVersion(observation.Packet.SchemaVersion)
	snapshot.PacketBaselineVersion = normalizeSchemaVersion(observation.Packet.BaselineVersion)
	snapshot.Compatibility = normalizeCompatibility(observation.Packet.Compatibility)
	snapshot.SourceCoverage = normalizeSourceCoverage(observation.Packet.SourceCoverage)
	snapshot.StoreQueryClass = normalizeStoreQueryClass(observation.QueryClass)
	snapshot.StoreBackend = normalizeStoreBackend(observation.Backend)
	snapshot.QueryTimedOut = observation.TimedOut
	h.record(ctx, snapshot)
}

func (h Hooks) ObserveRanking(ctx context.Context, observation RankingObservation) {
	snapshot := baseSnapshot(KindRanking, observation.Outcome, observation.Duration)
	snapshot.QueryVersion = normalizeQueryVersion(observation.QueryVersion)
	snapshot.RankingVersion = normalizeRankingVersion(observation.RankingVersion)
	h.record(ctx, snapshot)
}

func (h Hooks) ObserveEvidence(ctx context.Context, observation EvidenceObservation) {
	snapshot := baseSnapshot(KindEvidence, observation.Outcome, observation.Duration)
	snapshot.SourceFallback = normalizeSourceFallback(observation.SourceFallback)
	snapshot.SourceCoverage = normalizeSourceCoverage(observation.SourceCoverage)
	h.record(ctx, snapshot)
}

func (h Hooks) ObserveEpisode(ctx context.Context, observation EpisodeObservation) {
	snapshot := baseSnapshot(KindEpisode, observation.Outcome, observation.Duration)
	snapshot.EpisodeOutcome = normalizeEpisodeOutcome(observation.EpisodeOutcome)
	snapshot.AuditDelivery = normalizeAuditDelivery(observation.AuditDelivery)
	h.record(ctx, snapshot)
}

func (h Hooks) record(ctx context.Context, snapshot SupportSnapshot) {
	if requestID, ok := RequestIDFromContext(ctx); ok {
		snapshot.RequestID = requestID
	}
	defer func() { _ = recover() }()
	h.sink.Record(snapshot)
}

func baseSnapshot(kind Kind, outcome Outcome, duration time.Duration) SupportSnapshot {
	return SupportSnapshot{
		Kind:                  kind,
		Outcome:               normalizeOutcome(outcome),
		Operation:             OperationUnknown,
		HTTPStatusClass:       HTTPStatusUnknown,
		DurationMillis:        durationMillis(duration),
		PacketStatus:          PacketStatusUnknown,
		PacketSchemaVersion:   SchemaVersionUnknown,
		PacketBaselineVersion: SchemaVersionUnknown,
		SourceFallback:        SourceFallbackUnknown,
		QueryVersion:          QueryVersionUnknown,
		RankingVersion:        RankingVersionUnknown,
		Denial:                DenialUnknown,
		Compatibility:         CompatibilityUnknown,
		SourceCoverage:        SourceCoverageUnknown,
		StoreQueryClass:       StoreQueryUnknown,
		StoreBackend:          StoreBackendUnknown,
		EpisodeOutcome:        EpisodeOutcomeUnknown,
		AuditDelivery:         AuditDeliveryUnknown,
	}
}
