package projectionrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
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
	// ObserveProjectionDrain (CHAOS-3826) fires once per (org, source) per
	// Tick, after that pair's in-tick drain loop stops -- see runPair's
	// doc comment. Content-safe by the same rule ObserveProjectionOutcome
	// documents.
	ObserveProjectionDrain(DrainOutcome)
}

// DrainYieldReason is a closed vocabulary naming why one (org, source)
// pair's in-tick drain loop (CHAOS-3826) stopped pulling further batches.
type DrainYieldReason string

const (
	// DrainYieldExhausted: the source reported no further batch available,
	// or reached a terminal build-completion mode -- ordinary steady state.
	DrainYieldExhausted DrainYieldReason = "exhausted"
	// DrainYieldBudgetExceeded: more work was available but this
	// organization's per-tick drain budget (Config.DrainBatchBudget,
	// shared across every source the organization projects) was spent.
	// The next Tick resumes from the checkpoint this tick's last batch
	// advanced to -- no work is lost, only deferred.
	DrainYieldBudgetExceeded DrainYieldReason = "budget_exceeded"
	// DrainYieldError: a batch attempt failed mid-drain; the existing
	// due()/recordBackoff per-pair schedule governs the retry, exactly as
	// it did before CHAOS-3826.
	DrainYieldError DrainYieldReason = "error"
	// DrainYieldContextDone: ctx was cancelled mid-drain.
	DrainYieldContextDone DrainYieldReason = "context_done"
)

// DrainOutcome is CHAOS-3826's telemetry for one (org, source) pair's
// in-tick drain: how many RunOnce attempts this Tick actually made for the
// pair (Batches -- a cost signal: every attempt is a real checkpoint-load
// round-trip even when nothing was available to apply, see
// ProjectionWorker.RunOnce's own !available branch) and why the loop
// stopped (drain-yield-reason).
type DrainOutcome struct {
	OrgID  string
	Source string
	// Batches counts every RunOnce attempt this tick, including the final
	// attempt that discovers the source is exhausted (available=false) --
	// see chaos3826_drain_test.go's TestChaos3826_DrainTelemetryReportsBatchesAndYieldReason
	// for why that attempt is deliberately counted (it is a real round-trip,
	// not free). Codex round-1 F1: this means an ordinary tick with exactly
	// ONE new batch reports Batches=2 (apply + confirm-exhausted), which is
	// correct for cost accounting but is NOT by itself the right signal for
	// "did an actual multi-batch drain happen" -- use Applied for that.
	Batches int
	// Applied counts only the attempts within Batches that actually applied
	// a batch (run.Applied == true). Applied > 1 is the true "the fix drained
	// a real backlog" signal; Applied <= 1 is routine steady state even when
	// Batches == 2 from the mandatory confirm-exhausted attempt.
	Applied     int
	YieldReason DrainYieldReason
	Duration    time.Duration
	At          time.Time
}

type noopObserver struct{}

func (noopObserver) ObserveProjectionOutcome(Outcome)    {}
func (noopObserver) ObserveProjectionDrain(DrainOutcome) {}

// SlogObserver logs every tick outcome through log/slog (codex round-3 F2).
//
// The projector previously defaulted to noopObserver and no production
// construction supplied anything else, so a projection tick that failed --
// including one failing because vector state could not be reconciled (R2-3,
// round-3 F1) -- produced no operational signal at all. A failing tick that
// holds its checkpoint is the correct behavior, but it is only SAFE behavior
// if someone can see it happening; otherwise a stalled organization is
// indistinguishable from an idle one.
//
// Content-safe by the same rule Observer documents: counts, IDs, durations,
// and error text only.
type SlogObserver struct{ Logger *slog.Logger }

func (o SlogObserver) ObserveProjectionOutcome(outcome Outcome) {
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"org_id", outcome.OrgID, "source", outcome.Source,
		"duration_ms", outcome.Duration.Milliseconds(),
	}
	if outcome.Err != nil {
		// Codex round-4 F3: a CLASSIFICATION, never the error's own text.
		//
		// Outcome.Err is whatever a source, checkpoint store, or backend
		// returned. The graph adapter sanitizes its own errors
		// (safeDependencyError), but a ClickHouse driver error or a Postgres
		// checkpoint error arrives here unbounded, and logging it verbatim
		// would put raw dependency text into production telemetry -- breaking
		// the guarantee this observer's own doc comment and
		// docs/operations.md both make.
		//
		// Same discipline as the embedder's model-identity error: name the
		// classified thing, never the received text.
		logger.Error("context_fabric: projection tick failed; checkpoint held for replay",
			append(attrs, "failure_class", classifyOutcomeError(outcome.Err))...)
		return
	}
	logger.Debug("context_fabric: projection tick completed", attrs...)
}

// ObserveProjectionDrain logs CHAOS-3826's per-pair drain summary. Routine
// pairs (the overwhelming common case: nothing new, or exactly one new
// batch, to project) log at Debug, matching ObserveProjectionOutcome's own
// level; an actual multi-batch drain -- the fix doing its job -- logs at
// Info so it is visible without raising the default log level.
//
// Codex round-1 F1: the level decision keys on outcome.Applied, NOT
// outcome.Batches. Batches includes the mandatory confirm-exhausted
// attempt every drain makes once it runs dry, so an ordinary tick with
// exactly one genuinely new batch reports Batches=2 -- gating on Batches
// would log that routine case at Info as "drained multiple batches" even
// though only one batch actually applied.
func (o SlogObserver) ObserveProjectionDrain(outcome DrainOutcome) {
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"org_id", outcome.OrgID, "source", outcome.Source,
		"batches", outcome.Batches, "applied", outcome.Applied, "drain_yield_reason", string(outcome.YieldReason),
		"duration_ms", outcome.Duration.Milliseconds(),
	}
	if outcome.Applied > 1 {
		logger.Info("context_fabric: projection tick drained multiple batches", attrs...)
		return
	}
	logger.Debug("context_fabric: projection tick drain summary", attrs...)
}

// RebuildMarker enforces the CHAOS-3753 codex finding C2 invariant: no code
// path may run incremental projection against a purged-but-not-reset
// graph. PurgeOrganization and resetting every source's checkpoint are two
// separate durable operations with no shared transaction between them (the
// backend and the checkpoint store are different systems), so a crash
// between them must be detectable and resumable rather than silently
// leaving a stale, non-empty checkpoint pointing at data the backend no
// longer has. RebuildMarkerStore (pgprojection) is the production
// implementation; see migrations/postgres/0007_context_fabric_projection_rebuild_markers.sql.
type RebuildMarker interface {
	// BeginRebuild marks orgID as having a rebuild in progress. Idempotent.
	BeginRebuild(ctx context.Context, orgID string) error
	// IsRebuildInProgress reports whether orgID currently has a marker.
	IsRebuildInProgress(ctx context.Context, orgID string) (bool, error)
	// CompleteRebuild clears orgID's marker. Idempotent.
	CompleteRebuild(ctx context.Context, orgID string) error
}

const (
	defaultPollInterval = 15 * time.Second
	defaultConcurrency  = 4
	defaultMaxBackoff   = 5 * time.Minute
	baseBackoff         = 5 * time.Second
	// defaultDrainBatchBudget (CHAOS-3826) is how many EXTRA batches (beyond
	// the one every configured source always attempts each tick) one
	// organization's Tick may pull across all its sources combined before
	// yielding. 500 extra batches at the 200-row page cap is 100,000 rows
	// per tick -- comfortably drains the ~36k-subject trial organization
	// that motivated this ticket (~180 batches) in a single tick, while
	// still bounding a much larger organization's tick so it cannot starve
	// other organizations queued behind it in the same Tick call (see
	// runPair's doc comment).
	defaultDrainBatchBudget = 500
)

type Config struct {
	OrgIDs         []string
	Sources        []SourcePair
	Backend        contextfabric.ProjectionBackend
	Checkpoints    contextfabric.ProjectionCheckpointStore
	RebuildMarkers RebuildMarker // required -- see RebuildMarker's doc comment
	Locker         OrgLocker     // nil -> NoopOrgLocker (in-process mutex only)
	Observer       Observer      // nil -> discarded
	// ReuseInvalidator is optional (CHAOS-3782). When set, Coordinator
	// calls InvalidateOrganizationReuse for orgID immediately after a
	// rebuild (explicit or crash-resumed) completes -- see
	// performRebuild's call site for why watermark equality alone cannot
	// be trusted to catch a rebuild (TRD §19.7.3's drift item D15
	// hazard). Nil disables this hook entirely; answer reuse then relies
	// solely on watermark comparison, which is a known, accepted gap
	// until this is wired in production composition.
	ReuseInvalidator contextfabric.ReuseInvalidator
	// Lifecycle (CHAOS-3898 S2a-2, design brief §3.1/§3.5/item 8) is
	// optional. When set, Coordinator drives every organization rebuild
	// and every CHAOS-3882 divergence recovery through the
	// build-aside-and-swap lifecycle machine (BeginBuild -> per-epoch
	// ticks -> Flip) instead of the legacy in-place PurgeOrganization
	// path (performRebuild), and every steady-state tick reads/advances
	// the checkpoint set of the organization's CURRENT ACTIVE epoch
	// rather than always epoch 0 (design brief §3.4 -- required for
	// correctness the moment any organization has ever flipped: an
	// unmigrated organization's ActiveEpoch is always 0, so this changes
	// nothing until BeginBuild is first called for it). nil preserves the
	// exact pre-CHAOS-3898 behavior byte-for-byte -- see performRebuild's
	// own doc comment. pglifecycle.Store is the production implementation.
	Lifecycle contextfabric.GraphLifecycleStore
	// EpochCheckpoints resolves an epoch-scoped contextfabric.ProjectionCheckpointStore
	// VIEW (design brief §3.4) -- REQUIRED when Lifecycle is set (NewCoordinator
	// refuses the pairing otherwise), ignored when Lifecycle is nil.
	// pgprojection.CheckpointStore.ForEpoch is the production implementation.
	EpochCheckpoints func(epoch int64) contextfabric.ProjectionCheckpointStore
	// RetireScheduler (design brief §3.5) is optional: when set,
	// Coordinator sweeps due epoch retirements once per Tick, under the
	// SAME per-organization single-flight discipline (in-process mutex +
	// OrgLocker) every other per-org operation in this file already has.
	// pglifecycle.RetireExecutor is the production implementation.
	RetireScheduler RetireScheduler
	// LifecycleTelemetry (design brief §5b) is optional: wires
	// cf_checkpoint_epoch_state, computed from data Coordinator already
	// reads (checkpoint cursor ages) that pglifecycle/falkorgraph have no
	// visibility into on their own.
	LifecycleTelemetry contextfabric.GraphLifecycleTelemetry
	// EpochResolverInvalidator (CHAOS-4208) is optional: when set,
	// Coordinator calls Invalidate(orgID) on it immediately after a
	// beginLifecycleBuild or Flip transition commits, so the FalkorDB
	// adapter's OrgEpochResolver (a separate object over the same
	// Postgres store -- see EpochResolverInvalidator's own doc comment)
	// stops serving a stale pre-transition epoch for the rest of its
	// bounded lease. Nil preserves the pre-CHAOS-4208 behavior: readers
	// simply wait out the lease, which is bounded staleness, not
	// incorrectness. pglifecycle.CachedResolver satisfies this directly.
	EpochResolverInvalidator contextfabric.EpochResolverInvalidator
	// GraceWindow (design brief D11, operator-set) is how long a flipped
	// epoch's predecessor is retained before it becomes eligible for
	// retirement -- Flip's own graceWindow parameter. Defaults to 24h
	// when Lifecycle is set and this is <= 0; ignored when Lifecycle is
	// nil.
	GraceWindow  time.Duration
	PollInterval time.Duration
	Concurrency  int
	MaxBackoff   time.Duration
	// DrainBatchBudget (CHAOS-3826) bounds runPair/runBuildPair's in-tick
	// drain -- see defaultDrainBatchBudget's doc comment. 0 (unset)
	// defaults; a NEGATIVE value explicitly disables extra draining (every
	// source still gets its one due()-gated attempt per tick, exactly the
	// pre-CHAOS-3826 cadence) -- unlike every other numeric knob on this
	// Config, 0 is not itself a meaningful "disabled" value here (a
	// disabled drain still makes the one mandatory attempt), so it cannot
	// double as the sentinel the way Concurrency/PollInterval's zero value
	// does.
	DrainBatchBudget int
	Now              func() time.Time
	Logger           *slog.Logger
}

