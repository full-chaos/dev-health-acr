package devhealthsource_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// An organization with more repositories than the from-zero read asks one
// table for: 200 repositories, 198 of them re-stamped by the sync before every
// tick, and 90 days of work items that name only the first. Every repository
// must have a node after the first tick of the build.
func TestEveryRepositoryOfALargeOrganizationHasANodeEarlyInAFromZeroBuild(t *testing.T) {
	t.Parallel()
	const orgID, repositories = "org-1", 200
	start := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	now := start
	repoID := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
	var repos [][]any
	for n := 1; n <= repositories; n++ {
		at := start.Add(-90 * 24 * time.Hour).Add(time.Duration(n) * time.Second)
		id := repoID(n)
		repos = append(repos, []any{id, "acme/repo-" + id[30:], "github", at, at, ""})
	}
	restamp := func() {
		for n := 3; n <= repositories; n++ {
			at := now.Add(-5 * time.Minute).Add(time.Duration(n) * 400 * time.Millisecond)
			repos[n-1][3], repos[n-1][4] = at, at
		}
	}
	var items [][]any
	for n := 0; n < 2000; n++ {
		at := start.Add(-90 * 24 * time.Hour).Add(time.Duration(n) * ((90*24*time.Hour - 2*time.Hour) / 2000))
		items = append(items, []any{fmt.Sprintf("WI-%04d", n), repoID(1), "acme/repo-" + repoID(1)[30:], fmt.Sprintf("item %d", n), "open", "", at, at, uint8(0), zeroTime, "bug", "ACME", "Acme", []string{}})
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: repos, cursorOf: repoCursorOf},
		{match: "FROM work_items AS w", rows: items, cursorOf: workItemCursorOf},
	}}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatal(err)
	}
	source.SetClockForTest(func() time.Time { return now })
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	source.WithLogger(logger)
	backend := &catchUpBackend{repository: map[string]int{}, watermarks: map[string]contextfabric.ProjectionWatermark{}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{orgID},
		Sources: []projectionrun.SourcePair{{Name: devhealthsource.SourceName, Source: source}},
		Backend: backend, Checkpoints: &summaryCheckpoints{data: map[string]contextfabric.ProjectionCheckpoint{}}, RebuildMarkers: summaryRebuildMarker{},
		DrainBatchBudget: 1, Logger: logger, Observer: projectionrun.SlogObserver{Logger: logger},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	afterTick := map[int]int{}
	ticks := 0
	for tick := 1; tick <= 40; tick++ {
		if tick > 1 {
			now = now.Add(time.Hour)
		}
		restamp()
		backend.tick = tick
		logs.Reset()
		coordinator.Tick(context.Background())
		ticks = tick
		afterTick[tick] = len(backend.repository)
		t.Logf("tick %2d: repositories with a node %d/%d, batches so far %d", tick, len(backend.repository), repositories, backend.batches)
		if !strings.Contains(logs.String(), `"drain_yield_reason":"budget_exceeded"`) {
			break
		}
	}
	if ticks < 4 {
		t.Fatalf("precondition: the build took %d ticks; it must span several", ticks)
	}
	if afterTick[1] != repositories {
		firstTick := map[int]int{}
		for _, tick := range backend.repository {
			firstTick[tick]++
		}
		t.Fatalf("after tick 1 of %d: %d of %d repositories have a node (after tick 2: %d); first node per tick: %v; repository 151 on tick %d, repository 200 on tick %d",
			ticks, afterTick[1], repositories, afterTick[2], firstTick, backend.repository[repoID(151)], backend.repository[repoID(200)])
	}
}

// The teams/projects source runs the same phase: 200 teams and 200 projects,
// both over the from-zero read's cap. Every team and every project is emitted
// before the fact position moves, and the dimension batches do not share an
// id.
func TestTeamsAndProjectsOfALargeOrganizationAreEmittedBeforeTheFactWalk(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 10, 3, 55, 0, 0, time.UTC)
	var teams, projects [][]any
	for n := 0; n < 200; n++ {
		stamp := at.Add(time.Duration(n) * time.Millisecond)
		teams = append(teams, teamRow(fmt.Sprintf("gh:team-%03d", n), fmt.Sprintf("Team %d", n), "", "github", fmt.Sprintf("team-%03d", n), 1, stamp, nil, nil))
		projects = append(projects, projectRow(fmt.Sprintf("00000000-0000-4000-9000-%012d", n), fmt.Sprintf("Project %d", n), "", "linear", "backlog", fmt.Sprintf("https://linear.app/acme/project/p-%012d", n), 1, stamp))
	}
	stampOf := func(column int) func([]any) (time.Time, string) {
		return func(row []any) (time.Time, string) { return row[column].(time.Time), row[0].(string) }
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM teams AS tm FINAL", rows: teams, cursorOf: stampOf(6)},
		{match: "FROM projects FINAL\nWHERE", rows: projects, cursorOf: stampOf(7)},
	}}
	source := enabledTeamsProjectsSource(t, client)
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}
	kinds := map[string]int{}
	ids := map[string]bool{}
	for call := 1; call <= 10; call++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		if !available {
			break
		}
		if ids[batch.BatchID] {
			t.Fatalf("call %d: batch id %s was used by an earlier batch", call, batch.BatchID)
		}
		ids[batch.BatchID] = true
		checkpoint.Cursor = batch.NextCursor
		if devhealthsource.CursorFactPositionMovedForTest(t, batch.NextCursor) {
			break
		}
		for _, e := range batch.Entities {
			kinds[string(e.Subject.Kind)]++
		}
		if !source.ProjectionCatchUp(checkpoint).WorkAhead {
			t.Fatalf("call %d: a dimension batch does not say work is left", call)
		}
	}
	if kinds["team"] != 200 || kinds["project"] != 200 {
		t.Fatalf("entities before the fact position moved: %v, want 200 teams and 200 projects", kinds)
	}
}

