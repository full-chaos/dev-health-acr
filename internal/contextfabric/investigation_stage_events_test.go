package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// stageStep is one transition without its time, for exact comparison.
type stageStep struct {
	stage contractsv1.ContextFabricInvestigationStage
	state contractsv1.ContextFabricInvestigationStageState
}

func (s stageStep) String() string { return string(s.stage) + ":" + string(s.state) }

func startedStep(stage contractsv1.ContextFabricInvestigationStage) stageStep {
	return stageStep{stage, contractsv1.ContextFabricStageStarted}
}

func completedStep(stage contractsv1.ContextFabricInvestigationStage) stageStep {
	return stageStep{stage, contractsv1.ContextFabricStageCompleted}
}

// both is a started and a completed step of one stage.
func both(stage contractsv1.ContextFabricInvestigationStage) []stageStep {
	return []stageStep{startedStep(stage), completedStep(stage)}
}

func steps(parts ...[]stageStep) []stageStep {
	var out []stageStep
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

var (
	stInterpreting = contractsv1.ContextFabricStageInterpreting
	stResolving    = contractsv1.ContextFabricStageResolvingSubjects
	stReading      = contractsv1.ContextFabricStageReadingFacts
	stSynthesizing = contractsv1.ContextFabricStageSynthesizing
	stValidating   = contractsv1.ContextFabricStageValidating
)

// tickingClock is a clock that moves one millisecond per reading, so every
// transition has its own time.
func tickingClock() func() time.Time {
	var mu sync.Mutex
	at := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(time.Millisecond)
		return at
	}
}

func drainStageTransitions(sink *InvestigationStageSink) []InvestigationStageTransition {
	var out []InvestigationStageTransition
	for {
		select {
		case transition := <-sink.Transitions():
			out = append(out, transition)
		default:
			return out
		}
	}
}

func stepsOf(transitions []InvestigationStageTransition) []stageStep {
	out := make([]stageStep, 0, len(transitions))
	for _, transition := range transitions {
		out = append(out, stageStep{transition.Stage, transition.State})
	}
	return out
}

func sameSteps(got, want []stageStep) bool {
	return fmt.Sprint(got) == fmt.Sprint(want)
}

// stageStream builds the stream the route writes for one run -- accepted,
// one stage event per transition, the terminal -- through the contract's own
// types, and holds it to the contract's stream grammar. The error terminal
// names the last stage the sink started, the rule the route follows.
func stageStream(t *testing.T, start time.Time, transitions []InvestigationStageTransition, result *InvestigationResult) error {
	t.Helper()
	const requestID = "request_12345678"
	reserved := "result_12345678"
	seq := int64(0)
	next := func(kind contractsv1.ContextFabricInvestigationEventType) contractsv1.ContextFabricInvestigationEvent {
		seq++
		return contractsv1.ContextFabricInvestigationEvent{SchemaVersion: contractsv1.ContextFabricInvestigationEventSchema, Type: kind, RequestID: requestID, Seq: seq}
	}
	accepted := next(contractsv1.ContextFabricInvestigationEventAccepted)
	accepted.ReservedResultID, accepted.StartedAt, accepted.BudgetSeconds = reserved, &start, 30
	events := []contractsv1.ContextFabricInvestigationEvent{accepted}
	var lastStarted contractsv1.ContextFabricInvestigationStage
	for _, transition := range transitions {
		event := next(contractsv1.ContextFabricInvestigationEventStage)
		elapsed := transition.At.Sub(start).Milliseconds()
		event.Stage, event.State, event.ElapsedMS = transition.Stage, transition.State, &elapsed
		if transition.State == contractsv1.ContextFabricStageStarted {
			lastStarted = transition.Stage
		}
		events = append(events, event)
	}
	if result != nil {
		body, err := json.Marshal(*result)
		if err != nil {
			t.Fatal(err)
		}
		terminal := next(contractsv1.ContextFabricInvestigationEventResult)
		terminal.Result = body
		events = append(events, terminal)
	} else {
		terminal := next(contractsv1.ContextFabricInvestigationEventError)
		terminal.ReservedResultID, terminal.Stage = reserved, lastStarted
		terminal.Error = &contractsv1.ErrorEnvelope{SchemaVersion: contractsv1.ErrorSchema, RequestID: requestID, Error: contractsv1.ErrorDetail{
			Code: "internal_error", Message: "Context Fabric investigation failed", HTTPStatus: 500,
		}}
		events = append(events, terminal)
	}
	return contractsv1.ValidateContextFabricInvestigationEventStream(events)
}

