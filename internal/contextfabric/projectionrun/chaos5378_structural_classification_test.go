package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// The four pins in this file are ONE design change, not four patches.
//
// Eleven review rounds fixed the reported instance of one class and shipped
// the next instance of it. Each pin below asserts a STRUCTURAL property --
// that a call site cannot express the defect at all -- rather than that one
// named site no longer does. They are kept together because the fixes
// interact: (a) is only fully observable on the summary once (d) has stopped
// discarding named failures, and (c) is only expressible once the three org
// paths share one classifier.

// --- (a) A wrapped propagated cancellation is truncation at EVERY stage ---

// structuralCancelStore fails ONE numbered call of ONE method with a BARE
// context.Canceled, having first cancelled the tick.
//
// Bare is the whole point. markPair decides PropagatedCancellation by
// IDENTITY (err == context.Canceled), and every caller that wraps before
// calling it hands over an error that can never satisfy that test -- so the
// bare-vs-wrapped policy never runs at that stage. The double therefore
// produces the exact input the policy is written for; if the policy is
// unreachable, that is the code's defect, not the fixture's.
type structuralCancelStore struct {
	*fakeCheckpointStore
	cancel context.CancelFunc
	// failLoadAt / failCASAt are 1-based call ordinals; 0 means never.
	// The CAS ordinal is what separates two stages that share one method:
	// against a fresh store the FIRST CAS is RunOnce's source-version claim
	// (claim_cas) and the SECOND is its final checkpoint advance
	// (final_cas).
	failLoadAt int32
	failCASAt  int32
	loads      atomic.Int32
	casCalls   atomic.Int32
}

func (s *structuralCancelStore) LoadProjectionCheckpoint(ctx context.Context, org, source string) (contextfabric.ProjectionCheckpoint, error) {
	if s.loads.Add(1) == s.failLoadAt {
		s.cancel()
		return contextfabric.ProjectionCheckpoint{}, context.Canceled
	}
	return s.fakeCheckpointStore.LoadProjectionCheckpoint(ctx, org, source)
}

func (s *structuralCancelStore) CompareAndSwapProjectionCheckpoint(ctx context.Context, expected, updated contextfabric.ProjectionCheckpoint) error {
	if s.casCalls.Add(1) == s.failCASAt {
		s.cancel()
		return context.Canceled
	}
	return s.fakeCheckpointStore.CompareAndSwapProjectionCheckpoint(ctx, expected, updated)
}

// cancellingBackend is the apply-stage half of the same shape.
type cancellingBackend struct {
	*fakeBackend
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (b *cancellingBackend) ApplyProjectionBatch(context.Context, contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.calls.Add(1)
	b.cancel()
	return contextfabric.ProjectionReceipt{}, context.Canceled
}

// TestStructural_AWrappedPropagatedCancellationIsTruncationAtEveryStage is
// confirm11's P1-4, and it is the one that explains the other three.
//
// markPair compares err == context.Canceled, but four of its five call sites
// hand it fmt.Errorf("...: %w", err) -- the wrapper exists BEFORE the identity
// check runs. So PropagatedCancellation is false at checkpoint_load, claim_cas,
// apply and final_cas, and only the direct source read can ever set it. The
// bare-vs-wrapped policy the invariant of record rests on has never actually
// executed at any of the stages that can INHERIT the tick's cancellation --
// and it passed 58 of 58 mutants while doing nothing, which is why a pin at
// one site would not have caught it either.
//
// The observable is our own drain telemetry: shutting the process down mid-tick
// reports YieldReason "error" (a real failure an operator should investigate)
// where "context_done" (we cancelled it ourselves) is the truth.
//
// All four stages are arms of ONE test on purpose. Three earlier rounds fixed
// the reported site; the property is that NO site can express it.
func TestStructural_AWrappedPropagatedCancellationIsTruncationAtEveryStage(t *testing.T) {
	t.Parallel()
	for _, arm := range []struct {
		stage string
		// reached asserts the fixture actually drove the stage under test.
		// A pin whose double was never called measures nothing.
		build func(cancel context.CancelFunc) (cfg projectionrun.Config, reached func() bool)
	}{
		{
			stage: "checkpoint_load",
			build: func(cancel context.CancelFunc) (projectionrun.Config, func() bool) {
				// failLoadAt is 2, not 1. runOrgLegacy's divergence probe
				// (checkpointStoreDiverged) loads the checkpoint of every
				// configured source BEFORE any pair runs, so failing the
				// first load cancels the tick before RunOnce is ever
				// reached and no pair outcome exists to classify -- the
				// arm would then fail for the wrong reason and prove
				// nothing about the stage. The `reached` guard below is
				// what makes that visible instead of silent.
				store := &structuralCancelStore{fakeCheckpointStore: newFakeCheckpointStore(), cancel: cancel, failLoadAt: 2}
				return projectionrun.Config{
					Sources:     []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
					Backend:     newFakeBackend(),
					Checkpoints: store,
				}, func() bool { return store.loads.Load() >= 2 }
			},
		},
		{
			stage: "claim_cas",
			build: func(cancel context.CancelFunc) (projectionrun.Config, func() bool) {
				store := &structuralCancelStore{fakeCheckpointStore: newFakeCheckpointStore(), cancel: cancel, failCASAt: 1}
				return projectionrun.Config{
					Sources:     []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
					Backend:     newFakeBackend(),
					Checkpoints: store,
				}, func() bool { return store.casCalls.Load() >= 1 }
			},
		},
		{
			stage: "apply",
			build: func(cancel context.CancelFunc) (projectionrun.Config, func() bool) {
				backend := &cancellingBackend{fakeBackend: newFakeBackend(), cancel: cancel}
				return projectionrun.Config{
					Sources:     []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
					Backend:     backend,
					Checkpoints: newFakeCheckpointStore(),
				}, func() bool { return backend.calls.Load() >= 1 }
			},
		},
		{
			stage: "final_cas",
			build: func(cancel context.CancelFunc) (projectionrun.Config, func() bool) {
				store := &structuralCancelStore{fakeCheckpointStore: newFakeCheckpointStore(), cancel: cancel, failCASAt: 2}
				return projectionrun.Config{
					Sources:     []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
					Backend:     newFakeBackend(),
					Checkpoints: store,
				}, func() bool { return store.casCalls.Load() >= 2 }
			},
		},
	} {
		t.Run(arm.stage, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			observer := &recordingObserver{}
			cfg, reached := arm.build(cancel)
			cfg.OrgIDs = []string{"org-a"}
			cfg.RebuildMarkers = newFakeRebuildMarker()
			cfg.Observer = observer
			cfg.Logger = logger

			coordinator, err := projectionrun.NewCoordinator(cfg)
			if err != nil {
				t.Fatalf("new coordinator: %v", err)
			}
			coordinator.Tick(ctx)

			if !reached() {
				t.Fatalf("the %s stage was never driven -- every assertion below would be vacuous", arm.stage)
			}
			drains := observer.snapshot()
			if len(drains) != 1 {
				t.Fatalf("DrainOutcomes = %d, want exactly 1: %+v", len(drains), drains)
			}
			if drains[0].YieldReason != projectionrun.DrainYieldContextDone {
				t.Errorf("YieldReason = %q at stage %s, want %q -- the tick's OWN cancellation reached this stage bare, and reporting it as an error tells an operator to investigate a failure that is our own shutdown",
					drains[0].YieldReason, arm.stage, projectionrun.DrainYieldContextDone)
			}

			summary := freshnessSummary(t, &buffer)
			requireSummaryScope(t, summary)
			requireBucketIdentity(t, summary)

			// The source must not be named: it was never the thing that
			// failed. Green at the parent for these stages, and kept here
			// because the fix moves the classification -- a fix that
			// over-corrected into "name the source on any cancellation"
			// would satisfy the YieldReason assertion above and break this.
			if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
				t.Errorf("sources_failed = %v at stage %s, want 0 -- our own %s was cancelled, not the source", got, arm.stage, arm.stage)
			}
			if names, _ := summary["failed_sources"].([]any); len(names) != 0 {
				t.Errorf("failed_sources = %v at stage %s, want empty", names, arm.stage)
			}

			// And it must not become a NAMED pair failure either. This is
			// the assertion that ties this pin to the truncation pin below:
			// once recordPairFailure stops discarding named failures on a
			// truncated tick, a stage whose identity check never ran would
			// start publishing pair_failed:[source:<stage>] for a tick we
			// cancelled ourselves. Truncation is not a failure of anything.
			if got := summaryNumber(t, summary, "pair_failures"); got != 0 {
				t.Errorf("pair_failures = %v at stage %s, want 0 -- a cancellation passing through a stage that added nothing of its own is truncation, not a failure of the pair", got, arm.stage)
			}
			if names, _ := summary["pair_failed"].([]any); len(names) != 0 {
				t.Errorf("pair_failed = %v at stage %s, want empty", names, arm.stage)
			}
			if summaryBool(t, summary, "tick_complete") {
				t.Errorf("tick_complete = true at stage %s, want false -- the tick was cancelled mid-drain and established no verdict for this organization", arm.stage)
			}
		})
	}
}

