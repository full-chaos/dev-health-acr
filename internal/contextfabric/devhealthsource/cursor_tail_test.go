package devhealthsource

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
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

func TestAKeyThatDoesNotSortBelowTheSentinelIsCutNotPassedOver(t *testing.T) {
	t.Parallel()
	key := cursorSentinelKey + strings.Repeat("z", maxCursorKeyBytes(cursorSpaceIngest, cursorTailAt))
	kept, cut, passed := fitCursorTail(cursorSpaceIngest, []candidate{cursorTailRow("a"), cursorTailRow(key)}, false)
	if len(kept) != 1 || cut != 1 || passed || kept[0].passOver {
		t.Fatalf("kept=%d cut=%d passed=%v: a key at or after the sentinel must not be passed over (the cursor would land before it)", len(kept), cut, passed)
	}
}

func TestAnOverlapFrontierKeyThatDoesNotSortBelowTheSentinelBuildsNothingAndDoesNotFail(t *testing.T) {
	at := pageCutStamp
	plan := overlongOverlapPlan(&liveKeysetRows{rows: keysetRows{{at: at.Add(-time.Minute), key: "00000000-0000-4000-8000-000000000001"}}})
	frontier := cursorState{Since: at, After: cursorSentinelKey + strings.Repeat("z", maxCursorKeyBytes(plan.cursorSpace(), at))}
	batch, available, err := plan.overlapBatch(context.Background(), "org", "unused", frontier)
	if err != nil || available || len(batch.Entities) != 0 {
		t.Fatalf("available=%v err=%v entities=%d, want no batch and no error", available, err, len(batch.Entities))
	}
}

// A valid item whose row key is longer than the cursor can carry: the paged
// walk must report it as quarantined (oversize_cursor_key) and still reach the
// rows beyond. Seeded rows: cap+3 over-long keys, then two ordinary ones.
func pagedWalkOverOverlongKeys(t *testing.T, overlong, pageBound int, logs *bytes.Buffer) (batch contextfabric.ProjectionBatch, available bool, err error, observed []quarantineObservation) {
	t.Helper()
	at := pageCutStamp
	long := strings.Repeat("x", maxCursorKeyBytes(cursorSpaceIngest, at)+10)
	var rows keysetRows
	for i := 0; i < overlong; i++ {
		rows = append(rows, keysetRow{at: at, key: fmt.Sprintf("a%04d", i) + long})
	}
	rows = append(rows, keysetRow{at: at, key: "z0001"}, keysetRow{at: at, key: "z0002"})
	scan := overlongKeyScan
	plan := sourcePlan{
		client: rows, source: "cursor_tail_test", version: ClickHouseSourceVersion,
		tables: []entityTable{{name: "repos", query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
			return fetch(ctx, rows, "", rowLimitBindings(orgID, cursor, limit), limit, scan)
		}}},
		observeQuarantine: func(o quarantineObservation) { observed = append(observed, o) },
		overlongPageBound: pageBound,
	}
	if logs != nil {
		plan.logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	batch, available, err = plan.pagedBatch(context.Background(), "org", "", cursorState{}, false)
	return batch, available, err, observed
}

func TestAPagedWalkQuarantinesValidItemsWhoseKeysTheCursorCannotCarry(t *testing.T) {
	t.Parallel()
	batch, available, err, observed := pagedWalkOverOverlongKeys(t, incrementalBatchCap+3, 0, nil)
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the batch of the rows beyond the over-long ones", available, err)
	}
	var ids []string
	for _, e := range batch.Entities {
		ids = append(ids, e.Subject.CanonicalID)
	}
	if got := strings.Join(ids, ","); !strings.HasSuffix(got, "repository:z0001,repository:z0002") {
		t.Fatalf("entities = %v, want the two ordinary rows reached", ids)
	}
	var oversize int
	for _, o := range observed {
		if o.Reason == quarantineOversizeCursorKey && o.Kind == "entity" {
			oversize++
		}
	}
	if oversize == 0 {
		t.Fatalf("no entity quarantined as oversize_cursor_key: %+v", observed)
	}
}

