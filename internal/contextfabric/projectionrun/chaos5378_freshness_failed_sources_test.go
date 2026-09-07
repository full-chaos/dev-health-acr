package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// freshnessSummary returns the single decoded tick freshness summary the
// capture holds. Matched on the record's own msg, and asserted to be
// UNIQUE: a test that took "the first one" would keep passing if the line
// started firing twice per tick.
func freshnessSummary(t *testing.T, buffer *bytes.Buffer) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if msg, _ := record["msg"].(string); msg == "context_fabric: projection tick freshness summary" {
			found = append(found, record)
		}
	}
	if len(found) != 1 {
		t.Fatalf("tick freshness summary lines = %d, want exactly 1 per tick -- log:\n%s", len(found), buffer.String())
	}
	return found[0]
}

// requireSummaryScope asserts the line declares what it covers. A health
// summary that does not say its scope gets read as covering everything,
// which is how the line this disclosure repairs came to be trusted as a
// readiness signal it never was.
func requireSummaryScope(t *testing.T, record map[string]any) {
	t.Helper()
	if scope, _ := record["summary_scope"].(string); scope == "" {
		t.Errorf("the freshness summary does not name its scope; line: %v", record)
	}
}

func summaryNumber(t *testing.T, record map[string]any, key string) float64 {
	t.Helper()
	value, ok := record[key]
	if !ok {
		t.Fatalf("the freshness summary carries no %q field at all -- an absent count and a measured zero must never read alike; line: %v", key, record)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("%q = %v (%T), want a number", key, value, value)
	}
	return number
}

// TestTheFreshnessSummaryDisclosesAFailedRequiredSource is the readiness
// defect this ticket closes, and it is not hypothetical: this exact line
// reported orgs_ok:1, orgs_rebuild_required:0 every 15 seconds through the
// whole teams-projects outage, while that source failed every single tick
// with dependency_unavailable.
//
// The mechanism is the classification, not the logging: runPair drops
// runPairOnce's error and returns evaluated=true, stale=false for a source
// that failed, so runOrg's switch reads "evaluated, not stale" and records
// the organization OK as long as ANY sibling source came back fresh. The
// per-pair failure does reach a Warn line, but the Info summary an operator
// reads for readiness says green.
//
// Two sources, one healthy and one failing, is the minimum shape that
// discriminates: with only the failing source the org would fall to
// backoff and the bug would not appear.
func TestTheFreshnessSummaryDisclosesAFailedRequiredSource(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	backend := newFakeBackend()
	// pages:1 -- one batch then available=false, a source that genuinely
	// catches up. The default (pages:0) is an ever-growing stream that
	// drains until the fake backend refuses, which reports as a failed
	// source and would make this fixture's "healthy" sibling not healthy.
	healthy := &fakeSource{name: "source-healthy", pages: 1}
	failing := &fakeSource{name: "source-failing", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-a"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-healthy", Source: healthy},
			{Name: "source-failing", Source: failing},
		},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	if failing.calls.Load() == 0 {
		t.Fatal("the failing source was never called -- the tick did not reach the shape under test, so every assertion below would be vacuous")
	}
	if healthy.calls.Load() == 0 {
		t.Fatal("the healthy source was never called -- without a sibling that evaluated fresh this fixture cannot show the defect")
	}

	summary := freshnessSummary(t, &buffer)

	// The disclosure itself: how many configured sources failed this tick,
	// and which. Counts and names both, because "1 failed" does not tell an
	// operator whether the failing one is the source their question depends
	// on.
	if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
		t.Errorf("sources_failed = %v, want 1 -- one configured source failed every attempt this tick", got)
	}
	if got := summaryNumber(t, summary, "sources_evaluated"); got < 1 {
		t.Errorf("sources_evaluated = %v, want at least 1 -- the denominator a failure count is read against", got)
	}
	names, ok := summary["failed_sources"].([]any)
	if !ok {
		t.Fatalf("the freshness summary carries no failed_sources list -- a count alone cannot say WHICH source is down; line: %v", summary)
	}
	if len(names) != 1 || names[0] != "source-failing" {
		t.Errorf("failed_sources = %v, want [source-failing]", names)
	}

	// The classification half: the organization must no longer read as
	// plain OK while one of its configured sources failed every attempt.
	// Whatever bucket it lands in, orgs_ok is a green light and this
	// organization is not green.
	if got := summaryNumber(t, summary, "orgs_ok"); got != 0 {
		t.Errorf("orgs_ok = %v, want 0 -- an organization with a source failing every tick is not OK, and this line stayed green through a real outage", got)
	}
	if got := summaryNumber(t, summary, "orgs_source_failed"); got != 1 {
		t.Errorf("orgs_source_failed = %v, want 1", got)
	}
}

// TestTheFreshnessSummaryReportsExplicitZerosOnAHealthyTick is the other
// half, and the one that makes the counts above readable. A tick where
// nothing failed must still carry the failure fields, at zero: if they
// appeared only when non-zero, an operator could never tell a healthy tick
// from a build of the projector that does not emit them at all -- which is
// the same ambiguity that let the outage hide, one level up.
func TestTheFreshnessSummaryReportsExplicitZerosOnAHealthyTick(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	backend := newFakeBackend()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "source-healthy", Source: &fakeSource{name: "source-healthy", pages: 1}}},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	summary := freshnessSummary(t, &buffer)
	if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
		t.Errorf("sources_failed = %v, want an explicit 0", got)
	}
	if got := summaryNumber(t, summary, "orgs_source_failed"); got != 0 {
		t.Errorf("orgs_source_failed = %v, want an explicit 0", got)
	}
	names, ok := summary["failed_sources"].([]any)
	if !ok {
		t.Fatalf("failed_sources is absent on a healthy tick -- it must be present and empty, or a reader cannot tell a healthy tick from a projector that never emits it; line: %v", summary)
	}
	if len(names) != 0 {
		t.Errorf("failed_sources = %v, want empty on a healthy tick", names)
	}
	if got := summaryNumber(t, summary, "orgs_ok"); got != 1 {
		t.Errorf("orgs_ok = %v, want 1 -- a genuinely healthy organization must still read green", got)
	}
}

// TestAFailedSourceIsDistinguishableFromOneThatNeverRanThisTick is the
// missing-versus-measured-zero distinction applied to the sources
// themselves. Two counters -- evaluated and failed -- have a hole in the
// middle: a source that was not due this tick is absent from both, which
// looks exactly like a source that does not exist. An operator asking "is
// dev_health_teams_projects being read at all" cannot answer that from a
// line that only ever names what it did read.
//
// The three states must stay separable on one line: FAILED (ran, errored),
// NOT DUE (never ran this tick), and the state that is deliberately NOT a
// gap -- ran and legitimately found nothing, which is a success.
func TestAFailedSourceIsDistinguishableFromOneThatNeverRanThisTick(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	backend := newFakeBackend()
	failing := &fakeSource{name: "source-failing", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}
	// dormant:true is a source that RUNS and finds nothing -- a successful
	// empty population, which must never be reported as a gap.
	empty := &fakeSource{name: "source-empty", dormant: true}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-a"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-failing", Source: failing},
			{Name: "source-empty", Source: empty},
		},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	if failing.calls.Load() == 0 || empty.calls.Load() == 0 {
		t.Fatalf("both sources must have run (failing=%d empty=%d) for this arm to mean anything", failing.calls.Load(), empty.calls.Load())
	}

	summary := freshnessSummary(t, &buffer)

	// The line declares what it covers. A summary that does not say its
	// scope gets read as covering everything, which is how the line this
	// disclosure repairs came to be trusted as a readiness signal.
	requireSummaryScope(t, summary)

	// The empty source is a SUCCESS, not a gap.
	if got := summaryNumber(t, summary, "sources_evaluated"); got != 2 {
		t.Errorf("sources_evaluated = %v, want 2 -- both sources ran; a successful empty population is still an evaluation", got)
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
		t.Errorf("sources_failed = %v, want 1 -- only one source errored", got)
	}
	names, _ := summary["failed_sources"].([]any)
	if len(names) != 1 || names[0] != "source-failing" {
		t.Errorf("failed_sources = %v, want [source-failing] -- the empty source must not appear here", names)
	}

	// The third state is present and explicitly zero here: nothing was
	// skipped this tick, and saying so is what makes a NON-zero value on a
	// later tick readable.
	if got := summaryNumber(t, summary, "sources_in_failure_backoff"); got != 0 {
		t.Errorf("sources_in_failure_backoff = %v, want an explicit 0 -- both sources ran this tick", got)
	}
	withheld, ok := summary["failure_backoff_sources"].([]any)
	if !ok {
		t.Fatalf("the freshness summary carries no failure_backoff_sources list -- a source withheld by its own backoff is then indistinguishable from one that does not exist; line: %v", summary)
	}
	if len(withheld) != 0 {
		t.Errorf("failure_backoff_sources = %v, want empty", withheld)
	}
}

