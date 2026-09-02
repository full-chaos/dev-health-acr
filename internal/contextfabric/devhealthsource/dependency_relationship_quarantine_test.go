package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4874 / CHAOS-4571 regression suite.
//
// PROD INCIDENT this pins: org c6a38355 stalled with dev_health_clickhouse
// applying 61 batches and then failing every tick, deterministically, for
// hours. work_item_dependencies carried relationship_type values outside the
// 12-member v1 vocabulary -- EXTERNAL_ISSUE_KEY 1,296 rows, BLOCKED_BY 15,
// IS_BLOCKED_BY 1 -- and 105 of the 201 rows on the failing page were
// EXTERNAL_ISSUE_KEY. The producer cast the value straight into
// ContextFabricRelationshipType; ContextFabricProjectionBatch.Validate is
// all-or-nothing, so ONE such row rejected the entire page, the coordinator
// held its checkpoint, and the identical page rebuilt and failed forever.
// The whole table lay after the cursor, which is why 61 batches drained
// first and then it died.

const (
	dependencyTable = "FROM work_item_dependencies AS d"
	quarantineLine  = "context_fabric: projection item quarantined"
)

// dependencyRow builds one work_item_dependencies fixture row in the column
// order queryWorkItemDependencies scans.
func dependencyRow(sourceID, targetID, relationshipType string, at, created time.Time) []any {
	return []any{sourceID, targetID, relationshipType, "repo-1", "example-org/widget-service", at,
		created, uint8(0), zeroTime, uint8(1), created, uint8(0), zeroTime, "repo-1"}
}

// unresolvedDependencyRow is dependencyRow with targetHasCreated = 0: the
// target does not resolve to a work_items row, so queryWorkItemDependencies
// takes its ref-form branch and mints a work_item_ref stub entity beside the
// edge. Every external issue key has this shape by construction.
func unresolvedDependencyRow(sourceID, targetID, relationshipType string, at, created time.Time) []any {
	return []any{sourceID, targetID, relationshipType, "repo-1", "example-org/widget-service", at,
		created, uint8(0), zeroTime, uint8(0), zeroTime, uint8(0), zeroTime, ""}
}

// projectWithQuarantineLog projects one batch with a captured logger and
// returns the batch plus every quarantine observation the source reported.
func projectWithQuarantineLog(t *testing.T, tables []fakeTable, cursor string) (contextfabric.ProjectionBatch, bool, error, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source = source.WithLogger(logger)
	batch, available, batchErr := source.NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor})

	var observations []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" || !strings.Contains(line, quarantineLine) {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		observations = append(observations, entry)
	}
	return batch, available, batchErr, observations
}

func dependencyTablesOnly(t *testing.T, at time.Time, rows [][]any) []fakeTable {
	t.Helper()
	tables := baseTables(at)
	for index, table := range tables {
		if table.match == dependencyTable {
			tables[index].rows = rows
			// Required for any test that pages: without it the fake replays
			// every row regardless of cursor (see fakeTable.cursorOf), which
			// would make a "nothing was lost" assertion vacuously true.
			// Mirrors queryWorkItemDependencies' own sincePredicate/orderBy
			// pair: d.last_synced, and the repo:source:target:type row key.
			tables[index].cursorOf = func(row []any) (time.Time, string) {
				return row[5].(time.Time), row[3].(string) + ":" + row[0].(string) + ":" + row[1].(string) + ":" + row[2].(string)
			}
			continue
		}
		tables[index].rows = nil
	}
	return tables
}

// TestOneUnknownRelationshipTypeQuarantinesTheItemNotTheBatch is the
// POISONING regression, and the core of the fix.
//
// RED on parent: the single EXTERNAL_ISSUE_KEY row makes
// ContextFabricProjectionBatch.Validate reject the WHOLE page, so
// NextProjectionBatch returns an error and none of the 199 legal
// dependencies is projected -- the exact prod wedge.
// GREEN here: the illegal item is dropped and counted; the 199 legal edges
// still project; the batch is published and the cursor advances.
func TestOneUnknownRelationshipTypeQuarantinesTheItemNotTheBatch(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const legal = 199
	rows := make([][]any, 0, legal+1)
	for i := 0; i < legal; i++ {
		rows = append(rows, dependencyRow(fmt.Sprintf("WI-%03d", i), fmt.Sprintf("WI-T%03d", i), "RELATES_TO", at.Add(time.Duration(i)*time.Second), created))
	}
	rows = append(rows, dependencyRow("WI-BAD", "EXT-123", "EXTERNAL_ISSUE_KEY", at.Add(legal*time.Second), created))

	batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	if err != nil {
		t.Fatalf("the batch was rejected instead of quarantining the one bad item: %v", err)
	}
	if !available {
		t.Fatal("expected a batch")
	}

	relates, external := 0, 0
	for _, r := range batch.Relationships {
		switch r.Type {
		case contractsv1.ContextFabricRelationshipRelatesTo:
			relates++
		case "EXTERNAL_ISSUE_KEY":
			external++
		}
	}
	if relates != legal {
		t.Fatalf("projected RELATES_TO edges = %d, want %d -- one bad row must not cost the good ones", relates, legal)
	}
	if external != 0 {
		t.Fatalf("an EXTERNAL_ISSUE_KEY edge reached the batch (%d); it is outside the v1 vocabulary", external)
	}
	if len(observations) != 1 {
		t.Fatalf("quarantine observations = %d, want exactly 1: %+v", len(observations), observations)
	}
	if got := observations[0]["quarantine_reason"]; got != "unknown_relationship_type" {
		t.Fatalf("quarantine_reason = %v, want %q", got, "unknown_relationship_type")
	}
	if got := observations[0]["relationship_type"]; got != "EXTERNAL_ISSUE_KEY" {
		t.Fatalf("relationship_type detail = %v, want %q -- an operator must be able to tell one unmapped value from many distinct problems", got, "EXTERNAL_ISSUE_KEY")
	}
}

