package observability

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type assemblyObserver struct{ hooks Hooks }

func NewAssemblyObserver(hooks Hooks) contextpacket.AssemblyObserver {
	return assemblyObserver{hooks: hooks}
}

func (o assemblyObserver) ObserveStoreQuery(ctx context.Context, observation contextpacket.StoreQueryObservation) {
	queryClass := StoreQueryPacket
	if observation.Operation == contextpacket.StoreOperationEvidence {
		queryClass = StoreQueryEvidence
	}
	outcome := assemblyOutcome(observation.Outcome)
	o.hooks.ObserveStore(ctx, StoreObservation{
		QueryClass: queryClass, Backend: assemblyStoreBackend(observation.Backend), Outcome: outcome, Duration: observation.Duration,
		TimedOut: observation.Outcome == contextpacket.OperationTimeout,
	})
}

func assemblyStoreBackend(backend contextpacket.StoreBackend) StoreBackend {
	switch backend {
	case contextpacket.StoreBackendMemory:
		return StoreBackendMemory
	case contextpacket.StoreBackendPostgres:
		return StoreBackendPostgres
	case contextpacket.StoreBackendClickHouse:
		return StoreBackendClickHouse
	default:
		return StoreBackendUnknown
	}
}

func (o assemblyObserver) ObserveRanking(ctx context.Context, observation contextpacket.RankingObservation) {
	o.hooks.ObserveRanking(ctx, RankingObservation{
		Outcome: assemblyOutcome(observation.Outcome), Duration: observation.Duration,
		QueryVersion: QueryVersion(observation.QueryVersion), RankingVersion: RankingVersion(observation.RankingVersion),
	})
}

func (o assemblyObserver) ObservePacket(ctx context.Context, observation contextpacket.PacketObservation) {
	coverage := SourceCoverageFull
	if observation.UnavailableSources > 0 || observation.StaleSources > 0 {
		coverage = SourceCoveragePartial
	}
	o.hooks.ObserveStore(ctx, StoreObservation{
		QueryClass: StoreQueryPacket, Outcome: assemblyOutcome(observation.Outcome), Duration: observation.Duration,
		Packet: PacketObservation{
			Status: PacketStatus(observation.Status), Bytes: int64(observation.Bytes), Tokens: int64(observation.Tokens), Items: int64(observation.Items),
			SchemaVersion: SchemaVersion(observation.SchemaVersion), BaselineVersion: SchemaVersionContextPacket,
			SourceCoverage: coverage, StaleSources: int64(observation.StaleSources), UnavailableSources: int64(observation.UnavailableSources),
			Compatibility: assemblyCompatibility(observation.Compatibility), VersionMismatch: observation.VersionMismatch,
		},
	})
}

func assemblyCompatibility(compatibility contextpacket.CompatibilityOutcome) CompatibilityStatus {
	switch compatibility {
	case contextpacket.CompatibilityCompatible:
		return CompatibilityCompatible
	case contextpacket.CompatibilityIncompatible:
		return CompatibilityIncompatible
	default:
		return CompatibilityUnknown
	}
}

func assemblyOutcome(outcome contextpacket.OperationOutcome) Outcome {
	switch outcome {
	case contextpacket.OperationSuccess:
		return OutcomeSuccess
	case contextpacket.OperationCanceled:
		return OutcomeCanceled
	case contextpacket.OperationDenied:
		return OutcomeDenied
	case contextpacket.OperationFailure, contextpacket.OperationTimeout:
		return OutcomeFailure
	default:
		return OutcomeUnknown
	}
}
