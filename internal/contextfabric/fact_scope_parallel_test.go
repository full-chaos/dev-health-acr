package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type scopeProbeExpander struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	started  chan FactKind
	// hold blocks a kind until release is closed or the call is cancelled.
	hold    map[FactKind]chan struct{}
	failing map[FactKind]error
	panics  map[FactKind]bool
	// barrier, when set, makes every call wait until that many are in flight.
	barrier int
	dwell   time.Duration
	arrived int
	// completed counts the calls that have returned.
	completed int
	opened    chan struct{}
}

func (e *scopeProbeExpander) ExpandFactScope(ctx context.Context, request FactScopeExpansionRequest) (FactScopeExpansionResult, error) {
	e.mu.Lock()
	e.inFlight++
	if e.inFlight > e.maxSeen {
		e.maxSeen = e.inFlight
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.inFlight--
		e.completed++
		e.mu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return FactScopeExpansionResult{}, err
	}
	if e.started != nil {
		e.started <- request.RequirementKind
	}
	if e.barrier > 0 {
		e.mu.Lock()
		e.arrived++
		if e.arrived >= e.barrier && e.opened != nil {
			close(e.opened)
		}
		opened := e.opened
		e.mu.Unlock()
		select {
		case <-opened:
		case <-time.After(5 * time.Second):
			return FactScopeExpansionResult{}, errors.New("barrier not reached")
		}
	}
	if e.dwell > 0 {
		time.Sleep(e.dwell)
	}
	if e.panics[request.RequirementKind] {
		panic("probe panic " + string(request.RequirementKind))
	}
	if release, ok := e.hold[request.RequirementKind]; ok {
		select {
		case <-release:
		case <-ctx.Done():
			return FactScopeExpansionResult{}, ctx.Err()
		}
	}
	if err, ok := e.failing[request.RequirementKind]; ok {
		return FactScopeExpansionResult{}, err
	}
	return FactScopeExpansionResult{Targets: []SubjectRef{{Kind: request.TargetKind, CanonicalID: "probe:" + string(request.RequirementKind), Label: string(request.RequirementKind)}}}, nil
}

func scopeProbeInput(kinds ...FactKind) (factScopeResolveInput, map[FactKind]FactCapability) {
	capabilities := map[FactKind]FactCapability{}
	requirements := make([]factPlanRequirement, 0, len(kinds))
	for _, kind := range kinds {
		capabilities[kind] = planCapability(kind, string(kind), SubjectRepository)
		requirements = append(requirements, factPlanRequirement{Kind: kind})
	}
	return factScopeResolveInput{
		Roots:            []SubjectRef{scopeTeam},
		Requirements:     requirements,
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		PullRequestFacts: true,
	}, capabilities
}

var scopeProbeKinds = []FactKind{FactPullRequests, FactContinuousIntegration, FactBlockers}

func resolveProbe(t *testing.T, ctx context.Context, fanout int, expander FactScopeExpander, kinds ...FactKind) FactReadScope {
	t.Helper()
	resolver := NewFactReadScopeResolver(expander)
	resolver.fanout = fanout
	input, capabilities := scopeProbeInput(kinds...)
	return resolver.Resolve(ctx, storage.Principal{OrgID: "org"}, input, capabilities)
}

func TestScopeResolveExpandsEveryProbeKind(t *testing.T) {
	scope := resolveProbe(t, context.Background(), 1, &scopeProbeExpander{}, scopeProbeKinds...)
	if len(scope.Events) != 3 {
		t.Fatalf("events = %d, want 3 (a probe that expands nothing proves nothing): %+v", len(scope.Events), scope.Events)
	}
	for _, event := range scope.Events {
		if event.Outcome != FactScopeExpanded {
			t.Fatalf("event %s outcome = %s, want expanded", event.RequirementKind, event.Outcome)
		}
	}
}

func TestScopeResolveFanOutOneAndFourAgree(t *testing.T) {
	cases := map[string]func() FactScopeExpander{
		"all succeed": func() FactScopeExpander { return &scopeProbeExpander{} },
		"second fails": func() FactScopeExpander {
			return &scopeProbeExpander{failing: map[FactKind]error{FactContinuousIntegration: errors.New("boom")}}
		},
		"first and last fail": func() FactScopeExpander {
			return &scopeProbeExpander{failing: map[FactKind]error{FactPullRequests: errors.New("a"), FactBlockers: errors.New("c")}}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			serial := resolveProbe(t, context.Background(), 1, build(), scopeProbeKinds...)
			parallel := resolveProbe(t, context.Background(), 4, build(), scopeProbeKinds...)
			if !reflect.DeepEqual(serial, parallel) {
				t.Fatalf("fan-out 4 differs from fan-out 1:\nserial   %+v\nparallel %+v", serial, parallel)
			}
		})
	}
}

func TestScopeResolveRepeatedKindStaysSerialAndEqual(t *testing.T) {
	kinds := []FactKind{FactPullRequests, FactContinuousIntegration, FactPullRequests}
	serial := resolveProbe(t, context.Background(), 1, &scopeProbeExpander{}, kinds...)
	parallel := resolveProbe(t, context.Background(), 4, &scopeProbeExpander{barrier: 0}, kinds...)
	if !reflect.DeepEqual(serial, parallel) {
		t.Fatalf("repeated kind differs:\nserial   %+v\nparallel %+v", serial, parallel)
	}
}

