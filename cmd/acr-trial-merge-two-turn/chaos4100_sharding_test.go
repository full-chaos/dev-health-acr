package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// CHAOS-4100 tests.
//
// This ticket makes the merged artifact the SOLE judge of a sharded run, so
// two properties stop being incidental and become load-bearing:
//
//  1. the judge must actually REFUSE. evaluateGates re-evaluates a superset
//     of the harness's own bars, and nothing exercised its refusal path --
//     only its pass path. A judge whose acquittal is tested and whose
//     conviction is not is not a judge.
//  2. sharding must be MEASUREMENT-INVARIANT. Re-cutting the same run into
//     finer shards must not change the verdict, or "per-case granularity"
//     is a different experiment rather than a faster one.

// writeShards serializes reports to a temp dir and returns their paths, in
// the order given.
func writeShards(t *testing.T, reports []twoTurnReport) (dir string, paths []string) {
	t.Helper()
	dir = t.TempDir()
	for i, r := range reports {
		raw, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatalf("marshal shard %d: %v", i, err)
		}
		p := filepath.Join(dir, "shard"+itoa(i)+".json")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatalf("write shard %d: %v", i, err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// mergeToReport runs the real tool over reports and returns the merged
// artifact plus the gate verdict. Deliberately goes through run() and reads
// the file back off disk rather than calling mergeReports directly -- the
// artifact a reader receives is the one on disk, and this ticket makes that
// artifact the verdict.
func mergeToReport(t *testing.T, reports []twoTurnReport) (twoTurnReport, string, error) {
	t.Helper()
	dir, paths := writeShards(t, reports)
	out := filepath.Join(dir, "merged.json")
	var stdout bytes.Buffer
	err := run(out, paths, &stdout)
	raw, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("merged artifact was not written: %v (run err: %v)", readErr, err)
	}
	var merged twoTurnReport
	if jsonErr := json.Unmarshal(raw, &merged); jsonErr != nil {
		t.Fatalf("merged artifact is not valid JSON: %v", jsonErr)
	}
	return merged, stdout.String(), err
}

// ---------------------------------------------------------------------------
// 1. The judge must refuse
// ---------------------------------------------------------------------------

// TestChaos4100_GateRefusalIsReportedAndNonZero closes the gap CHAOS-4100
// depends on: with bars evaluated on the merged artifact, evaluateGates IS
// the run's verdict, and until now only its VALID path was asserted.
//
// Each case violates exactly one bar, so a refusal that fired for some other
// reason would not satisfy the message assertion.
func TestChaos4100_GateRefusalIsReportedAndNonZero(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*twoTurnReport)
		expect string
	}{
		"wrong_commit": {
			func(r *twoTurnReport) { r.WrongCommitCount = 1 },
			"wrong_commit",
		},
		"false_no_match": {
			func(r *twoTurnReport) { r.FalseNoMatchCount = 1 },
			"false_no_match",
		},
		"synthesis_status_override_uncommitted": {
			// CHAOS-4103: a THIRD zero-tolerance bar, deliberately its own
			// subtest rather than folded into false_no_match above -- the
			// two are separate counts checked by separate fail() calls.
			func(r *twoTurnReport) { r.SynthesisStatusOverrideUncommittedCount = 1 },
			"synthesis_status_override_uncommitted",
		},
		"pair_invalid": {
			func(r *twoTurnReport) { r.InferredPairInvalidCount = 1 },
			"pair_invalid",
		},
		"unjustified": {
			func(r *twoTurnReport) { r.InferredUnjustifiedCount = 1 },
			"unjustified",
		},
		"window_commit": {
			func(r *twoTurnReport) { r.WindowCommitCount = 1 },
			"window_commit",
		},
		"population_bar_positive_applied": {
			// The class that is SKIPPED per shard and can therefore only
			// ever be caught here. At 1-case granularity every shard
			// legitimately reports zero for most of these, so if the merge
			// step did not evaluate them nothing would.
			func(r *twoTurnReport) { r.PositiveAppliedCount = 0 },
			"positive_applied",
		},
	} {
		t.Run(name, func(t *testing.T) {
			shard := shardReport(0, 1, nil)
			tc.mutate(&shard)
			merged, stdout, err := mergeToReport(t, []twoTurnReport{shard})
			if err == nil {
				t.Fatalf("evaluateGates accepted a report violating %s -- the merged artifact is the only judge of a sharded run", tc.expect)
			}
			if !strings.Contains(stdout, "INVALID") {
				t.Errorf("stdout = %q, want it to say INVALID so an operator reading the log sees the verdict", stdout)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("gate error = %q, want it to name %q", err.Error(), tc.expect)
			}
			// The artifact is still written on refusal, deliberately: the
			// evidence of WHY the run failed is the artifact itself.
			if merged.ReportSchemaVersion != expectedSchemaVersion {
				t.Errorf("merged artifact not written correctly on the refusal path")
			}
		})
	}
}

