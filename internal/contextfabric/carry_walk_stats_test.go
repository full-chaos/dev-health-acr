package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const walkTick = 7 * time.Millisecond

// buildTickingCarryEngine is buildCarryTestEngine with a fake clock that
// advances walkTick on every read.
func buildTickingCarryEngine(t *testing.T, store InvestigationResultStore) *Engine {
	t.Helper()
	var reads int64
	engine := buildCarryTestEngine(t, store)
	engine.now = func() time.Time {
		reads++
		return time.Unix(1000, 0).UTC().Add(time.Duration(reads) * walkTick)
	}
	return engine
}

func windowConfirmedResult(id string) InvestigationResult {
	r := validInvestigationResult()
	r.ResultID = id
	r.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowQuestionStated}
	return r
}

func edgeTo(id, target string) InvestigationResult {
	r := validInvestigationResult()
	r.ResultID = id
	r.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, target, "kindr_"+id)}
	r.ConfirmedStructure[0].Member = contractsv1.ContextFabricStructureNeedExpectedKind
	return r
}

func TestCarryWalkStats_WindowFanOutVisitedAndDuration(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_x": edgeTo("result_x", "result_a"), "result_y": edgeTo("result_y", "result_a"), "result_a": windowConfirmedResult("result_a"),
	}}
	engine := buildTickingCarryEngine(t, store)
	request := validInvestigationRequest()
	receipts := []BoundSubjectReceipt{{ResultID: "result_x", ReceiptID: "receipt_x"}, {ResultID: "result_y", ReceiptID: "receipt_y"}}
	request.PriorSubjectReceipts = receipts
	ctx, rec := withCarryWalkRecorder(context.Background(), engine.now)
	got := engine.resolveCarriedWindow(ctx, storage.Principal{}, request, receipts, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryHit || got.ChainDepth != 1 {
		t.Fatalf("resolveCarriedWindow() = %q depth %d, want hit at depth 1", got.Outcome, got.ChainDepth)
	}
	if s := rec.snapshot(); s.Visited != 3 || s.WalkMillis() != 7 {
		t.Fatalf("walk stats = visited %d, walk_ms %d, want visited 3 (x, y, a) and walk_ms 7 (one walk on the fake clock)", s.Visited, s.WalkMillis())
	}
}

func TestCarryWalkStats_WindowTwoWalksAreSummed(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{
		results: map[string]InvestigationResult{"result_r": validInvestigationResult(), "result_p": validInvestigationResult(), "result_q": validInvestigationResult()},
		parents: map[string]string{"result_p": "result_q"},
	}
	engine := buildTickingCarryEngine(t, store)
	request := validInvestigationRequest()
	request.ParentResultID = "result_p"
	receipts := []BoundSubjectReceipt{{ResultID: "result_r", ReceiptID: "receipt_r"}}
	request.PriorSubjectReceipts = receipts
	ctx, rec := withCarryWalkRecorder(context.Background(), engine.now)
	got := engine.resolveCarriedWindow(ctx, storage.Principal{}, request, receipts, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != WindowCarryMissNoConfirmedWindow {
		t.Fatalf("resolveCarriedWindow().Outcome = %q, want miss_no_confirmed_window", got.Outcome)
	}
	if s := rec.snapshot(); s.Visited != 3 || s.WalkMillis() != 14 {
		t.Fatalf("walk stats = visited %d, walk_ms %d, want visited 3 (r, then p and q) and walk_ms 14 (two walks)", s.Visited, s.WalkMillis())
	}
}

func TestCarryWalkStats_KindFanOutVisitedAndDuration(t *testing.T) {
	t.Parallel()
	kindHolder := validInvestigationResult()
	kindHolder.ResultID = "result_a"
	kindHolder.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_zero", "kindr_zero")}
	x, y := validInvestigationResult(), validInvestigationResult()
	x.ResultID, y.ResultID = "result_x", "result_y"
	store := &staticResultStore{
		results: map[string]InvestigationResult{"result_x": x, "result_y": y, "result_a": kindHolder},
		parents: map[string]string{"result_x": "result_a", "result_y": "result_a"},
	}
	engine := buildTickingCarryEngine(t, store)
	request := validInvestigationRequest()
	receipts := []BoundSubjectReceipt{{ResultID: "result_x", ReceiptID: "receipt_x"}, {ResultID: "result_y", ReceiptID: "receipt_y"}}
	request.PriorSubjectReceipts = receipts
	ctx, rec := withCarryWalkRecorder(context.Background(), engine.now)
	got := engine.resolveCarriedKind(ctx, storage.Principal{}, request, receipts, ResolvedGraphBinding{Epoch: 0}, nil, "")
	if got.Outcome != KindCarryHit || got.ChainDepth != 1 {
		t.Fatalf("resolveCarriedKind() = %q depth %d, want hit at depth 1", got.Outcome, got.ChainDepth)
	}
	if s := rec.snapshot(); s.Visited != 3 || s.WalkMillis() != 7 {
		t.Fatalf("walk stats = visited %d, walk_ms %d, want visited 3 (x, y, a) and walk_ms 7", s.Visited, s.WalkMillis())
	}
}

func TestCarryWalkStats_RecordingDoesNotChangeTheAnswer(t *testing.T) {
	t.Parallel()
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_x": edgeTo("result_x", "result_a"), "result_a": windowConfirmedResult("result_a"),
	}}
	engine := buildTickingCarryEngine(t, store)
	request := validInvestigationRequest()
	receipts := []BoundSubjectReceipt{{ResultID: "result_x", ReceiptID: "receipt_x"}}
	request.PriorSubjectReceipts = receipts
	bare := engine.resolveCarriedWindow(context.Background(), storage.Principal{}, request, receipts, ResolvedGraphBinding{Epoch: 0})
	ctx, _ := withCarryWalkRecorder(context.Background(), engine.now)
	measured := engine.resolveCarriedWindow(ctx, storage.Principal{}, request, receipts, ResolvedGraphBinding{Epoch: 0})
	if bare.Outcome != measured.Outcome || bare.ChainDepth != measured.ChainDepth || bare.SourceResultID != measured.SourceResultID || bare.ViaStoredAncestry != measured.ViaStoredAncestry || bare.Window == nil || measured.Window == nil || *bare.Window != *measured.Window {
		t.Fatalf("measured carry %#v differs from bare carry %#v: telemetry must not change the answer", measured, bare)
	}
}

func TestCarryWalkStats_MissLinesCarryTheStatsToo(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := buildCarryTestEngine(t, &staticResultStore{})
	engine.telemetry = telemetry
	walk := CarryWalkStats{Visited: 3, OtherEpochVisited: 2, Duration: 7 * time.Millisecond}
	engine.recordWindowCarry(context.Background(), storage.Principal{}, windowCarryResult{Outcome: WindowCarryMissNoConfirmedWindow}, CarrySeedReceipt, walk)
	engine.recordKindCarry(context.Background(), storage.Principal{}, kindCarryResult{Outcome: KindCarryMissNoConfirmedKind}, CarrySeedReceipt, walk)
	if len(telemetry.windowCarryWalks) != 1 || telemetry.windowCarryWalks[0] != walk {
		t.Fatalf("window miss walks = %#v, want %#v", telemetry.windowCarryWalks, walk)
	}
	if len(telemetry.kindCarryWalks) != 1 || telemetry.kindCarryWalks[0] != walk {
		t.Fatalf("kind miss walks = %#v, want %#v", telemetry.kindCarryWalks, walk)
	}
}
