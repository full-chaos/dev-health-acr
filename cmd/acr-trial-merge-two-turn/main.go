// Command acr-trial-merge-two-turn merges N per-shard
// TestChaos3742TwoTurnConfirmationReplay artifacts (CHAOS-4033) into one
// run-level artifact, then re-applies every non-vacuity/coverage gate the
// per-shard test itself SKIPS when sharded (see the "sharded" bool in
// internal/runtime/hosted/chaos3742_two_turn_confirmation_test.go) over the
// UNION of every shard's data -- this merge is the ONLY place those gates
// get evaluated for a parallel run. A single sharded process's own artifact
// is never standalone valid evidence; this tool's own exit code is.
//
// Usage: acr-trial-merge-two-turn -out <merged.json> <shard1.json> [shard2.json ...]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
)

// expectedSchemaVersion pins this tool to the exact twoTurnReport shape it
// knows how to merge. Bump ONLY together with a matching bump to the
// mirrored structs below -- ReportSchemaVersion's own doc comment
// (chaos3742_two_turn_confirmation_test.go) is the authority on what
// changed; a _test.go type cannot be imported by a regular package, so this
// file is a hand-maintained mirror, not a shared definition.
const expectedSchemaVersion = "6"

type trialCommitGateProvenance struct {
	LoneFloorEnv                   string `json:"lone_floor_env,omitempty"`
	TopFloorEnv                    string `json:"top_floor_env,omitempty"`
	TopGapEnv                      string `json:"top_gap_env,omitempty"`
	VectorMarginCommitThresholdEnv string `json:"vector_margin_commit_threshold_env,omitempty"`
}

type trialProvenance struct {
	CorpusSHA256          string                    `json:"corpus_sha256"`
	Model                 string                    `json:"model,omitempty"`
	ModelFallback         string                    `json:"model_fallback,omitempty"`
	Transport             string                    `json:"transport"`
	ExchangeModelName     string                    `json:"exchange_model_name,omitempty"`
	ExchangeSessionID     string                    `json:"exchange_session_id,omitempty"`
	SourceCommit          string                    `json:"source_commit"`
	SourceDirty           bool                      `json:"source_dirty"`
	SourceDiffDigest      string                    `json:"source_diff_digest,omitempty"`
	RunStartedAt          string                    `json:"run_started_at"`
	ExecutionShape        string                    `json:"execution_shape,omitempty"`
	ShardIndex            *int                      `json:"shard_index,omitempty"`
	ShardCount            *int                      `json:"shard_count,omitempty"`
	ControlsContinue      bool                      `json:"controls_continue"`
	ResolvedActiveEpoch   int64                     `json:"resolved_active_epoch"`
	GraphLifecycleEnabled bool                      `json:"graph_lifecycle_enabled"`
	CommitGate            trialCommitGateProvenance `json:"commit_gate"`
	CostMethodology       string                    `json:"cost_methodology,omitempty"`
	SandboxMode           string                    `json:"sandbox_mode,omitempty"`
}

type twoTurnCaseResult struct {
	Index                  int    `json:"index"`
	Member                 string `json:"member"`
	Arm                    string `json:"arm"`
	Turn1Status            string `json:"turn1_status"`
	Turn2Status            string `json:"turn2_status"`
	OfferMiss              bool   `json:"offer_miss"`
	Applied                bool   `json:"applied"`
	Reused                 bool   `json:"reused"`
	TierRoutedCorrectly    bool   `json:"tier_routed_correctly,omitempty"`
	InferredClassification string `json:"inferred_classification,omitempty"`
	PairInvalid            bool   `json:"pair_invalid,omitempty"`
	FalseNoMatch           bool   `json:"false_no_match,omitempty"`
	CommittedCount         int    `json:"committed_count"`
	WrongCommit            bool   `json:"wrong_commit"`
	MutationProbe          string `json:"mutation_probe,omitempty"`
	MutationTripped        bool   `json:"mutation_tripped,omitempty"`
	ArmInvalidReason       string `json:"arm_invalid_reason,omitempty"`
}

