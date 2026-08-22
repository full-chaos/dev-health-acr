package main

// CHAOS-4058: focused proof that this tool's schema-version bump (6->7)
// and new Timings/TimingSummary merge handling actually work -- this
// binary had no test coverage at all before this change, so these pin the
// exact behavior the round-2 codex review flagged as a real gap (a v7
// producer artifact from TestChaos3742TwoTurnConfirmationReplay would
// otherwise be silently rejected by this hand-maintained mirror's old
// expectedSchemaVersion="6").
//
// CHAOS-4062: schema bumped again (7->8) for the shadow-insensitivity
// trace probe -- ShadowKindInsensitivityEvaluated/
// ShadowKindInsensitivityOutcome and BaselineCommittedSubjects/
// HintedCommittedSubjects on twoTurnCaseResult, populated only for the
// "unjustified" InferredClassification outcome. Purely additive per-case
// passthrough (no new merge arithmetic), but still a new key a stale
// mirror would silently drop -- shardReport's "unjustified" result row and
// TestRunEndToEndMergesValidV10Shards below pin that these fields actually
// survive the real JSON round trip through run().
//
// CHAOS-4079: schema bumped again (9->10) for ShadowKindInsensitivityMode,
// which discriminates a verdict proven across an actual census narrowing
// ("narrowed") from one merely observed under a hint that narrowed nothing
// ("observed_no_overlap"/"observed_subsumed") -- the case the probe could
// not evaluate at all before.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
			AnchorMembershipOffersEnabled: true,
		},
		BaseSHA: "deadbeef", OracleAnnexPath: "annex.json", OracleAnnexCorpusSHA: "abc123", OracleAnnexSignedOff: true,
		CasesRun: 1, PositiveAppliedCount: 1, WindowPositiveAppliedCount: 1, GateReachableCount: 1,
		WindowInferredTierRanCount: 1, WindowGatedCount: 1, WindowClassDefaultGatedCount: 1,
		InferredKindHandleDecisiveCount: 1, InferredBaselineEquivalentCount: 1,
		ControlsTotal: 1, ControlsWitnessed: 1,
		OfferMissCount: map[string]int{}, ConfirmedWrongRedeemedCount: map[string]int{"expected_kind": 1},
		MutationProbesTripped: map[string]int{}, MutationProbesRun: map[string]int{},
		ApplicableMembers: []string{"expected_kind"},
		Results: []twoTurnCaseResult{
			{Index: shardIndex, Member: "expected_kind", Arm: "positive", Applied: true},
			// CHAOS-4062 schema v8: an "unjustified"-classified inferred-tier
			// row carrying the new Shadow*/*CommittedSubjects fields, so the
			// round-trip tests below can pin that this hand-maintained
			// mirror actually passes them through (TierRoutedCorrectly:true
			// keeps evaluateGates' inferred-tier routing check satisfied).
			{
				Index: shardIndex, Member: "expected_kind", Arm: "inferred_tier",
				InferredClassification:           "unjustified",
				TierRoutedCorrectly:              true,
				ShadowKindInsensitivityEvaluated: true,
				ShadowKindInsensitivityOutcome:   "would_no_match",
				ShadowKindInsensitivityMode:      "observed_no_overlap",
				BaselineCommittedSubjects:        []twoTurnSubjectKindID{{Kind: "repository", CanonicalID: "repository:acme/widgets"}},
				HintedCommittedSubjects:          []twoTurnSubjectKindID{{Kind: "person", CanonicalID: "person:acme/j.doe"}},
			},
		},
		Timings: timings,
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

