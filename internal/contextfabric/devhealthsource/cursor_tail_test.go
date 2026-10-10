package devhealthsource

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var cursorTailAt = time.Date(2026, 1, 14, 12, 0, 1, 0, time.UTC)

func cursorTailRow(key string) candidate {
	return candidate{observedAt: cursorTailAt, sortKey: key}
}

func TestTheCursorKeyLimitIsTheLongestKeyTheEncodedCursorCarries(t *testing.T) {
	t.Parallel()
	limit := maxCursorKeyBytes(cursorSpaceIngest, cursorTailAt)
	if limit <= 0 || limit >= contractsv1.ContextFabricProjectionCursorMaxLength {
		t.Fatalf("limit %d is not between 0 and the cursor bound", limit)
	}
	if !cursorKeyFits(cursorSpaceIngest, cursorTailAt, strings.Repeat("a", limit)) {
		t.Fatalf("a key of %d bytes does not fit", limit)
	}
	if cursorKeyFits(cursorSpaceIngest, cursorTailAt, strings.Repeat("a", limit+1)) {
		t.Fatalf("a key of %d bytes fits, the limit is not tight", limit+1)
	}
	encoded, err := encodeCursorIn(cursorSpaceIngest, cursorState{Since: cursorTailAt, After: strings.Repeat("a", limit), Ack: strings.Repeat("0", cursorAckHeadroom)})
	if err != nil || len(encoded) > contractsv1.ContextFabricProjectionCursorMaxLength {
		t.Fatalf("a limit-sized cursor with an ack is %d bytes (err %v), over the bound", len(encoded), err)
	}
}

func TestFitCursorTailPassesOverAReadToTheEndAndCutsAPageWithRowsBeyond(t *testing.T) {
	t.Parallel()
	limit := maxCursorKeyBytes(cursorSpaceIngest, cursorTailAt)
	long := strings.Repeat("z", limit+1)
	page := func() []candidate {
		return []candidate{cursorTailRow("a"), cursorTailRow("b"), cursorTailRow(long)}
	}

	kept, cut, passed := fitCursorTail(cursorSpaceIngest, page(), false)
	if len(kept) != 3 || cut != 0 || !passed || !kept[2].passOver || kept[0].passOver || kept[1].passOver {
		t.Fatalf("read to the end: kept=%d cut=%d passed=%v, want the whole page with only the tail marked", len(kept), cut, passed)
	}

	kept, cut, passed = fitCursorTail(cursorSpaceIngest, page(), true)
	if len(kept) != 2 || cut != 1 || passed || kept[1].sortKey != "b" {
		t.Fatalf("rows beyond: kept=%d cut=%d passed=%v, want the over-long tail cut", len(kept), cut, passed)
	}

	kept, cut, passed = fitCursorTail(cursorSpaceIngest, []candidate{cursorTailRow(long), cursorTailRow(long + "y")}, true)
	if len(kept) != 0 || cut != 2 || passed {
		t.Fatalf("only over-long rows: kept=%d cut=%d passed=%v, want all cut", len(kept), cut, passed)
	}

	kept, cut, passed = fitCursorTail(cursorSpaceIngest, []candidate{cursorTailRow(long), cursorTailRow("b")}, true)
	if len(kept) != 2 || cut != 0 || passed {
		t.Fatalf("an over-long row that is not the tail: kept=%d cut=%d passed=%v, want it left alone", len(kept), cut, passed)
	}
}

func TestFitCursorTailOfNothingIsNothing(t *testing.T) {
	t.Parallel()
	for _, ahead := range []bool{false, true} {
		kept, cut, passed := fitCursorTail(cursorSpaceIngest, nil, ahead)
		if len(kept) != 0 || cut != 0 || passed {
			t.Fatalf("ahead=%v: kept=%d cut=%d passed=%v", ahead, len(kept), cut, passed)
		}
	}
}

func overlongOverlapPlan(store *liveKeysetRows) sourcePlan {
	at := pageCutStamp
	return sourcePlan{
		client: keysetRows{}, source: "cursor_tail_test", version: ClickHouseSourceVersion,
		tables: []entityTable{repositoryKeysetTable(store)},
		now:    func() time.Time { return at.Add(time.Minute) }, overlap: 15 * time.Minute, window: newWindowMemo(), windowScope: windowScopeFor("org", 0),
	}
}

