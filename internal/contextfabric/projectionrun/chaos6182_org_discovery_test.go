package projectionrun_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// CHAOS-6182: acr-projector auto-discovers the organizations it projects,
// instead of serving only a hand-maintained env allowlist. These tests pin
// the three properties that decide whether that is safe to run
// unattended: a newly appearing organization gets a full build WITHOUT
// disturbing an existing organization's incremental progress; an
// enumeration failure never shrinks the served set; and every refresh says
// what it decided and why.

// fakeOrgSource is a scripted projectionrun.OrgSource: each ListOrgs call
// consumes the next scripted result, and the LAST one repeats forever, so a
// test states the sequence of enumerations rather than counting calls.
type fakeOrgSource struct {
	mu      sync.Mutex
	results []fakeOrgResult
	calls   int
}

type fakeOrgResult struct {
	orgs    []string
	skipped []contextfabric.SkippedOrg
	err     error
}

func (f *fakeOrgSource) ListOrgs(context.Context) (contextfabric.OrgDiscoveryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return contextfabric.OrgDiscoveryResult{}, nil
	}
	index := f.calls
	if index >= len(f.results) {
		index = len(f.results) - 1
	}
	f.calls++
	result := f.results[index]
	return contextfabric.OrgDiscoveryResult{
		OrgIDs:  append([]string(nil), result.orgs...),
		Skipped: append([]contextfabric.SkippedOrg(nil), result.skipped...),
	}, result.err
}

func (f *fakeOrgSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// capturingLogger returns a production-shaped JSON logger (the handler
// cmd/acr-projector actually wires to stdout) plus a reader over what it
// emitted. Asserting the EMITTED line, rather than a value returned to the
// test, is what makes the telemetry claim checkable.
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelInfo})), buffer
}

func logLines(t *testing.T, buffer *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var matched []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buffer.Bytes()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := map[string]any{}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("log line is not JSON: %v (%s)", err, scanner.Text())
		}
		if line["msg"] == msg {
			matched = append(matched, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan log: %v", err)
	}
	return matched
}

func appliedOrgs(backend *fakeBackend) map[string]int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	counts := map[string]int{}
	for _, batch := range backend.applied {
		counts[batch.OrgID] += len(batch.Entities)
	}
	return counts
}