// --- (b) The stage is stamped by the error's ORIGIN, not by the helper ---

// progressReportingSource is dormant (available=false, no error) so RunOnce
// takes the persistConsumedProgress path, and reports whatever the arm needs
// from its ProjectionProgress capability.
type progressReportingSource struct {
	name          string
	progress      contextfabric.ConsumedProgress
	progressOK    bool
	progressErr   error
	calls         atomic.Int32
	progressCalls atomic.Int32
}

func (s *progressReportingSource) NextProjectionBatch(context.Context, contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	s.calls.Add(1)
	return contextfabric.ProjectionBatch{}, false, nil
}

func (s *progressReportingSource) CurrentProjectionSourceVersion() string { return "test.v1" }

func (s *progressReportingSource) ConsumedWithoutPublishing(context.Context, contextfabric.ProjectionCheckpoint) (contextfabric.ConsumedProgress, bool, error) {
	s.progressCalls.Add(1)
	return s.progress, s.progressOK, s.progressErr
}

// casFailingCheckpointStore fails every CAS with a concrete, non-cancellation
// error of OUR OWN io -- the control direction for the pin below.
type casFailingCheckpointStore struct {
	*fakeCheckpointStore
	err      error
	casCalls atomic.Int32
}

func (s *casFailingCheckpointStore) CompareAndSwapProjectionCheckpoint(context.Context, contextfabric.ProjectionCheckpoint, contextfabric.ProjectionCheckpoint) error {
	s.casCalls.Add(1)
	return s.err
}

