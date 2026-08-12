package projectionrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// SourcePair binds a configured ProjectionSource to the Source name it
// projects under (matching what devhealthsource writes onto its batches).
type SourcePair struct {
	Name   string
	Source contextfabric.ProjectionSource
}

// Outcome is one (org, source) tick's result, for telemetry and readiness.
type Outcome struct {
	OrgID    string
	Source   string
	Run      contextfabric.ProjectionRun
	Err      error
	Duration time.Duration
	At       time.Time
}

// Observer receives every tick outcome. Implementations must stay
// content-safe: Outcome carries counts, IDs, and errors, never projected
// entity/relationship/episode content.
type Observer interface {
	ObserveProjectionOutcome(Outcome)
}

type noopObserver struct{}

func (noopObserver) ObserveProjectionOutcome(Outcome) {}

const (
	defaultPollInterval = 15 * time.Second
	defaultConcurrency  = 4
	defaultMaxBackoff   = 5 * time.Minute
	baseBackoff         = 5 * time.Second
)

type Config struct {
	OrgIDs       []string
	Sources      []SourcePair
	Backend      contextfabric.ProjectionBackend
	Checkpoints  contextfabric.ProjectionCheckpointStore
	Locker       OrgLocker // nil -> NoopOrgLocker (in-process mutex only)
	Observer     Observer  // nil -> discarded
	PollInterval time.Duration
	Concurrency  int
	MaxBackoff   time.Duration
	Now          func() time.Time
	Logger       *slog.Logger
}

// Coordinator schedules contextfabric.ProjectionWorker.RunOnce across every
// configured organization and source, enforcing single-flight per
// organization (CHAOS-3753's amendment), bounded retry with backoff per
// (org, source) pair, cancellation, and failure isolation: one pair's error
// never blocks another org or source.
type Coordinator struct {
	orgIDs      []string
	sourceNames []string
	workers     map[string]*contextfabric.ProjectionWorker
	locker      OrgLocker
	observer    Observer
	poll        time.Duration
	concurrency int
	maxBackoff  time.Duration
	now         func() time.Time
	logger      *slog.Logger

	orgMu sync.Map // orgID -> *sync.Mutex, in-process first line of defense

	backoffMu sync.Mutex
	backoff   map[string]*pairBackoff // "orgID\x00source" -> state
}

type pairBackoff struct {
	consecutiveFailures int
	nextAttempt         time.Time
}

