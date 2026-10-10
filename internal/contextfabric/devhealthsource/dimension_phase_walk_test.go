package devhealthsource

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func dimensionTable(name string, store *liveKeysetRows) entityTable {
	table := namedRepositoryTable(name, store)
	table.dimension = true
	return table
}

// tableOf names the table a repository key of catchUpKey belongs to.
func tableOf(key string) string { return key[21:23] }

// entitiesPerTable counts a batch's repository entities by the table number in
// their key ("org" is the seed).
func entitiesPerTable(batch contextfabric.ProjectionBatch) map[string]int {
	out := map[string]int{}
	for _, e := range batch.Entities {
		if id, ok := strings.CutPrefix(e.Subject.CanonicalID, "repository:"); ok {
			out[tableOf(id)]++
		} else {
			out["org"]++
		}
	}
	return out
}

// dimensionPlan is a from-zero plan: 400 old fact rows (table 01) and one
// dimension table (02) whose rows are all stamped "now", as a sync leaves
// them. The seed is one organization entity.
func dimensionPlan(now time.Time, logs *bytes.Buffer, dimensions *liveKeysetRows) sourcePlan {
	plan := sourcePlan{
		client: keysetRows{}, source: "dimension_phase_test", version: ClickHouseSourceVersion,
		tables: []entityTable{
			namedRepositoryTable("facts", catchUpRows(1, 400, now.Add(-90*24*time.Hour), time.Hour)),
			dimensionTable("dims", dimensions),
		},
		seed: func(orgID string) []candidate {
			return []candidate{organizationCandidate(orgID, organizationAnchorTime)}
		},
		now: func() time.Time { return now }, overlap: defaultReprojectOverlap, window: newWindowMemo(),
	}
	if logs != nil {
		plan.logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	return plan
}

// nextApplied takes the next batch and moves the checkpoint as the worker does
// after a successful apply.
func nextApplied(t *testing.T, plan sourcePlan, checkpoint *contextfabric.ProjectionCheckpoint) contextfabric.ProjectionBatch {
	t.Helper()
	batch, available, err := plan.nextBatch(context.Background(), *checkpoint)
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want a batch", available, err)
	}
	if batch.Cursor != checkpoint.Cursor {
		t.Fatalf("the batch starts at a cursor that is not the checkpoint's")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("the batch is not valid: %v", err)
	}
	checkpoint.Cursor = batch.NextCursor
	return batch
}