// TestTheOutageShapeExactly pins CHAOS-4789 as it actually presented, not a
// paraphrase of it: one source failing with dependency_unavailable on EVERY
// tick, beside a healthy sibling, for more than one tick -- and orgs_ok must
// never read 1 on any of them. The single-tick arm above could in principle
// be satisfied by a fix that only reports the first failure; this one cannot.
func TestTheOutageShapeExactly(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	backend := newFakeBackend()
	// pages:0 would drain unboundedly; the healthy sibling catches up.
	healthy := &fakeSource{name: "source-healthy", pages: 1}
	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-a"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-healthy", Source: healthy},
			{Name: "dev_health_teams_projects", Source: failing},
		},
		Backend:        backend,
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	const ticks = 3
	for i := 0; i < ticks; i++ {
		coordinator.Tick(context.Background())
	}

	var summaries []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if msg, _ := record["msg"].(string); msg == "context_fabric: projection tick freshness summary" {
			summaries = append(summaries, record)
		}
	}
	if len(summaries) != ticks {
		t.Fatalf("tick freshness summaries = %d, want %d (one per tick) -- log:\n%s", len(summaries), ticks, buffer.String())
	}

	for i, summary := range summaries {
		okCount, present := summary["orgs_ok"].(float64)
		if !present {
			t.Fatalf("tick %d summary carries no orgs_ok: %v", i+1, summary)
		}
		if okCount != 0 {
			t.Errorf("tick %d: orgs_ok = %v, want 0 -- this is the line that read 1 every fifteen seconds through the outage", i+1, okCount)
		}
		// On tick one the source FAILS an attempt; on later ticks its own
		// failure backoff withholds it, so it cannot appear in
		// sources_failed. The union of the two is what must never go
		// quiet -- a disclosure that fired only on the first failure would
		// leave the outage silent from tick two onward, which is exactly
		// what the real one did.
		failed, _ := summary["sources_failed"].(float64)
		withheld, _ := summary["sources_in_failure_backoff"].(float64)
		if failed+withheld < 1 {
			t.Errorf("tick %d: sources_failed=%v + sources_in_failure_backoff=%v, want the broken source disclosed on EVERY tick", i+1, failed, withheld)
		}
		var names []any
		if list, ok := summary["failed_sources"].([]any); ok {
			names = append(names, list...)
		}
		if list, ok := summary["failure_backoff_sources"].([]any); ok {
			names = append(names, list...)
		}
		found := false
		for _, name := range names {
			if name == "dev_health_teams_projects" {
				found = true
			}
		}
		if !found {
			t.Errorf("tick %d: neither failed_sources nor failure_backoff_sources names dev_health_teams_projects (%v)", i+1, names)
		}
	}
}

// servingLifecycleStore is the minimum lifecycle store that puts an
// organization in the STEADY state (serving, not building), so Tick takes
// runOrgLifecycle's per-source loop rather than runOrgLegacy's.
//
// The interface is EMBEDDED as a nil value rather than stubbed method by
// method: every method except Get is unreachable on the steady-state path,
// and a nil embedded interface panics if one is ever called. That is the
// behaviour worth having -- a hand-written stub returning zero values would
// let this arm keep passing if the path changed to call something else.
type servingLifecycleStore struct {
	contextfabric.GraphLifecycleStore
}

func (servingLifecycleStore) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	return contextfabric.OrgGraphLifecycle{Status: contextfabric.LifecycleStatusServing, ActiveEpoch: 0}, true, nil
}

// TestTheLifecyclePathDisclosesAFailedSourceToo is a coverage gap the
// mutation battery found and the tests did not: deleting the
// `sourceFailed = sourceFailed || pairFailed || pairWithheld` line from
// runOrgLifecycle SURVIVED the whole suite, because every arm above drives
// runOrgLegacy. The two paths carry the SAME classification switch by hand,
// so a fix applied to one and not the other is invisible — which is exactly
// the shape of the defect this ticket exists to close, one code path over.
//
// A deployment with Config.Lifecycle set takes this path for every steady
// tick, so it is not a secondary case.
func TestTheLifecyclePathDisclosesAFailedSourceToo(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	healthy := &fakeSource{name: "source-healthy", pages: 1}
	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-a"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-healthy", Source: healthy},
			{Name: "dev_health_teams_projects", Source: failing},
		},
		Backend:          backend,
		Checkpoints:      checkpoints,
		RebuildMarkers:   newFakeRebuildMarker(),
		Lifecycle:        servingLifecycleStore{},
		EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	if failing.calls.Load() == 0 || healthy.calls.Load() == 0 {
		t.Fatalf("both sources must run on the lifecycle path (failing=%d healthy=%d) -- if neither ran, Tick did not take runOrgLifecycle and this arm proves nothing", failing.calls.Load(), healthy.calls.Load())
	}

	summary := freshnessSummary(t, &buffer)
	requireSummaryScope(t, summary)
	if got := summaryNumber(t, summary, "orgs_ok"); got != 0 {
		t.Errorf("orgs_ok = %v, want 0 -- the lifecycle path must not report an organization healthy while one of its sources failed every attempt", got)
	}
	if got := summaryNumber(t, summary, "orgs_source_failed"); got != 1 {
		t.Errorf("orgs_source_failed = %v, want 1 on the lifecycle path", got)
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
		t.Errorf("sources_failed = %v, want 1", got)
	}
	names, _ := summary["failed_sources"].([]any)
	if len(names) != 1 || names[0] != "dev_health_teams_projects" {
		t.Errorf("failed_sources = %v, want [dev_health_teams_projects]", names)
	}
}

// summaryBool reads a bool field, failing if it is absent — the same
// missing-vs-measured rule the counts follow.
func summaryBool(t *testing.T, record map[string]any, key string) bool {
	t.Helper()
	value, ok := record[key]
	if !ok {
		t.Fatalf("the freshness summary carries no %q field at all; line: %v", key, record)
	}
	b, ok := value.(bool)
	if !ok {
		t.Fatalf("%q = %v (%T), want a bool", key, value, value)
	}
	return b
}

// requireBucketIdentity is the invariant this change CLAIMED and did not
// have: every configured organization lands in exactly one bucket, so the
// buckets sum to the configured count.
//
// It was false before this change and false after it until now — divergence
// recovery returned having recorded `orgs_divergence_recovered` and no
// bucket at all, so one configured organization summed to zero and simply
// vanished from the line. A reviewer executed that. Asserting the identity
// here, rather than asserting individual buckets arm by arm, is what makes
// a future path that forgets to classify impossible to miss.
func requireBucketIdentity(t *testing.T, record map[string]any) {
	t.Helper()
	configured := summaryNumber(t, record, "orgs_configured")
	sum := summaryNumber(t, record, "orgs_ok") +
		summaryNumber(t, record, "orgs_rebuild_required") +
		summaryNumber(t, record, "orgs_backoff") +
		summaryNumber(t, record, "orgs_source_failed") +
		summaryNumber(t, record, "orgs_divergence_recovered") +
		summaryNumber(t, record, "orgs_unevaluated")
	if configured != sum {
		t.Errorf("bucket identity BROKEN: orgs_configured=%v but ok+rebuild_required+backoff+source_failed+divergence_recovered+unevaluated=%v -- an organization is unaccounted for on this line; %v", configured, sum, record)
	}
}

