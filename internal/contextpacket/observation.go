package contextpacket

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type OperationOutcome string

const (
	OperationSuccess  OperationOutcome = "success"
	OperationFailure  OperationOutcome = "failure"
	OperationDenied   OperationOutcome = "denied"
	OperationCanceled OperationOutcome = "canceled"
	OperationTimeout  OperationOutcome = "timeout"
)

type StoreOperation string

const (
	StoreOperationScope    StoreOperation = "scope"
	StoreOperationEvidence StoreOperation = "evidence"
)

type StoreQueryObservation struct {
	Operation StoreOperation
	Backend   StoreBackend
	Outcome   OperationOutcome
	Duration  time.Duration
}

type StoreBackend string

const (
	StoreBackendUnknown    StoreBackend = "unknown"
	StoreBackendMemory     StoreBackend = "memory"
	StoreBackendPostgres   StoreBackend = "postgres"
	StoreBackendClickHouse StoreBackend = "clickhouse"
)

type RankingObservation struct {
	Outcome        OperationOutcome
	Duration       time.Duration
	QueryVersion   string
	RankingVersion string
}

type PacketObservation struct {
	Outcome            OperationOutcome
	Duration           time.Duration
	Status             contractsv1.PacketStatus
	SchemaVersion      string
	QueryVersion       string
	RankingVersion     string
	Items              int
	Tokens             int
	Bytes              int
	StaleSources       int
	UnavailableSources int
	Compatibility      CompatibilityOutcome
	VersionMismatch    bool
}

type AssemblyObserver interface {
	ObserveStoreQuery(context.Context, StoreQueryObservation)
	ObserveRanking(context.Context, RankingObservation)
	ObservePacket(context.Context, PacketObservation)
}

func operationOutcome(err error) OperationOutcome {
	switch {
	case err == nil:
		return OperationSuccess
	case errors.Is(err, context.Canceled):
		return OperationCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return OperationTimeout
	default:
		return OperationFailure
	}
}

func (a *Assembler) resolveScope(ctx context.Context, principal storage.Principal, request contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	started := a.options.Now()
	ctx, completeTrace := a.startTrace(ctx, TraceObservation{Stage: TraceStageStore, StoreOperation: StoreOperationScope})
	scope, err := a.store.ResolveScope(ctx, principal, request)
	completeTrace(operationOutcome(err))
	a.observeStoreQuery(ctx, StoreOperationScope, err, a.options.Now().Sub(started))
	return scope, err
}

func (a *Assembler) contextForTask(ctx context.Context, principal storage.Principal, request contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	started := a.options.Now()
	ctx, completeTrace := a.startTrace(ctx, TraceObservation{Stage: TraceStageStore, StoreOperation: StoreOperationEvidence})
	bundle, err := a.store.ContextForTask(ctx, principal, request)
	completeTrace(operationOutcome(err))
	a.observeStoreQuery(ctx, StoreOperationEvidence, err, a.options.Now().Sub(started))
	return bundle, err
}

func (a *Assembler) observeStoreQuery(ctx context.Context, operation StoreOperation, err error, duration time.Duration) {
	if a.options.Observer != nil && a.options.StoreBackend != StoreBackendClickHouse {
		a.options.Observer.ObserveStoreQuery(ctx, StoreQueryObservation{Operation: operation, Backend: a.options.StoreBackend, Outcome: operationOutcome(err), Duration: duration})
	}
}

func beginStoreQueryObservation(ctx context.Context, observer AssemblyObserver, operation StoreOperation) func(error) {
	started := time.Now()
	return func(err error) {
		if observer == nil {
			return
		}
		defer func() { _ = recover() }()
		observer.ObserveStoreQuery(ctx, StoreQueryObservation{
			Operation: operation, Backend: StoreBackendClickHouse, Outcome: operationOutcome(err), Duration: time.Since(started),
		})
	}
}

func (a *Assembler) observeRanking(ctx context.Context, queryVersion, rankingVersion string, duration time.Duration) {
	if a.options.Observer != nil {
		a.options.Observer.ObserveRanking(ctx, RankingObservation{Outcome: OperationSuccess, Duration: duration, QueryVersion: queryVersion, RankingVersion: rankingVersion})
	}
}

func (a *Assembler) observePacket(ctx context.Context, request contractsv1.ContextPacketRequest, packet contractsv1.ContextPacket, err error, duration time.Duration) {
	if a.options.Observer == nil {
		return
	}
	staleSources := 0
	for _, watermark := range packet.Freshness.Watermarks {
		if watermark.Status == "stale" {
			staleSources++
		}
	}
	compatibility, versionMismatch := packetCompatibility(request, packet)
	a.options.Observer.ObservePacket(ctx, PacketObservation{
		Outcome: operationOutcome(err), Duration: duration, Status: packet.Status,
		SchemaVersion: packet.SchemaVersion, QueryVersion: packet.QueryVersion, RankingVersion: packet.RankingVersion,
		Items: len(packet.Items), Tokens: packet.Budget.EstimatedTokens, Bytes: packet.Budget.SerializedBytes,
		StaleSources: staleSources, UnavailableSources: len(packet.Coverage.SourcesUnavailable),
		Compatibility: compatibility, VersionMismatch: versionMismatch,
	})
}