func mustDecode(t *testing.T, cursor string) cursorState {
	t.Helper()
	state, err := decodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// 1,300 rows of a dimension table. The first batch holds the seed and four
// pages (a fifth would pass the contract's 1,000 entities); the second batch
// holds the other 500 rows; no fact row is in either. The third batch is the
// first fact page. The fact position does not move before it.
func TestDimensionTableOverTheCapIsReadWholeBeforeTheFirstFactRow(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	plan := dimensionPlan(now, &logs, catchUpRows(2, 1300, now.Add(-10*time.Minute), time.Millisecond))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	dropped := 0
	plan.dropConsumed = func(string) { dropped++ }

	first := nextApplied(t, plan, &checkpoint)
	if dropped != 1 {
		t.Fatalf("a published dimension batch dropped the consumed-progress memo %d times, want 1", dropped)
	}
	if got, want := entitiesPerTable(first), (map[string]int{"org": 1, "02": 800}); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch 1 entities = %v, want %v", got, want)
	}
	if state := mustDecode(t, first.NextCursor); !state.Since.IsZero() || state.After != "" || state.Dim == nil || state.Dim.At != 0 || state.Dim.Rows != 800 || state.Dim.Batches != 1 {
		t.Fatalf("batch 1 next cursor = %+v (dim %+v), want a zero fact position and the dimension position after 800 rows", state, state.Dim)
	}
	if len(first.NextCursor) > 512 {
		t.Fatalf("the cursor is %d characters, over the contract's 512", len(first.NextCursor))
	}
	if report := reportCatchUp(plan.window, checkpoint); !report.WorkAhead || report.CursorKnown {
		t.Fatalf("after batch 1: report = %+v, want work ahead and no fact cursor", report)
	}

	second := nextApplied(t, plan, &checkpoint)
	if got, want := entitiesPerTable(second), (map[string]int{"02": 500}); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch 2 entities = %v, want %v", got, want)
	}
	if first.BatchID == second.BatchID {
		t.Fatal("two dimension batches share one batch id")
	}
	if state := mustDecode(t, second.NextCursor); !state.Since.IsZero() || state.Dim == nil || state.Dim.At != 1 || state.Dim.Rows != 1300 || state.Dim.Batches != 2 {
		t.Fatalf("batch 2 next cursor = %+v (dim %+v), want a zero fact position and every dimension table read", state, state.Dim)
	}
	if report := reportCatchUp(plan.window, checkpoint); !report.WorkAhead {
		t.Fatalf("after the last dimension batch: report = %+v, want work ahead (the fact walk has not started)", report)
	}
	if ended := logLinesWith(t, logs.String(), dimensionEndedMessage); len(ended) != 0 {
		t.Fatalf("the phase is said to have ended before a read found the tables whole and the walk started: %v", ended)
	}

	third := nextApplied(t, plan, &checkpoint)
	if got, want := entitiesPerTable(third), (map[string]int{"01": incrementalBatchCap}); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch 3 entities = %v, want the first fact page %v", got, want)
	}
	if state := mustDecode(t, third.NextCursor); state.Since.IsZero() || state.Dim != nil {
		t.Fatalf("batch 3 next cursor = %+v, want a fact position and no dimension position", state)
	}

	batches := logLinesWith(t, logs.String(), dimensionBatchMessage)
	if len(batches) != 2 || batches[0]["phase_complete"] != false || batches[1]["phase_complete"] != true ||
		batches[0]["batch_rows"] != float64(800) || batches[1]["batch_rows"] != float64(500) || batches[1]["phase_rows"] != float64(1300) {
		t.Fatalf("dimension batch lines = %v", batches)
	}
	ended := logLinesWith(t, logs.String(), dimensionEndedMessage)
	if len(ended) != 1 || ended[0]["phase_rows"] != float64(1300) || ended[0]["phase_batches"] != float64(2) || !reflect.DeepEqual(stringsOf(ended[0]["dimension_tables"]), []string{"dims"}) {
		t.Fatalf("dimension phase end lines = %v", ended)
	}
	walk := logLinesWith(t, logs.String(), completeTablesMessage)
	if len(walk) != 1 || !reflect.DeepEqual(stringsOf(walk[0]["tables_left_truncated"]), []string{"facts"}) {
		t.Fatalf("the walk's first line = %v, want tables_left_truncated to name the fact table only", walk)
	}
}

// A restart in the middle of the phase (a new process: an empty memo) reads on
// after the last applied dimension batch and still reads every row.
func TestDimensionPhaseContinuesAfterARestart(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	store := catchUpRows(2, 1300, now.Add(-10*time.Minute), time.Millisecond)
	seen := map[string]bool{}
	note := func(batch contextfabric.ProjectionBatch) {
		for key := range entityKeys(batch) {
			seen[key] = true
		}
	}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: "dimension_phase_test"}
	note(nextApplied(t, dimensionPlan(now, nil, store), &checkpoint))

	restarted := dimensionPlan(now, nil, store)
	second := nextApplied(t, restarted, &checkpoint)
	if got, want := entitiesPerTable(second), (map[string]int{"02": 500}); !reflect.DeepEqual(got, want) {
		t.Fatalf("the batch after the restart = %v, want the 500 rows left %v", got, want)
	}
	note(second)
	for _, row := range store.rows {
		if !seen[row.key] {
			t.Fatalf("dimension row %s was not emitted by the phase", row.key)
		}
	}
	if third := nextApplied(t, restarted, &checkpoint); entitiesPerTable(third)["01"] != incrementalBatchCap {
		t.Fatalf("the batch after the phase = %v, want the first fact page", entitiesPerTable(third))
	}
}

// A dimension batch the backend did not apply leaves the checkpoint where it
// was: the next call returns the same batch, not the pages after it.
func TestDimensionBatchThatWasNotAppliedIsReadAgain(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	plan := dimensionPlan(now, nil, catchUpRows(2, 1300, now.Add(-10*time.Minute), time.Millisecond))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	nextApplied(t, plan, &checkpoint)
	held := checkpoint
	lost := nextApplied(t, plan, &held)
	again := nextApplied(t, plan, &checkpoint)
	if lost.BatchID != again.BatchID || !reflect.DeepEqual(entityKeys(lost), entityKeys(again)) || len(again.Entities) != 500 {
		t.Fatalf("the retry returned batch %s with %d entities, want batch %s again with 500", again.BatchID, len(again.Entities), lost.BatchID)
	}
}