// TestProdShapedPageProjectsTheLegalRowsAndCountsTheRest reproduces the
// FAILING PAGE's own composition, as measured read-only on prod by
// lane-4571-phase3: 201 rows, of which 105 are EXTERNAL_ISSUE_KEY and 96 are
// legal. Under the fix that page publishes 96 relationships and reports 105
// quarantines, so batch 62 applies and the organization resumes.
func TestProdShapedPageProjectsTheLegalRowsAndCountsTheRest(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const illegal, legal = 105, 96
	rows := make([][]any, 0, illegal+legal)
	for i := 0; i < illegal; i++ {
		// UNRESOLVED targets, which is what an external issue key always is:
		// it names an issue in another system, never a work_items row. These
		// rows take the ref-form branch and emit a stub entity alongside the
		// edge -- the shape that produced the orphan-node defect, so the
		// entity assertion below is load-bearing, not vacuously zero.
		rows = append(rows, unresolvedDependencyRow(fmt.Sprintf("WI-X%03d", i), fmt.Sprintf("EXT-%03d", i), "EXTERNAL_ISSUE_KEY", at.Add(time.Duration(i)*time.Second), created))
	}
	for i := 0; i < legal; i++ {
		rows = append(rows, dependencyRow(fmt.Sprintf("WI-G%03d", i), fmt.Sprintf("WI-H%03d", i), "RELATES_TO", at.Add(time.Duration(illegal+i)*time.Second), created))
	}

	tables := dependencyTablesOnly(t, at, rows)
	batch, available, err, observations := projectWithQuarantineLog(t, tables, testCursor(t, at.Add(-time.Hour), ""))
	if err != nil {
		t.Fatalf("the prod-shaped page was rejected: %v", err)
	}
	if !available {
		t.Fatal("expected a batch")
	}
	// 201 rows exceed incrementalBatchCap (200) by one, exactly as on prod:
	// truncateToCompleteRows defers the 201st ROW to the next page rather
	// than splitting it. So page one carries 105 quarantined + 95 projected,
	// and the 96th legal edge is DEFERRED, not lost -- proven below.
	const projectedOnFirstPage = legal - 1
	if len(batch.Relationships) != projectedOnFirstPage {
		t.Fatalf("relationships on page 1 = %d, want %d (%d legal less the one row deferred past the %d-row cap)",
			len(batch.Relationships), projectedOnFirstPage, legal, illegal+projectedOnFirstPage)
	}
	// NOT ONE work_item_ref stub may reach the graph: each quarantined edge
	// takes its stub with it. Without the dependent sweep this is 105
	// unreachable orphan nodes -- and on the real organization, 1,296.
	if len(batch.Entities) != 0 {
		t.Fatalf("entities = %d, want 0: every stub belonged to a quarantined edge and must have been dropped with it", len(batch.Entities))
	}
	reasons := make(map[string]int)
	for _, entry := range observations {
		reasons[fmt.Sprint(entry["quarantine_reason"])]++
	}
	// Two drops per illegal row: the edge for its unknown type, and the stub
	// that existed only to be that edge's endpoint. Both counted, neither
	// silent.
	if reasons["unknown_relationship_type"] != illegal {
		t.Fatalf("unknown_relationship_type = %d, want %d: %v", reasons["unknown_relationship_type"], illegal, reasons)
	}
	if reasons["orphaned_dependent"] != illegal {
		t.Fatalf("orphaned_dependent = %d, want %d: %v", reasons["orphaned_dependent"], illegal, reasons)
	}
	if len(observations) != 2*illegal {
		t.Fatalf("total quarantine observations = %d, want %d: %v", len(observations), 2*illegal, reasons)
	}

	// The deferred row must project on the NEXT page. Quarantining items
	// must never advance the cursor past a row that was never emitted --
	// that would turn a visible wedge into silent data loss, which is
	// strictly worse.
	next, nextAvailable, nextErr, _ := projectWithQuarantineLog(t, tables, batch.NextCursor)
	if nextErr != nil {
		t.Fatalf("second page failed: %v", nextErr)
	}
	if !nextAvailable {
		t.Fatal("the 201st row was neither projected on page 1 nor available on page 2 -- it was LOST")
	}
	if len(next.Relationships) != 1 {
		t.Fatalf("page 2 relationships = %d, want exactly the 1 deferred edge", len(next.Relationships))
	}
}

