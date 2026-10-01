package contextfabric

import (
	"context"
	"errors"
	"sort"
	"time"

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
	// Selection is how the facts that stayed were chosen on the last pass.
	Selection SynthesisInputSelection
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
// stay are the most relevant to the question (see factRelevance); facts the
// ranking cannot tell apart keep their read order, so an input with nothing
// to rank on is bounded exactly as it was by position. A fact about a
// committed subject always stays.
//
// The second result is false when nothing could be removed. The third names
// how the facts that stayed were chosen.
func boundSynthesisFacts(facts []CanonicalFact, ranking factRanking, overflow *ModelInputOverflow) ([]CanonicalFact, bool, SynthesisInputSelection) {
	if overflow == nil || overflow.Bytes <= 0 || overflow.MaxBytes <= 0 {
		return facts, false, SynthesisInputSelectionPosition
	}
	protected := make(map[string]struct{}, len(ranking.committed))
	for _, subject := range ranking.committed {
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
	scores := make([]factRelevance, len(facts))
	byKind := make(map[FactKind][]int)
	for index, fact := range facts {
		scores[index] = ranking.score(fact)
		if _, ok := protected[fact.Subject.CanonicalID]; !ok {
			byKind[fact.Kind] = append(byKind[fact.Kind], index)
		}
	}
	selection := SynthesisInputSelectionPosition
	keep := make([]bool, len(facts))
	for index, fact := range facts {
		if _, ok := protected[fact.Subject.CanonicalID]; ok {
			keep[index] = true
		}
	}
	for kind, indexes := range byKind {
		ordered := append([]int(nil), indexes...)
		sort.SliceStable(ordered, func(i, j int) bool { return scores[ordered[i]].before(scores[ordered[j]]) })
		for rank, index := range ordered {
			if index != indexes[rank] {
				selection = SynthesisInputSelectionRelevance
			}
			if rank < quota[kind] {
				keep[index] = true
			}
		}
	}
	bounded := make([]CanonicalFact, 0, len(facts))
	for index, fact := range facts {
		if keep[index] {
			bounded = append(bounded, fact)
		}
	}
	return bounded, len(bounded) < len(facts), selection
}

// SynthesisInputSelection names how the facts that stayed in a bounded input
// were chosen.
type SynthesisInputSelection string

const (
	// SynthesisInputSelectionRelevance: the ranking changed the order inside
	// at least one kind, so the cut kept facts a read-order cut would not have.
	SynthesisInputSelectionRelevance SynthesisInputSelection = "relevance"
	// SynthesisInputSelectionPosition: the ranking left every kind in read
	// order, so the cut kept the first facts read.
	SynthesisInputSelectionPosition SynthesisInputSelection = "position"
)

// factRanking carries what the question says about which facts matter.
type factRanking struct {
	committed    []SubjectRef
	requirements []FactRequirement
	// windowStart and windowEnd bound the question window; both nil means the
	// question has no window to rank against.
	windowStart, windowEnd *time.Time
}

func newFactRanking(input SynthesisInput) factRanking {
	ranking := factRanking{committed: input.Graph.Resolution.Committed, requirements: input.Interpretation.FactRequirements}
	start, end := input.Interpretation.TimeContext.Start, input.Interpretation.TimeContext.End
	if window := input.EvidenceWindow; window != nil && window.Start != nil && window.End != nil {
		start, end = window.Start, window.End
	}
	if start != nil && end != nil && !end.Before(*start) {
		ranking.windowStart, ranking.windowEnd = start, end
	}
	return ranking
}

// factRelevance is a fact's rank, compared field by field in the order the
// fields are declared.
type factRelevance struct {
	committed bool
	required  bool
	// overlap: 3 inside the window, 2 partly inside, 1 unknown, 0 outside.
	overlap  int
	observed time.Time
}

func (a factRelevance) before(b factRelevance) bool {
	if a.committed != b.committed {
		return a.committed
	}
	if a.required != b.required {
		return a.required
	}
	if a.overlap != b.overlap {
		return a.overlap > b.overlap
	}
	return a.observed.After(b.observed)
}

func (r factRanking) score(fact CanonicalFact) factRelevance {
	var score factRelevance
	for _, subject := range r.committed {
		if subject.CanonicalID == fact.Subject.CanonicalID {
			score.committed = true
			break
		}
	}
	for _, requirement := range r.requirements {
		if requirement.Kind != fact.Kind {
			continue
		}
		if len(requirement.Subjects) == 0 {
			score.required = true
			break
		}
		for _, subject := range requirement.Subjects {
			if subject.CanonicalID == fact.Subject.CanonicalID {
				score.required = true
			}
		}
	}
	score.overlap = r.overlap(fact)
	if fact.ObservedAt != nil {
		score.observed = *fact.ObservedAt
	}
	return score
}

// overlap places the fact's own period, or its event time when it has no
// period, against the question window.
func (r factRanking) overlap(fact CanonicalFact) int {
	if r.windowStart == nil {
		return 0
	}
	from, to, ok := factPeriod(fact)
	if !ok {
		return 1
	}
	switch {
	case to.Before(*r.windowStart) || from.After(*r.windowEnd):
		return 0
	case !from.Before(*r.windowStart) && !to.After(*r.windowEnd):
		return 3
	default:
		return 2
	}
}

func factPeriod(fact CanonicalFact) (time.Time, time.Time, bool) {
	start, end := factTimeField(fact, "window_start"), factTimeField(fact, "window_end")
	if start != nil && end != nil {
		return *start, *end, true
	}
	if fact.EventAt != nil {
		return *fact.EventAt, *fact.EventAt, true
	}
	return time.Time{}, time.Time{}, false
}

func factTimeField(fact CanonicalFact, name string) *time.Time {
	value, ok := fact.Fields[name]
	if !ok || value.String == nil {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if parsed, err := time.Parse(layout, *value.String); err == nil {
			return &parsed
		}
	}
	return nil
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
	ranking := newFactRanking(input)
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
		bounded, reduced, selection := boundSynthesisFacts(given.Facts.Facts, ranking, overflow)
		if selection == SynthesisInputSelectionRelevance || event.Selection == "" {
			event.Selection = selection
		}
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
