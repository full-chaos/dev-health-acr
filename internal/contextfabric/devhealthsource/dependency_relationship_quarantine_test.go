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
		rows = append(rows, dependencyRow(fmt.Sprintf("WI-X%03d", i), fmt.Sprintf("EXT-%03d", i), "EXTERNAL_ISSUE_KEY", at.Add(time.Duration(i)*time.Second), created))
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
	if len(observations) != illegal {
		t.Fatalf("quarantine observations = %d, want %d", len(observations), illegal)
	}
	for _, entry := range observations {
		if entry["quarantine_reason"] != "unknown_relationship_type" {
			t.Fatalf("unexpected quarantine reason: %v", entry["quarantine_reason"])
		}
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

// TestQuarantiningTheLastRowStillAdvancesTheCursorPastIt isolates the
// cursorSource/items separation in buildBatch, which no other test pinned
// (found by mutation: replacing cursorSource with items in buildBatch's
// NextCursor derivation left the whole suite green).
//
// The rule: NextCursor comes from every candidate the page CONSUMED, not
// from the subset that survived quarantine. Derive it from the survivors
// instead and a page whose LAST row is quarantined reports a watermark
// BEHIND that row -- so the next tick re-reads it, re-quarantines it,
// re-reports it, and the cursor never passes it. That is the original wedge
// wearing a different mask: the organization stops making progress at
// exactly the bad row, and now it does so while logging a quarantine every
// tick forever instead of an error.
//
// Seeds two legal rows followed by an illegal one at the LATEST timestamp,
// so the quarantined row is unambiguously last.
func TestQuarantiningTheLastRowStillAdvancesTheCursorPastIt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	rows := [][]any{
		dependencyRow("WI-1", "WI-2", "RELATES_TO", at, created),
		dependencyRow("WI-3", "WI-4", "RELATES_TO", at.Add(time.Second), created),
		dependencyRow("WI-5", "EXT-9", "EXTERNAL_ISSUE_KEY", at.Add(2*time.Second), created), // LAST, and quarantined
	}
	tables := dependencyTablesOnly(t, at, rows)

	batch, available, err, observations := projectWithQuarantineLog(t, tables, testCursor(t, at.Add(-time.Hour), ""))
	if err != nil || !available {
		t.Fatalf("page 1: err=%v available=%v", err, available)
	}
	if len(batch.Relationships) != 2 {
		t.Fatalf("page 1 relationships = %d, want 2", len(batch.Relationships))
	}
	if len(observations) != 1 {
		t.Fatalf("page 1 quarantines = %d, want 1", len(observations))
	}

	// The decisive assertion: the cursor must be PAST the quarantined row,
	// so the next tick finds nothing and never sees that row again.
	_, nextAvailable, nextErr, nextObservations := projectWithQuarantineLog(t, tables, batch.NextCursor)
	if nextErr != nil {
		t.Fatalf("page 2: %v", nextErr)
	}
	if nextAvailable {
		t.Fatal("page 2 returned a batch: the cursor did not advance past the quarantined last row")
	}
	if len(nextObservations) != 0 {
		t.Fatalf("the quarantined row was re-read and re-quarantined on page 2 (%d observations) -- the cursor was derived from the SURVIVING items instead of every consumed candidate, so the walk can never pass it",
			len(nextObservations))
	}
}