// TestBlockedByInvertsToBlocksWithSwappedEndpoints pins chris's 2026-09-02
// ruling. "A is blocked by B" and "B blocks A" state the same fact, so both
// spellings must emit ONE BLOCKS edge in the SAME direction and converge on
// the SAME relationship id -- otherwise the graph would carry two
// contradictory edges for one dependency.
func TestBlockedByInvertsToBlocksWithSwappedEndpoints(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, spelling := range []string{"BLOCKED_BY", "IS_BLOCKED_BY"} {
		t.Run(spelling, func(t *testing.T) {
			// "WI-A is blocked by WI-B" must become "WI-B BLOCKS WI-A".
			rows := [][]any{dependencyRow("WI-A", "WI-B", spelling, at, created)}
			batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
			if err != nil || !available {
				t.Fatalf("inverted row was not projected: err=%v available=%v", err, available)
			}
			if len(observations) != 0 {
				t.Fatalf("an inverted spelling must be MAPPED, never quarantined: %+v", observations)
			}
			if len(batch.Relationships) != 1 {
				t.Fatalf("relationships = %d, want 1", len(batch.Relationships))
			}
			edge := batch.Relationships[0]
			if edge.Type != contractsv1.ContextFabricRelationshipBlocks {
				t.Fatalf("type = %q, want %q", edge.Type, contractsv1.ContextFabricRelationshipBlocks)
			}
			// The EXACT inverse: the row's target is the BLOCKS edge's FROM.
			if !strings.Contains(edge.From.CanonicalID, "WI-B") {
				t.Fatalf("From = %q, want the row's TARGET (WI-B) -- the endpoints must be exchanged", edge.From.CanonicalID)
			}
			if !strings.Contains(edge.To.CanonicalID, "WI-A") {
				t.Fatalf("To = %q, want the row's SOURCE (WI-A)", edge.To.CanonicalID)
			}

			// Convergence: the equivalent forward row must derive the SAME id.
			forward := [][]any{dependencyRow("WI-B", "WI-A", "BLOCKS", at, created)}
			forwardBatch, _, forwardErr, _ := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, forward), testCursor(t, at.Add(-time.Hour), ""))
			if forwardErr != nil {
				t.Fatalf("forward row failed: %v", forwardErr)
			}
			if len(forwardBatch.Relationships) != 1 {
				t.Fatalf("forward relationships = %d, want 1", len(forwardBatch.Relationships))
			}
			if edge.RelationshipID != forwardBatch.Relationships[0].RelationshipID {
				t.Fatalf("%s produced relationship id %q but the equivalent BLOCKS row produced %q -- both state the same fact and must converge on one edge",
					spelling, edge.RelationshipID, forwardBatch.Relationships[0].RelationshipID)
			}
		})
	}
}

// TestLegalVocabularyRowsKeepTheirPreExistingRelationshipIdentity is the
// replay guard for the identity change. An already-projected edge must keep
// the id it has: the mapping's identity spelling applies ONLY to values that
// take a mapping entry, and those could never have projected before (they
// wedged their batch). If this fails, deploying the fix would orphan every
// existing dependency edge instead of replaying batch 62.
func TestLegalVocabularyRowsKeepTheirPreExistingRelationshipIdentity(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, spelling := range []string{"blocks", "relates_to", "duplicates"} {
		t.Run(spelling, func(t *testing.T) {
			rows := [][]any{dependencyRow("WIDGET-101", "WIDGET-099", spelling, at, created)}
			batch, available, err, _ := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
			if err != nil || !available {
				t.Fatalf("legal row not projected: err=%v available=%v", err, available)
			}
			want := workItemDependencyRelationshipID(t, "repo-1", "WIDGET-101", "repo-1", "WIDGET-099", spelling)
			if batch.Relationships[0].RelationshipID != want {
				t.Fatalf("relationship id = %q, want the pre-existing %q -- an unmapped value's identity spelling must stay the RAW column value or every projected edge is orphaned",
					batch.Relationships[0].RelationshipID, want)
			}
		})
	}
}

// TestClickHouseSourceVersionStaysV6ForReplay pins the constant, with the
// reason, so a later bump is a deliberate act rather than a side effect.
//
// REPLAY DEPENDENCY: ProjectionWorker.RunOnce reloads the checkpoint every
// tick and a failed build returns before the checkpoint save, so a held
// cursor replays under any binary -- UNLESS the stored SourceVersion differs
// from the batch's, which returns ErrProjectionSourceVersionChanged and
// demands an operator-driven rebuild instead. Prod stores
// devhealthsource.clickhouse.v6. This fix changes no already-projected
// item's meaning (rows that would newly map or quarantine never projected at
// all -- they wedged), so the existing graph stays valid and batch 62
// replays with no operator action. Bumping this constant would throw that
// away and force a full rebuild of every organization.
func TestClickHouseSourceVersionStaysV6ForReplay(t *testing.T) {
	t.Parallel()
	if devhealthsource.ClickHouseSourceVersion != "devhealthsource.clickhouse.v6" {
		t.Fatalf("ClickHouseSourceVersion = %q, want %q -- see this test's doc comment: bumping it forces a rebuild instead of replaying the held checkpoint",
			devhealthsource.ClickHouseSourceVersion, "devhealthsource.clickhouse.v6")
	}
}