// RetireScheduler drives due per-epoch retirements to completion --
// pglifecycle.RetireExecutor is the production implementation. Defined here
// (an interface this package accepts, not a type it imports) rather than
// depending on pglifecycle directly, matching RebuildMarker/OrgLocker's own
// convention: this package stays backend-neutral; composition supplies the
// concrete Postgres-backed type.
type RetireScheduler interface {
	// DueRetirements lists every retirement whose drain bound has already
	// elapsed -- read-only, never itself mutates anything.
	DueRetirements(ctx context.Context) ([]contextfabric.EpochRetirement, error)
	// RunOne drives ONE (org, epoch) retirement to completion (or refuses
	// it, recording why via Telemetry) -- see pglifecycle.RetireExecutor.RunOne's
	// own doc comment for the guard sequence and its known gaps.
	RunOne(ctx context.Context, orgID string, epoch int64) error
}

// Coordinator schedules contextfabric.ProjectionWorker.RunOnce across every
// configured organization and source, enforcing single-flight per
// organization (CHAOS-3753's amendment), bounded retry with backoff per
// (org, source) pair, cancellation, and failure isolation: one pair's error
// never blocks another org or source.
type Coordinator struct {
	orgIDs           []string
	sourceNames      []string
	workers          map[string]*contextfabric.ProjectionWorker
	sources          map[string]contextfabric.ProjectionSource // CHAOS-3887: needed for the freshness signal's current_source_version, which ProjectionWorker does not expose
	backend          contextfabric.ProjectionBackend
	checkpoints      contextfabric.ProjectionCheckpointStore
	rebuildMarkers   RebuildMarker
	locker           OrgLocker
	observer         Observer
	reuseInvalidator contextfabric.ReuseInvalidator
	lifecycle        contextfabric.GraphLifecycleStore
	epochCheckpoints func(int64) contextfabric.ProjectionCheckpointStore
	retireScheduler  RetireScheduler
	lifecycleTelem   contextfabric.GraphLifecycleTelemetry
	epochInvalidator contextfabric.EpochResolverInvalidator
	graceWindow      time.Duration
	poll             time.Duration
	concurrency      int
	maxBackoff       time.Duration
	drainBudget      int
	now              func() time.Time
	logger           *slog.Logger

	orgMu sync.Map // orgID -> *sync.Mutex, in-process first line of defense

	backoffMu sync.Mutex
	backoff   map[string]*pairBackoff // "orgID\x00source" -> state

	// buildStarted (CHAOS-3826 telemetry) records when THIS process opened
	// an org's currently-building epoch (beginLifecycleBuild's success
	// path), purely so runBuildTick's Flip-success log line can report the
	// build's wall-clock duration. In-process only, like orgMu/backoff: a
	// coordinator restart mid-build loses the start time and simply omits
	// the field, which is a known, accepted gap -- the lifecycle state
	// machine itself (BeginBuild/Flip/durable progress rows) is unaffected.
	buildStarted sync.Map // orgID -> time.Time
}

// defaultGraceWindow is design brief D11's operator-set retention window,
// applied only when Config.Lifecycle is set and Config.GraceWindow is
// unset. 24h matches D11's own example range (24-72h) at its shorter,
// more conservative end.
const defaultGraceWindow = 24 * time.Hour

type pairBackoff struct {
	consecutiveFailures int
	nextAttempt         time.Time
}

// allowsOrg reports whether orgID is in the coordinator's configured
// allowlist (Config.OrgIDs / ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS). Every
// entry point that acts on an organization by ID -- Tick (implicitly, since
// it only ever iterates c.orgIDs) and Rebuild (explicitly, since it takes
// an arbitrary caller-supplied ID) -- must go through this so an operator
// invoking `acr-projector rebuild --org <id>` can never purge a tenant the
// deployment was never configured to project.
func (c *Coordinator) allowsOrg(orgID string) bool {
	for _, allowed := range c.orgIDs {
		if allowed == orgID {
			return true
		}
	}
	return false
}

func NewCoordinator(cfg Config) (*Coordinator, error) {
	if cfg.Backend == nil || cfg.Checkpoints == nil {
		return nil, errors.New("projectionrun: backend and checkpoint store are required")
	}
	if cfg.RebuildMarkers == nil {
		return nil, errors.New("projectionrun: rebuild markers are required (see RebuildMarker's doc comment)")
	}
	if len(cfg.Sources) == 0 {
		return nil, errors.New("projectionrun: at least one source is required")
	}
	if cfg.Lifecycle != nil && cfg.EpochCheckpoints == nil {
		return nil, errors.New("projectionrun: EpochCheckpoints is required when Lifecycle is set")
	}
	if cfg.Lifecycle != nil && cfg.GraceWindow <= 0 {
		cfg.GraceWindow = defaultGraceWindow
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
	switch {
	case cfg.DrainBatchBudget == 0:
		cfg.DrainBatchBudget = defaultDrainBatchBudget
	case cfg.DrainBatchBudget < 0:
		cfg.DrainBatchBudget = 0
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
	sources := make(map[string]contextfabric.ProjectionSource, len(cfg.Sources))
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
		sources[pair.Name] = pair.Source
		sourceNames = append(sourceNames, pair.Name)
	}
	return &Coordinator{
		orgIDs: append([]string(nil), cfg.OrgIDs...), sourceNames: sourceNames, workers: workers, sources: sources,
		backend: cfg.Backend, checkpoints: cfg.Checkpoints, rebuildMarkers: cfg.RebuildMarkers,
		locker: cfg.Locker, observer: cfg.Observer, reuseInvalidator: cfg.ReuseInvalidator,
		lifecycle: cfg.Lifecycle, epochCheckpoints: cfg.EpochCheckpoints, retireScheduler: cfg.RetireScheduler,
		lifecycleTelem: cfg.LifecycleTelemetry, epochInvalidator: cfg.EpochResolverInvalidator, graceWindow: cfg.GraceWindow,
		poll: cfg.PollInterval, concurrency: cfg.Concurrency,
		maxBackoff: cfg.MaxBackoff, drainBudget: cfg.DrainBatchBudget, now: cfg.Now, logger: cfg.Logger, backoff: make(map[string]*pairBackoff),
	}, nil
}

// Rebuild purges an organization's projected graph state and resets every
// configured source's checkpoint to the zero cursor, under the same
// single-flight guard as ordinary ticks (org lock, then in-process mutex).
// The next Tick for this organization then replays each source's bounded
// full-snapshot batch (see devhealthsource's empty-cursor convention) --
// Rebuild itself does not project anything; it only clears the way.
func (c *Coordinator) Rebuild(ctx context.Context, orgID string) error {
	if strings.TrimSpace(orgID) == "" {
		return errors.New("projectionrun: organization is required")
	}
	if !c.allowsOrg(orgID) {
		return fmt.Errorf("projectionrun: organization %s is not in the configured allowlist", orgID)
	}
	mutexAny, _ := c.orgMu.LoadOrStore(orgID, &sync.Mutex{})
	mutex := mutexAny.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	unlock, err := c.locker.Lock(ctx, orgID)
	if err != nil {
		return fmt.Errorf("projectionrun: acquire organization lock for rebuild: %w", err)
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			c.logger.WarnContext(ctx, "projection organization unlock failed after rebuild", "org_id", orgID, "failure_class", classifyOutcomeError(unlockErr))
		}
	}()
	if c.lifecycle != nil {
		_, err := c.beginLifecycleBuild(ctx, orgID)
		return err
	}
	return c.performRebuild(ctx, orgID)
}

// Rollback restores orgID's active epoch to the one a prior flip retained
// during its grace window (design brief §3.1 step 4/§3.4), under the same
// single-flight guard as Rebuild. Legal only while the organization's
// lifecycle row is in LifecycleStatusGrace; a rollback attempted outside
// that window returns contextfabric.ErrLifecycleTransitionRefused (grace
// has already ended -- begin_retire or the retire executor may already have
// acted) or contextfabric.ErrLifecycleConflict (a race). The restored
// epoch's own checkpoint set was frozen at flip time and is untouched by
// this call -- ordinary ticks resume from it on the very next tick and
// replay exactly the gap (pglifecycle's own TestRollback_ResumesOwnCheckpoints_NoRowSkipped
// pins this at the storage layer). Fires the SAME reuse-invalidation bump a
// flip fires (design brief §3.4's "belt and braces": the epoch predicate
// alone would already reject stale reuse rows, but the bump is immediate).
func (c *Coordinator) Rollback(ctx context.Context, orgID string) error {
	if c.lifecycle == nil {
		return errors.New("projectionrun: rollback requires a configured graph lifecycle store")
	}
	if strings.TrimSpace(orgID) == "" {
		return errors.New("projectionrun: organization is required")
	}
	if !c.allowsOrg(orgID) {
		return fmt.Errorf("projectionrun: organization %s is not in the configured allowlist", orgID)
	}
	mutexAny, _ := c.orgMu.LoadOrStore(orgID, &sync.Mutex{})
	mutex := mutexAny.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()

	unlock, err := c.locker.Lock(ctx, orgID)
	if err != nil {
		return fmt.Errorf("projectionrun: acquire organization lock for rollback: %w", err)
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			c.logger.WarnContext(ctx, "projection organization unlock failed after rollback", "org_id", orgID, "failure_class", classifyOutcomeError(unlockErr))
		}
	}()

	row, found, err := c.lifecycle.Get(ctx, orgID)
	if err != nil {
		return fmt.Errorf("projectionrun: read graph lifecycle row: %w", err)
	}
	if !found || row.Status != contextfabric.LifecycleStatusGrace {
		return fmt.Errorf("%w: organization is not currently in a rollback-eligible grace window", contextfabric.ErrLifecycleTransitionRefused)
	}
	rolled, err := c.lifecycle.Rollback(ctx, orgID, row.ActiveEpoch, c.now())
	if err != nil {
		return fmt.Errorf("projectionrun: rollback: %w", err)
	}
	c.logger.WarnContext(ctx, "context_fabric: graph epoch rollback", "org_id", orgID, "from_epoch", row.ActiveEpoch, "to_epoch", rolled.ActiveEpoch)
	c.invalidateEpochResolution(ctx, orgID, contextfabric.LifecycleTransitionRollback)
	if c.reuseInvalidator != nil {
		if invalidateErr := c.reuseInvalidator.InvalidateOrganizationReuse(ctx, orgID); invalidateErr != nil {
			c.logger.WarnContext(ctx, "invalidate answer reuse after rollback failed", "org_id", orgID, "failure_class", classifyOutcomeError(invalidateErr))
		}
	}
	return nil
}