type stageRun struct {
	result      InvestigationResult
	err         error
	panicked    bool
	transitions []InvestigationStageTransition
	start       time.Time
}

// runStageScenario runs one Investigate with a production-capacity sink and
// returns what the engine reported. A panic is recovered here, as the route's
// investigateRecovered does.
func runStageScenario(t *testing.T, engine *Engine, request InvestigationRequest, ctx context.Context) stageRun {
	t.Helper()
	clock := tickingClock()
	run := stageRun{start: clock()}
	sink := NewInvestigationStageSink(InvestigationStageSinkCapacity, clock, func(InvestigationStageTransition) {
		t.Error("a production-capacity sink dropped a transition")
	})
	func() {
		defer func() {
			if recover() != nil {
				run.panicked = true
			}
		}()
		run.result, run.err = engine.Investigate(WithInvestigationStageSink(ctx, sink), stageScenarioPrincipal(), request)
	}()
	run.transitions = drainStageTransitions(sink)
	return run
}

// TestStageEventOrderHoldsOnEveryExit is the fault list of the event stream
// (plan §5.1, I-S03's list, run here at the engine): every exit reports its
// stages in the contract's order, the stream the route would write from them
// passes the contract's grammar, and an error names the stage it ended in.
// Each case states the EXACT transitions, so a site that stopped reporting --
// or reported on the wrong side of its port call -- fails by name.
func TestStageEventOrderHoldsOnEveryExit(t *testing.T) {
	answered := steps(both(stInterpreting), both(stResolving), both(stReading), both(stSynthesizing), both(stValidating))
	cases := []struct {
		name      string
		faults    stageScenarioFaults
		timeout   time.Duration
		want      []stageStep
		wantErr   func(error) bool
		wantPanic bool
	}{
		{name: "answered", want: answered},
		{name: "early terminal, no subject resolved", faults: stageScenarioFaults{unresolved: true},
			want: steps(both(stInterpreting), both(stResolving), both(stValidating))},
		{name: "interpret error", faults: stageScenarioFaults{interpretErr: errors.New("model refused")},
			want: []stageStep{startedStep(stInterpreting)}, wantErr: func(err error) bool { return err != nil }},
		{name: "fact read error", faults: stageScenarioFaults{factErr: fmt.Errorf("%w: clickhouse down", ErrUnavailable)},
			want: steps(both(stInterpreting), both(stResolving), []stageStep{startedStep(stReading)}), wantErr: func(err error) bool { return errors.Is(err, ErrUnavailable) }},
		{name: "request deadline during the fact read", faults: stageScenarioFaults{factWaitsForDeadline: true}, timeout: 200 * time.Millisecond,
			want: steps(both(stInterpreting), both(stResolving), []stageStep{startedStep(stReading)}), wantErr: func(err error) bool { return errors.Is(err, context.DeadlineExceeded) }},
		{name: "model deadline", faults: stageScenarioFaults{synthErr: fmt.Errorf("%w: %w", ErrModelTimeout, context.DeadlineExceeded)},
			want: steps(both(stInterpreting), both(stResolving), both(stReading), []stageStep{startedStep(stSynthesizing)}), wantErr: func(err error) bool { return errors.Is(err, ErrModelTimeout) }},
		{name: "panic in synthesis", faults: stageScenarioFaults{synthPanic: true},
			want: steps(both(stInterpreting), both(stResolving), both(stReading), []stageStep{startedStep(stSynthesizing)}), wantPanic: true},
		{name: "save error", faults: stageScenarioFaults{saveErr: errStageScenarioSave},
			want: steps(both(stInterpreting), both(stResolving), both(stReading), both(stSynthesizing), []stageStep{startedStep(stValidating)}), wantErr: func(err error) bool { return errors.Is(err, errStageScenarioSave) }},
		{name: "early terminal, save error", faults: stageScenarioFaults{unresolved: true, saveErr: errStageScenarioSave},
			want: steps(both(stInterpreting), both(stResolving), []stageStep{startedStep(stValidating)}), wantErr: func(err error) bool { return errors.Is(err, errStageScenarioSave) }},
		{name: "org purged at save", faults: stageScenarioFaults{saveErr: storage.ErrOrgPurged},
			want: steps(both(stInterpreting), both(stResolving), both(stReading), both(stSynthesizing), []stageStep{startedStep(stValidating)}), wantErr: func(err error) bool { return errors.Is(err, storage.ErrOrgPurged) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			engine, _ := stageScenarioEngine(t, test.faults)
			ctx := context.Background()
			if test.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.timeout)
				defer cancel()
			}
			run := runStageScenario(t, engine, validInvestigationRequest(), ctx)
			got := stepsOf(run.transitions)
			if !sameSteps(got, test.want) {
				t.Fatalf("transitions %v, want %v", got, test.want)
			}
			if run.panicked != test.wantPanic {
				t.Fatalf("panicked = %t, want %t", run.panicked, test.wantPanic)
			}
			switch {
			case test.wantPanic:
			case test.wantErr != nil:
				if run.err == nil || !test.wantErr(run.err) {
					t.Fatalf("Investigate error = %v, not this case's fault", run.err)
				}
			default:
				if run.err != nil {
					t.Fatalf("Investigate error = %v", run.err)
				}
			}
			var result *InvestigationResult
			if run.err == nil && !run.panicked {
				result = &run.result
			}
			if err := stageStream(t, run.start, run.transitions, result); err != nil {
				t.Fatalf("the stream built from these transitions breaks the contract grammar: %v", err)
			}
		})
	}
}