// TestOrgDiscovery_BuildsANewlyDiscoveredOrgWhileAnExistingOneAdvances is
// the core claim. It asserts the STATE the feature exists to reach -- a
// newly appearing organization's entities actually reach the graph backend
// -- not merely that a refresh ran.
//
// The second half is the part that makes it safe: org-a's checkpoint must
// keep advancing from the value it already held, not restart at the zero
// cursor. A refresh that rebuilt the effective set by replacing it (rather
// than unioning into it) would still pass the "org-b got built" half while
// silently re-projecting every existing tenant from scratch every tick.
func TestOrgDiscovery_BuildsANewlyDiscoveredOrgWhileAnExistingOneAdvances(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	orgs := &fakeOrgSource{results: []fakeOrgResult{
		{orgs: []string{"org-a"}},
		{orgs: []string{"org-a", "org-b"}},
	}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		// No static allowlist at all: the whole org set comes from discovery.
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		// One RunOnce attempt per tick, so cursor values are exact rather
		// than "however far the drain got".
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	coordinator.Tick(ctx)
	firstTickApplied := appliedOrgs(backend)
	if firstTickApplied["org-a"] == 0 {
		t.Fatalf("tick 1 must have projected org-a's entities into the backend, applied=%v", firstTickApplied)
	}
	if firstTickApplied["org-b"] != 0 {
		t.Fatalf("org-b was not discoverable yet on tick 1, but entities were applied for it: %v", firstTickApplied)
	}
	orgACheckpointAfterTick1, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-a", "source-a")
	if err != nil {
		t.Fatalf("load org-a checkpoint: %v", err)
	}
	if orgACheckpointAfterTick1.Cursor == "" {
		t.Fatal("org-a's checkpoint must have advanced on tick 1")
	}

	coordinator.Tick(ctx)
	secondTickApplied := appliedOrgs(backend)
	if secondTickApplied["org-b"] == 0 {
		t.Fatalf("tick 2 must have projected the newly discovered org-b's entities, applied=%v", secondTickApplied)
	}

	// org-b is brand new, so it has no checkpoint: the existing zero-cursor
	// full-build path is what must have served it. Proven by the cursor it
	// reached being a FIRST cursor ("" -> "n" under fakeSource's rule), not
	// by assuming.
	orgBCheckpoint, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-b", "source-a")
	if err != nil {
		t.Fatalf("load org-b checkpoint: %v", err)
	}
	if orgBCheckpoint.Cursor != "n" {
		t.Fatalf("a newly discovered organization must build from the zero cursor; got %q, want %q", orgBCheckpoint.Cursor, "n")
	}

	// And org-a continued INCREMENTALLY from where it already was.
	orgACheckpointAfterTick2, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-a", "source-a")
	if err != nil {
		t.Fatalf("load org-a checkpoint: %v", err)
	}
	if orgACheckpointAfterTick2.Cursor != orgACheckpointAfterTick1.Cursor+"n" {
		t.Fatalf("org-a must advance from its prior watermark, not reset: after tick 1 %q, after tick 2 %q",
			orgACheckpointAfterTick1.Cursor, orgACheckpointAfterTick2.Cursor)
	}

	lines := logLines(t, buffer, "context_fabric: projection organization discovery")
	if len(lines) != 2 {
		t.Fatalf("expected one discovery line per tick, got %d", len(lines))
	}
	if got := lines[0]["orgs_new"]; got != float64(1) {
		t.Fatalf("tick 1 discovered org-a for the first time; orgs_new = %v, want 1", got)
	}
	if got := lines[1]["orgs_new"]; got != float64(1) {
		t.Fatalf("tick 2 discovered org-b for the first time; orgs_new = %v, want 1", got)
	}
	for i, line := range lines {
		if got := line["org_discovery_outcome"]; got != "succeeded" {
			t.Fatalf("line %d: org_discovery_outcome = %v, want succeeded", i, got)
		}
		if got := line["orgs_static"]; got != float64(0) {
			t.Fatalf("line %d: orgs_static = %v, want 0", i, got)
		}
	}
	if got := lines[1]["orgs_effective"]; got != float64(2) {
		t.Fatalf("tick 2: orgs_effective = %v, want 2", got)
	}
	if got := lines[1]["orgs_discovered"]; got != float64(2) {
		t.Fatalf("tick 2: orgs_discovered = %v, want 2", got)
	}

	// The tick summary's own bucket identity must cover the discovered set,
	// or a discovered organization is projected but unaccounted for.
	summaries := logLines(t, buffer, "context_fabric: projection tick freshness summary")
	if len(summaries) != 2 {
		t.Fatalf("expected two tick summaries, got %d", len(summaries))
	}
	if got := summaries[1]["orgs_configured"]; got != float64(2) {
		t.Fatalf("tick 2 summary: orgs_configured = %v, want 2", got)
	}
}

// TestOrgDiscovery_AFailedEnumerationKeepsTheLastKnownSet: the failure that
// would be invisible. A projector whose discovery read fails and which then
// serves the empty set looks exactly like a healthy idle projector -- same
// tick cadence, same clean summary, zero organizations. The last-known set
// is retained and the failure is announced with its own outcome.
func TestOrgDiscovery_AFailedEnumerationKeepsTheLastKnownSet(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	enumerationFailure := errors.New("clickhouse unavailable")
	orgs := &fakeOrgSource{results: []fakeOrgResult{
		{orgs: []string{"org-a"}},
		{orgs: []string{"org-a", "org-b"}},
		{err: enumerationFailure},
	}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	coordinator.Tick(ctx)
	coordinator.Tick(ctx)

	orgACursorBeforeFailure, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-a", "source-a")
	if err != nil {
		t.Fatalf("load org-a checkpoint: %v", err)
	}
	orgBCursorBeforeFailure, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-b", "source-a")
	if err != nil {
		t.Fatalf("load org-b checkpoint: %v", err)
	}

	coordinator.Tick(ctx) // enumeration fails

	// Both organizations still ticked: the set did not shrink.
	orgACursorAfter, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-a", "source-a")
	if err != nil {
		t.Fatalf("load org-a checkpoint: %v", err)
	}
	orgBCursorAfter, err := checkpoints.LoadProjectionCheckpoint(ctx, "org-b", "source-a")
	if err != nil {
		t.Fatalf("load org-b checkpoint: %v", err)
	}
	if orgACursorAfter.Cursor != orgACursorBeforeFailure.Cursor+"n" {
		t.Fatalf("org-a must keep ticking through a discovery failure: %q -> %q", orgACursorBeforeFailure.Cursor, orgACursorAfter.Cursor)
	}
	if orgBCursorAfter.Cursor != orgBCursorBeforeFailure.Cursor+"n" {
		t.Fatalf("org-b must keep ticking through a discovery failure: %q -> %q", orgBCursorBeforeFailure.Cursor, orgBCursorAfter.Cursor)
	}

	failures := logLines(t, buffer, "context_fabric: projection organization discovery failed")
	if len(failures) != 1 {
		t.Fatalf("expected exactly one discovery-failure line, got %d", len(failures))
	}
	failure := failures[0]
	if got := failure["org_discovery_outcome"]; got != "failed" {
		t.Fatalf("org_discovery_outcome = %v, want failed", got)
	}
	if got := failure["orgs_effective"]; got != float64(2) {
		t.Fatalf("a failed enumeration must retain the last-known set: orgs_effective = %v, want 2", got)
	}
	if got := failure["orgs_discovered"]; got != float64(2) {
		t.Fatalf("a failed enumeration must retain the last-known discovered set: orgs_discovered = %v, want 2", got)
	}
	if got := failure["orgs_new"]; got != float64(0) {
		t.Fatalf("a failed enumeration adds nothing: orgs_new = %v, want 0", got)
	}
	if failure["level"] != "WARN" {
		t.Fatalf("a discovery failure must be loud: level = %v, want WARN", failure["level"])
	}
	// A failure must never masquerade as a success line.
	if success := logLines(t, buffer, "context_fabric: projection organization discovery"); len(success) != 2 {
		t.Fatalf("expected two SUCCESS discovery lines across three ticks, got %d", len(success))
	}
}

// TestOrgDiscovery_FirstEverFailureFallsBackToTheStaticAllowlist: the other
// failure case, which has a DIFFERENT consequence and therefore its own
// outcome token -- there is no last-known set to retain, so the static
// allowlist is all the projector has. Reporting both as "failed" would make
// "we are serving a retained set" and "we are serving nothing but the
// static list" the same line.
func TestOrgDiscovery_FirstEverFailureFallsBackToTheStaticAllowlist(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	orgs := &fakeOrgSource{results: []fakeOrgResult{{err: errors.New("clickhouse unavailable")}}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:    []string{"org-static"},
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	if applied := appliedOrgs(backend); applied["org-static"] == 0 {
		t.Fatalf("the static allowlist must still be projected when discovery fails outright: %v", applied)
	}
	failures := logLines(t, buffer, "context_fabric: projection organization discovery failed")
	if len(failures) != 1 {
		t.Fatalf("expected one failure line, got %d", len(failures))
	}
	if got := failures[0]["org_discovery_outcome"]; got != "failed_no_prior_set" {
		t.Fatalf("org_discovery_outcome = %v, want failed_no_prior_set", got)
	}
	if got := failures[0]["orgs_effective"]; got != float64(1) {
		t.Fatalf("orgs_effective = %v, want 1 (the static allowlist alone)", got)
	}
}

// TestOrgDiscovery_UnionsWithTheStaticAllowlistAndDeduplicates covers the
// input domain of the set merge itself: an overlap between the two sources,
// a duplicate within the discovered set, a padded id, and a blank id. A
// blank reaching the effective set would surface downstream as an
// unattributable pair failure (see NewCoordinator's own OrgIDs check), and
// a duplicate would double-schedule an organization within one tick.
func TestOrgDiscovery_UnionsWithTheStaticAllowlistAndDeduplicates(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	checkpoints := newFakeCheckpointStore()
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{
		"org-shared", // also in the static allowlist
		"org-only-discovered",
		"org-only-discovered", // duplicate within one enumeration
		"  org-padded  ",      // whitespace-padded
		"   ",                 // blank
		"",                    // empty
	}}}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:    []string{"org-shared", "org-only-static"},
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   backend, Checkpoints: checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	applied := appliedOrgs(backend)
	projected := make([]string, 0, len(applied))
	for orgID, entities := range applied {
		if entities == 0 {
			t.Fatalf("organization %q was scheduled but projected no entities", orgID)
		}
		projected = append(projected, orgID)
	}
	sort.Strings(projected)
	want := []string{"org-only-discovered", "org-only-static", "org-padded", "org-shared"}
	if len(projected) != len(want) {
		t.Fatalf("projected organizations = %v, want %v", projected, want)
	}
	for i := range want {
		if projected[i] != want[i] {
			t.Fatalf("projected organizations = %v, want %v", projected, want)
		}
	}

	// org-shared appears in BOTH inputs and must be ticked exactly once --
	// one batch of one entity under DrainBatchBudget: -1.
	if applied["org-shared"] != 1 {
		t.Fatalf("an organization in both the static allowlist and the discovered set must be scheduled once; applied %d entities", applied["org-shared"])
	}

	line := logLines(t, buffer, "context_fabric: projection organization discovery")
	if len(line) != 1 {
		t.Fatalf("expected one discovery line, got %d", len(line))
	}
	if got := line[0]["orgs_static"]; got != float64(2) {
		t.Fatalf("orgs_static = %v, want 2", got)
	}
	if got := line[0]["orgs_discovered"]; got != float64(3) {
		t.Fatalf("orgs_discovered = %v, want 3 (deduped, trimmed, blanks dropped)", got)
	}
	if got := line[0]["orgs_effective"]; got != float64(4) {
		t.Fatalf("orgs_effective = %v, want 4", got)
	}
}

// TestOrgDiscovery_RebuildAdmitsADiscoveredOrganizationOnlyAfterARefresh
// pins the CLI path. `acr-projector rebuild --org` is a separate short-lived
// process with no Tick, so without RefreshOrgs it decides admission against
// the static allowlist alone -- and would refuse exactly the organizations
// auto-discovery exists to pick up.
func TestOrgDiscovery_RebuildAdmitsADiscoveredOrganizationOnlyAfterARefresh(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{"org-discovered"}}}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   backend, Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	// Before any refresh, the effective set is the (empty) static allowlist.
	if err := coordinator.Rebuild(ctx, "org-discovered"); err == nil {
		t.Fatal("expected rebuild to be refused before discovery has ever run")
	}
	if err := coordinator.RefreshOrgs(ctx); err != nil {
		t.Fatalf("refresh orgs: %v", err)
	}
	if err := coordinator.Rebuild(ctx, "org-discovered"); err != nil {
		t.Fatalf("rebuild after refresh: %v", err)
	}
	if !backend.purged["org-discovered"] {
		t.Fatal("rebuild must have purged the discovered organization's graph state")
	}
	// An organization NOT in either set stays refused, refresh or not: the
	// admission check is still a check.
	if err := coordinator.Rebuild(ctx, "org-never-seen"); err == nil {
		t.Fatal("expected rebuild to be refused for an organization in neither the static allowlist nor the discovered set")
	}
}

