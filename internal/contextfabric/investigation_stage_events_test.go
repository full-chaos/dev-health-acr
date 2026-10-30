package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
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

// lastStartedStep is the last stage a step list starts ("" for none).
func lastStartedStep(list []stageStep) contractsv1.ContextFabricInvestigationStage {
	var stage contractsv1.ContextFabricInvestigationStage
	for _, step := range list {
		if step.state == contractsv1.ContextFabricStageStarted {
			stage = step.stage
		}
	}
	return stage
}

func sameSteps(got, want []stageStep) bool {
	return fmt.Sprint(got) == fmt.Sprint(want)
}

// stageStream builds the stream the route writes for one run -- accepted,
// one stage event per transition, the terminal -- through the contract's own
// types, and holds it to the contract's stream grammar. errorStage is the
// stage the error terminal names, given by the caller from what it expects
// (the exit's own fault), not taken from the transitions under test, so a
// transition list that ends in the wrong stage fails the grammar.
func stageStream(t *testing.T, start time.Time, transitions []InvestigationStageTransition, result *InvestigationResult, errorStage contractsv1.ContextFabricInvestigationStage) error {
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
	for _, transition := range transitions {
		event := next(contractsv1.ContextFabricInvestigationEventStage)
		elapsed := transition.At.Sub(start).Milliseconds()
		event.Stage, event.State, event.ElapsedMS = transition.Stage, transition.State, &elapsed
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
		terminal.ReservedResultID, terminal.Stage = reserved, errorStage
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
	run, sink := runStageScenarioWithCapacity(t, engine, request, ctx, InvestigationStageSinkCapacity)
	if sink.Dropped() != 0 {
		t.Errorf("a production-capacity sink dropped %d transitions", sink.Dropped())
	}
	return run
}

// runStageScenarioWithCapacity is runStageScenario with a sink of capacity
// slots and nothing reading it while Investigate runs.
func runStageScenarioWithCapacity(t *testing.T, engine *Engine, request InvestigationRequest, ctx context.Context, capacity int) (stageRun, *InvestigationStageSink) {
	t.Helper()
	run := stageRun{start: time.Now()}
	sink := NewInvestigationStageSink(capacity)
	func() {
		defer func() {
			if recover() != nil {
				run.panicked = true
			}
		}()
		run.result, run.err = engine.Investigate(WithInvestigationStageSink(ctx, sink), stageScenarioPrincipal(), request)
	}()
	run.transitions = drainStageTransitions(sink)
	return run, sink
}

// TestStageEventOrderHoldsOnEveryExit is the fault list of the event stream
// (plan §5.1, I-S03's list, run here at the engine): every exit reports its
// stages in the contract's order, the stream the route would write from them
// passes the contract's grammar, and an error names the stage it ended in.
// Each case states the EXACT transitions, so a site that stopped reporting --
// or reported on the wrong side of its port call -- fails by name.
// stageExitCase is one exit of the fault list: its faults and the EXACT
// transitions a production sink receives.
type stageExitCase struct {
	name      string
	faults    stageScenarioFaults
	timeout   time.Duration
	want      []stageStep
	wantErr   func(error) bool
	wantPanic bool
}

func stageExitCases() []stageExitCase {
	answered := steps(both(stInterpreting), both(stResolving), both(stReading), both(stSynthesizing), both(stValidating))
	return []stageExitCase{
		{name: "answered", want: answered},
		{name: "early terminal, no subject resolved", faults: stageScenarioFaults{unresolved: true},
			want: steps(both(stInterpreting), both(stResolving), both(stValidating))},
		{name: "interpret error", faults: stageScenarioFaults{interpretErr: errors.New("model refused")},
			want: []stageStep{startedStep(stInterpreting)}, wantErr: func(err error) bool { return err != nil }},
		{name: "resolution error", faults: stageScenarioFaults{resolveErr: fmt.Errorf("%w: falkordb down", ErrUnavailable)},
			want: steps(both(stInterpreting), []stageStep{startedStep(stResolving)}), wantErr: func(err error) bool { return errors.Is(err, ErrUnavailable) }},
		{name: "early terminal over the engine byte ceiling", faults: stageScenarioFaults{unresolved: true, maxSerializedBytes: 512},
			want: steps(both(stInterpreting), both(stResolving), []stageStep{startedStep(stValidating)}), wantErr: func(err error) bool { return err != nil }},
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
}

func TestStageEventOrderHoldsOnEveryExit(t *testing.T) {
	cases := stageExitCases()
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
			if err := stageStream(t, run.start, run.transitions, result, lastStartedStep(test.want)); err != nil {
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
	// The refusal is decided inside synthesis (after the re-synthesis), so
	// synthesizing is still open and the error names it.
	got := stepsOf(run.transitions)
	want := steps(both(stInterpreting), both(stResolving), both(stReading), []stageStep{startedStep(stSynthesizing)})
	if !sameSteps(got, want) {
		t.Fatalf("transitions %v, want %v", got, want)
	}
	if err := stageStream(t, run.start, run.transitions, nil, stSynthesizing); err != nil {
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

// TestAStalledReaderNeverBlocksTheEngine: nothing reads the sink, and the
// sink has the smallest capacity (two slots, one held back). The engine must
// still finish, the sink must overflow after the first transition and count
// every later one by stage, and the held-back slot must complete the stage the
// reader saw start, so the reader's stream with the answer is valid. Nothing
// the caller supplies runs on the engine's goroutine when a transition is
// dropped (the sink takes no drop callback).
func TestAStalledReaderNeverBlocksTheEngine(t *testing.T) {
	engine, _ := stageScenarioEngine(t, stageScenarioFaults{})
	start := time.Now()
	sink := NewInvestigationStageSink(1) // below the minimum: two slots
	type outcome struct {
		result InvestigationResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := engine.Investigate(WithInvestigationStageSink(context.Background(), sink), stageScenarioPrincipal(), validInvestigationRequest())
		done <- outcome{result, err}
	}()
	var run outcome
	select {
	case run = <-done:
		if run.err != nil {
			t.Fatalf("Investigate error = %v", run.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Investigate did not return while the sink was full: the engine waited on its reader")
	}
	transitions := drainStageTransitions(sink)
	if got, want := stepsOf(transitions), both(stInterpreting); !sameSteps(got, want) {
		t.Fatalf("received %v, want %v (the first transition, then the held-back completion)", got, want)
	}
	// interpreting's completion reached the reader late, in the held-back
	// slot: it is not counted as lost.
	wantDropped := map[contractsv1.ContextFabricInvestigationStage]int64{stResolving: 2, stReading: 2, stSynthesizing: 2, stValidating: 2}
	if got := sink.DroppedByStage(); fmt.Sprint(got) != fmt.Sprint(wantDropped) {
		t.Fatalf("dropped by stage %v, want %v", got, wantDropped)
	}
	if sink.Dropped() != 8 {
		t.Fatalf("Dropped() = %d, want 8", sink.Dropped())
	}
	if err := stageStream(t, start, transitions, &run.result, ""); err != nil {
		t.Fatalf("the reader's stream with the answer breaks the grammar: %v", err)
	}
}

// TestEveryExitStaysValidWhenTheSinkOverflows runs every exit of the fault
// list with every capacity from the minimum to one that holds the whole run,
// nothing reading. For each: the reader receives a prefix of the exit's
// production transitions -- plus, on a served answer only, the held-back
// completion of the stage the prefix leaves open -- the drop count is exactly
// what the prefix lacks, and the stream the route builds with the exit's
// terminal passes the contract's grammar.
func TestEveryExitStaysValidWhenTheSinkOverflows(t *testing.T) {
	isPrefix := func(got, of []stageStep) bool { return len(got) <= len(of) && sameSteps(got, of[:len(got)]) }
	overflowed := 0
	for _, test := range stageExitCases() {
		for capacity := 2; capacity <= 12; capacity++ {
			t.Run(fmt.Sprintf("%s/capacity=%d", test.name, capacity), func(t *testing.T) {
				engine, _ := stageScenarioEngine(t, test.faults)
				ctx := context.Background()
				if test.timeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, test.timeout)
					defer cancel()
				}
				run, sink := runStageScenarioWithCapacity(t, engine, validInvestigationRequest(), ctx, capacity)
				served := run.err == nil && !run.panicked
				got := stepsOf(run.transitions)
				prefix := got
				if !isPrefix(got, test.want) {
					if !served || len(got) < 2 {
						t.Fatalf("received %v: not a prefix of %v, and only a served answer may add the held-back completion", got, test.want)
					}
					prefix = got[:len(got)-1]
					last, opened := got[len(got)-1], prefix[len(prefix)-1]
					if !isPrefix(prefix, test.want) || last.state != contractsv1.ContextFabricStageCompleted || opened != startedStep(last.stage) {
						t.Fatalf("received %v: the last step is not the completion of the stage the prefix left open", got)
					}
				}
				// Every transition the reader did not get is counted, and only
				// those: a held-back completion stands in for its dropped one.
				if want := int64(len(test.want) - len(got)); sink.Dropped() != want {
					t.Fatalf("Dropped() = %d, want %d (the production run has %d transitions, the reader got %d)", sink.Dropped(), want, len(test.want), len(got))
				}
				if len(prefix) < len(test.want) {
					overflowed++
				}
				var result *InvestigationResult
				if served {
					result = &run.result
				}
				// The route names the last stage its stream started, so after an
				// overflow that is the last stage of the received prefix.
				if err := stageStream(t, run.start, run.transitions, result, lastStartedStep(prefix)); err != nil {
					t.Fatalf("received %v: the stream breaks the contract grammar: %v", got, err)
				}
			})
		}
	}
	if overflowed == 0 {
		t.Fatal("no run overflowed its sink: the property was never exercised")
	}
}

// TestAProductionSinkHoldsAWholeRun: a run makes at most two transitions per
// stage, so a production sink with no reader at all drops nothing.
func TestAProductionSinkHoldsAWholeRun(t *testing.T) {
	if max := 2 * len(contractsv1.ContextFabricInvestigationStages()); InvestigationStageSinkCapacity < max {
		t.Fatalf("capacity %d is below the %d transitions one run can make", InvestigationStageSinkCapacity, max)
	}
	engine, _ := stageScenarioEngine(t, stageScenarioFaults{})
	sink := NewInvestigationStageSink(InvestigationStageSinkCapacity)
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
	start := time.Now()
	sink := NewInvestigationStageSink(InvestigationStageSinkCapacity)
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
	if err := stageStream(t, start, transitions, &result, ""); err != nil {
		t.Fatalf("grammar: %v", err)
	}
	for index := 1; index < len(transitions); index++ {
		if transitions[index].At.Before(transitions[index-1].At) {
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

// TestAnOverflowedSinkSendsNothingMore: once the sink overflowed, it sends no
// ordinary transition again, even after the reader caught up. A sink that
// resumed would hand the reader a gap (a stage starting while the one it
// last saw is still open), which no consumer can repair; a prefix needs no
// repair. The held-back completion then closes the stage the reader saw.
func TestAnOverflowedSinkSendsNothingMore(t *testing.T) {
	start := time.Now()
	sink := NewInvestigationStageSink(2)
	ctx := WithInvestigationStageSink(context.Background(), sink)
	startInvestigationStage(ctx, stInterpreting)    // sent: the buffer now holds one, the held-back slot is left
	completeInvestigationStage(ctx, stInterpreting) // overflow: dropped
	received := drainStageTransitions(sink)         // the reader catches up
	startInvestigationStage(ctx, stResolving)       // still dropped
	completeInvestigationStage(ctx, stResolving)    // still dropped
	startInvestigationStage(ctx, stSynthesizing)    // still dropped
	completeOpenInvestigationStage(ctx)             // the served answer: the held-back completion of interpreting
	received = append(received, drainStageTransitions(sink)...)
	if got, want := stepsOf(received), both(stInterpreting); !sameSteps(got, want) {
		t.Fatalf("received %v, want %v", got, want)
	}
	if sink.Dropped() != 4 {
		t.Fatalf("Dropped() = %d, want 4 (resolving twice, synthesizing twice; interpreting's completion arrived late)", sink.Dropped())
	}
	result := validInvestigationResult()
	result.GeneratedAt = start
	if err := stageStream(t, start, received, &result, ""); err != nil {
		t.Fatalf("grammar: %v", err)
	}
}

// TestTheStreamHelperCanFail (round-2 F4): stageStream is the check every
// order test leans on, so it must refuse a wrong stream. An error terminal
// that names a stage other than the last one started, and a result with a
// stage still open, both fail it.
func TestTheStreamHelperCanFail(t *testing.T) {
	start := time.Now()
	at := func(ms int) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
	transitions := []InvestigationStageTransition{
		{Stage: stInterpreting, State: contractsv1.ContextFabricStageStarted, At: at(1)},
		{Stage: stInterpreting, State: contractsv1.ContextFabricStageCompleted, At: at(2)},
		{Stage: stResolving, State: contractsv1.ContextFabricStageStarted, At: at(3)},
	}
	if err := stageStream(t, start, transitions, nil, stResolving); err != nil {
		t.Fatalf("control: the right error stage was refused: %v", err)
	}
	if err := stageStream(t, start, transitions, nil, stInterpreting); err == nil {
		t.Fatal("an error naming a stage other than the last one started passed")
	}
	result := validInvestigationResult()
	result.GeneratedAt = start
	if err := stageStream(t, start, transitions, &result, ""); err == nil {
		t.Fatal("a result with a stage still open passed")
	}
}

// TestTheSinkTakesNoCallerFunction (round-2 F3): nothing a caller supplies
// may run on the engine's goroutine, so the sink's constructor takes no
// function at all -- no drop callback (round 1), no clock (round 2).
func TestTheSinkTakesNoCallerFunction(t *testing.T) {
	constructor := reflect.TypeOf(NewInvestigationStageSink)
	for index := 0; index < constructor.NumIn(); index++ {
		if constructor.In(index).Kind() == reflect.Func {
			t.Fatalf("NewInvestigationStageSink parameter %d is a function (%s): it would run on the engine's goroutine", index, constructor.In(index))
		}
	}
	sinkType := reflect.TypeOf(InvestigationStageSink{})
	for index := 0; index < sinkType.NumField(); index++ {
		if sinkType.Field(index).Type.Kind() == reflect.Func {
			t.Fatalf("InvestigationStageSink holds a function field %q", sinkType.Field(index).Name)
		}
	}
}

// TestSynthesisStaysOpenThroughTheBudgetRetry (round-3 finding): the stage-3
// budget re-synthesis is still synthesis, so when it starts the reader must
// not have been told synthesizing completed. The synthesizer reads what the
// sink has delivered at the moment the second pass begins.
func TestSynthesisStaysOpenThroughTheBudgetRetry(t *testing.T) {
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(4), 20, budgetStageOptions(4, time.Second), &calls)
	inner := engine.synthesizer
	var atRetry []stageStep
	retried := false
	engine.synthesizer = synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		if calls == 1 {
			retried = true
			atRetry = stepsOf(drainStageTransitions(investigationStageSinkFrom(ctx)))
		}
		return inner.Synthesize(ctx, principal, input)
	})
	sink := NewInvestigationStageSink(InvestigationStageSinkCapacity)
	if _, err := engine.Investigate(WithInvestigationStageSink(context.Background(), sink), stageScenarioPrincipal(), validInvestigationRequestWithConfirmedWindow()); err == nil {
		t.Fatal("the fixture must end in the budget refusal after one re-synthesis")
	}
	if !retried {
		t.Fatal("the re-synthesis never ran; the test would mean nothing")
	}
	want := steps(both(stInterpreting), both(stResolving), both(stReading), []stageStep{startedStep(stSynthesizing)})
	if !sameSteps(atRetry, want) {
		t.Fatalf("delivered when the re-synthesis began %v, want %v (synthesizing still open)", atRetry, want)
	}
}