// TestChaos4100_GateAcceptsTheSameRunSplitAnyWay is the control for the
// refusal table above: the unmutated fixture must PASS, or every assertion
// there would be satisfied by a permanently-failing gate.
func TestChaos4100_GateAcceptsTheSameRunSplitAnyWay(t *testing.T) {
	if _, stdout, err := mergeToReport(t, []twoTurnReport{shardReport(0, 1, nil)}); err != nil {
		t.Fatalf("the clean fixture must pass the gate, got %v (stdout %q)", err, stdout)
	}
}

// ---------------------------------------------------------------------------
// 2. Sharding is measurement-invariant (Tier 1)
// ---------------------------------------------------------------------------

// TestChaos4100_RecuttingIntoPerCaseShardsIsMeasurementInvariant is the
// ticket's A/B property, proved deterministically rather than observed once.
//
// THE CLAIM: re-cutting the SAME run into finer shards changes nothing a
// reader or a bar can see. Not "the two runs agreed once on one corpus" --
// that is a live A/B run's evidence, and a live run also varies the model,
// the clock and the machine, so an agreement there is weaker evidence about
// SHARDING specifically than this is.
//
// THE METHOD: take a coarse split, re-cut its rows into one-case shards
// WITHOUT touching a single row, merge both, and require the two merged
// artifacts to be identical except for the fields that describe the cut
// itself. Any merge arithmetic that is not a pure fold over rows -- an
// average, a first-shard-wins, a count of shards -- fails this.
func TestChaos4100_RecuttingIntoPerCaseShardsIsMeasurementInvariant(t *testing.T) {
	// Four cases, cut coarsely into two shards of two.
	coarse := []twoTurnReport{
		shardWithCases(t, 0, 2, []int{0, 2}),
		shardWithCases(t, 1, 2, []int{1, 3}),
	}
	// The SAME four cases, cut per-case into four shards of one.
	fine := []twoTurnReport{
		shardWithCases(t, 0, 4, []int{0}),
		shardWithCases(t, 1, 4, []int{1}),
		shardWithCases(t, 2, 4, []int{2}),
		shardWithCases(t, 3, 4, []int{3}),
	}

	coarseMerged, _, coarseErr := mergeToReport(t, coarse)
	fineMerged, _, fineErr := mergeToReport(t, fine)
	if coarseErr != nil || fineErr != nil {
		t.Fatalf("both cuts must reach a verdict: coarse=%v fine=%v", coarseErr, fineErr)
	}

	// Normalize away ONLY the description of the cut. Everything else --
	// every count, every map, every row, the applicable-member union and
	// the recomputed anti-vacuity verdict -- must already match.
	for _, r := range []*twoTurnReport{&coarseMerged, &fineMerged} {
		r.Provenance.ShardIndex = nil
		r.Provenance.ShardCount = nil
		r.Provenance.Sharding.Granularity = 0
		// Timings carry per-shard wall clock, which is exactly what
		// changing the cut is SUPPOSED to change.
		r.Timings = nil
		r.TimingSummary = nil
	}
	if !reflect.DeepEqual(coarseMerged, fineMerged) {
		t.Fatalf("re-cutting the same run into per-case shards changed the merged artifact -- sharding is not measurement-invariant.\ncoarse: %+v\n\nfine:   %+v", coarseMerged, fineMerged)
	}
	if coarseMerged.CasesRun != 4 {
		t.Fatalf("cases_run = %d, want 4 -- the fixture is not exercising four distinct cases", coarseMerged.CasesRun)
	}
}