// The phase is for a dimension table the from-zero read did not return whole.
// A dimension table at the cap goes with the first fact page; a fact table
// over the cap starts no phase. (The seed takes one row of the page.)
func TestNoDimensionPhaseWhenEveryDimensionTableCameBackWhole(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	plan := dimensionPlan(now, &logs, catchUpRows(2, snapshotPerQueryCap, now.Add(-10*time.Minute), time.Millisecond))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	first := nextApplied(t, plan, &checkpoint)
	if got, want := entitiesPerTable(first), (map[string]int{"org": 1, "01": incrementalBatchCap - 1, "02": snapshotPerQueryCap}); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch 1 entities = %v, want the first fact page with the whole dimension table %v", got, want)
	}
	if state := mustDecode(t, first.NextCursor); state.Dim != nil || state.Since.IsZero() {
		t.Fatalf("batch 1 next cursor = %+v, want a fact position and no dimension position", state)
	}
	if lines := logLinesWith(t, logs.String(), dimensionBatchMessage); len(lines) != 0 {
		t.Fatalf("a dimension phase ran: %v", lines)
	}
}

// A small organization is one full snapshot, as before.
func TestNoDimensionPhaseForAFullSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	plan := dimensionPlan(now, nil, catchUpRows(2, 20, now.Add(-10*time.Minute), time.Millisecond))
	plan.tables[0] = namedRepositoryTable("facts", catchUpRows(1, 30, now.Add(-24*time.Hour), time.Minute))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	batch := nextApplied(t, plan, &checkpoint)
	if !batch.FullSnapshot || !batch.CompleteEnumeration || len(batch.Entities) != 51 {
		t.Fatalf("full_snapshot=%v complete=%v entities=%d, want one full snapshot of 51", batch.FullSnapshot, batch.CompleteEnumeration, len(batch.Entities))
	}
}

// Three dimension tables: two over the cap, one whole. The phase reads the two
// (one after the other, in one batch here), and the whole one goes with the
// first fact page.
func TestDimensionPhaseReadsEveryTruncatedDimensionTable(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	plan := dimensionPlan(now, nil, catchUpRows(2, 300, now.Add(-10*time.Minute), time.Millisecond))
	plan.tables = append(plan.tables,
		dimensionTable("dims_whole", catchUpRows(3, 100, now.Add(-10*time.Minute), time.Millisecond)),
		dimensionTable("dims_last", catchUpRows(4, 250, now.Add(-10*time.Minute), time.Millisecond)))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	first := nextApplied(t, plan, &checkpoint)
	if got, want := entitiesPerTable(first), (map[string]int{"org": 1, "02": 300, "04": 250}); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch 1 entities = %v, want %v", got, want)
	}
	state := mustDecode(t, first.NextCursor)
	if state.Dim == nil || !slices.Equal(state.Dim.Tables, []string{"dims", "dims_last"}) || state.Dim.At != 2 {
		t.Fatalf("dimension position = %+v, want both truncated tables read", state.Dim)
	}
	second := nextApplied(t, plan, &checkpoint)
	if got, want := entitiesPerTable(second), (map[string]int{"01": incrementalBatchCap, "03": 100}); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch 2 entities = %v, want the first fact page with the whole dimension table %v", got, want)
	}
}

// A page limit ends a batch in the middle of a table; the next batch starts
// after the last row of that page, in the same table.
func TestDimensionPhaseStopsAndResumesInsideATable(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	// Every row on ONE stamp, as one sync run leaves them: the row key alone
	// orders them.
	store := catchUpRows(2, 1300, now.Add(-10*time.Minute), 0)
	plan := dimensionPlan(now, nil, store)
	plan.seed = nil
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	first := nextApplied(t, plan, &checkpoint)
	if len(first.Entities) != dimensionPagesPerBatch*incrementalBatchCap {
		t.Fatalf("batch 1 has %d entities, want %d pages of %d", len(first.Entities), dimensionPagesPerBatch, incrementalBatchCap)
	}
	state := mustDecode(t, first.NextCursor)
	if last := store.rows[dimensionPagesPerBatch*incrementalBatchCap-1]; state.Dim == nil || state.Dim.At != 0 || !state.Dim.Since.Equal(last.at) || state.Dim.After != last.key {
		t.Fatalf("dimension position = %+v, want the last row of page %d", state.Dim, dimensionPagesPerBatch)
	}
	second := nextApplied(t, plan, &checkpoint)
	if got := entityKeys(second); len(got) != 300 || !got[store.rows[1000].key] || !got[store.rows[1299].key] || got[store.rows[999].key] {
		t.Fatalf("batch 2 has %d entities, want exactly the 300 rows after the first batch", len(got))
	}
}

