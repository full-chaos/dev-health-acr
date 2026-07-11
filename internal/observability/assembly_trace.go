package observability

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type assemblyTraceBoundary struct {
	hooks    Hooks
	boundary TraceBoundary
}

func NewAssemblyTraceBoundary(hooks Hooks, boundary TraceBoundary) contextpacket.AssemblyTraceBoundary {
	return assemblyTraceBoundary{hooks: hooks, boundary: boundary}
}

func (b assemblyTraceBoundary) Start(ctx context.Context, observation contextpacket.TraceObservation) (context.Context, func(contextpacket.OperationOutcome)) {
	var traceContext context.Context
	var complete func(Outcome)
	switch observation.Stage {
	case contextpacket.TraceStageRequest:
		traceContext, complete = b.hooks.StartRequestTrace(ctx, OperationContext, b.boundary)
	case contextpacket.TraceStageStore:
		queryClass := StoreQueryPacket
		if observation.StoreOperation == contextpacket.StoreOperationEvidence {
			queryClass = StoreQueryEvidence
		}
		traceContext, complete = b.hooks.StartStoreTrace(ctx, queryClass, b.boundary)
	case contextpacket.TraceStageRanking:
		traceContext, complete = b.hooks.StartRankingTrace(ctx, b.boundary)
	default:
		return ctx, func(contextpacket.OperationOutcome) {}
	}
	return traceContext, func(outcome contextpacket.OperationOutcome) { complete(assemblyOutcome(outcome)) }
}
