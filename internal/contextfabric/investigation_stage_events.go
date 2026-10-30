package contextfabric

import (
	"context"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The engine side of the investigation event stream (CHAOS-11910, plan of
// record §2.7.2). Investigate reports the boundaries of its five wire stages
// -- contractsv1.ContextFabricInvestigationStages -- to a sink carried on the
// request context, the same side-channel shape as WithSynthesisInputCollector.
// The route that serves the stream (CHAOS-11911) installs the sink, turns each
// transition into a `stage` event and writes it; every other caller installs
// nothing, and then every call below is a no-op: no allocation, no lock, no
// change to what Investigate returns.
//
// The sites, one per stage:
//
//	interpreting       the interpreter call in Investigate
//	resolving_subjects the decisive ResolveSubjects call in Investigate
//	reading_facts      the canonical fact read in Investigate
//	synthesizing       synthesizeAndAssemble (every synthesis pass enters here)
//	validating         finalizeServedAs: the final budget assertion and
//	                   validation, which every serving path (fresh, reused,
//	                   terminal) runs before its Save, so the Save is inside
//	                   the stage
//
// A path may skip stages (answer reuse, an early clarification, a window or
// structure veto) and a stage may be entered more than once (the budget
// re-synthesis, a second finalization). The sink, not each site, keeps the
// stream in the contract's grammar: a stage starts only after every stage
// already started, a repeat or an earlier stage is ignored, a stage still open
// when a later one starts is completed first, and only the open stage
// completes. Investigate completes the open stage when it returns a served
// answer, so a stream never ends on a result with a stage open; a failure
// leaves it open, and the error terminal names it.
//
// The sink never blocks the engine and runs no caller code on the engine's
// goroutine (it takes no caller function, not even a clock). Transitions go into a buffered channel of
// InvestigationStageSinkCapacity, and one slot of it is held back. When only
// that slot is left (the reader is behind), the sink OVERFLOWS: from then on it
// sends no ordinary transition, and counts each one it drops, by stage. So what
// a reader receives is always a prefix of the run's transitions, and a prefix
// is a valid stage sequence on its own. When Investigate returns a served
// answer, the held-back slot carries the completion of the stage the reader
// last saw start (if the reader saw it start but not complete), so the stream
// the reader can build ends with no stage open, as a `result` requires. A
// failure leaves that stage open, and the error terminal names it.
//
// The route reads DroppedByStage after Investigate returns and records
// acr_investigation_events_dropped_total from it, off the engine's goroutine.
// The grammar bounds a run to two transitions per stage, ten in all, below the
// capacity, so a production sink drops nothing even when its reader is
// stalled; the overflow is the guarantee that a stalled reader can never stall
// the engine. Terminal events never pass through the sink: the route writes
// them itself, after Investigate returns, so a terminal cannot be dropped.

// InvestigationStageSinkCapacity is the buffer of a production sink.
const InvestigationStageSinkCapacity = 32

// InvestigationStageTransition is one stage boundary: the stage, whether it
// started or completed, and when (time.Now).
type InvestigationStageTransition struct {
	Stage contractsv1.ContextFabricInvestigationStage
	State contractsv1.ContextFabricInvestigationStageState
	At    time.Time
}

// InvestigationStageSink receives one request's stage transitions. Create it
// with NewInvestigationStageSink, install it with WithInvestigationStageSink,
// and read Transitions while Investigate runs.
type InvestigationStageSink struct {
	transitions chan InvestigationStageTransition

	mu sync.Mutex
	// open and lastStarted are the engine's stage state, which the grammar
	// rules are decided on.
	open        contractsv1.ContextFabricInvestigationStage
	lastStarted contractsv1.ContextFabricInvestigationStage
	// delivered is the stage whose start the reader received and whose
	// completion it did not.
	delivered  contractsv1.ContextFabricInvestigationStage
	overflowed bool
	dropped    map[contractsv1.ContextFabricInvestigationStage]int64
}

// NewInvestigationStageSink makes a sink with room for capacity transitions
// (at least two: one slot is held back for the closing completion). Each
// transition is stamped with time.Now: the sink takes no caller function, so
// nothing a caller supplies runs on the engine's goroutine.
func NewInvestigationStageSink(capacity int) *InvestigationStageSink {
	if capacity < 2 {
		capacity = 2
	}
	return &InvestigationStageSink{
		transitions: make(chan InvestigationStageTransition, capacity),
		dropped:     map[contractsv1.ContextFabricInvestigationStage]int64{},
	}
}

// Transitions is the channel the stream writer reads. The sink never closes
// it: the writer stops reading when Investigate has returned and the buffer is
// empty.
func (s *InvestigationStageSink) Transitions() <-chan InvestigationStageTransition {
	return s.transitions
}

// Dropped counts the transitions that never reached the reader.
func (s *InvestigationStageSink) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, count := range s.dropped {
		total += count
	}
	return total
}

// DroppedByStage is Dropped by stage, for the route's metric. It is a copy.
func (s *InvestigationStageSink) DroppedByStage() map[contractsv1.ContextFabricInvestigationStage]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[contractsv1.ContextFabricInvestigationStage]int64, len(s.dropped))
	for stage, count := range s.dropped {
		out[stage] = count
	}
	return out
}