// Dimension pages that hold nothing to project give no batch. The position
// still moves (the consumed-progress cursor), the call says work is left, and
// the read goes on from there to the fact walk.
func TestDimensionPagesWithNothingToProjectMoveThePosition(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	rows := progressRows((dimensionPagesPerBatch+maxOmittedPageSkips)*incrementalBatchCap+300, now.Add(-10*time.Minute), time.Microsecond)
	plan := dimensionPlan(now, nil, nil)
	plan.seed = nil
	dims := keysetTable("dims", rows)
	dims.dimension = true
	plan.tables[1] = dims
	var consumed string
	plan.recordConsumed = func(_, cursor string) { consumed = cursor }
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	_, available, err := plan.nextBatch(context.Background(), checkpoint)
	if err != nil || available {
		t.Fatalf("available=%v err=%v, want no batch", available, err)
	}
	state := mustDecode(t, consumed)
	if last := rows[(dimensionPagesPerBatch+maxOmittedPageSkips)*incrementalBatchCap-1]; state.Dim == nil || state.Dim.At != 0 || !state.Dim.Since.Equal(last.at) || state.Dim.After != last.key || !state.Since.IsZero() {
		t.Fatalf("consumed cursor = %+v (dim %+v), want the dimension position after the pages read", state, state.Dim)
	}
	if !reportCatchUp(plan.window, checkpoint).WorkAhead {
		t.Fatal("the call read pages and stopped before the end, and does not say work is left")
	}
	checkpoint.Cursor = consumed
	if batch := nextApplied(t, plan, &checkpoint); entitiesPerTable(batch)["01"] != incrementalBatchCap {
		t.Fatalf("the next batch = %v, want the first fact page", entitiesPerTable(batch))
	}
}

// A cursor that names a table this plan does not read: the table is passed
// over and the phase reads the tables it knows.
func TestDimensionPhasePassesOverATableThePlanDoesNotRead(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	plan := dimensionPlan(now, nil, catchUpRows(2, 300, now.Add(-10*time.Minute), time.Millisecond))
	cursor, err := encodeCursorIn(plan.cursorSpace(), cursorState{Dim: &dimensionPosition{Tables: []string{"gone", "dims"}}})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source, Cursor: cursor}
	if batch := nextApplied(t, plan, &checkpoint); entitiesPerTable(batch)["02"] != 300 || len(batch.Entities) != 300 {
		t.Fatalf("batch = %v, want the 300 rows of the known table and no seed", entitiesPerTable(batch))
	}
}

// A dimension read that fails is a table read failure, with no batch.
func TestDimensionReadFailureIsATableReadError(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	plan := dimensionPlan(now, nil, catchUpRows(2, 300, now.Add(-10*time.Minute), time.Millisecond))
	// 300 rows on ONE cursor position and row key: the keyset cannot step.
	same := make(keysetRows, 300)
	for i := range same {
		same[i] = keysetRow{at: now, key: catchUpKey(2, 1)}
	}
	plan.tables[1] = dimensionTable("dims", &liveKeysetRows{rows: same})
	_, available, err := plan.nextBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source})
	var readErr *tableReadError
	if available || !errors.As(err, &readErr) || readErr.table != "dims" {
		t.Fatalf("available=%v err=%v, want a read error that names the dimension table", available, err)
	}
}

// The production registries: repositories; teams and projects.
func TestProductionDimensionTables(t *testing.T) {
	names := func(tables []entityTable) []string {
		var all []string
		for _, table := range tables {
			all = append(all, table.name)
		}
		return sourcePlan{tables: tables}.dimensionTablesIn(all)
	}
	if got := names(entityTables); !slices.Equal(got, []string{"repos"}) {
		t.Errorf("clickhouse source dimension tables = %v, want [repos]", got)
	}
	if got := names(teamsProjectsTablesFor(nil, nil, nil, nil, true)); !slices.Equal(got, []string{"teams", "projects"}) {
		t.Errorf("teams/projects source dimension tables = %v, want [teams projects]", got)
	}
	for _, table := range append(entityTables[:len(entityTables):len(entityTables)], teamsProjectsTablesFor(nil, nil, nil, nil, true)...) {
		if table.dimension && !slices.ContainsFunc(table.subjectKinds, func(contractsv1.ContextFabricSubjectKind) bool { return true }) {
			t.Errorf("table %s is a dimension table and declares no entity kind", table.name)
		}
	}
}