// TestOrgDiscovery_RefreshOrgsIsANoOpWithoutAnOrgSource: the default path.
// A deployment that has not opted in must behave exactly as it did before
// CHAOS-6182 -- no enumeration, no discovery line, and the CLI's
// unconditional RefreshOrgs call must be free.
func TestOrgDiscovery_RefreshOrgsIsANoOpWithoutAnOrgSource(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{"org-1"},
		Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend: backend, Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	if err := coordinator.RefreshOrgs(ctx); err != nil {
		t.Fatalf("refresh orgs without a source must be a no-op, got %v", err)
	}
	coordinator.Tick(ctx)

	if applied := appliedOrgs(backend); applied["org-1"] == 0 {
		t.Fatalf("the static allowlist must still be projected: %v", applied)
	}
	if lines := logLines(t, buffer, "context_fabric: projection organization discovery"); len(lines) != 0 {
		t.Fatalf("a deployment without an OrgSource must emit no discovery line, got %d", len(lines))
	}
	if lines := logLines(t, buffer, "context_fabric: projection organization discovery failed"); len(lines) != 0 {
		t.Fatalf("a deployment without an OrgSource must emit no discovery-failure line, got %d", len(lines))
	}
}

// TestOrgDiscovery_RefreshesOncePerTick guards the cost of the feature: the
// enumeration runs at the start of each tick, not per organization or per
// (org, source) pair, which is what makes a bounded DISTINCT read
// affordable at a 15s cadence.
func TestOrgDiscovery_RefreshesOncePerTick(t *testing.T) {
	t.Parallel()
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{"org-a", "org-b", "org-c"}}}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources: []projectionrun.SourcePair{
			{Name: "source-a", Source: &fakeSource{name: "source-a"}},
			{Name: "source-b", Source: &fakeSource{name: "source-b"}},
		},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	coordinator.Tick(context.Background())
	if got := orgs.callCount(); got != 2 {
		t.Fatalf("ListOrgs must run once per tick regardless of organization or source count; got %d calls across 2 ticks", got)
	}
}

