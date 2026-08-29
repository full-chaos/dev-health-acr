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
	"sort"
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
		WindowInferredTierRanCount: 1, WindowGatedCount: 1, WindowGatedSilentCount: 1, WindowClassDefaultGatedCount: 1,
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

// TestMergeReportsSumsPairRetriedCountAcrossShards (CHAOS-4138, schema v17)
// is the direct proof PairRetriedCount survives a merge -- the same
// direct-mergeReports-call style TestMergeReportsConcatenatesTimingsAndRecomputesSummary
// above uses, not the round-trip test (which never asserts this specific
// field's arithmetic). InferredPairInvalidCount rides along unset (0) on
// both shards here deliberately: this test pins ONLY the new field's own
// `+=` correctness, not InferredPairInvalidCount==0's own zero-tolerance
// gate (main's own evaluateGates, exercised elsewhere).
func TestMergeReportsSumsPairRetriedCountAcrossShards(t *testing.T) {
	shard0 := shardReport(0, 2, nil)
	shard0.PairRetriedCount = 1
	shard1 := shardReport(1, 2, nil)
	shard1.PairRetriedCount = 2

	merged := mergeReports([]twoTurnReport{shard0, shard1})

	if got, want := merged.PairRetriedCount, 3; got != want {
		t.Errorf("merged.PairRetriedCount = %d, want %d (1 + 2, summed across both shards)", got, want)
	}
}