// beginLifecycleBuild is the CHAOS-3898 S2a-2 replacement for performRebuild's
// purge-then-reset sequence (design brief item 8's MANDATORY conversion): it
// durably opens a build-aside epoch (BeginBuild) and returns immediately --
// matching performRebuild's OWN fast-return contract exactly (Rebuild's own
// doc comment: "the next serve tick replays a full snapshot from scratch").
// This call never itself projects anything or touches the backend; the
// actual replay happens over subsequent ticks, driven by runOrgLifecycle's
// build branch (runBuildTick) below.
//
// Idempotent the same way performRebuild is: BeginBuild refuses
// (ErrLifecycleTransitionRefused) when a build or grace window is already
// open for this organization, which is treated as "already in progress"
// rather than an error -- an operator re-running `rebuild --org` while a
// prior rebuild is still replaying must not fail loudly for doing the
// obviously safe thing.
//
// Returns opened=true only when THIS call actually transitioned the row
// serving->building (CHAOS-4208): a caller (recoverFromDivergenceLifecycle)
// must be able to tell that apart from every other reason BeginBuild can
// return a nil error -- a build genuinely already in progress, OR the row
// being refused for an entirely different, non-error reason such as a
// post-flip grace window, which BeginBuild's own SQL predicate (WHERE
// status = 'serving') refuses identically to "already building" but which
// is NOT "already in progress": nothing will start replaying, and a caller
// that can't tell the difference ends up logging that it did (the
// self-contradicting "opened a build-aside epoch" WARN this ticket fixes).
func (c *Coordinator) beginLifecycleBuild(ctx context.Context, orgID string) (opened bool, err error) {
	_, err = c.lifecycle.BeginBuild(ctx, orgID, c.sourceNames, c.now())
	if err == nil {
		c.logger.InfoContext(ctx, "context_fabric: build-aside epoch opened; replay will proceed over subsequent ticks", "org_id", orgID)
		c.buildStarted.Store(orgID, c.now())
		c.invalidateEpochResolution(ctx, orgID, contextfabric.LifecycleTransitionBeginBuild)
		return true, nil
	}
	if !errors.Is(err, contextfabric.ErrLifecycleTransitionRefused) {
		return false, fmt.Errorf("projectionrun: begin build-aside epoch: %w", err)
	}
	// Refused. Re-read to report WHY, distinguishing "a build is already
	// open" (routine, matches this call's own intent) from any other
	// observed status (e.g. grace) that refuses BeginBuild for a reason
	// that has nothing to do with a build already running. A re-read
	// FAILURE (a transient dependency error, context cancellation) must
	// propagate as an error, never default to "serving" and report success
	// -- CHAOS-4208 round-2: silently treating "I don't know the current
	// state" as a benign, already-handled refusal let an operator's Rebuild
	// call report nil and let automatic recovery clear its backoff as
	// though nothing were wrong.
	row, found, getErr := c.lifecycle.Get(ctx, orgID)
	if getErr != nil {
		return false, fmt.Errorf("projectionrun: begin build-aside epoch: re-read lifecycle row after CAS refusal: %w", getErr)
	}
	if found && row.Status == contextfabric.LifecycleStatusBuilding {
		c.logger.InfoContext(ctx, "context_fabric: build-aside epoch already open for this organization; not restarting", "org_id", orgID)
		return false, nil
	}
	status := contextfabric.LifecycleStatusServing
	if found {
		status = row.Status
	}
	c.logger.InfoContext(ctx, "context_fabric: cannot open a build-aside epoch right now; organization is not in a rebuildable state",
		"org_id", orgID, "observed_status", string(status))
	return false, nil
}

// invalidateEpochResolution notifies a wired EpochResolverInvalidator that
// a lifecycle transition just committed for orgID (CHAOS-4208's fix for
// the missing "flip/rollback call this as a courtesy" wiring
// EpochResolverInvalidator's own doc comment describes). Nil-safe --
// EpochResolverInvalidator is optional, matching every other invalidator
// field on Config (e.g. ReuseInvalidator).
func (c *Coordinator) invalidateEpochResolution(ctx context.Context, orgID string, transition contextfabric.LifecycleTransition) {
	if c.epochInvalidator == nil {
		return
	}
	c.epochInvalidator.Invalidate(orgID)
	if c.lifecycleTelem != nil {
		c.lifecycleTelem.RecordEpochResolverInvalidation(ctx, orgID, transition)
	}
}

// performRebuild is the crash-resumable rebuild sequence, callable either as
// an explicit Rebuild or as runOrg's automatic resume of a rebuild a prior
// crash interrupted. Every step is idempotent (BeginRebuild, PurgeOrganization
// per ADR 0007, and a checkpoint reset that's a no-op when already ""), so
// re-running the whole sequence from scratch is always safe regardless of
// which step a previous attempt actually reached. The caller must already
// hold both the in-process mutex and the OrgLocker for orgID.
//
// Order matters for the CHAOS-3753 codex finding C2 invariant (no code path
// may run incremental projection against a purged-but-not-reset graph): the
// marker is durably persisted BEFORE the purge, so a crash at any point
// after that leaves it in place; runOrg refuses ordinary ticks and instead
// resumes this exact sequence while the marker is present. It is cleared
// only after every checkpoint is confirmed reset.
func (c *Coordinator) performRebuild(ctx context.Context, orgID string) error {
	if err := c.rebuildMarkers.BeginRebuild(ctx, orgID); err != nil {
		return fmt.Errorf("projectionrun: mark rebuild in progress: %w", err)
	}
	if err := c.backend.PurgeOrganization(ctx, orgID); err != nil {
		return fmt.Errorf("projectionrun: purge organization: %w", err)
	}
	if err := c.resetAllCheckpoints(ctx, orgID); err != nil {
		return err
	}
	// CHAOS-3782, AC-3782-4 (Codex round-1 F5): invalidate answer reuse
	// for this organization BEFORE clearing the rebuild marker, and treat
	// a failure here as a rebuild failure (the marker stays set, so the
	// NEXT tick's crash-resume path -- runOrg's IsRebuildInProgress check
	// -- reruns this entire idempotent sequence, including this call,
	// until it succeeds).
	//
	// This order matters. The prior order (invalidate AFTER
	// CompleteRebuild, best-effort/log-and-continue on failure) had a
	// silent crash window: a crash or a failure between clearing the
	// marker and recording the invalidation left the marker gone --
	// runOrg would then see IsRebuildInProgress == false and never retry
	// -- with AC-3782-4's guarantee permanently unmet for that
	// organization and nothing surfacing it beyond a log line (D15's
	// watermark/window bounds are the OTHER, independent safety net, not
	// a substitute for this one actually firing). Invalidate-then-complete
	// closes that window: InvalidateOrganizationReuse is idempotent (its
	// ON CONFLICT clause only ever moves invalidated_at forward), so a
	// crash between the two steps, or a resumed retry after a transient
	// failure, safely re-invalidates rather than skipping it.
	if c.reuseInvalidator != nil {
		if err := c.reuseInvalidator.InvalidateOrganizationReuse(ctx, orgID); err != nil {
			return fmt.Errorf("projectionrun: invalidate answer reuse: %w", err)
		}
	}
	if err := c.rebuildMarkers.CompleteRebuild(ctx, orgID); err != nil {
		return fmt.Errorf("projectionrun: clear rebuild marker: %w", err)
	}
	c.logger.InfoContext(ctx, "projection organization rebuilt", "org_id", orgID)
	return nil
}

func (c *Coordinator) resetAllCheckpoints(ctx context.Context, orgID string) error {
	var resetErrs []error
	for _, source := range c.sourceNames {
		current, err := c.checkpoints.LoadProjectionCheckpoint(ctx, orgID, source)
		if err != nil {
			resetErrs = append(resetErrs, fmt.Errorf("load checkpoint for %s: %w", source, err))
			continue
		}
		// CHAOS-3779 codex round-4 M2: Cursor == "" alone no longer means
		// "nothing to reset" -- the H2-residual claim mechanism
		// (ProjectionWorker.RunOnce, internal/contextfabric/projector.go)
		// can durably persist a checkpoint with Cursor == "" but a
		// non-empty SourceVersion (zero progress claimed under a version
		// that then failed to apply). Skipping that checkpoint here would
		// leave the claimed SourceVersion in place forever: the very
		// mismatch guard the claim exists to protect would then refuse
		// EVERY future batch for this organization, including the ones
		// this rebuild is supposed to unblock -- a permanent wedge, not
		// merely a missed reset. Only skip when BOTH fields are already
		// empty; that is the sole state that is genuinely "never
		// projected, or already reset."
		if current.Cursor == "" && current.SourceVersion == "" {
			continue
		}
		reset := contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: source, UpdatedAt: c.now().UTC()}
		if err := c.checkpoints.CompareAndSwapProjectionCheckpoint(ctx, current, reset); err != nil {
			resetErrs = append(resetErrs, fmt.Errorf("reset checkpoint for %s: %w", source, err))
		}
	}
	if len(resetErrs) > 0 {
		return errors.Join(resetErrs...)
	}
	return nil
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

// tickFreshnessStats is the CHAOS-3887 (H12) fleet aggregate: one Tick's
// per-organization freshness classification, so "how many organizations are
// pending a rebuild" is a single logged number instead of a grep across
// every replica's per-pair lines. Counters, never IDs -- content-safe.
//
// Classification is per ORGANIZATION, not per (org, source) pair: an
// organization counts as rebuildRequired if ANY of its configured sources'
// freshness check found checkpoint_source_version != current_source_version
// this tick. ok means every pair that was actually evaluated this tick came
// back fresh. backoff means no pair was evaluated at all this tick (the
// organization's lock was busy, a rebuild was in progress, or every source
// is presently in its per-pair failure backoff window) -- distinct from ok
// because "fresh" is not something this tick observed for it.
type tickFreshnessStats struct {
	orgsOK              int64
	orgsRebuildRequired int64
	orgsBackoff         int64
	// orgsDivergenceRecovered (CHAOS-3882) counts organizations for which
	// THIS tick detected checkpoint-vs-store divergence and drove an
	// automatic recovery (successful or not) -- distinct from
	// orgsRebuildRequired, which is CHAOS-3887's "an operator must run
	// `acr-projector rebuild --org`" signal. This one means the opposite:
	// no operator action was needed, the coordinator already acted.
	orgsDivergenceRecovered int64
}