// TestOrgDiscoveryOutcomeVocabularyIsClosed pins the closed vocabulary the
// org_discovery_outcome field may carry, from the producer's own exported
// list rather than a second copy.
func TestOrgDiscoveryOutcomeVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	vocabulary := projectionrun.OrgDiscoveryOutcomeVocabulary()
	want := map[string]bool{"succeeded": true, "failed": true, "failed_no_prior_set": true}
	if len(vocabulary) != len(want) {
		t.Fatalf("vocabulary = %v, want %d members", vocabulary, len(want))
	}
	for _, member := range vocabulary {
		if !want[member] {
			t.Fatalf("unexpected org_discovery_outcome member %q", member)
		}
		delete(want, member)
	}
	if len(want) != 0 {
		t.Fatalf("missing org_discovery_outcome members: %v", want)
	}
}

// TestOrgDiscovery_ConcurrentReadsAndRefreshesStayRaceFree drives the
// readiness-probe reader (LivenessCheck) and the tick-side writer
// (refreshOrgs) against each other. Meaningful only under -race, which is
// how this package's suite runs in CI.
func TestOrgDiscovery_ConcurrentReadsAndRefreshesStayRaceFree(t *testing.T) {
	t.Parallel()
	orgs := &fakeOrgSource{results: []fakeOrgResult{
		{orgs: []string{"org-a"}},
		{orgs: []string{"org-a", "org-b"}},
		{orgs: []string{"org-b", "org-c"}},
	}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:    []string{"org-static"},
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Concurrency: 4, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			coordinator.Tick(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := coordinator.LivenessCheck(ctx); err != nil {
				t.Errorf("liveness check: %v", err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = coordinator.RefreshOrgs(ctx)
		}()
	}
	wg.Wait()
}

// TestOrgDiscovery_DenyListExcludesADiscoveredOrganization is the
// operator's explicit exclusion. It filters the DISCOVERED set only, and
// the excluded organization is named with its reason -- an organization
// that silently stops being projected after a config change is the shape
// nobody can debug.
func TestOrgDiscovery_DenyListExcludesADiscoveredOrganization(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{"org-keep", "org-deny"}}}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource:        orgs,
		OrgDiscoveryDeny: []string{" org-deny ", "", "org-never-discovered"},
		Sources:          []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:          backend, Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	applied := appliedOrgs(backend)
	if applied["org-keep"] == 0 {
		t.Fatalf("org-keep must be projected: %v", applied)
	}
	if applied["org-deny"] != 0 {
		t.Fatalf("a denied organization must never be projected: %v", applied)
	}
	// A denied organization must not be admissible through the CLI lever
	// either, or the deny list would be advisory.
	if err := coordinator.Rebuild(context.Background(), "org-deny"); err == nil {
		t.Fatal("expected rebuild to be refused for a denied organization")
	}

	line := logLines(t, buffer, "context_fabric: projection organization discovery")[0]
	if got := line["orgs_skipped_denied"]; got != float64(1) {
		t.Fatalf("orgs_skipped_denied = %v, want 1", got)
	}
	if got := line["orgs_effective"]; got != float64(1) {
		t.Fatalf("orgs_effective = %v, want 1", got)
	}
	skips := logLines(t, buffer, "context_fabric: projection organization skipped")
	if len(skips) != 1 {
		t.Fatalf("expected exactly one skip line, got %d", len(skips))
	}
	if skips[0]["org_skip_reason"] != "denied" || skips[0]["org_id"] != "org-deny" {
		t.Fatalf("skip line = %v, want org-deny/denied", skips[0])
	}
}