// shardWithCases builds a shard carrying exactly the given case indices,
// with per-case counters scaled so the merged totals are identical however
// the cases are distributed.
func shardWithCases(t *testing.T, shardIndex, shardCount int, caseIndices []int) twoTurnReport {
	t.Helper()
	r := shardReport(shardIndex, shardCount, nil)
	r.Results = nil
	r.CasesRun = len(caseIndices)
	r.PositiveAppliedCount = len(caseIndices)
	r.WindowPositiveAppliedCount = len(caseIndices)
	r.GateReachableCount = len(caseIndices)
	r.WindowInferredTierRanCount = len(caseIndices)
	r.WindowGatedCount = len(caseIndices)
	r.WindowGatedSilentCount = len(caseIndices)
	r.WindowClassDefaultGatedCount = len(caseIndices)
	r.InferredKindHandleDecisiveCount = len(caseIndices)
	r.InferredBaselineEquivalentCount = len(caseIndices)
	r.ControlsTotal = len(caseIndices)
	r.ControlsWitnessed = len(caseIndices)
	r.ConfirmedWrongRedeemedCount = map[string]int{"expected_kind": len(caseIndices)}
	r.Provenance.Sharding.CaseIndices = append([]int(nil), caseIndices...)
	r.Provenance.Sharding.ConcurrencyCap = 8
	r.Provenance.Sharding.ProvisioningMode = "template_clone"
	for _, index := range caseIndices {
		r.Results = append(r.Results,
			twoTurnCaseResult{Index: index, Member: "expected_kind", Arm: "positive", Applied: true},
			twoTurnCaseResult{Index: index, Member: "expected_kind", Arm: "inferred_tier", TierRoutedCorrectly: true, InferredClassification: "baseline_equivalent"},
		)
	}
	return r
}

// ---------------------------------------------------------------------------
// 3. Sharding provenance on the merged artifact
// ---------------------------------------------------------------------------

// TestChaos4100_MergedShardingProvenanceUnionsCasesAndTakesSlowestProvision
// pins the two fields that must NOT be inherited from shard 0 with the rest
// of provenance.
//
// Inheriting them would make the merged artifact state, authoritatively,
// that the run covered only shard 0's cases and provisioned in shard 0's
// time. A merged artifact describing one shard is worse than one describing
// none, because it reads as the whole run.
func TestChaos4100_MergedShardingProvenanceUnionsCasesAndTakesSlowestProvision(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	b := shardWithCases(t, 1, 2, []int{1, 3})
	a.Provenance.Sharding.DatabaseProvisionMillis = 120
	b.Provenance.Sharding.DatabaseProvisionMillis = 950

	merged, _, err := mergeToReport(t, []twoTurnReport{a, b})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if want := []int{0, 1, 2, 3}; !reflect.DeepEqual(merged.Provenance.Sharding.CaseIndices, want) {
		t.Errorf("merged case_indices = %v, want the sorted union %v -- this is what a reader checks against the annex to prove no case was dropped", merged.Provenance.Sharding.CaseIndices, want)
	}
	if got := merged.Provenance.Sharding.DatabaseProvisionMillis; got != 950 {
		t.Errorf("merged database_provision_millis = %d, want 950 (the SLOWEST shard) -- shards provision concurrently, so a sum would report time the run never spent", got)
	}
	if merged.Provenance.Sharding.ConcurrencyCap != 8 || merged.Provenance.Sharding.ProvisioningMode != "template_clone" {
		t.Errorf("launch-wide sharding fields did not survive the merge: %+v", merged.Provenance.Sharding)
	}
}