const twoTurnArmInferredTier = "inferred_tier"

type twoTurnReport struct {
	ReportSchemaVersion                     string              `json:"report_schema_version"`
	Provenance                              trialProvenance     `json:"provenance"`
	BaseSHA                                 string              `json:"base_sha"`
	OracleAnnexPath                         string              `json:"oracle_annex_path"`
	OracleAnnexCorpusSHA                    string              `json:"oracle_annex_corpus_sha256"`
	OracleAnnexSignedOff                    bool                `json:"oracle_annex_signed_off"`
	CasesRun                                int                 `json:"cases_run"`
	PositiveAppliedCount                    int                 `json:"positive_applied_count"`
	WindowPositiveAppliedCount              int                 `json:"window_positive_applied_count"`
	GateReachableCount                      int                 `json:"gate_reachable_count"`
	StructureAndWindowDisclosureAbsentCount int                 `json:"structure_and_window_disclosure_absent_count"`
	OfferMissCount                          map[string]int      `json:"offer_miss_count"`
	WrongCommitCount                        int                 `json:"wrong_commit_count"`
	FalseNoMatchCount                       int                 `json:"false_no_match_count"`
	WindowCommitCount                       int                 `json:"window_commit_count"`
	WindowInferredTierRanCount              int                 `json:"window_inferred_tier_ran_count"`
	WindowArmErrorCount                     int                 `json:"window_arm_error_count"`
	WindowGatedCount                        int                 `json:"window_gated_count"`
	WindowClassDefaultGatedCount            int                 `json:"window_class_default_gated_count"`
	InferredKindHandleDecisiveCount         int                 `json:"inferred_kind_handle_decisive_count"`
	InferredBaselineEquivalentCount         int                 `json:"inferred_baseline_equivalent_count"`
	InferredKindInsensitivityAttestedCount  int                 `json:"inferred_kind_insensitivity_attested_count"`
	InferredUnjustifiedCount                int                 `json:"inferred_unjustified_count"`
	InferredPairInvalidCount                int                 `json:"inferred_pair_invalid_count"`
	ConfirmedWrongRedeemedCount             map[string]int      `json:"confirmed_wrong_redeemed_committable_count"`
	ApplicableMembers                       []string            `json:"applicable_members"`
	AntiVacuityValid                        bool                `json:"anti_vacuity_valid"`
	AnchorAntiVacuityDenominator            int                 `json:"anchor_anti_vacuity_denominator"`
	AnchorNonEnumerableKindExcludedCount    int                 `json:"anchor_non_enumerable_kind_excluded_count"`
	MutationProbesTripped                   map[string]int      `json:"mutation_probes_tripped"`
	MutationProbesRun                       map[string]int      `json:"mutation_probes_run"`
	ControlsTotal                           int                 `json:"controls_total"`
	ControlsWitnessed                       int                 `json:"controls_witnessed"`
	ControlsWitnessedNoMatchCensusBacked    int                 `json:"controls_witnessed_no_match_census_backed"`
	Results                                 []twoTurnCaseResult `json:"results"`
}

func main() {
	out := flag.String("out", "", "path to write the merged artifact (refuses to overwrite an existing file)")
	flag.Parse()
	shardPaths := flag.Args()
	if *out == "" || len(shardPaths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: acr-trial-merge-two-turn -out <merged.json> <shard1.json> [shard2.json ...]")
		os.Exit(2)
	}
	if err := run(*out, shardPaths, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "acr-trial-merge-two-turn: %v\n", err)
		os.Exit(1)
	}
}

