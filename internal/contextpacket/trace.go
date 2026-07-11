package contextpacket

import "context"

type TraceStage string

const (
	TraceStageRequest TraceStage = "request"
	TraceStageStore   TraceStage = "store"
	TraceStageRanking TraceStage = "ranking"
)

type TraceObservation struct {
	Stage          TraceStage
	StoreOperation StoreOperation
}

type AssemblyTraceBoundary interface {
	Start(context.Context, TraceObservation) (context.Context, func(OperationOutcome))
}

func (a *Assembler) startTrace(ctx context.Context, observation TraceObservation) (context.Context, func(OperationOutcome)) {
	if a.options.Tracer == nil {
		return ctx, func(OperationOutcome) {}
	}
	tracedCtx, complete := safeStartTrace(a.options.Tracer, ctx, observation)
	return tracedCtx, func(outcome OperationOutcome) {
		defer func() { _ = recover() }()
		complete(outcome)
	}
}

func safeStartTrace(tracer AssemblyTraceBoundary, ctx context.Context, observation TraceObservation) (tracedCtx context.Context, complete func(OperationOutcome)) {
	tracedCtx, complete = ctx, func(OperationOutcome) {}
	defer func() { _ = recover() }()
	startedCtx, startedComplete := tracer.Start(ctx, observation)
	if startedCtx != nil {
		tracedCtx = startedCtx
	}
	if startedComplete != nil {
		complete = startedComplete
	}
	return tracedCtx, complete
}