// TestChaos4100_LaunchWideShardingFieldsMustAgreeAcrossShards pins the
// refusal. Granularity, concurrency cap and provisioning mode are decided
// once per fan-out, so shards disagreeing means artifacts from two different
// launches are being merged -- and the merged wall-clock and contention
// story would then describe a run that never happened.
func TestChaos4100_LaunchWideShardingFieldsMustAgreeAcrossShards(t *testing.T) {
	for name, mutate := range map[string]func(*twoTurnReport){
		"granularity":       func(r *twoTurnReport) { r.Provenance.Sharding.Granularity = 7 },
		"concurrency_cap":   func(r *twoTurnReport) { r.Provenance.Sharding.ConcurrencyCap = 64 },
		"provisioning_mode": func(r *twoTurnReport) { r.Provenance.Sharding.ProvisioningMode = "container" },
	} {
		t.Run(name, func(t *testing.T) {
			a := shardWithCases(t, 0, 2, []int{0, 2})
			b := shardWithCases(t, 1, 2, []int{1, 3})
			mutate(&b)
			dir, paths := writeShards(t, []twoTurnReport{a, b})
			var stdout bytes.Buffer
			err := run(filepath.Join(dir, "merged.json"), paths, &stdout)
			if err == nil {
				t.Fatalf("merging shards that disagree about %s must be refused", name)
			}
			if !strings.Contains(err.Error(), "sharding."+name) {
				t.Errorf("error = %q, want it to name provenance.sharding.%s", err.Error(), name)
			}
			if errors.Is(err, os.ErrNotExist) {
				t.Errorf("unexpected error class: %v", err)
			}
		})
	}
}

// TestChaos4135_ResponderModelMustAgreeAcrossShards mirrors
// TestChaos4100_LaunchWideShardingFieldsMustAgreeAcrossShards' own pattern
// for the SAME reason (codex xhigh review, MEDIUM, confirmed): ResponderModel
// is a launch-level fact -- one responder model answers a whole run -- so two
// shards disagreeing about it means artifacts from two different launches
// (or a launcher that changed ACR_TEST_TRIAL_RESPONDER_MODEL mid-run) are
// being merged into one, which mergeReports' own "Provenance: first.Provenance"
// (inheriting only the FIRST shard's value) would otherwise silently
// misattribute.
func TestChaos4135_ResponderModelMustAgreeAcrossShards(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	a.Provenance.ResponderModel = "gpt-5.6-luna"
	b := shardWithCases(t, 1, 2, []int{1, 3})
	b.Provenance.ResponderModel = "gpt-5.6-sol"
	dir, paths := writeShards(t, []twoTurnReport{a, b})
	var stdout bytes.Buffer
	err := run(filepath.Join(dir, "merged.json"), paths, &stdout)
	if err == nil {
		t.Fatal("merging shards that disagree about responder_model must be refused")
	}
	if !strings.Contains(err.Error(), "provenance.responder_model") {
		t.Errorf("error = %q, want it to name provenance.responder_model", err.Error())
	}
}

// TestChaos4186_DataPlaneMustAgreeAcrossShards mirrors
// TestChaos4135_ResponderModelMustAgreeAcrossShards' own pattern for the
// SAME reason: DataPlane is a launch-level fact -- one store backend
// (compose|kiac|override) serves a whole run -- so two shards disagreeing
// about it means artifacts from two different launches (or an operator
// changing ACR_TRIAL_DATA_PLANE mid-run) are being merged into one, which
// mergeReports' own "Provenance: first.Provenance" (inheriting only the
// FIRST shard's value) would otherwise silently misattribute.
func TestChaos4186_DataPlaneMustAgreeAcrossShards(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	a.Provenance.DataPlane = "kiac"
	b := shardWithCases(t, 1, 2, []int{1, 3})
	b.Provenance.DataPlane = "compose"
	dir, paths := writeShards(t, []twoTurnReport{a, b})
	var stdout bytes.Buffer
	err := run(filepath.Join(dir, "merged.json"), paths, &stdout)
	if err == nil {
		t.Fatal("merging shards that disagree about data_plane must be refused")
	}
	if !strings.Contains(err.Error(), "provenance.data_plane") {
		t.Errorf("error = %q, want it to name provenance.data_plane", err.Error())
	}
}

