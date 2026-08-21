package main

// CHAOS-4058: focused proof that this tool's schema-version bump (6->7)
// and new Timings/TimingSummary merge handling actually work -- this
// binary had no test coverage at all before this change, so these pin the
// exact behavior the round-2 codex review flagged as a real gap (a v7
// producer artifact from TestChaos3742TwoTurnConfirmationReplay would
// otherwise be silently rejected by this hand-maintained mirror's old
// expectedSchemaVersion="6").

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shardReport(shardIndex, shardCount int, timings []twoTurnCaseTiming) twoTurnReport {
	idx, count := shardIndex, shardCount
	return twoTurnReport{
		ReportSchemaVersion: expectedSchemaVersion,
		Provenance: trialProvenance{
			CorpusSHA256: "abc123", Transport: "file_exchange", SourceCommit: "deadbeef",
			RunStartedAt: "2026-08-21T00:00:00Z", ExecutionShape: "parallel",
			ShardIndex: &idx, ShardCount: &count,
		},
		BaseSHA: "deadbeef", OracleAnnexPath: "annex.json", OracleAnnexCorpusSHA: "abc123", OracleAnnexSignedOff: true,
		CasesRun: 1, PositiveAppliedCount: 1, WindowPositiveAppliedCount: 1, GateReachableCount: 1,
		WindowInferredTierRanCount: 1, WindowGatedCount: 1, WindowClassDefaultGatedCount: 1,
		InferredKindHandleDecisiveCount: 1, InferredBaselineEquivalentCount: 1,
		ControlsTotal: 1, ControlsWitnessed: 1,
		OfferMissCount: map[string]int{}, ConfirmedWrongRedeemedCount: map[string]int{"expected_kind": 1},
		MutationProbesTripped: map[string]int{}, MutationProbesRun: map[string]int{},
		ApplicableMembers: []string{"expected_kind"},
		Results:           []twoTurnCaseResult{{Index: shardIndex, Member: "expected_kind", Arm: "positive", Applied: true}},
		Timings:           timings,
	}
}

// TestMergeReportsConcatenatesTimingsAndRecomputesSummary is the direct
// proof of the CHAOS-4058 gap the round-2 codex review found: Timings must
// concatenate across shards (each shard's cases are disjoint, exactly like
// Results), and TimingSummary must be RECOMPUTED from the merged Timings,
// never trusted from -- or simply concatenated out of -- any one shard's
// own (necessarily partial) summary.
func TestMergeReportsConcatenatesTimingsAndRecomputesSummary(t *testing.T) {
	shard0 := shardReport(0, 2, []twoTurnCaseTiming{
		{Index: 0, Member: "expected_kind", Arms: []twoTurnArmTiming{
			{Arm: "turn1", WallDurationMS: 100, ResponderCallCount: 1, ResponderCallTotalMS: 100, ResponderCallMaxMS: 100},
		}},
	})
	shard1 := shardReport(1, 2, []twoTurnCaseTiming{
		{Index: 1, Member: "expected_kind", Arms: []twoTurnArmTiming{
			{Arm: "turn1", WallDurationMS: 300, ResponderCallCount: 1, ResponderCallTotalMS: 250, ResponderCallMaxMS: 250},
		}},
	})

	merged := mergeReports([]twoTurnReport{shard0, shard1})

	if got := len(merged.Timings); got != 2 {
		t.Fatalf("merged.Timings has %d entries, want 2 (one per shard's own case)", got)
	}
	if len(merged.TimingSummary) != 1 || merged.TimingSummary[0].Arm != "turn1" {
		t.Fatalf("merged.TimingSummary = %+v, want exactly one \"turn1\" aggregate", merged.TimingSummary)
	}
	turn1 := merged.TimingSummary[0]
	if turn1.SampleCount != 2 || turn1.WallMeanMS != 200 || turn1.WallMaxMS != 300 {
		t.Errorf("merged turn1 summary = %+v, want {count:2 mean:200 max:300} (recomputed over BOTH shards, not copied from either)", turn1)
	}
	if turn1.ResponderCallCount != 2 || turn1.ResponderCallTotalMS != 350 || turn1.ResponderCallMaxMS != 250 {
		t.Errorf("merged turn1 responder-call summary = %+v, want {count:2 total:350 max:250}", turn1)
	}
}

// TestRunRejectsSchemaVersionMismatch pins the fail-closed behavior this
// tool's own doc comment promises: an artifact whose report_schema_version
// does not match this binary's own expectedSchemaVersion is refused with a
// clear diagnostic, never silently merged (or silently misread) under the
// wrong shape -- exactly what would have happened here had the schema
// bump to "7" landed without a matching update to this mirror.
func TestRunRejectsSchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	stale := shardReport(0, 1, nil)
	stale.ReportSchemaVersion = "6"
	raw, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal stale shard: %v", err)
	}
	shardPath := filepath.Join(dir, "shard0.json")
	if err := os.WriteFile(shardPath, raw, 0o600); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	err = run(filepath.Join(dir, "merged.json"), []string{shardPath}, os.Stdout)
	if err == nil {
		t.Fatal("run() with a schema_version=6 shard = nil error, want a rejection (this tool expects 7)")
	}
	if !strings.Contains(err.Error(), `report_schema_version="6"`) || !strings.Contains(err.Error(), `want "7"`) {
		t.Errorf("run() error = %q, want it to name both the got (6) and want (7) schema versions", err.Error())
	}
}