func run(outPath string, shardPaths []string, stdout io.Writer) error {
	shards := make([]twoTurnReport, 0, len(shardPaths))
	for _, p := range shardPaths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		var r twoTurnReport
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		if r.ReportSchemaVersion != expectedSchemaVersion {
			return fmt.Errorf("%s: report_schema_version=%q, want %q -- this merge tool is a hand-maintained mirror of twoTurnReport and must be updated in lockstep with any future schema bump before merging a newer artifact", p, r.ReportSchemaVersion, expectedSchemaVersion)
		}
		if r.Provenance.ExecutionShape != "parallel" {
			return fmt.Errorf("%s: provenance.execution_shape=%q, want \"parallel\" -- refusing to merge a sequential (unsharded) artifact", p, r.Provenance.ExecutionShape)
		}
		if r.Provenance.ShardIndex == nil || r.Provenance.ShardCount == nil {
			return fmt.Errorf("%s: missing shard_index/shard_count despite execution_shape=parallel", p)
		}
		shards = append(shards, r)
	}

	if err := validateShardSet(shards, shardPaths); err != nil {
		return err
	}
	if err := validateResultUniqueness(shards, shardPaths); err != nil {
		return err
	}

	merged := mergeReports(shards)

	rawOut, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged report: %w", err)
	}
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w (refusing to overwrite an existing artifact)", outPath, err)
	}
	if _, err := f.Write(rawOut); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", outPath, err)
	}

	if gateErr := evaluateGates(merged); gateErr != nil {
		fmt.Fprintf(stdout, "merged report written to %s -- INVALID:\n%v\n", outPath, gateErr)
		return gateErr
	}
	fmt.Fprintf(stdout, "merged report written to %s: cases_run=%d shards=%d wrong_commits=%d anti_vacuity_valid=%v VALID\n",
		outPath, merged.CasesRun, len(shards), merged.WrongCommitCount, merged.AntiVacuityValid)
	return nil
}

// validateShardSet checks the shard set is complete (every index 0..N-1
// present exactly once) and that every shard came from the SAME run
// (identical corpus/annex/source identity) before any merge arithmetic
// runs -- merging shards from different runs would silently produce a
// meaningless artifact.
func validateShardSet(shards []twoTurnReport, paths []string) error {
	if len(shards) == 0 {
		return fmt.Errorf("no shard artifacts given")
	}
	wantCount := *shards[0].Provenance.ShardCount
	seenIndex := map[int]string{}
	for i, s := range shards {
		if got := *s.Provenance.ShardCount; got != wantCount {
			return fmt.Errorf("%s: shard_count=%d, want %d (every shard artifact in one merge must share the same shard_count)", paths[i], got, wantCount)
		}
		idx := *s.Provenance.ShardIndex
		if idx < 0 || idx >= wantCount {
			return fmt.Errorf("%s: shard_index=%d out of range [0,%d)", paths[i], idx, wantCount)
		}
		if prior, dup := seenIndex[idx]; dup {
			return fmt.Errorf("%s and %s both claim shard_index=%d -- refusing to merge a duplicate/ambiguous shard set", prior, paths[i], idx)
		}
		seenIndex[idx] = paths[i]
	}
	if len(shards) != wantCount {
		var missing []int
		for i := 0; i < wantCount; i++ {
			if _, ok := seenIndex[i]; !ok {
				missing = append(missing, i)
			}
		}
		return fmt.Errorf("shard_count=%d but %d artifact(s) given, missing shard_index %v -- refusing a partial merge (a partial union under-counts coverage and could pass gates it should not)", wantCount, len(shards), missing)
	}

	first := shards[0]
	for i := 1; i < len(shards); i++ {
		s := shards[i]
		switch {
		case s.Provenance.CorpusSHA256 != first.Provenance.CorpusSHA256:
			return mismatchErr(paths[i], paths[0], "provenance.corpus_sha256", s.Provenance.CorpusSHA256, first.Provenance.CorpusSHA256)
		case s.Provenance.Transport != first.Provenance.Transport:
			return mismatchErr(paths[i], paths[0], "provenance.transport", s.Provenance.Transport, first.Provenance.Transport)
		case s.Provenance.SourceCommit != first.Provenance.SourceCommit:
			return mismatchErr(paths[i], paths[0], "provenance.source_commit", s.Provenance.SourceCommit, first.Provenance.SourceCommit)
		case s.Provenance.SourceDirty != first.Provenance.SourceDirty:
			return mismatchErr(paths[i], paths[0], "provenance.source_dirty", fmt.Sprint(s.Provenance.SourceDirty), fmt.Sprint(first.Provenance.SourceDirty))
		case s.Provenance.SourceDiffDigest != first.Provenance.SourceDiffDigest:
			return mismatchErr(paths[i], paths[0], "provenance.source_diff_digest", s.Provenance.SourceDiffDigest, first.Provenance.SourceDiffDigest)
		case s.BaseSHA != first.BaseSHA:
			return mismatchErr(paths[i], paths[0], "base_sha", s.BaseSHA, first.BaseSHA)
		case s.OracleAnnexPath != first.OracleAnnexPath:
			return mismatchErr(paths[i], paths[0], "oracle_annex_path", s.OracleAnnexPath, first.OracleAnnexPath)
		case s.OracleAnnexCorpusSHA != first.OracleAnnexCorpusSHA:
			return mismatchErr(paths[i], paths[0], "oracle_annex_corpus_sha256", s.OracleAnnexCorpusSHA, first.OracleAnnexCorpusSHA)
		case s.OracleAnnexSignedOff != first.OracleAnnexSignedOff:
			return mismatchErr(paths[i], paths[0], "oracle_annex_signed_off", fmt.Sprint(s.OracleAnnexSignedOff), fmt.Sprint(first.OracleAnnexSignedOff))
		}
	}
	return nil
}

