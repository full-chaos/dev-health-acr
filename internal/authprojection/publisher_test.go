package authprojection

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

type fakeRow struct {
	Event
	published bool
}

// fakeStore is the claim's rule in memory: the oldest unpublished row, not in held, whose aggregate has no earlier
// unpublished revision. Rows are listed in created order.
type fakeStore struct {
	rows    []*fakeRow
	helds   [][]string
	err     error
	claimed []string
}

func (s *fakeStore) add(aggregate string, revision int) {
	s.rows = append(s.rows, &fakeRow{Event: Event{AggregateID: aggregate, Revision: revision, Body: []byte(fmt.Sprintf("%s:%d", aggregate, revision))}})
}

func (s *fakeStore) PublishNext(ctx context.Context, held []string, send func(context.Context, Event) bool) (bool, Event, error) {
	s.helds = append(s.helds, slices.Clone(held))
	if s.err != nil {
		return false, Event{}, s.err
	}
	for _, row := range s.rows {
		if row.published || slices.Contains(held, row.AggregateID) {
			continue
		}
		earlier := false
		for _, other := range s.rows {
			if other.AggregateID == row.AggregateID && other.Revision < row.Revision && !other.published {
				earlier = true
			}
		}
		if earlier {
			continue
		}
		s.claimed = append(s.claimed, string(row.Body))
		if send(ctx, row.Event) {
			row.published = true
		}
		return true, row.Event, nil
	}
	return false, Event{}, nil
}

func (s *fakeStore) unpublished() int {
	n := 0
	for _, row := range s.rows {
		if !row.published {
			n++
		}
	}
	return n
}

// scriptedPoster answers by the event body; the default is applied.
type scriptedPoster struct {
	answers map[string]Result
	posts   []string
}

func (p *scriptedPoster) Post(_ context.Context, body []byte) Result {
	p.posts = append(p.posts, string(body))
	if r, ok := p.answers[string(body)]; ok {
		return r
	}
	return Result{Outcome: OutcomeApplied}
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newTestPublisher(store Store, poster Poster) (*Publisher, *clock) {
	c := &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	return NewPublisher(store, poster, c.Now), c
}

func TestRevisionsGoOutInOrderPerAggregateAndEachIsMarkedOnce(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	store.add("b", 1)
	store.add("a", 2)
	store.add("a", 3)
	poster := &scriptedPoster{}
	publisher, _ := newTestPublisher(store, poster)
	report, err := publisher.Tick(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if report.Applied != 4 || store.unpublished() != 0 {
		t.Fatalf("report = %+v, unpublished = %d", report, store.unpublished())
	}
	order := map[string][]int{}
	for _, row := range store.rows {
		_ = row
	}
	for _, body := range poster.posts {
		var a string
		var r int
		_, _ = fmt.Sscanf(body, "%1s:%d", &a, &r)
		order[a] = append(order[a], r)
	}
	if !slices.Equal(order["a"], []int{1, 2, 3}) {
		t.Fatalf("aggregate a went out as %v", order["a"])
	}
}

func TestANextRevisionNeverGoesBeforeAnUnacknowledgedOne(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	store.add("a", 2)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeRetry}}}
	publisher, _ := newTestPublisher(store, poster)
	if _, err := publisher.Tick(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(poster.posts, "a:2") {
		t.Fatalf("revision 2 went out while revision 1 was unacknowledged: %v", poster.posts)
	}
	if store.unpublished() != 2 {
		t.Fatalf("unpublished = %d", store.unpublished())
	}
}

func TestEveryNonAckAnswerLeavesTheEventUnmarked(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeRetry, OutcomeSchema, OutcomeAuthRefused, OutcomeBusy, OutcomeOther, OutcomeNoAnswer, OutcomeNoToken} {
		store := &fakeStore{}
		store.add("a", 1)
		publisher, _ := newTestPublisher(store, &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: outcome}}})
		if _, err := publisher.Tick(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
		if store.unpublished() != 1 {
			t.Errorf("%s: the event was marked published", outcome)
		}
	}
}