// TestStructural_TheStageIsStampedByTheErrorsOrigin is confirm11's P1-2, and
// it is CHAOS-4789's own defect wearing the new fields.
//
// persistConsumedProgress can return a SOURCE-owned error -- the source's own
// ConsumedWithoutPublishing read failing -- but its caller stamps EVERY error
// out of that helper progress_cas. So a real source outage is published as
// pair_failed:[source:progress_cas] sources_failed:0: the split that exists to
// stop us blaming a source for our own io now hides a source that is down,
// which is strictly worse than the silence it replaced.
//
// The two arms are the two directions, and both matter. Stamping by the
// helper's NAME is wrong; stamping everything the helper returns as the
// SOURCE's would be wrong the other way, and would page the source's owner for
// our own checkpoint store. The stage has to come from where the error was
// PRODUCED.
func TestStructural_TheStageIsStampedByTheErrorsOrigin(t *testing.T) {
	t.Parallel()

	t.Run("a source-owned progress error names the source", func(t *testing.T) {
		t.Parallel()
		var buffer bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

		source := &progressReportingSource{
			name:        "dev_health_teams_projects",
			progressErr: fmt.Errorf("teams projects consumed-progress read: %w", contextfabric.ErrUnavailable),
		}
		coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
			OrgIDs:         []string{"org-a"},
			Sources:        []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: source}},
			Backend:        newFakeBackend(),
			Checkpoints:    newFakeCheckpointStore(),
			RebuildMarkers: newFakeRebuildMarker(),
			Logger:         logger,
		})
		if err != nil {
			t.Fatalf("new coordinator: %v", err)
		}
		coordinator.Tick(context.Background())

		if source.progressCalls.Load() == 0 {
			t.Fatal("ConsumedWithoutPublishing was never called -- the tick did not reach the path under test")
		}
		summary := freshnessSummary(t, &buffer)
		requireBucketIdentity(t, summary)

		if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
			t.Errorf("sources_failed = %v, want 1 -- the SOURCE's own consumed-progress read failed, and a source outage disclosed as a pair failure is exactly the 4789 defect this line exists to end", got)
		}
		names, _ := summary["failed_sources"].([]any)
		if len(names) != 1 || names[0] != "dev_health_teams_projects" {
			t.Errorf("failed_sources = %v, want [dev_health_teams_projects]", summary["failed_sources"])
		}
		if got := summaryNumber(t, summary, "pair_failures"); got != 0 {
			t.Errorf("pair_failures = %v, want 0 -- nothing of ours failed here; counting it in both places makes the two counters describe one event twice", got)
		}
		if pairNames, _ := summary["pair_failed"].([]any); len(pairNames) != 0 {
			t.Errorf("pair_failed = %v, want empty -- progress_cas names OUR checkpoint advance, and it never ran", pairNames)
		}
		if got := summaryNumber(t, summary, "orgs_source_failed"); got != 1 {
			t.Errorf("orgs_source_failed = %v, want 1", got)
		}
	})

	// The control direction, and the reason this is a stage question rather
	// than a "blame the source" question. Same helper, same call site: the
	// source reported progress perfectly well and OUR checkpoint advance
	// failed. That is a pair failure, and the source must stay unnamed.
	t.Run("our own progress CAS is still a pair failure", func(t *testing.T) {
		t.Parallel()
		var buffer bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

		source := &progressReportingSource{
			name:       "dev_health_teams_projects",
			progress:   contextfabric.ConsumedProgress{NextCursor: "cursor-advanced", SourceVersion: "test.v1"},
			progressOK: true,
		}
		store := &casFailingCheckpointStore{
			fakeCheckpointStore: newFakeCheckpointStore(),
			err:                 fmt.Errorf("advance checkpoint in postgres: %w", contextfabric.ErrUnavailable),
		}
		coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
			OrgIDs:         []string{"org-a"},
			Sources:        []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: source}},
			Backend:        newFakeBackend(),
			Checkpoints:    store,
			RebuildMarkers: newFakeRebuildMarker(),
			Logger:         logger,
		})
		if err != nil {
			t.Fatalf("new coordinator: %v", err)
		}
		coordinator.Tick(context.Background())

		if store.casCalls.Load() == 0 {
			t.Fatal("the progress CAS never ran -- this control proves nothing")
		}
		summary := freshnessSummary(t, &buffer)
		requireBucketIdentity(t, summary)

		if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
			t.Errorf("sources_failed = %v, want 0 -- the source reported its progress correctly and OUR checkpoint store refused the write; naming the source pages the wrong team", got)
		}
		if names, _ := summary["failed_sources"].([]any); len(names) != 0 {
			t.Errorf("failed_sources = %v, want empty", names)
		}
		if got := summaryNumber(t, summary, "pair_failures"); got != 1 {
			t.Errorf("pair_failures = %v, want 1", got)
		}
		pairNames, _ := summary["pair_failed"].([]any)
		if len(pairNames) != 1 || pairNames[0] != "dev_health_teams_projects:progress_cas" {
			t.Errorf("pair_failed = %v, want [dev_health_teams_projects:progress_cas] -- the stage still has to name OUR step when ours is the step that failed", summary["pair_failed"])
		}
	})
}

// --- (c) One org classifier: the build path reaches the source bucket ---

// TestStructural_ASourceFailingThroughABuildReachesTheSourceFailedBucket is
// confirm11's P1-1, and the reason the fix is one shared classifier rather
// than a fifth hand-maintained switch.
//
// runOrgLegacy, runOrgLifecycle and runBuildTick each carry the bucket
// decision by hand. The build copy feeds its failures into the per-source
// counters but overrides the caller's pre-set backoff bucket only for
// buildPairBroke, so a required source failing through a live build reports
// orgs_backoff:1 orgs_source_failed:0 on every tick -- the bucket and the
// counter printed beside it disagreeing about the same organization.
//
// Three ticks, not one. A pair that errors enters its own failure backoff and
// is not due on the next tick, so a fix that fires only while the source is
// actually running goes quiet after tick one with the source still broken --
// which is close to no fix at all, and is the defect the first 5378 commit
// shipped.
func TestStructural_ASourceFailingThroughABuildReachesTheSourceFailedBucket(t *testing.T) {
	t.Parallel()

	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}
	checkpoints := newFakeCheckpointStore()

	// ONE coordinator across all three ticks. The failure backoff lives in
	// the coordinator, so a fresh one per tick re-runs the pair every time
	// and never reaches the withheld state -- which is exactly the state
	// this arm exists to cover, and my first version of it silently did not.
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-a"},
		Sources:          []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: failing}},
		Backend:          newFakeBackend(),
		Checkpoints:      checkpoints,
		RebuildMarkers:   newFakeRebuildMarker(),
		Lifecycle:        &buildFailingLifecycleStore{epoch: 1},
		EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow:      time.Hour,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	for i := 0; i < 3; i++ {
		coordinator.Tick(context.Background())
	}
	summaries := allFreshnessSummaries(t, &buffer)
	if len(summaries) != 3 {
		t.Fatalf("freshness summaries = %d, want 3 (one per tick)", len(summaries))
	}
	for i, summary := range summaries {
		tick := i + 1
		requireBucketIdentity(t, summary)

		if got := summaryNumber(t, summary, "orgs_source_failed"); got != 1 {
			t.Errorf("tick %d: orgs_source_failed = %v, want 1 -- a required source is down and this organization's BUCKET has to say so, not only the counter beside it", tick, got)
		}
		if got := summaryNumber(t, summary, "orgs_backoff"); got != 0 {
			t.Errorf("tick %d: orgs_backoff = %v, want 0 -- \"this organization is building\" is not the same state as \"this organization is building and a required source is down\", and backoff is the bucket an operator reads as ordinary", tick, got)
		}
		if got := summaryNumber(t, summary, "orgs_ok"); got != 0 {
			t.Errorf("tick %d: orgs_ok = %v, want 0", tick, got)
		}

		// The disclosure has to keep naming it on every tick, whether the
		// source failed an attempt this tick or is sitting in the failure
		// backoff that its previous failure set.
		failed, _ := summary["failed_sources"].([]any)
		withheld, _ := summary["failure_backoff_sources"].([]any)
		named := false
		for _, list := range [][]any{failed, withheld} {
			for _, name := range list {
				if name == "dev_health_teams_projects" {
					named = true
				}
			}
		}
		if !named {
			t.Errorf("tick %d: dev_health_teams_projects appears in neither failed_sources=%v nor failure_backoff_sources=%v -- the source is still broken and this tick went quiet about it",
				tick, summary["failed_sources"], summary["failure_backoff_sources"])
		}
	}

	if failing.calls.Load() == 0 {
		t.Fatal("the failing source was never called across three ticks -- the fixture never drove the build path")
	}
}