// refuseFirstBackend refuses its first apply (a graph write failure) and
// applies every later batch. It records the id of every batch it was handed.
type refuseFirstBackend struct {
	recordingBackend
	attempted []string
}

func (b *refuseFirstBackend) ApplyProjectionBatch(ctx context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.attempted = append(b.attempted, batch.BatchID)
	if len(b.attempted) == 1 {
		return contextfabric.ProjectionReceipt{}, errors.New("simulated graph write failure")
	}
	return b.recordingBackend.ApplyProjectionBatch(ctx, batch)
}

// dimensionPhaseRun is what a from-zero build left in the backend up to its
// first fact batch.
type dimensionPhaseRun struct {
	before           map[string]int // entities by kind, applied before the fact position moved
	firstFact        map[string]int // entities by kind in the first batch that moved it
	dimensionBatches int
	attempted        []string // batch ids in the order the backend was handed them
	applied          []string // batch ids in the order the backend applied them
}

// runDimensionPhaseThroughWorker drives the production worker over source
// from an empty checkpoint, one RunOnce per tick, until the first applied
// batch that moves the fact position. The backend refuses the first apply.
func runDimensionPhaseThroughWorker(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource, sourceName, orgID string) dimensionPhaseRun {
	t.Helper()
	checkpoints := &memoryCheckpoints{checkpoint: contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: sourceName}}
	backend := &refuseFirstBackend{}
	worker, err := contextfabric.NewProjectionWorker(source, backend, checkpoints, contextfabric.ProjectionWorkerOptions{})
	if err != nil {
		t.Fatalf("NewProjectionWorker: %v", err)
	}
	run := dimensionPhaseRun{before: map[string]int{}, firstFact: map[string]int{}}
	for tick := 0; tick < 40; tick++ {
		before := checkpoints.checkpoint.Cursor
		_, err := worker.RunOnce(ctx, orgID, sourceName)
		if tick == 0 {
			if err == nil || checkpoints.checkpoint.Cursor != before {
				t.Fatalf("tick 0: err=%v cursor moved=%v; the refused apply must fail the run and leave the cursor", err, checkpoints.checkpoint.Cursor != before)
			}
			continue
		}
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		applied := backend.applied
		backend.applied = nil
		for _, batch := range applied {
			run.applied = append(run.applied, batch.BatchID)
			into := run.before
			moved := devhealthsource.CursorFactPositionMovedForTest(t, batch.NextCursor)
			if moved {
				into = run.firstFact
			} else {
				run.dimensionBatches++
			}
			for _, e := range batch.Entities {
				into[string(e.Subject.Kind)]++
			}
			if moved {
				run.attempted = backend.attempted
				return run
			}
		}
	}
	t.Fatalf("no batch moved the fact position in 40 ticks; applied before it: %v", run.before)
	return run
}

// The production worker over the production source, with a backend that
// refuses the first apply: the refused dimension batch is handed to the
// backend again (same id) and the checkpoint did not move; every repository
// is applied before the first fact row.
func TestWorkerAppliesEveryRepositoryBeforeTheFirstFactRowAfterARefusedApply(t *testing.T) {
	t.Parallel()
	const repositories = 1100
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	repoID := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
	var repos, items [][]any
	for n := 1; n <= repositories; n++ {
		at := now.Add(-5 * time.Minute).Add(time.Duration(n) * time.Millisecond)
		repos = append(repos, []any{repoID(n), "acme/repo-" + repoID(n)[30:], "github", at, at, ""})
	}
	for n := 0; n < 600; n++ {
		at := now.Add(-90 * 24 * time.Hour).Add(time.Duration(n) * time.Hour)
		items = append(items, []any{fmt.Sprintf("WI-%04d", n), repoID(1), "acme/repo-" + repoID(1)[30:], fmt.Sprintf("item %d", n), "open", "", at, at, uint8(0), zeroTime, "bug", "ACME", "Acme", []string{}})
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: repos, cursorOf: repoCursorOf},
		{match: "FROM work_items AS w", rows: items, cursorOf: workItemCursorOf},
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := runDimensionPhaseThroughWorker(t, context.Background(), source, devhealthsource.SourceName, "org-1")
	if run.before[string(contextfabric.SubjectRepository)] != repositories || run.before[string(contextfabric.SubjectWorkItem)] != 0 {
		t.Fatalf("applied before the fact position moved: %v, want %d repositories and no work item", run.before, repositories)
	}
	if run.dimensionBatches != 2 || run.firstFact[string(contextfabric.SubjectWorkItem)] == 0 {
		t.Fatalf("%d dimension batches, first fact batch %v; want 2 and a batch of work items", run.dimensionBatches, run.firstFact)
	}
	if len(run.attempted) != len(run.applied)+1 || run.attempted[0] != run.attempted[1] || run.attempted[1] != run.applied[0] {
		t.Fatalf("attempted %v, applied %v; want the refused batch handed over again and applied first", run.attempted, run.applied)
	}
}