// cancellingSource cancels the tick's context from INSIDE the drain, on its
// first call, then returns. That is the reviewer's shape and it matters:
// cancelling before Tick means the organization is never dispatched and the
// classification switch -- the code the fix changes -- is never reached, so a
// pin built that way passes on the broken tree.
type cancellingSource struct {
	name   string
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (c *cancellingSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	c.calls.Add(1)
	c.cancel()
	return contextfabric.ProjectionBatch{}, false, ctx.Err()
}

func (c *cancellingSource) CurrentProjectionSourceVersion() string { return "test.v1" }

// TestACancelledTickSaysItDidNotFinish is the reviewer's first P1. A
// mid-drain cancellation reached the ordinary classification switch and
// recorded `orgs_ok:1` on a tick that never finished, with the cancellation
// warning sitting below Info -- so the summary asserted health for work it
// had not done.
//
// Cancellation is still NOT a source failure; that decision was right. What
// was wrong is letting an unfinished tick reach any verdict at all.
func TestACancelledTickSaysItDidNotFinish(t *testing.T) {
	t.Parallel()
	for _, twoSources := range []bool{false, true} {
		name := "one source"
		if twoSources {
			name = "two sources"
		}
		t.Run(name, func(t *testing.T) {
			var buffer bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			canceller := &cancellingSource{name: "source-cancel", cancel: cancel}
			sources := []projectionrun.SourcePair{{Name: "source-cancel", Source: canceller}}
			if twoSources {
				sources = append(sources, projectionrun.SourcePair{Name: "source-b", Source: &fakeSource{name: "source-b", pages: 1}})
			}

			coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
				OrgIDs: []string{"org-a"}, Sources: sources,
				Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(),
				RebuildMarkers: newFakeRebuildMarker(), Logger: logger,
			})
			if err != nil {
				t.Fatalf("new coordinator: %v", err)
			}
			coordinator.Tick(ctx)

			if canceller.calls.Load() == 0 {
				t.Fatal("the cancelling source never ran -- the tick was cancelled before dispatch, so the classification switch this pin exists for was never reached")
			}

			summary := freshnessSummary(t, &buffer)
			requireBucketIdentity(t, summary)
			if summaryBool(t, summary, "tick_complete") {
				t.Errorf("tick_complete = true on a cancelled tick")
			}
			if got := summaryNumber(t, summary, "orgs_ok"); got != 0 {
				t.Errorf("orgs_ok = %v, want 0 -- a tick that did not finish cannot report an organization healthy", got)
			}
			if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 1 {
				t.Errorf("orgs_unevaluated = %v, want 1 -- the organization the tick did not finish must be counted", got)
			}
		})
	}
}

// TestACompleteTickSaysSo keeps the healthy state assertable rather than
// inferred from the absence of a warning, and pins the explicit zero.
func TestACompleteTickSaysSo(t *testing.T) {
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
	if !summaryBool(t, summary, "tick_complete") {
		t.Errorf("tick_complete = false on a tick that finished")
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 0 {
		t.Errorf("orgs_unevaluated = %v, want an explicit 0", got)
	}
	if got := summaryNumber(t, summary, "orgs_ok"); got != 1 {
		t.Errorf("orgs_ok = %v, want 1", got)
	}
}

// TestABackoffExpiringMidDecisionStillDisclosesTheSource is the reviewer's
// third P1, and it was a genuine time-of-check/time-of-use race in the code
// this ticket added.
//
// `due()` and `inFailureBackoff()` each took their own clock reading. A
// backoff expiring BETWEEN them made due() refuse the attempt while
// inFailureBackoff() answered false, so the pair was unevaluated, unfailed
// and unwithheld at once — it appeared in no counter, and a healthy sibling
// carried the organization to green while the failing source was never
// retried that tick.
//
// The clock here advances on EVERY read, so any implementation that reads it
// twice to decide one question lands on opposite sides of the boundary. One
// read deciding both facts is immune by construction.
func TestABackoffExpiringMidDecisionStillDisclosesTheSource(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	base := time.Now().UTC()
	var mu sync.Mutex
	reads := 0
	// Every read advances by a full hour: two reads deciding one question
	// cannot agree about whether a backoff has expired.
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		reads++
		return base.Add(time.Duration(reads) * time.Hour)
	}

	healthy := &fakeSource{name: "source-healthy", pages: 1}
	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-a"},
		Sources: []projectionrun.SourcePair{
			{Name: "source-healthy", Source: healthy},
			{Name: "dev_health_teams_projects", Source: failing},
		},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(), Now: clock, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	const ticks = 3
	for i := 0; i < ticks; i++ {
		coordinator.Tick(context.Background())
	}

	var summaries []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if msg, _ := record["msg"].(string); msg == "context_fabric: projection tick freshness summary" {
			summaries = append(summaries, record)
		}
	}
	if len(summaries) != ticks {
		t.Fatalf("summaries = %d, want %d", len(summaries), ticks)
	}
	for i, summary := range summaries {
		requireBucketIdentity(t, summary)
		if got := summaryNumber(t, summary, "orgs_ok"); got != 0 {
			t.Errorf("tick %d: orgs_ok = %v, want 0 -- a clock that moves between two reads must not be able to make a failing source vanish", i+1, got)
		}
		failed := summaryNumber(t, summary, "sources_failed")
		withheld := summaryNumber(t, summary, "sources_in_failure_backoff")
		if failed+withheld < 1 {
			t.Errorf("tick %d: sources_failed=%v + sources_in_failure_backoff=%v, want the broken source disclosed on every tick", i+1, failed, withheld)
		}
	}
}

// countNowCallsIn returns how many times fn's body calls c.now(). Walked on
// the AST: a text search would count a mention in a comment, and the whole
// point here is a SECOND real call.
func countNowCallsIn(t *testing.T, filename, fnName string) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found, calls := false, 0
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			return true
		}
		found = true
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "now" {
				calls++
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatalf("no function %q in %s -- the pin is asserting about code that no longer exists", fnName, filename)
	}
	return calls
}

// TestDueStateReadsTheClockExactlyOnce pins the invariant behind the
// reviewer's third P1 structurally, because the behavioural arm cannot
// construct the boundary reliably: the backoff delay carries jitter, so a
// test clock cannot be placed just-before and just-after `nextAttempt` on
// demand.
//
// The defect was two reads deciding one question -- `due()` refusing an
// attempt while a second, later read answered "not in backoff", leaving the
// pair unevaluated, unfailed AND unwithheld. One read is what makes that
// impossible, so one read is what is asserted.
func TestDueStateReadsTheClockExactlyOnce(t *testing.T) {
	t.Parallel()
	if got := countNowCallsIn(t, "coordinator.go", "dueState"); got != 1 {
		t.Errorf("dueState calls c.now() %d times, want exactly 1 -- two reads deciding one question is the race this fixed: a backoff expiring between them makes a withheld pair report as neither withheld nor evaluated", got)
	}

	// Negative control: the same walk over a tree whose second read is
	// commented out must count 1, not 2 -- proving the walk sees code, not text.
	control, err := parser.ParseFile(token.NewFileSet(), "control.go", `package projectionrun

func (c *Coordinator) dueState(key string) (bool, bool) {
	now := c.now()
	// now2 := c.now()
	_ = now
	return true, false
}
`, 0)
	if err != nil {
		t.Fatalf("parse the negative control: %v", err)
	}
	calls := 0
	ast.Inspect(control, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "now" {
				calls++
			}
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("negative control counted %d now() calls, want 1 -- the walk is matching text, not the AST", calls)
	}
}