// --- (d) Truncation suppresses claims of health, never established facts ---

// namedFailureThenCancelStore fails the pair with a concrete error of OUR
// OWN -- not a cancellation -- and only THEN cancels the tick.
//
// The order is the pin. The failure is a fact the tick established; the
// cancellation arrives afterwards and cannot un-establish it.
type namedFailureThenCancelStore struct {
	*fakeCheckpointStore
	cancel context.CancelFunc
	loads  atomic.Int32
}

// The SECOND load, for the same reason the checkpoint_load arm above uses
// the second: runOrgLegacy's divergence probe loads every configured
// source's checkpoint before any pair runs, so failing the first cancels
// the tick before RunOnce exists to fail at all.
func (s *namedFailureThenCancelStore) LoadProjectionCheckpoint(ctx context.Context, org, source string) (contextfabric.ProjectionCheckpoint, error) {
	if s.loads.Add(1) < 2 {
		return s.fakeCheckpointStore.LoadProjectionCheckpoint(ctx, org, source)
	}
	err := fmt.Errorf("load projection checkpoint from postgres: %w", contextfabric.ErrUnavailable)
	s.cancel()
	return contextfabric.ProjectionCheckpoint{}, err
}

// TestStructural_ANamedPairFailureSurvivesATruncatedTick is confirm11's P1-3,
// and it is the same error confirm6 caught, reintroduced in the new recorder.
//
// recordPairFailure returns early whenever the organization's evaluation was
// truncated, although pairOutcomeOf has ALREADY classified the error as a
// named pair failure. recordPair's own doc comment states the opposite rule:
// a pair that failed established a fact the tick cannot un-observe by dying
// afterwards. Truncation suppresses claims of HEALTH -- readings the tick
// never finished taking -- never facts already in hand.
//
// The distinction matters in production precisely during a shutdown, which is
// when a dependency going down and a process being drained happen together
// and an operator most needs to tell them apart.
func TestStructural_ANamedPairFailureSurvivesATruncatedTick(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &namedFailureThenCancelStore{fakeCheckpointStore: newFakeCheckpointStore(), cancel: cancel}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
		Backend:        newFakeBackend(),
		Checkpoints:    store,
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if store.loads.Load() < 2 {
		t.Fatalf("checkpoint loads = %d, want at least 2 -- the failing load never happened, so every assertion below would be vacuous", store.loads.Load())
	}
	summary := freshnessSummary(t, &buffer)
	requireSummaryScope(t, summary)
	requireBucketIdentity(t, summary)

	if got := summaryNumber(t, summary, "pair_failures"); got != 1 {
		t.Errorf("pair_failures = %v, want 1 -- the checkpoint store failed with a concrete error BEFORE the tick was cancelled, and a cancellation arriving afterwards cannot un-observe it", got)
	}
	names, _ := summary["pair_failed"].([]any)
	if len(names) != 1 || names[0] != "dev_health_teams_projects:checkpoint_load" {
		t.Errorf("pair_failed = %v, want [dev_health_teams_projects:checkpoint_load] -- a count with no name cannot tell an operator which pair and which step broke", summary["pair_failed"])
	}

	// The organization-bucket half, under the amended ladder: an ESTABLISHED
	// failure outranks truncation, so this organization is bucketed for the
	// broken pair rather than swallowed by unevaluated. That is the same
	// rule as the pair-level one above, one level up.
	if got := summaryNumber(t, summary, "orgs_pair_failed"); got != 1 {
		t.Errorf("orgs_pair_failed = %v, want 1 -- the pair broke before the tick was cancelled, and the bucket must carry the fact rather than lose it to the cancellation", got)
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 0 {
		t.Errorf("orgs_unevaluated = %v, want 0 -- this organization established something, which is exactly what unevaluated must not swallow", got)
	}

	// And the tick still has to say it did not finish. Keeping the
	// established failure moves the organization OUT of unevaluated, so
	// tick_complete can no longer be derived from that bucket alone --
	// truncation is observed separately, and this is the assertion that
	// holds the two apart.
	if got := summaryNumber(t, summary, "orgs_truncated"); got != 1 {
		t.Errorf("orgs_truncated = %v, want 1 -- the organization left the unevaluated bucket, so this counter is the only thing left saying the tick was cut short", got)
	}
	if summaryBool(t, summary, "tick_complete") {
		t.Error("tick_complete = true, want false -- the tick was cancelled mid-evaluation")
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
		t.Errorf("sources_failed = %v, want 0 -- OUR checkpoint store failed and the source was never called", got)
	}
	if got := summaryNumber(t, summary, "orgs_ok"); got != 0 {
		t.Errorf("orgs_ok = %v, want 0", got)
	}
}

// --- the 4789 shape end to end, in the phase it was never driven in ---

// twoSourceBuildLifecycleStore is buildFailingLifecycleStore with a SECOND
// required source. The sibling is the whole point: with only the failing
// source the organization falls to backoff for a reason that has nothing to
// do with the classification, and the defect does not appear.
type twoSourceBuildLifecycleStore struct {
	contextfabric.GraphLifecycleStore
	epoch int64
}

func (b *twoSourceBuildLifecycleStore) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	target := b.epoch
	return contextfabric.OrgGraphLifecycle{
		Status: contextfabric.LifecycleStatusBuilding, ActiveEpoch: 0, TargetEpoch: &target,
		RequiredSources: []string{"source-healthy", "dev_health_teams_projects"},
	}, true, nil
}

