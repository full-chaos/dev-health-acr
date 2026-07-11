package contextpacket

import (
	"context"
	"testing"
)

func TestStartTraceIsolatesBoundaryPanics(t *testing.T) {
	assembler := &Assembler{options: Options{Tracer: panickingTraceBoundary{}}}
	ctx := context.Background()

	tracedCtx, complete := assembler.startTrace(ctx, TraceObservation{Stage: TraceStageRequest})
	complete(OperationSuccess)

	if tracedCtx != ctx {
		t.Fatal("panicking trace boundary replaced context")
	}
}

func TestCompleteTraceIsolatesBoundaryPanics(t *testing.T) {
	assembler := &Assembler{options: Options{Tracer: panickingTraceCompletion{}}}

	_, complete := assembler.startTrace(context.Background(), TraceObservation{Stage: TraceStageRanking})
	complete(OperationSuccess)
}

type panickingTraceBoundary struct{}

func (panickingTraceBoundary) Start(context.Context, TraceObservation) (context.Context, func(OperationOutcome)) {
	panic("trace start")
}

type panickingTraceCompletion struct{}

func (panickingTraceCompletion) Start(ctx context.Context, _ TraceObservation) (context.Context, func(OperationOutcome)) {
	return ctx, func(OperationOutcome) { panic("trace completion") }
}