// TestRunEndToEndMergesValidV10Shards is the real JSON round-trip codex
// round-3 review demanded: TestMergeReportsConcatenatesTimingsAndRecomputesSummary
// above calls mergeReports directly on in-memory structs, which never
// proves this tool's actual entrypoint -- json.Unmarshal of a real shard
// file, run()'s validation gates, json.Marshal of the merged output, and a
// second independent decode of what landed on disk -- handles a valid v10
// artifact at all. Two real shard files go in; the written merged file is
// read back and its Timings/TimingSummary/Provenance, plus (CHAOS-4062)
// each shard's "unjustified" row's Shadow*/*CommittedSubjects fields
// (including CHAOS-4079's ShadowKindInsensitivityMode), are
// asserted against what shardReport built, closing the gap between "the
// merge function is correct" and "the tool as actually invoked is
// correct".
func TestRunEndToEndMergesValidV10Shards(t *testing.T) {
	dir := t.TempDir()
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

	var shardPaths []string
	for i, shard := range []twoTurnReport{shard0, shard1} {
		raw, err := json.Marshal(shard)
		if err != nil {
			t.Fatalf("marshal shard %d: %v", i, err)
		}
		p := filepath.Join(dir, fmt.Sprintf("shard%d.json", i))
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatalf("write shard %d: %v", i, err)
		}
		shardPaths = append(shardPaths, p)
	}

	mergedPath := filepath.Join(dir, "merged.json")
	var stdout bytes.Buffer
	if err := run(mergedPath, shardPaths, &stdout); err != nil {
		t.Fatalf("run() on two valid v10 shards = %v, want nil (both shards should satisfy every gate)", err)
	}
	if !strings.Contains(stdout.String(), "VALID") {
		t.Errorf("run() stdout = %q, want it to report VALID", stdout.String())
	}

	mergedRaw, err := os.ReadFile(mergedPath)
	if err != nil {
		t.Fatalf("read merged output: %v", err)
	}
	var merged twoTurnReport
	if err := json.Unmarshal(mergedRaw, &merged); err != nil {
		t.Fatalf("unmarshal merged output: %v", err)
	}

	if merged.ReportSchemaVersion != "10" {
		t.Errorf("merged.ReportSchemaVersion = %q, want \"10\"", merged.ReportSchemaVersion)
	}
	if !merged.Provenance.AnchorMembershipOffersEnabled {
		t.Errorf("merged.Provenance.AnchorMembershipOffersEnabled = false, want true (must survive the real JSON round trip, codex round-3 finding)")
	}
	if got := len(merged.Timings); got != 2 {
		t.Fatalf("merged (on-disk) Timings has %d entries, want 2", got)
	}
	if len(merged.TimingSummary) != 1 || merged.TimingSummary[0].Arm != "turn1" {
		t.Fatalf("merged (on-disk) TimingSummary = %+v, want exactly one \"turn1\" aggregate", merged.TimingSummary)
	}
	turn1 := merged.TimingSummary[0]
	if turn1.SampleCount != 2 || turn1.WallMeanMS != 200 || turn1.WallMaxMS != 300 || turn1.ResponderCallMaxMS != 250 {
		t.Errorf("merged (on-disk) turn1 summary = %+v, want {count:2 mean:200 max:300 call_max:250}", turn1)
	}

	// CHAOS-4062 schema v8: each shard's "unjustified" inferred_tier row
	// must survive the real JSON round trip with its Shadow*/
	// *CommittedSubjects fields intact -- this is the actual bug a stale
	// mirror (missing these fields) would produce: json.Unmarshal would
	// silently drop them on read, and the re-Marshal on write would never
	// emit them, without either failing the schema-version gate.
	var unjustified []twoTurnCaseResult
	for _, res := range merged.Results {
		if res.Arm == twoTurnArmInferredTier && res.InferredClassification == "unjustified" {
			unjustified = append(unjustified, res)
		}
	}
	if len(unjustified) != 2 {
		t.Fatalf("merged (on-disk) unjustified inferred_tier rows = %d, want 2 (one per shard)", len(unjustified))
	}
	for _, res := range unjustified {
		if !res.ShadowKindInsensitivityEvaluated || res.ShadowKindInsensitivityOutcome != "would_no_match" || res.ShadowKindInsensitivityMode != "observed_no_overlap" {
			t.Errorf("case %d: shadow_kind_insensitivity(evaluated=%v outcome=%q mode=%q), want (true,\"would_no_match\",\"observed_no_overlap\")", res.Index, res.ShadowKindInsensitivityEvaluated, res.ShadowKindInsensitivityOutcome, res.ShadowKindInsensitivityMode)
		}
		wantBaseline := []twoTurnSubjectKindID{{Kind: "repository", CanonicalID: "repository:acme/widgets"}}
		wantHinted := []twoTurnSubjectKindID{{Kind: "person", CanonicalID: "person:acme/j.doe"}}
		if !reflect.DeepEqual(res.BaselineCommittedSubjects, wantBaseline) {
			t.Errorf("case %d: BaselineCommittedSubjects = %+v, want %+v", res.Index, res.BaselineCommittedSubjects, wantBaseline)
		}
		if !reflect.DeepEqual(res.HintedCommittedSubjects, wantHinted) {
			t.Errorf("case %d: HintedCommittedSubjects = %+v, want %+v", res.Index, res.HintedCommittedSubjects, wantHinted)
		}
	}
}