func (b *twoSourceBuildLifecycleStore) SourceProgress(context.Context, string, int64) ([]contextfabric.BuildSourceProgress, error) {
	return nil, nil
}

func (b *twoSourceBuildLifecycleStore) RecordSourceProgress(context.Context, string, int64, string, contextfabric.BuildCompletionMode, int64, time.Time) error {
	return nil
}

func (b *twoSourceBuildLifecycleStore) Flip(context.Context, string, int64, time.Duration, time.Time) (contextfabric.OrgGraphLifecycle, error) {
	return contextfabric.OrgGraphLifecycle{}, errors.New("not flipping in this fixture")
}

// TestStructural_TheOutageShapeExactlyDuringABuild is the 4789 shape driven
// end to end through the phase it was never driven in: three ticks, one
// required source failing beside a healthy sibling, during a live graph build.
//
// TestTheOutageShapeExactly already pins this for the steady-state pass. Its
// build twin never existed, and that gap is exactly where the surviving defect
// lived: the sibling evaluated fresh, the failing source was counted but could
// not reach the bucket, and the line read orgs_backoff:1 orgs_source_failed:0
// for as long as the build ran.
func TestStructural_TheOutageShapeExactlyDuringABuild(t *testing.T) {
	t.Parallel()

	healthy := &fakeSource{name: "source-healthy", pages: 1}
	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}
	checkpoints := newFakeCheckpointStore()

	// ONE coordinator across all three ticks -- see the sibling arm: the
	// failure backoff is coordinator state, and a fresh coordinator per tick
	// silently turns "withheld on later ticks" back into "failed again".
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-a"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-healthy", Source: healthy},
			{Name: "dev_health_teams_projects", Source: failing},
		},
		Backend:          newFakeBackend(),
		Checkpoints:      checkpoints,
		RebuildMarkers:   newFakeRebuildMarker(),
		Lifecycle:        &twoSourceBuildLifecycleStore{epoch: 1},
		EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		GraceWindow:      time.Hour,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	for i := 0; i < 3; i++ {
		coordinator.Tick(context.Background())
	}
	summaries := allFreshnessSummaries(t, &buffer)
	if len(summaries) != 3 {
		t.Fatalf("freshness summaries = %d, want 3 (one per tick)", len(summaries))
	}
	for i, summary := range summaries {
		tick := i + 1
		requireBucketIdentity(t, summary)
		requireSummaryScope(t, summary)

		if got := summaryNumber(t, summary, "orgs_source_failed"); got != 1 {
			t.Errorf("tick %d: orgs_source_failed = %v, want 1 -- a healthy sibling must not carry this organization out of the failing bucket, in a build any more than in steady state", tick, got)
		}
		if got := summaryNumber(t, summary, "orgs_ok"); got != 0 {
			t.Errorf("tick %d: orgs_ok = %v, want 0", tick, got)
		}
		if got := summaryNumber(t, summary, "orgs_backoff"); got != 0 {
			t.Errorf("tick %d: orgs_backoff = %v, want 0 -- backoff is what an operator reads as \"building, nothing wrong\"", tick, got)
		}

		// The union that must never go quiet: on tick one the source fails
		// an attempt, on later ticks its own failure backoff withholds it.
		failed, _ := summary["failed_sources"].([]any)
		withheld, _ := summary["failure_backoff_sources"].([]any)
		named := false
		for _, list := range [][]any{failed, withheld} {
			for _, name := range list {
				if name == "dev_health_teams_projects" {
					named = true
				}
			}
		}
		if !named {
			t.Errorf("tick %d: dev_health_teams_projects appears in neither failed_sources=%v nor failure_backoff_sources=%v", tick, summary["failed_sources"], summary["failure_backoff_sources"])
		}
	}

	if healthy.calls.Load() == 0 || failing.calls.Load() == 0 {
		t.Fatalf("fixture never drove both sources (healthy=%d failing=%d) -- without a sibling that evaluated fresh this arm cannot show the defect", healthy.calls.Load(), failing.calls.Load())
	}
}

// --- the shutdown cost, re-measured ---

// shutdownAtNthLoadStore cancels the whole tick partway through a fleet of
// organizations, the way a process being drained does.
type shutdownAtNthLoadStore struct {
	*fakeCheckpointStore
	cancel context.CancelFunc
	after  int32
	loads  atomic.Int32
}

func (s *shutdownAtNthLoadStore) LoadProjectionCheckpoint(ctx context.Context, org, source string) (contextfabric.ProjectionCheckpoint, error) {
	if s.loads.Add(1) == s.after {
		s.cancel()
	}
	if ctx.Err() != nil {
		// What a real dependency does once the tick's context is done: it
		// returns the BARE sentinel, having nothing of its own to add.
		return contextfabric.ProjectionCheckpoint{}, context.Canceled
	}
	return s.fakeCheckpointStore.LoadProjectionCheckpoint(ctx, org, source)
}