func mismatchErr(path, firstPath, field, got, want string) error {
	return fmt.Errorf("%s: %s=%q does not match %s's %q -- shards must come from the SAME run (same corpus, same annex, same source tree) to merge safely", path, field, got, firstPath, want)
}

// resultKey identifies one twoTurnCaseResult row uniquely. Index alone is
// NOT unique -- a single case legitimately produces up to 4 rows (one per
// twoTurnArm value: positive, inferred_tier, confirmed_wrong, mutation --
// chaos3742_two_turn_confirmation_test.go's 4 report.Results append sites),
// and the mutation arm itself appends one row PER PROBE it runs for that
// case (runTwoTurnMutationArm), so MutationProbe distinguishes those from
// each other too. Empty for the other three arms, which never repeat.
type resultKey struct {
	Index         int
	Arm           string
	MutationProbe string
}

// validateResultUniqueness (codex round-3 finding, MEDIUM): validateShardSet
// checks shard_index/shard_count HEADERS are complete and consistent, but
// never checked the Results PAYLOAD actually partitions the corpus
// disjointly -- a stale artifact, a re-run shard reusing an old
// shard_index, or a future bug in the round-robin selection could silently
// double-count a case's results into the merged sums/gates without this.
// Checked across ALL shards together (not per-shard) so a duplicate
// spanning two different shard files is caught, not just one repeated
// within a single file.
func validateResultUniqueness(shards []twoTurnReport, paths []string) error {
	seen := map[resultKey]string{}
	for i, s := range shards {
		for _, r := range s.Results {
			k := resultKey{Index: r.Index, Arm: r.Arm, MutationProbe: r.MutationProbe}
			if prior, dup := seen[k]; dup {
				return fmt.Errorf("duplicate result for case index=%d arm=%q mutation_probe=%q: reported by both %s and %s -- shards must partition the corpus disjointly; refusing to merge an overlapping or relabeled shard set", k.Index, k.Arm, k.MutationProbe, prior, paths[i])
			}
			seen[k] = paths[i]
		}
	}
	return nil
}