// lockedLocker cancels the tick from INSIDE the lock attempt, then fails.
//
// Cancelling before Tick would not do: the dispatch loop catches an
// already-cancelled context and the organization is never dispatched at all,
// so runOrg -- and the org-lock exit this arm exists for -- is never reached
// and the arm passes on the broken tree. That mistake has now been made
// twice in this file; the cancellation must come from inside the path under
// test.
type lockedLocker struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (l *lockedLocker) Lock(ctx context.Context, orgID string) (func() error, error) {
	l.calls.Add(1)
	l.cancel()
	return nil, context.Canceled
}

// cancellingLifecycleStore cancels the tick from inside the lifecycle read,
// which is the second exit the confirmation pass found. Same reasoning as
// lockedLocker: the cancellation has to happen on the path under test.
type cancellingLifecycleStore struct {
	contextfabric.GraphLifecycleStore
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (l *cancellingLifecycleStore) Get(ctx context.Context, orgID string) (contextfabric.OrgGraphLifecycle, bool, error) {
	l.calls.Add(1)
	l.cancel()
	return contextfabric.OrgGraphLifecycle{}, false, context.Canceled
}

// TestEveryPerOrgPathCommitsThroughTheFinalizer is the INVARIANT pin. The
// same defect was found at FOUR exits across three review rounds, each fix a
// patch at one more site. The rule is now structural: a bucket is committed
// only by the org-scope finalizer, and every per-org function receives the
// scope rather than the raw stats — so an exit CANNOT record a bucket
// directly, and one that decides nothing is still accounted for.
//
// Walked on the AST, receiver-qualified: Coordinator has its own
// recordBackoff(key, err) for per-pair retry scheduling, and matching on the
// selector name alone reported six violations that were not violations —
// the same "matched something adjacent to the question" shape as the
// stage-token substring trap.
func TestEveryPerOrgPathCommitsThroughTheFinalizer(t *testing.T) {
	t.Parallel()
	buckets := map[string]bool{
		"recordBackoff": true, "recordOK": true, "recordRebuildRequired": true,
		"recordSourceFailedOrg": true, "recordDivergenceRecovered": true,
	}
	if got := countDirectBucketCalls(t, "coordinator.go", buckets); got != 0 {
		t.Errorf("bucket recorders called directly %d time(s) on a `stats` receiver -- every commit must go through the org-scope finalizer", got)
	}

	// The finalizer must actually be installed, and exactly once.
	if got := countDeferredFinishes(t, "coordinator.go"); got != 1 {
		t.Errorf("`defer scope.finish()` appears %d time(s), want exactly 1 -- zero means no exit is covered, more than one means an organization can be committed twice", got)
	}

	// Every per-org function takes the SCOPE, not the raw stats: that is what
	// makes a direct bucket call unavailable rather than merely discouraged.
	for _, fn := range []string{"runOrgLegacy", "runOrgLifecycle", "recoverFromDivergence", "recoverFromDivergenceLifecycle"} {
		if !takesOrgScope(t, "coordinator.go", fn) {
			t.Errorf("%s does not take *orgScope -- a per-org path holding raw stats can record a bucket without the finalizer", fn)
		}
	}

	// Negative controls both ways.
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		{"commented out", "package projectionrun\n\nfunc f(stats *tickFreshnessStats) {\n\t// stats.recordBackoff()\n\t_ = stats\n}\n", 0},
		{"a real direct call", "package projectionrun\n\nfunc f(stats *tickFreshnessStats) {\n\tstats.recordBackoff()\n}\n", 1},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "control.go", tc.src, 0)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		if got := countDirectBucketCallsIn(file, buckets); got != tc.want {
			t.Errorf("negative control %q counted %d, want %d -- the walk is matching text, not the AST", tc.name, got, tc.want)
		}
	}
}

func countDeferredFinishes(t *testing.T, filename string) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		def, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		if sel, ok := def.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "finish" {
			found++
		}
		return true
	})
	return found
}

func takesOrgScope(t *testing.T, filename, fnName string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		for _, param := range fn.Type.Params.List {
			star, ok := param.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if ident, ok := star.X.(*ast.Ident); ok && ident.Name == "orgScope" {
				return true
			}
		}
		return false
	}
	t.Fatalf("no function %q in %s -- the pin asserts about code that no longer exists", fnName, filename)
	return false
}

// TestReviewCancellationWhileWaitingForOrgLock is the confirmation pass's own
// test, adopted verbatim in shape: an organization whose lock attempt is
// interrupted by cancellation must not be published as an ordinary backoff on
// a line claiming the tick completed.
func TestReviewCancellationWhileWaitingForOrgLock(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	locker := &lockedLocker{cancel: cancel}

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a", pages: 1}}},
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Locker:         locker,
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if locker.calls.Load() == 0 {
		t.Fatal("the lock was never attempted -- the organization was not dispatched, so the org-lock exit this arm exists for was never reached")
	}
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	if summaryBool(t, summary, "tick_complete") {
		t.Errorf("tick_complete = true while cancellation stopped the tick")
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 1 {
		t.Errorf("orgs_unevaluated = %v, want 1", got)
	}
	if got := summaryNumber(t, summary, "orgs_backoff"); got != 0 {
		t.Errorf("orgs_backoff = %v, want 0 -- cancellation is not an ordinary backoff", got)
	}
}

// TestReviewCancellationDuringLifecycleBuildIsUnevaluated is the confirmation
// pass's second test: the lifecycle build path recorded backoff and claimed a
// complete tick when cancellation was what stopped it.
func TestReviewCancellationDuringLifecycleBuildIsUnevaluated(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &cancellingLifecycleStore{cancel: cancel}

	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-a"},
		Sources:          []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a", pages: 1}}},
		Backend:          newFakeBackend(),
		Checkpoints:      checkpoints,
		RebuildMarkers:   newFakeRebuildMarker(),
		Lifecycle:        store,
		EpochCheckpoints: func(int64) contextfabric.ProjectionCheckpointStore { return checkpoints },
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if store.calls.Load() == 0 {
		t.Fatal("the lifecycle row was never read -- the lifecycle exit this arm exists for was never reached")
	}
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	if summaryBool(t, summary, "tick_complete") {
		t.Errorf("tick_complete = true on a cancelled lifecycle tick")
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 1 {
		t.Errorf("orgs_unevaluated = %v, want 1 -- the lifecycle path must not report a cancelled organization as evaluated", got)
	}
}

func countDirectBucketCalls(t *testing.T, filename string, buckets map[string]bool) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return countDirectBucketCallsIn(file, buckets)
}

// countDirectBucketCallsIn counts CALLS to a bucket recorder on a `stats`
// receiver. Two deliberate narrowings:
//
//   - a bare reference (`scope.stats.recordBackoff` handed to record() as a
//     value) is not a CallExpr and is exactly the shape that is allowed;
//   - the receiver must be `stats`. Coordinator has its OWN
//     recordBackoff(key, err) -- the per-pair retry scheduler, a different
//     method that merely shares a name -- and counting it reported six
//     violations that were not violations.
func countDirectBucketCallsIn(file *ast.File, buckets map[string]bool) int {
	direct := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !buckets[sel.Sel.Name] {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "stats" {
			direct++
		}
		return true
	})
	return direct
}

// gatedUnlockLocker hands back an unlock that cancels the tick when it runs.
// The deferred unlock fires AFTER the organization's classification, so the
// context is cancelled only once a real verdict has been established -- the
// reviewer's shape for proving that "cancellation wins" erased real
// information.
type gatedUnlockLocker struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (l *gatedUnlockLocker) Lock(ctx context.Context, orgID string) (func() error, error) {
	l.calls.Add(1)
	return func() error { l.cancel(); return nil }, nil
}