// TestStageEventOrderHoldsOnABudgetRefusal is the engine's planned refusal
// (CHAOS-4636 stage 3): the answer did not fit after one re-synthesis. The
// second synthesis pass reports nothing new.
func TestStageEventOrderHoldsOnABudgetRefusal(t *testing.T) {
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(4), 20, budgetStageOptions(4, time.Second), &calls)
	run := runStageScenario(t, engine, validInvestigationRequestWithConfirmedWindow(), context.Background())
	var refusal AnswerBudgetRefusal
	if !errors.As(run.err, &refusal) {
		t.Fatalf("Investigate error = %v, want a budget refusal", run.err)
	}
	if calls < 2 {
		t.Fatalf("synthesizer ran %d times; the fixture must reach the re-synthesis for this case to mean anything", calls)
	}
	got := stepsOf(run.transitions)
	want := steps(both(stInterpreting), both(stResolving), both(stReading), both(stSynthesizing))
	if !sameSteps(got, want) {
		t.Fatalf("transitions %v, want %v", got, want)
	}
	if err := stageStream(t, run.start, run.transitions, nil); err != nil {
		t.Fatalf("grammar: %v", err)
	}
}

// TestASinkChangesNothingInvestigateServes holds the answer served WITH a sink
// byte-equal to the golden recorded before the sites existed, the same golden
// the no-sink run is held to.
func TestASinkChangesNothingInvestigateServes(t *testing.T) {
	for stem, faults := range stageScenarioAnswers {
		t.Run(stem, func(t *testing.T) {
			engine, _ := stageScenarioEngine(t, faults)
			run := runStageScenario(t, engine, validInvestigationRequest(), context.Background())
			if run.err != nil {
				t.Fatal(run.err)
			}
			if len(run.transitions) == 0 {
				t.Fatal("the sink received nothing; the comparison would be vacuous")
			}
			want, err := os.ReadFile(stageScenarioGoldenPath(stem))
			if err != nil {
				t.Fatal(err)
			}
			if got := encodeStageScenarioAnswer(t, run.result); !bytes.Equal(got, want) {
				t.Fatalf("%s: the answer served with a sink differs from the golden", stem)
			}
		})
	}
}

// TestAStalledReaderNeverBlocksTheEngine: nothing reads the sink. With a
// one-slot buffer the engine must still finish, keep the first transition,
// and drop and count every later one -- the drop is the mechanism that keeps
// the engine moving, so it is asserted exactly, not as "some".
func TestAStalledReaderNeverBlocksTheEngine(t *testing.T) {
	engine, _ := stageScenarioEngine(t, stageScenarioFaults{})
	var mu sync.Mutex
	var dropped []stageStep
	sink := NewInvestigationStageSink(1, tickingClock(), func(transition InvestigationStageTransition) {
		mu.Lock()
		defer mu.Unlock()
		dropped = append(dropped, stageStep{transition.Stage, transition.State})
	})
	done := make(chan error, 1)
	go func() {
		_, err := engine.Investigate(WithInvestigationStageSink(context.Background(), sink), stageScenarioPrincipal(), validInvestigationRequest())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Investigate error = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Investigate did not return while the sink was full: the engine waited on its reader")
	}
	buffered := stepsOf(drainStageTransitions(sink))
	if !sameSteps(buffered, []stageStep{startedStep(stInterpreting)}) {
		t.Fatalf("buffered %v, want the first transition only", buffered)
	}
	mu.Lock()
	defer mu.Unlock()
	wantDropped := steps([]stageStep{completedStep(stInterpreting)}, both(stResolving), both(stReading), both(stSynthesizing), both(stValidating))
	if !sameSteps(dropped, wantDropped) {
		t.Fatalf("dropped %v, want %v", dropped, wantDropped)
	}
	if sink.Dropped() != int64(len(wantDropped)) {
		t.Fatalf("Dropped() = %d, want %d", sink.Dropped(), len(wantDropped))
	}
}

