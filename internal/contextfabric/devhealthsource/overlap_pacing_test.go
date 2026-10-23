package devhealthsource

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const pacingInterval = 5 * time.Minute

// pacedRig is a churnRig whose source is caught up with a 600-row window
// (three pages of walk) and keeps a trickle of new rows landing, so the
// frontier stays inside the window's reach.
func pacedRig(t *testing.T, interval time.Duration) *churnRig {
	r := newChurnRig(t, churnStart)
	r.plan.windowInterval = interval
	r.land(600, churnStart.Add(-20*time.Minute), churnStart.Add(-time.Minute))
	r.runTick()
	return r
}

func (r *churnRig) trickle(i int) {
	if i%8 == 0 {
		r.landOne(r.now.Add(-time.Second))
	}
}

// Window passes that follow each other at once re-read the window on every
// tick for as long as the frontier is within overlap + slack of now. The
// interval is the pause between two passes.
func TestWindowPassesAreSeparatedByTheInterval(t *testing.T) {
	t.Parallel()
	const ticks = 120
	reads := map[time.Duration]int{}
	for _, interval := range []time.Duration{0, pacingInterval} {
		r := pacedRig(t, interval)
		r.windowReads = 0
		starts := map[time.Time]bool{}
		for i := 1; i <= ticks; i++ {
			r.advance()
			r.trickle(i)
			r.runTick()
			if p := r.pass(); p.walking {
				starts[p.passStart] = true
			}
		}
		reads[interval] = r.windowReads
		t.Logf("interval=%s window reads over %d ticks=%d passes started=%d", interval, ticks, r.windowReads, len(starts))
		if len(starts) < 3 {
			t.Fatalf("interval %s: %d passes started in %d ticks; want the passes to keep ending and starting", interval, len(starts), ticks)
		}
	}
	if reads[0] < ticks {
		t.Fatalf("precondition: with no interval the window is walked on every tick; got %d reads in %d ticks", reads[0], ticks)
	}
	if reads[pacingInterval]*3 > reads[0] {
		t.Fatalf("window reads with a %s interval = %d, without = %d; want at most a third", pacingInterval, reads[pacingInterval], reads[0])
	}
}

// A late row that lands right after a pass ended waits for the next pass: it
// is projected about one interval later, not before it and not much after.
// With no interval the next tick finds it.
func TestLateRowWaitsForTheNextPassAndIsProjectedWithinTheInterval(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		interval       time.Duration
		minLag, maxLag int
	}{
		{interval: 0, minLag: 0, maxLag: 3},
		{interval: pacingInterval, minLag: int(pacingInterval/churnTick) - 2, maxLag: int(pacingInterval/churnTick) + 8},
	} {
		r := pacedRig(t, testCase.interval)
		ended, late, landed := 0, "", 0
		for i := 1; i <= 80 && late == ""; i++ {
			r.advance()
			r.trickle(i)
			was := r.pass()
			r.runTick()
			if now := r.pass(); was.walking && (!now.walking || !now.passStart.Equal(was.passStart)) {
				ended = r.tick
				r.advance()
				late, landed = r.landOne(churnStart.Add(-19*time.Minute)), r.tick+1
				r.runTick()
			}
		}
		if late == "" {
			t.Fatalf("interval %s: no pass ended in 80 ticks", testCase.interval)
		}
		for i := 1; i <= 80; i++ {
			if _, ok := r.projected[late]; ok {
				break
			}
			r.advance()
			r.trickle(i)
			r.runTick()
		}
		at, ok := r.projected[late]
		lag := at - landed
		t.Logf("interval=%s pass ended tick %d, late row landed tick %d, projected=%v at tick %d (lag %d)", testCase.interval, ended, landed, ok, at, lag)
		if !ok {
			t.Fatalf("interval %s: the late row was never projected", testCase.interval)
		}
		if lag < testCase.minLag || lag > testCase.maxLag {
			t.Fatalf("interval %s: late row projected %d ticks after it landed; want %d..%d", testCase.interval, lag, testCase.minLag, testCase.maxLag)
		}
	}
}

// The interval paces the trailing-window walk only. A row beyond the
// frontier is read by the paged path on the tick it lands, whatever the
// interval and wherever the window walk stands.
func TestNewRowsOnThePagedPathAreNotDelayedByTheInterval(t *testing.T) {
	t.Parallel()
	r := pacedRig(t, pacingInterval)
	waited := 0
	for i := 1; i <= 60; i++ {
		r.advance()
		if r.pass().walking || r.now.Before(r.pass().nextStart) {
			key := r.landOne(r.now.Add(-time.Second))
			r.runTick()
			if got := r.projected[key]; got != r.tick {
				t.Fatalf("tick %d: a new row landed beyond the frontier was projected at tick %d, want the same tick", r.tick, got)
			}
			if r.now.Before(r.pass().nextStart) {
				waited++
			}
			continue
		}
		r.runTick()
	}
	if waited < 5 {
		t.Fatalf("precondition: %d ticks fell inside the pause between passes; want at least 5", waited)
	}
}