// TestReviewCancellationAfterVerdict is the second confirmation pass's P1,
// adopted as it was written. An organization whose sources fully ran and
// whose classification returned a verdict keeps that verdict even if the
// context is cancelled immediately afterwards.
//
// This is the arm that refuted "cancellation wins": both this shape and the
// mid-drain shape have a cancelled context by the time finish() runs, and
// they need opposite answers. Completion is the discriminator, not the
// context — which is why finish() no longer reads ctx at all.
func TestReviewCancellationAfterVerdict(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source := &fakeSource{name: "source-a", pages: 1}
	locker := &gatedUnlockLocker{cancel: cancel}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: source}},
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Locker:         locker,
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if source.calls.Load() == 0 || locker.calls.Load() == 0 {
		t.Fatalf("the organization was not fully evaluated (source calls=%d, lock calls=%d) -- this arm asserts that a COMPLETED verdict survives a later cancellation, so it would prove nothing", source.calls.Load(), locker.calls.Load())
	}

	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	if got := summaryNumber(t, summary, "orgs_ok"); got != 1 {
		t.Errorf("orgs_ok = %v, want 1 -- the organization finished its work; cancelling afterwards must not erase the verdict it established", got)
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 0 {
		t.Errorf("orgs_unevaluated = %v, want 0 -- this organization WAS evaluated", got)
	}
	if !summaryBool(t, summary, "tick_complete") {
		t.Errorf("tick_complete = false although every configured organization reached a verdict")
	}
}

// TestNoCounterIsWrittenOutsideTheFinalizer extends the structural pin to the
// second confirmation pass's P2: Tick incremented orgsUnevaluated directly
// for organizations it never dispatched, which made "the finalizer is the
// only commit path" nearly true rather than true. No runtime identity break
// came of it -- the point is that the pin did not enforce what its name
// claimed.
func TestNoCounterIsWrittenOutsideTheFinalizer(t *testing.T) {
	t.Parallel()
	if got := countUnevaluatedWritesOutsideFinish(t, "coordinator.go"); got != 0 {
		t.Errorf("orgsUnevaluated is written %d time(s) outside finish() -- every commit must go through the finalizer, or a bypass can diverge from its semantics without any test noticing", got)
	}

	// Negative control: a direct write outside finish must be counted.
	file, err := parser.ParseFile(token.NewFileSet(), "control.go", `package projectionrun

func f(stats *tickFreshnessStats) {
	atomic.AddInt64(&stats.orgsUnevaluated, 1)
}
`, 0)
	if err != nil {
		t.Fatalf("parse the negative control: %v", err)
	}
	if got := countUnevaluatedWritesIn(file); got != 1 {
		t.Fatalf("negative control counted %d direct writes, want 1 -- the walk is not seeing the write it exists to catch", got)
	}
}

// countUnevaluatedWritesOutsideFinish counts references to orgsUnevaluated in
// any function OTHER than finish(), which is allowed to touch it.
func countUnevaluatedWritesOutsideFinish(t *testing.T, filename string) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	found := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// finish() commits it; recordUnevaluated is the accessor finish calls.
		if fn.Name.Name == "finish" || fn.Name.Name == "recordUnevaluated" {
			continue
		}
		found += countUnevaluatedWritesIn(fn)
	}
	return found
}

// countUnevaluatedWritesIn counts MUTATIONS of orgsUnevaluated, not
// references to it. The summary line reads the counter with
// atomic.LoadInt64, which is not a bypass of the finalizer and must not be
// counted -- counting it made this pin report the log line itself as a
// violation, which is the same "matched something adjacent to the question"
// shape as the earlier receiver mix-up.
func countUnevaluatedWritesIn(n ast.Node) int {
	found := 0
	ast.Inspect(n, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (fn.Sel.Name != "AddInt64" && fn.Sel.Name != "StoreInt64") {
			return true
		}
		for _, arg := range call.Args {
			unary, ok := arg.(*ast.UnaryExpr)
			if !ok || unary.Op != token.AND {
				continue
			}
			if sel, ok := unary.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "orgsUnevaluated" {
				found++
			}
		}
		return true
	})
	return found
}

// selfCancellingSource returns a context error OF ITS OWN MAKING while the
// tick's context stays live. It is a failing source, not a cancelled tick,
// and the difference is the whole of the reviewer's fourth P1.
type selfCancellingSource struct {
	name  string
	calls atomic.Int32
}

func (s *selfCancellingSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	s.calls.Add(1)
	return contextfabric.ProjectionBatch{}, false, context.Canceled
}

func (s *selfCancellingSource) CurrentProjectionSourceVersion() string { return "test.v1" }

// TestASourceOwnedContextErrorIsAFailureNotATruncation is the reviewer's
// fourth P1. A source returning context.Canceled while the TICK's context is
// perfectly live has failed. Classifying it as truncation hid a terminal
// source failure as unevaluated work and, worse, stopped the line naming the
// failing source -- which is the disclosure this whole ticket exists for.
func TestASourceOwnedContextErrorIsAFailureNotATruncation(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	source := &selfCancellingSource{name: "dev_health_teams_projects"}
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
	// context.Background() -- the tick is NEVER cancelled.
	coordinator.Tick(context.Background())

	if source.calls.Load() == 0 {
		t.Fatal("the source never ran")
	}
	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	if !summaryBool(t, summary, "tick_complete") {
		t.Errorf("tick_complete = false although the tick's own context was never cancelled")
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 0 {
		t.Errorf("orgs_unevaluated = %v, want 0 -- the tick was not cancelled; the SOURCE failed", got)
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
		t.Errorf("sources_failed = %v, want 1 -- a source returning a context error under a live tick has failed", got)
	}
	names, _ := summary["failed_sources"].([]any)
	if len(names) != 1 || names[0] != "dev_health_teams_projects" {
		t.Errorf("failed_sources = %v, want the failing source NAMED -- hiding it as unevaluated is the defect", names)
	}
}

// TestTheScopeOwnsTheContext is the invariant pin for model 7, the seventh
// and last structural form of this rule. Six earlier models recorded
// completion, or truncation, at sites chosen by hand; each was refuted by the
// next review on a site the previous had not considered — including, in the
// end, an operation nobody had noticed took a context at all.
//
// Enumerating the sites is what kept failing. Model 7 removes the ability to
// have an unobserved site: the per-org functions take no context, so the only
// way to reach the tick's context for an operation is scope.run, which
// observes automatically. This pin asserts that shape rather than any
// symptom, because every symptom-level pin so far was satisfied by a broken
// model.
func TestTheScopeOwnsTheContext(t *testing.T) {
	t.Parallel()
	const file = "coordinator.go"
	perOrg := []string{"runOrgLegacy", "runOrgLifecycle", "recoverFromDivergence", "recoverFromDivergenceLifecycle", "runBuildTick"}

	// 1. No per-org function may take a context: that is what makes an
	//    unobserved operation unwritable rather than merely discouraged.
	for _, fn := range perOrg {
		if takesContext(t, file, fn) {
			t.Errorf("%s takes a context.Context -- a per-org function holding the tick context can invoke an operation without observation, which is how six earlier models failed", fn)
		}
	}

	// 2. Inside those functions, every call that passes a context must be
	//    lexically inside scope.run — except slog, which cannot be cancelled
	//    and has no outcome to observe.
	for _, fn := range perOrg {
		if bad := contextCallsOutsideRun(t, file, fn); len(bad) != 0 {
			t.Errorf("%s passes a context outside scope.run at: %v", fn, bad)
		}
	}

	// 3. logCtx is the logging exemption and must not become a back door: it
	//    may appear ONLY as the ctx argument of a c.logger.*Context call.
	if bad := logCtxMisuses(t, file); len(bad) != 0 {
		t.Errorf("scope.logCtx() reaches a non-logger call at: %v -- the exemption exists for slog and nothing else", bad)
	}

	// 4. tick_complete is derived at the summary, never assigned.
	if got := countCallsNamed(t, file, "markIncomplete"); got != 0 {
		t.Errorf("markIncomplete is called %d time(s) -- tick_complete is derived from the bucket identity, never set by hand; the flag and orgs_unevaluated disagreed on every path that forgot it", got)
	}

	// Negative controls, both directions.
	ctlBad, err := parser.ParseFile(token.NewFileSet(), "c.go", `package projectionrun

func (c *Coordinator) runOrgLegacy(scope *orgScope, orgID string) {
	c.thing(scope.logCtx(), orgID)
}
`, 0)
	if err != nil {
		t.Fatalf("parse control: %v", err)
	}
	if got := logCtxMisusesIn(ctlBad); len(got) != 1 {
		t.Fatalf("negative control: a logCtx() handed to a non-logger call was NOT caught (%v)", got)
	}
	ctlOK, err := parser.ParseFile(token.NewFileSet(), "c.go", `package projectionrun

func (c *Coordinator) runOrgLegacy(scope *orgScope, orgID string) {
	c.logger.WarnContext(scope.logCtx(), "x")
}
`, 0)
	if err != nil {
		t.Fatalf("parse control: %v", err)
	}
	if got := logCtxMisusesIn(ctlOK); len(got) != 0 {
		t.Fatalf("negative control: a logCtx() in a logger call was wrongly flagged (%v)", got)
	}
}

func funcDecl(t *testing.T, filename, fnName string) *ast.FuncDecl {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == fnName {
			return fn
		}
	}
	t.Fatalf("no function %q in %s -- the pin asserts about code that no longer exists", fnName, filename)
	return nil
}