func TestAHeldAggregateLeavesTheClaimSoPoisonedHeadsDoNotStarveOthers(t *testing.T) {
	const poisoned = 130
	store := &fakeStore{}
	answers := map[string]Result{}
	for i := 0; i < poisoned; i++ {
		id := fmt.Sprintf("poison%03d", i)
		store.add(id, 1)
		answers[id+":1"] = Result{Outcome: OutcomeSchema}
	}
	store.add("healthy", 1)
	poster := &scriptedPoster{answers: answers}
	publisher, _ := newTestPublisher(store, poster)
	// Batch 100, 130 poisoned aggregates older than the healthy one: the first tick holds 100, the second the rest,
	// and the healthy event goes out on the second tick, never starved.
	first, err := publisher.Tick(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if first.Schema != 100 || first.Held != 100 || !first.Full {
		t.Fatalf("first tick = %+v", first)
	}
	second, err := publisher.Tick(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied != 1 || second.Schema != 30 {
		t.Fatalf("second tick = %+v", second)
	}
	for i, held := range store.helds {
		if i > 0 && len(held) == 0 {
			t.Fatalf("claim %d took an empty held set after holds", i)
		}
	}
	healthy := store.rows[len(store.rows)-1]
	if !healthy.published {
		t.Fatal("the healthy aggregate never flowed")
	}
}

func TestAHoldPassesTheHeldSetSortedAndTheRetryComesAfterTheBackoff(t *testing.T) {
	store := &fakeStore{}
	store.add("b", 1)
	store.add("a", 1)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeRetry}, "b:1": {Outcome: OutcomeRetry}}}
	publisher, clk := newTestPublisher(store, poster)
	if _, err := publisher.Tick(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if got := store.helds[len(store.helds)-1]; !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("held passed to the last claim = %v", got)
	}
	posts := len(poster.posts)
	clk.now = clk.now.Add(DefaultBackoff - time.Second)
	if _, err := publisher.Tick(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if len(poster.posts) != posts {
		t.Fatalf("a held aggregate was retried inside its backoff")
	}
	clk.now = clk.now.Add(2 * time.Second)
	if _, err := publisher.Tick(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if len(poster.posts) != posts+2 {
		t.Fatalf("held aggregates were not retried after the backoff: %d posts", len(poster.posts))
	}
}

func TestTheBackoffDoublesAndStopsAtFiveMinutes(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeRetry}}}
	publisher, clk := newTestPublisher(store, poster)
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, wait := range want {
		if _, err := publisher.Tick(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
		if got := publisher.held["a"].until.Sub(clk.now); got != wait {
			t.Fatalf("hold %d lasts %v, want %v", i+1, got, wait)
		}
		clk.now = clk.now.Add(wait)
	}
}

func TestAnAckClearsTheHoldAndTheFailureCount(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeRetry}}}
	publisher, clk := newTestPublisher(store, poster)
	if _, err := publisher.Tick(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	poster.answers["a:1"] = Result{Outcome: OutcomeApplied}
	clk.now = clk.now.Add(DefaultBackoff)
	report, err := publisher.Tick(context.Background(), 100)
	if err != nil || report.Applied != 1 {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
	if _, held := publisher.held["a"]; held {
		t.Fatal("an acknowledged aggregate stays held")
	}
}

func TestEachOutcomeIsCountedApart(t *testing.T) {
	store := &fakeStore{}
	answers := map[string]Result{}
	for id, outcome := range map[string]Outcome{"r": OutcomeRetry, "s": OutcomeSchema, "o": OutcomeOther, "c": OutcomeConflict, "f": OutcomeFinalRefused} {
		store.add(id, 1)
		answers[id+":1"] = Result{Outcome: outcome}
	}
	store.add("ok", 1)
	publisher, _ := newTestPublisher(store, &scriptedPoster{answers: answers})
	report, err := publisher.Tick(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if report.Retry != 1 || report.Schema != 1 || report.Other != 1 || report.Conflict != 1 || report.FinalRefused != 1 || report.Applied != 1 || report.Held != 3 || report.Stopped != "" {
		t.Fatalf("report = %+v", report)
	}
}

func TestAFinalAnswerMarksTheEventAndLetsTheNextRevisionFlow(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeConflict, OutcomeFinalRefused} {
		store := &fakeStore{}
		store.add("a", 1)
		store.add("a", 2)
		poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: outcome}}}
		publisher, _ := newTestPublisher(store, poster)
		report, err := publisher.Tick(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if store.unpublished() != 0 || report.Held != 0 || !slices.Equal(poster.posts, []string{"a:1", "a:2"}) {
			t.Errorf("%s: unpublished = %d, report = %+v, posts = %v", outcome, store.unpublished(), report, poster.posts)
		}
	}
}

func TestAFinalAnswerClearsAnEarlierHold(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeRetry}}}
	publisher, clk := newTestPublisher(store, poster)
	_, _ = publisher.Tick(context.Background(), 100)
	poster.answers["a:1"] = Result{Outcome: OutcomeConflict}
	clk.now = clk.now.Add(DefaultBackoff)
	if _, err := publisher.Tick(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if _, held := publisher.held["a"]; held || store.unpublished() != 0 {
		t.Fatalf("held = %v, unpublished = %d", held, store.unpublished())
	}
}

func TestAuthRefusalAndNoTokenStopTheTickWithoutHolding(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeAuthRefused, OutcomeNoToken} {
		store := &fakeStore{}
		store.add("a", 1)
		store.add("b", 1)
		poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: outcome}}}
		publisher, _ := newTestPublisher(store, poster)
		report, err := publisher.Tick(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if report.Stopped != outcome || len(poster.posts) != 1 || report.Held != 0 {
			t.Errorf("%s: report = %+v, posts = %v", outcome, report, poster.posts)
		}
		if _, err := publisher.Tick(context.Background(), 100); err != nil || len(poster.posts) != 2 {
			t.Errorf("%s: the next tick must try again at once: posts = %v", outcome, poster.posts)
		}
	}
}