// The pause is memory of this process, like the rest of the window state: a
// restart (empty memo) walks the window at once.
func TestARestartWalksTheWindowAtOnce(t *testing.T) {
	t.Parallel()
	r := pacedRig(t, pacingInterval)
	if r.windowReads == 0 {
		t.Fatalf("the first drain did not walk the window")
	}
	for i := 1; i <= 40 && r.pass().walking; i++ {
		r.advance()
		r.runTick()
	}
	if r.pass().walking || !r.now.Before(r.pass().nextStart) {
		t.Fatalf("precondition: want a pass ended and the pause running (walking=%v now=%s nextStart=%s)", r.pass().walking, r.now, r.pass().nextStart)
	}
	r.advance()
	before := r.windowReads
	r.runTick()
	if r.windowReads != before {
		t.Fatalf("a tick inside the pause read the window %d times", r.windowReads-before)
	}
	r.plan.window = newWindowMemo()
	r.advance()
	r.runTick()
	if r.windowReads == before {
		t.Fatalf("a restarted projector did not walk the window on its first tick")
	}
}

// A side-effect-free read (PeekProjectionBatch) runs the engine on a copy of
// the memo. Starting a pass there must not start the pause on the real one.
func TestAPeekDoesNotStartThePause(t *testing.T) {
	t.Parallel()
	r := pacedRig(t, pacingInterval)
	for i := 1; i <= 40 && !(r.pass().walking == false && r.now.Before(r.pass().nextStart)); i++ {
		r.advance()
		r.runTick()
	}
	r.now = r.pass().nextStart.Add(time.Second)
	want := r.pass()
	peek := r.plan
	peek.window = peek.window.snapshot()
	if _, _, err := peek.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: r.plan.source, Cursor: r.cursor}); err != nil {
		t.Fatal(err)
	}
	if got := r.pass(); got != want {
		t.Fatalf("a peek changed the shared pass state: %+v, want %+v", got, want)
	}
	before := r.windowReads
	r.runTick()
	if r.windowReads == before {
		t.Fatalf("the pass that was due did not start after a peek")
	}
}

// The pause counts from the end of the pass. Reads that take longer than the
// interval must not leave the pause already over when the pass ends.
func TestThePauseStartsWhenThePassEndsNotWhenTheCallStarted(t *testing.T) {
	t.Parallel()
	r := pacedRig(t, pacingInterval)
	r.windowSkew = pacingInterval + time.Minute
	for i := 1; i <= 40; i++ {
		was := r.pass()
		r.advance()
		r.runTick()
		if was.walking && !r.pass().walking {
			break
		}
	}
	if r.pass().walking {
		t.Fatalf("precondition: no pass ended in 40 ticks")
	}
	r.windowSkew = 0
	ended := r.now
	if due := r.pass().nextStart; due.Before(ended.Add(pacingInterval - churnTick)) {
		t.Fatalf("the pause ends at %s, %s after the pass ended; want the interval %s", due, due.Sub(ended), pacingInterval)
	}
	before := r.windowReads
	for r.now.Before(ended.Add(pacingInterval - 2*churnTick)) {
		r.advance()
		r.runTick()
	}
	if r.windowReads != before {
		t.Fatalf("%d window statements ran inside the pause that follows the pass", r.windowReads-before)
	}
}

// The "pass ended" line names when the next pass may start, and only when a
// pause is set.
func TestPassEndedLineNamesWhenTheNextPassMayStart(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{0, pacingInterval} {
		r := pacedRig(t, interval)
		var line map[string]any
		for i := 1; i <= 40 && line == nil; i++ {
			r.advance()
			r.trickle(i)
			line = r.runTick().ended
		}
		if line == nil {
			t.Fatalf("interval %s: no \"pass ended\" line in 40 ticks", interval)
		}
		got, has := line["next_pass_not_before"]
		if interval == 0 {
			if has {
				t.Fatalf("interval 0: line carries next_pass_not_before=%v", got)
			}
			continue
		}
		if want := r.pass().nextStart.UTC().Format(time.RFC3339Nano); got != want {
			t.Fatalf("next_pass_not_before = %v, want %s", got, want)
		}
	}
}

func TestSourcesPauseBetweenWindowPassesByDefault(t *testing.T) {
	t.Parallel()
	clickhouse, err := NewClickHouseProjectionSource(keysetRows{})
	if err != nil {
		t.Fatal(err)
	}
	teams, err := NewTeamsProjectsSource(keysetRows{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := clickhouse.WindowPassInterval(); got != DefaultWindowPassInterval {
		t.Errorf("the ClickHouse source pauses %s between window passes by default, want %s", got, DefaultWindowPassInterval)
	}
	if got := teams.WindowPassInterval(); got != DefaultWindowPassInterval {
		t.Errorf("the teams/projects source pauses %s between window passes by default, want %s", got, DefaultWindowPassInterval)
	}
	if got := clickhouse.plan("").windowInterval; got != DefaultWindowPassInterval {
		t.Errorf("the ClickHouse plan pauses %s, want %s", got, DefaultWindowPassInterval)
	}
}
