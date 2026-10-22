package contextfabric

import (
	"context"
	"errors"
	"sync"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ErrModelSourceMixed marks a turn whose service-side model calls ran on more
// than one model source. One turn never mixes the organization's own model and
// the platform model; a turn that did is refused rather than labelled with
// either, because a label would hide whose model produced part of the answer.
var ErrModelSourceMixed = errors.New("context fabric turn mixed model sources")

type modelSourceRecorderKey struct{}

// ModelSourceRecorder collects the model source of every service-side model
// call of one turn. It is safe for concurrent use: the interpretation ensemble
// calls the model from several goroutines.
type ModelSourceRecorder struct {
	mu      sync.Mutex
	sources map[contractsv1.ContextFabricModelSource]struct{}
}

// WithModelSourceRecorder installs a fresh recorder for one turn.
func WithModelSourceRecorder(ctx context.Context) context.Context {
	return context.WithValue(ctx, modelSourceRecorderKey{}, &ModelSourceRecorder{sources: map[contractsv1.ContextFabricModelSource]struct{}{}})
}

// RecordModelSource notes that a service-side model call of this turn runs on
// source. It is a no-op on a context with no recorder, so a model call made
// outside an investigation turn records nothing.
func RecordModelSource(ctx context.Context, source contractsv1.ContextFabricModelSource) {
	recorder, ok := ctx.Value(modelSourceRecorderKey{}).(*ModelSourceRecorder)
	if !ok || recorder == nil || source == "" {
		return
	}
	recorder.mu.Lock()
	recorder.sources[source] = struct{}{}
	recorder.mu.Unlock()
}

// ModelSourceOf reports the one source the turn's model calls ran on: "" when
// none ran, ErrModelSourceMixed when more than one source was recorded.
func ModelSourceOf(ctx context.Context) (contractsv1.ContextFabricModelSource, error) {
	recorder, ok := ctx.Value(modelSourceRecorderKey{}).(*ModelSourceRecorder)
	if !ok || recorder == nil {
		return "", nil
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	switch len(recorder.sources) {
	case 0:
		return "", nil
	case 1:
		for source := range recorder.sources {
			return source, nil
		}
	}
	return "", ErrModelSourceMixed
}

// stampModelSource puts the turn's model source on a result about to be served
// and stored. A turn that ran no service-side model call leaves the result
// as it is, so a reused result keeps the source of the turn that produced it.
func stampModelSource(ctx context.Context, result InvestigationResult) (InvestigationResult, error) {
	source, err := ModelSourceOf(ctx)
	if err != nil {
		return result, err
	}
	if source != "" {
		result.ModelSource = source
	}
	return result, nil
}