type investigationStageSinkKey struct{}

// WithInvestigationStageSink installs sink on ctx for one Investigate call. A
// nil sink leaves ctx unchanged.
func WithInvestigationStageSink(ctx context.Context, sink *InvestigationStageSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, investigationStageSinkKey{}, sink)
}

func investigationStageSinkFrom(ctx context.Context) *InvestigationStageSink {
	sink, _ := ctx.Value(investigationStageSinkKey{}).(*InvestigationStageSink)
	return sink
}

// startInvestigationStage reports that the engine entered stage.
func startInvestigationStage(ctx context.Context, stage contractsv1.ContextFabricInvestigationStage) {
	if sink := investigationStageSinkFrom(ctx); sink != nil {
		sink.start(stage)
	}
}

// completeInvestigationStage reports that stage finished without an error.
func completeInvestigationStage(ctx context.Context, stage contractsv1.ContextFabricInvestigationStage) {
	if sink := investigationStageSinkFrom(ctx); sink != nil {
		sink.complete(stage)
	}
}

// completeOpenInvestigationStage reports that Investigate returns a served
// answer: the open stage completes, and the reader's stream ends with no
// stage open. Investigate calls it once, and only then.
func completeOpenInvestigationStage(ctx context.Context) {
	if sink := investigationStageSinkFrom(ctx); sink != nil {
		sink.completeOpen()
	}
}

func (s *InvestigationStageSink) start(stage contractsv1.ContextFabricInvestigationStage) {
	if stage.Order() == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The stream never goes back and never repeats a stage: a second
	// synthesis pass, a second finalization, or a stage reached on a path
	// that already passed it adds nothing.
	if s.lastStarted != "" && stage.Order() <= s.lastStarted.Order() {
		return
	}
	if s.open != "" {
		s.emit(s.open, contractsv1.ContextFabricStageCompleted)
	}
	s.emit(stage, contractsv1.ContextFabricStageStarted)
	s.open, s.lastStarted = stage, stage
}

func (s *InvestigationStageSink) complete(stage contractsv1.ContextFabricInvestigationStage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == "" || s.open != stage {
		return
	}
	s.emit(stage, contractsv1.ContextFabricStageCompleted)
	s.open = ""
}

func (s *InvestigationStageSink) completeOpen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open != "" {
		s.emit(s.open, contractsv1.ContextFabricStageCompleted)
		s.open = ""
	}
	// After an overflow the reader may have seen a stage start and not
	// complete; the held-back slot carries that completion. It is always
	// free: an ordinary send never takes the last slot, and this is the one
	// send that may.
	if s.delivered != "" {
		s.transitions <- InvestigationStageTransition{Stage: s.delivered, State: contractsv1.ContextFabricStageCompleted, At: time.Now()}
		// That completion was counted as dropped when the engine reported
		// it after the overflow; it reached the reader late, not never.
		if s.dropped[s.delivered]--; s.dropped[s.delivered] <= 0 {
			delete(s.dropped, s.delivered)
		}
		s.delivered = ""
	}
}

// emit sends one ordinary transition, or drops it once the sink has
// overflowed. The caller holds s.mu, and only emit and completeOpen send, so
// the length check below cannot be raced into a blocking send: the reader
// only ever shortens the buffer.
func (s *InvestigationStageSink) emit(stage contractsv1.ContextFabricInvestigationStage, state contractsv1.ContextFabricInvestigationStageState) {
	if !s.overflowed && len(s.transitions) >= cap(s.transitions)-1 {
		s.overflowed = true
	}
	if s.overflowed {
		s.dropped[stage]++
		return
	}
	s.transitions <- InvestigationStageTransition{Stage: stage, State: state, At: time.Now()}
	switch state {
	case contractsv1.ContextFabricStageStarted:
		s.delivered = stage
	case contractsv1.ContextFabricStageCompleted:
		s.delivered = ""
	}
}