// Many consecutive pages of over-long keys: the walk goes past more pages than
// one tick may skip of omitted rows, up to its own hard bound, and then yields
// with a WARN that names the pages and rows.
func TestAPagedWalkGoesPastManyPagesOfOverlongKeysUpToItsOwnBound(t *testing.T) {
	t.Parallel()
	batch, available, err, _ := pagedWalkOverOverlongKeys(t, incrementalBatchCap*(maxOmittedPageSkips+2), 0, nil)
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the walk to reach the rows beyond %d pages of over-long keys", available, err, maxOmittedPageSkips+2)
	}
	if len(batch.Entities) != 2 {
		t.Fatalf("entities = %d, want the two ordinary rows", len(batch.Entities))
	}
}

func TestAPagedWalkYieldsWithAWarnAtTheOverlongPageBound(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	_, available, err, _ := pagedWalkOverOverlongKeys(t, incrementalBatchCap*6, 3, &logs)
	if err != nil || available {
		t.Fatalf("available=%v err=%v, want a yield at the bound", available, err)
	}
	if !strings.Contains(logs.String(), `"pages":3`) || !strings.Contains(logs.String(), `"rows":600`) {
		t.Fatalf("no WARN naming the pages and rows skipped: %s", logs.String())
	}
}

// overlongKeyScan reads (position, key) rows into a valid repository entity named
// by the first five characters of the key, so the key can be as long as a test needs.
func overlongKeyScan(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
	var rowAt time.Time
	var key string
	if err := r.Scan(&rowAt, &key); err != nil {
		return nil, err
	}
	id := key[:5]
	slug := "acme/" + id
	entity := contractsv1.ContextFabricEntityProjection{
		Subject:        contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:" + id, Label: slug},
		Authorization:  repoAuthorization(slug),
		EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, id)},
		ObservedAt:     rowAt, ValidFrom: requiredTime(rowAt), SourceVersion: ClickHouseSourceVersion,
	}
	return []candidate{{observedAt: rowAt, sortKey: key, entity: &entity}}, nil
}

// A from-scratch snapshot cannot pass over a last row whose key sorts at or after
// the sentinel; that row is quarantined and the rest of the snapshot is published.
func TestASnapshotQuarantinesAValidItemWhoseKeyCannotBePassedOver(t *testing.T) {
	t.Parallel()
	at := pageCutStamp
	rows := keysetRows{
		{at: at, key: "a0001"},
		{at: at, key: cursorSentinelKey + strings.Repeat("z", maxCursorKeyBytes(cursorSpaceIngest, at))},
	}
	var observed []quarantineObservation
	plan := sourcePlan{
		client: rows, source: "cursor_tail_test", version: ClickHouseSourceVersion,
		tables: []entityTable{{name: "repos", query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
			return fetch(ctx, rows, "", rowLimitBindings(orgID, cursor, limit), limit, overlongKeyScan)
		}}},
		observeQuarantine: func(o quarantineObservation) { observed = append(observed, o) },
		now:               func() time.Time { return at.Add(time.Minute) },
	}
	batch, available, err := plan.nextBatchPage(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org", Source: "cursor_tail_test"})
	if err != nil || !available {
		t.Fatalf("available=%v err=%v, want the snapshot batch", available, err)
	}
	found := false
	for _, e := range batch.Entities {
		found = found || e.Subject.CanonicalID == "repository:a0001"
	}
	if !found {
		t.Fatalf("the ordinary row is missing from the snapshot")
	}
	for _, o := range observed {
		if o.Reason == quarantineOversizeCursorKey {
			return
		}
	}
	t.Fatalf("no oversize_cursor_key quarantine: %+v", observed)
}