func TestAnOverlapBatchWhoseFrontierKeyIsOverlongCarriesACursorThatFits(t *testing.T) {
	at := pageCutStamp
	key := "00000000-0000-4000-8000-000000000001"
	plan := overlongOverlapPlan(&liveKeysetRows{rows: keysetRows{{at: at.Add(-time.Minute), key: key}}})
	long := strings.Repeat("z", maxCursorKeyBytes(plan.cursorSpace(), at)+1)
	frontier := cursorState{Since: at, After: long}
	batch, available, err := plan.overlapBatch(context.Background(), "org", "unused", frontier)
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the window batch", available, err)
	}
	state, err := decodeCursor(batch.NextCursor)
	if err != nil || state.After != cursorSentinelKey || !state.Since.Equal(at) {
		t.Fatalf("NextCursor = %+v (err %v), want the frontier timestamp with the key sentinel", state, err)
	}
}

func TestAnOverlapBatchWhoseLastRowKeyIsOverlongStillBuilds(t *testing.T) {
	at := pageCutStamp
	plan := overlongOverlapPlan(&liveKeysetRows{})
	long := strings.Repeat("z", maxCursorKeyBytes(plan.cursorSpace(), at)+1)
	plan = overlongOverlapPlan(&liveKeysetRows{rows: keysetRows{
		{at: at.Add(-2 * time.Minute), key: "00000000-0000-4000-8000-000000000001"},
		{at: at.Add(-time.Minute), key: long},
	}})
	frontier := cursorState{Since: at, After: "00000000-0000-4000-8000-000000000003"}
	batch, available, err := plan.overlapBatch(context.Background(), "org", "unused", frontier)
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the window batch", available, err)
	}
	if state, err := decodeCursor(batch.NextCursor); err != nil || state.After != frontier.After {
		t.Fatalf("NextCursor = %+v (err %v), want the frontier the window batch keeps", state, err)
	}
}

func TestAPassedOverTailCursorLandsAfterItsRowAndAnUnmarkedOneIsRefused(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("z", maxCursorKeyBytes(cursorSpaceIngest, cursorTailAt)+1)
	row := cursorTailRow(long)
	if _, err := encodeTailCursor(cursorSpaceIngest, row); err == nil {
		t.Fatalf("an unmarked over-long tail encoded a cursor")
	}
	row.passOver = true
	encoded, err := encodeTailCursor(cursorSpaceIngest, row)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	state, err := decodeCursor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !state.Since.Equal(cursorTailAt) || state.After != cursorSentinelKey || !(state.After > long) {
		t.Fatalf("cursor = %+v, want the same timestamp with the key sentinel, which sorts after the row key", state)
	}
	if state.after(cursorTailAt, long) || state.after(cursorTailAt, long+"y") {
		t.Fatalf("a row at the same timestamp with key <= the over-long one is read again")
	}
	if !state.after(cursorTailAt.Add(time.Nanosecond), "a") {
		t.Fatalf("a later row is not read")
	}
}

func TestOversizeCursorKeyRowsAreQuarantinedByKindAndMarkersAreNot(t *testing.T) {
	t.Parallel()
	entity := cursorTailRow("e")
	entity.entity = &contractsv1.ContextFabricEntityProjection{}
	var seen []quarantineObservation
	quarantineOversizeCursorKeyRows([]candidate{entity, cursorTailRow("marker")}, func(o quarantineObservation) { seen = append(seen, o) })
	if quarantineOversizeCursorKey != "oversize_cursor_key" {
		t.Fatalf("reason token = %q", quarantineOversizeCursorKey)
	}
	if len(seen) != 1 || seen[0].Reason != quarantineOversizeCursorKey || seen[0].Kind != "entity" {
		t.Fatalf("observations = %+v, want one entity quarantined as oversize_cursor_key", seen)
	}
	quarantineOversizeCursorKeyRows([]candidate{entity}, nil)
}