// TestMergeReportsSumsConfirmedKindVectorCensusAcrossShards (CHAOS-4307,
// schema v30) is PairRetriedCount's own twin for the new census rollup:
// direct mergeReports call, pins the map-sum (StateCount, same discipline as
// MutationProbesRun/MutationProbesTripped) AND the five scalar `+=` sums
// together, since all six fields are folded from the SAME event stream and a
// mirror drift on any one of them would silently understate the run's own
// cost/closure numbers.
func TestMergeReportsSumsConfirmedKindVectorCensusAcrossShards(t *testing.T) {
	shard0 := shardReport(0, 2, nil)
	shard0.ConfirmedKindVectorCensusStateCount = map[string]int{"complete": 1, "over_budget": 1}
	shard0.ConfirmedKindVectorCensusPopulationSum = 20
	shard0.ConfirmedKindVectorCensusComparisonSum = 44
	shard0.ConfirmedKindVectorCensusQueryCountSum = 2
	shard0.ConfirmedKindVectorCensusRivalCountAboveTauSum = 5
	shard0.ConfirmedKindVectorCensusDurationMSSum = 100

	shard1 := shardReport(1, 2, nil)
	shard1.ConfirmedKindVectorCensusStateCount = map[string]int{"complete": 2, "failed": 1}
	shard1.ConfirmedKindVectorCensusPopulationSum = 8
	shard1.ConfirmedKindVectorCensusComparisonSum = 33
	shard1.ConfirmedKindVectorCensusQueryCountSum = 3
	shard1.ConfirmedKindVectorCensusRivalCountAboveTauSum = 2
	shard1.ConfirmedKindVectorCensusDurationMSSum = 250

	merged := mergeReports([]twoTurnReport{shard0, shard1})

	wantStates := map[string]int{"complete": 3, "over_budget": 1, "failed": 1}
	if !reflect.DeepEqual(merged.ConfirmedKindVectorCensusStateCount, wantStates) {
		t.Errorf("merged.ConfirmedKindVectorCensusStateCount = %+v, want %+v", merged.ConfirmedKindVectorCensusStateCount, wantStates)
	}
	if got, want := merged.ConfirmedKindVectorCensusPopulationSum, int64(28); got != want {
		t.Errorf("merged.ConfirmedKindVectorCensusPopulationSum = %d, want %d (20+8)", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusComparisonSum, int64(77); got != want {
		t.Errorf("merged.ConfirmedKindVectorCensusComparisonSum = %d, want %d (44+33)", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusQueryCountSum, 5; got != want {
		t.Errorf("merged.ConfirmedKindVectorCensusQueryCountSum = %d, want %d (2+3)", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusRivalCountAboveTauSum, int64(7); got != want {
		t.Errorf("merged.ConfirmedKindVectorCensusRivalCountAboveTauSum = %d, want %d (5+2)", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusDurationMSSum, int64(350); got != want {
		t.Errorf("merged.ConfirmedKindVectorCensusDurationMSSum = %d, want %d (100+250)", got, want)
	}
}

// TestMergeReportsConfirmedKindVectorCensusDefaultsToEmptyNotNil pins that a
// run with zero folded census events (StateCount nil on every shard, same
// zero value the field's own omitempty tag produces on an unmarshaled
// artifact that never carried the key) still merges to a non-nil, empty map
// -- the SAME "always addressable, never nil" convention OfferMissCount and
// every other summed map field on this struct already gets from mergeReports'
// own struct-literal initialization, so a caller can safely index it without
// a nil-map guard regardless of whether any shard ever populated it.
func TestMergeReportsConfirmedKindVectorCensusDefaultsToEmptyNotNil(t *testing.T) {
	shard0 := shardReport(0, 2, nil)
	shard1 := shardReport(1, 2, nil)

	merged := mergeReports([]twoTurnReport{shard0, shard1})

	if merged.ConfirmedKindVectorCensusStateCount == nil {
		t.Error("merged.ConfirmedKindVectorCensusStateCount = nil, want a non-nil empty map")
	}
	if len(merged.ConfirmedKindVectorCensusStateCount) != 0 {
		t.Errorf("merged.ConfirmedKindVectorCensusStateCount = %+v, want empty", merged.ConfirmedKindVectorCensusStateCount)
	}
}

// TestMutationProbeCoverage (CHAOS-4165) pins mutationProbeCoverage's own
// three behaviors: the floor caps required_min at mutationProbeCoverageFloor
// when eligible exceeds it; a small eligible population caps required_min at
// eligible itself instead (never demanding more runs than the population
// could structurally supply); and a probe kind entirely ABSENT from `ran`
// still gets a returned entry with Runs=0, never silently dropped (the
// exact gap a naive `range ran` would reintroduce -- see
// mutationProbeKinds's own doc comment).
func TestMutationProbeCoverage(t *testing.T) {
	t.Run("eligible exceeds floor: required_min is the floor", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 7, "corrupt_receipt": 7, "stale_superseded_offer": 7}, 65)
		for _, kind := range mutationProbeKinds {
			if got := cov[kind]; got.RequiredMin != mutationProbeCoverageFloor || !got.Adequate {
				t.Errorf("%s = %+v, want required_min=%d adequate=true", kind, got, mutationProbeCoverageFloor)
			}
		}
	})
	t.Run("low run count under the floor: adequate is false", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 1, "corrupt_receipt": 1, "stale_superseded_offer": 1}, 65)
		for _, kind := range mutationProbeKinds {
			if got := cov[kind]; got.Runs != 1 || got.RequiredMin != mutationProbeCoverageFloor || got.Adequate {
				t.Errorf("%s = %+v, want runs=1 required_min=%d adequate=false", kind, got, mutationProbeCoverageFloor)
			}
		}
	})
	t.Run("eligible population smaller than the floor: required_min caps at eligible, not the floor", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 2}, 2)
		got := cov["remove_confirmation"]
		if got.RequiredMin != 2 || !got.Adequate {
			t.Errorf("remove_confirmation = %+v, want required_min=2 adequate=true -- a 2-case eligible population must never be held to a 5-run floor it cannot structurally reach", got)
		}
	})
	t.Run("a probe kind absent from ran still gets an explicit zero-runs entry", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{"remove_confirmation": 7}, 65)
		if len(cov) != len(mutationProbeKinds) {
			t.Fatalf("mutationProbeCoverage returned %d kinds, want all %d of mutationProbeKinds regardless of what `ran` carries", len(cov), len(mutationProbeKinds))
		}
		if got := cov["stale_superseded_offer"]; got.Runs != 0 || got.Adequate {
			t.Errorf("stale_superseded_offer (absent from ran) = %+v, want runs=0 adequate=false, not silently missing", got)
		}
	})
	t.Run("zero eligible population: never adequate, even at runs=0 required_min=0", func(t *testing.T) {
		cov := mutationProbeCoverage(map[string]int{}, 0)
		for _, kind := range mutationProbeKinds {
			if got := cov[kind]; got.Adequate {
				t.Errorf("%s = %+v, want adequate=false -- a zero-eligible population's 0>=0 must never read as adequate (codex review finding)", kind, got)
			}
		}
	})
}

