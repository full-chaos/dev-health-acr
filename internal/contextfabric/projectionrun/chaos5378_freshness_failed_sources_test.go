package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
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

// TestEveryBucketRecordingGoesThroughClassify is the INVARIANT pin, and it
// exists because the same defect was found three times at three different
// exits: the classification switch, the per-source loop's early return, then
// the organization-lock and lifecycle-build paths. Each time the fix was a
// patch at one more call site, and each time a reviewer found another site.
//
// So the rule is enforced structurally rather than by adding a fourth patch:
// every bucket recording goes through classify(), which owns the "a cancelled
// tick has established no verdict" decision once. A new exit that records a
// bucket directly is a test failure here, not a defect found two rounds
// later.
//
// Walked on the AST, not by text search: a commented-out direct call must not
// register (negative control below).
func TestEveryBucketRecordingGoesThroughClassify(t *testing.T) {
	t.Parallel()
	buckets := map[string]bool{
		"recordBackoff": true, "recordOK": true, "recordRebuildRequired": true,
		"recordSourceFailedOrg": true, "recordDivergenceRecovered": true,
	}
	direct := countDirectBucketCalls(t, "coordinator.go", buckets)
	if direct != 0 {
		t.Errorf("bucket recorders CALLED directly %d time(s) outside classify() -- every one must go through classify, which owns the cancelled-tick decision; a direct call is how the same defect reached three separate exits", direct)
	}

	// Negative control: a tree whose only direct call is commented out must
	// count 0, and one with a real direct call must count 1 -- proving the
	// walk sees code, not text.
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

func countDirectBucketCalls(t *testing.T, filename string, buckets map[string]bool) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return countDirectBucketCallsIn(file, buckets)
}

// countDirectBucketCallsIn counts CALLS to a bucket recorder. A bare
// reference (`stats.recordBackoff` handed to classify as a value) is NOT a
// call and is exactly the shape that is allowed, so only CallExpr counts.
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
		// Receiver-qualified deliberately. Coordinator has its OWN
		// recordBackoff(key, err) -- the per-pair retry scheduler, an
		// entirely different method that merely shares a name. Counting it
		// made this pin report six violations that were not violations,
		// which is the same "matched something adjacent to the question"
		// shape the stage-token substring trap has.
		recv, ok := sel.X.(*ast.Ident)
		if ok && recv.Name == "stats" {
			direct++
		}
		return true
	})
	return direct
}

// lockedLocker refuses the organization lock, which is the reviewer's
// org-lock cancellation shape: that path recorded orgs_backoff and claimed
// tick_complete:true even though cancellation was what stopped it.
type lockedLocker struct{}

func (lockedLocker) Lock(ctx context.Context, orgID string) (func() error, error) {
	return nil, ctx.Err()
}

// TestACancelledOrgLockIsNotReportedAsAnOrdinaryBackoff is the confirmation
// pass's P1. An organization whose lock attempt was interrupted by
// cancellation is not "backing off" in the ordinary sense -- the tick simply
// never got to it -- and reporting it as backoff on a line claiming
// tick_complete:true hides the cancellation entirely at Info.
func TestACancelledOrgLockIsNotReportedAsAnOrdinaryBackoff(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:         []string{"org-a"},
		Sources:        []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a", pages: 1}}},
		Backend:        newFakeBackend(),
		Checkpoints:    newFakeCheckpointStore(),
		RebuildMarkers: newFakeRebuildMarker(),
		Locker:         lockedLocker{},
		Logger:         logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(ctx)

	summary := freshnessSummary(t, &buffer)
	requireBucketIdentity(t, summary)
	if summaryBool(t, summary, "tick_complete") {
		t.Errorf("tick_complete = true while cancellation stopped the tick")
	}
	if got := summaryNumber(t, summary, "orgs_unevaluated"); got != 1 {
		t.Errorf("orgs_unevaluated = %v, want 1 -- a cancelled organization is unevaluated, not backing off", got)
	}
	if got := summaryNumber(t, summary, "orgs_backoff"); got != 0 {
		t.Errorf("orgs_backoff = %v, want 0 -- reporting cancellation as ordinary backoff hides it", got)
	}
}
