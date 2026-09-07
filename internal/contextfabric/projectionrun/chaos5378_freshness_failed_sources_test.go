package projectionrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

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
	if scope, _ := summary["summary_scope"].(string); scope == "" {
		t.Errorf("the freshness summary does not name its scope; line: %v", summary)
	}

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