// TestOrgDiscovery_TheStaticAllowlistIsNeverFilteredByTheDenyList: OrgIDs
// is an always-include list. An operator who both named an organization and
// denied it has contradicted themselves, and the explicit inclusion is the
// more specific instruction -- silently honouring the deny instead would
// leave a hand-pinned tenant unprojected with no line saying why.
func TestOrgDiscovery_TheStaticAllowlistIsNeverFilteredByTheDenyList(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{"org-static"}}}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:           []string{"org-static"},
		OrgSource:        orgs,
		OrgDiscoveryDeny: []string{"org-static"},
		Sources:          []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:          backend, Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())
	if applied := appliedOrgs(backend); applied["org-static"] == 0 {
		t.Fatalf("an explicitly configured organization must be projected regardless of the deny list: %v", applied)
	}
	if err := coordinator.Rebuild(context.Background(), "org-static"); err != nil {
		t.Fatalf("an explicitly configured organization must stay rebuildable: %v", err)
	}
}

// TestOrgDiscovery_AdapterSkipsArePublishedWithTheirReasons: the adapter's
// own eligibility decisions (no_repo, inactive) reach the log through the
// coordinator, per reason and per organization. Without this, "the trial
// has 88 orgs and only 4 are projected" is an unexplained number.
func TestOrgDiscovery_AdapterSkipsArePublishedWithTheirReasons(t *testing.T) {
	t.Parallel()
	orgs := &fakeOrgSource{results: []fakeOrgResult{{
		orgs: []string{"org-ok"},
		skipped: []contextfabric.SkippedOrg{
			{OrgID: "org-junk-1", Reason: contextfabric.OrgSkipReasonInactive},
			{OrgID: "org-junk-2", Reason: contextfabric.OrgSkipReasonInactive},
			{OrgID: "org-orphan", Reason: contextfabric.OrgSkipReasonNoRepo},
		},
	}}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	line := logLines(t, buffer, "context_fabric: projection organization discovery")[0]
	for field, want := range map[string]float64{
		"orgs_skipped":          3,
		"orgs_skipped_inactive": 2,
		"orgs_skipped_no_repo":  1,
		"orgs_skipped_denied":   0, // present at zero, on every refresh
		"orgs_effective":        1,
	} {
		if got := line[field]; got != want {
			t.Fatalf("%s = %v, want %v", field, got, want)
		}
	}
	if line["skipped_truncated"] != false {
		t.Fatalf("skipped_truncated = %v, want false", line["skipped_truncated"])
	}
	skips := logLines(t, buffer, "context_fabric: projection organization skipped")
	if len(skips) != 3 {
		t.Fatalf("every skipped organization must be named while under the cap; got %d", len(skips))
	}
}

// TestOrgDiscovery_SkipLoggingIsCappedButCountsAreNot is the log-volume
// bound. A shared environment with scores of throwaway tenants must not
// emit one line per tenant every 15 seconds forever -- but the COUNTS stay
// complete, and the line says it truncated, so a capped run is never
// mistaken for a smaller problem.
func TestOrgDiscovery_SkipLoggingIsCappedButCountsAreNot(t *testing.T) {
	t.Parallel()
	const junk = 88
	skipped := make([]contextfabric.SkippedOrg, 0, junk)
	for i := 0; i < junk; i++ {
		skipped = append(skipped, contextfabric.SkippedOrg{
			OrgID: fmt.Sprintf("org-junk-%02d", i), Reason: contextfabric.OrgSkipReasonInactive,
		})
	}
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{"org-ok"}, skipped: skipped}}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	coordinator.Tick(context.Background())

	line := logLines(t, buffer, "context_fabric: projection organization discovery")[0]
	if got := line["orgs_skipped_inactive"]; got != float64(junk) {
		t.Fatalf("the per-reason count must be complete regardless of the log cap: got %v, want %d", got, junk)
	}
	if line["skipped_truncated"] != true {
		t.Fatalf("skipped_truncated = %v, want true", line["skipped_truncated"])
	}
	skips := logLines(t, buffer, "context_fabric: projection organization skipped")
	if len(skips) != 20 {
		t.Fatalf("individual skip lines = %d, want the documented cap of 20", len(skips))
	}
}