func takesContext(t *testing.T, filename, fnName string) bool {
	t.Helper()
	for _, param := range funcDecl(t, filename, fnName).Type.Params.List {
		if sel, ok := param.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Context" {
			return true
		}
	}
	return false
}

// contextCallsOutsideRun returns the callee names that receive a context
// argument while NOT lexically inside a scope.run literal. slog calls are
// exempt by name.
func contextCallsOutsideRun(t *testing.T, filename, fnName string) []string {
	t.Helper()
	fn := funcDecl(t, filename, fnName)
	var bad []string
	var walk func(n ast.Node, insideRun bool)
	walk = func(n ast.Node, insideRun bool) {
		ast.Inspect(n, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "run" {
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						walk(lit.Body, true)
					}
				}
				return false
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && strings.HasSuffix(sel.Sel.Name, "Context") {
				return true // slog: exempt
			}
			if !insideRun {
				for _, arg := range call.Args {
					if ident, ok := arg.(*ast.Ident); ok && ident.Name == "ctx" {
						bad = append(bad, exprName(call.Fun))
					}
				}
			}
			return true
		})
	}
	walk(fn.Body, false)
	return bad
}

func logCtxMisuses(t *testing.T, filename string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return logCtxMisusesIn(file)
}

func logCtxMisusesIn(n ast.Node) []string {
	var bad []string
	ast.Inspect(n, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		carries := false
		for _, arg := range call.Args {
			inner, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}
			if sel, ok := inner.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "logCtx" {
				carries = true
			}
		}
		if !carries {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasSuffix(sel.Sel.Name, "Context") {
			bad = append(bad, exprName(call.Fun))
		}
		return true
	})
	return bad
}

func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprName(v.X) + "." + v.Sel.Name
	}
	return "?"
}

func countCallsNamed(t *testing.T, filename, name string) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return countCallsNamedIn(file, name)
}

func countCallsNamedIn(n ast.Node, name string) int {
	found := 0
	ast.Inspect(n, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			found++
		}
		return true
	})
	return found
}

// buildFailingLifecycleStore puts an organization into a BUILDING lifecycle
// row so Tick takes runBuildTick, which is the phase whose source failures
// went unnamed.
type buildFailingLifecycleStore struct {
	contextfabric.GraphLifecycleStore
	epoch int64
}

func (b *buildFailingLifecycleStore) Get(context.Context, string) (contextfabric.OrgGraphLifecycle, bool, error) {
	target := b.epoch
	return contextfabric.OrgGraphLifecycle{
		Status: contextfabric.LifecycleStatusBuilding, ActiveEpoch: 0, TargetEpoch: &target,
		RequiredSources: []string{"dev_health_teams_projects"},
	}, true, nil
}

func (b *buildFailingLifecycleStore) SourceProgress(context.Context, string, int64) ([]contextfabric.BuildSourceProgress, error) {
	return nil, nil
}

func (b *buildFailingLifecycleStore) RecordSourceProgress(context.Context, string, int64, string, contextfabric.BuildCompletionMode, int64, time.Time) error {
	return nil
}

func (b *buildFailingLifecycleStore) Flip(context.Context, string, int64, time.Duration, time.Time) (contextfabric.OrgGraphLifecycle, error) {
	return contextfabric.OrgGraphLifecycle{}, errors.New("not flipping in this fixture")
}

// TestModel7Review_BuildFailureIsCountedAndNamed is the reviewer's test,
// adopted as written. A source failing during a LIVE GRAPH BUILD was executed
// by runBuildPair and then recorded nowhere: the summary printed
// sources_evaluated:0, sources_failed:0, failed_sources:[] while the source
// was down.
//
// The summary declared its own scope as steady-state-only, and that note was
// accurate. It was also not an excuse: a required source going unnamed is
// exactly what this line exists to prevent, whichever phase it fails in.
func TestModel7Review_BuildFailureIsCountedAndNamed(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}
	checkpoints := newFakeCheckpointStore()
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
	coordinator.Tick(context.Background())

	if failing.calls.Load() == 0 {
		t.Fatal("the source never ran -- the tick did not take the build path, so this arm would prove nothing")
	}

	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
		t.Errorf("sources_failed = %v, want 1 -- a required source failing during a build is still a required source that is down", got)
	}
	names, _ := summary["failed_sources"].([]any)
	if len(names) != 1 || names[0] != "dev_health_teams_projects" {
		t.Errorf("failed_sources = %v, want it NAMED -- an unnamed failing source is the defect this line exists to prevent", names)
	}
	if got := summaryNumber(t, summary, "build_sources_failed"); got != 1 {
		t.Errorf("build_sources_failed = %v, want 1 -- the build phase's own share must stay distinguishable", got)
	}
	buildNames, ok := summary["build_failed_sources"].([]any)
	if !ok || len(buildNames) != 1 || buildNames[0] != "dev_health_teams_projects" {
		t.Errorf("build_failed_sources = %v, want [dev_health_teams_projects]", summary["build_failed_sources"])
	}
	if scope, _ := summary["summary_scope"].(string); scope != "steady_state_and_build" {
		t.Errorf("summary_scope = %q, want steady_state_and_build -- the line must declare the scope it now actually covers", scope)
	}
}

// TestASteadyStateTickReportsExplicitBuildZeros keeps the build counters
// readable: present and zero when no build ran, so a reader never has to tell
// "no build failures" from "this build does not report them".
func TestASteadyStateTickReportsExplicitBuildZeros(t *testing.T) {
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
	if got := summaryNumber(t, summary, "build_sources_failed"); got != 0 {
		t.Errorf("build_sources_failed = %v, want an explicit 0", got)
	}
	names, ok := summary["build_failed_sources"].([]any)
	if !ok {
		t.Fatalf("build_failed_sources absent on a steady-state tick -- it must be present and empty; line: %v", summary)
	}
	if len(names) != 0 {
		t.Errorf("build_failed_sources = %v, want empty", names)
	}
}

// --- confirm5 findings: the build phase did not honour the steady-state
// three-state contract. All three are the SAME class -- the build path was
// given a disclosure but not the rules that make a disclosure honest.