func TestBusyStopsTheTickAndPausesForRetryAfterWithoutHolding(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	store.add("b", 1)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeBusy, RetryAfter: 2 * time.Minute}}}
	publisher, clk := newTestPublisher(store, poster)
	report, err := publisher.Tick(context.Background(), 100)
	if err != nil || report.Stopped != OutcomeBusy || report.Held != 0 || len(poster.posts) != 1 {
		t.Fatalf("report = %+v, err = %v, posts = %v", report, err, poster.posts)
	}
	clk.now = clk.now.Add(119 * time.Second)
	paused, _ := publisher.Tick(context.Background(), 100)
	if !paused.Paused || len(poster.posts) != 1 {
		t.Fatalf("a tick inside Retry-After went out: %+v, posts = %v", paused, poster.posts)
	}
	delete(poster.answers, "a:1")
	clk.now = clk.now.Add(time.Second)
	resumed, _ := publisher.Tick(context.Background(), 100)
	if resumed.Paused || resumed.Applied != 2 {
		t.Fatalf("resumed = %+v", resumed)
	}
}

func TestBusyWithNoRetryAfterPausesForTheDefaultBackoff(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeBusy}}}
	publisher, clk := newTestPublisher(store, poster)
	_, _ = publisher.Tick(context.Background(), 100)
	clk.now = clk.now.Add(DefaultBackoff - time.Second)
	if report, _ := publisher.Tick(context.Background(), 100); !report.Paused {
		t.Fatal("not paused inside the default backoff")
	}
	clk.now = clk.now.Add(time.Second)
	if report, _ := publisher.Tick(context.Background(), 100); report.Paused {
		t.Fatal("still paused after the default backoff")
	}
}

func TestNoAnswerHoldsTheAggregateAndStopsTheTick(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	store.add("b", 1)
	poster := &scriptedPoster{answers: map[string]Result{"a:1": {Outcome: OutcomeNoAnswer}}}
	publisher, _ := newTestPublisher(store, poster)
	report, err := publisher.Tick(context.Background(), 100)
	if err != nil || report.Stopped != OutcomeNoAnswer || report.NoAnswer != 1 || report.Held != 1 || len(poster.posts) != 1 {
		t.Fatalf("report = %+v, err = %v, posts = %v", report, err, poster.posts)
	}
	next, _ := publisher.Tick(context.Background(), 100)
	if next.Applied != 1 || !slices.Equal(poster.posts, []string{"a:1", "b:1"}) {
		t.Fatalf("the held timeout starves the others: %+v, posts = %v", next, poster.posts)
	}
}

func TestAFullBatchIsReportedAndAShortOneIsNot(t *testing.T) {
	store := &fakeStore{}
	for i := 0; i < 5; i++ {
		store.add(fmt.Sprintf("a%d", i), 1)
	}
	publisher, _ := newTestPublisher(store, &scriptedPoster{})
	report, _ := publisher.Tick(context.Background(), 3)
	if !report.Full || report.Applied != 3 {
		t.Fatalf("report = %+v", report)
	}
	report, _ = publisher.Tick(context.Background(), 3)
	if report.Full || report.Applied != 2 {
		t.Fatalf("report = %+v", report)
	}
	exact := &fakeStore{}
	for i := 0; i < 3; i++ {
		exact.add(fmt.Sprintf("e%d", i), 1)
	}
	publisher, _ = newTestPublisher(exact, &scriptedPoster{})
	if report, _ = publisher.Tick(context.Background(), 3); !report.Full {
		t.Fatalf("a batch that used its whole limit is full: %+v", report)
	}
}

func TestTheHeldSetIsBounded(t *testing.T) {
	store := &fakeStore{}
	answers := map[string]Result{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("p%d", i)
		store.add(id, 1)
		answers[id+":1"] = Result{Outcome: OutcomeSchema}
	}
	publisher, _ := newTestPublisher(store, &scriptedPoster{answers: answers})
	publisher.maxHeld = 3
	report, err := publisher.Tick(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.held) != 3 || !report.HeldFull || report.Stopped != "held_full" {
		t.Fatalf("held = %d, report = %+v", len(publisher.held), report)
	}
}

func TestAStoreFailureEndsTheTickWithItsErrorAndNothingIsHeld(t *testing.T) {
	store := &fakeStore{err: errors.New("database is down")}
	publisher, _ := newTestPublisher(store, &scriptedPoster{})
	if _, err := publisher.Tick(context.Background(), 100); err == nil {
		t.Fatal("the store failure was swallowed")
	}
	if len(publisher.held) != 0 {
		t.Fatal("a store failure held an aggregate")
	}
}

func TestADuplicateAnswerIsAnAckSoAResendIsHarmless(t *testing.T) {
	store := &fakeStore{}
	store.add("a", 1)
	poster := &scriptedPoster{}
	publisher, _ := newTestPublisher(store, poster)
	_, _ = publisher.Tick(context.Background(), 100)
	store.rows[0].published = false // the commit was lost after the ack: the row is claimed again
	report, _ := publisher.Tick(context.Background(), 100)
	if report.Applied != 1 || len(poster.posts) != 2 || poster.posts[0] != poster.posts[1] {
		t.Fatalf("report = %+v, posts = %v", report, poster.posts)
	}
}