// TestAProductionSinkHoldsAWholeRun: a run makes at most two transitions per
// stage, so a production sink with no reader at all drops nothing.
func TestAProductionSinkHoldsAWholeRun(t *testing.T) {
	if max := 2 * len(contractsv1.ContextFabricInvestigationStages()); InvestigationStageSinkCapacity < max {
		t.Fatalf("capacity %d is below the %d transitions one run can make", InvestigationStageSinkCapacity, max)
	}
	engine, _ := stageScenarioEngine(t, stageScenarioFaults{})
	sink := NewInvestigationStageSink(InvestigationStageSinkCapacity, tickingClock(), nil)
	if _, err := engine.Investigate(WithInvestigationStageSink(context.Background(), sink), stageScenarioPrincipal(), validInvestigationRequest()); err != nil {
		t.Fatal(err)
	}
	if sink.Dropped() != 0 || len(drainStageTransitions(sink)) != 10 {
		t.Fatalf("a production sink dropped %d of a whole run", sink.Dropped())
	}
}

// TestTheSinkKeepsTheStreamGrammar drives the sink directly with the
// out-of-order calls real paths make (a skipped stage, a repeat, a stage
// reached after a later one, a completion of a stage that is not open) and
// holds the result to the contract's grammar.
func TestTheSinkKeepsTheStreamGrammar(t *testing.T) {
	clock := tickingClock()
	start := clock()
	sink := NewInvestigationStageSink(InvestigationStageSinkCapacity, clock, nil)
	ctx := WithInvestigationStageSink(context.Background(), sink)

	startInvestigationStage(ctx, contractsv1.ContextFabricInvestigationStage("graph")) // outside the vocabulary, first: ignored
	startInvestigationStage(ctx, stResolving)                                          // skips interpreting
	startInvestigationStage(ctx, stInterpreting)                                       // earlier than one started: ignored
	completeInvestigationStage(ctx, stReading)                                         // not open: ignored
	startInvestigationStage(ctx, stSynthesizing)                                       // resolving still open: completed first
	startInvestigationStage(ctx, stSynthesizing)                                       // repeat: ignored
	completeInvestigationStage(ctx, stSynthesizing)
	completeOpenInvestigationStage(ctx) // nothing open: no-op
	startInvestigationStage(ctx, stValidating)
	completeOpenInvestigationStage(ctx)
	startInvestigationStage(ctx, stReading) // after the last stage: ignored

	transitions := drainStageTransitions(sink)
	want := steps([]stageStep{startedStep(stResolving), completedStep(stResolving)}, both(stSynthesizing), both(stValidating))
	if got := stepsOf(transitions); !sameSteps(got, want) {
		t.Fatalf("transitions %v, want %v", got, want)
	}
	result := validInvestigationResult()
	result.GeneratedAt = start
	if err := stageStream(t, start, transitions, &result); err != nil {
		t.Fatalf("grammar: %v", err)
	}
	for index := 1; index < len(transitions); index++ {
		if !transitions[index].At.After(transitions[index-1].At) {
			t.Fatal("transitions are not stamped in order")
		}
	}
}

// TestNoSinkIsANoOp: without a sink every report is a no-op, and a nil sink
// installs nothing.
func TestNoSinkIsANoOp(t *testing.T) {
	ctx := context.Background()
	if WithInvestigationStageSink(ctx, nil) != ctx {
		t.Fatal("a nil sink changed the context")
	}
	startInvestigationStage(ctx, stInterpreting)
	completeInvestigationStage(ctx, stInterpreting)
	completeOpenInvestigationStage(ctx)
	if investigationStageSinkFrom(ctx) != nil {
		t.Fatal("a sink appeared on a context that never had one")
	}
}