func (s *tickFreshnessStats) recordOK()              { atomic.AddInt64(&s.orgsOK, 1) }
func (s *tickFreshnessStats) recordRebuildRequired() { atomic.AddInt64(&s.orgsRebuildRequired, 1) }
func (s *tickFreshnessStats) recordBackoff()         { atomic.AddInt64(&s.orgsBackoff, 1) }
func (s *tickFreshnessStats) recordDivergenceRecovered() {
	atomic.AddInt64(&s.orgsDivergenceRecovered, 1)
}

// Tick runs one bounded-concurrency pass over every configured organization.
// Exported so hosting composition (and tests) can drive ticks explicitly
// instead of waiting on PollInterval.
func (c *Coordinator) Tick(ctx context.Context) {
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	stats := &tickFreshnessStats{}
	for _, orgID := range c.orgIDs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(orgID string) {
			defer wg.Done()
			defer func() { <-sem }()
			c.runOrg(ctx, orgID, stats)
		}(orgID)
	}
	wg.Wait()
	// CHAOS-3887 (H12): logged every tick, not only when something is
	// wrong, so "no signal" and "zero orgs pending rebuild" stay
	// distinguishable -- the same reasoning SlogObserver's doc comment
	// gives for logging successful ticks, not just failures.
	c.logger.InfoContext(ctx, "context_fabric: projection tick freshness summary",
		"orgs_ok", atomic.LoadInt64(&stats.orgsOK),
		"orgs_rebuild_required", atomic.LoadInt64(&stats.orgsRebuildRequired),
		"orgs_backoff", atomic.LoadInt64(&stats.orgsBackoff),
		"pending_rebuild_orgs_total", atomic.LoadInt64(&stats.orgsRebuildRequired),
		// CHAOS-3882: how many organizations this tick found in
		// checkpoint-vs-store divergence and drove an automatic recovery
		// for -- see checkpointStoreDiverged's doc comment.
		"orgs_divergence_recovered", atomic.LoadInt64(&stats.orgsDivergenceRecovered),
	)
	// CHAOS-3898 S2a-2 (design brief §3.1 step 5/§3.5): sweep organizations
	// whose grace window has elapsed into begin_retire (creating the
	// grace_expired EpochRetirement record), THEN sweep every retirement
	// whose OWN drain bound has elapsed to actual deletion -- a no-op pair
	// when Lifecycle/RetireScheduler are unconfigured. Order matters only
	// for freshness (a just-expired grace can be picked up by the SAME
	// tick's retirement sweep once its own drain bound separately elapses,
	// not this one).
	c.sweepGraceExpirations(ctx)
	c.sweepRetirements(ctx)
}

// runOrg enforces single-flight per organization across a whole multi-source
// pass: the in-process mutex is a fast, always-on first line of defense; the
// OrgLocker (PostgresOrgLocker in production) makes the guarantee hold
// across acr-projector replicas too. Both are non-blocking (TryLock /
// pg_try_advisory_lock): a busy organization is skipped this tick, not
// queued, so one slow organization can never starve the others.
//
// Dispatches to runOrgLifecycle when Config.Lifecycle is set (CHAOS-3898
// S2a-2's build-aside-and-swap conversion), else to runOrgLegacy -- the
// EXACT pre-CHAOS-3898 marker-based purge/reset behavior, byte-for-byte
// unchanged, which every existing composition root and test that does not
// configure Lifecycle continues to exercise.
func (c *Coordinator) runOrg(ctx context.Context, orgID string, stats *tickFreshnessStats) {
	mutexAny, _ := c.orgMu.LoadOrStore(orgID, &sync.Mutex{})
	mutex := mutexAny.(*sync.Mutex)
	if !mutex.TryLock() {
		stats.recordBackoff()
		return
	}
	defer mutex.Unlock()

	unlock, err := c.locker.Lock(ctx, orgID)
	if err != nil {
		stats.recordBackoff()
		if !errors.Is(err, ErrOrgLocked) {
			c.logger.WarnContext(ctx, "projection organization lock failed", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		}
		return
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			c.logger.WarnContext(ctx, "projection organization unlock failed", "org_id", orgID, "failure_class", classifyOutcomeError(unlockErr))
		}
	}()

	if c.lifecycle != nil {
		c.runOrgLifecycle(ctx, orgID, stats)
		return
	}
	c.runOrgLegacy(ctx, orgID, stats)
}