// TestQuarantiningEveryItemOfTheLastRowStillAdvancesTheCursorPastIt isolates
// the cursorSource/items separation in buildBatch, which no other test
// pinned (found by mutation: replacing cursorSource with items in the
// NextCursor derivation left the whole suite green).
//
// The rule: NextCursor comes from every candidate the page CONSUMED, not
// from the subset that survived quarantine. Derive it from the survivors and
// a page whose last row contributed NOTHING reports a watermark behind that
// row -- the next tick re-reads it, re-quarantines it, re-reports it, and the
// walk never passes it. That is the original wedge wearing a different mask:
// no error, just a quarantine logged every tick forever and no progress.
//
// It must be a row whose EVERY item is quarantined. A work_item_dependencies
// row is the wrong instrument here: its illegal edge is accompanied by a
// healing tombstone that is perfectly valid and shares the row's sort key, so
// the surviving item lands on the same cursor position and the two
// derivations agree by accident. A work_items row with an untrimmed title
// fails on BOTH of its candidates -- the entity's Subject.Label and the
// BELONGS_TO_REPOSITORY edge's From, which is that same subject -- leaving
// the row with no representative at all.
//
// (The untrimmed title is itself a real producer defect: tables.go's
// `label := title; if strings.TrimSpace(label) == ""` trims to TEST the
// value but assigns it UNTRIMMED. Normalizing it is follow-up work; this
// test only relies on such a row being unprojectable, which it is.)
func TestQuarantiningEveryItemOfTheLastRowStillAdvancesTheCursorPastIt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)

	row := func(id, title string, observedAt time.Time) []any {
		return []any{id, "repo-000", "example-org/repo-000", title, "in_progress", "", observedAt, observedAt, uint8(0), zeroTime, "", "", "", []string{}}
	}
	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_items AS w" {
			tables[index].rows = [][]any{
				row("WI-1", "Investigate checkout flake", at),
				row("WI-2", "Fix the retry budget", at.Add(time.Second)),
				// LAST, and every one of its candidates is unprojectable.
				row("WI-3", "Trailing whitespace survives the guard ", at.Add(2*time.Second)),
			}
			tables[index].cursorOf = workItemCursorOf
			continue
		}
		tables[index].rows = nil
	}

	batch, available, err, observations := projectWithQuarantineLog(t, tables, testCursor(t, at.Add(-time.Hour), ""))
	if err != nil || !available {
		t.Fatalf("page 1: err=%v available=%v", err, available)
	}
	if len(batch.Entities) != 2 {
		t.Fatalf("page 1 entities = %d, want 2 (the third row is entirely quarantined)", len(batch.Entities))
	}
	if len(observations) != 2 {
		t.Fatalf("page 1 quarantines = %d, want 2 (the bad row's entity AND its BELONGS_TO_REPOSITORY edge): %+v", len(observations), observations)
	}
	for _, entry := range observations {
		if entry["quarantine_reason"] != "untrimmed_label" {
			t.Fatalf("quarantine_reason = %v, want %q", entry["quarantine_reason"], "untrimmed_label")
		}
	}

	// The decisive assertion.
	_, nextAvailable, nextErr, nextObservations := projectWithQuarantineLog(t, tables, batch.NextCursor)
	if nextErr != nil {
		t.Fatalf("page 2: %v", nextErr)
	}
	if nextAvailable {
		t.Fatal("page 2 returned a batch: the cursor did not advance past the fully-quarantined last row")
	}
	if len(nextObservations) != 0 {
		t.Fatalf("the fully-quarantined row was re-read and re-quarantined on page 2 (%d observations) -- NextCursor was derived from the SURVIVING items instead of every consumed candidate, so the walk can never pass it",
			len(nextObservations))
	}
}

// TestQuarantiningAnEdgeAlsoDropsTheStubThatOnlyExistedToBeItsEndpoint is the
// regression for a defect the FIX itself introduced -- the standing lesson
// that a repair is new code with its own attack surface, not a patch that
// only needs re-checking against the original bug.
//
// queryWorkItemDependencies' ref-form branch emits two candidates for one
// row: a work_item_ref STUB entity, and the edge that is the stub's entire
// reason to exist. Per-item quarantine judges items individually, so an edge
// dropped for an unknown relationship type left the stub behind: a node with
// nothing pointing at it, unreachable by any traversal.
//
// This is exactly the production shape. An EXTERNAL_ISSUE_KEY row's target is
// an external issue key, which by construction is not a work item, so every
// one of those rows takes this branch -- 1,296 of them on the affected
// organization, and before the quarantine existed none of it projected at all
// because the batch wedged. The repair made the class newly reachable.
//
// The RESOLVED branch was checked for the same hazard and does NOT have it,
// recorded here so the next reader need not re-derive it. When a resolved
// row's edge is quarantined its two healing tombstones still apply:
// (1) the EDGE tombstone targets a ref-form relationship id derived under the
// same rejected spelling, and an edge under a type the contract refuses could
// never have been written, so the delete matches nothing -- a genuine no-op,
// exactly as applyTombstone's idempotency contract intends; (2) the NODE
// tombstone keys only on the target, never on relationship_type, and stays
// correct because the target genuinely resolved -- the stub it retires is
// obsolete regardless of why this particular edge was dropped. Neither can
// remove a valid prior edge, so no dependent link is needed there.
func TestQuarantiningAnEdgeAlsoDropsTheStubThatOnlyExistedToBeItsEndpoint(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// targetHasCreated = 0 -> the ref-form branch: the target does not
	// resolve to a work item, which is what an external issue key always is.
	unresolved := func(sourceID, targetID, relType string, observedAt time.Time) []any {
		return []any{sourceID, targetID, relType, "repo-1", "example-org/widget-service", observedAt,
			created, uint8(0), zeroTime, uint8(0), zeroTime, uint8(0), zeroTime, ""}
	}

	t.Run("unknown type drops the edge AND its stub", func(t *testing.T) {
		rows := [][]any{unresolved("WI-1", "EXT-ABC-123", "EXTERNAL_ISSUE_KEY", at)}
		batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Nothing projectable is left, so this page yields no batch at all.
		if available && len(batch.Entities) > 0 {
			t.Fatalf("a work_item_ref stub was projected with no edge pointing at it: %+v -- an orphan node, unreachable by any traversal", batch.Entities)
		}
		reasons := make(map[string]int)
		for _, entry := range observations {
			reasons[fmt.Sprint(entry["quarantine_reason"])]++
		}
		if reasons["unknown_relationship_type"] != 1 {
			t.Fatalf("want exactly 1 unknown_relationship_type quarantine, got %v", reasons)
		}
		if reasons["orphaned_dependent"] != 1 {
			t.Fatalf("the stub entity must be dropped as an orphaned dependent and COUNTED, never silently: got %v", reasons)
		}
	})

	t.Run("a legal type keeps both the edge and its stub", func(t *testing.T) {
		rows := [][]any{unresolved("WI-1", "EXT-ABC-123", "RELATES_TO", at)}
		batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
		if err != nil || !available {
			t.Fatalf("legal ref-form row was not projected: err=%v available=%v", err, available)
		}
		if len(observations) != 0 {
			t.Fatalf("a legal row must quarantine nothing: %+v", observations)
		}
		if len(batch.Entities) != 1 || len(batch.Relationships) != 1 {
			t.Fatalf("entities=%d relationships=%d, want 1 and 1 -- the dependent sweep must not touch a healthy pair",
				len(batch.Entities), len(batch.Relationships))
		}
	})
}