// TestStructural_AShutdownNamesNothing re-measures the cost the invariant of
// record priced: "in doubt, name the source -- cost: 20 orgs => 20 warnings".
//
// Two findings, and the second is the more useful one.
//
// First: with markPair classifying the RAW cause, an ordinary shutdown is no
// longer in doubt at all. Every stage sees a bare sentinel under a done tick,
// calls it truncation, and the line reports tick_complete:false rather than
// naming innocent sources.
//
// Second: the "20 orgs => 20 warnings" estimate was already pessimistic, and
// the measurement is what shows it. Each organization's per-source loop exits
// at its `scope.done()` guard, so at most ONE pair per organization is ever
// mid-flight when a cancellation lands -- and the organizations dispatched
// after it never reach a pair at all. The fleet cost of a shutdown is
// therefore bounded by concurrency, not by fleet size.
//
// Concurrency is pinned to 1 and the cancel is timed to land INSIDE the first
// organization's own RunOnce checkpoint load, because an arm whose
// cancellation lands between organizations observes no pair outcome and would
// pass against any classification at all. Measured: the P1-4 mutant (markPair
// wrapping before it classifies) turns this arm red, which is what makes it a
// pin rather than a green line.
func TestStructural_AShutdownNamesNothing(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	orgs := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		orgs = append(orgs, fmt.Sprintf("org-%02d", i))
	}
	observer := &recordingObserver{}
	// after:2 -- load 1 is runOrgLegacy's divergence probe for org-00, load 2
	// is that organization's own RunOnce. Cancelling there is the only timing
	// at which a pair is genuinely mid-flight.
	store := &shutdownAtNthLoadStore{fakeCheckpointStore: newFakeCheckpointStore(), cancel: cancel, after: 2}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         orgs,
		Sources:        []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
		Backend:        newFakeBackend(),
		Checkpoints:    store,
		RebuildMarkers: newFakeRebuildMarker(),
		Concurrency:    1,
		Observer:       observer,
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if store.loads.Load() < 2 {
		t.Fatalf("checkpoint loads = %d -- the shutdown never happened mid-tick", store.loads.Load())
	}
	// The vacuity guard this arm cannot do without: if no pair was mid-flight
	// when the cancellation landed, there is no classification to observe and
	// every count below would be zero for a reason that has nothing to do
	// with the fix.
	drains := observer.snapshot()
	if len(drains) == 0 {
		t.Fatal("no drain outcome was produced -- the cancellation landed between organizations, so no pair was ever classified and this arm proves nothing")
	}
	for _, drain := range drains {
		if drain.YieldReason != projectionrun.DrainYieldContextDone {
			t.Errorf("YieldReason = %q for %s/%s, want %q -- our own shutdown is not a failure of the pair", drain.YieldReason, drain.OrgID, drain.Source, projectionrun.DrainYieldContextDone)
		}
	}
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)

	if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
		t.Errorf("sources_failed = %v across a 20-organization shutdown, want 0 -- these are OUR cancellations, and paging twenty source owners for a deploy is the cost the invariant priced and no longer has to pay", got)
	}
	if got := summaryNumber(t, summary, "pair_failures"); got != 0 {
		t.Errorf("pair_failures = %v across a 20-organization shutdown, want 0 -- a cancellation passing through a stage that added nothing of its own is truncation, not a failure", got)
	}
	if summaryBool(t, summary, "tick_complete") {
		t.Error("tick_complete = true, want false -- the tick was cancelled mid-fleet")
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got < 1 {
		t.Errorf("orgs_unevaluated = %v, want at least 1 -- a cancelled fleet must show up as organizations with no verdict", got)
	}
}

// --- (e) and (f): the ladder amendment. Truncation is a MEMBER of the
// vocabulary, and established facts outrank it. ---

// buildCancellingStore returns a BARE context.Canceled from the build's own
// RunOnce, having cancelled the tick first: the build cut by a propagated
// cancellation, not by anything failing.
type buildCancellingStore struct {
	*fakeCheckpointStore
	cancel context.CancelFunc
	loads  atomic.Int32
}

func (s *buildCancellingStore) LoadProjectionCheckpoint(context.Context, string, string) (contextfabric.ProjectionCheckpoint, error) {
	s.loads.Add(1)
	s.cancel()
	return contextfabric.ProjectionCheckpoint{}, context.Canceled
}

// TestStructural_ABuildCutByPropagatedCancellationIsUnevaluated is amendment
// shape (e).
//
// A build that was cut short established nothing. It must not land in
// backoff -- backoff is what an operator reads as "building, nothing wrong",
// and that is a claim about a tick that finished looking. Truncation
// therefore outranks both backoff and stale in the ladder: those two are
// READINGS, and a tick cut short did not finish taking one.
//
// The build path signals this as truncated rather than evaluated:
// `evaluated: true` is reserved for a build that ran to a verdict.
func TestStructural_ABuildCutByPropagatedCancellationIsUnevaluated(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &buildCancellingStore{fakeCheckpointStore: newFakeCheckpointStore(), cancel: cancel}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-a"},
		Sources:          []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
		Backend:          newFakeBackend(),
		Checkpoints:      store,
		RebuildMarkers:   newFakeRebuildMarker(),
		Lifecycle:        &buildFailingLifecycleStore{epoch: 1},
		EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return store },
		GraceWindow:      time.Hour,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if store.loads.Load() == 0 {
		t.Fatal("the build never reached a checkpoint load -- the tick did not take the path under test")
	}
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	requireSummaryScope(t, summary)

	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 1 {
		t.Errorf("orgs_unevaluated = %v, want 1 -- a build cut by the tick's own cancellation reached no verdict", got)
	}
	if got := summaryNumber(t, summary, "orgs_backoff"); got != 0 {
		t.Errorf("orgs_backoff = %v, want 0 -- backoff reads as \"building, nothing wrong\", which is a claim about a tick that finished looking", got)
	}
	if got := summaryNumber(t, summary, "orgs_source_failed"); got != 0 {
		t.Errorf("orgs_source_failed = %v, want 0 -- nothing failed here; the tick was cancelled", got)
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
		t.Errorf("sources_failed = %v, want 0 -- our own cancelled checkpoint load must never name the source", got)
	}
	if summaryBool(t, summary, "tick_complete") {
		t.Error("tick_complete = true, want false")
	}
}

