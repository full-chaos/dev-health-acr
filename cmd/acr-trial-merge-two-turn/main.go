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
// file is a hand-maintained mirror, not a shared definition. "10"
// (CHAOS-4079 write-free shadow-probe observability): twoTurnCaseResult
// gained ShadowKindInsensitivityMode, and the v9 note below about the
// zero-overlap wiring gap is RESOLVED -- the probe now evaluates (write-free,
// deriving its verdict from census results the round already collected
// rather than issuing a second live census read) for a wrong-kind hint, so
// ShadowKindInsensitivityEvaluated/Outcome populate on rows where they were
// structurally always false/absent at v9. That is a MEANING change for two
// existing keys, not merely an added one, which is why it bumps rather than
// riding along as additive passthrough. The measurement's own pass condition
// is unchanged: kind_insensitivity_attested still requires mode=="narrowed",
// so no row's InferredClassification moves as a result. Purely additive
// passthrough for THIS tool regardless -- no merge arithmetic, since Results
// already concatenates across shards. "9"
// (CHAOS-4039 v5 measurement-contract correction, team-lead ruling
// 2026-08-22): InferredClassification's own baseline_equivalent definition
// changed MEANING (no new/removed wire field -- this mirror's
// twoTurnCaseResult/twoTurnReport shape is byte-identical to v8) from
// bit-for-bit equality of two model-derived hashes to engine-deterministic
// decision-state equivalence -- see chaos3742_two_turn_confirmation_test.go's
// own ReportSchemaVersion doc comment for the full mechanism (including why
// the CHAOS-4062 shadow kind-insensitivity probe's own zero-overlap wiring
// gap was NOT part of this bump -- that first fix was drafted and rejected
// on adversarial review; the write-free construction landed at "10"). Bumped purely
// so a v8 artifact (measured under the old, unsatisfiable definition) can
// never be merged as if it were directly comparable to a v9 one. "8"
// (CHAOS-4062): twoTurnCaseResult gained ShadowKindInsensitivityEvaluated/
// ShadowKindInsensitivityOutcome and BaselineCommittedSubjects/
// HintedCommittedSubjects (see twoTurnSubjectKindID below) -- per-case
// observational fields only, populated for the "unjustified"
// InferredClassification outcome alone. Purely additive passthrough: no
// merge arithmetic needed, since Results already concatenates across
// shards (mergeReports) and every field on the row rides along with it.
// "7" (CHAOS-4058): Timings/TimingSummary are new, purely additive fields --
// see twoTurnArmTiming/twoTurnCaseTiming/twoTurnArmTimingSummary below and
// mergeReports' own handling of them.
// "11" (CHAOS-4086): twoTurnCaseResult gained eleven instant-diagnosis
// fields -- CommittedSubjects/ExpectedKind/ExpectedID on every arm,
// CommitGate/TiedStatisticalTop/SearchTruncated off the decision trace, the
// three KindCoverage* fields off the new kind_coverage_floor trace stage, and
// ArmInvalidStage/ArmInvalidErrorType beside ArmInvalidReason. Purely
// additive per-row keys: no merge arithmetic changes, because Results
// concatenates verbatim and every field on the row rides along with it. The
// mirror below still had to be updated in the same change -- a field this
// struct does not declare is dropped on decode, which would delete the
// diagnosis from the merged artifact while every count still agreed.
// "12" (CHAOS-4100): trialProvenance gained the `sharding` block --
// case_indices, granularity, concurrency_cap, provisioning_mode and
// database_provision_millis. Purely additive provenance; no merge
// arithmetic changes and no gate reads it. The mirror below still had to
// gain it in the same change: a field this struct does not declare is
// dropped on decode, which would erase how the run was fanned out from
// every merged artifact while the per-shard artifacts still carried it.
// "13" (CHAOS-4103): twoTurnCaseResult gained the SynthesisStatusOverride
// Fired/From/To/Reason/CommittedCount block (see the producer's own
// ReportSchemaVersion doc comment), and twoTurnReport gained
// SynthesisStatusOverrideUncommittedCount -- summed across shards by
// mergeReports below, the SAME `+=` arithmetic WrongCommitCount/
// FalseNoMatchCount already get, and gated by the SAME zero-tolerance fail()
// check as those two (a blocking defect signal, not observational). The
// per-row block is purely additive passthrough (Results concatenates
// verbatim); the mirror still had to gain it in the same change for the
// SAME reason "11"/"12" did -- an undeclared field is dropped on decode.
// "14" (CHAOS-4120): three changes, all requiring the bump. (a)
// FalseNoMatch/FalseNoMatchCount widen from inferred_tier-only to ALSO
// cover the positive arm -- a MEANING change for an existing key (see the
// producer's own ReportSchemaVersion doc comment); FalseNoMatchCount's own
// `+=` merge arithmetic below is UNCHANGED, since it already sums the raw
// per-shard count regardless of which arm produced it. (b)
// twoTurnCaseResult gained the CHAOS-4120 turn-1 facts block (Turn1CommitGate/
// Turn1TiedStatisticalTop/Turn1SearchTruncated, the three Turn1KindCoverage*
// fields, Turn1TermSearchTruncated/Turn1QuestionSearchTruncated,
// ExpectedInPool, AnchorOptionsCount, HandleOptionsCount, CensusRan,
// CensusComplete, CensusCount) -- purely additive passthrough, no merge
// arithmetic. (c)
// twoTurnCaseResult also gained Regime, stamped from the engine's own
// RecordWindowCanonicalization telemetry (see the producer's own doc
// comment on twoTurnTurn1Facts.Regime) -- also purely additive passthrough.
// For the SAME "an undeclared field is dropped on decode" reason
// "11"/"12"/"13" each needed the mirror updated in the same change.
// "15" (CHAOS-4135, follow-up from the shard-54 CHAOS-4117 diagnosis,
// 2026-08-22, team-lead ruling "D-plus"): twoTurnCaseResult gained
// TraceEvents/BaselineTraceEvents -- the harness's already-captured
// per-call ResolutionTraceEvent stream, persisted for anomalous rows only
// (see the producer's own doc comment and twoTurnCaseResultIsAnomalous).
// Purely additive passthrough per row (Results concatenates verbatim, no
// merge arithmetic), mirrored here as json.RawMessage rather than a full
// struct -- see the field's own doc comment below for why. The mirror
// still had to gain it in the same change for the SAME "an undeclared
// field is dropped on decode" reason every prior bump needed it for.
// Rides the SAME v15 bump (scope addition, team-lead, 2026-08-22, from
// lane-4118's CHAOS-4113 scoping): trialProvenance also gained
// ResponderModel -- see that field's own doc comment in the producer
// (generative_trial_live_test.go) for the provenance gap it closes.
// Purely additive; no merge arithmetic (Provenance rides through from the
// FIRST shard, see mergeReports, and every shard of one run shares one
// responder model by construction) -- mirrored here for the same
// undeclared-field-dropped-on-decode reason as everything else on this
// list.
const expectedSchemaVersion = "15"

