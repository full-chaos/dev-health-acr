package projectionrun_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

type failureClassLog struct {
	mu      sync.Mutex
	classes []string
}

func (l *failureClassLog) add(class string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.classes = append(l.classes, class)
}

func (l *failureClassLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Clone(l.classes)
	slices.Sort(out)
	return out
}

func tickWithSource(t *testing.T, orgs []string, source *fakeSource, log *failureClassLog) *fakeBackend {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	backend := newFakeBackend()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  orgs,
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend: backend, Checkpoints: newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(), Concurrency: 1, Logger: logger,
		Observer: projectionrun.SlogObserver{Logger: logger, PairFailure: log.add},
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	return backend
}

// One failed (organization, source) pair is one count, carrying the pair's
// failure class, through a real coordinator tick.
func TestPairFailureHookFiresOncePerFailedPairWithItsClass(t *testing.T) {
	t.Parallel()
	log := &failureClassLog{}
	failing := &fakeSource{name: "source-a", err: fmt.Errorf("read: %w", contextfabric.ErrUnavailable)}
	tickWithSource(t, []string{"org-1", "org-2"}, failing, log)
	if got, want := log.snapshot(), []string{"dependency_unavailable", "dependency_unavailable"}; !slices.Equal(got, want) {
		t.Fatalf("pair failure classes = %v, want %v", got, want)
	}
}

func TestPairFailureHookNamesAnUnclassifiedFailure(t *testing.T) {
	t.Parallel()
	log := &failureClassLog{}
	tickWithSource(t, []string{"org-1"}, &fakeSource{name: "source-a", err: errors.New("raw driver text")}, log)
	if got, want := log.snapshot(), []string{"unclassified"}; !slices.Equal(got, want) {
		t.Fatalf("pair failure classes = %v, want %v", got, want)
	}
}

// A tick that succeeds counts nothing.
func TestPairFailureHookIsSilentOnASuccessfulTick(t *testing.T) {
	t.Parallel()
	log := &failureClassLog{}
	backend := tickWithSource(t, []string{"org-1", "org-2"}, &fakeSource{name: "source-a", pages: 2}, log)
	backend.mu.Lock()
	applied := len(backend.applied)
	backend.mu.Unlock()
	if applied == 0 {
		t.Fatal("no batch was applied; the success premise did not hold")
	}
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("a successful tick fired the pair failure hook: %v", got)
	}
}

// An unset hook is the log-only observer and must not panic.
func TestSlogObserverWithoutAPairFailureHookStillLogsOnly(t *testing.T) {
	t.Parallel()
	projectionrun.SlogObserver{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}.ObserveProjectionOutcome(projectionrun.Outcome{Err: errors.New("x")})
}
