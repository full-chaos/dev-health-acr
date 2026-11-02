package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// budgetRun is one Investigate call under a chosen recheck budget, with what the
// fresh path saw of its context.
type budgetRun struct {
	recheckRun
	freshDeadline    time.Time
	freshHasDeadline bool
	freshCtxErr      error
}

// runBudgeted runs the real Engine.Investigate -> tryReuse path under budget.
func runBudgeted(t *testing.T, ctx context.Context, graph GraphReader, candidate InvestigationResult, budget time.Duration) *budgetRun {
	t.Helper()
	return runBudgetedWith(t, ctx, graph, candidate, budget, nil)
}

// runBudgetedWith is runBudgeted with the recheck's budget context made by
// budgetContext, when it is not nil.
func runBudgetedWith(t *testing.T, ctx context.Context, graph GraphReader, candidate InvestigationResult, budget time.Duration, budgetContext func(context.Context, time.Duration) (context.Context, context.CancelFunc)) *budgetRun {
	t.Helper()
	run := &budgetRun{recheckRun: recheckRun{telemetry: &recordingTelemetry{}}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(ctx context.Context, _ storage.Principal, _ InvestigationRequest) (InterpretedQuestion, error) {
			run.modelCalls++
			run.freshDeadline, run.freshHasDeadline = ctx.Deadline()
			run.freshCtxErr = ctx.Err()
			return InterpretedQuestion{}, errors.New("fresh investigation reached")
		}),
		Graph: graph, Facts: failingFactReader{t: t}, Results: &resultStoreStub{}, Telemetry: run.telemetry,
		ReuseGate: optionalOwnerReuseGate{candidate: candidate},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached")
			return InvestigationResult{}, nil
		}),
	}, EngineOptions{
		ServiceVersion: "acr-test", NewResultID: func() string { return freshTestResultID }, ReuseRecheckBudget: budget,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	if budgetContext != nil {
		engine.recheckContext = budgetContext
	}
	run.result, run.err = engine.Investigate(ctx, ownerPrincipal("user_a", "acme/tools"), validInvestigationRequest())
	return run
}

// citedCandidate is a stored answer over subjects that cites one ref, so the
// recheck reaches discovery, and the graph that proves it through the last
// subject.
func citedCandidate(subjects []SubjectRef) (InvestigationResult, *recheckGraph) {
	candidate := recheckCandidate(subjects)
	candidate.EvidenceRefIDs = []string{evidenceRef(0)}
	return candidate, &recheckGraph{pathRefs: map[string]string{subjects[len(subjects)-1].CanonicalID: evidenceRef(0)}}
}

// teamCandidate is a stored answer over team subjects that cites nothing, so its
// one read past the resolver is the removed-team read.
func teamCandidate(count int) InvestigationResult {
	subjects := make([]SubjectRef, 0, count)
	for index := range count {
		subjects = append(subjects, SubjectRef{Kind: SubjectTeam, CanonicalID: fmt.Sprintf("team_%03d", index), Label: fmt.Sprintf("Team %03d", index)})
	}
	return recheckCandidate(subjects)
}

// TestARecheckPastItsBudgetIsANamedMiss (CHAOS-12511): a read cut off by the
// recheck's own budget reads as the deadline miss, whatever that read reported:
// its context's error, a socket's own timeout, or a partial result with no error
// at all. Time says nothing about a grant, so it must never read as a refusal.
// The fresh path then runs once, on the request's context, with its deadline
// unchanged. Nothing of the expired recheck reaches an answer.
func TestARecheckPastItsBudgetIsANamedMiss(t *testing.T) {
	t.Parallel()
	const budget = 30 * time.Millisecond
	socketTimeout := errors.New("read tcp 10.0.0.1:6379: i/o timeout")
	for _, tc := range []struct {
		name     string
		subjects int
		build    func() (InvestigationResult, *recheckGraph)
	}{
		{"a resolution hangs", 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangResolveOn = 2
			return candidate, graph
		}},
		{"a discovery in the last batch hangs", 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangDiscoverOn = 2
			return candidate, graph
		}},
		{"the removed-team read hangs", 54, func() (InvestigationResult, *recheckGraph) {
			return teamCandidate(54), &recheckGraph{hangRemoved: true}
		}},
		{"a resolution error arrives after expiry", 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangResolveOn, graph.afterHang = 2, socketTimeout
			return candidate, graph
		}},
		{"a discovery error arrives after expiry", 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangDiscoverOn, graph.afterHang = 1, socketTimeout
			return candidate, graph
		}},
		{"a removed-team error arrives after expiry", 54, func() (InvestigationResult, *recheckGraph) {
			return teamCandidate(54), &recheckGraph{hangRemoved: true, afterHang: socketTimeout}
		}},
		{"a resolution comes back partial after expiry", 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangResolveOn, graph.hangPartial = 2, true
			return candidate, graph
		}},
		{"a discovery comes back partial after expiry", 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangDiscoverOn, graph.hangPartial = 2, true
			return candidate, graph
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			parentDeadline, _ := parent.Deadline()
			candidate, graph := tc.build()
			run := runBudgeted(t, parent, graph, candidate, budget)
			run.requireMiss(t, AnswerReuseMissRecheckDeadline, AnswerReuseRefusal{})
			if run.err == nil || run.result.Reused {
				t.Fatalf("reused %t err %v: a stored answer must not come out of an expired recheck", run.result.Reused, run.err)
			}
			if got := run.telemetry.answerReuseRechecks[0]; !got.Ran || got.Subjects != tc.subjects {
				t.Fatalf("recheck = %+v, the line must keep its measurement on expiry", got)
			}
			if !run.freshHasDeadline || !run.freshDeadline.Equal(parentDeadline) {
				t.Fatalf("the fresh path's deadline = %v (has %t), want the request's %v unchanged", run.freshDeadline, run.freshHasDeadline, parentDeadline)
			}
			if run.freshCtxErr != nil {
				t.Fatalf("the fresh path ran on an ended context: %v", run.freshCtxErr)
			}
			if len(run.telemetry.answerReuseContainment) != 0 {
				t.Fatalf("containment = %+v: an expired recheck proves nothing, so it measures nothing", run.telemetry.answerReuseContainment)
			}
		})
	}
}