// trialShardingProvenance mirrors the producer's identically-named type
// (CHAOS-4100, schema v12): how a run was fanned out. Corpus-safe -- case
// indices, counts, milliseconds and a closed provisioning label.
type trialShardingProvenance struct {
	CaseIndices             []int  `json:"case_indices,omitempty"`
	Granularity             int    `json:"granularity,omitempty"`
	ConcurrencyCap          int    `json:"concurrency_cap,omitempty"`
	ProvisioningMode        string `json:"provisioning_mode,omitempty"`
	DatabaseProvisionMillis int64  `json:"database_provision_millis,omitempty"`
}

type trialCommitGateProvenance struct {
	LoneFloorEnv                   string `json:"lone_floor_env,omitempty"`
	TopFloorEnv                    string `json:"top_floor_env,omitempty"`
	TopGapEnv                      string `json:"top_gap_env,omitempty"`
	VectorMarginCommitThresholdEnv string `json:"vector_margin_commit_threshold_env,omitempty"`
}

type trialProvenance struct {
	CorpusSHA256      string `json:"corpus_sha256"`
	Model             string `json:"model,omitempty"`
	ModelFallback     string `json:"model_fallback,omitempty"`
	Transport         string `json:"transport"`
	ExchangeModelName string `json:"exchange_model_name,omitempty"`
	ExchangeSessionID string `json:"exchange_session_id,omitempty"`
	SourceCommit      string `json:"source_commit"`
	SourceDirty       bool   `json:"source_dirty"`
	SourceDiffDigest  string `json:"source_diff_digest,omitempty"`
	RunStartedAt      string `json:"run_started_at"`
	ExecutionShape    string `json:"execution_shape,omitempty"`
	ShardIndex        *int   `json:"shard_index,omitempty"`
	ShardCount        *int   `json:"shard_count,omitempty"`
	// Sharding (CHAOS-4100) mirrors trialProvenance's identically-named
	// block. See the producer's own doc comments for what each field
	// means; the tags below are the contract.
	Sharding              trialShardingProvenance `json:"sharding"`
	ControlsContinue      bool                    `json:"controls_continue"`
	ResolvedActiveEpoch   int64                   `json:"resolved_active_epoch"`
	GraphLifecycleEnabled bool                    `json:"graph_lifecycle_enabled"`
	// AnchorMembershipOffersEnabled (codex round-3 finding, pre-existing
	// mirror drift unrelated to CHAOS-4058's own timing fields, fixed here
	// while already touching this struct for the schema bump): the
	// producer's trialProvenance (generative_trial_live_test.go) has
	// carried this field since CHAOS-3742 RUN 3 -- without it here,
	// json.Unmarshal silently dropped a sharded run's own anchor-membership
	// provenance on every merge.
	AnchorMembershipOffersEnabled bool                      `json:"anchor_membership_offers_enabled"`
	CommitGate                    trialCommitGateProvenance `json:"commit_gate"`
	CostMethodology               string                    `json:"cost_methodology,omitempty"`
	SandboxMode                   string                    `json:"sandbox_mode,omitempty"`
	// ResponderModel (CHAOS-4135, schema v15) mirrors trialProvenance's
	// identically-named field -- see the producer's own doc comment
	// (generative_trial_live_test.go) for the provenance gap this closes.
	// Purely additive passthrough; no merge arithmetic (Provenance rides
	// through from the FIRST shard, see mergeReports, and every shard of
	// one run shares one responder model by construction).
	ResponderModel string `json:"responder_model,omitempty"`
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
	// ShadowKindInsensitivityEvaluated/ShadowKindInsensitivityOutcome and
	// BaselineCommittedSubjects/HintedCommittedSubjects (CHAOS-4062,
	// schema v8) mirror twoTurnCaseResult's identically-named fields in
	// chaos3742_two_turn_confirmation_test.go byte-for-byte -- see that
	// file's own doc comments. Populated ONLY for the "unjustified"
	// InferredClassification outcome; observational passthrough, no merge
	// arithmetic (this whole row concatenates across shards as-is).
	ShadowKindInsensitivityEvaluated bool   `json:"shadow_kind_insensitivity_evaluated,omitempty"`
	ShadowKindInsensitivityOutcome   string `json:"shadow_kind_insensitivity_outcome,omitempty"`
	// ShadowKindInsensitivityMode (CHAOS-4079, schema v10) mirrors
	// twoTurnCaseResult's identically-named field: which explicit-kind
	// narrowing situation produced the two fields above ("narrowed" |
	// "observed_no_overlap" | "observed_subsumed"). A consumer must read it
	// alongside them -- only "narrowed" means the verdict attests
	// insensitivity across an actual change to the census hypothesis set.
	ShadowKindInsensitivityMode string                 `json:"shadow_kind_insensitivity_mode,omitempty"`
	BaselineCommittedSubjects   []twoTurnSubjectKindID `json:"baseline_committed_subjects,omitempty"`
	HintedCommittedSubjects     []twoTurnSubjectKindID `json:"hinted_committed_subjects,omitempty"`
	// TraceEvents/BaselineTraceEvents (CHAOS-4135, schema v15) mirror
	// twoTurnCaseResult's identically-named fields -- populated by the
	// producer ONLY on anomalous rows (see that file's own doc comment and
	// twoTurnCaseResultIsAnomalous). json.RawMessage, deliberately NOT a
	// full mirror of graphrank.ResolutionTraceEvent: this tool never reads
	// or computes anything from trace-event content (no gate, no merge
	// arithmetic touches it), so mirroring that still-growing struct here
	// would create a maintenance coupling this tool does not need -- every
	// future field the graphrank package adds would otherwise force a
	// matching edit here for a type this tool only ever passes through
	// unread. RawMessage preserves whatever the producer wrote byte-for-byte
	// regardless, the same "concatenates verbatim, no arithmetic" treatment
	// every other purely-observational block on this row already gets.
	TraceEvents         json.RawMessage `json:"trace_events,omitempty"`
	BaselineTraceEvents json.RawMessage `json:"baseline_trace_events,omitempty"`
	// CHAOS-4086 (schema v11) instant-diagnosis fields, mirroring
	// twoTurnCaseResult's identically-named block. Rows ride through
	// mergeReports verbatim, so these need no merge arithmetic -- but they
	// MUST be declared here anyway: an absent field is silently dropped by
	// json.Unmarshal and the merged artifact would lose exactly the
	// diagnosis this schema bump exists to carry. That has happened before
	// on this mirror (trialProvenance.AnchorMembershipOffersEnabled).
	//
	// See twoTurnCaseResult's own block for what each one means and why it
	// is corpus-safe; the tags below are the contract.
	CommittedSubjects          []twoTurnSubjectKindID `json:"committed_subjects,omitempty"`
	ExpectedKind               string                 `json:"expected_kind,omitempty"`
	ExpectedID                 string                 `json:"expected_id,omitempty"`
	CommitGate                 string                 `json:"commit_gate,omitempty"`
	TiedStatisticalTop         bool                   `json:"tied_statistical_top,omitempty"`
	SearchTruncated            bool                   `json:"search_truncated,omitempty"`
	KindCoverageFloorFired     bool                   `json:"kind_coverage_floor_fired,omitempty"`
	KindCoverageMissingKinds   int                    `json:"kind_coverage_missing_kinds,omitempty"`
	KindCoverageFloorTruncated bool                   `json:"kind_coverage_floor_truncated,omitempty"`
	ArmInvalidStage            string                 `json:"arm_invalid_stage,omitempty"`
	ArmInvalidErrorType        string                 `json:"arm_invalid_error_type,omitempty"`
	// CHAOS-4103 (schema v13) synthesis-status override provenance, mirroring
	// twoTurnCaseResult's identically-named block byte-for-byte -- see that
	// file's own doc comment for what each field means and why
	// SynthesisStatusOverrideCommittedCount deliberately has no omitempty.
	// Purely additive passthrough: Results concatenates verbatim across
	// shards, so no merge arithmetic changes for this block itself.
	SynthesisStatusOverrideFired          bool   `json:"synthesis_status_override_fired"`
	SynthesisStatusOverrideFrom           string `json:"synthesis_status_override_from,omitempty"`
	SynthesisStatusOverrideTo             string `json:"synthesis_status_override_to,omitempty"`
	SynthesisStatusOverrideReason         string `json:"synthesis_status_override_reason,omitempty"`
	SynthesisStatusOverrideCommittedCount int    `json:"synthesis_status_override_committed_count"`
	// CHAOS-4120 (schema v14) turn-1 facts, mirroring twoTurnCaseResult's
	// identically-named block byte-for-byte -- see that file's own doc
	// comment for what each field means and why they are separate,
	// Turn1-prefixed fields rather than reusing CommitGate/TiedStatisticalTop/
	// SearchTruncated/KindCoverage* above. Purely additive passthrough:
	// Results concatenates verbatim across shards, so no merge arithmetic
	// changes for this block itself.
	Turn1CommitGate                 string `json:"turn1_commit_gate,omitempty"`
	Turn1TiedStatisticalTop         bool   `json:"turn1_tied_statistical_top,omitempty"`
	Turn1SearchTruncated            bool   `json:"turn1_search_truncated,omitempty"`
	Turn1KindCoverageFloorFired     bool   `json:"turn1_kind_coverage_floor_fired,omitempty"`
	Turn1KindCoverageMissingKinds   int    `json:"turn1_kind_coverage_missing_kinds,omitempty"`
	Turn1KindCoverageFloorTruncated bool   `json:"turn1_kind_coverage_floor_truncated,omitempty"`
	Turn1TermSearchTruncated        bool   `json:"turn1_term_search_truncated,omitempty"`
	Turn1QuestionSearchTruncated    bool   `json:"turn1_question_search_truncated,omitempty"`
	ExpectedInPool                  bool   `json:"expected_in_pool"`
	AnchorOptionsCount              int    `json:"anchor_options_count"`
	HandleOptionsCount              int    `json:"handle_options_count"`
	CensusRan                       bool   `json:"census_ran"`
	CensusComplete                  bool   `json:"census_complete"`
	CensusCount                     int    `json:"census_count"`
	// Regime (CHAOS-4120) mirrors twoTurnCaseResult's identically-named
	// field -- see that file's own doc comment (twoTurnTurn1Facts.Regime)
	// for the engine-native derivation off RecordWindowCanonicalization.
	Regime string `json:"turn1_regime,omitempty"`
}