// TestOrgSkipReasonVocabularyIsClosed pins the skip vocabulary from the
// producer's own exported list rather than a second copy.
func TestOrgSkipReasonVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	want := map[contextfabric.OrgSkipReason]bool{"no_repo": true, "inactive": true, "denied": true}
	vocabulary := contextfabric.OrgSkipReasonVocabulary()
	if len(vocabulary) != len(want) {
		t.Fatalf("vocabulary = %v, want %d members", vocabulary, len(want))
	}
	for _, member := range vocabulary {
		if !want[member] {
			t.Fatalf("unexpected org_skip_reason member %q", member)
		}
		delete(want, member)
	}
	if len(want) != 0 {
		t.Fatalf("missing org_skip_reason members: %v", want)
	}
}

// blockingOrgSource blocks inside its FIRST ListOrgs until released, and
// records the greatest number of ListOrgs calls ever in flight at once.
// Both are needed to state the refresh-ordering property: the enumeration
// and the state it produces must commit as one step.
type blockingOrgSource struct {
	entered  chan struct{} // closed when the FIRST enumeration begins
	release  chan struct{} // closed by the test to let the first enumeration return
	second   chan struct{} // closed when a SECOND enumeration begins
	firstErr error

	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
}

func newBlockingOrgSource(firstErr error) *blockingOrgSource {
	return &blockingOrgSource{
		entered: make(chan struct{}), release: make(chan struct{}),
		second: make(chan struct{}), firstErr: firstErr,
	}
}