// TestAllQuarantinedTailAdvancesTheDurableCursorExactlyOnce is the regression
// for the second defect the fix itself introduced, and the more dangerous of
// the two.
//
// Per-item quarantine made a fully-unprojectable PAGE reachable for this
// source for the first time. `pagedBatch`'s skip loop advances past such
// pages IN-PROCESS only; the DURABLE checkpoint moves when a batch publishes,
// or through `persistConsumedProgress`, which requires the source to
// implement contextfabric.ProjectionProgress. This source did not implement
// it -- only TeamsProjectsSource did -- so an all-quarantined TAIL left the
// checkpoint exactly where it was. Every tick then re-read the same rows,
// re-quarantined them and re-logged every drop, forever, with no error and no
// progress: the original wedge with the diagnosis removed.
//
// On the affected organization that is 1,296 EXTERNAL_ISSUE_KEY rows, each
// costing two quarantine lines (the edge and the stub that existed only to be
// its endpoint) -- roughly 2,592 WARN lines per tick, indefinitely.
//
// The assertion that matters is the SECOND tick's quarantine count: each row
// must be judged exactly ONCE across both ticks. A test that only checked the
// cursor value would pass against a memo that advanced the cursor but still
// re-read the rows.
func TestAllQuarantinedTailAdvancesTheDurableCursorExactlyOnce(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// The whole tail is unprojectable: nothing publishable after it.
	rows := [][]any{
		unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created),
		unresolvedDependencyRow("WI-2", "EXT-2", "EXTERNAL_ISSUE_KEY", at.Add(time.Second), created),
	}
	tables := dependencyTablesOnly(t, at, rows)
	start := testCursor(t, at.Add(-time.Hour), "")

	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var buf bytes.Buffer
	source = source.WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: start}

	// Tick 1: nothing publishable, so no batch.
	if _, available, err := source.NextProjectionBatch(context.Background(), checkpoint); err != nil || available {
		t.Fatalf("tick 1: err=%v available=%v, want no batch and no error", err, available)
	}
	firstTickDrops := strings.Count(buf.String(), quarantineLine)
	if firstTickDrops != 4 {
		t.Fatalf("tick 1 quarantines = %d, want 4 (2 rows x edge+stub)", firstTickDrops)
	}

	// The worker asks for consumed-without-publishing progress. Without this
	// the checkpoint cannot move at all.
	progress, ok, err := source.ConsumedWithoutPublishing(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("consumed progress: %v", err)
	}
	if !ok {
		t.Fatal("the source offered NO consumed progress for an all-quarantined tail: the checkpoint cannot advance, so these rows are re-read and re-logged on every tick forever")
	}
	if progress.NextCursor == "" || progress.NextCursor == checkpoint.Cursor {
		t.Fatalf("consumed NextCursor = %q, want a cursor strictly past the quarantined rows", progress.NextCursor)
	}
	if progress.SourceVersion != devhealthsource.ClickHouseSourceVersion {
		t.Fatalf("consumed SourceVersion = %q, want %q -- progress must be bound to the producer identity that derived it",
			progress.SourceVersion, devhealthsource.ClickHouseSourceVersion)
	}

	// Tick 2 from the advanced checkpoint, exactly as the worker would.
	buf.Reset()
	advanced := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: progress.NextCursor}
	if _, available, err := source.NextProjectionBatch(context.Background(), advanced); err != nil || available {
		t.Fatalf("tick 2: err=%v available=%v, want no batch and no error", err, available)
	}
	if second := strings.Count(buf.String(), quarantineLine); second != 0 {
		t.Fatalf("tick 2 re-quarantined %d items: the rows were read a second time, so the cursor did not really pass them and the log flood is unbounded", second)
	}
}