// twoTurnSubjectKindID mirrors chaos3742_two_turn_confirmation_test.go's
// identically-named type byte-for-byte (CHAOS-4062, schema v8): a committed
// subject's Kind+CanonicalID only, Label deliberately dropped (that file's
// own "no question/label text" discipline).
type twoTurnSubjectKindID struct {
	Kind        string `json:"kind"`
	CanonicalID string `json:"canonical_id"`
}

const twoTurnArmInferredTier = "inferred_tier"

// twoTurnArmTiming/twoTurnCaseTiming/twoTurnArmTimingSummary (CHAOS-4058)
// mirror the identically-named types in
// chaos3742_two_turn_confirmation_test.go byte-for-byte (JSON tags
// included) -- see that file's own doc comments for what each field means.
type twoTurnArmTiming struct {
	Arm                  string `json:"arm"`
	WallDurationMS       int64  `json:"wall_duration_ms"`
	ResponderCallCount   int    `json:"responder_call_count,omitempty"`
	ResponderCallTotalMS int64  `json:"responder_call_total_ms,omitempty"`
	ResponderCallMaxMS   int64  `json:"responder_call_max_ms,omitempty"`
}

type twoTurnCaseTiming struct {
	Index  int                `json:"index"`
	Member string             `json:"member"`
	Arms   []twoTurnArmTiming `json:"arms"`
}