// mergeReports combines every shard's counters/maps/sets into one artifact
// representing their union. See each field's own comment in
// chaos3742_two_turn_confirmation_test.go for what it means; the merge rule
// here is: scalar counts sum, map[string]int counts sum per key,
// ApplicableMembers unions, Results concatenates, and AntiVacuityValid is
// recomputed from the MERGED ApplicableMembers/ConfirmedWrongRedeemedCount
// (never trusted from any one shard -- a shard sharded by corpus case can
// legitimately see zero cases for a member the corpus assigns elsewhere).
func mergeReports(shards []twoTurnReport) twoTurnReport {
	first := shards[0]
	merged := twoTurnReport{
		ReportSchemaVersion:         first.ReportSchemaVersion,
		Provenance:                  first.Provenance,
		BaseSHA:                     first.BaseSHA,
		OracleAnnexPath:             first.OracleAnnexPath,
		OracleAnnexCorpusSHA:        first.OracleAnnexCorpusSHA,
		OracleAnnexSignedOff:        first.OracleAnnexSignedOff,
		OfferMissCount:              map[string]int{},
		ConfirmedWrongRedeemedCount: map[string]int{},
		MutationProbesTripped:       map[string]int{},
		MutationProbesRun:           map[string]int{},
	}
	// The merged artifact represents the UNION of every shard, not any one
	// shard -- never mistakable for a single shard's own report.
	merged.Provenance.ShardIndex = nil
	merged.Provenance.ShardCount = nil
	earliest := first.Provenance.RunStartedAt
	for _, s := range shards[1:] {
		if s.Provenance.RunStartedAt < earliest {
			earliest = s.Provenance.RunStartedAt
		}
	}
	merged.Provenance.RunStartedAt = earliest

	applicableSeen := map[string]bool{}
	for _, s := range shards {
		merged.CasesRun += s.CasesRun
		merged.PositiveAppliedCount += s.PositiveAppliedCount
		merged.WindowPositiveAppliedCount += s.WindowPositiveAppliedCount
		merged.GateReachableCount += s.GateReachableCount
		merged.StructureAndWindowDisclosureAbsentCount += s.StructureAndWindowDisclosureAbsentCount
		merged.WrongCommitCount += s.WrongCommitCount
		merged.FalseNoMatchCount += s.FalseNoMatchCount
		merged.WindowCommitCount += s.WindowCommitCount
		merged.WindowInferredTierRanCount += s.WindowInferredTierRanCount
		merged.WindowArmErrorCount += s.WindowArmErrorCount
		merged.WindowGatedCount += s.WindowGatedCount
		merged.WindowClassDefaultGatedCount += s.WindowClassDefaultGatedCount
		merged.InferredKindHandleDecisiveCount += s.InferredKindHandleDecisiveCount
		merged.InferredBaselineEquivalentCount += s.InferredBaselineEquivalentCount
		merged.InferredKindInsensitivityAttestedCount += s.InferredKindInsensitivityAttestedCount
		merged.InferredUnjustifiedCount += s.InferredUnjustifiedCount
		merged.InferredPairInvalidCount += s.InferredPairInvalidCount
		merged.AnchorAntiVacuityDenominator += s.AnchorAntiVacuityDenominator
		merged.AnchorNonEnumerableKindExcludedCount += s.AnchorNonEnumerableKindExcludedCount
		merged.ControlsTotal += s.ControlsTotal
		merged.ControlsWitnessed += s.ControlsWitnessed
		merged.ControlsWitnessedNoMatchCensusBacked += s.ControlsWitnessedNoMatchCensusBacked

		for k, v := range s.OfferMissCount {
			merged.OfferMissCount[k] += v
		}
		for k, v := range s.ConfirmedWrongRedeemedCount {
			merged.ConfirmedWrongRedeemedCount[k] += v
		}
		for k, v := range s.MutationProbesTripped {
			merged.MutationProbesTripped[k] += v
		}
		for k, v := range s.MutationProbesRun {
			merged.MutationProbesRun[k] += v
		}
		for _, m := range s.ApplicableMembers {
			applicableSeen[m] = true
		}
		merged.Results = append(merged.Results, s.Results...)
	}

	merged.ApplicableMembers = make([]string, 0, len(applicableSeen))
	for m := range applicableSeen {
		merged.ApplicableMembers = append(merged.ApplicableMembers, m)
	}
	sort.Strings(merged.ApplicableMembers)

	unsatisfied := 0
	for _, m := range merged.ApplicableMembers {
		if merged.ConfirmedWrongRedeemedCount[m] < 1 {
			unsatisfied++
		}
	}
	merged.AntiVacuityValid = len(merged.ApplicableMembers) > 0 && unsatisfied == 0

	return merged
}