// TestStaleConsumedMemoIsRefusedAndLeavesTheCursorWhereItWas pins the memo's
// safety guard. A memo records the cursor it advanced TO and the checkpoint it
// started FROM; asked about a different checkpoint it must refuse, because a
// memo derived from another cursor space says nothing about this one. The
// worst case is then a lost optimisation, never a skipped row.
func TestStaleConsumedMemoIsRefusedAndLeavesTheCursorWhereItWas(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created)}
	tables := dependencyTablesOnly(t, at, rows)

	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	recordedAt := testCursor(t, at.Add(-time.Hour), "")
	if _, _, err := source.NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: recordedAt}); err != nil {
		t.Fatalf("seed call: %v", err)
	}

	// Ask about a DIFFERENT checkpoint than the memo was recorded against.
	other := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: testCursor(t, at.Add(-2*time.Hour), "")}
	progress, ok, err := source.ConsumedWithoutPublishing(context.Background(), other)
	if err != nil {
		t.Fatalf("consumed progress: %v", err)
	}
	if ok {
		t.Fatalf("a memo recorded against a different checkpoint was ACCEPTED (NextCursor=%q): it would move a cursor over rows it never examined", progress.NextCursor)
	}
	if progress.NextCursor != "" {
		t.Fatalf("a refused memo must offer no cursor at all, got %q", progress.NextCursor)
	}
}

// TestBothEquivalentSpellingsOnOnePageCollapseToOneEdge closes the gap the
// convergence test left: it proved BLOCKED_BY and BLOCKS derive the same id
// when projected SEPARATELY, which does not show what happens when both
// spellings for the same pair land in ONE batch. They must collapse to a
// single edge -- two rows stating the same fact, one relationship.
func TestBothEquivalentSpellingsOnOnePageCollapseToOneEdge(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// "WI-A is blocked by WI-B" and "WI-B blocks WI-A" -- the same fact.
	rows := [][]any{
		dependencyRow("WI-A", "WI-B", "BLOCKED_BY", at, created),
		dependencyRow("WI-B", "WI-A", "BLOCKS", at.Add(time.Second), created),
	}
	batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	if err != nil || !available {
		t.Fatalf("err=%v available=%v", err, available)
	}
	// EXACTLY ONE edge survives. Convergence is the point -- two rows
	// stating one fact become one relationship -- but the contract enforces
	// unique relationship IDs per batch, so the redundant restatement has to
	// be resolved by the producer or the batch is rejected outright.
	if len(batch.Relationships) != 1 {
		t.Fatalf("relationships = %d, want exactly 1: %+v", len(batch.Relationships), batch.Relationships)
	}
	edge := batch.Relationships[0]
	if edge.Type != contractsv1.ContextFabricRelationshipBlocks {
		t.Fatalf("relationship type = %q, want BLOCKS", edge.Type)
	}
	// Direction must be the forward one: WI-B blocks WI-A.
	if !strings.Contains(edge.From.CanonicalID, "WI-B") || !strings.Contains(edge.To.CanonicalID, "WI-A") {
		t.Fatalf("edge direction is %s -> %s, want WI-B -> WI-A", edge.From.CanonicalID, edge.To.CanonicalID)
	}
	// The redundant row is dropped and COUNTED, never silent.
	reasons := map[string]int{}
	for _, entry := range observations {
		reasons[fmt.Sprint(entry["quarantine_reason"])]++
	}
	if reasons["duplicate_within_batch"] != 1 {
		t.Fatalf("want exactly 1 duplicate_within_batch observation, got %v", reasons)
	}
	if len(observations) != 1 {
		t.Fatalf("the only drop may be the duplicate; got %+v", observations)
	}
	// Its healing tombstone converges on one identity too, or the batch is
	// rejected for duplicate tombstones instead -- the same defect one field
	// over.
	tombstoneKeys := map[string]int{}
	for _, tomb := range batch.Tombstones {
		tombstoneKeys[string(tomb.Kind)+"|"+tomb.CanonicalID]++
	}
	for key, count := range tombstoneKeys {
		if count != 1 {
			t.Fatalf("tombstone %s appears %d times; kind+canonical_id must be unique within a batch", key, count)
		}
	}
}

// TestAllQuarantinedFullSnapshotIsCountedAndAdvances executes round 1's
// second P1, which the reviewer could only ARGUE (its fixture would have
// needed a container). Both halves are real and both were introduced by
// per-item quarantine, which is wired into the SHARED assembly engine and so
// changed every source at once -- not only the one this change set out to fix.
//
// Half 1, the silent loss: TeamsProjectsSource's plan() did not pass an
// observeQuarantine hook, so every item the engine dropped on that source was
// dropped with NO signal whatsoever. That violates the standing ruling that a
// drop is always counted, and it is strictly worse than the wedge it replaced:
// a wedge announces itself.
//
// Half 2, the stall: fullSnapshot returned on an all-quarantined candidate set
// without recording consumed progress, so a from-scratch projection whose
// first snapshot is entirely unprojectable re-read and re-dropped the same
// rows on every tick forever, with no batch and no error. This source has no
// seed candidate, so nothing guarantees it a surviving item -- which is
// exactly why it, and not the ClickHouse source, is where this bites.
//
// The fixture uses timestamps past the epoch-nanosecond range Go can
// represent (DateTime64 reaches 2299; Go stops at 2262-04-11), which the
// contract rejects regardless of which field a producer maps where -- so the
// test does not depend on the teams producer's own field choices.
func TestAllQuarantinedFullSnapshotIsCountedAndAdvances(t *testing.T) {
	t.Parallel()
	beyondGo := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM teams AS tm FINAL", rows: [][]any{
			teamRow("gh:ops-team", "Ops Team", "Ops Team", "github", "ops-team", 1, beyondGo, nil, nil),
		}},
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			projectRow("70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891", "chaos-ops", "full.chaos/chaos-ops", "gitlab", "", "https://gitlab.com/full.chaos/chaos-ops", 1, beyondGo),
		}},
	}}

	var buf bytes.Buffer
	source := enabledTeamsProjectsSource(t, client).
		WithLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}

	_, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if available {
		t.Fatal("expected no batch: every candidate is unprojectable")
	}

	// Half 1: the drops must be COUNTED, never silent.
	drops := strings.Count(buf.String(), quarantineLine)
	if drops == 0 {
		t.Fatal("TeamsProjectsSource dropped every candidate with NO quarantine signal at all -- a silent loss, which is worse than the wedge it replaced")
	}
	if !strings.Contains(buf.String(), devhealthsource.TeamsProjectsSourceName) {
		t.Fatalf("quarantine lines must name their own source, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "unrepresentable_instant") {
		t.Fatalf("want the specific reason token, not a generic one: %s", buf.String())
	}

	// Half 2: the checkpoint must be able to move, or the same rows are
	// re-read and re-dropped on every tick forever.
	progress, ok, err := source.ConsumedWithoutPublishing(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("consumed progress: %v", err)
	}
	if !ok || progress.NextCursor == "" {
		t.Fatal("an all-quarantined FULL SNAPSHOT offered no consumed progress: the cursor cannot advance, so this organization never projects and never errors")
	}
}

