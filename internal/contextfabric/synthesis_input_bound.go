package contextfabric

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SynthesisInputBoundOutcome is the closed vocabulary of how bounding the
// facts handed to answer synthesis ended.
type SynthesisInputBoundOutcome string

const (
	// SynthesisInputBoundFitted: the model input fit after the facts were
	// bounded, and the model call was placed with the bounded set.
	SynthesisInputBoundFitted SynthesisInputBoundOutcome = "fitted"
	// SynthesisInputBoundExhausted: the model input still exceeded the bound
	// when no further fact could be removed or the pass limit was reached.
	// The investigation ends with ErrModelInputTooLarge.
	SynthesisInputBoundExhausted SynthesisInputBoundOutcome = "exhausted"
)

// SynthesisInputBoundEvent reports one bounding decision. Counts and a closed
// outcome only.
type SynthesisInputBoundEvent struct {
	Outcome SynthesisInputBoundOutcome
	// Passes is how many times the fact set was reduced.
	Passes int
	// InputBytes is the encoded size of the unbounded input; MaxInputBytes is
	// the runtime's bound.
	InputBytes    int
	MaxInputBytes int
	FactsRead     int
	FactsGiven    int
	KindsRead     int
	KindsGiven    int
	KindsBounded  int
}

const (
	// maxSynthesisInputBoundPasses bounds the reduce-and-measure loop. A pass
	// costs one encode and no model call.
	maxSynthesisInputBoundPasses = 6
	// synthesisInputFillPercent is the share of the bound a reduction aims
	// for, so a pass that lands near the bound does not need a second one.
	synthesisInputFillPercent = 90
)

// boundSynthesisFacts reduces a fact set that made the model input overflow.
//
// Every kind keeps the same share of its facts, the share the overflow calls
// for, and at least one: no kind is removed. Within a kind the facts that
// stay are the first in read order, so kinds read over the same subjects keep
// the same subjects. A fact about a committed subject always stays.
//
// The second result is false when nothing could be removed.
func boundSynthesisFacts(facts []CanonicalFact, committed []SubjectRef, overflow *ModelInputOverflow) ([]CanonicalFact, bool) {
	if overflow == nil || overflow.Bytes <= 0 || overflow.MaxBytes <= 0 {
		return facts, false
	}
	protected := make(map[string]struct{}, len(committed))
	for _, subject := range committed {
		protected[subject.CanonicalID] = struct{}{}
	}
	quota := factCountsByKind(facts)
	for kind, count := range quota {
		keep := int(int64(count) * int64(overflow.MaxBytes) * synthesisInputFillPercent / (int64(overflow.Bytes) * 100))
		if keep < 1 {
			keep = 1
		}
		quota[kind] = keep
	}
	for _, fact := range facts {
		if _, ok := protected[fact.Subject.CanonicalID]; ok {
			quota[fact.Kind]--
		}
	}
	bounded := make([]CanonicalFact, 0, len(facts))
	for _, fact := range facts {
		if _, ok := protected[fact.Subject.CanonicalID]; ok {
			bounded = append(bounded, fact)
			continue
		}
		if quota[fact.Kind] > 0 {
			quota[fact.Kind]--
			bounded = append(bounded, fact)
		}
	}
	return bounded, len(bounded) < len(facts)
}

func factCountsByKind(facts []CanonicalFact) map[FactKind]int {
	counts := make(map[FactKind]int)
	for _, fact := range facts {
		counts[fact.Kind]++
	}
	return counts
}

// synthesizeWithinInputBound places the synthesis call and, when the runtime
// answers that its input does not fit, reduces the facts and places it again.
// It returns the input the model was given and whether that input was bounded.
func (r RuntimeAnswerSynthesizer) synthesizeWithinInputBound(ctx context.Context, principal storage.Principal, input SynthesisInput) (SynthesisInput, SynthesisDraft, ModelExecutionReceipt, bool, error) {
	given := input
	var event SynthesisInputBoundEvent
	for {
		draft, receipt, err := r.Runtime.SynthesizeAnswer(ctx, principal, given)
		var overflow *ModelInputOverflow
		if !errors.As(err, &overflow) {
			if event.Passes > 0 {
				event.Outcome = SynthesisInputBoundFitted
				r.recordSynthesisInputBound(ctx, principal, event, input, given)
			}
			return given, draft, receipt, event.Passes > 0, err
		}
		if event.Passes == 0 {
			event.InputBytes, event.MaxInputBytes = overflow.Bytes, overflow.MaxBytes
		}
		bounded, reduced := boundSynthesisFacts(given.Facts.Facts, given.Graph.Resolution.Committed, overflow)
		if !reduced || event.Passes == maxSynthesisInputBoundPasses {
			event.Outcome = SynthesisInputBoundExhausted
			r.recordSynthesisInputBound(ctx, principal, event, input, given)
			return given, draft, receipt, false, err
		}
		event.Passes++
		given.Facts.Facts = bounded
	}
}

func (r RuntimeAnswerSynthesizer) recordSynthesisInputBound(ctx context.Context, principal storage.Principal, event SynthesisInputBoundEvent, read, given SynthesisInput) {
	if r.Telemetry == nil {
		return
	}
	readCounts, givenCounts := factCountsByKind(read.Facts.Facts), factCountsByKind(given.Facts.Facts)
	event.FactsRead, event.FactsGiven = len(read.Facts.Facts), len(given.Facts.Facts)
	event.KindsRead, event.KindsGiven = len(readCounts), len(givenCounts)
	for kind, count := range readCounts {
		if givenCounts[kind] < count {
			event.KindsBounded++
		}
	}
	r.Telemetry.RecordSynthesisInputBound(ctx, principal, event)
}
