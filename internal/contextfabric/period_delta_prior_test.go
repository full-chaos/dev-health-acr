package contextfabric

import (
	"context"
	"encoding/json"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"sync"
	"testing"
	"time"
)

type prefetchFactReader struct {
	bundle  CanonicalFactBundle
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
	seen    context.Context
}

func (r *prefetchFactReader) ReadFacts(ctx context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
	r.mu.Lock()
	r.calls++
	r.seen = ctx
	r.mu.Unlock()
	close(r.started)
	select {
	case <-r.release:
		return r.bundle, nil
	case <-ctx.Done():
		return CanonicalFactBundle{}, ctx.Err()
	}
}

func newPrefetchFixture() (*Engine, *prefetchFactReader, SubjectRef, []CanonicalFact, QuestionFrame) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"}
	reader := &prefetchFactReader{
		started: make(chan struct{}), release: make(chan struct{}),
		bundle: CanonicalFactBundle{Facts: []CanonicalFact{periodDeltaHealthFact(team, map[string]FactValue{
			"severity": StringFactValue("high"), "severity_as_of": StringFactValue("2026-08-19"),
		})}},
	}
	current := []CanonicalFact{periodDeltaHealthFact(team, map[string]FactValue{
		"severity": StringFactValue("elevated"), "severity_as_of": StringFactValue("2026-09-18"),
	})}
	return &Engine{facts: reader, telemetry: &recordingTelemetry{}}, reader, team, current, QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}
}

func TestPeriodDeltaPriorReadRunsBeforeTheCurrentFactsAreKnownAndServesTheSameFacts(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

	engine, reader, team, current, frame := newPrefetchFixture()
	prior := engine.startPeriodDeltaPriorRead(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{team}, asOf, true)
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the prior read did not start before the current facts were applied")
	}
	served := make(chan []CanonicalFact, 1)
	go func() {
		served <- engine.applyPeriodDeltaWithPrior(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{team}, current, asOf, true, prior)
	}()
	select {
	case <-served:
		t.Fatal("applyPeriodDelta returned before the prior read finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(reader.release)
	prefetched := <-served
	if reader.calls != 1 {
		t.Fatalf("prior reads = %d, want exactly the prefetched one", reader.calls)
	}

	plainEngine, plainReader, plainTeam, plainCurrent, plainFrame := newPrefetchFixture()
	close(plainReader.release)
	plain := plainEngine.applyPeriodDelta(context.Background(), periodDeltaTestPrincipal(), plainFrame, []SubjectRef{plainTeam}, plainCurrent, asOf, true)
	left, _ := json.Marshal(prefetched)
	right, _ := json.Marshal(plain)
	if string(left) != string(right) {
		t.Fatalf("prefetched prior serves different facts\nprefetched: %s\nplain:      %s", left, right)
	}
}

func TestPeriodDeltaPriorReadStopsWhenNobodyWaitsForIt(t *testing.T) {
	t.Parallel()
	engine, reader, team, _, frame := newPrefetchFixture()
	prior := engine.startPeriodDeltaPriorRead(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{team}, time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), true)
	<-reader.started
	prior.stop()
	if _, err := prior.wait(); err != context.Canceled {
		t.Fatalf("stopped prior read error = %v, want context.Canceled", err)
	}
}

func TestPeriodDeltaPriorReadIsNotStartedWithoutTheObligationOrAnAnchor(t *testing.T) {
	t.Parallel()
	engine, _, team, _, frame := newPrefetchFixture()
	asOf := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	if engine.startPeriodDeltaPriorRead(context.Background(), periodDeltaTestPrincipal(), QuestionFrame{}, []SubjectRef{team}, asOf, true) != nil {
		t.Fatal("started a prior read for a frame without the obligation")
	}
	if engine.startPeriodDeltaPriorRead(context.Background(), periodDeltaTestPrincipal(), frame, []SubjectRef{team}, asOf, false) != nil {
		t.Fatal("started a prior read without an anchored instant")
	}
}