// TestChaos4386_MaxSerializedBytesConfiguredMustAgreeAcrossShards mirrors
// TestChaos4135_ResponderModelMustAgreeAcrossShards' own pattern for the
// SAME reason (codex review round 1, P2, confirmed): MaxSerializedBytesConfigured
// is a launch-level fact -- one effective ACR_MAX_SERIALIZED_BYTES ceiling
// governs a whole run -- so two shards disagreeing about it means artifacts
// from servers configured differently are being merged into one, which
// mergeReports' own "MaxSerializedBytesConfigured: first.MaxSerializedBytesConfigured"
// (inheriting only the FIRST shard's value) would otherwise silently carry
// forward -- misclassifying every OTHER shard's rows' over_max_serialized_bytes_count
// against a cap they were never actually measured under.
func TestChaos4386_MaxSerializedBytesConfiguredMustAgreeAcrossShards(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	a.MaxSerializedBytesConfigured = 262144
	b := shardWithCases(t, 1, 2, []int{1, 3})
	b.MaxSerializedBytesConfigured = 131072
	dir, paths := writeShards(t, []twoTurnReport{a, b})
	var stdout bytes.Buffer
	err := run(filepath.Join(dir, "merged.json"), paths, &stdout)
	if err == nil {
		t.Fatal("merging shards that disagree about max_serialized_bytes_configured must be refused")
	}
	if !strings.Contains(err.Error(), "max_serialized_bytes_configured") {
		t.Errorf("error = %q, want it to name max_serialized_bytes_configured", err.Error())
	}
}

// TestChaos4386_MergeRecomputesFromResultByteSamplesNotJustRowFinals is the
// codex review round 3 (P1, confirmed) regression: a shard's own
// ResultByteSamples can contain a call that never became any row's own
// final ResultBytes (a setup call, a baseline leg, an early turn) --
// mergeReports must recompute the merged distribution from the
// CONCATENATED ResultByteSamples, never from merged.Results[].ResultBytes
// alone, or an oversized intermediate call from one shard would silently
// disappear from the merged artifact.
func TestChaos4386_MergeRecomputesFromResultByteSamplesNotJustRowFinals(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	a.MaxSerializedBytesConfigured = 262144
	// Every ROW's own final ResultBytes is small -- comfortably under
	// budget -- but this shard's raw call population also includes one
	// oversized (300000-byte) intermediate call that was never any row's
	// own final result.
	for i := range a.Results {
		a.Results[i].ResultBytes = 5000
	}
	a.ResultByteSamples = []int64{5000, 5000, 300000}
	b := shardWithCases(t, 1, 2, []int{1, 3})
	b.MaxSerializedBytesConfigured = 262144
	for i := range b.Results {
		b.Results[i].ResultBytes = 6000
	}
	b.ResultByteSamples = []int64{6000, 6000}

	merged := mergeReports([]twoTurnReport{a, b})

	if merged.MaxResultBytes != 300000 {
		t.Errorf("merged.MaxResultBytes = %d, want 300000 -- the oversized intermediate call from shard 0 must survive the merge even though no ROW's own final result carries it", merged.MaxResultBytes)
	}
	if merged.OverMaxSerializedBytesCount != 1 {
		t.Errorf("merged.OverMaxSerializedBytesCount = %d, want 1 -- recomputing from merged.Results[].ResultBytes alone (every row is 5000-6000, all under budget) would silently report 0 and hide the oversized intermediate call", merged.OverMaxSerializedBytesCount)
	}
	wantSamples := []int64{5000, 5000, 6000, 6000, 300000}
	if !reflect.DeepEqual(merged.ResultByteSamples, wantSamples) {
		t.Errorf("merged.ResultByteSamples = %v, want %v sorted -- codex review round 4 (P2, confirmed): unsorted concatenation makes the persisted artifact depend on shard argument order even though the population and every derived statistic are identical either way", merged.ResultByteSamples, wantSamples)
	}
}