// TestStructural_ASourceFailureUnderATruncatedTickKeepsItsBucket is amendment
// shape (f), and it is the ORGANIZATION-bucket half of the pair-level rule
// pinned above.
//
// A source that failed, failed. The tick dying afterwards does not un-observe
// it -- so the organization stays in the source-failed bucket, the source
// stays NAMED, and the line still says the tick did not complete. Established
// facts outrank truncation; truncation outranks readings.
//
// tick_complete cannot be derived from the unevaluated bucket alone once that
// is true: this organization is legitimately NOT unevaluated, and a tick
// cancelled mid-flight must still report itself unfinished. Truncation is
// therefore observed separately from the bucket it no longer wins, which is
// also what keeps "cancelled while the source was already down" distinct from
// "ran to completion with the source down".
func TestStructural_ASourceFailureUnderATruncatedTickKeepsItsBucket(t *testing.T) {
	t.Parallel()
	for _, buildPhase := range []bool{false, true} {
		name := "steady state"
		if buildPhase {
			name = "build phase"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// The source definitively fails, and only THEN kills the tick.
			source := &failThenCancelSource{name: "dev_health_teams_projects", cancel: cancel}
			checkpoints := newFakeCheckpointStore()
			cfg := projectionrun.Config{
				OrgIDs:         []string{"org-a"},
				Sources:        []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: source}},
				Backend:        newFakeBackend(),
				Checkpoints:    checkpoints,
				RebuildMarkers: newFakeRebuildMarker(),
				Logger:         logger,
			}
			if buildPhase {
				cfg.Lifecycle = &buildFailingLifecycleStore{epoch: 1}
				cfg.EpochCheckpoints = func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints }
				cfg.GraceWindow = time.Hour
			}
			coordinator, err := projectionrun.NewCoordinator(cfg)
			if err != nil {
				t.Fatalf("new coordinator: %v", err)
			}
			coordinator.Tick(ctx)

			if source.calls.Load() == 0 {
				t.Fatal("the source never ran -- no failure was established, so this arm proves nothing")
			}
			summary := freshnessSummary(t, &buffer)
			requireBucketIdentity(t, summary)

			if got := summaryNumber(t, summary, "orgs_source_failed"); got != 1 {
				t.Errorf("orgs_source_failed = %v, want 1 -- the source failed BEFORE the tick was cancelled, and a cancellation arriving afterwards does not un-observe it", got)
			}
			if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 0 {
				t.Errorf("orgs_unevaluated = %v, want 0 -- this organization reached a verdict about a required source, which is exactly what unevaluated must not swallow", got)
			}
			if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
				t.Errorf("sources_failed = %v, want 1", got)
			}
			names, _ := summary["failed_sources"].([]any)
			if len(names) != 1 || names[0] != "dev_health_teams_projects" {
				t.Errorf("failed_sources = %v, want [dev_health_teams_projects] -- an unnamed failing source is the defect this line exists to prevent", summary["failed_sources"])
			}
			if got := summaryNumber(t, summary, "orgs_truncated"); got != 1 {
				t.Errorf("orgs_truncated = %v, want 1 -- the organization kept its source_failed bucket, so this counter is what still reports the tick unfinished", got)
			}
			if summaryBool(t, summary, "tick_complete") {
				t.Error("tick_complete = true, want false -- the tick was cancelled mid-flight, and keeping the established failure must not turn it into a finished tick")
			}
		})
	}
}

// TestStructural_AWithheldSourceReachesTheSourceBucketWithNothingEvaluated
// answers, as a pin rather than as prose, what the ladder returns for
// sourceFailed && !evaluated -- and shows the combination is REACHABLE rather
// than hypothetical.
//
// It is the second tick of an outage. The pair failed on tick one and entered
// its own failure backoff, so on tick two it is not due: nothing runs,
// `evaluated` is false, and `sourceFailed` is true because a pair withheld by
// the backoff its own failure set is still a source that is down.
//
// Under a ladder that opens with `!evaluated -> backoff` this reads
// orgs_backoff:1 orgs_source_failed:0 -- backoff hiding a source failure,
// which is the same class as the build path's defect. Established facts first
// is what makes it source_failed.
func TestStructural_AWithheldSourceReachesTheSourceBucketWithNothingEvaluated(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}

	// ONE coordinator across both ticks. The failure backoff lives in the
	// coordinator, not in the checkpoint store, so a fresh coordinator per
	// tick would re-run the pair every time and never reach the withheld
	// state this arm is about.
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: failing}},
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	coordinator.Tick(context.Background()) // establishes the failure, arms the backoff
	callsAfterFirst := failing.calls.Load()
	if callsAfterFirst == 0 {
		t.Fatal("the source never ran on tick one -- no failure was established, so tick two would not be the shape under test")
	}
	coordinator.Tick(context.Background()) // withheld: nothing runs

	if failing.calls.Load() != callsAfterFirst {
		t.Fatalf("the source ran again on tick two (calls %d -> %d) -- it was not withheld, so this arm does not exercise sourceFailed && !evaluated", callsAfterFirst, failing.calls.Load())
	}

	summaries := allFreshnessSummaries(t, &buffer)
	if len(summaries) != 2 {
		t.Fatalf("freshness summaries = %d, want 2 (one per tick)", len(summaries))
	}
	summary := summaries[1]
	requireBucketIdentity(t, summary)

	if got := summaryNumber(t, summary, "sources_evaluated"); got != 0 {
		t.Errorf("sources_evaluated = %v, want 0 -- the premise of this arm is that nothing ran this tick", got)
	}
	if got := summaryNumber(t, summary, "sources_in_failure_backoff"); got != 1 {
		t.Errorf("sources_in_failure_backoff = %v, want 1", got)
	}
	if got := summaryNumber(t, summary, "orgs_source_failed"); got != 1 {
		t.Errorf("orgs_source_failed = %v, want 1 -- a source withheld by the backoff its OWN failure set is still down, and the bucket has to say so", got)
	}
	if got := summaryNumber(t, summary, "orgs_backoff"); got != 0 {
		t.Errorf("orgs_backoff = %v, want 0 -- backoff here would be the failure hiding behind \"not due\", which is how this outage went quiet after tick one", got)
	}
}