// One batch reads a bounded number of pages even when its rows are far from
// the contract's bounds: 1,300 rows of which only the first is an entity.
func TestDimensionBatchReadsABoundedNumberOfPages(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	store := catchUpRows(2, 1300, now.Add(-10*time.Minute), time.Millisecond)
	first := store.rows[0].key
	plan := dimensionPlan(now, nil, nil)
	plan.seed = nil
	plan.tables[1] = entityTable{name: "dims", dimension: true, query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return fetch(ctx, store, "", rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
			rows, err := scanRepositoryKeysetRow(r)
			if err != nil || rows[0].sortKey == first {
				return rows, err
			}
			return []candidate{progressCandidate(rows[0].observedAt, rows[0].sortKey)}, nil
		})
	}}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	batch := nextApplied(t, plan, &checkpoint)
	state := mustDecode(t, batch.NextCursor)
	if len(batch.Entities) != 1 || state.Dim == nil || state.Dim.Rows != dimensionPagesPerBatch*incrementalBatchCap || state.Dim.At != 0 {
		t.Fatalf("batch 1: %d entities, dimension position %+v, want 1 entity and %d pages read", len(batch.Entities), state.Dim, dimensionPagesPerBatch)
	}
}

// A batch holds at most the contract's entities, relationships and tombstones.
func TestWithinBatchBoundsCountsEveryKind(t *testing.T) {
	of := func(entities, relationships, tombstones int) []candidate {
		var all []candidate
		for i := 0; i < entities; i++ {
			all = append(all, candidate{entity: &contractsv1.ContextFabricEntityProjection{}})
		}
		for i := 0; i < relationships; i++ {
			all = append(all, candidate{relationship: &contractsv1.ContextFabricRelationshipProjection{}})
		}
		for i := 0; i < tombstones; i++ {
			all = append(all, candidate{tombstone: &contractsv1.ContextFabricProjectionTombstone{}})
		}
		return all
	}
	e, r, x := contractsv1.ContextFabricProjectionBatchMaxEntities, contractsv1.ContextFabricProjectionBatchMaxRelationships, contractsv1.ContextFabricProjectionBatchMaxTombstones
	if !withinBatchBounds(of(e, r, x)) {
		t.Error("a batch at every bound is refused")
	}
	if withinBatchBounds(of(e+1, 0, 0)) || withinBatchBounds(of(0, r+1, 0)) || withinBatchBounds(of(0, 0, x+1)) {
		t.Error("a batch one over a bound is accepted")
	}
}

// Dimension rows take the same producer-side repair and the same per-item
// judgement as the walk's rows: a label with blanks around it is trimmed and
// the row is emitted; an item the contract refuses is dropped alone.
func TestDimensionRowsAreNormalizedBeforeTheyAreJudged(t *testing.T) {
	now := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	store := catchUpRows(2, 300, now.Add(-10*time.Minute), time.Millisecond)
	unprojectable := store.rows[7].key
	plan := dimensionPlan(now, nil, nil)
	plan.seed = nil
	plan.tables[1] = entityTable{name: "dims", dimension: true, query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return fetch(ctx, store, "", rowLimitBindings(orgID, cursor, limit), limit, func(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
			rows, err := scanRepositoryKeysetRow(r)
			if err == nil {
				rows[0].entity.Subject.Label = "  " + rows[0].entity.Subject.Label + " "
				if rows[0].sortKey == unprojectable {
					// An instant the contract cannot hold: the item is dropped.
					beyond := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC)
					rows[0].entity.ObservedAt, rows[0].entity.ValidFrom = beyond, &beyond
				}
			}
			return rows, err
		})
	}}
	repaired := 0
	plan.observeNormalization = func(normalizationObservation) { repaired++ }
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org", Source: plan.source}
	batch := nextApplied(t, plan, &checkpoint)
	if len(batch.Entities) != 299 || repaired != 299 || entityKeys(batch)[unprojectable] {
		t.Fatalf("%d entities, %d repaired, want 299 (the row the contract refuses is dropped) and 299", len(batch.Entities), repaired)
	}
	if state := mustDecode(t, batch.NextCursor); state.Dim == nil || state.Dim.Rows != 300 {
		t.Fatalf("dimension position = %+v, want all 300 rows read", state.Dim)
	}
	for _, e := range batch.Entities {
		if e.Subject.Label != strings.TrimSpace(e.Subject.Label) {
			t.Fatalf("label %q was emitted with blanks around it", e.Subject.Label)
		}
	}
}