// TestARecheckHasOneBudgetAcrossItsBatches (CHAOS-12511): every read of the
// recheck runs under one derived deadline, so three batches share it. Checked two
// ways: the deadline each read saw is the same value, and reads that each fit the
// budget still spend it together.
func TestARecheckHasOneBudgetAcrossItsBatches(t *testing.T) {
	t.Parallel()
	t.Run("every read sees the same deadline", func(t *testing.T) {
		t.Parallel()
		candidate, graph := citedCandidate(reuseSubjects(120))
		run := runBudgeted(t, context.Background(), graph, candidate, time.Hour)
		run.requireHit(t)
		if len(graph.deadlines) != 6 {
			t.Fatalf("reads = %d, want 6", len(graph.deadlines))
		}
		for index, deadline := range graph.deadlines {
			if deadline.IsZero() {
				t.Fatalf("read %d carried no deadline", index)
			}
			if !deadline.Equal(graph.deadlines[0]) {
				t.Fatalf("read %d deadline %v differs from the first %v: that is a budget per read", index, deadline, graph.deadlines[0])
			}
		}
	})
	t.Run("the removed-team read shares it", func(t *testing.T) {
		t.Parallel()
		graph := &recheckGraph{}
		run := runBudgeted(t, context.Background(), graph, teamCandidate(54), time.Hour)
		run.requireHit(t)
		if len(graph.deadlines) != 3 || graph.deadlines[2].IsZero() || !graph.deadlines[2].Equal(graph.deadlines[0]) {
			t.Fatalf("deadlines = %v, want 3 equal ones: two resolutions and the removed-team read", graph.deadlines)
		}
	})
	t.Run("reads that each fit spend it together", func(t *testing.T) {
		t.Parallel()
		candidate, graph := citedCandidate(reuseSubjects(120))
		graph.readDelay = 80 * time.Millisecond
		run := runBudgeted(t, context.Background(), graph, candidate, 200*time.Millisecond)
		run.requireMiss(t, AnswerReuseMissRecheckDeadline, AnswerReuseRefusal{})
		if reads := len(graph.deadlines); reads >= 6 {
			t.Fatalf("reads = %d: all of them ran, so nothing shared the budget", reads)
		}
	})
}

// TestARealErrorWithBothContextsAliveKeepsItsOutcome (CHAOS-12511): a read that
// fails on its own, with the budget and the request both running, is the failure
// it always was.
func TestARealErrorWithBothContextsAliveKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	failed := errors.New("graph read failed")
	for _, tc := range []struct {
		name    string
		build   func() (InvestigationResult, *recheckGraph)
		outcome AnswerReuseOutcome
		refusal AnswerReuseRefusal
	}{
		{"a resolution error", func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.resolveErrOn = 2
			return candidate, graph
		}, AnswerReuseMissAuthorization, AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectRecheckFailed}},
		{"a discovery error", func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.discoverErrOn = 2
			return candidate, graph
		}, AnswerReuseMissRecheckUnavailable, AnswerReuseRefusal{}},
		{"a removed-team error", func() (InvestigationResult, *recheckGraph) {
			return teamCandidate(54), &recheckGraph{removedErr: failed}
		}, AnswerReuseMissRecheckUnavailable, AnswerReuseRefusal{}},
		{"a subject not committed", func() (InvestigationResult, *recheckGraph) {
			subjects := reuseSubjects(54)
			candidate, graph := citedCandidate(subjects)
			graph.denied = map[string]struct{}{subjects[53].CanonicalID: {}}
			return candidate, graph
		}, AnswerReuseMissAuthorization, AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectNotCommitted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			candidate, graph := tc.build()
			runBudgeted(t, context.Background(), graph, candidate, time.Hour).requireMiss(t, tc.outcome, tc.refusal)
		})
	}
}