// TestConfirm5_ACancelledBuildDoesNotAssertAReading is confirm5's first P1.
// runBuildTick recorded its pair outcome unconditionally after scope.run, so a
// tick truncated mid-drain still asserted sources_evaluated:1 -- a reading it
// never finished taking. This is the model 1-7 defect at PAIR granularity: the
// organization was correctly marked unevaluated while the per-source counters
// beside it claimed the work had been done.
func TestConfirm5_ACancelledBuildDoesNotAssertAReading(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	canceller := &cancellingSource{name: "dev_health_teams_projects", cancel: cancel}
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-a"},
		Sources:          []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: canceller}},
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
	coordinator.Tick(ctx)

	if canceller.calls.Load() == 0 {
		t.Fatal("the source never ran -- the tick did not reach the build drain, so this arm would prove nothing")
	}
	summary := freshnessSummary(t, &buffer)
	if got := summaryBool(t, summary, "tick_complete"); got {
		t.Fatalf("tick_complete = true on a cancelled tick -- the fixture did not truncate, so the arm proves nothing")
	}
	if got := summaryNumber(t, summary, "sources_evaluated"); got != 0 {
		t.Errorf("sources_evaluated = %v on a TRUNCATED tick, want 0 -- a tick that did not finish has no reading to report for the pair it cut short", got)
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 1 {
		t.Errorf("orgs_unevaluated = %v, want 1", got)
	}
}

// TestConfirm5_ASourceOwnedCancelDuringBuildIsAFailure is confirm5's second
// P1. runBuildPair reported failure only for DrainYieldError, but a source
// returning context.Canceled from its OWN internals under a live tick yields
// DrainYieldContextDone -- so the build path called it healthy while the
// steady-state path (TestASourceOwnedContextErrorIsAFailureNotATruncation)
// calls the identical shape a failure. The same source must not read
// differently depending on which phase happened to be running.
func TestConfirm5_ASourceOwnedCancelDuringBuildIsAFailure(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	source := &selfCancellingSource{name: "dev_health_teams_projects"}
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-a"},
		Sources:          []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: source}},
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
	// context.Background() -- the TICK is never cancelled.
	coordinator.Tick(context.Background())

	if source.calls.Load() == 0 {
		t.Fatal("the source never ran -- the tick did not reach the build drain")
	}
	summary := freshnessSummary(t, &buffer)
	if got := summaryBool(t, summary, "tick_complete"); !got {
		t.Fatalf("tick_complete = false, but the tick's own context was never cancelled -- the fixture is wrong, not the code")
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
		t.Errorf("sources_failed = %v, want 1 -- a source returning context.Canceled under a LIVE tick has failed, in the build phase exactly as in steady state", got)
	}
	names, _ := summary["failed_sources"].([]any)
	if len(names) != 1 || names[0] != "dev_health_teams_projects" {
		t.Errorf("failed_sources = %v, want it NAMED", summary["failed_sources"])
	}
	if got := summaryNumber(t, summary, "build_sources_failed"); got != 1 {
		t.Errorf("build_sources_failed = %v, want 1", got)
	}
}

// TestConfirm5_ABuildSourceStaysNamedOnTheSecondTick is confirm5's third and
// worst P1: the CHAOS-4789 outage shape, reproduced inside the phase this
// change had just claimed to cover. The build drain has its own failure
// backoff key, so on tick two the pair is withheld and never runs -- and
// because build outcomes had no withheld state, the source vanished from the
// line entirely. That is the exact silence this ticket exists to end, and it
// made summary_scope:"steady_state_and_build" a false claim.
func TestConfirm5_ABuildSourceStaysNamedOnTheSecondTick(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	failing := &fakeSource{name: "dev_health_teams_projects", err: fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)}
	checkpoints := newFakeCheckpointStore()
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

	for tick := 1; tick <= 2; tick++ {
		buffer.Reset()
		coordinator.Tick(context.Background())
		summary := freshnessSummary(t, &buffer)
		requireBucketIdentity(t, summary)

		failedNames, _ := summary["failed_sources"].([]any)
		withheldNames, _ := summary["failure_backoff_sources"].([]any)
		named := false
		for _, list := range [][]any{failedNames, withheldNames} {
			for _, n := range list {
				if n == "dev_health_teams_projects" {
					named = true
				}
			}
		}
		if !named {
			t.Errorf("tick %d: the failing build source is named in NEITHER failed_sources (%v) nor failure_backoff_sources (%v) -- this is the CHAOS-4789 silence, inside the build phase",
				tick, summary["failed_sources"], summary["failure_backoff_sources"])
		}
		if got := summaryNumber(t, summary, "sources_failed") + summaryNumber(t, summary, "sources_in_failure_backoff"); got != 1 {
			t.Errorf("tick %d: sources_failed + sources_in_failure_backoff = %v, want 1 -- a source that is still down must be counted on EVERY tick, not only the one it happened to run on", tick, got)
		}
	}
	if failing.calls.Load() == 0 {
		t.Fatal("the source never ran on either tick")
	}
}

// TestConfirm5_ACancelledSteadyStateTickDoesNotAssertAReading is the SWEEP.
// confirm5 found the unconditional pair recording in the build path, but the
// steady-state loop had the identical shape and the identical consequence, so
// fixing only the reported instance would have left the class alive in the
// path the ticket is actually about.
func TestConfirm5_ACancelledSteadyStateTickDoesNotAssertAReading(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	canceller := &cancellingSource{name: "source-cancel", cancel: cancel}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "source-cancel", Source: canceller}},
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	if canceller.calls.Load() == 0 {
		t.Fatal("the source never ran -- the tick did not reach the steady-state drain")
	}
	summary := freshnessSummary(t, &buffer)
	if got := summaryBool(t, summary, "tick_complete"); got {
		t.Fatalf("tick_complete = true on a cancelled tick -- the fixture did not truncate")
	}
	if got := summaryNumber(t, summary, "sources_evaluated"); got != 0 {
		t.Errorf("sources_evaluated = %v on a TRUNCATED steady-state tick, want 0 -- the same rule the build path now follows", got)
	}
}

// TestEveryPairOutcomeGoesThroughTheScope pins the rule structurally, because
// the behavioural arms above only cover the two call sites that exist today.
// The build path was added WITHOUT the truncation guard and no test noticed;
// a third path would be added the same way. Every recordPairOutcome and
// recordBuildPairOutcome call must sit inside a scope.recordPair closure.
func TestEveryPairOutcomeGoesThroughTheScope(t *testing.T) {
	t.Parallel()
	if got := countPairRecordingsOutsideScope(t, "."); got != 0 {
		t.Errorf("%d pair-outcome recording(s) bypass scope.recordPair -- a truncated tick can assert a reading it never took, which is exactly how the build path shipped broken", got)
	}

	// Negative control: a bare recording outside the closure must be counted.
	file, err := parser.ParseFile(token.NewFileSet(), "control.go", `package projectionrun

func f(scope *orgScope, source string) {
	scope.stats.recordPairOutcome(source, true, false, false, false)
}
`, 0)
	if err != nil {
		t.Fatalf("parse the negative control: %v", err)
	}
	if got := countPairRecordingsOutsideScopeIn(file); got != 1 {
		t.Fatalf("negative control counted %d bypasses, want 1 -- the walk is not seeing the bypass it exists to catch", got)
	}

	// confirm6 P3: the pin matched call expressions only, so `record :=
	// scope.stats.recordPairOutcome; record(...)` reported ZERO bypasses.
	// A pin whose job is to stop the next call site being added unguarded
	// must see the alias too.
	aliased, err := parser.ParseFile(token.NewFileSet(), "alias.go", `package projectionrun

func h(scope *orgScope, source string) {
	record := scope.stats.recordPairOutcome
	record(source, true, false, false, false)
}
`, 0)
	if err != nil {
		t.Fatalf("parse the aliasing control: %v", err)
	}
	if got := countPairRecordingsOutsideScopeIn(aliased); got != 1 {
		t.Fatalf("aliasing control counted %d bypasses, want 1 -- a method VALUE defeats a call-only pin, which confirm6 executed against the previous version", got)
	}

	// Positive control: the guarded form must NOT be counted, or the pin
	// would fire on correct code and get "fixed" by deleting it.
	ok, err := parser.ParseFile(token.NewFileSet(), "ok.go", `package projectionrun

func g(scope *orgScope, source string) {
	scope.recordPair(func(truncated bool) {
		scope.stats.recordPairOutcome(source, true, false, false, truncated)
	})
}
`, 0)
	if err != nil {
		t.Fatalf("parse the positive control: %v", err)
	}
	if got := countPairRecordingsOutsideScopeIn(ok); got != 0 {
		t.Fatalf("positive control counted %d bypasses in correctly guarded code, want 0", got)
	}
}