func TestScopeResolveErrorOrderFollowsInputOrder(t *testing.T) {
	// requirement 1 (pull requests) is slow, requirement 2 fails at once.
	release := make(chan struct{})
	expander := &scopeProbeExpander{
		started: make(chan FactKind, 3),
		hold:    map[FactKind]chan struct{}{FactPullRequests: release},
		failing: map[FactKind]error{FactContinuousIntegration: errors.New("ci down")},
	}
	done := make(chan FactReadScope, 1)
	go func() { done <- resolveProbe(t, context.Background(), 4, expander, scopeProbeKinds...) }()
	for seen := 0; seen < 3; seen++ {
		<-expander.started
	}
	close(release)
	scope := <-done
	if len(scope.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(scope.Events))
	}
	want := []FactKind{FactPullRequests, FactContinuousIntegration, FactBlockers}
	for i, event := range scope.Events {
		if event.RequirementKind != want[i] {
			t.Fatalf("event %d is %s, want %s: events follow input order, not completion order", i, event.RequirementKind, want[i])
		}
	}
	if scope.Events[0].Outcome != FactScopeExpanded || scope.Events[1].Outcome == FactScopeExpanded {
		t.Fatalf("outcomes = %s, %s; want the slow first expanded and the second failed", scope.Events[0].Outcome, scope.Events[1].Outcome)
	}
	serial := resolveProbe(t, context.Background(), 1, &scopeProbeExpander{failing: expander.failing}, scopeProbeKinds...)
	if !reflect.DeepEqual(serial, scope) {
		t.Fatalf("slow-first parallel turn differs from the serial walk:\nserial   %+v\nparallel %+v", serial, scope)
	}
}

func TestScopeResolveCancelStopsEveryInFlightRead(t *testing.T) {
	never := make(chan struct{})
	hold := map[FactKind]chan struct{}{FactPullRequests: never, FactContinuousIntegration: never, FactBlockers: never}
	expander := &scopeProbeExpander{started: make(chan FactKind, 3), hold: hold}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan FactReadScope, 1)
	go func() { done <- resolveProbe(t, ctx, 4, expander, scopeProbeKinds...) }()
	for seen := 0; seen < 3; seen++ {
		<-expander.started
	}
	cancel()
	select {
	case scope := <-done:
		if len(scope.Events) != 3 {
			t.Fatalf("events = %d, want 3", len(scope.Events))
		}
		for _, event := range scope.Events {
			if event.Outcome != FactScopeFailed {
				t.Fatalf("event %s outcome = %s, want failed", event.RequirementKind, event.Outcome)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve did not return after cancel: in-flight reads were not stopped")
	}
	// A turn cancelled before it starts reads the same order and outcomes at
	// fan-out 1 and 4. (The work-item admission gate picks between a free slot
	// and the cancelled context at random, so the failure class of that one
	// kind is not compared.)
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	serial := resolveProbe(t, cancelled, 1, &scopeProbeExpander{hold: hold}, scopeProbeKinds...)
	parallel := resolveProbe(t, cancelled, 4, &scopeProbeExpander{hold: hold}, scopeProbeKinds...)
	if len(serial.Events) != len(parallel.Events) {
		t.Fatalf("event counts differ: %d vs %d", len(serial.Events), len(parallel.Events))
	}
	for i := range serial.Events {
		if serial.Events[i].RequirementKind != parallel.Events[i].RequirementKind || serial.Events[i].Outcome != parallel.Events[i].Outcome {
			t.Fatalf("event %d differs: %s/%s vs %s/%s", i, serial.Events[i].RequirementKind, serial.Events[i].Outcome, parallel.Events[i].RequirementKind, parallel.Events[i].Outcome)
		}
	}
}

func TestScopeResolveFanOutIsBounded(t *testing.T) {
	// two slots, three requirements: never more than two in flight.
	expander := &scopeProbeExpander{dwell: 30 * time.Millisecond}
	resolveProbe(t, context.Background(), 2, expander, scopeProbeKinds...)
	if expander.maxSeen > 2 {
		t.Fatalf("max in flight = %d, want <= 2", expander.maxSeen)
	}
	// four slots: the three requirements really overlap (barrier proves it).
	overlap := &scopeProbeExpander{barrier: 3, opened: make(chan struct{})}
	scope := resolveProbe(t, context.Background(), 4, overlap, scopeProbeKinds...)
	for _, event := range scope.Events {
		if event.Outcome != FactScopeExpanded {
			t.Fatalf("event %s outcome = %s: the three expansions did not overlap", event.RequirementKind, event.Outcome)
		}
	}
	if overlap.maxSeen != 3 {
		t.Fatalf("max in flight = %d, want 3", overlap.maxSeen)
	}
	// fan-out 1 never overlaps.
	one := &scopeProbeExpander{dwell: 10 * time.Millisecond}
	resolveProbe(t, context.Background(), 1, one, scopeProbeKinds...)
	if one.maxSeen != 1 {
		t.Fatalf("fan-out 1 max in flight = %d, want 1", one.maxSeen)
	}
}

func TestScopeResolvePanicSurfacesInCallerAtItsPlace(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expander panic did not reach the caller")
		}
	}()
	resolveProbe(t, context.Background(), 4, &scopeProbeExpander{panics: map[FactKind]bool{FactContinuousIntegration: true}}, scopeProbeKinds...)
}