type twoTurnArmTimingSummary struct {
	Arm                  string  `json:"arm"`
	SampleCount          int     `json:"sample_count"`
	WallMeanMS           float64 `json:"wall_mean_ms"`
	WallP50MS            int64   `json:"wall_p50_ms"`
	WallMaxMS            int64   `json:"wall_max_ms"`
	ResponderCallMaxMS   int64   `json:"responder_call_max_ms"`
	ResponderCallCount   int     `json:"responder_call_count"`
	ResponderCallTotalMS int64   `json:"responder_call_total_ms"`
}

type twoTurnReport struct {
	ReportSchemaVersion                     string          `json:"report_schema_version"`
	Provenance                              trialProvenance `json:"provenance"`
	BaseSHA                                 string          `json:"base_sha"`
	OracleAnnexPath                         string          `json:"oracle_annex_path"`
	OracleAnnexCorpusSHA                    string          `json:"oracle_annex_corpus_sha256"`
	OracleAnnexSignedOff                    bool            `json:"oracle_annex_signed_off"`
	CasesRun                                int             `json:"cases_run"`
	PositiveAppliedCount                    int             `json:"positive_applied_count"`
	WindowPositiveAppliedCount              int             `json:"window_positive_applied_count"`
	GateReachableCount                      int             `json:"gate_reachable_count"`
	StructureAndWindowDisclosureAbsentCount int             `json:"structure_and_window_disclosure_absent_count"`
	OfferMissCount                          map[string]int  `json:"offer_miss_count"`
	WrongCommitCount                        int             `json:"wrong_commit_count"`
	FalseNoMatchCount                       int             `json:"false_no_match_count"`
	// SynthesisStatusOverrideUncommittedCount (CHAOS-4103, schema v13) mirrors
	// twoTurnReport's identically-named field -- see that file's own doc
	// comment. A THIRD zero-tolerance bar alongside WrongCommitCount/
	// FalseNoMatchCount below.
	SynthesisStatusOverrideUncommittedCount int                 `json:"synthesis_status_override_uncommitted_count"`
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
	// Timings/TimingSummary (CHAOS-4058, purely additive, observational
	// only): Timings concatenates across shards exactly like Results
	// does (each shard's cases are disjoint); TimingSummary is never
	// trusted from any one shard and is always RECOMPUTED from the
	// merged Timings, mirroring AntiVacuityValid's own "never trust a
	// per-shard aggregate" discipline -- see mergeReports.
	Timings       []twoTurnCaseTiming       `json:"timings,omitempty"`
	TimingSummary []twoTurnArmTimingSummary `json:"timing_summary,omitempty"`
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
		// ResponderModel (CHAOS-4135, codex xhigh review, MEDIUM, confirmed):
		// mergeReports inherits Provenance from shards[0] alone (see below),
		// so without this check a mixed run -- shards launched with
		// different ACR_TEST_TRIAL_RESPONDER_MODEL values, or one with it
		// set and one without -- would silently merge under the FIRST
		// shard's own label, misattributing every other shard's answers to
		// a model that did not actually produce them. Same "launch-level,
		// not shard-level" reasoning as the sharding fields immediately
		// below.
		case s.Provenance.ResponderModel != first.Provenance.ResponderModel:
			return mismatchErr(paths[i], paths[0], "provenance.responder_model", s.Provenance.ResponderModel, first.Provenance.ResponderModel)
		// CHAOS-4100: the three sharding fields that describe the LAUNCH
		// rather than the shard. Granularity, concurrency cap and
		// provisioning mode are decided once for the whole fan-out, so
		// shards disagreeing about them means artifacts from two different
		// launches are being merged into one -- and the merged wall-clock
		// and contention story would then describe a run that never
		// happened. CaseIndices and DatabaseProvisionMillis are
		// deliberately NOT compared here: those legitimately differ per
		// shard and are combined in mergeReports instead.
		case s.Provenance.Sharding.Granularity != first.Provenance.Sharding.Granularity:
			return mismatchErr(paths[i], paths[0], "provenance.sharding.granularity", fmt.Sprint(s.Provenance.Sharding.Granularity), fmt.Sprint(first.Provenance.Sharding.Granularity))
		case s.Provenance.Sharding.ConcurrencyCap != first.Provenance.Sharding.ConcurrencyCap:
			return mismatchErr(paths[i], paths[0], "provenance.sharding.concurrency_cap", fmt.Sprint(s.Provenance.Sharding.ConcurrencyCap), fmt.Sprint(first.Provenance.Sharding.ConcurrencyCap))
		case s.Provenance.Sharding.ProvisioningMode != first.Provenance.Sharding.ProvisioningMode:
			return mismatchErr(paths[i], paths[0], "provenance.sharding.provisioning_mode", s.Provenance.Sharding.ProvisioningMode, first.Provenance.Sharding.ProvisioningMode)
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
	Member        string
	Arm           string
	MutationProbe string
}