// TestQuarantinedTombstoneReportsItsOwnBound is round 1's P3. A dependency
// row's healing tombstones derive EffectiveAt from the SAME observedAt as the
// relationship, so an out-of-range source timestamp produced three drops with
// one cause -- reported as unrepresentable_instant for the relationship and
// the generic contract_bound_violation for both tombstones, leaving two thirds
// of the signal unattributable to the defect that caused it.
func TestQuarantinedTombstoneReportsItsOwnBound(t *testing.T) {
	t.Parallel()
	beyondGo := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := [][]any{dependencyRow("WI-1", "WI-2", "RELATES_TO", beyondGo, created)}

	_, _, _, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, beyondGo, rows), testCursor(t, created, ""))
	if len(observations) == 0 {
		t.Fatal("expected quarantines")
	}
	byKind := map[string]string{}
	for _, entry := range observations {
		byKind[fmt.Sprint(entry["item_kind"])] = fmt.Sprint(entry["quarantine_reason"])
	}
	if got, ok := byKind["tombstone"]; !ok {
		t.Fatalf("expected a tombstone drop among %v", observations)
	} else if got != "unrepresentable_instant" {
		t.Fatalf("tombstone quarantine_reason = %q, want %q -- a tombstone derived from the same out-of-range timestamp as its relationship must name the same cause, not fall through to the generic token",
			got, "unrepresentable_instant")
	}
}

// TestMappedUnresolvedPairDoesNotDuplicateItsStubSubject is round 2's P1.
//
// Two unresolved rows for the same pair under the two inverted spellings both
// map to BLOCKS, so the relationship dedup collapses them to one edge -- but
// each row also mints the SAME work_item_ref stub subject, and entities carry
// their own batch-level uniqueness rule ("subject must appear at most once per
// batch", keyed on kind + canonical ID). Deduplicating only edges therefore
// left both stubs, the contract rejected the batch, and the checkpoint was
// held: the wedge again, reached through the dedup pass that was written to
// prevent it.
//
// Worth recording WHY this was missed rather than only that it was: the
// entity rule is worded differently from the other three, so the search that
// enumerated "must be unique within a batch" did not match it and the lane
// concluded entities had no uniqueness constraint. A negative search is only
// evidence when the term is proven to match the thing sought.
func TestMappedUnresolvedPairDoesNotDuplicateItsStubSubject(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	rows := [][]any{
		unresolvedDependencyRow("WI-1", "EXT-1", "BLOCKED_BY", at, created),
		unresolvedDependencyRow("WI-1", "EXT-1", "IS_BLOCKED_BY", at.Add(time.Second), created),
	}
	batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	if err != nil {
		t.Fatalf("the batch was rejected instead of collapsing the duplicate stub: %v", err)
	}
	if !available {
		t.Fatal("expected a batch")
	}
	if len(batch.Entities) != 1 {
		t.Fatalf("entities = %d, want exactly 1 stub for the one unresolved target: %+v", len(batch.Entities), batch.Entities)
	}
	if len(batch.Relationships) != 1 {
		t.Fatalf("relationships = %d, want exactly 1 converged edge", len(batch.Relationships))
	}
	reasons := map[string]int{}
	for _, entry := range observations {
		reasons[fmt.Sprint(entry["quarantine_reason"])]++
	}
	if reasons["duplicate_within_batch"] == 0 {
		t.Fatalf("the redundant stub and edge must be dropped as duplicates and COUNTED: %v", reasons)
	}
}