// allFreshnessSummaries returns every summary the capture holds, in order, for
// the arms that drive more than one tick through one logger.
func allFreshnessSummaries(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if msg, _ := record["msg"].(string); msg == "context_fabric: projection tick freshness summary" {
			found = append(found, record)
		}
	}
	return found
}

// TestStructural_OrgsTruncatedIsEmittedUnguardedAndAtZero pins the new key on
// the emission that can actually observe the raw state, and on the zero path.
//
// A counter that appears only when non-zero cannot be told from a projector
// that does not emit it at all -- the same ambiguity one level up that let the
// outage hide behind orgs_ok. So a COMPLETE tick must print orgs_truncated=0
// explicitly, and tick_complete must be true beside it.
func TestStructural_OrgsTruncatedIsEmittedUnguardedAndAtZero(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "source-healthy", Source: &fakeSource{name: "source-healthy", pages: 1}}},
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	// summaryNumber fatals when the key is ABSENT, which is the assertion:
	// present and zero, never omitted because there was nothing to report.
	if got := summaryNumber(t, summary, "orgs_truncated"); got != 0 {
		t.Errorf("orgs_truncated = %v on a healthy complete tick, want an explicit 0", got)
	}
	if got := summaryNumber(t, summary, "orgs_stale"); got != 0 {
		t.Errorf("orgs_stale = %v on a healthy complete tick, want an explicit 0", got)
	}
	if !summaryBool(t, summary, "tick_complete") {
		t.Error("tick_complete = false on a tick that finished every organization, want true -- the derivation must not read every tick as truncated")
	}
	if got := summaryNumber(t, summary, "orgs_ok"); got != 1 {
		t.Errorf("orgs_ok = %v, want 1", got)
	}
}

// buildPairBreakingStore fails the BUILD's own checkpoint load with a
// concrete error of OURS -- not a cancellation, and not the source -- and only
// THEN cancels the tick.
//
// The build path has no divergence probe, so its first load is RunOnce's.
type buildPairBreakingStore struct {
	*fakeCheckpointStore
	cancel context.CancelFunc
	loads  atomic.Int32
}

func (s *buildPairBreakingStore) LoadProjectionCheckpoint(context.Context, string, string) (contextfabric.ProjectionCheckpoint, error) {
	s.loads.Add(1)
	err := fmt.Errorf("load projection checkpoint from postgres: %w", contextfabric.ErrUnavailable)
	s.cancel() // the tick dies AFTER our step has definitively broken
	return contextfabric.ProjectionCheckpoint{}, err
}

// TestStructural_ABuildPairFailureSurvivesATruncatedTick closes a genuine
// coverage gap that the mutation battery found, not a reviewer.
//
// Battery 34126152011 arm M4-BUILD-PAIR-FAILURE-DROPPED-WHEN-TRUNCATED
// SURVIVED: guarding the build path's recordPairFailure call on !truncated
// passed 2215 tests. Nothing in the suite drove a BUILD-path PAIR break under
// a truncated tick.
//
// The neighbouring arms look like they cover it and do not. (f)'s build_phase
// drives a SOURCE failure, so buildBroke is false there; the named-pair-failure
// pin drives the steady-state path. The build path's own copy of the rule was
// therefore unpinned -- which is exactly how this class survived eleven review
// rounds: the reported site was fixed and its sibling was not.
//
// Same rule as everywhere else: truncation suppresses claims of health, never
// facts already established.
func TestStructural_ABuildPairFailureSurvivesATruncatedTick(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &buildPairBreakingStore{fakeCheckpointStore: newFakeCheckpointStore(), cancel: cancel}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-a"},
		Sources:          []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: &fakeSource{name: "dev_health_teams_projects", pages: 1}}},
		Backend:          newFakeBackend(),
		Checkpoints:      store,
		RebuildMarkers:   newFakeRebuildMarker(),
		Lifecycle:        &buildFailingLifecycleStore{epoch: 1},
		EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return store },
		GraceWindow:      time.Hour,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if store.loads.Load() == 0 {
		t.Fatal("the build never reached a checkpoint load -- the tick did not take the path under test")
	}
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)

	if got := summaryNumber(t, summary, "pair_failures"); got != 1 {
		t.Errorf("pair_failures = %v, want 1 -- OUR checkpoint load broke during the build BEFORE the tick was cancelled, and a cancellation arriving afterwards does not un-observe it", got)
	}
	names, _ := summary["pair_failed"].([]any)
	if len(names) != 1 || names[0] != "dev_health_teams_projects:checkpoint_load" {
		t.Errorf("pair_failed = %v, want [dev_health_teams_projects:checkpoint_load]", summary["pair_failed"])
	}
	if got := summaryNumber(t, summary, "orgs_pair_failed"); got != 1 {
		t.Errorf("orgs_pair_failed = %v, want 1 -- the build broke at OUR step, and the bucket has to carry that rather than lose it to the cancellation", got)
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
		t.Errorf("sources_failed = %v, want 0 -- our checkpoint store broke, not the source", got)
	}
	if got := summaryNumber(t, summary, "orgs_truncated"); got != 1 {
		t.Errorf("orgs_truncated = %v, want 1", got)
	}
	if summaryBool(t, summary, "tick_complete") {
		t.Error("tick_complete = true, want false")
	}
}