func NewCoordinator(cfg Config) (*Coordinator, error) {
	if cfg.Backend == nil || cfg.Checkpoints == nil {
		return nil, errors.New("projectionrun: backend and checkpoint store are required")
	}
	if len(cfg.Sources) == 0 {
		return nil, errors.New("projectionrun: at least one source is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultConcurrency
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Locker == nil {
		cfg.Locker = NoopOrgLocker{}
	}
	if cfg.Observer == nil {
		cfg.Observer = noopObserver{}
	}
	workers := make(map[string]*contextfabric.ProjectionWorker, len(cfg.Sources))
	sourceNames := make([]string, 0, len(cfg.Sources))
	for _, pair := range cfg.Sources {
		if pair.Name == "" || pair.Source == nil {
			return nil, fmt.Errorf("projectionrun: source pair %q is incomplete", pair.Name)
		}
		if _, exists := workers[pair.Name]; exists {
			return nil, fmt.Errorf("projectionrun: duplicate source %q", pair.Name)
		}
		worker, err := contextfabric.NewProjectionWorker(pair.Source, cfg.Backend, cfg.Checkpoints, contextfabric.ProjectionWorkerOptions{Now: cfg.Now})
		if err != nil {
			return nil, fmt.Errorf("projectionrun: build worker for %q: %w", pair.Name, err)
		}
		workers[pair.Name] = worker
		sourceNames = append(sourceNames, pair.Name)
	}
	return &Coordinator{
		orgIDs: append([]string(nil), cfg.OrgIDs...), sourceNames: sourceNames, workers: workers,
		locker: cfg.Locker, observer: cfg.Observer, poll: cfg.PollInterval, concurrency: cfg.Concurrency,
		maxBackoff: cfg.MaxBackoff, now: cfg.Now, logger: cfg.Logger, backoff: make(map[string]*pairBackoff),
	}, nil
}

// Run schedules ticks on PollInterval until ctx is canceled. It never
// returns early because of a single organization's or source's failure.
func (c *Coordinator) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	for {
		c.Tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Tick runs one bounded-concurrency pass over every configured organization.
// Exported so hosting composition (and tests) can drive ticks explicitly
// instead of waiting on PollInterval.
func (c *Coordinator) Tick(ctx context.Context) {
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	for _, orgID := range c.orgIDs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(orgID string) {
			defer wg.Done()
			defer func() { <-sem }()
			c.runOrg(ctx, orgID)
		}(orgID)
	}
	wg.Wait()
}

// runOrg enforces single-flight per organization across a whole multi-source
// pass: the in-process mutex is a fast, always-on first line of defense; the
// OrgLocker (PostgresOrgLocker in production) makes the guarantee hold
// across acr-projector replicas too. Both are non-blocking (TryLock /
// pg_try_advisory_lock): a busy organization is skipped this tick, not
// queued, so one slow organization can never starve the others.
func (c *Coordinator) runOrg(ctx context.Context, orgID string) {
	mutexAny, _ := c.orgMu.LoadOrStore(orgID, &sync.Mutex{})
	mutex := mutexAny.(*sync.Mutex)
	if !mutex.TryLock() {
		return
	}
	defer mutex.Unlock()

	unlock, err := c.locker.Lock(ctx, orgID)
	if err != nil {
		if !errors.Is(err, ErrOrgLocked) {
			c.logger.WarnContext(ctx, "projection organization lock failed", "org_id", orgID, "error", err)
		}
		return
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			c.logger.WarnContext(ctx, "projection organization unlock failed", "org_id", orgID, "error", unlockErr)
		}
	}()

	for _, source := range c.sourceNames {
		if ctx.Err() != nil {
			return
		}
		c.runPair(ctx, orgID, source)
	}
}

func (c *Coordinator) runPair(ctx context.Context, orgID, source string) {
	key := orgID + "\x00" + source
	if !c.due(key) {
		return
	}
	started := c.now()
	run, err := c.workers[source].RunOnce(ctx, orgID, source)
	outcome := Outcome{OrgID: orgID, Source: source, Run: run, Err: err, Duration: c.now().Sub(started), At: c.now()}
	c.recordBackoff(key, err)
	c.observer.ObserveProjectionOutcome(outcome)
	switch {
	case err != nil:
		c.logger.WarnContext(ctx, "projection pair failed", "org_id", orgID, "source", source, "error", err, "duration_ms", outcome.Duration.Milliseconds())
	case run.Applied:
		c.logger.InfoContext(ctx, "projection batch applied", "org_id", orgID, "source", source, "batch_id", run.BatchID, "backend_watermark", run.BackendWatermark, "duration_ms", outcome.Duration.Milliseconds())
	}
}

func (c *Coordinator) due(key string) bool {
	c.backoffMu.Lock()
	defer c.backoffMu.Unlock()
	state, ok := c.backoff[key]
	if !ok {
		return true
	}
	return !c.now().Before(state.nextAttempt)
}

func (c *Coordinator) recordBackoff(key string, err error) {
	c.backoffMu.Lock()
	defer c.backoffMu.Unlock()
	state, ok := c.backoff[key]
	if !ok {
		state = &pairBackoff{}
		c.backoff[key] = state
	}
	if err == nil {
		state.consecutiveFailures = 0
		state.nextAttempt = time.Time{}
		return
	}
	state.consecutiveFailures++
	delay := baseBackoff * time.Duration(1<<min(state.consecutiveFailures-1, 10))
	if delay > c.maxBackoff {
		delay = c.maxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(baseBackoff))) //nolint:gosec // scheduling jitter, not security-sensitive
	state.nextAttempt = c.now().Add(delay + jitter)
}