// validateResultUniqueness (codex round-3 finding, MEDIUM; Member added
// after a live 4-way dry-run finding): validateShardSet checks
// shard_index/shard_count HEADERS are complete and consistent, but never
// checked the Results PAYLOAD actually partitions the corpus disjointly --
// a stale artifact, a re-run shard reusing an old shard_index, or a future
// bug in the round-robin selection could silently double-count results
// into the merged sums/gates without this. Checked across ALL shards
// together (not per-shard) so a duplicate spanning two different shard
// files is caught, not just one repeated within a single file.
//
// Member is REQUIRED in the key, not just Index/Arm/MutationProbe: the
// oracle annex expands each on-disk corpus case into one twoTurnOracleEntry
// PER MEMBER it tests (kind/anchor/window/handle -- loadTwoTurnOracleAnnex),
// so annex.Entries positions 4k..4k+3 typically share the SAME Index with
// four DIFFERENT Members. Sharding round-robins by POSITION
// (chaos3742_two_turn_confirmation_test.go's own shard-selection comment),
// so with shard_count==4 this can align exactly with that 4-member
// expansion -- every shard then legitimately processes the SAME case
// index, one member each. A live dry run against the real annex hit this
// exactly: case index=0 arm=positive appeared in all 4 shards, each for a
// DIFFERENT member, which an Index-only (or Index+Arm) key wrongly reports
// as a duplicate.
func validateResultUniqueness(shards []twoTurnReport, paths []string) error {
	seen := map[resultKey]string{}
	for i, s := range shards {
		for _, r := range s.Results {
			k := resultKey{Index: r.Index, Member: r.Member, Arm: r.Arm, MutationProbe: r.MutationProbe}
			if prior, dup := seen[k]; dup {
				return fmt.Errorf("duplicate result for case index=%d member=%q arm=%q mutation_probe=%q: reported by both %s and %s -- shards must partition the corpus disjointly; refusing to merge an overlapping or relabeled shard set", k.Index, k.Member, k.Arm, k.MutationProbe, prior, paths[i])
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
		merged.SynthesisStatusOverrideUncommittedCount += s.SynthesisStatusOverrideUncommittedCount
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
		merged.Timings = append(merged.Timings, s.Timings...)
	}
	merged.TimingSummary = summarizeTwoTurnTiming(merged.Timings)

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

	// CHAOS-4100: canonical row order, so the merged artifact does not
	// depend on HOW the run was cut.
	//
	// Results was a plain concatenation in shard order, which means the
	// same run cut two ways produced the same ROWS in a different
	// SEQUENCE: cutting cases {0,2} + {1,3} yields 0,2,1,3 where per-case
	// shards yield 0,1,2,3. Every count and every verdict already matched;
	// only the ordering did not.
	//
	// That is not cosmetic for this ticket. The acceptance test for
	// per-case sharding is that a merged artifact can be COMPARED against
	// a coarse-sharded one on the same tip, and two files whose rows are
	// permuted cannot be diffed by any tool a human would reach for. Row
	// order carried no information to begin with -- the shard that
	// happened to be listed first is not a fact about the measurement --
	// so sorting loses nothing and makes the artifact reproducible.
	//
	// Sorted by the SAME key validateResultUniqueness already proves
	// unique, so the order is total: no two rows can compare equal.
	sort.Slice(merged.Results, func(i, j int) bool {
		a, b := merged.Results[i], merged.Results[j]
		if a.Index != b.Index {
			return a.Index < b.Index
		}
		if a.Member != b.Member {
			return a.Member < b.Member
		}
		if a.Arm != b.Arm {
			return a.Arm < b.Arm
		}
		return a.MutationProbe < b.MutationProbe
	})

	// CHAOS-4100: the two sharding fields that are per-SHARD are combined
	// here rather than inherited from shards[0] with the rest of
	// provenance. Inheriting them would make the merged artifact claim the
	// whole run covered only shard 0's cases and provisioned in shard 0's
	// time -- a merged artifact that describes one shard is worse than one
	// that describes none, because it reads as authoritative.
	//
	// CaseIndices UNIONS: it is the merged artifact's own statement of
	// which corpus positions the run actually covered, and it is what a
	// reader checks against the annex to prove nothing was dropped.
	caseIndexSeen := make(map[int]struct{})
	for _, shard := range shards {
		for _, index := range shard.Provenance.Sharding.CaseIndices {
			caseIndexSeen[index] = struct{}{}
		}
	}
	mergedIndices := make([]int, 0, len(caseIndexSeen))
	for index := range caseIndexSeen {
		mergedIndices = append(mergedIndices, index)
	}
	sort.Ints(mergedIndices)
	merged.Provenance.Sharding.CaseIndices = mergedIndices

	// DatabaseProvisionMillis takes the MAX, not the sum: the shards
	// provision CONCURRENTLY, so summing would report a duration the run
	// never spent. The slowest shard's provisioning is the one that
	// actually delayed the run, and it is the number to compare against
	// the container substrate this ticket replaced.
	var slowestProvision int64
	for _, shard := range shards {
		if shard.Provenance.Sharding.DatabaseProvisionMillis > slowestProvision {
			slowestProvision = shard.Provenance.Sharding.DatabaseProvisionMillis
		}
	}
	merged.Provenance.Sharding.DatabaseProvisionMillis = slowestProvision

	return merged
}

// summarizeTwoTurnTiming mirrors chaos3742_two_turn_confirmation_test.go's
// identically-named function byte-for-byte (see this file's own top-of-type
// comment on why this is a hand-maintained mirror, not a shared
// definition): reduces per-case timings into one run-level aggregate per
// arm, preserving first-seen arm order.
func summarizeTwoTurnTiming(timings []twoTurnCaseTiming) []twoTurnArmTimingSummary {
	order := make([]string, 0, 5)
	byArm := map[string][]twoTurnArmTiming{}
	for _, ct := range timings {
		for _, at := range ct.Arms {
			if _, seen := byArm[at.Arm]; !seen {
				order = append(order, at.Arm)
			}
			byArm[at.Arm] = append(byArm[at.Arm], at)
		}
	}
	summaries := make([]twoTurnArmTimingSummary, 0, len(order))
	for _, arm := range order {
		samples := byArm[arm]
		wall := make([]int64, len(samples))
		var totalWall, maxWall int64
		var callCount int
		var callTotal, callMax int64
		for i, s := range samples {
			wall[i] = s.WallDurationMS
			totalWall += s.WallDurationMS
			if s.WallDurationMS > maxWall {
				maxWall = s.WallDurationMS
			}
			callCount += s.ResponderCallCount
			callTotal += s.ResponderCallTotalMS
			if s.ResponderCallMaxMS > callMax {
				callMax = s.ResponderCallMaxMS
			}
		}
		sort.Slice(wall, func(i, j int) bool { return wall[i] < wall[j] })
		summaries = append(summaries, twoTurnArmTimingSummary{
			Arm: arm, SampleCount: len(samples),
			WallMeanMS: float64(totalWall) / float64(len(samples)),
			WallP50MS:  wall[len(wall)/2], WallMaxMS: maxWall,
			ResponderCallCount: callCount, ResponderCallTotalMS: callTotal, ResponderCallMaxMS: callMax,
		})
	}
	return summaries
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
	// CHAOS-4103: THIRD zero-tolerance bar, deliberately separate from
	// false_no_match_count above -- see twoTurnReport's own doc comment on
	// SynthesisStatusOverrideUncommittedCount for why the two are never
	// folded together.
	if r.SynthesisStatusOverrideUncommittedCount > 0 {
		fail("synthesis_status_override_uncommitted_count=%d, want 0 -- an engine routing bug (see chaos4098_synthesis_status.go), not a soft status", r.SynthesisStatusOverrideUncommittedCount)
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