// TestTheCallersOwnEndIsANamedMiss (CHAOS-12511): the request's own context
// ending during the recheck says nothing about a grant or about the graph, so it
// is its own outcome at every read, whatever that read reported: its context's
// error, or a partial result with no error at all. When the budget has ended too,
// the request's end is named: on the real client a cancel does not interrupt a
// read in flight, so a read cancelled by the caller returns at the budget's
// deadline, and both have ended by then
// (falkorgraph.TestACancelDoesNotStopAReadInFlight).
func TestTheCallersOwnEndIsANamedMiss(t *testing.T) {
	t.Parallel()
	const hold, early, late = 80 * time.Millisecond, 10 * time.Millisecond, 40 * time.Millisecond
	for _, tc := range []struct {
		name     string
		parent   func(*recheckGraph) (context.Context, context.CancelFunc)
		budget   time.Duration
		subjects int
		build    func() (InvestigationResult, *recheckGraph)
	}{
		{"a cancellation during a resolution", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangResolveOn = 1
			return candidate, graph
		}},
		{"a cancellation during a later resolution", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangResolveOn = 2
			return candidate, graph
		}},
		{"a cancellation during a discovery", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangDiscoverOn = 1
			return candidate, graph
		}},
		{"a cancellation during the last discovery", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangDiscoverOn = 2
			return candidate, graph
		}},
		{"a cancellation during removed teams", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			return teamCandidate(54), &recheckGraph{hangRemoved: true}
		}},
		{"a resolution comes back partial after a cancellation", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangResolveOn, graph.hangPartial = 2, true
			return candidate, graph
		}},
		{"a discovery comes back partial after a cancellation", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangDiscoverOn, graph.hangPartial = 2, true
			return candidate, graph
		}},
		{"an error of its own comes back after a cancellation", cancelOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangDiscoverOn, graph.afterHang = 1, errors.New("read tcp 10.0.0.1:6379: i/o timeout")
			return candidate, graph
		}},
		{"the request's deadline ends first", deadlineOnHang, time.Hour, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.hangResolveOn = 1
			return candidate, graph
		}},
		{"both ended, the budget first", cancelOnReturn, early, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.holdFor = hold
			return candidate, graph
		}},
		{"both ended, the request first", cancelOnRead, late, 54, func() (InvestigationResult, *recheckGraph) {
			candidate, graph := citedCandidate(reuseSubjects(54))
			graph.holdFor = hold
			return candidate, graph
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			candidate, graph := tc.build()
			parent, cancel := tc.parent(graph)
			defer cancel()
			run := runBudgeted(t, parent, graph, candidate, tc.budget)
			if run.hit() || run.result.Reused {
				t.Fatal("a stored answer was served after the request ended")
			}
			telemetry := run.telemetry
			if len(telemetry.answerReuseOutcomes) != 1 || telemetry.answerReuseOutcomes[0] != AnswerReuseMissRecheckCancelled {
				t.Fatalf("outcomes = %v, want exactly [%s]", telemetry.answerReuseOutcomes, AnswerReuseMissRecheckCancelled)
			}
			if telemetry.answerReuseRefusals[0] != (AnswerReuseRefusal{}) {
				t.Fatalf("refusal = %+v, want none: a request's end says nothing about a grant", telemetry.answerReuseRefusals[0])
			}
			if got := telemetry.answerReuseRechecks[0]; !got.Ran || got.Subjects != tc.subjects {
				t.Fatalf("recheck = %+v, the line must keep its measurement", got)
			}
			if len(telemetry.answerReuseContainment) != 0 {
				t.Fatalf("containment = %+v: a recheck the request ended proves nothing", telemetry.answerReuseContainment)
			}
		})
	}
}

// The request ends inside the read the case is about, never on a timer: a timer
// can fire before the recheck starts, under CPU pressure (CHAOS-12573).

// cancelOnHang is a request its caller cancels when a read hangs.
func cancelOnHang(graph *recheckGraph) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	graph.onHang = cancel
	return ctx, cancel
}

// cancelOnRead is a request its caller cancels as a held read starts.
func cancelOnRead(graph *recheckGraph) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	graph.onRead = cancel
	return ctx, cancel
}

// cancelOnReturn is a request its caller cancels as a held read returns.
func cancelOnReturn(graph *recheckGraph) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	graph.onReturn = cancel
	return ctx, cancel
}

