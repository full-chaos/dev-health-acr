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
}

type noopObserver struct{}

func (noopObserver) ObserveProjectionOutcome(Outcome) {}

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
	PollInterval     time.Duration
	Concurrency      int
	MaxBackoff       time.Duration
	Now              func() time.Time
	Logger           *slog.Logger
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
	poll             time.Duration
	concurrency      int
	maxBackoff       time.Duration
	now              func() time.Time
	logger           *slog.Logger

	orgMu sync.Map // orgID -> *sync.Mutex, in-process first line of defense

	backoffMu sync.Mutex
	backoff   map[string]*pairBackoff // "orgID\x00source" -> state
}

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
		poll: cfg.PollInterval, concurrency: cfg.Concurrency,
		maxBackoff: cfg.MaxBackoff, now: cfg.Now, logger: cfg.Logger, backoff: make(map[string]*pairBackoff),
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
	return c.performRebuild(ctx, orgID)
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
}

// runOrg enforces single-flight per organization across a whole multi-source
// pass: the in-process mutex is a fast, always-on first line of defense; the
// OrgLocker (PostgresOrgLocker in production) makes the guarantee hold
// across acr-projector replicas too. Both are non-blocking (TryLock /
// pg_try_advisory_lock): a busy organization is skipped this tick, not
// queued, so one slow organization can never starve the others.
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
	if c.checkpointStoreDiverged(ctx, orgID) {
		c.recoverFromDivergence(ctx, orgID, stats)
		return
	}

	evaluated, stale := false, false
	for _, source := range c.sourceNames {
		if ctx.Err() != nil {
			return
		}
		pairEvaluated, pairStale := c.runPair(ctx, orgID, source)
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
func (c *Coordinator) checkpointStoreDiverged(ctx context.Context, orgID string) bool {
	for _, source := range c.sourceNames {
		checkpoint, err := c.checkpoints.LoadProjectionCheckpoint(ctx, orgID, source)
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
		if c.checkpointStoreDiverged(ctx, orgID) {
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

// runPair runs one (org, source) tick and reports the CHAOS-3887 freshness
// classification back to runOrg for the per-tick fleet aggregate: evaluated
// is true when this pair's freshness was actually checked this tick (it was
// due), and stale is true when that check found a producer-identity drift
// pending rebuild.
func (c *Coordinator) runPair(ctx context.Context, orgID, source string) (evaluated, stale bool) {
	key := orgID + "\x00" + source
	if !c.due(key) {
		return false, false
	}
	started := c.now()
	run, err := c.workers[source].RunOnce(ctx, orgID, source)
	outcome := Outcome{OrgID: orgID, Source: source, Run: run, Err: err, Duration: c.now().Sub(started), At: c.now()}
	c.recordBackoff(key, err)
	c.observer.ObserveProjectionOutcome(outcome)
	switch {
	case err != nil:
		c.logger.WarnContext(ctx, "projection pair failed", "org_id", orgID, "source", source, "failure_class", classifyOutcomeError(err), "duration_ms", outcome.Duration.Milliseconds())
	case run.Applied:
		c.logger.InfoContext(ctx, "projection batch applied", "org_id", orgID, "source", source, "batch_id", run.BatchID, "backend_watermark", run.BackendWatermark, "duration_ms", outcome.Duration.Milliseconds())
	}
	// CHAOS-3887 (H1): computed and logged regardless of run.Applied/err --
	// the whole point is that the prior guard (projector.go RunOnce) only
	// ever compared checkpoint SourceVersion against a batch's SourceVersion
	// INSIDE the available==true branch, so a dormant organization (no new
	// rows, available=false, no error) got no freshness signal at all.
	return true, c.emitProjectionFreshness(ctx, orgID, source)
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