func (s *blockingOrgSource) ListOrgs(context.Context) (contextfabric.OrgDiscoveryResult, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()

	if call == 1 {
		close(s.entered)
		<-s.release
		return contextfabric.OrgDiscoveryResult{}, s.firstErr
	}
	if call == 2 {
		close(s.second)
	}
	return contextfabric.OrgDiscoveryResult{OrgIDs: []string{"org-new"}}, nil
}

// awaitNoSecondEnumeration waits for a second enumeration to BEGIN while
// the first is still blocked, and fails if one does.
//
// The wait is generous and one-sided on purpose. Under the correct
// implementation the signal can never arrive, so the test always waits the
// full budget and then proceeds -- there is no way for it to fail
// spuriously. Under an implementation that enumerates outside the refresh
// lock, the second goroutine only has to be scheduled once for the signal
// to arrive, which a second is many orders of magnitude more than enough
// for. Simply reading a counter right after starting the goroutine does
// NOT discriminate: the goroutine has usually not run yet, so the counter
// reads 1 either way and the test passes against the defect it exists to
// catch.
func (s *blockingOrgSource) awaitNoSecondEnumeration(t *testing.T) {
	t.Helper()
	select {
	case <-s.second:
		t.Fatal("a second refresh enumerated while the first was still in flight: the enumeration and its commit are not one step, so an older read can land last and overwrite a newer one")
	case <-time.After(time.Second):
	}
}

func (s *blockingOrgSource) counts() (calls, maxInFlight int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.maxInFlight
}

