package observability

import "context"

type TraceName string

const (
	TraceRequest TraceName = "acr.request"
	TraceStore   TraceName = "acr.store"
	TraceRanking TraceName = "acr.ranking"
)

type TraceObservation struct {
	Name            TraceName
	RequestID       RequestID
	Operation       Operation
	StoreQueryClass StoreQueryClass
}

type TraceBoundary interface {
	Start(context.Context, TraceObservation) (context.Context, func(Outcome))
}

func (h Hooks) StartRequestTrace(ctx context.Context, operation Operation, boundary TraceBoundary) (context.Context, func(Outcome)) {
	return startTrace(ctx, boundary, TraceObservation{Name: TraceRequest, RequestID: requestID(ctx), Operation: normalizeOperation(operation)})
}

func (h Hooks) StartStoreTrace(ctx context.Context, queryClass StoreQueryClass, boundary TraceBoundary) (context.Context, func(Outcome)) {
	return startTrace(ctx, boundary, TraceObservation{Name: TraceStore, RequestID: requestID(ctx), StoreQueryClass: normalizeStoreQueryClass(queryClass)})
}

func (h Hooks) StartRankingTrace(ctx context.Context, boundary TraceBoundary) (context.Context, func(Outcome)) {
	return startTrace(ctx, boundary, TraceObservation{Name: TraceRanking, RequestID: requestID(ctx)})
}

func startTrace(ctx context.Context, boundary TraceBoundary, observation TraceObservation) (context.Context, func(Outcome)) {
	if boundary == nil {
		return ctx, func(Outcome) {}
	}
	traceContext, complete := boundary.Start(ctx, observation)
	return traceContext, func(outcome Outcome) { complete(normalizeOutcome(outcome)) }
}

func requestID(ctx context.Context) RequestID {
	value, _ := RequestIDFromContext(ctx)
	return value
}