// countPairRecordingsOutsideScope counts recordPairOutcome /
// recordBuildPairOutcome calls that are not lexically inside a
// scope.recordPair(func(){...}) argument.
// It scans EVERY non-test file in the package, not just coordinator.go: a
// recorder added from a sibling file was invisible to the earlier version,
// which confirm7 demonstrated with an out-of-scope control.
//
// KNOWN LIMIT, stated rather than implied. This is a syntactic walk, so it
// sees direct selectors and method values but cannot follow a recorder
// reached through an interface, a struct field, or a function returned by a
// helper -- confirm7 executed all three and this pin reports 0 for each. It
// catches the way a call site is actually added by hand; it is not a proof
// that no path exists. A pin that pretended otherwise would be worse than one
// that names its boundary.
func countPairRecordingsOutsideScope(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	scanned, found := 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		found += countPairRecordingsOutsideScopeIn(file)
	}
	if scanned == 0 {
		t.Fatalf("scanned no production files in %s -- the pin would pass vacuously", dir)
	}
	return found
}

func countPairRecordingsOutsideScopeIn(root ast.Node) int {
	// Collect the closure bodies passed to recordPair; anything inside one is
	// guarded. Positions are used rather than node identity so the second
	// walk does not have to rebuild the parent chain.
	type span struct{ lo, hi token.Pos }
	var guarded []span
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "recordPair" {
			return true
		}
		for _, arg := range call.Args {
			if lit, ok := arg.(*ast.FuncLit); ok {
				guarded = append(guarded, span{lit.Pos(), lit.End()})
			}
		}
		return true
	})

	found := 0
	ast.Inspect(root, func(n ast.Node) bool {
		// Every SELECTOR naming the recorders counts, not only call
		// expressions: `record := scope.stats.recordPairOutcome` is a method
		// VALUE that a call-only walk never sees, and confirm6 executed
		// exactly that against the previous version of this pin and got 0.
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// Match the METHOD name only; the receiver is stats, not scope, so a
		// receiver-qualified match would miss the very sites being pinned.
		if sel.Sel.Name != "recordPairOutcome" && sel.Sel.Name != "recordBuildPairOutcome" {
			return true
		}
		// The method DECLARATIONS carry the name on a FuncDecl, not a
		// selector, so they cannot land here.
		for _, g := range guarded {
			if sel.Pos() >= g.lo && sel.End() <= g.hi {
				return true
			}
		}
		found++
		return true
	})
	return found
}

// TestConfirm5_AHealthyBuildSourceIsCountedAsEvaluated closes a real gap a
// surviving mutant found: deleting sourcesEvaluated++ from
// recordBuildPairOutcome's success branch changed nothing any test could see.
// Every build arm drove a FAILING source, so the branch that reports a build
// source having run CLEANLY was never exercised.
//
// It matters for the same reason the steady-state twin does: a successful
// source must be reported as a success, never as an absence. `sources_failed:0`
// only means "everything that ran, ran clean" if the things that ran are
// actually counted -- otherwise a healthy tick and a tick that measured
// nothing print the same line.
func TestConfirm5_AHealthyBuildSourceIsCountedAsEvaluated(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))

	healthy := &fakeSource{name: "dev_health_teams_projects", pages: 1}
	checkpoints := newFakeCheckpointStore()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-a"},
		Sources:          []projectionrun.SourcePair{{Name: "dev_health_teams_projects", Source: healthy}},
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
	coordinator.Tick(context.Background())

	if healthy.calls.Load() == 0 {
		t.Fatal("the source never ran -- the tick did not take the build path, so this arm would prove nothing")
	}
	summary := freshnessSummary(t, &buffer)
	if got := summaryNumber(t, summary, "sources_evaluated"); got != 1 {
		t.Errorf("sources_evaluated = %v, want 1 -- a build source that RAN and succeeded must be counted, or a healthy tick is indistinguishable from one that measured nothing", got)
	}
	if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
		t.Errorf("sources_failed = %v, want 0 -- the source succeeded", got)
	}
	if got := summaryNumber(t, summary, "build_sources_failed"); got != 0 {
		t.Errorf("build_sources_failed = %v, want 0", got)
	}
	names, ok := summary["failed_sources"].([]any)
	if !ok || len(names) != 0 {
		t.Errorf("failed_sources = %v, want an empty list", summary["failed_sources"])
	}
}

// failThenCancelSource fails with a REAL, non-context error and only then
// cancels the tick. The failure is a fact by the time the cancellation
// arrives, so it must survive it.
type failThenCancelSource struct {
	name   string
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (f *failThenCancelSource) NextProjectionBatch(ctx context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	f.calls.Add(1)
	err := fmt.Errorf("teams projects read: %w", contextfabric.ErrUnavailable)
	f.cancel() // the tick dies AFTER the source has definitively failed
	return contextfabric.ProjectionBatch{}, false, err
}

func (f *failThenCancelSource) CurrentProjectionSourceVersion() string { return "test.v1" }

// TestConfirm6_ACancellationAfterAFailureDoesNotUnobserveIt is confirm6's P1,
// and it is a defect my OWN fix for the previous round introduced. Truncation
// must suppress claims of HEALTH, not facts already established: a source that
// failed, failed, and a tick dying afterwards does not un-observe it.
//
// Dropping it is the ticket's own defect -- a required source down and
// unnamed -- reintroduced by the fix for the previous one.
func TestConfirm6_ACancellationAfterAFailureDoesNotUnobserveIt(t *testing.T) {
	t.Parallel()
	for _, buildPhase := range []bool{false, true} {
		name := "steady state"
		if buildPhase {
			name = "build phase"
		}
		t.Run(name, func(t *testing.T) {
			var buffer bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

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
				t.Fatal("the source never ran -- the tick did not reach the drain")
			}
			summary := freshnessSummary(t, &buffer)
			if got := summaryBool(t, summary, "tick_complete"); got {
				t.Fatalf("tick_complete = true -- the fixture did not truncate, so this arm proves nothing")
			}
			if got := summaryNumber(t, summary, "sources_failed"); got != 1 {
				t.Errorf("sources_failed = %v, want 1 -- the source failed with a real error BEFORE the cancellation; a tick dying afterwards does not un-observe it", got)
			}
			names, _ := summary["failed_sources"].([]any)
			if len(names) != 1 || names[0] != "dev_health_teams_projects" {
				t.Errorf("failed_sources = %v, want it NAMED -- an unnamed failing source is the defect this line exists to prevent", summary["failed_sources"])
			}
		})
	}
}

// TestConfirm7_ABarePropagatedCancellationIsStillTruncation is the other half,
// and it is what stops the rule above from simply naming everything. A source
// that returns the sentinel UNCHANGED added nothing of its own: that is the
// tick's cancellation passing through, and it must stay truncation.
func TestConfirm7_ABarePropagatedCancellationIsStillTruncation(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// cancellingSource returns ctx.Err() bare, after cancelling.
	source := &cancellingSource{name: "dev_health_teams_projects", cancel: cancel}
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
	coordinator.Tick(ctx)

	if source.calls.Load() == 0 {
		t.Fatal("the source never ran")
	}
	summary := freshnessSummary(t, &buffer)
	if got := summaryNumber(t, summary, "sources_failed"); got != 0 {
		t.Errorf("sources_failed = %v, want 0 -- a BARE propagated sentinel is the tick's own cancellation passing through, not a source failure", got)
	}
	if got := summaryBool(t, summary, "tick_complete"); got {
		t.Errorf("tick_complete = true, want false")
	}
}