// runOrgLegacy is the pre-CHAOS-3898 per-org tick body, unchanged: marker-based
// crash-resume, epoch-0-pinned divergence check, epoch-0-pinned per-source
// ticking. The caller (runOrg) already holds both organization locks.
func (c *Coordinator) runOrgLegacy(ctx context.Context, orgID string, stats *tickFreshnessStats) {
	// CHAOS-3753 codex finding C2 invariant: never run incremental
	// projection against a purged-but-not-reset graph. A marker present
	// here means a prior Rebuild (this replica or another) crashed between
	// purging the backend and confirming every checkpoint reset -- resume
	// that exact sequence instead of proceeding, and skip ordinary
	// projection for this org this tick regardless of outcome (the marker
	// state, not a stale checkpoint, is the true source of truth right now).
	if inProgress, err := c.rebuildMarkers.IsRebuildInProgress(ctx, orgID); err != nil {
		stats.recordBackoff()
		c.logger.WarnContext(ctx, "check rebuild marker failed; skipping tick", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		return
	} else if inProgress {
		stats.recordBackoff()
		if err := c.performRebuild(ctx, orgID); err != nil {
			c.logger.WarnContext(ctx, "resume interrupted rebuild failed; will retry next tick", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		} else {
			c.logger.InfoContext(ctx, "resumed an interrupted rebuild", "org_id", orgID)
		}
		return
	}

	// CHAOS-3882: never run ordinary projection against an organization
	// whose durable checkpoint has outrun the graph backend's own state --
	// the incident this ticket closes. Checked AFTER the rebuild-in-progress
	// resume above (a marker already in flight is the higher-priority signal)
	// and BEFORE the ordinary per-source loop below, exactly like that
	// resume branch: skip ordinary projection this tick regardless of
	// outcome and drive recovery through the SAME performRebuild sequence,
	// under the SAME org lock this method already holds.
	if c.checkpointStoreDiverged(ctx, orgID, c.checkpoints) {
		c.recoverFromDivergence(ctx, orgID, stats)
		return
	}

	evaluated, stale := false, false
	budget := c.drainBudget // CHAOS-3826: shared across every source this org drains this tick
	for _, source := range c.sourceNames {
		if ctx.Err() != nil {
			return
		}
		pairEvaluated, pairStale := c.runPair(ctx, orgID, source, c.checkpoints, &budget)
		evaluated = evaluated || pairEvaluated
		stale = stale || pairStale
	}
	switch {
	case !evaluated:
		stats.recordBackoff()
	case stale:
		stats.recordRebuildRequired()
	default:
		stats.recordOK()
	}
}

// runOrgLifecycle is runOrgLegacy's CHAOS-3898 S2a-2 replacement, active
// whenever Config.Lifecycle is set (design brief item 8's MANDATORY
// conversion): the lifecycle row itself -- not a separate marker table --
// is the durable "is a build in progress" signal, and steady-state ticking
// reads/advances the organization's CURRENT ACTIVE epoch's checkpoint set
// (design brief §3.4), not always epoch 0. The caller (runOrg) already
// holds both organization locks.
func (c *Coordinator) runOrgLifecycle(ctx context.Context, orgID string, stats *tickFreshnessStats) {
	row, found, err := c.lifecycle.Get(ctx, orgID)
	if err != nil {
		stats.recordBackoff()
		c.logger.WarnContext(ctx, "read graph lifecycle row failed; skipping tick", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		return
	}
	if found && row.Status == contextfabric.LifecycleStatusBuilding {
		stats.recordBackoff()
		c.runBuildTick(ctx, orgID, row)
		return
	}

	epoch := int64(0)
	checkpoints := c.checkpoints
	if found && row.ActiveEpoch != 0 {
		epoch = row.ActiveEpoch
		checkpoints = c.epochCheckpoints(epoch)
	}

	if c.checkpointStoreDiverged(ctx, orgID, checkpoints) {
		c.recoverFromDivergenceLifecycle(ctx, orgID, stats)
		return
	}

	evaluated, stale := false, false
	budget := c.drainBudget // CHAOS-3826: shared across every source this org drains this tick
	for _, source := range c.sourceNames {
		if ctx.Err() != nil {
			return
		}
		pairEvaluated, pairStale := c.runPair(ctx, orgID, source, checkpoints, &budget)
		evaluated = evaluated || pairEvaluated
		stale = stale || pairStale
	}
	c.recordCheckpointEpochState(ctx, orgID, epoch, contextfabric.CheckpointEpochActive, checkpoints)
	switch {
	case !evaluated:
		stats.recordBackoff()
	case stale:
		stats.recordRebuildRequired()
	default:
		stats.recordOK()
	}
}

// runBuildTick drives one round of per-source ticks for an organization
// whose lifecycle row is LifecycleStatusBuilding (design brief §3.1 step 2,
// §3.3): every configured source's tick writes into TargetEpoch's own
// checkpoint set and graph key (the latter resolved transparently by the
// backend's own KeyResolver -- see falkorgraph.Config.EpochResolver), and
// this method classifies each tick's outcome into design brief item 5's
// four completion shapes, persisting progress via RecordSourceProgress
// EVERY tick (not only on completion) so a source's cumulative
// rows_projected survives a crash/restart between ticks -- see
// classifyBuildCompletion's own doc comment. Once every required source has
// reported a non-pending mode, it attempts Flip; Flip's own gate refuses
// harmlessly until that is true, so calling it unconditionally every tick
// once a build is open is always safe.
func (c *Coordinator) runBuildTick(ctx context.Context, orgID string, row contextfabric.OrgGraphLifecycle) {
	if row.TargetEpoch == nil {
		c.logger.WarnContext(ctx, "graph lifecycle row is building with no target epoch; skipping", "org_id", orgID)
		return
	}
	targetEpoch := *row.TargetEpoch
	checkpoints := c.epochCheckpoints(targetEpoch)

	progress, err := c.lifecycle.SourceProgress(ctx, orgID, targetEpoch)
	if err != nil {
		c.logger.WarnContext(ctx, "read build source progress failed; skipping build tick", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		return
	}
	byName := make(map[string]contextfabric.BuildSourceProgress, len(progress))
	for _, p := range progress {
		byName[p.Source] = p
	}

	// Iterates row.RequiredSources -- the set FROZEN at BeginBuild -- not
	// c.sourceNames (today's live config). A source added to configuration
	// after this build opened must not retroactively become required (it
	// was never frozen into the flip gate); a source REMOVED from
	// configuration while a build is open must still surface as a loud,
	// permanently-pending gap rather than the build silently ticking
	// c.sourceNames and never reaching it -- the exact "a source that
	// cannot report exhaustion MUST fail the flip gate, never silently
	// pass" rule (design brief §9 item 3) applies here too.
	// CHAOS-3826: one shared budget across every required source's
	// in-tick drain this organization's build tick performs -- see
	// runPair's doc comment for the fairness rationale, which applies
	// identically here (a large-backlog build must not starve other
	// organizations' next tick).
	budget := c.drainBudget
	for _, source := range row.RequiredSources {
		if ctx.Err() != nil {
			return
		}
		if existing, ok := byName[source]; ok && existing.CompletionMode != contextfabric.BuildCompletionPending {
			continue // already terminal for this build; nothing more to do
		}
		projectionSource, configured := c.sources[source]
		if !configured {
			c.logger.WarnContext(ctx, "required build source is no longer configured; flip will remain blocked until it is restored", "org_id", orgID, "source", source)
			continue
		}
		if enablement, ok := projectionSource.(contextfabric.ProjectionSourceEnablement); ok && !enablement.Enabled() {
			if rerr := c.lifecycle.RecordSourceProgress(ctx, orgID, targetEpoch, source, contextfabric.BuildCompletionDisabledAtFreeze, 0, c.now()); rerr != nil {
				c.logger.WarnContext(ctx, "record disabled-at-freeze source progress failed", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(rerr))
			}
			continue
		}
		priorRows := byName[source].RowsProjected
		c.runBuildPair(ctx, orgID, source, targetEpoch, checkpoints, priorRows, &budget)
	}
	c.recordCheckpointEpochState(ctx, orgID, targetEpoch, contextfabric.CheckpointEpochBuilding, checkpoints)

	flipped, err := c.lifecycle.Flip(ctx, orgID, targetEpoch, c.graceWindow, c.now())
	switch {
	case err == nil:
		attrs := []any{"org_id", orgID, "from_epoch", row.ActiveEpoch, "to_epoch", flipped.ActiveEpoch}
		// CHAOS-3826: report the build's wall-clock duration when this
		// process is the one that opened it (buildStarted's doc comment).
		if started, ok := c.buildStarted.LoadAndDelete(orgID); ok {
			attrs = append(attrs, "build_wall_clock_ms", c.now().Sub(started.(time.Time)).Milliseconds())
		}
		c.logger.InfoContext(ctx, "context_fabric: graph epoch flip", attrs...)
		c.invalidateEpochResolution(ctx, orgID, contextfabric.LifecycleTransitionFlip)
		if c.reuseInvalidator != nil {
			if invalidateErr := c.reuseInvalidator.InvalidateOrganizationReuse(ctx, orgID); invalidateErr != nil {
				c.logger.WarnContext(ctx, "invalidate answer reuse after flip failed", "org_id", orgID, "failure_class", classifyOutcomeError(invalidateErr))
			}
		}
	case errors.Is(err, contextfabric.ErrLifecycleTransitionRefused):
		// Expected, ordinary mid-build state: not every required source has
		// reported a terminal completion yet. Next tick tries again.
	case errors.Is(err, contextfabric.ErrLifecycleConflict):
		c.logger.WarnContext(ctx, "flip lost a lifecycle CAS race", "org_id", orgID, "failure_class", classifyOutcomeError(err))
	default:
		c.logger.WarnContext(ctx, "flip attempt failed", "org_id", orgID, "failure_class", classifyOutcomeError(err))
	}
}

// runBuildPair drains ONE source's build-epoch batches within THIS tick
// (CHAOS-3826), under the SAME due()/recordBackoff() per-pair scheduling
// gate ordinary runPair ticks use (a distinct keyspace, "\x00build\x00",
// so a build tick and a steady-state tick for the same (org, source)
// never share backoff state). The first attempt is always made
// (due()-gated only, preserving every required source's pre-3826 per-tick
// cadence); once a batch applies and the build is still non-terminal
// (classifyBuildCompletion's pending case -- more pages remain), the next
// batch is fetched immediately under the SAME org lock instead of waiting
// a full poll interval, bounded by budget (shared across every source
// this organization's build tick drains -- see the call site and
// runPair's doc comment for the fairness rationale). RecordSourceProgress
// is durably upserted after every applied batch, not only at the end, so
// a mid-drain failure never loses credit for the batches that DID apply.
func (c *Coordinator) runBuildPair(ctx context.Context, orgID, source string, epoch int64, checkpoints contextfabric.ProjectionCheckpointStore, priorRows int64, budget *int) {
	key := orgID + "\x00build\x00" + source
	started := c.now()
	total := priorRows
	batches, applied := 0, 0
	reason := DrainYieldExhausted
	// lastMode/progressStale track the durable RecordSourceProgress write
	// that would need retrying if the loop exits before it succeeds --
	// see the finalizing write below.
	var lastMode contextfabric.BuildCompletionMode
	progressStale := false
	for {
		if !c.due(key) {
			break
		}
		batches++ // Codex round-2 F3: every attempt counts, matching runPair -- a worker-construction or RunOnce failure is still a real round-trip.
		attemptStarted := c.now()
		worker, werr := c.workerFor(source, checkpoints)
		if werr != nil {
			c.recordBackoff(key, werr)
			c.logger.WarnContext(ctx, "build tick worker construction failed", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(werr))
			reason = DrainYieldError
			break
		}
		run, err := worker.RunOnce(ctx, orgID, source)
		c.recordBackoff(key, err)
		outcome := Outcome{OrgID: orgID, Source: source, Run: run, Err: err, Duration: c.now().Sub(attemptStarted), At: c.now()}
		c.observer.ObserveProjectionOutcome(outcome)
		if err != nil {
			// Codex round-2 F2: classify on whether err ITSELF is a
			// cancellation, not on the ambient ctx.Err() -- checking ctx.Err()
			// independently could misclassify a genuine concrete backend
			// error as DrainYieldContextDone if ctx happened to also be
			// cancelled (e.g. process shutdown) at the moment of the check,
			// hiding the real failure from drain telemetry (retry/backoff
			// still sees the real error either way via recordBackoff above).
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				reason = DrainYieldContextDone
			} else {
				c.logger.WarnContext(ctx, "build tick pair failed", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(err), "duration_ms", outcome.Duration.Milliseconds())
				reason = DrainYieldError
			}
			break
		}
		if run.Applied {
			applied++
			total += int64(run.ItemsApplied)
		}
		mode, terminal := classifyBuildCompletion(run)
		if !terminal {
			mode = contextfabric.BuildCompletionPending
		} else {
			c.logger.InfoContext(ctx, "context_fabric: build source completed", "org_id", orgID, "source", source, "epoch", epoch, "completion_mode", string(mode), "rows_projected", total)
		}
		lastMode = mode
		if rerr := c.lifecycle.RecordSourceProgress(ctx, orgID, epoch, source, mode, total, c.now()); rerr != nil {
			// Codex round-2 F1: log and KEEP DRAINING (do not stop here).
			// The next batch's own RecordSourceProgress call writes the
			// FULL in-memory `total` accumulated so far this call -- since
			// the checkpoint has already advanced regardless of whether
			// THIS write succeeded, a later successful write within the
			// same runBuildPair call self-heals the gap completely. Only
			// stopping immediately (CHAOS-3826 round-1's own fix) removed
			// that self-healing chance: the checkpoint had already moved
			// past this batch, so priorRows on the NEXT tick would read
			// the stale pre-failure total forever, permanently
			// undercounting rows this batch genuinely applied -- worse
			// than the transient staleness this fix accepts. progressStale
			// tracks whether the finalizing write below still needs to run.
			c.logger.WarnContext(ctx, "record build source progress failed; will retry before the drain returns", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(rerr))
			progressStale = true
		} else {
			progressStale = false
		}
		if terminal {
			reason = DrainYieldExhausted
			break
		}
		if ctx.Err() != nil {
			reason = DrainYieldContextDone
			break
		}
		if budget == nil || *budget <= 0 {
			reason = DrainYieldBudgetExceeded
			break
		}
		*budget--
	}
	// Finalizing write (Codex round-2 F1): if the LAST RecordSourceProgress
	// attempt in the loop above failed and nothing afterward re-wrote it,
	// retry once more with the final accumulated total before this call
	// returns -- once runBuildPair returns, the next tick's priorRows can
	// only ever see what was durably recorded here, so this is the last
	// chance to avoid a permanent undercount from a transient write
	// failure. A failure here is a genuine, bounded residual gap (Postgres
	// unavailable for this whole drain) -- logged loudly, not silently
	// accepted, and self-heals on the NEXT tick's own successful write
	// (which reads a corrected total by re-applying the same batches, if
	// the source has not already advanced its checkpoint past them --
	// otherwise this is the SAME pre-existing checkpoint/progress
	// non-atomicity gap performRebuild's own sibling paths carry today,
	// not something this ticket introduces or is scoped to close).
	if progressStale {
		if rerr := c.lifecycle.RecordSourceProgress(ctx, orgID, epoch, source, lastMode, total, c.now()); rerr != nil {
			c.logger.WarnContext(ctx, "record build source progress failed after retry; rows_projected may undercount until a future successful write", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(rerr))
		}
	}
	if batches > 0 {
		c.observer.ObserveProjectionDrain(DrainOutcome{
			OrgID: orgID, Source: source, Batches: batches, Applied: applied, YieldReason: reason,
			Duration: c.now().Sub(started), At: c.now(),
		})
	}
}

// classifyBuildCompletion maps one build tick's ProjectionRun onto design
// brief §3.3/item 5's four completion shapes:
//
//   - Applied && CompleteEnumeration: the source itself claims this batch
//     enumerated everything through the current cursor -- paged_final.
//   - Applied && !CompleteEnumeration: still paging; NOT terminal. The next
//     tick's own outcome is what eventually decides -- a source is never
//     penalized for not claiming CompleteEnumeration on every intermediate
//     page (episodes.go's own doc comment: only a from-scratch, untruncated
//     page claims it truthfully today).
//   - !Applied && PreviousCursor == "": nothing was EVER available for this
//     organization/source, from the very first tick -- empty_first_tick.
//   - !Applied && PreviousCursor != "": paged through some rows across
//     earlier ticks, now genuinely caught up -- cursor_exhausted. This is
//     the general-purpose exhaustion signal a multi-page source that never
//     claims CompleteEnumeration on its last page still reaches correctly.
func classifyBuildCompletion(run contextfabric.ProjectionRun) (mode contextfabric.BuildCompletionMode, terminal bool) {
	if run.Applied {
		if run.CompleteEnumeration {
			return contextfabric.BuildCompletionPagedFinal, true
		}
		return "", false
	}
	if run.PreviousCursor == "" {
		return contextfabric.BuildCompletionEmptyFirstTick, true
	}
	return contextfabric.BuildCompletionCursorExhausted, true
}

// workerFor constructs a contextfabric.ProjectionWorker bound to a specific
// checkpoint view -- always freshly, never from a precomputed map:
// NewProjectionWorker does no I/O, so there is no meaningful cost to
// constructing one per tick, and doing so is what lets ordinary ticks,
// build ticks, and any future epoch all share one code path with no
// epoch-keyed cache to keep coherent. c.workers (built once at construction
// time) exists ONLY to validate every configured source's wiring fails
// loudly at startup, never to be read at tick time.
func (c *Coordinator) workerFor(source string, checkpoints contextfabric.ProjectionCheckpointStore) (*contextfabric.ProjectionWorker, error) {
	return contextfabric.NewProjectionWorker(c.sources[source], c.backend, checkpoints, contextfabric.ProjectionWorkerOptions{Now: c.now})
}

// divergenceBackoffKey is a synthetic "source" name for reusing the
// coordinator's existing due()/recordBackoff() pair-scheduling gate
// (designed for ordinary (org, source) ticks in runPair) to also throttle
// CHAOS-3882 divergence-recovery attempts, one per organization. Prefixed
// with a NUL byte so it can never collide with a real SourcePair.Name --
// devhealthsource callers choose those freely, but never with an embedded
// NUL (the same separator runPair's own key already relies on being unique).
const divergenceBackoffKey = "\x00chaos3882-projection-liveness-divergence"

// checkpointStoreDiverged is the CHAOS-3882 liveness check: it reports
// whether ANY of orgID's configured sources shows checkpoint-vs-store
// divergence -- the durable Postgres checkpoint recorded a successful
// ApplyProjectionBatch for that source (BackendWatermark != ""), but the
// graph backend's own durable sentinel for that (org, source) is now
// CONFIRMED absent. This is exactly the CHAOS-3882 incident: a FalkorDB
// container restart lost the projected graph -- and the projection-time
// watermark node with it -- while the Postgres checkpoint, a wholly
// separate durable store, stayed advanced. Nothing then replayed, and ACR
// silently served resolution against an empty graph: an empty result set
// reads as "no candidates found" (clean no-match/clarification behavior),
// not as an outage.
//
// Cheap by construction: one Postgres checkpoint read and one single-node
// graph read (ProjectionWatermark -- the exact same MATCH-by-key lookup
// emitProjectionFreshness already performs for the CHAOS-3887 signal) per
// configured source. Never a graph scan.
//
// Deliberately conservative about what counts as "confirmed absent": only
// errors.Is(err, contextfabric.ErrProjectionWatermarkNotFound) -- the
// backend's durable, deliberate "no such sentinel" answer, see
// ProjectionBackend's doc comment -- counts. Any OTHER error (a transient
// dependency outage, a timeout) reports "not diverged" for that source,
// exactly like emitProjectionFreshness's own "unknown must never masquerade
// as needs rebuild" discipline: a flaky network blip must never trigger
// performRebuild's PurgeOrganization against organization data that is
// actually still fine.
//
// A source whose checkpoint was never durably applied to the backend
// (BackendWatermark == "", e.g. a never-projected organization, or one
// already reset by a prior rebuild) is never divergent -- there is nothing
// for the graph to have lost. This is what keeps this check silent on
// TestCHAOS3887_NeverProjectedOrgReportsUnknownNotStale's exact fixture: a
// backend watermark read failure alone, with no corroborating durable
// checkpoint claim, is not evidence of anything.
func (c *Coordinator) checkpointStoreDiverged(ctx context.Context, orgID string, checkpoints contextfabric.ProjectionCheckpointStore) bool {
	for _, source := range c.sourceNames {
		checkpoint, err := checkpoints.LoadProjectionCheckpoint(ctx, orgID, source)
		if err != nil {
			continue // cannot read the durable baseline; not evidence either way
		}
		if strings.TrimSpace(checkpoint.BackendWatermark) == "" {
			continue // never durably applied here, or already reset -- nothing to lose
		}
		watermark, err := c.backend.ProjectionWatermark(ctx, orgID, source)
		if err == nil && strings.TrimSpace(watermark.SourceVersion) != "" {
			continue // the backend still has its sentinel; healthy
		}
		if err != nil && !errors.Is(err, contextfabric.ErrProjectionWatermarkNotFound) {
			continue // unknown (dependency error), not a confirmed divergence
		}
		return true
	}
	return false
}

// LivenessCheck is the CHAOS-3882 readiness-path leg of the same liveness
// signal Tick already checks at startup (Run's first loop iteration) and on
// every poll interval: it lets cmd/acr-projector's /readyz probe (see
// api.ReadinessCheck) surface checkpoint-vs-store divergence immediately,
// rather than an operator only finding out from the tick logs. Read-only --
// it never purges or resets anything itself; ordinary Tick scheduling
// (recoverFromDivergence, under the org lock) remains the only place
// recovery actually happens, so a readiness probe running concurrently with
// a tick can never race a rebuild.
//
// Cheap the same way checkpointStoreDiverged is: bounded by the configured
// organization allowlist, one Postgres read and one single-node graph read
// per (org, source), never a graph scan. A deployment with a very large
// allowlist should poll /readyz less often, not avoid this check -- the
// alternative is the CHAOS-3882 incident itself: a healthy-looking probe
// while resolution is silently served against an empty graph.
func (c *Coordinator) LivenessCheck(ctx context.Context) error {
	var diverged []string
	for _, orgID := range c.orgIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.checkpointStoreDiverged(ctx, orgID, c.resolveOrgCheckpoints(ctx, orgID)) {
			diverged = append(diverged, orgIDHash(orgID))
		}
	}
	if len(diverged) == 0 {
		return nil
	}
	// Content-safe: org_id_hash values only, joined into the error text --
	// never a raw organization identifier, matching every other CHAOS-3882
	// signal's discipline.
	return fmt.Errorf("projection checkpoint-store divergence pending recovery for %d organization(s): %s", len(diverged), strings.Join(diverged, ","))
}

// recoverFromDivergence drives the CHAOS-3882 recovery for an organization
// checkpointStoreDiverged flagged. It reuses performRebuild -- the SAME
// crash-resumable, idempotent purge-then-reset-checkpoints-then-invalidate-
// reuse sequence an explicit `acr-projector rebuild --org` already runs --
// rather than inventing a second, parallel recovery mechanism: a fresh
// PurgeOrganization against a graph the backend already confirms is empty
// is a documented no-op (PurgeOrganization's own doc comment), and resetting
// every source's checkpoint is precisely "invalidate the checkpoint so the
// normal replay machinery re-projects" -- the ticket's own framing.
//
// Never a rebuild loop: a successful performRebuild resets EVERY configured
// source's checkpoint (resetAllCheckpoints), which clears BackendWatermark
// back to "" for all of them -- so checkpointStoreDiverged reports false
// again on the very next tick purely as a side effect of the fix, with no
// separate "already recovered" bit to maintain. The divergenceBackoffKey gate
// below exists for the OTHER case: a performRebuild attempt that itself
// fails (e.g. the backend is genuinely down) must not be retried on every
// poll tick with no backoff -- it reuses the same due()/recordBackoff()
// pair-scheduling gate runPair already relies on for ordinary tick failures,
// rather than adding a second backoff table.
//
// Every log line here is content-safe: org_id_hash and a bounded failure
// class only, matching classifyOutcomeError's closed vocabulary -- never a
// raw organization identifier or dependency error text.
func (c *Coordinator) recoverFromDivergence(ctx context.Context, orgID string, stats *tickFreshnessStats) {
	key := orgID + "\x00" + divergenceBackoffKey
	if !c.due(key) {
		stats.recordBackoff()
		return
	}
	hash := orgIDHash(orgID)
	// LOUD and unconditional (Error, not Debug/Info): this is an active
	// incident signal -- the durable checkpoint says data was projected and
	// the graph backend says otherwise -- not routine scheduling chatter.
	c.logger.ErrorContext(ctx, "context_fabric: projection checkpoint-store divergence detected (CHAOS-3882); the durable checkpoint outran the graph backend's own state -- triggering automatic recovery instead of serving resolution against a silently empty or stale graph",
		"org_id_hash", hash)
	err := c.performRebuild(ctx, orgID)
	c.recordBackoff(key, err)
	stats.recordDivergenceRecovered()
	if err != nil {
		c.logger.ErrorContext(ctx, "context_fabric: automatic projection-liveness recovery failed; will retry with backoff",
			"org_id_hash", hash, "failure_class", classifyOutcomeError(err))
		return
	}
	c.logger.WarnContext(ctx, "context_fabric: automatic projection-liveness recovery completed; every configured source's checkpoint was reset and replay will resume on the next tick",
		"org_id_hash", hash)
}

// recoverFromDivergenceLifecycle is recoverFromDivergence's CHAOS-3898
// S2a-2 replacement (design brief item 8's MANDATORY conversion of the
// CHAOS-3882 recovery path): it opens a build-aside epoch (beginLifecycleBuild)
// instead of purging the organization's serving graph in place. The SAME
// divergenceBackoffKey gate throttles a failing attempt; a SUCCEEDING
// beginLifecycleBuild call needs no "already recovered" bit either -- once
// the build flips, runOrgLifecycle's steady branch resolves the NEW active
// epoch and checkpointStoreDiverged naturally reports false again, the same
// way resetAllCheckpoints made the legacy path self-clearing.
func (c *Coordinator) recoverFromDivergenceLifecycle(ctx context.Context, orgID string, stats *tickFreshnessStats) {
	key := orgID + "\x00" + divergenceBackoffKey
	if !c.due(key) {
		stats.recordBackoff()
		return
	}
	hash := orgIDHash(orgID)
	c.logger.ErrorContext(ctx, "context_fabric: projection checkpoint-store divergence detected (CHAOS-3882); the durable checkpoint outran the graph backend's own state -- triggering automatic build-aside recovery instead of serving resolution against a silently empty or stale graph",
		"org_id_hash", hash)
	opened, err := c.beginLifecycleBuild(ctx, orgID)
	c.recordBackoff(key, err)
	stats.recordDivergenceRecovered()
	if err != nil {
		c.logger.ErrorContext(ctx, "context_fabric: automatic projection-liveness recovery failed to open a build-aside epoch; will retry with backoff",
			"org_id_hash", hash, "failure_class", classifyOutcomeError(err))
		return
	}
	if !opened {
		// beginLifecycleBuild already logged WHY (a build already in
		// progress, or the organization isn't in a rebuildable state right
		// now, e.g. a post-flip grace window) -- nothing NEW happened this
		// tick, so don't claim otherwise (CHAOS-4208: this used to fire the
		// WARN below unconditionally, including when the CAS was refused
		// for an unrelated reason and no epoch was opened).
		return
	}
	c.logger.WarnContext(ctx, "context_fabric: automatic projection-liveness recovery opened a build-aside epoch; replay will proceed over subsequent ticks",
		"org_id_hash", hash)
}

// runPairOnce attempts exactly ONE RunOnce call for (orgID, source),
// gated by the due()/recordBackoff per-pair schedule -- the same
// single-attempt body this function always was before CHAOS-3826.
// evaluated is false only when due() refused the attempt outright;
// applied mirrors ProjectionRun.Applied (a batch existed and was
// projected -- the signal runPair's drain loop keeps going on); err
// reports a RunOnce/worker-construction failure (a distinct yield reason
// from "no more work", even though both currently leave applied false)
// -- returned as the concrete error, not a bool, so runPair (Codex round-2
// F2) can classify cancellation by inspecting THIS error's own identity
// rather than the ambient ctx.Err(), which could coincidentally be set by
// an unrelated cancellation and mislabel a genuine backend error.
func (c *Coordinator) runPairOnce(ctx context.Context, orgID, source string, checkpoints contextfabric.ProjectionCheckpointStore) (evaluated, applied bool, err error, stale bool) {
	key := orgID + "\x00" + source
	if !c.due(key) {
		return false, false, nil, false
	}
	started := c.now()
	worker, werr := c.workerFor(source, checkpoints)
	if werr != nil {
		c.recordBackoff(key, werr)
		c.logger.WarnContext(ctx, "projection worker construction failed", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(werr))
		return true, false, werr, false
	}
	run, runErr := worker.RunOnce(ctx, orgID, source)
	outcome := Outcome{OrgID: orgID, Source: source, Run: run, Err: runErr, Duration: c.now().Sub(started), At: c.now()}
	c.recordBackoff(key, runErr)
	c.observer.ObserveProjectionOutcome(outcome)
	if runErr != nil {
		c.logger.WarnContext(ctx, "projection pair failed", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(runErr), "duration_ms", outcome.Duration.Milliseconds())
		return true, false, runErr, false
	}
	if run.Applied {
		c.logger.InfoContext(ctx, "projection batch applied", "org_id", orgID, "source", source, "batch_id", run.BatchID, "backend_watermark", run.BackendWatermark, "duration_ms", outcome.Duration.Milliseconds())
	}
	// CHAOS-3887 (H1): computed and logged regardless of run.Applied/err --
	// the whole point is that the prior guard (projector.go RunOnce) only
	// ever compared checkpoint SourceVersion against a batch's SourceVersion
	// INSIDE the available==true branch, so a dormant organization (no new
	// rows, available=false, no error) got no freshness signal at all.
	return true, run.Applied, nil, c.emitProjectionFreshness(ctx, orgID, source)
}

// runPair drains (org, source)'s pending batches within THIS tick
// (CHAOS-3826): after a batch applies and NextProjectionBatch still has
// more available, the next batch is fetched immediately under the SAME
// org lock instead of waiting a full poll interval -- the drip-feed this
// ticket exists to close. The first attempt always runs (due()-gated
// only), preserving every configured source's pre-3826 per-tick freshness
// cadence regardless of budget; only ADDITIONAL attempts spend budget.
// Also reports the CHAOS-3887 freshness classification back to runOrg for
// the per-tick fleet aggregate: evaluated is true when this pair was
// actually checked this tick (it was due), and stale is true when the
// LAST attempt's check found a producer-identity drift pending rebuild.
// budget is a POINTER shared across every source this organization's
// Tick evaluates (see runOrgLegacy/runOrgLifecycle's call sites): a
// large-backlog source cannot starve its sibling sources' first attempt,
// and bounding the total across all of them keeps this ORGANIZATION's
// runOrg call bounded, so it cannot starve other organizations dispatched
// in the same Tick (Tick.wg.Wait blocks the next poll on every dispatched
// runOrg returning). The 200-row page cap (batch size) is unchanged --
// only the inter-batch idle inside one tick is removed.
func (c *Coordinator) runPair(ctx context.Context, orgID, source string, checkpoints contextfabric.ProjectionCheckpointStore, budget *int) (evaluated, stale bool) {
	started := c.now()
	batches, applied := 0, 0
	reason := DrainYieldExhausted
	for {
		pairEvaluated, pairApplied, pairErr, pairStale := c.runPairOnce(ctx, orgID, source, checkpoints)
		if !pairEvaluated {
			if batches == 0 {
				return false, false
			}
			break
		}
		evaluated = true
		// Codex round-3 F1: OR across every attempt this drain makes, never
		// overwrite. Before CHAOS-3826's in-tick draining, runPair made
		// exactly ONE attempt per tick, so assignment and OR were
		// equivalent; now a drain can make several attempts, and an
		// attempt that errors out reports pairStale=false unconditionally
		// (runPairOnce never reaches the freshness check on an error) --
		// plain assignment let that false overwrite a REAL staleness
		// signal an earlier attempt in the SAME tick already found,
		// silently clearing runOrg's per-tick freshness aggregate
		// (tickFreshnessStats) to "OK" despite the tick having genuinely
		// observed drift.
		stale = stale || pairStale
		batches++
		if pairApplied {
			applied++
		}
		switch {
		// Codex round-2 F2 (refining round-1 F3): classify on whether
		// pairErr ITSELF is a cancellation, not on the ambient ctx.Err().
		// Checking ctx.Err() independently could misclassify a genuine
		// concrete backend error as DrainYieldContextDone if ctx happened
		// to also be cancelled (e.g. process shutdown) at the moment of
		// the check, hiding the real failure from drain telemetry --
		// recordBackoff (inside runPairOnce) still sees the real error
		// either way, so retry/backoff behavior is unaffected.
		case pairErr != nil && (errors.Is(pairErr, context.Canceled) || errors.Is(pairErr, context.DeadlineExceeded)):
			reason = DrainYieldContextDone
		case pairErr != nil:
			reason = DrainYieldError
		case !pairApplied:
			reason = DrainYieldExhausted
		case ctx.Err() != nil:
			reason = DrainYieldContextDone
		case budget == nil || *budget <= 0:
			reason = DrainYieldBudgetExceeded
		default:
			*budget--
			continue
		}
		break
	}
	if batches > 0 {
		c.observer.ObserveProjectionDrain(DrainOutcome{
			OrgID: orgID, Source: source, Batches: batches, Applied: applied, YieldReason: reason,
			Duration: c.now().Sub(started), At: c.now(),
		})
	}
	return evaluated, stale
}

// emitProjectionFreshness is the CHAOS-3887 (H1) per-org, per-source
// freshness signal: it reads the durably-written falkorgraph
// ProjectionWatermark (SourceVersion + projected_at per org/source) --
// previously written on every successful apply and read only by the
// acr-projector /readyz probe, which discards the value -- and compares its
// SourceVersion against the source's CURRENT producer SourceVersion.
//
// Unlike the ProjectionWorker.RunOnce guard this signal is not gated on
// batch availability: it runs for every (org, source) pair this tick
// actually evaluates, including a dormant organization that has no new
// rows and so never builds a batch. That is precisely the gap this ticket
// closes -- a dormant organization's already-projected nodes staying
// computed under stale producer logic, with no signal distinguishing it
// from a freshly-projected one.
//
// stale reports true only when both a durable watermark and the source's
// current version are known AND they differ. No watermark yet (a
// never-projected organization/source) and no known current version (a
// source that does not implement ProjectionSourceVersion) both report
// stale=false -- "unknown" must never masquerade as "needs rebuild".
//
// Content-safe: every field is a count, version string, bool, or the
// one-way org_id_hash -- never a raw organization identifier or any
// projected row content.
func (c *Coordinator) emitProjectionFreshness(ctx context.Context, orgID, source string) bool {
	watermark, err := c.backend.ProjectionWatermark(ctx, orgID, source)
	hash := orgIDHash(orgID)
	if err != nil || strings.TrimSpace(watermark.SourceVersion) == "" {
		// Not found (never durably projected yet) and a real read failure
		// are deliberately not distinguished here (codex-round style
		// discipline: no raw error text, and Coordinator has no
		// backend-specific sentinel to classify by without coupling this
		// backend-agnostic scheduler to one ProjectionBackend
		// implementation's error types). Either way there is no durable
		// baseline to compare against this tick, so nothing further to
		// report but "unknown".
		c.logger.DebugContext(ctx, "context_fabric: projection freshness unknown; no durable watermark yet",
			"org_id_hash", hash, "source", source)
		return false
	}
	current := currentSourceVersion(c.sources[source])
	staleFields := []any{
		"org_id_hash", hash, "source", source,
		"checkpoint_source_version", watermark.SourceVersion, "current_source_version", current,
	}
	if strings.TrimSpace(current) == "" {
		// Source does not implement the optional ProjectionSourceVersion
		// capability -- no code-current baseline to compare the durable
		// watermark against, so staleness is unknown, not false-positive.
		c.logger.DebugContext(ctx, "context_fabric: projection freshness unknown; source does not report a current version", staleFields...)
		return false
	}
	stale := watermark.SourceVersion != current
	ageSeconds := c.now().UTC().Sub(watermark.ProjectedAt.UTC()).Seconds()
	if ageSeconds < 0 {
		ageSeconds = 0
	}
	fields := append(staleFields, "stale", stale, "projected_at_age_seconds", ageSeconds)
	if stale {
		c.logger.WarnContext(ctx, "context_fabric: projection freshness stale; rebuild required", fields...)
	} else {
		c.logger.DebugContext(ctx, "context_fabric: projection freshness", fields...)
	}
	return stale
}

// resolveOrgCheckpoints returns the checkpoint-store VIEW an ordinary
// (non-build) operation for orgID should read -- the organization's current
// ACTIVE epoch's own set (design brief §3.4), or c.checkpoints (epoch 0)
// when Lifecycle is unconfigured or the organization has never migrated
// (no lifecycle row, or ActiveEpoch == 0 -- the SAME legacy row set either
// way, since epoch 0 IS c.checkpoints' own rows). Used by LivenessCheck,
// which -- unlike runOrgLifecycle -- has no already-fetched lifecycle row
// to reuse.
func (c *Coordinator) resolveOrgCheckpoints(ctx context.Context, orgID string) contextfabric.ProjectionCheckpointStore {
	if c.lifecycle == nil {
		return c.checkpoints
	}
	row, found, err := c.lifecycle.Get(ctx, orgID)
	if err != nil || !found || row.ActiveEpoch == 0 {
		return c.checkpoints
	}
	return c.epochCheckpoints(row.ActiveEpoch)
}

// recordCheckpointEpochState emits cf_checkpoint_epoch_state (design brief
// §5b) for one (org, epoch): a no-op when LifecycleTelemetry is unconfigured.
// cursorAge is the MAX age across every configured source's checkpoint for
// this epoch -- "how stale is the least-fresh source", the more
// operationally meaningful aggregate for an "at a glance" signal than a
// per-source breakdown the interface's own (org, epoch) shape does not
// carry.
func (c *Coordinator) recordCheckpointEpochState(ctx context.Context, orgID string, epoch int64, state contextfabric.CheckpointEpochState, checkpoints contextfabric.ProjectionCheckpointStore) {
	if c.lifecycleTelem == nil {
		return
	}
	var maxAge time.Duration
	for _, source := range c.sourceNames {
		checkpoint, err := checkpoints.LoadProjectionCheckpoint(ctx, orgID, source)
		if err != nil || checkpoint.UpdatedAt.IsZero() {
			continue
		}
		age := c.now().Sub(checkpoint.UpdatedAt)
		if age < 0 {
			age = 0
		}
		if age > maxAge {
			maxAge = age
		}
	}
	c.lifecycleTelem.RecordCheckpointEpochState(ctx, orgID, epoch, state, maxAge)
}

// sweepRetirements drives every due epoch retirement to completion (design
// brief §3.5), once per Tick, after the ordinary per-org pass -- a no-op
// when RetireScheduler is unconfigured. Bounded by the SAME organization
// allowlist every other entry point enforces (allowsOrg's own doc comment):
// a replica group scoped to a subset of organizations must never retire an
// epoch belonging to one it was not configured to project.
func (c *Coordinator) sweepRetirements(ctx context.Context) {
	if c.retireScheduler == nil {
		return
	}
	due, err := c.retireScheduler.DueRetirements(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "list due epoch retirements failed", "failure_class", classifyOutcomeError(err))
		return
	}
	for _, retirement := range due {
		if ctx.Err() != nil {
			return
		}
		if !c.allowsOrg(retirement.OrgID) {
			continue
		}
		c.retireOne(ctx, retirement.OrgID, retirement.Epoch)
	}
}

// retireOne drives ONE (org, epoch) retirement under the SAME per-organization
// single-flight discipline (in-process mutex + OrgLocker) every other
// per-org operation in this file already has -- so a retirement can never
// run concurrently with a build/flip/rollback for the SAME organization,
// even though RetireScheduler's own CAS machinery independently prevents a
// double-delete.
func (c *Coordinator) retireOne(ctx context.Context, orgID string, epoch int64) {
	mutexAny, _ := c.orgMu.LoadOrStore(orgID, &sync.Mutex{})
	mutex := mutexAny.(*sync.Mutex)
	if !mutex.TryLock() {
		return // an ordinary tick or build for this organization is in flight; retry next sweep
	}
	defer mutex.Unlock()

	unlock, err := c.locker.Lock(ctx, orgID)
	if err != nil {
		if !errors.Is(err, ErrOrgLocked) {
			c.logger.WarnContext(ctx, "acquire organization lock for retirement failed", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		}
		return
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			c.logger.WarnContext(ctx, "organization unlock failed after retirement", "org_id", orgID, "failure_class", classifyOutcomeError(unlockErr))
		}
	}()
	if err := c.retireScheduler.RunOne(ctx, orgID, epoch); err != nil {
		c.logger.WarnContext(ctx, "retire epoch failed", "org_id", orgID, "epoch", epoch, "failure_class", classifyOutcomeError(err))
		return
	}
	c.logger.InfoContext(ctx, "context_fabric: epoch retired", "org_id", orgID, "epoch", epoch)
}

// sweepGraceExpirations transitions every configured organization whose
// lifecycle row is LifecycleStatusGrace with an elapsed GraceDeadline into
// begin_retire (design brief §3.1 step 5): the point of no return, creating
// the grace_expired EpochRetirement record RetireScheduler later drains.
// Bounded by the SAME organization allowlist LivenessCheck iterates, one
// Postgres read per organization -- a no-op when Lifecycle is unconfigured.
func (c *Coordinator) sweepGraceExpirations(ctx context.Context) {
	if c.lifecycle == nil {
		return
	}
	for _, orgID := range c.orgIDs {
		if ctx.Err() != nil {
			return
		}
		c.tryBeginRetire(ctx, orgID)
	}
}

// tryBeginRetire attempts begin_retire for orgID under the SAME per-organization
// single-flight discipline every other per-org operation in this file has.
// A non-grace status, a not-yet-elapsed deadline, or a lock held by an
// in-flight tick/build for this organization are all ordinary, expected
// outcomes -- silently skipped, retried on the next tick -- never logged as
// failures.
func (c *Coordinator) tryBeginRetire(ctx context.Context, orgID string) {
	mutexAny, _ := c.orgMu.LoadOrStore(orgID, &sync.Mutex{})
	mutex := mutexAny.(*sync.Mutex)
	if !mutex.TryLock() {
		return
	}
	defer mutex.Unlock()
	unlock, err := c.locker.Lock(ctx, orgID)
	if err != nil {
		return
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			c.logger.WarnContext(ctx, "organization unlock failed after grace-expiration sweep", "org_id", orgID, "failure_class", classifyOutcomeError(unlockErr))
		}
	}()

	row, found, err := c.lifecycle.Get(ctx, orgID)
	if err != nil {
		c.logger.WarnContext(ctx, "read graph lifecycle row failed during grace sweep", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		return
	}
	if !found || row.Status != contextfabric.LifecycleStatusGrace || row.GraceDeadline == nil {
		return
	}
	now := c.now()
	if now.Before(*row.GraceDeadline) {
		return
	}
	_, retirement, err := c.lifecycle.BeginRetire(ctx, orgID, row.ActiveEpoch, now, false)
	if err != nil {
		// ErrLifecycleTransitionRefused/ErrLifecycleConflict are ordinary
		// races (a concurrent rollback won, or the deadline moved) -- only
		// anything else is worth a warning.
		if !errors.Is(err, contextfabric.ErrLifecycleTransitionRefused) && !errors.Is(err, contextfabric.ErrLifecycleConflict) {
			c.logger.WarnContext(ctx, "begin retire failed", "org_id", orgID, "failure_class", classifyOutcomeError(err))
		}
		return
	}
	c.logger.InfoContext(ctx, "context_fabric: grace window elapsed; epoch queued for retirement", "org_id", orgID, "epoch", retirement.Epoch)
}

// currentSourceVersion reads the CHAOS-3887 optional
// contextfabric.ProjectionSourceVersion capability off source, returning ""
// when source is nil or does not implement it -- the caller treats that as
// "unknown", never as a mismatch.
func currentSourceVersion(source contextfabric.ProjectionSource) string {
	versioned, ok := source.(contextfabric.ProjectionSourceVersion)
	if !ok {
		return ""
	}
	return versioned.CurrentProjectionSourceVersion()
}

// orgIDHash one-way hashes orgID for telemetry (CHAOS-3887 corpus safety):
// counts/enums/versions/hashes/bools only, never a raw organization
// identifier. Same construction (SHA-256, first 6 bytes, hex) as
// devhealthsource's redactOrg -- this package cannot import devhealthsource
// (which itself imports contextfabric) without a cycle, so the scheme is
// duplicated rather than shared, not reinvented.
func orgIDHash(orgID string) string {
	sum := sha256.Sum256([]byte(orgID))
	return hex.EncodeToString(sum[:6])
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

// Outcome failure classes. A closed, bounded vocabulary: an unrecognized error
// reports failureClassUnclassified rather than leaking anything about itself.
const (
	failureClassCanceled      = "canceled"
	failureClassConflict      = "checkpoint_conflict"
	failureClassLocked        = "organization_locked"
	failureClassRebuildNeeded = "rebuild_required"
	failureClassUnavailable   = "dependency_unavailable"
	failureClassRateLimited   = "dependency_rate_limited"
	// failureClassBudgetExceeded (CHAOS-3848) is a permanent per-query
	// condition (e.g. ClickHouse TOO_MANY_BYTES/TOO_MANY_ROWS), distinct
	// from failureClassUnavailable's transient-dependency-outage meaning:
	// identical retry against unchanged data fails the identical way, so an
	// operator needs to see this is not "wait for the dependency to come
	// back".
	failureClassBudgetExceeded = "query_budget_exceeded"
	failureClassInvalidResult  = "invalid_result"
	failureClassUnclassified   = "unclassified"
)

// ClassifyFailure is classifyOutcomeError exported for the projector binary,
// which logs its own lifecycle failures and must use the SAME bounded
// vocabulary. Codex round-5: a second logging site with its own error
// formatting is how the first sanitized one gets bypassed.
//
// ADD SENTINELS TO failureClasses, NOT HERE AND NOT AS AN INLINE CHECK. The
// probe test enumerates that table, so a sentinel added to it is automatically
// covered; a check written inline -- above the loop, or in this function -- is
// invisible to the probes and reintroduces exactly the gap the table exists to
// close.
func ClassifyFailure(err error) string { return classifyOutcomeError(err) }

// failureClasses is THE single place a sentinel-to-class pairing lives.
//
// ORDERED: the first errors.Is match wins, so entries that must share a class
// (cancellation and deadline) sit adjacent and both resolve to it. Reordering
// changes classification, so treat the order as part of the contract.
//
// classifyOutcomeError loops over this table, and the probe test in
// observer_test.go RANGES OVER THE SAME TABLE. That is the point of it being a
// table at all (codex round 7): the previous form kept the classifier's
// sentinels as inline switch arms and the probes as a separate hand-written
// list, so adding a branch without adding a probe left the new branch
// untested while every existing probe stayed green. Deriving both from one
// declaration makes that mistake structurally impossible rather than merely
// less likely.
var failureClasses = []struct {
	sentinel error
	class    string
}{
	// Cancellation and deadline share a class; adjacency plus first-match
	// ordering is what expresses that.
	{context.Canceled, failureClassCanceled},
	{context.DeadlineExceeded, failureClassCanceled},
	{contextfabric.ErrProjectionConflict, failureClassConflict},
	{ErrOrgLocked, failureClassLocked},
	{contextfabric.ErrProjectionSourceVersionChanged, failureClassRebuildNeeded},
	{contextfabric.ErrRateLimited, failureClassRateLimited},
	// Must precede ErrUnavailable: a query-budget error is a permanent
	// per-query condition, and letting the more generic sentinel match
	// first would silently reclassify it as a transient dependency outage.
	{contextfabric.ErrQueryBudgetExceeded, failureClassBudgetExceeded},
	{contextfabric.ErrUnavailable, failureClassUnavailable},
	{contextfabric.ErrInvalidResult, failureClassInvalidResult},
}

// classifyOutcomeError maps a tick failure onto the closed vocabulary above.
//
// It deliberately offers NO escape hatch to the raw text -- not even at debug
// level. A guarantee that holds only at some log levels is not a guarantee: it
// makes leak-or-not depend on deployment configuration, which is precisely how
// this class of defect recurs. Operators who need dependency-specific detail
// have it at the dependency, which logs its own errors with its own
// sanitization; what this signal is FOR is answering "is this organization
// stalled, and roughly why", which the class answers.
//
// failureClassUnclassified is a real answer, not a shrug: it means a failure
// arrived that this vocabulary does not yet name, which is itself the signal
// that the vocabulary needs extending.
func classifyOutcomeError(err error) string {
	if err == nil {
		return ""
	}
	for _, entry := range failureClasses {
		if errors.Is(err, entry.sentinel) {
			return entry.class
		}
	}
	return failureClassUnclassified
}