// TestRunRejectsSchemaVersionMismatch pins the fail-closed behavior this
// tool's own doc comment promises: an artifact whose report_schema_version
// does not match this binary's own expectedSchemaVersion is refused with a
// clear diagnostic, never silently merged (or silently misread) under the
// wrong shape -- exactly what would have happened here had the schema
// bump to "10" (CHAOS-4079's write-free shadow-probe observability) landed
// without a matching update to this mirror: a stale "9" artifact reported
// ShadowKindInsensitivityEvaluated=false for every wrong-kind row because
// the probe could not evaluate there at all, and must never be silently
// merged as if directly comparable to a "10" one, where the same key
// carries a real verdict.
func TestRunRejectsSchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	stale := shardReport(0, 1, nil)
	stale.ReportSchemaVersion = "9"
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
		t.Fatal("run() with a schema_version=9 shard = nil error, want a rejection (this tool expects 10)")
	}
	if !strings.Contains(err.Error(), `report_schema_version="9"`) || !strings.Contains(err.Error(), `want "10"`) {
		t.Errorf("run() error = %q, want it to name both the got (9) and want (10) schema versions", err.Error())
	}
}

// TestTwoTurnCaseResultDecodesProducerShapedShadowFields (codex xhigh review
// finding, CHAOS-4062): every other test in this file builds its shard JSON
// by marshaling this package's OWN twoTurnCaseResult mirror, then decodes it
// back into that same mirror -- a wrong or misspelled json tag on the new
// Shadow*/*CommittedSubjects fields would round-trip through itself
// undetected. This test instead decodes a HAND-WRITTEN JSON literal, keyed
// exactly the way the real producer
// (chaos3742_two_turn_confirmation_test.go's twoTurnCaseResult/
// twoTurnSubjectKindID) emits it, independent of this mirror's own encoding.
// It also pins that a zero-value result omits all four new keys entirely
// (their shared "unjustified"-only, omitempty contract).
func TestTwoTurnCaseResultDecodesProducerShapedShadowFields(t *testing.T) {
	const producerJSON = `{
		"index": 7,
		"member": "expected_kind",
		"arm": "inferred_tier",
		"inferred_classification": "unjustified",
		"shadow_kind_insensitivity_evaluated": true,
		"shadow_kind_insensitivity_outcome": "would_no_match",
		"shadow_kind_insensitivity_mode": "observed_no_overlap",
		"baseline_committed_subjects": [{"kind": "repository", "canonical_id": "repository:acme/widgets"}],
		"hinted_committed_subjects": [{"kind": "person", "canonical_id": "person:acme/j.doe"}]
	}`
	var got twoTurnCaseResult
	if err := json.Unmarshal([]byte(producerJSON), &got); err != nil {
		t.Fatalf("unmarshal producer-shaped JSON: %v", err)
	}
	if !got.ShadowKindInsensitivityEvaluated || got.ShadowKindInsensitivityOutcome != "would_no_match" || got.ShadowKindInsensitivityMode != "observed_no_overlap" {
		t.Errorf("shadow_kind_insensitivity(evaluated=%v outcome=%q mode=%q), want (true,\"would_no_match\",\"observed_no_overlap\") -- json tag mismatch would silently zero this", got.ShadowKindInsensitivityEvaluated, got.ShadowKindInsensitivityOutcome, got.ShadowKindInsensitivityMode)
	}
	wantBaseline := []twoTurnSubjectKindID{{Kind: "repository", CanonicalID: "repository:acme/widgets"}}
	wantHinted := []twoTurnSubjectKindID{{Kind: "person", CanonicalID: "person:acme/j.doe"}}
	if !reflect.DeepEqual(got.BaselineCommittedSubjects, wantBaseline) {
		t.Errorf("BaselineCommittedSubjects = %+v, want %+v", got.BaselineCommittedSubjects, wantBaseline)
	}
	if !reflect.DeepEqual(got.HintedCommittedSubjects, wantHinted) {
		t.Errorf("HintedCommittedSubjects = %+v, want %+v", got.HintedCommittedSubjects, wantHinted)
	}

	zeroRaw, err := json.Marshal(twoTurnCaseResult{Index: 1, Member: "expected_kind", Arm: "positive"})
	if err != nil {
		t.Fatalf("marshal zero-value result: %v", err)
	}
	for _, key := range []string{
		`"shadow_kind_insensitivity_evaluated"`,
		`"shadow_kind_insensitivity_outcome"`,
		`"shadow_kind_insensitivity_mode"`,
		`"baseline_committed_subjects"`,
		`"hinted_committed_subjects"`,
	} {
		if strings.Contains(string(zeroRaw), key) {
			t.Errorf("zero-value twoTurnCaseResult JSON = %s, want it to omit %s (omitempty)", zeroRaw, key)
		}
	}
}
