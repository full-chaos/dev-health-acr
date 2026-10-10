package devhealthsource_test

import (
	"bytes"
	"context"
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