func TestGoScopeBoundedSharesTheResolveBudget(t *testing.T) {
	ran := false
	GoScopeBounded(context.Background(), func(context.Context) { ran = true })(true)
	if !ran {
		t.Fatal("without a budget the work runs at join")
	}
	tokens := make(chan struct{}, 1)
	ctx := context.WithValue(context.Background(), scopeTokensKey{}, tokens)
	tokens <- struct{}{}
	started := false
	join := GoScopeBounded(ctx, func(context.Context) { started = true })
	if started {
		t.Fatal("with no free slot the work is held back, not started beside the caller")
	}
	join(true)
	if !started {
		t.Fatal("a held-back read runs at join(true)")
	}
	started = false
	GoScopeBounded(ctx, func(context.Context) { started = true })(false)
	if started {
		t.Fatal("a held-back read never runs after join(false): the serial walk would not have started it")
	}
	<-tokens
	release := make(chan struct{})
	wait := GoScopeBounded(ctx, func(context.Context) { <-release })
	if len(tokens) != 1 {
		t.Fatal("a free slot is taken by the helper")
	}
	close(release)
	wait(true)
	if len(tokens) != 0 {
		t.Fatal("the slot is returned when the helper ends")
	}
}

func TestGoScopeBoundedAbandonCancelsAndDoesNotWait(t *testing.T) {
	tokens := make(chan struct{}, 1)
	ctx := context.WithValue(context.Background(), scopeTokensKey{}, tokens)
	release := make(chan struct{})
	sawCancel := make(chan struct{})
	join := GoScopeBounded(ctx, func(readCtx context.Context) {
		<-readCtx.Done()
		close(sawCancel)
		<-release
	})
	returned := make(chan struct{})
	go func() { join(false); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("join(false) waited for the read it abandoned")
	}
	select {
	case <-sawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("join(false) did not cancel the read's context")
	}
	close(release)
}

func TestGoScopeBoundedRaisesAPanicAtJoin(t *testing.T) {
	tokens := make(chan struct{}, 1)
	ctx := context.WithValue(context.Background(), scopeTokensKey{}, tokens)
	join := GoScopeBounded(ctx, func(context.Context) { panic("boom") })
	defer func() {
		if recover() == nil {
			t.Fatal("the panic of the read did not reach the caller")
		}
	}()
	join(true)
}

func (e *scopeProbeExpander) completedCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.completed
}

// The first requirement is held while the others finish, then the turn is
// cancelled. The serial walk starts the others after the cancel, so they fail;
// the side by side walk must serve the same scope, not the answers the others
// finished with before the cancel.
func TestScopeResolveCancelAfterTheOthersFinishedMatchesSerial(t *testing.T) {
	run := func(fanout int) FactReadScope {
		never := make(chan struct{})
		expander := &scopeProbeExpander{hold: map[FactKind]chan struct{}{FactPullRequests: never}}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan FactReadScope, 1)
		go func() { done <- resolveProbe(t, ctx, fanout, expander, scopeProbeKinds...) }()
		want := 0
		if fanout > 1 {
			want = 2
		}
		for deadline := time.Now().Add(5 * time.Second); expander.completedCalls() < want; {
			if time.Now().After(deadline) {
				t.Fatalf("fan-out %d: the other requirements did not finish", fanout)
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case scope := <-done:
			return scope
		case <-time.After(5 * time.Second):
			t.Fatalf("fan-out %d: Resolve did not return after cancel", fanout)
		}
		return FactReadScope{}
	}
	serial, parallel := run(1), run(4)
	if len(serial.Events) != 3 || len(parallel.Events) != 3 {
		t.Fatalf("events = %d and %d, want 3 each", len(serial.Events), len(parallel.Events))
	}
	for i := range serial.Events {
		s, p := serial.Events[i], parallel.Events[i]
		if s.RequirementKind != p.RequirementKind || s.Outcome != p.Outcome || len(s.Policy) != len(p.Policy) {
			t.Fatalf("event %d: serial %s/%s, side by side %s/%s", i, s.RequirementKind, s.Outcome, p.RequirementKind, p.Outcome)
		}
		if s.Outcome != FactScopeFailed {
			t.Fatalf("event %d outcome = %s, want failed after the cancel", i, s.Outcome)
		}
	}
	if len(parallel.DerivedSubjects) != len(serial.DerivedSubjects) || len(parallel.Derivations) != len(serial.Derivations) {
		t.Fatalf("derived subjects/derivations differ: serial %d/%d, side by side %d/%d", len(serial.DerivedSubjects), len(serial.Derivations), len(parallel.DerivedSubjects), len(parallel.Derivations))
	}
}