// TestValidRowKeepsItsStubWhenAQuarantinedRowSharesTheTarget is round 3's
// second finding, and an ORDERING bug between the two passes this branch
// added -- neither of which is wrong on its own.
//
// Two unresolved rows name the same target. One row's edge is quarantined for
// an unknown type; the other's is perfectly valid. Both mint the SAME
// `work_item_ref` stub subject. Deduplicating BEFORE the orphan sweep keeps
// whichever sorted earlier — the quarantined row's stub — and the sweep then
// removes it as an orphan, by which time the valid row's stub has already
// been discarded as a duplicate. The batch ends up carrying a relationship
// that points at a subject it never projected.
//
// That is not merely untidy. The graph then holds only the implicit endpoint
// stub the edge write creates, which asserts no validity window and is
// therefore admitted at EVERY requested time — so a temporally-bounded
// subject silently becomes unbounded, over-admitting it historically.
//
// Sweeping orphans FIRST and deduplicating afterwards makes the passes
// compose: the sweep removes exactly the unsupported candidates, and
// deduplication then chooses among candidates that are all still supported.
func TestValidRowKeepsItsStubWhenAQuarantinedRowSharesTheTarget(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// The quarantined row sorts FIRST, so it is the one dedup would keep.
	rows := [][]any{
		unresolvedDependencyRow("WI-1", "EXT-1", "EXTERNAL_ISSUE_KEY", at, created),
		unresolvedDependencyRow("WI-2", "EXT-1", "RELATES_TO", at.Add(time.Second), created),
	}
	batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, at.Add(-time.Hour), ""))
	if err != nil || !available {
		t.Fatalf("err=%v available=%v", err, available)
	}

	if len(batch.Relationships) != 1 {
		t.Fatalf("relationships = %d, want the 1 valid RELATES_TO edge", len(batch.Relationships))
	}
	edge := batch.Relationships[0]

	// The decisive assertion: the surviving edge's endpoint must be projected
	// as a real entity, not left to the backend's implicit stub.
	if len(batch.Entities) != 1 {
		t.Fatalf("entities = %d, want exactly 1 stub for the target the surviving edge points at -- without it the backend mints an implicit stub with NO validity window, admitted at every requested time: %+v",
			len(batch.Entities), batch.Entities)
	}
	stub := batch.Entities[0]
	if stub.Subject.CanonicalID != edge.To.CanonicalID {
		t.Fatalf("projected stub %q does not match the surviving edge's target %q", stub.Subject.CanonicalID, edge.To.CanonicalID)
	}
	if stub.ValidFrom == nil {
		t.Fatal("the surviving stub must carry the validity window its source row asserted; a nil window is what the implicit backend stub would have given us anyway")
	}

	// Both drops still counted, neither silent.
	reasons := map[string]int{}
	for _, entry := range observations {
		reasons[fmt.Sprint(entry["quarantine_reason"])]++
	}
	if reasons["unknown_relationship_type"] != 1 {
		t.Fatalf("want the quarantined edge counted once: %v", reasons)
	}
	if reasons["orphaned_dependent"] != 1 {
		t.Fatalf("want the quarantined row's own stub dropped as an orphan and counted: %v", reasons)
	}
}

// TestQuarantinedDuplicateEdgeDoesNotOrphanTheSurvivingRowsStub is defect six,
// found by the generated-input property test rather than by inspection.
//
// The inverted-spelling mapping makes `A BLOCKED_BY B` and `B BLOCKS A`
// converge on ONE relationship id -- deliberately, so the two spellings become
// one edge. But the orphan sweep recorded quarantined relationships BY ID, and
// a quarantined id is not the same thing as an id with no surviving carrier:
// when one row's edge breaches a bound the other's does not, the sweep would
// orphan the SURVIVING row's stub, deleting a valid endpoint because an
// unrelated duplicate failed.
//
// Every hand-built fixture missed this because it needs three things at once:
// one shared id, two source rows, and validity that diverges between them.
// That combination is what a generator produces and a fixture author does not.
func TestQuarantinedDuplicateEdgeDoesNotOrphanTheSurvivingRowsStub(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Past Go's epoch-nanosecond range: the contract refuses it, so this
	// row's edge AND stub are quarantined while the other row's are fine.
	beyondGo := time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC)

	rows := [][]any{
		// Healthy row: A is blocked by B -> emitted as B BLOCKS A.
		unresolvedDependencyRow("WI-A", "EXT-1", "BLOCKED_BY", at, created),
		// Same pair, same converged id, but unprojectable on its own bound.
		unresolvedDependencyRow("WI-A", "EXT-1", "IS_BLOCKED_BY", beyondGo, created),
	}
	batch, available, err, observations := projectWithQuarantineLog(t, dependencyTablesOnly(t, at, rows), testCursor(t, created, ""))
	if err != nil || !available {
		t.Fatalf("err=%v available=%v", err, available)
	}
	if len(batch.Relationships) != 1 {
		t.Fatalf("relationships = %d, want the 1 healthy converged edge", len(batch.Relationships))
	}
	edge := batch.Relationships[0]
	if len(batch.Entities) != 1 {
		t.Fatalf("entities = %d, want the healthy row's stub kept -- a quarantined DUPLICATE of an edge must not orphan the surviving row's endpoint: %+v",
			len(batch.Entities), batch.Entities)
	}
	// The inverted spelling exchanges endpoints, so the ref stub is the
	// edge's FROM here, not its TO -- assert it is an endpoint, not which one.
	stub := batch.Entities[0].Subject.CanonicalID
	if stub != edge.From.CanonicalID && stub != edge.To.CanonicalID {
		t.Fatalf("kept stub %q is not an endpoint of the surviving edge (%s -> %s)",
			stub, edge.From.CanonicalID, edge.To.CanonicalID)
	}
	reasons := map[string]int{}
	for _, entry := range observations {
		reasons[fmt.Sprint(entry["quarantine_reason"])]++
	}
	if reasons["orphaned_dependent"] != 0 {
		t.Fatalf("no stub may be orphaned here: the shared id still has a surviving carrier: %v", reasons)
	}
}