// evaluateGates reproduces every assertion
// TestChaos3742TwoTurnConfirmationReplay's own final block applies,
// checking the coverage/non-vacuity gates (skipped per shard) for the
// FIRST time here, and re-checking the correctness gates (already
// unconditional per shard) as a cheap redundant guard against a merge bug.
// This function's own error IS the authoritative pass/fail signal for a
// parallel run -- no per-shard go test exit code is.
func evaluateGates(r twoTurnReport) error {
	var failures []string
	fail := func(format string, args ...interface{}) {
		failures = append(failures, fmt.Sprintf(format, args...))
	}

	if r.PositiveAppliedCount == 0 {
		fail("positive_applied_count=0 across %d merged cases -- the positive arm never converted a single case, so this run proves nothing about conversion", r.CasesRun)
	}
	if !r.AntiVacuityValid {
		var unsatisfied []string
		for _, m := range r.ApplicableMembers {
			if r.ConfirmedWrongRedeemedCount[m] < 1 {
				unsatisfied = append(unsatisfied, m)
			}
		}
		fail("confirmed_wrong arm anti-vacuity check failed across the merged shards: members %v redeemed zero designated committable negatives", unsatisfied)
	}
	if r.WindowInferredTierRanCount == 0 {
		fail("window_inferred_tier_ran_count=0 across %d merged cases -- window inferred-tier arm never once completed", r.CasesRun)
	}
	if r.WindowClassDefaultGatedCount == 0 {
		fail("window_class_default_gated_count=0 across %d merged cases -- gate 2 never fired once", r.CasesRun)
	}
	if r.WindowPositiveAppliedCount == 0 {
		fail("window_positive_applied_count=0 across %d merged cases -- window's own receipt redemption never once reached a confirmed decisive answer", r.CasesRun)
	}
	if r.InferredKindHandleDecisiveCount == 0 {
		fail("inferred_kind_handle_decisive_count=0 -- kind/handle inferred-tier never committed a single case across the merged shards")
	}
	if r.ControlsTotal == 0 {
		fail("controls_total=0: the merged run recorded NO control cases -- D0 cannot be reported")
	}

	if r.WrongCommitCount > 0 {
		fail("wrong_commit_count=%d, want 0 (DP9: ZERO wrong commits, period)", r.WrongCommitCount)
	}
	if r.WindowCommitCount > 0 {
		fail("window_commit_count=%d, want 0", r.WindowCommitCount)
	}
	if r.WindowGatedCount != r.WindowInferredTierRanCount {
		fail("window_gated_count=%d, want %d (== window_inferred_tier_ran_count)", r.WindowGatedCount, r.WindowInferredTierRanCount)
	}
	if r.FalseNoMatchCount > 0 {
		fail("false_no_match_count=%d, want 0", r.FalseNoMatchCount)
	}
	if r.InferredPairInvalidCount > 0 {
		fail("inferred_pair_invalid_count=%d, want 0", r.InferredPairInvalidCount)
	}
	if r.InferredUnjustifiedCount > 0 {
		fail("inferred_unjustified_count=%d, want 0", r.InferredUnjustifiedCount)
	}
	for probe, ran := range r.MutationProbesRun {
		if tripped := r.MutationProbesTripped[probe]; tripped != ran {
			fail("mutation probe %q tripped %d/%d attempts, want %d/%d", probe, tripped, ran, ran, ran)
		}
	}
	if r.ControlsTotal > 0 && r.ControlsWitnessed < r.ControlsTotal {
		fail("controls_witnessed=%d/%d, want %d/%d", r.ControlsWitnessed, r.ControlsTotal, r.ControlsTotal, r.ControlsTotal)
	}
	for _, res := range r.Results {
		if res.Arm == twoTurnArmInferredTier && res.ArmInvalidReason == "" && !res.TierRoutedCorrectly {
			fail("case %d member %q: inferred-tier injection did not route to explicit_unattributed/inferred_default in the echo", res.Index, res.Member)
		}
	}

	if len(failures) == 0 {
		return nil
	}
	msg := ""
	for _, f := range failures {
		msg += "  - " + f + "\n"
	}
	return fmt.Errorf("%s", msg)
}