// TestChaos4386_MergedResultByteSamplesOrderIsInvariantToShardArgumentOrder
// is the codex review round 4 (P2, confirmed) regression directly: merging
// the SAME two shards in the OPPOSITE argument order must produce a
// byte-identical (not merely statistically-equal) ResultByteSamples slice
// -- the same sharding-invariance goal TestChaos4100_LaunchWideShardingFieldsMustAgreeAcrossShards's
// own package comment states for merged.Results.
func TestChaos4386_MergedResultByteSamplesOrderIsInvariantToShardArgumentOrder(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	a.ResultByteSamples = []int64{300000, 5000, 90000}
	b := shardWithCases(t, 1, 2, []int{1, 3})
	b.ResultByteSamples = []int64{6000, 400000}

	forward := mergeReports([]twoTurnReport{a, b})
	reversed := mergeReports([]twoTurnReport{b, a})

	if !reflect.DeepEqual(forward.ResultByteSamples, reversed.ResultByteSamples) {
		t.Fatalf("ResultByteSamples differs by shard argument order: forward=%v reversed=%v", forward.ResultByteSamples, reversed.ResultByteSamples)
	}
	want := []int64{5000, 6000, 90000, 300000, 400000}
	if !reflect.DeepEqual(forward.ResultByteSamples, want) {
		t.Errorf("ResultByteSamples = %v, want %v (sorted ascending)", forward.ResultByteSamples, want)
	}
}

// TestChaos4313_ResponderTransportMustAgreeAcrossShards mirrors
// TestChaos4135_ResponderModelMustAgreeAcrossShards' own pattern for the
// SAME reason: ResponderTransport is a launch-level fact -- one transport
// (api|codex) answers a whole run -- so two shards disagreeing about it
// means artifacts from two different launches (or an operator changing
// ACR_TEST_TRIAL_RESPONDER_TRANSPORT mid-run) are being merged into one,
// which mergeReports' own "Provenance: first.Provenance" (inheriting only
// the FIRST shard's value) would otherwise silently misattribute.
func TestChaos4313_ResponderTransportMustAgreeAcrossShards(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	a.Provenance.ResponderTransport = "api"
	b := shardWithCases(t, 1, 2, []int{1, 3})
	b.Provenance.ResponderTransport = "codex"
	dir, paths := writeShards(t, []twoTurnReport{a, b})
	var stdout bytes.Buffer
	err := run(filepath.Join(dir, "merged.json"), paths, &stdout)
	if err == nil {
		t.Fatal("merging shards that disagree about responder_transport must be refused")
	}
	if !strings.Contains(err.Error(), "provenance.responder_transport") {
		t.Errorf("error = %q, want it to name provenance.responder_transport", err.Error())
	}
}

// TestChaos4313_ResponderEffortMustAgreeAcrossShards mirrors
// TestChaos4313_ResponderTransportMustAgreeAcrossShards' own pattern for the
// SAME reason: ResponderEffort is a launch-level fact -- one reasoning-
// effort tier answers a whole run -- so two shards disagreeing about it
// means artifacts from two different launches (or an operator changing
// ACR_TEST_TRIAL_RESPONDER_EFFORT mid-run) are being merged into one.
func TestChaos4313_ResponderEffortMustAgreeAcrossShards(t *testing.T) {
	a := shardWithCases(t, 0, 2, []int{0, 2})
	a.Provenance.ResponderEffort = "medium"
	b := shardWithCases(t, 1, 2, []int{1, 3})
	b.Provenance.ResponderEffort = "xhigh"
	dir, paths := writeShards(t, []twoTurnReport{a, b})
	var stdout bytes.Buffer
	err := run(filepath.Join(dir, "merged.json"), paths, &stdout)
	if err == nil {
		t.Fatal("merging shards that disagree about responder_effort must be refused")
	}
	if !strings.Contains(err.Error(), "provenance.responder_effort") {
		t.Errorf("error = %q, want it to name provenance.responder_effort", err.Error())
	}
}