// TestMergeReportsSumsMutationProbeEligibleCountAndRecomputesCoverage
// (CHAOS-4165) mirrors TestMergeReportsSumsPairRetriedCountAcrossShards
// immediately above: MutationProbeEligibleCount sums per-shard like every
// other structural count, and MutationProbeCoverage is RECOMPUTED from the
// MERGED sums (never trusted from -- or concatenated out of -- any one
// shard, the same discipline AntiVacuityValid already follows).
func TestMergeReportsSumsMutationProbeEligibleCountAndRecomputesCoverage(t *testing.T) {
	shard0 := shardReport(0, 2, nil)
	shard0.MutationProbeEligibleCount = 1
	shard0.MutationProbesRun = map[string]int{"remove_confirmation": 1, "corrupt_receipt": 1, "stale_superseded_offer": 1}
	shard0.MutationProbesTripped = map[string]int{"remove_confirmation": 1, "corrupt_receipt": 1, "stale_superseded_offer": 1}
	shard1 := shardReport(1, 2, nil)
	shard1.MutationProbeEligibleCount = 1
	shard1.MutationProbesRun = map[string]int{"remove_confirmation": 1, "corrupt_receipt": 1, "stale_superseded_offer": 1}
	shard1.MutationProbesTripped = map[string]int{"remove_confirmation": 1, "corrupt_receipt": 1, "stale_superseded_offer": 1}

	merged := mergeReports([]twoTurnReport{shard0, shard1})

	if got, want := merged.MutationProbeEligibleCount, 2; got != want {
		t.Errorf("merged.MutationProbeEligibleCount = %d, want %d (1 + 1, summed across both shards)", got, want)
	}
	// eligible=2 is below the floor, so required_min caps at the eligible
	// population itself (2) -- runs=2 (1+1 summed) exactly meets it.
	for _, kind := range mutationProbeKinds {
		got := merged.MutationProbeCoverage[kind]
		if got.Runs != 2 || got.RequiredMin != 2 || !got.Adequate {
			t.Errorf("merged.MutationProbeCoverage[%q] = %+v, want {runs:2 required_min:2 adequate:true} -- recomputed from the MERGED sums, not copied from either shard", kind, got)
		}
	}
}