// deadlineOnHang is a request whose own deadline passes when a read hangs.
func deadlineOnHang(graph *recheckGraph) (context.Context, context.CancelFunc) {
	ctx := &requestDeadline{done: make(chan struct{})}
	var once sync.Once
	graph.onHang = func() { once.Do(func() { close(ctx.done) }) }
	return ctx, func() {}
}

// requestDeadline is a request context whose deadline the test passes:
// DeadlineExceeded once done is closed.
type requestDeadline struct{ done chan struct{} }

func (*requestDeadline) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *requestDeadline) Done() <-chan struct{}     { return c.done }
func (c *requestDeadline) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (*requestDeadline) Value(any) any { return nil }

// TestADeadlinePastWhoseTimerHasNotRunIsSpent (CHAOS-12573): a context whose
// deadline has passed by the clock is ended, even while its Err is still nil
// because its timer has not run, as on a starved scheduler. The recheck stops
// after the read it is in, with the named miss, and starts no other read.
func TestADeadlinePastWhoseTimerHasNotRunIsSpent(t *testing.T) {
	t.Parallel()
	candidate, graph := citedCandidate(reuseSubjects(54))
	run := runBudgetedWith(t, context.Background(), graph, candidate, time.Hour, func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		inner, cancel := context.WithCancel(ctx)
		return lateTimer{Context: inner, deadline: time.Now().Add(-time.Millisecond)}, cancel
	})
	run.requireMiss(t, AnswerReuseMissRecheckDeadline, AnswerReuseRefusal{})
	if len(graph.deadlines) != 1 {
		t.Fatalf("reads = %d, want 1: a read must not start past the deadline, whatever Err says", len(graph.deadlines))
	}
}

// TestARequestDeadlinePastWhoseTimerHasNotRunIsTheRequestsEnd (CHAOS-12573):
// the same for the request's own context. Its deadline has passed by the clock
// while its Err is still nil, so the recheck names the request's end, not the
// budget's, though the budget's context has ended with it.
func TestARequestDeadlinePastWhoseTimerHasNotRunIsTheRequestsEnd(t *testing.T) {
	t.Parallel()
	candidate, graph := citedCandidate(reuseSubjects(54))
	parent := lateTimer{Context: context.Background(), deadline: time.Now().Add(-time.Millisecond)}
	run := runBudgeted(t, parent, graph, candidate, time.Hour)
	if run.result.Reused {
		t.Fatal("a stored answer was served after the request's deadline")
	}
	if got := run.telemetry.answerReuseOutcomes; len(got) != 1 || got[0] != AnswerReuseMissRecheckCancelled {
		t.Fatalf("outcomes = %v, want exactly [%s]", got, AnswerReuseMissRecheckCancelled)
	}
}

// lateTimer is a context whose deadline has passed and whose Err is nil.
type lateTimer struct {
	context.Context
	deadline time.Time
}

func (c lateTimer) Deadline() (time.Time, bool) { return c.deadline, true }

// TestARecheckJustInsideItsBudgetIsStillAHit (CHAOS-12511): the budget ends only
// what runs past it. A recheck that finishes with little time left is a hit.
func TestARecheckJustInsideItsBudgetIsStillAHit(t *testing.T) {
	t.Parallel()
	candidate, graph := citedCandidate(reuseSubjects(54))
	graph.finishBefore = 300 * time.Millisecond
	run := runBudgeted(t, context.Background(), graph, candidate, time.Second)
	run.requireHit(t)
	if got := graph.discoveredSizes(); !slices.Equal(got, []int{50, 4}) {
		t.Fatalf("discovery batches = %v, want [50 4]", got)
	}
}

// TestAQueryThatIgnoresItsContextHoldsTheRecheck (CHAOS-12511) states what the
// budget cannot do. A read that ignores its context holds the recheck until it
// returns, because the recheck waits for its reads and abandons none. The bound
// is only as real as the client: the real read client honours the deadline
// (falkorgraph.TestAReadInFlightStopsAtItsContextDeadline). The outcome is still
// the named miss, and it comes late.
func TestAQueryThatIgnoresItsContextHoldsTheRecheck(t *testing.T) {
	t.Parallel()
	const budget, hold = 20 * time.Millisecond, 120 * time.Millisecond
	candidate, graph := citedCandidate(reuseSubjects(54))
	graph.holdFor = hold
	started := time.Now()
	run := runBudgeted(t, context.Background(), graph, candidate, budget)
	elapsed := time.Since(started)
	run.requireMiss(t, AnswerReuseMissRecheckDeadline, AnswerReuseRefusal{})
	if elapsed < hold {
		t.Fatalf("returned after %s, before the read that ignores its context could end (%s)", elapsed, hold)
	}
	if len(graph.deadlines) != 1 {
		t.Fatalf("reads = %d, want 1: the next read must not start after expiry", len(graph.deadlines))
	}
}