// TestOrgDiscovery_ARefreshEnumeratesAndCommitsAsOneStep is the
// out-of-order-commit regression. With the enumeration outside the lock,
// two concurrent refreshes commit in COMPLETION order rather than start
// order: a slow older read lands last and overwrites the newer one, so a
// just-discovered organization silently disappears again -- and because
// the older read here returns nothing, the effective set collapses to the
// (empty) static allowlist while the line still says outcome=succeeded.
//
// The property that makes that impossible is stronger than "the writes are
// locked": the READ and the write are one step, so the last commit is also
// the last read. That is what this asserts -- the second refresh's
// enumeration must not even begin until the first has committed, which is
// why the call count, not just the final set, is checked.
func TestOrgDiscovery_ARefreshEnumeratesAndCommitsAsOneStep(t *testing.T) {
	t.Parallel()
	source := newBlockingOrgSource(nil)
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: source,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	first := make(chan error, 1)
	go func() { first <- coordinator.RefreshOrgs(ctx) }()
	<-source.entered // the first refresh is inside ListOrgs, holding the refresh

	second := make(chan error, 1)
	go func() { second <- coordinator.RefreshOrgs(ctx) }()

	// The second refresh must be QUEUED, not racing: while the first is
	// still enumerating, the source must not be called again.
	source.awaitNoSecondEnumeration(t)

	close(source.release)
	if err := <-first; err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	calls, maxInFlight := source.counts()
	if maxInFlight != 1 {
		t.Fatalf("refreshes must never enumerate concurrently: max in flight = %d", maxInFlight)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	// The later enumeration's result is what stands.
	if err := coordinator.Rebuild(ctx, "org-new"); err != nil {
		t.Fatalf("the newer enumeration's organization must survive: %v", err)
	}
}

// TestOrgDiscovery_AFailedRefreshCannotOverwriteALaterSuccess is the same
// hazard with the two commits' outcomes swapped: the in-flight older
// refresh FAILS. Its failure must not retroactively discard the newer
// successful discovery -- a transient ClickHouse error that started before
// a successful read must not un-discover what that read found.
func TestOrgDiscovery_AFailedRefreshCannotOverwriteALaterSuccess(t *testing.T) {
	t.Parallel()
	source := newBlockingOrgSource(errors.New("clickhouse unavailable"))
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: source,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	first := make(chan error, 1)
	go func() { first <- coordinator.RefreshOrgs(ctx) }()
	<-source.entered
	second := make(chan error, 1)
	go func() { second <- coordinator.RefreshOrgs(ctx) }()
	source.awaitNoSecondEnumeration(t)
	close(source.release)

	if err := <-first; err == nil {
		t.Fatal("the first refresh was scripted to fail")
	}
	if err := <-second; err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if err := coordinator.Rebuild(ctx, "org-new"); err != nil {
		t.Fatalf("a successful refresh must survive an earlier in-flight failure: %v", err)
	}
}

// TestOrgDiscovery_SkipLoggingRotatesSoEveryOrganizationIsEventuallyNamed
// is the fixed-window regression. Capping the detail lines is right;
// always naming the FIRST 20 is not -- with 88 excluded tenants the same
// 20 are named every tick forever and the other 68 never receive a
// decision record at all, on any tick, at any level. That is not a volume
// bound, it is a permanent blind spot, and it is invisible precisely
// because the log looks busy.
//
// The bar is coverage within ceil(len/cap) refreshes at an UNCHANGED
// per-refresh volume, so the fix cannot be "log more".
func TestOrgDiscovery_SkipLoggingRotatesSoEveryOrganizationIsEventuallyNamed(t *testing.T) {
	t.Parallel()
	const junk = 88
	const cap = 20
	skipped := make([]contextfabric.SkippedOrg, 0, junk)
	for i := 0; i < junk; i++ {
		skipped = append(skipped, contextfabric.SkippedOrg{
			OrgID: fmt.Sprintf("org-junk-%02d", i), Reason: contextfabric.OrgSkipReasonInactive,
		})
	}
	orgs := &fakeOrgSource{results: []fakeOrgResult{{orgs: []string{"org-ok"}, skipped: skipped}}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()

	ticksToCover := (junk + cap - 1) / cap // 5
	for tick := 1; tick <= ticksToCover; tick++ {
		if err := coordinator.RefreshOrgs(ctx); err != nil {
			t.Fatalf("refresh %d: %v", tick, err)
		}
		lines := logLines(t, buffer, "context_fabric: projection organization skipped")
		// The per-refresh volume must stay capped, or "rotation" would just
		// be a louder log.
		if len(lines) != tick*cap {
			t.Fatalf("after %d refreshes: detail lines = %d, want exactly %d (%d per refresh)", tick, len(lines), tick*cap, cap)
		}
	}

	named := map[string]bool{}
	for _, line := range logLines(t, buffer, "context_fabric: projection organization skipped") {
		named[line["org_id"].(string)] = true
	}
	if len(named) != junk {
		missing := make([]string, 0, junk)
		for _, skip := range skipped {
			if !named[skip.OrgID] {
				missing = append(missing, skip.OrgID)
			}
		}
		t.Fatalf("after %d refreshes only %d of %d excluded organizations were ever named; never named: %v",
			ticksToCover, len(named), junk, missing)
	}
}

// TestOrgDiscovery_FirstEverFailureWithNoStaticAllowlist is the domain cell
// the sibling first-failure test does not reach, and it is the RECOMMENDED
// production shape: discovery on, no static allowlist at all. The outcome
// must still be failed_no_prior_set (nothing is being retained), and the
// effective set is legitimately empty -- which must not be reported as a
// success.
func TestOrgDiscovery_FirstEverFailureWithNoStaticAllowlist(t *testing.T) {
	t.Parallel()
	orgs := &fakeOrgSource{results: []fakeOrgResult{
		{err: errors.New("clickhouse unavailable")},
		{orgs: []string{"org-a"}},
	}}
	logger, buffer := capturingLogger()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgSource: orgs,
		Sources:   []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a"}}},
		Backend:   newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(),
		DrainBatchBudget: -1, Logger: logger,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	ctx := context.Background()
	coordinator.Tick(ctx)

	failures := logLines(t, buffer, "context_fabric: projection organization discovery failed")
	if len(failures) != 1 {
		t.Fatalf("expected one failure line, got %d", len(failures))
	}
	if got := failures[0]["org_discovery_outcome"]; got != "failed_no_prior_set" {
		t.Fatalf("org_discovery_outcome = %v, want failed_no_prior_set -- there is no prior set to retain", got)
	}
	if got := failures[0]["orgs_effective"]; got != float64(0) {
		t.Fatalf("orgs_effective = %v, want 0", got)
	}
	if got := failures[0]["orgs_new"]; got != float64(0) {
		t.Fatalf("orgs_new = %v, want 0", got)
	}

	// And the failure is not sticky: the next enumeration succeeds and the
	// organization is projected.
	coordinator.Tick(ctx)
	if err := coordinator.Rebuild(ctx, "org-a"); err != nil {
		t.Fatalf("discovery must recover on the next tick: %v", err)
	}
}