// TestRunEndToEndMergesValidShards is the real JSON round-trip codex
// round-3 review demanded: TestMergeReportsConcatenatesTimingsAndRecomputesSummary
// above calls mergeReports directly on in-memory structs, which never
// proves this tool's actual entrypoint -- json.Unmarshal of a real shard
// file, run()'s validation gates, json.Marshal of the merged output, and a
// second independent decode of what landed on disk -- handles a valid
// current-schema artifact at all. Two real shard files go in; the written merged file is
// read back and its Timings/TimingSummary/Provenance, plus (CHAOS-4062)
// each shard's "unjustified" row's Shadow*/*CommittedSubjects fields
// (including CHAOS-4079's ShadowKindInsensitivityMode), are
// asserted against what shardReport built, closing the gap between "the
// merge function is correct" and "the tool as actually invoked is
// correct".
func TestRunEndToEndMergesValidShards(t *testing.T) {
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
	// CHAOS-4307 (codex R1, Medium, confirmed): TestMergeReportsSumsConfirmedKindVectorCensusAcrossShards
	// above only proves mergeReports' own in-memory arithmetic -- it never
	// proves these fields survive the real json.Marshal-to-disk,
	// json.Unmarshal-from-disk round trip this tool's actual entrypoint
	// performs. A mirror tag/name drift (the exact class of bug this whole
	// end-to-end test exists to catch, per this test's own top-of-function
	// comment) would silently drop an `omitempty` field on decode without
	// tripping the schema-version gate or any purely-in-memory test.
	shard0.ConfirmedKindVectorCensusStateCount = map[string]int{"complete": 1}
	shard0.ConfirmedKindVectorCensusPopulationSum = 20
	shard0.ConfirmedKindVectorCensusComparisonSum = 44
	shard0.ConfirmedKindVectorCensusQueryCountSum = 2
	shard0.ConfirmedKindVectorCensusRivalCountAboveTauSum = 5
	shard0.ConfirmedKindVectorCensusDurationMSSum = 100
	shard1.ConfirmedKindVectorCensusStateCount = map[string]int{"complete": 1, "over_budget": 1}
	shard1.ConfirmedKindVectorCensusPopulationSum = 8
	shard1.ConfirmedKindVectorCensusComparisonSum = 33
	shard1.ConfirmedKindVectorCensusQueryCountSum = 3
	shard1.ConfirmedKindVectorCensusRivalCountAboveTauSum = 2
	shard1.ConfirmedKindVectorCensusDurationMSSum = 250

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

	// Derived, never restated (CHAOS-4100): this assertion was rewritten by
	// hand on each of the last three schema bumps -- three chances to
	// update the tool and forget the test. The tool's own constant is the
	// authority for what it accepts.
	if merged.ReportSchemaVersion != expectedSchemaVersion {
		t.Errorf("merged.ReportSchemaVersion = %q, want %q", merged.ReportSchemaVersion, expectedSchemaVersion)
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

	// CHAOS-4307: the real on-disk round trip of the new census rollup --
	// see this fixture's own comment above for why the purely in-memory
	// mergeReports test cannot catch a JSON tag/name drift on its own.
	wantStates := map[string]int{"complete": 2, "over_budget": 1}
	if !reflect.DeepEqual(merged.ConfirmedKindVectorCensusStateCount, wantStates) {
		t.Errorf("merged (on-disk) ConfirmedKindVectorCensusStateCount = %+v, want %+v", merged.ConfirmedKindVectorCensusStateCount, wantStates)
	}
	if got, want := merged.ConfirmedKindVectorCensusPopulationSum, int64(28); got != want {
		t.Errorf("merged (on-disk) ConfirmedKindVectorCensusPopulationSum = %d, want %d", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusComparisonSum, int64(77); got != want {
		t.Errorf("merged (on-disk) ConfirmedKindVectorCensusComparisonSum = %d, want %d", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusQueryCountSum, 5; got != want {
		t.Errorf("merged (on-disk) ConfirmedKindVectorCensusQueryCountSum = %d, want %d", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusRivalCountAboveTauSum, int64(7); got != want {
		t.Errorf("merged (on-disk) ConfirmedKindVectorCensusRivalCountAboveTauSum = %d, want %d", got, want)
	}
	if got, want := merged.ConfirmedKindVectorCensusDurationMSSum, int64(350); got != want {
		t.Errorf("merged (on-disk) ConfirmedKindVectorCensusDurationMSSum = %d, want %d", got, want)
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
	if !strings.Contains(err.Error(), `report_schema_version="9"`) || !strings.Contains(err.Error(), fmt.Sprintf("want %q", expectedSchemaVersion)) {
		t.Errorf("run() error = %q, want it to name both the got (9) and the tool's own expected schema version", err.Error())
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

// TestTwoTurnCaseResultDecodesProducerShapedDiagnosisFields is the CHAOS-4086
// (schema v11) counterpart of the shadow-field test above, and exists for the
// identical reason: the end-to-end merge test round-trips through THIS
// mirror's own encoder, so a tag typo here would be invisible to it -- the
// bytes would be self-consistently wrong.
//
// The literal below is keyed exactly as the producer emits it. If a tag
// drifts, the field decodes to its zero value and the merged artifact loses
// precisely the diagnosis this schema bump exists to carry, while every
// count still agrees and every other test still passes.
func TestTwoTurnCaseResultDecodesProducerShapedDiagnosisFields(t *testing.T) {
	const producerJSON = `{
		"index": 60,
		"member": "expected_kind",
		"arm": "positive",
		"committed_count": 1,
		"wrong_commit": true,
		"committed_subjects": [{"kind": "project", "canonical_id": "project:acme/widgets"}],
		"expected_kind": "repository",
		"expected_id": "repository:acme/widgets",
		"commit_gate": "evidence_census",
		"tied_statistical_top": true,
		"search_truncated": true,
		"kind_coverage_floor_fired": true,
		"kind_coverage_missing_kinds": 3,
		"kind_coverage_floor_truncated": true,
		"kind_coverage_missing_kinds_list": ["work_item", "repository", "project"],
		"arm_invalid_stage": "validation",
		"arm_invalid_error_type": "*errors.errorString"
	}`
	var got twoTurnCaseResult
	if err := json.Unmarshal([]byte(producerJSON), &got); err != nil {
		t.Fatalf("unmarshal producer-shaped JSON: %v", err)
	}
	wantCommitted := []twoTurnSubjectKindID{{Kind: "project", CanonicalID: "project:acme/widgets"}}
	if !reflect.DeepEqual(got.CommittedSubjects, wantCommitted) {
		t.Errorf("CommittedSubjects = %+v, want %+v -- a tag mismatch silently zeroes this", got.CommittedSubjects, wantCommitted)
	}
	// The whole acceptance bar in one assertion: this row says WHAT was
	// committed, WHAT was expected, WHICH gate fired and whether the
	// coverage floor was involved -- with no annex and no trace re-read.
	for name, pair := range map[string][2]string{
		"expected_kind":          {got.ExpectedKind, "repository"},
		"expected_id":            {got.ExpectedID, "repository:acme/widgets"},
		"commit_gate":            {got.CommitGate, "evidence_census"},
		"arm_invalid_stage":      {got.ArmInvalidStage, "validation"},
		"arm_invalid_error_type": {got.ArmInvalidErrorType, "*errors.errorString"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q -- json tag mismatch would silently zero this", name, pair[0], pair[1])
		}
	}
	for name, pair := range map[string][2]bool{
		"tied_statistical_top":          {got.TiedStatisticalTop, true},
		"search_truncated":              {got.SearchTruncated, true},
		"kind_coverage_floor_fired":     {got.KindCoverageFloorFired, true},
		"kind_coverage_floor_truncated": {got.KindCoverageFloorTruncated, true},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
	if got.KindCoverageMissingKinds != 3 {
		t.Errorf("kind_coverage_missing_kinds = %d, want 3", got.KindCoverageMissingKinds)
	}
	// codex xhigh R3 (2026-08-23, LOW finding): three entries, matching
	// kind_coverage_missing_kinds' count above -- production always keeps
	// them in lockstep.
	wantMissingKindsList := []string{"work_item", "repository", "project"}
	if !reflect.DeepEqual(got.KindCoverageMissingKindsList, wantMissingKindsList) {
		t.Errorf("KindCoverageMissingKindsList = %+v, want %+v -- a tag mismatch silently zeroes this", got.KindCoverageMissingKindsList, wantMissingKindsList)
	}

	zeroRaw, err := json.Marshal(twoTurnCaseResult{Index: 1, Member: "expected_kind", Arm: "positive"})
	if err != nil {
		t.Fatalf("marshal zero-value result: %v", err)
	}
	// Keys are matched WITH their trailing colon, unlike the shadow test
	// above. "expected_kind" is not only a key here, it is also a legal
	// VALUE of the member field ("member":"expected_kind"), so the bare
	// substring form reports a phantom omitempty violation on every
	// expected_kind row. Caught by this test on first run.
	//
	// CHAOS-4183 phase 2: kind_coverage_floor_fired/kind_coverage_missing_
	// kinds/kind_coverage_floor_truncated REMOVED from this list -- omitempty
	// was dropped from all three (twoTurnCaseResult's own doc comment) so a
	// zero-value row now DELIBERATELY carries them at false/0 rather than
	// omitting them; asserting their absence here would fail the very fix
	// this phase exists to ship.
	for _, key := range []string{
		`"committed_subjects":`, `"expected_kind":`, `"expected_id":`,
		`"commit_gate":`, `"tied_statistical_top":`, `"search_truncated":`,
		`"arm_invalid_stage":`, `"arm_invalid_error_type":`,
	} {
		if strings.Contains(string(zeroRaw), key) {
			t.Errorf("zero-value twoTurnCaseResult JSON = %s, want it to omit %s (omitempty)", zeroRaw, key)
		}
	}
	// CHAOS-4183 phase 2: the positive twin of the removal above -- these
	// six keys (arm-level trio + Turn1-prefixed twin) must be PRESENT on a
	// zero-value row, at false/0, never omitted. This is the actual
	// regression pin for the fix: a jq query on a real artifact treats an
	// absent key differently from a present-but-zero one, and this test
	// proves the mirror struct (not just the producer) upholds that.
	for _, key := range []string{
		`"kind_coverage_floor_fired":`, `"kind_coverage_missing_kinds":`,
		`"kind_coverage_floor_truncated":`,
		`"turn1_kind_coverage_floor_fired":`, `"turn1_kind_coverage_missing_kinds":`,
		`"turn1_kind_coverage_floor_truncated":`,
	} {
		if !strings.Contains(string(zeroRaw), key) {
			t.Errorf("zero-value twoTurnCaseResult JSON = %s, want it to carry %s at its zero value (omitempty dropped, CHAOS-4183 phase 2)", zeroRaw, key)
		}
	}
}

// TestTwoTurnCaseResultDecodesProducerShapedSynthesisStatusOverrideFields
// (CHAOS-4103) is this tool's consumer-level proof: real v13-shaped bytes,
// the uncommitted shape's own reason value, decoded through the actual
// mirror struct -- not a struct literal constructed in-process, which would
// prove nothing about the JSON tags a genuine producer artifact depends on.
func TestTwoTurnCaseResultDecodesProducerShapedSynthesisStatusOverrideFields(t *testing.T) {
	const producerJSON = `{
		"index": 12,
		"member": "expected_kind",
		"arm": "inferred_tier",
		"committed_count": 0,
		"wrong_commit": false,
		"synthesis_status_override_fired": true,
		"synthesis_status_override_from": "clarification_required",
		"synthesis_status_override_to": "no_match",
		"synthesis_status_override_reason": "clarification_unavailable_uncommitted",
		"synthesis_status_override_committed_count": 0
	}`
	var got twoTurnCaseResult
	if err := json.Unmarshal([]byte(producerJSON), &got); err != nil {
		t.Fatalf("unmarshal producer-shaped JSON: %v", err)
	}
	if !got.SynthesisStatusOverrideFired {
		t.Error("synthesis_status_override_fired = false, want true -- a tag mismatch silently zeroes this")
	}
	for name, pair := range map[string][2]string{
		"synthesis_status_override_from":   {got.SynthesisStatusOverrideFrom, "clarification_required"},
		"synthesis_status_override_to":     {got.SynthesisStatusOverrideTo, "no_match"},
		"synthesis_status_override_reason": {got.SynthesisStatusOverrideReason, "clarification_unavailable_uncommitted"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q -- json tag mismatch would silently zero this", name, pair[0], pair[1])
		}
	}
	// The distinguishing datum itself: 0 must survive decode, not be
	// confused with "field absent" -- this is the exact value that tells
	// this shape apart from the ordinary committed override.
	if got.SynthesisStatusOverrideCommittedCount != 0 {
		t.Errorf("synthesis_status_override_committed_count = %d, want 0", got.SynthesisStatusOverrideCommittedCount)
	}

	// A committed shape decodes with the ORDINARY reason, not the
	// uncommitted one, and a nonzero committed count.
	const committedJSON = `{
		"index": 13,
		"member": "expected_kind",
		"arm": "positive",
		"committed_count": 1,
		"synthesis_status_override_fired": true,
		"synthesis_status_override_reason": "clarification_unavailable",
		"synthesis_status_override_committed_count": 1
	}`
	var gotCommitted twoTurnCaseResult
	if err := json.Unmarshal([]byte(committedJSON), &gotCommitted); err != nil {
		t.Fatalf("unmarshal producer-shaped JSON: %v", err)
	}
	if gotCommitted.SynthesisStatusOverrideReason != "clarification_unavailable" {
		t.Errorf("reason = %q, want the ordinary %q", gotCommitted.SynthesisStatusOverrideReason, "clarification_unavailable")
	}
	if gotCommitted.SynthesisStatusOverrideCommittedCount != 1 {
		t.Errorf("committed_count = %d, want 1", gotCommitted.SynthesisStatusOverrideCommittedCount)
	}

	// Fired and CommittedCount carry NO omitempty (0/false are exactly the
	// values that distinguish "did not fire" from "fired uncommitted") --
	// a zero-value row must still emit both keys explicitly. From/To/Reason
	// DO carry omitempty: meaningless when Fired is false, and must not
	// clutter every ordinary row that never triggered the override.
	zeroRaw, err := json.Marshal(twoTurnCaseResult{Index: 1, Member: "expected_kind", Arm: "positive"})
	if err != nil {
		t.Fatalf("marshal zero-value result: %v", err)
	}
	for _, want := range []string{`"synthesis_status_override_fired":false`, `"synthesis_status_override_committed_count":0`} {
		if !strings.Contains(string(zeroRaw), want) {
			t.Errorf("zero-value twoTurnCaseResult JSON = %s, want it to contain %s (no omitempty)", zeroRaw, want)
		}
	}
	for _, key := range []string{`"synthesis_status_override_from":`, `"synthesis_status_override_to":`, `"synthesis_status_override_reason":`} {
		if strings.Contains(string(zeroRaw), key) {
			t.Errorf("zero-value twoTurnCaseResult JSON = %s, want it to omit %s (omitempty)", zeroRaw, key)
		}
	}
}

// TestTwoTurnReportDecodesProducerShapedConfirmedKindVectorCensusFields
// (CHAOS-4307, codex round 2, Medium, confirmed) is
// TestTwoTurnCaseResultDecodesProducerShapedShadowFields's own report-level
// twin: TestRunEndToEndMergesValidShards' round trip marshals and unmarshals
// this SAME mirror twoTurnReport type on both ends, which proves internal
// self-consistency but can never catch a json tag on this struct that
// disagrees with what the PRODUCER (a completely separate type in
// internal/runtime/hosted, which cannot be imported here) actually emits --
// a hand-written JSON literal using the exact key strings the producer's own
// doc comments specify is the only thing that can.
func TestTwoTurnReportDecodesProducerShapedConfirmedKindVectorCensusFields(t *testing.T) {
	const producerJSON = `{
		"report_schema_version": "30",
		"confirmed_kind_vector_census_state_count": {"complete": 2, "over_budget": 1},
		"confirmed_kind_vector_census_population_sum": 28,
		"confirmed_kind_vector_census_comparison_sum": 77,
		"confirmed_kind_vector_census_query_count_sum": 5,
		"confirmed_kind_vector_census_rival_count_above_tau_sum": 7,
		"confirmed_kind_vector_census_duration_ms_sum": 350
	}`
	var got twoTurnReport
	if err := json.Unmarshal([]byte(producerJSON), &got); err != nil {
		t.Fatalf("unmarshal producer-shaped JSON: %v", err)
	}
	wantStates := map[string]int{"complete": 2, "over_budget": 1}
	if !reflect.DeepEqual(got.ConfirmedKindVectorCensusStateCount, wantStates) {
		t.Errorf("ConfirmedKindVectorCensusStateCount = %+v, want %+v -- a json tag mismatch would silently leave this nil", got.ConfirmedKindVectorCensusStateCount, wantStates)
	}
	for name, pair := range map[string][2]int64{
		"confirmed_kind_vector_census_population_sum":            {got.ConfirmedKindVectorCensusPopulationSum, 28},
		"confirmed_kind_vector_census_comparison_sum":            {got.ConfirmedKindVectorCensusComparisonSum, 77},
		"confirmed_kind_vector_census_rival_count_above_tau_sum": {got.ConfirmedKindVectorCensusRivalCountAboveTauSum, 7},
		"confirmed_kind_vector_census_duration_ms_sum":           {got.ConfirmedKindVectorCensusDurationMSSum, 350},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %d, want %d -- a json tag mismatch would silently zero this", name, pair[0], pair[1])
		}
	}
	if got.ConfirmedKindVectorCensusQueryCountSum != 5 {
		t.Errorf("confirmed_kind_vector_census_query_count_sum = %d, want 5 -- a json tag mismatch would silently zero this", got.ConfirmedKindVectorCensusQueryCountSum)
	}

	// All six fields carry omitempty (a run that never folded a single
	// confirmed_kind_scope event with a populated census must not clutter
	// every ordinary merged artifact) -- a zero-value report must omit
	// every one of these keys.
	zeroRaw, err := json.Marshal(twoTurnReport{
		OfferMissCount: map[string]int{}, ConfirmedWrongRedeemedCount: map[string]int{},
		MutationProbesTripped: map[string]int{}, MutationProbesRun: map[string]int{},
	})
	if err != nil {
		t.Fatalf("marshal zero-value report: %v", err)
	}
	for _, key := range []string{
		`"confirmed_kind_vector_census_state_count"`,
		`"confirmed_kind_vector_census_population_sum"`,
		`"confirmed_kind_vector_census_comparison_sum"`,
		`"confirmed_kind_vector_census_query_count_sum"`,
		`"confirmed_kind_vector_census_rival_count_above_tau_sum"`,
		`"confirmed_kind_vector_census_duration_ms_sum"`,
	} {
		if strings.Contains(string(zeroRaw), key) {
			t.Errorf("zero-value twoTurnReport JSON = %s, want it to omit %s (omitempty)", zeroRaw, key)
		}
	}
}

// TestMirrorKeysMatchTheProducer is the drift pin's mirror half (CHAOS-4086).
//
// This tool is a HAND-MAINTAINED copy of the harness's twoTurnCaseResult, and
// until now nothing compared the two: the version constant catches a
// DELIBERATE bump, but a field silently missing from this struct decodes to
// its zero value and deletes that diagnosis from every merged artifact while
// every count still agrees and every other test still passes. It has happened
// (trialProvenance.AnchorMembershipOffersEnabled).
//
// The harness's struct lives in a _test.go file and cannot be imported here,
// so the two sides are compared against the SAME checked-in key list instead
// of against each other. The producer half is
// TestChaos4086_MirrorKeysMatchTheProducer in internal/runtime/hosted. Drift
// on either side fails one of them; a schema change must update the list,
// which is what forces this mirror to be revisited in the same change.
func TestMirrorKeysMatchTheProducer(t *testing.T) {
	golden := filepath.Join("..", "..", "testdata", "trial-report", "two_turn_case_result.keys")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read key golden: %v", err)
	}
	typ := reflect.TypeOf(twoTurnCaseResult{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		got = append(got, strings.Split(tag, ",")[0])
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, strings.Fields(string(want))) {
		t.Fatalf("merge-mirror twoTurnCaseResult JSON keys differ from the producer's checked-in list.\nmirror:   %v\nproducer: %v\nA key present in the producer and absent here is SILENTLY DROPPED on decode -- update this struct, do not regenerate the list.", got, strings.Fields(string(want)))
	}
}

// TestChaos4525CohortAnswerExpectedSurvivesDecode is the merge-mirror half of
// CHAOS-4525. The hazard this pins is specific and has bitten this file
// before (trialProvenance.AnchorMembershipOffersEnabled): encoding/json
// DROPS a field this mirror does not declare, silently. Because AnswerRate is
// RECOMPUTED here from the merged Results rather than trusted from a shard, a
// dropped cohort_answer_expected would not surface as a missing key -- it
// would surface as a merged artifact that quietly reports the pre-4525,
// anchored-only answer rate while still labelling itself v42.
//
// RED-FIRST: delete CohortAnswerExpected from twoTurnCaseResult in main.go and
// this test fails on the recomputed rate (0 instead of 0.5), not on a decode
// error -- which is exactly why the assertion is on the RATE and not merely on
// the round-tripped field.
func TestChaos4525CohortAnswerExpectedSurvivesDecode(t *testing.T) {
	// Written as the raw JSON a producer shard actually emits, never as a
	// Go struct literal: a struct literal round-trips through no json tag
	// at all and would pass even with the field undeclared.
	const rawResults = `[
      {"index": 68, "member": "expected_kind", "arm": "positive",
       "cohort_answer_expected": true, "terminal_status": "complete", "claimed_facts_count": 2},
      {"index": 69, "member": "expected_kind", "arm": "positive",
       "cohort_answer_expected": true, "terminal_status": "degraded", "claimed_facts_count": 0}
    ]`

	var results []twoTurnCaseResult
	if err := json.Unmarshal([]byte(rawResults), &results); err != nil {
		t.Fatalf("unmarshal shard results: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("decoded %d rows, want 2", len(results))
	}
	for i, res := range results {
		if !res.CohortAnswerExpected {
			t.Fatalf("row %d: cohort_answer_expected was dropped on decode -- the mirror does not declare the field", i)
		}
	}

	if got, want := chaos4386TwoTurnAnswerRate(results), 0.5; got != want {
		t.Errorf("recomputed AnswerRate = %v, want %v (both cohort rows eligible, one answered)", got, want)
	}

	// And the control half: the same two rows WITHOUT the cohort flag and
	// without an expected id must stay out of the denominator entirely, so
	// a passing assertion above cannot be explained by the gate having been
	// removed rather than widened.
	var controls []twoTurnCaseResult
	if err := json.Unmarshal([]byte(strings.ReplaceAll(rawResults, `"cohort_answer_expected": true`, `"cohort_answer_expected": false`)), &controls); err != nil {
		t.Fatalf("unmarshal control results: %v", err)
	}
	if got, want := chaos4386TwoTurnAnswerRate(controls), 0.0; got != want {
		t.Errorf("recomputed AnswerRate for control rows = %v, want %v", got, want)
	}
}
