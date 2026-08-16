package falkorgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// This file is CHAOS-3834's calibration TOOLING seam (embed-text spec §5
// L4 / §6 T4): a pure function that turns one oracle report's per-case S+
// (correct-pair), S- (best-wrong-neighbor), and hard-negative data into a
// recommended per-identity RetrievalPolicy, under the RECALL-GATE framing
// the CHAOS-3834 measurement ratified -- tau such that a TARGET FRACTION of
// correct pairs survive it, not a precision cliff that tries to separate S+
// from S- (the measured distributions overlap too much on the identity this
// was built against for a precision cliff to exist at any tau).
//
// Deliberately decoupled from oracle_live_test.go's unexported oracleReport
// type: that type lives in a _test.go file (test-only, per the CHAOS-3831
// harness), so a shipped, always-compiled function cannot import it. The
// types below mirror its JSON shape field-for-field instead -- JSON is the
// interface between T1's harness and T4's calibration tool, not the Go
// type -- so json.Unmarshal against a real oracle report.json (T1/T2/T3's
// artifact) populates these directly. Fields the calibration math does not
// read are simply omitted; json.Unmarshal ignores whatever else is present
// in the source document.

// CalibrationHardNegative mirrors one entry of oracle_live_test.go's
// hardNegative.
type CalibrationHardNegative struct {
	Kind        string  `json:"kind"`
	CanonicalID string  `json:"canonical_id"`
	Similarity  float64 `json:"similarity"`
}

// CalibrationCase mirrors one entry of oracle_live_test.go's oracleCaseResult
// -- only the fields the calibration math reads.
//
// HardNegativesTruncated mirrors oracleCaseResult's field of the same name,
// INCLUDING its pointer type (codex round-4 FIX B, tightening round-2 P2):
// nil means the harness reported NO completeness signal at all for this
// case -- a report from before this field existed (including a pre-CHAOS-3834
// baseline), or a harness variant that omits it. CalibrateFromReport treats
// nil as "ASSUME TRUNCATED" (the WORST case), never as "assume complete" --
// a plain bool's zero value (false) was the pre-fix hazard: it made every
// legacy report silently read as "complete" and resurrected the exact
// censored-list under-sizing bug this whole fix chain closes. A PRESENT
// false means the harness explicitly measured and reported a complete
// harvest for this case; a present true is the same explicit truncation
// signal round-2 P2 always had.
//
// HardNegativeAboveTauCount mirrors the sibling field, INCLUDING its pointer
// type: nil means "this report did not compute a total" (old report, or a
// harness variant that skips it), distinguishable from present-and-zero.
// CalibrateFromReport DOES read this for a truncated (or unknown-truncation)
// saturated case (see that function's doc comment) -- trusting it ONLY when
// the report-level Tau field EXACTLY equals the tau this function just
// recommended (codex round-3 P2: a total measured at a DIFFERENT tau, e.g.
// the report's original higher default, cannot see negatives sitting
// between the two floors, so trusting it unconditionally would silently
// under-size K again). See oracleCaseResult's copy of this field for the
// cross-tau caveat this exact-match requirement closes.
type CalibrationCase struct {
	Cause                     string                    `json:"cause"`
	CorrectSimilarity         *float64                  `json:"correct_similarity,omitempty"`
	BestWrongSimilarity       *float64                  `json:"best_wrong_similarity,omitempty"`
	HardNegatives             []CalibrationHardNegative `json:"hard_negatives,omitempty"`
	HardNegativeAboveTauCount *int                      `json:"hard_negative_above_tau_count,omitempty"`
	HardNegativesTruncated    *bool                     `json:"hard_negatives_truncated,omitempty"`

	// ExpectKind/ExpectID mirror oracleCaseResult's fields of the same name
	// (CHAOS-3829 Phase 2): CalibrateMarginFromReport needs the case's
	// EXPECTED subject to tell a correct VectorTop1 from a wrong one -- the
	// tau-calibration math above never needed this because it works purely
	// off similarity DISTRIBUTIONS, not per-case identity comparisons.
	ExpectKind string `json:"expect_kind,omitempty"`
	ExpectID   string `json:"expect_id,omitempty"`
	// VectorSearchTruncated, VectorTop1, VectorTop2, and VectorMargin mirror
	// oracle_live_test.go's oracleCaseResult fields of the same name (CHAOS-3829
	// Phase 1) -- see CalibrateMarginFromReport's doc comment for how they are
	// consumed.
	VectorSearchTruncated *bool                        `json:"vector_search_truncated,omitempty"`
	VectorTop1            *CalibrationVectorArmSubject `json:"vector_top1,omitempty"`
	VectorTop2            *CalibrationVectorArmSubject `json:"vector_top2,omitempty"`
	VectorMargin          *float64                     `json:"vector_margin,omitempty"`
	// UsedTermFallback mirrors oracle_live_test.go's oracleCaseResult field
	// of the same name (CHAOS-3829 codex r6 L1, accepted): true when this
	// case had no authored subject_terms and therefore ran through the
	// pre-CHAOS-3831 whole-question fallback. CalibrateMarginFromReport
	// excludes any case with this set from the eligible population -- see
	// the function doc comment's L1 section for why a fallback case's
	// margin measures geometry the carve-out can never see in production.
	UsedTermFallback bool `json:"used_term_fallback"`
}

// CalibrationVectorArmSubject mirrors oracle_live_test.go's vectorArmSubject
// (CHAOS-3829 Phase 1) -- see that type's doc comment for what Similarity
// (raw true-cosine, never production's transformed/floor-clamped Relevance)
// and Corroborated (did the lexical arm also propose this subject) mean.
type CalibrationVectorArmSubject struct {
	Kind         string  `json:"kind"`
	CanonicalID  string  `json:"canonical_id"`
	Similarity   float64 `json:"similarity"`
	Corroborated bool    `json:"corroborated"`
}

// CalibrationReport mirrors oracle_live_test.go's oracleReport -- only the
// fields the calibration math reads. TopK feeds the K recommendation's
// over-fetch-headroom arithmetic; Tau is carried through only as a
// diagnostic (the report's OWN tau, i.e. what the run that PRODUCED these
// similarities was already filtering with -- CalibrateFromReport computes
// its own recommended tau independently of it).
//
// EmbedIdentity/EmbedDimension mirror oracleReport's fields of the same
// name (codex round-4 FIX A, exact-measurement class -- the artifact-side
// twin of round-1's composition-tag pin and round-3's dimension pin): the
// embed retrieval identity string (EmbedRetrievalIdentityFromEnv's form)
// and embedding width this report's similarities were ACTUALLY measured
// against. See CalibrationOptions.TargetEmbedIdentity's doc comment for how
// CalibrateFromReport uses them -- a report with no stamp, or one that does
// not match the caller's target, is refused before any number in it is
// trusted.
type CalibrationReport struct {
	TopK           int               `json:"top_k"`
	Tau            float64           `json:"tau"`
	EmbedIdentity  string            `json:"embed_identity"`
	EmbedDimension int               `json:"embed_dimension"`
	Cases          []CalibrationCase `json:"cases"`
	// ControlCases mirrors oracle_live_test.go's oracleReport.ControlCases
	// (CHAOS-3829 Phase 2(c)): one CalibrationCase per no-match control,
	// read only by CalibrateMarginFromReport -- tau-calibration math above
	// never reads it.
	ControlCases []CalibrationCase `json:"control_cases,omitempty"`
	// Controls mirrors oracle_live_test.go's oracleReport.Controls (CHAOS-3829
	// codex r6 L2, accepted): the harness's own DECLARED count of control
	// cases it produced, incremented in the SAME loop iteration that appends
	// to ControlCases (oracle_live_test.go), so under an honest, complete
	// write the two are always equal by construction. Read only by
	// CalibrateMarginFromReport, which hard-fails when they diverge -- see
	// that function's doc comment for why a truncated report (a partial
	// write, a size-limited fetch, a corrupted file) silently shrinking
	// len(ControlCases) below what the harness actually measured must never
	// be allowed to pass as "these are all the controls there were" to a
	// safety-critical negative-gate computation.
	Controls int `json:"controls"`
	// Scored mirrors oracle_live_test.go's oracleReport.Scored (CHAOS-3829
	// codex r7 M3, accepted): the harness's own DECLARED count of SCORED
	// cases it produced, incremented in the SAME loop iteration that
	// appends to Cases (oracle_live_test.go), so under an honest, complete
	// write the two are always equal by construction -- EXACT symmetry
	// with Controls/ControlCases above (codex r6 L2). Read only by
	// CalibrateMarginFromReport, which hard-fails when they diverge, for
	// the identical truncated-report reason L2 already documents. NOT read
	// by CalibrateFromReport (the tau-calibration math above) -- that
	// function predates this field and is out of scope for this check,
	// exactly as L2 left CalibrateFromReport's own reading of ControlCases
	// (it has none) untouched.
	Scored int `json:"scored"`
}

// CalibrationOptions parameterizes CalibrateFromReport.
type CalibrationOptions struct {
	// TargetRecall is the fraction of S+ (correct-pair similarities) the
	// recommended tau must let survive -- e.g. 0.90 for a 90th-percentile
	// recall gate (spec §5 L4's framing, as directed by CHAOS-3834's ratified
	// conclusion: tau becomes "a low RECALL GATE ... with adjudication owned
	// by hybrid ranking + corroboration downstream", not a precision cliff).
	// Must be in (0, 1].
	TargetRecall float64

	// TargetEmbedIdentity and TargetDimension are the embed retrieval
	// identity string and embedding width the CALLER intends to apply this
	// recommendation to (codex round-4 FIX A) -- e.g. what
	// EmbedRetrievalIdentityFromEnv and a live embedder's Identity().Dimension
	// report for the deployment this calibration is FOR. Both are REQUIRED:
	// CalibrateFromReport returns ErrEmbeddingIdentityMismatch when either is
	// empty/zero, when report.EmbedIdentity/EmbedDimension is empty/zero (a
	// report with no stamp at all -- ABSENCE is not innocence, see that
	// error's doc comment), or when the two disagree. A recommendation
	// minted from one embedding space must never be silently handed to
	// LookupRetrievalPolicy for a DIFFERENT one -- this is the artifact-side
	// half of the same exact-measurement invariant retrieval_policy.go's
	// pinned table keys enforce on the CONSUMING side.
	TargetEmbedIdentity string
	TargetDimension     int
}

// CalibrationResult is CalibrateFromReport's recommendation plus the
// diagnostics that justify it -- so a caller (or a report the orchestrator
// reads) never has to take the recommended numbers on faith.
type CalibrationResult struct {
	// Policy.SimilarityFloor and Policy.OverFetchMultiplier are this
	// function's recommendation. Policy.EfRuntime is always left zero:
	// HNSW efRuntime is calibrated from a DIFFERENT measurement (the
	// CHAOS-3832 recall-vs-latency sweep over efRuntime/efConstruction
	// combinations, hnsw_sweep.go/hnsw_recall.go), which an oracle report's
	// per-case S+/S-/hard-negative data does not carry. Callers building a
	// full RetrievalPolicy entry set EfRuntime from that separate input.
	Policy RetrievalPolicy

	// ApplyReady is FAIL-CLOSED (codex round-2 P1): false whenever
	// HardNegativeRejectRate falls below NegativeGateRejectThreshold, OR
	// (codex round-6 P2) whenever the negative gate was never MEASURED at
	// all -- a report with no BestWrongSimilarity on any case and no
	// HardNegatives anywhere leaves an empty wrong-pool, whose vacuous
	// reject-rate default (1.0, "nothing to reject") would otherwise
	// trivially clear the threshold without a single impostor ever having
	// been checked. NegativeGateNote distinguishes the two false-causing
	// states explicitly ("UNMEASURED" vs "FAILED"). tau/K above remain
	// valid RECALL-CHANNEL diagnostics either way -- this does
	// NOT reopen tau as a precision knob, and CHAOS-3834's ratified design is
	// unchanged: tau is still picked from S+ recall alone (recallGateThreshold),
	// never re-derived from the negative pool. What changes is that a caller
	// can no longer treat Policy as apply-ready without checking this field
	// first: a recall-gate tau that also admits most impostors (the shipped
	// distribution's own reject rate is well below the threshold) needs an
	// explicit human decision that precision will come from hybrid ranking +
	// corroboration downstream, not a silent pass from this tool. The
	// hand-written calibratedIdentityText3Large entry in retrieval_policy.go
	// IS that explicit human decision -- it is not auto-applied output of
	// this function and is therefore not itself gated by ApplyReady; see its
	// doc comment for the sequencing-gate ruling recorded on CHAOS-3834.
	//
	// WHAT THIS FIELD ACTUALLY MEASURES (codex round-8 P1, chris-ratified
	// doctrine-split resolution): ApplyReady's criteria are TAU-LEVEL
	// PRECISION -- the PRE-T4 doctrine where tau itself must act as the
	// no-match barrier a candidate clears to be trusted, measured entirely
	// from HardNegativeRejectRate. Under the RATIFIED T4 design a
	// measurement can be a legitimate, ship-ready RECALL channel (tau picked
	// from S+ survival alone, precision deferred to downstream hybrid
	// ranking + corroboration adjudication) and STILL correctly report
	// ApplyReady=false here -- that is not a contradiction this field needs
	// to resolve, it is this field measuring a narrower thing (tau-level
	// precision) than the T4 recall-channel design ever claims to provide.
	// A human-ratified table entry (see calibratedIdentityText3Large) may
	// therefore knowingly supersede an ApplyReady=false verdict: this
	// function's fail-closed default protects an AUTOMATED caller that
	// never asked chris; it does not, and cannot, encode the recall-channel
	// doctrine's own precision story, which lives downstream of tau
	// entirely.
	ApplyReady bool
	// NegativeGateNote explains the ApplyReady verdict in one sentence, set
	// on BOTH outcomes, so a caller logging or printing this result never has
	// to re-derive the reasoning from the raw rate and threshold.
	NegativeGateNote string

	SPlusSampleSize        int
	SMinusSampleSize       int
	HardNegativeSampleSize int
	// AchievedRecall is the ACTUAL fraction of S+ that STRICTLY clear
	// Policy.SimilarityFloor (aboveSimilarityFloor's predicate, mirroring
	// production's own -- see recallGateThreshold's doc comment) -- always
	// >= TargetRecall by construction (see the function doc comment's
	// rounding rule and tau's Nextafter nudge), reported so a caller can see
	// the margin rather than re-deriving it.
	AchievedRecall float64
	// HardNegativeRejectRate is the fraction of the combined S-/hard-negative
	// pool that falls BELOW Policy.SimilarityFloor -- the spec §5 L4 test
	// criterion's other half ("≥90% of hard negatives fall below it").
	HardNegativeRejectRate float64
	// NearDuplicateP90 is the 90th-percentile, per case, count of hard
	// negatives whose similarity is >= the recommended tau -- the density
	// estimate Policy.OverFetchMultiplier is sized from. See the function
	// doc comment for the exact formula and the ambiguity it resolves.
	NearDuplicateP90 int

	// KApplyReady is a SEPARATE fail-closed gate from ApplyReady above,
	// for a different hazard (codex round-2 P2): Policy.OverFetchMultiplier
	// is sized from a per-case near-duplicate COUNT, which is only trustworthy
	// when every scored case's hard-negative harvest is complete. When at
	// least one case was truncated by the harness (CalibrationCase.
	// HardNegativesTruncated) AND every one of its serialized entries already
	// clears the recommended tau -- meaning the true count beyond the cap is
	// UNKNOWN, not merely small -- sizing K from that case's count would
	// silently under-estimate the density the same way the pre-fix bug did.
	// KApplyReady is false in that case, and Policy.OverFetchMultiplier is
	// forced to 0 ("unchanged"/no confident recommendation) rather than a
	// number that looks precise but rests on censored data. tau/K's OTHER
	// component (SimilarityFloor) is entirely unaffected -- this gate is
	// scoped to K alone.
	KApplyReady bool
	// KInsufficientDataNote explains the KApplyReady verdict in one sentence,
	// set on BOTH outcomes, mirroring NegativeGateNote's pattern.
	KInsufficientDataNote string
}

// CalibrationArtifact wraps a CalibrationResult with the provenance a
// WRITTEN, on-disk artifact needs (codex round-7 P2 FIX D) -- a bare
// CalibrationResult carries the RECOMMENDATION but nothing that identifies
// what it was recommended FOR: no embedding identity, no dimension, no
// target recall, nothing tying it back to the source report. Two files
// written by two runs against DIFFERENT embedding spaces (or different
// TargetRecall values) are byte-for-byte indistinguishable as
// CalibrationResult JSON, despite CalibrateFromReport itself refusing to
// MIX spaces at generation time (ErrEmbeddingIdentityMismatch) -- that check
// guards the run that PRODUCES the file, not a later run, script, or human
// that picks the file back up days afterward with no memory of which
// identity it was for. This is the same exact-measurement class already
// enforced everywhere else in this package (round-1's composition-tag pin,
// round-3's dimension pin, round-4's report-side identity stamp): the
// blessed artifact itself now carries enough provenance that a swapped or
// stale file is a mismatch a caller can DETECT, not just a mismatch this
// tool avoided producing.
type CalibrationArtifact struct {
	CalibrationResult

	// TargetEmbedIdentity, TargetDimension, and TargetRecall are the
	// CalibrationOptions this artifact was computed FOR -- a caller re-
	// reading this file later re-derives ITS OWN identity/dimension the
	// same way CalibrateFromReport's caller did and compares before
	// trusting Policy, exactly mirroring the generation-time check.
	TargetEmbedIdentity string  `json:"target_embed_identity"`
	TargetDimension     int     `json:"target_dimension"`
	TargetRecall        float64 `json:"target_recall"`

	// ReportTau is the SOURCE report's own tau (CalibrationReport.Tau) --
	// what the run that PRODUCED the similarities this artifact was
	// calibrated from was itself already filtering with, carried through so
	// the artifact alone (without the report file still being around)
	// answers "was this measured at a floor consistent with its own
	// recommendation" the way the report-side field always could.
	ReportTau float64 `json:"report_tau"`

	// SourceReportPath is the source report's own path on disk (empty when
	// the report was built in memory rather than read from a file -- e.g.
	// every synthetic-fixture test in this package). SourceReportSHA256 is
	// the SHA-256 of the source report's own JSON encoding, ALWAYS present
	// when the report itself is non-empty -- a path can go stale (the file
	// moved, renamed, or was deleted since), but the hash identifies the
	// EXACT measurement data this artifact was calibrated from regardless of
	// where -- or whether -- that file still exists.
	SourceReportPath   string `json:"source_report_path,omitempty"`
	SourceReportSHA256 string `json:"source_report_sha256,omitempty"`
}

// NewCalibrationArtifact builds the provenance-carrying wrapper a written
// artifact uses (see CalibrationArtifact's doc comment) from a
// CalibrateFromReport call's own inputs and result -- reportPath is the
// SOURCE report's path on disk (empty when the report was built in memory).
//
// RESIDUAL (codex r8 O3 was scoped to MarginCalibrationArtifact only, not
// re-litigated here): this function's own SourceReportSHA256 has the SAME
// lossy-re-serialization defect NewMarginCalibrationArtifact's own doc
// comment describes (hashes json.Marshal(report), a reduced mirror of the
// real file, never the file's own bytes) -- CHAOS-3834's tau-calibration
// tool predates this ticket and is out of CHAOS-3829's changeset scope; a
// future round against THIS tool specifically should apply the identical
// fix (thread the caller's raw report bytes through, as
// NewMarginCalibrationArtifact now does).
func NewCalibrationArtifact(result CalibrationResult, report CalibrationReport, opts CalibrationOptions, reportPath string) CalibrationArtifact {
	artifact := CalibrationArtifact{
		CalibrationResult:   result,
		TargetEmbedIdentity: opts.TargetEmbedIdentity,
		TargetDimension:     opts.TargetDimension,
		TargetRecall:        opts.TargetRecall,
		ReportTau:           report.Tau,
		SourceReportPath:    reportPath,
	}
	if encoded, err := json.Marshal(report); err == nil {
		sum := sha256.Sum256(encoded)
		artifact.SourceReportSHA256 = hex.EncodeToString(sum[:])
	}
	return artifact
}

// NegativeGateRejectThreshold is the spec §5 L4 test criterion's negative
// half, made an explicit, checkable constant (codex round-2 P1): at least
// this fraction of the combined S-/hard-negative pool must fall at or below
// the recommended tau for CalibrateFromReport to report ApplyReady=true.
// This does NOT feed back into how tau is chosen (recallGateThreshold reads
// S+ only, unchanged) -- it is a SEPARATE, fail-closed check on the tau that
// comes out, so a recall-gate value that also happens to admit most
// impostors cannot silently pass as a ready-to-apply policy.
const NegativeGateRejectThreshold = 0.90

var (
	// ErrNoCorrectSimilaritySamples reports that the report carries no S+
	// data at all (every case has a nil CorrectSimilarity) -- calibration has
	// nothing to set a recall gate against.
	ErrNoCorrectSimilaritySamples = errors.New("calibration report has no correct-pair similarity samples")
	// ErrInvalidTargetRecall reports a TargetRecall outside (0, 1], INCLUDING
	// non-finite values (codex round-7 P2 FIX C -- strconv.ParseFloat happily
	// accepts the literal string "NaN", and NaN fails BOTH range comparisons
	// below (<=0 is false, >1 is false), so a bare range check silently lets
	// NaN through; everything downstream that turns TargetRecall into a rank
	// (recallGateThreshold's excludeCount, ultimately math.Round((1-NaN)*n))
	// is then implementation-defined rather than a defined error. +Inf and
	// -Inf are equally nonsensical as a recall target and are rejected the
	// same way, at the same site, for the same reason: this is the ONE place
	// TargetRecall gets validated, so every caller -- the live test harness's
	// env-var parse included -- gets the same fail-closed behavior for free.
	ErrInvalidTargetRecall = errors.New("calibration target recall must be a finite number in (0, 1]")
	// ErrNoFeasibleFloor reports that the recall target forces a tau outside
	// floorApplicable's (0, 1) range (codex round-3 P2) -- e.g. TargetRecall
	// near 1.0 against an S+ sample at or below 0 (a genuinely low- or
	// negative-similarity correct pair the harness's UNCLAMPED
	// trueCosineSimilarity can report; production's own CosineFromDistance is
	// clamped to [0,1], but this oracle-side value is not). A tau this low is
	// not a usable recall-gate value at all: floorApplicable is the SAME
	// predicate EmbedderFromEnv gates a calibrated SimilarityFloor on before
	// ever applying it, so a result this function returned as
	// ApplyReady=true here would be silently ignored there -- returning an
	// error instead of a CalibrationResult makes that inapplicability loud
	// at calibration time, not silently discovered later at deploy time.
	ErrNoFeasibleFloor = errors.New("calibration target recall forces a similarity floor outside the applicable (0,1) range -- no feasible policy")
	// ErrEmbeddingIdentityMismatch reports that report.EmbedIdentity/
	// EmbedDimension is absent, or does not exactly match
	// opts.TargetEmbedIdentity/TargetDimension (codex round-4 FIX A --
	// exact-measurement class, artifact side). A report with no identity
	// stamp at all (pre-CHAOS-3834, or a harness variant that omits the
	// field) is treated the SAME as a mismatch, never as "unknown, proceed
	// anyway": a recommendation minted from an unstamped or wrong embedding
	// space must never reach a caller that then hands it to
	// LookupRetrievalPolicy for a DIFFERENT target identity/dimension than
	// what actually produced these similarities. A caller that omits its
	// own target (opts.TargetEmbedIdentity == "" or TargetDimension <= 0)
	// gets the SAME error -- calibrating without saying what it is FOR is
	// exactly the hazard this closes, not a mode this function supports.
	ErrEmbeddingIdentityMismatch = errors.New("calibration report's embed identity/dimension is missing or does not match the target identity/dimension")
)

// CalibrateFromReport computes a recommended (tau, K) RetrievalPolicy from
// one oracle report, per spec §5 L4 / §6 T4's recall-gate framing.
//
// TAU: the largest value such that AT LEAST opts.TargetRecall of the S+
// (correct-pair) samples STRICTLY CLEAR it under production's own floor
// predicate (aboveSimilarityFloor -- similarity > tau, never >=; see
// vectorSearchNodesWithOverFetch). Computed via the nearest-rank method on
// the ascending-sorted S+ sample: excludeCount = floor((1-TargetRecall) *
// n), then tau is nudged one float64 ULP below the (excludeCount)-th
// smallest sample (0-indexed) via math.Nextafter -- so exactly n-excludeCount
// samples clear it, INCLUDING that boundary sample itself (production would
// otherwise drop a candidate whose similarity exactly equals tau, making the
// un-nudged value claim a sample it could never actually retrieve). floor()
// (rather than round or ceil) guarantees n-excludeCount >= TargetRecall*n,
// i.e. AchievedRecall never undershoots the target. A single-sample report
// yields tau just below that sample and AchievedRecall = 1.0.
//
// K (OverFetchMultiplier): sized to the observed near-duplicate density
// AT THE RECOMMENDED TAU, per spec §5 L4 ("K sized to the observed tie/
// near-duplicate density"). For each case, count how many of its harvested
// hard negatives ALSO clear the recommended tau (i.e. would still be
// competing for top-K slots alongside the correct answer once the floor is
// applied). Take the CONVENTIONAL 90th percentile of that per-case count
// across all cases (nearest-rank method: ceil(0.90*n)-th smallest, a HIGH
// value such that ~90% of cases have a near-duplicate count at or below
// it) as the density estimate NearDuplicateP90 -- deliberately the OPPOSITE
// direction from tau's threshold (tau wants a LOW value most S+ clears;
// the density estimate wants a HIGH value that covers all but the busiest
// cases, so the over-fetch has headroom for them too). Then set
// OverFetchMultiplier = ceil((TopK + NearDuplicateP90) / TopK) -- the
// smallest multiplier whose raw fetch size (spec §5 L3's `multiplier*limit`)
// has room for TopK genuine candidates AND the estimated near-duplicate
// competitors, clamped to a minimum of 1. A report with TopK <= 0, or with
// every case's near-duplicate count at zero, recommends multiplier 1 (i.e.
// "unchanged" -- see RetrievalPolicy's doc comment on why zero already
// means that downstream). This formula is CHAOS-3834's resolved reading of
// an underspecified spec bullet; see this lane's report for the ambiguity
// note.
func CalibrateFromReport(report CalibrationReport, opts CalibrationOptions) (CalibrationResult, error) {
	if math.IsNaN(opts.TargetRecall) || math.IsInf(opts.TargetRecall, 0) || opts.TargetRecall <= 0 || opts.TargetRecall > 1 {
		return CalibrationResult{}, ErrInvalidTargetRecall
	}
	// codex round-4 FIX A: checked BEFORE anything in the report is trusted
	// -- a report minted from the wrong embedding space (or with no
	// identity stamp at all) must never reach the S+/S-/hard-negative math
	// below, let alone come back looking like a usable recommendation.
	if opts.TargetEmbedIdentity == "" || opts.TargetDimension <= 0 ||
		report.EmbedIdentity == "" || report.EmbedDimension <= 0 ||
		report.EmbedIdentity != opts.TargetEmbedIdentity || report.EmbedDimension != opts.TargetDimension {
		return CalibrationResult{}, ErrEmbeddingIdentityMismatch
	}

	var sPlus, sMinus []float64
	var hardNegatives []float64
	// perScoredCaseHardNegatives carries ONE entry per SCORED case (a case
	// with a CorrectSimilarity -- i.e. one that reached term-level
	// evaluation, per oracleCaseResult's doc comment), even when that
	// case's HardNegatives is empty. Kept alongside sPlus/sMinus/hardNegatives
	// rather than folded in during the first pass, because the
	// near-duplicate density (K) can only be computed AFTER tau is known.
	// A scored case with GENUINELY zero hard negatives is real density
	// signal ("nothing crowds this correct answer") and must count as a
	// zero in the distribution percentileInt draws from -- silently
	// omitting such cases would bias the density estimate upward by
	// counting only the crowded cases.
	// scoredCaseHardNegatives pairs a scored case's harvested similarities
	// with whether the HARNESS truncated that harvest (codex round-2 P2) --
	// carried per-case, not just as a report-wide flag, because K-sizing
	// safety is a per-case question: one truncated case with a saturated
	// (fully-above-tau) capped list poisons the density estimate even if
	// every other case's harvest was complete.
	type scoredCaseHardNegatives struct {
		similarities  []float64
		truncated     bool
		aboveTauCount *int
	}
	var perScoredCaseHardNegatives []scoredCaseHardNegatives

	for _, c := range report.Cases {
		if c.BestWrongSimilarity != nil {
			sMinus = append(sMinus, *c.BestWrongSimilarity)
		}
		if c.CorrectSimilarity == nil {
			continue
		}
		sPlus = append(sPlus, *c.CorrectSimilarity)
		similarities := make([]float64, len(c.HardNegatives))
		for i, hn := range c.HardNegatives {
			similarities[i] = hn.Similarity
			hardNegatives = append(hardNegatives, hn.Similarity)
		}
		// codex round-4 FIX B: nil (no completeness signal reported at all)
		// resolves to true -- ASSUME TRUNCATED, the worst case -- not false.
		// See CalibrationCase.HardNegativesTruncated's doc comment.
		truncated := true
		if c.HardNegativesTruncated != nil {
			truncated = *c.HardNegativesTruncated
		}
		perScoredCaseHardNegatives = append(perScoredCaseHardNegatives, scoredCaseHardNegatives{
			similarities: similarities, truncated: truncated, aboveTauCount: c.HardNegativeAboveTauCount,
		})
	}

	if len(sPlus) == 0 {
		return CalibrationResult{}, ErrNoCorrectSimilaritySamples
	}

	tau, achievedRecall := recallGateThreshold(sPlus, opts.TargetRecall)
	// codex round-3 P2: validate BEFORE building a result, not after -- a
	// high TargetRecall against a low- or negative-similarity S+ sample can
	// push tau to or below 0 (or, in principle, to or above 1). floorApplicable
	// is production's own applicability check (EmbedderFromEnv gates a
	// calibrated SimilarityFloor on it before ever using it), so a tau
	// outside it is not a policy this function can honestly recommend at
	// all -- return the error, never a CalibrationResult that LOOKS
	// apply-ready but that production would silently refuse to apply.
	if !floorApplicable(tau) {
		return CalibrationResult{}, ErrNoFeasibleFloor
	}

	wrongPool := make([]float64, 0, len(sMinus)+len(hardNegatives))
	wrongPool = append(wrongPool, sMinus...)
	wrongPool = append(wrongPool, hardNegatives...)
	rejectRate := 1.0 // vacuously "fully rejects" when there is nothing wrong to reject
	if len(wrongPool) > 0 {
		below := 0
		for _, w := range wrongPool {
			// Mirrors aboveSimilarityFloor (vector.go), production's single
			// floor predicate: a wrong-pool sample is REJECTED (production
			// drops it) whenever it does NOT clear tau, i.e. w <= tau, not
			// just w < tau -- an at-tau wrong sample is dropped in
			// production exactly like a below-tau one (codex round-1 P1
			// sibling: the un-mirrored `<` here undercounted rejection at
			// the boundary the same way the old recall math overcounted it).
			if !aboveSimilarityFloor(w, tau) {
				below++
			}
		}
		rejectRate = float64(below) / float64(len(wrongPool))
	}

	nearDupCounts := make([]int, len(perScoredCaseHardNegatives))
	// kInsufficientData is codex round-2 P2's refusal signal, now driven by
	// hardNegativeCaseCount's exhaustive decision table (codex round-5 FIX
	// B) rather than ad-hoc nested conditionals -- see that function's doc
	// comment for the full table and why round-4's len(similarities)>0
	// guard was a real, reachable bug (ACR_TEST_ORACLE_HARD_NEGATIVES=0).
	kInsufficientData := false
	for i, scored := range perScoredCaseHardNegatives {
		count, sufficient := hardNegativeCaseCount(scored.similarities, tau, scored.truncated, scored.aboveTauCount, report.Tau)
		if !sufficient {
			kInsufficientData = true
		}
		nearDupCounts[i] = count
	}
	p90 := percentileInt(nearDupCounts, 0.90)

	kApplyReady := !kInsufficientData
	kNote := "K sized from complete per-case hard-negative data (no scored case's harvest was truncated at its own tau-clearing boundary)"
	if kInsufficientData {
		kNote = fmt.Sprintf(
			"K NOT sized: at least one scored case's hard-negative harvest was truncated by the harness (HardNegativesTruncated) and every serialized entry already clears the recommended tau (%.4f) -- the true near-duplicate count beyond the cap is unknown, so OverFetchMultiplier falls back to 0 (unchanged) rather than a confident-looking but potentially under-sized value. If a total was reported, it was measured at report.Tau=%.4f, which does not exactly match the recommended tau -- counts measured at a different tau are not usable here; re-run the oracle harness at the recommended tau, raise ACR_TEST_ORACLE_HARD_NEGATIVES for this identity, or size K by hand from the full offline harvest.",
			tau, report.Tau,
		)
	}

	multiplier := 1
	if !kInsufficientData && report.TopK > 0 && p90 > 0 {
		multiplier = int(math.Ceil(float64(report.TopK+p90) / float64(report.TopK)))
		if multiplier < 1 {
			multiplier = 1
		}
	}

	// codex round-6 P2: an EMPTY wrongPool (no BestWrongSimilarity on any
	// scored case, no hard negatives anywhere in the report) leaves
	// rejectRate at its vacuous 1.0 default, which -- unguarded --
	// satisfies the >= threshold check below and reports ApplyReady=true
	// for a gate that was never actually measured against a single
	// impostor. Same fail-toward-fine class as every other gate this lane
	// has closed: "nothing was checked" must never read as "checked and
	// passed". measured is false ONLY when there is nothing to check;
	// applyReady requires BOTH measured AND the threshold clearing.
	measured := len(wrongPool) > 0
	applyReady := measured && rejectRate >= NegativeGateRejectThreshold
	var note string
	switch {
	case !measured:
		note = "negative gate UNMEASURED: this report carries no negative measurements at all -- no case has a BestWrongSimilarity and no case has any HardNegatives -- so the recall-gate tau above has never been checked against a single impostor. ApplyReady is false because NOTHING WAS MEASURED, not because measurement failed; this is distinct from a negative gate that ran and came back below threshold. Re-run the harness so scored cases populate BestWrongSimilarity and/or HardNegatives before trusting this tau."
	case applyReady:
		note = fmt.Sprintf(
			"negative gate PASSED: hard-negative reject rate %.4f clears the %.4f threshold; recall-gate tau is apply-ready",
			rejectRate, NegativeGateRejectThreshold,
		)
	default:
		note = fmt.Sprintf(
			"negative gate FAILED: hard-negative reject rate %.4f is below the %.4f threshold -- recall-only tau; "+
				"precision must come from hybrid/corroboration adjudication, not this floor; human ratification required "+
				"before this tau ships as a policy",
			rejectRate, NegativeGateRejectThreshold,
		)
	}

	return CalibrationResult{
		Policy: RetrievalPolicy{
			SimilarityFloor: tau,
			// 0 renders as "unchanged" (RetrievalPolicy's doc comment) when
			// the density estimate found no reason to widen the fetch.
			OverFetchMultiplier: overFetchMultiplierOrUnchanged(multiplier),
		},
		ApplyReady:             applyReady,
		NegativeGateNote:       note,
		KApplyReady:            kApplyReady,
		KInsufficientDataNote:  kNote,
		SPlusSampleSize:        len(sPlus),
		SMinusSampleSize:       len(sMinus),
		HardNegativeSampleSize: len(hardNegatives),
		AchievedRecall:         achievedRecall,
		HardNegativeRejectRate: rejectRate,
		NearDuplicateP90:       p90,
	}, nil
}

// ---------------------------------------------------------------------
// CHAOS-3829 Phase 2: VectorMarginCommitThreshold (M) calibration.
//
// A sibling to CalibrateFromReport above, reusing its fail-closed idioms
// (identity/dimension pinning via ErrEmbeddingIdentityMismatch, an
// insufficient-data refusal that leaves the recommendation nil rather than a
// number that reads as validated when nothing was actually checked) for a
// DIFFERENT question: not "what similarity floor gates retrieval", but "what
// margin between the vector arm's top-1 and top-2 candidate is decisive
// enough to auto-commit top-1, once corroboration holds" -- CHAOS-3829's
// ratified commit-path carve-out in graphrank.ResolveFromMergedCandidates.
//
// PHASE 2(c) REVISION (team-lead dispatch, 2026-08-16, re-reading lane-3829's
// own Phase 1+2 report): the eligibility predicate below no longer requires
// vectorSearchComplete. Rationale, verified against vector.go:390-392:
// db.idx.vector.queryNodes returns rows `ORDER BY score ASC` (ascending
// DISTANCE, i.e. descending similarity), and vectorSearchNodesWithOverFetch's
// truncation is derived from whether MORE than returnCap survivors cleared
// tau (vector.go's own doc comment) -- truncation therefore only ever cuts
// the TAIL of an already-similarity-ordered list, never reorders or drops
// the HEAD. The top-1/top-2 identities and similarities (and hence their
// margin) a truncated call returns are EXACTLY what an untruncated call
// would also have returned, provably, not merely usually -- so
// vectorSearchComplete is unnecessary for a top-1/top-2 margin gate
// specifically, unlike graphrank's existing top-of-two RELEVANCE-score gate
// (0.88/0.12), which genuinely can be second-guessed by a dropped competitor
// because a relevance score is not distance-ordered the way this is. This is
// a MEASUREMENT-TOOL-ONLY change -- no resolution.go/resolve.go code exists
// yet, and chris's ratification of the underlying production geometry
// (whether the eventual carve-out itself drops the vectorSearchComplete
// conjunct) is still pending; this function's own eligibility choice is
// provisional on that same ratification, and this doc comment is the record
// of why it was already made HERE, in the tool, ahead of it.
// ---------------------------------------------------------------------

// ErrNoMarginSamples reports that the report carries no ELIGIBLE margin
// sample at all: no case (scored or control) reached the corroborated-top-1
// + measurable-margin state CHAOS-3829's ratified commit-path carve-out
// operates in (see CalibrateMarginFromReport's doc comment for the exact
// eligibility predicate). There is nothing here to calibrate M from, not even
// an unsafe recommendation -- distinct from ApplyReady=false, which means
// SOME eligible cases exist but none of them were wrong-top1 (see that
// field's doc comment).
var ErrNoMarginSamples = errors.New("calibration report has no eligible vector-margin samples (corroborated top-1 + >=2 distinct vector-arm subjects)")

// ErrMarginReportConfigMismatch reports that report.Tau/report.TopK is
// absent, non-positive, or does not exactly match
// MarginCalibrationOptions.TargetTau/TargetTopK (codex r1 F7) -- see those
// fields' doc comment for why a mismatch here invalidates M the same way an
// embed-identity/dimension mismatch does.
var ErrMarginReportConfigMismatch = errors.New("calibration report's tau/topK is missing or does not match the target tau/topK")

// ErrMarginReportInternallyInconsistent reports that a CalibrationCase's
// stored VectorMargin does not equal VectorTop1.Similarity -
// VectorTop2.Similarity (codex r3 H2), or that either similarity is
// non-finite -- see checkMarginConsistency's doc comment. Wraps the
// specific case's population/index/details via fmt.Errorf's %w for a
// caller that wants to log or inspect the failure programmatically.
var ErrMarginReportInternallyInconsistent = errors.New("calibration report case's stored vector_margin does not match its own top-1/top-2 similarities")

// ErrMarginReportControlsCountMismatch reports that report.Controls (the
// harness's own declared control-case count) does not equal
// len(report.ControlCases) (codex r6 L2, accepted) -- see
// CalibrationReport.Controls' doc comment and CalibrateMarginFromReport's
// own doc comment for why a truncated report must hard-fail here rather
// than silently calibrating the negative gate from however many control
// cases happened to survive truncation.
var ErrMarginReportControlsCountMismatch = errors.New("calibration report's declared controls count does not match len(control_cases) -- the report may be truncated")

// ErrMarginReportScoredCountMismatch is ErrMarginReportControlsCountMismatch's
// EXACT symmetric counterpart for the SCORED population (codex r7 M3,
// accepted) -- see CalibrationReport.Scored's doc comment.
var ErrMarginReportScoredCountMismatch = errors.New("calibration report's declared scored count does not match len(cases) -- the report may be truncated")

// checkMarginConsistency validates every CalibrationCase in cases against
// the closed set of valid (VectorTop1, VectorTop2, VectorMargin) shapes
// (codex r3 H2, extended by codex r4 J3, closed by codex r6 L3 -- see the
// switch inside for the full three-shape enumeration). For the one shape
// that carries a genuine measurement (VectorTop1+VectorTop2+VectorMargin
// all present), the oracle harness always computes VectorMargin as
// VectorTop1.Similarity-VectorTop2.Similarity (oracle_live_test.go)
// whenever it has two ranked subjects to compute it FROM, so a report where
// the stored field disagrees with that recomputation, is MISSING despite
// two ranked subjects being present, or has a non-finite similarity cannot
// be trusted -- something corrupted it, hand-edited it, or a producer bug
// wrote it incorrectly.
//
// r4 J3 (accepted): the ORIGINAL H2 check only ran when VectorMargin was
// ALSO non-nil, silently treating "two subjects present, margin absent" the
// SAME as "no margin was ever claimed" (VectorTop2 itself nil) -- but those
// are different states. The producer contract (oracle_live_test.go, both
// the scored-case and control-case measurement paths) always writes
// VectorMargin whenever it writes VectorTop2, so an absent margin alongside
// a present VectorTop2 IS corruption -- silently skipping such a case could
// drop exactly the LARGEST wrong-top1 margin in the report (the one most
// likely to have been tampered with to hide it) while ApplyReady stayed
// true, understating M. Only a case with VectorTop2 itself nil (no second
// ranked subject at all -- margin is genuinely undefined, not merely
// unwritten) is exempt.
//
// label identifies which population (report.Cases vs report.ControlCases)
// for the error message.
func checkMarginConsistency(cases []CalibrationCase, label string) error {
	for i, c := range cases {
		hasTop1 := c.VectorTop1 != nil
		hasTop2 := c.VectorTop2 != nil
		hasMargin := c.VectorMargin != nil

		// codex r6 L3 (accepted): the closed set of valid (VectorTop1,
		// VectorTop2, VectorMargin) shapes is EXACTLY three -- {all absent}
		// (no vector-arm data at all), {VectorTop1 only} (one distinct
		// subject, no competitor to measure a margin against), and
		// {VectorTop1+VectorTop2+VectorMargin} (a full measurement, still
		// validated numerically below). Every OTHER combination -- a
		// VectorTop2 or VectorMargin present without VectorTop1, a
		// VectorMargin present without VectorTop2 (r4 J3's own asymmetric
		// case, now folded into this single enumeration), or VectorTop1+
		// VectorTop2 present with VectorMargin absent (r3 H2/r4 J3's
		// original check) -- is a shape the producer (oracle_live_test.go's
		// measureOneTermVectorArm/measureControlCase) never writes, so its
		// presence means the report was corrupted, hand-edited, or written
		// by a buggy producer, not that this case is merely unmeasured.
		switch {
		case !hasTop1 && !hasTop2 && !hasMargin:
			continue
		case hasTop1 && !hasTop2 && !hasMargin:
			continue
		case hasTop1 && hasTop2 && hasMargin:
			// Valid shape; still needs the numeric cross-check below.
		case hasTop1 && hasTop2 && !hasMargin:
			return fmt.Errorf("%w: %s[%d] has both VectorTop1 and VectorTop2 but no VectorMargin -- the producer always writes one alongside the other, so absence is corruption, not an unmeasured case", ErrMarginReportInternallyInconsistent, label, i)
		default:
			return fmt.Errorf("%w: %s[%d] has an impossible vector-arm shape (VectorTop1 present=%v, VectorTop2 present=%v, VectorMargin present=%v) -- the only valid shapes are {all absent}, {VectorTop1 only}, or {VectorTop1+VectorTop2+VectorMargin}", ErrMarginReportInternallyInconsistent, label, i, hasTop1, hasTop2, hasMargin)
		}

		s1, s2 := c.VectorTop1.Similarity, c.VectorTop2.Similarity
		if math.IsNaN(s1) || math.IsInf(s1, 0) || math.IsNaN(s2) || math.IsInf(s2, 0) {
			return fmt.Errorf("%w: %s[%d] has a non-finite vector-arm similarity (top1=%v top2=%v)", ErrMarginReportInternallyInconsistent, label, i, s1, s2)
		}
		// codex r8 O4 (accepted): the PRODUCTION invariant
		// 0 <= top2.Similarity <= top1.Similarity <= 1 -- embedprovider.
		// CosineFromDistance (the SAME conversion both production's
		// vectorSearchNodesWithOverFetch and the oracle harness's
		// trueCosineSimilarity ultimately rely on) is UNCONDITIONALLY
		// clamped to [0,1], and "top1"/"top2" are BY DEFINITION the
		// higher/lower of the two entries this measurement ranked (the
		// harness's own merge keeps the MAX similarity per subject, then
		// sorts descending before naming top1/top2) -- so top1 < top2 or
		// either value outside [0,1] is not a valid measurement under any
		// circumstance, corrupted or hand-edited data notwithstanding.
		// Without this check, a REVERSED ranking (top1/top2 swapped, e.g.
		// by a producer bug) still recomputes an internally "consistent"
		// but NEGATIVE margin that passes the check below unnoticed -- and
		// a negative value entering wrongMargins could itself produce a
		// NEGATIVE ThresholdM, which the RUNTIME gate
		// (vectorMarginCommitThreshold > 0, ResolveFromMergedCandidates)
		// would then silently read as "uncalibrated" and disable the
		// carve-out -- a calibration bug masquerading as an inert,
		// unremarkable zero rather than surfacing as the hard failure it
		// actually is. Fail loud HERE instead, at calibration time.
		if s1 < 0 || s1 > 1 || s2 < 0 || s2 > 1 || s1 < s2 {
			return fmt.Errorf("%w: %s[%d] violates the production similarity invariant 0<=top2<=top1<=1 (top1=%v top2=%v)", ErrMarginReportInternallyInconsistent, label, i, s1, s2)
		}
		if recomputed := s1 - s2; recomputed != *c.VectorMargin {
			return fmt.Errorf("%w: %s[%d] stored vector_margin=%v, but top1.similarity(%v)-top2.similarity(%v)=%v", ErrMarginReportInternallyInconsistent, label, i, *c.VectorMargin, s1, s2, recomputed)
		}
	}
	return nil
}

// MarginCalibrationOptions parameterizes CalibrateMarginFromReport. Mirrors
// CalibrationOptions' identity-pinning fields exactly (same exact-measurement
// rationale); there is no TargetRecall analogue here -- M is sized from a
// zero-tolerance construction (see the function doc comment), not a
// recall-gate percentile.
type MarginCalibrationOptions struct {
	// TargetEmbedIdentity and TargetDimension are the embed retrieval
	// identity string and embedding width the CALLER intends to apply this
	// recommendation to. See CalibrationOptions' fields of the same name.
	TargetEmbedIdentity string
	TargetDimension     int
	// TargetTau and TargetTopK are CHAOS-3829 codex r1 F7 (accepted): the
	// similarity floor and ANN result-set size the CALLER intends this M to
	// gate under. Both are REQUIRED (TargetTau > 0, TargetTopK > 0) and must
	// EXACTLY match report.Tau/report.TopK, or CalibrateMarginFromReport
	// refuses with ErrMarginReportConfigMismatch -- the same
	// exact-measurement discipline CalibrationOptions' identity/dimension
	// pinning already enforces, extended here because M's zero-tolerance
	// construction is only meaningful if the wrong-top1 margins it was
	// computed from were measured at the EXACT tau/topK the runtime carve-out
	// will actually operate under: a report measured at a different tau
	// admits/rejects a different candidate population entirely (a lower tau
	// lets more, closer near-duplicates survive to compete for top-1/top-2),
	// and a different topK changes vectorSearchNodesWithOverFetch's own
	// returnCap and therefore which candidates the ANN call even returns.
	TargetTau  float64
	TargetTopK int
}

// MarginCalibrationResult is CalibrateMarginFromReport's recommendation plus
// the diagnostics that justify it -- so a caller never has to take
// ThresholdM on faith.
type MarginCalibrationResult struct {
	// ThresholdM is the recommended VectorMarginCommitThreshold: the
	// smallest value such that EVERY wrong-top1 margin this report measured
	// (among the eligible, corroborated-top1 population -- see the function
	// doc comment's Phase 2(c) revision for why vectorSearchComplete is not
	// part of this predicate) falls STRICTLY below it. A POINTER: nil when
	// SafetyMeasured is false -- see that field's doc comment for why an M
	// computed from an empty wrong-sample would be a vacuous-truth hazard,
	// not a validated recommendation.
	ThresholdM *float64
	// WrongMarginMax is the largest wrong-top1 margin observed -- the exact
	// value ThresholdM was derived from (one float64 ULP above it, via
	// math.Nextafter), reported so a caller can see the empirical bound this
	// recommendation rests on without re-deriving it from ThresholdM. Also a
	// pointer, nil under the same condition as ThresholdM.
	WrongMarginMax *float64

	// ApplyReady is FAIL-CLOSED, equal to SafetyMeasured: false whenever no
	// wrong-top1 case was measured in the eligible population at all. This
	// mirrors CalibrateFromReport's own "measured := len(wrongPool) > 0"
	// negative-gate-unmeasured guard exactly -- an M computed from zero wrong
	// samples would vacuously "reject everything wrong" the same way an
	// empty wrongPool vacuously "rejects" 100% of nothing there. Unlike
	// CalibrateFromReport's negative gate (a THRESHOLD on a computed reject
	// rate), there is no separate threshold to fail here once SafetyMeasured
	// is true: M is constructed by definition to reject every wrong-top1
	// margin this report measured, so there is nothing further to gate on
	// besides whether that construction had any data to work from.
	ApplyReady bool
	// Note explains the verdict in one or two sentences, set on every
	// outcome, mirroring NegativeGateNote's pattern.
	Note string

	// SafetyMeasured is WrongSampleSize > 0 -- whether ThresholdM rests on
	// at least one observed wrong-top1 case. ReachMeasured is
	// CorrectSampleSize > 0 -- whether AchievedReach is a real fraction
	// (denominator > 0) rather than a vacuous 0. Exposed as named booleans
	// so a caller need not re-derive them from the sample-size fields below.
	SafetyMeasured bool
	ReachMeasured  bool

	// CorrectSampleSize/WrongSampleSize are the eligible population's split
	// by top-1 correctness (VectorTop1 matches the case's expected subject,
	// or not). WrongSampleSize includes both wrong-top1 SCORED cases and
	// corroborated-with-margin CONTROL cases (a control top-1 is always
	// wrong -- see the function doc comment's Phase 2(c) UNION rule).
	CorrectSampleSize int
	WrongSampleSize   int
	// WrongSampleSizeFromControls is the CONTROL-sourced subset of
	// WrongSampleSize -- how many of the wrong-top1 margins came from a
	// corroborated no-match control rather than a wrong-top1 scored case.
	// Reported separately so a caller can see how much of the safety bound
	// rests on which population.
	WrongSampleSizeFromControls int

	// ControlsInReport/ControlsWithVectorArmData/ControlsCorroborated are
	// Phase 2(c)'s explicit "record as a measured fact, not silently"
	// counters (team-lead dispatch): the TOTAL control cases in the report,
	// how many of those reached term-level vector-arm evaluation at all
	// (VectorTop1 != nil), and how many of THOSE were corroborated --
	// always populated, even when zero, so a reader never has to infer
	// "zero controls were corroborated" from an absent field.
	// ControlsCorroboratedWithoutMargin is the (expected small/zero) subset
	// of ControlsCorroborated whose vector arm proposed only ONE distinct
	// subject -- corroborated, but with no second candidate to measure a
	// margin against, so it cannot feed WrongSampleSize (see the function
	// doc comment).
	ControlsInReport                  int
	ControlsWithVectorArmData         int
	ControlsCorroborated              int
	ControlsCorroboratedWithoutMargin int

	// ExcludedFallbackCases/ExcludedFallbackControls are CHAOS-3829 codex r6
	// L1's (accepted) diagnostic: how many otherwise-eligible SCORED /
	// CONTROL cases (corroborated top-1, measurable margin -- every OTHER
	// eligibility condition already satisfied) were excluded from
	// CorrectSampleSize/WrongSampleSize specifically because
	// UsedTermFallback was set. See the function doc comment's L1 section
	// for why a fallback case's margin measures geometry the term-sourced-
	// only carve-out can never see in production. Recorded unconditionally
	// (even when zero), mirroring the Controls* counters' own "measured
	// fact, not silently" discipline.
	ExcludedFallbackCases    int
	ExcludedFallbackControls int

	// AchievedReach is the fraction of CORRECT-top1 eligible cases whose
	// VectorMargin clears ThresholdM -- how many of the cases where this
	// carve-out could have safely committed actually would, at the
	// recommended M. 0 whenever ReachMeasured or SafetyMeasured is false (no
	// denominator, or no M to test against).
	AchievedReach float64
}

// MarginCalibrationArtifact wraps a MarginCalibrationResult with the
// provenance a WRITTEN, on-disk artifact needs (codex r3 H3, mirroring
// CalibrationArtifact's identical shape/rationale) -- a bare
// MarginCalibrationResult carries the RECOMMENDATION but nothing that
// identifies what it was recommended FOR: no embedding identity, no
// dimension, no tau/topK, no source-report provenance. Two files written by
// two runs against DIFFERENT embedding spaces (or different tau/topK) are
// byte-for-byte indistinguishable as MarginCalibrationResult JSON, despite
// CalibrateMarginFromReport itself refusing to MIX spaces/configs at
// generation time (ErrEmbeddingIdentityMismatch / ErrMarginReportConfigMismatch)
// -- that check guards the run that PRODUCES the file, not a later run,
// script, or human that picks the file back up days afterward with no
// memory of which identity/tau/topK it was for.
type MarginCalibrationArtifact struct {
	MarginCalibrationResult

	// TargetEmbedIdentity, TargetDimension, TargetTau, and TargetTopK are
	// the MarginCalibrationOptions this artifact was computed FOR -- a
	// caller re-reading this file later re-derives its OWN target the same
	// way CalibrateMarginFromReport's caller did and compares before
	// trusting ThresholdM, exactly mirroring the generation-time check.
	TargetEmbedIdentity string  `json:"target_embed_identity"`
	TargetDimension     int     `json:"target_dimension"`
	TargetTau           float64 `json:"target_tau"`
	TargetTopK          int     `json:"target_topk"`

	// SourceReportPath is the source report's own path on disk (empty when
	// the report was built in memory rather than read from a file -- e.g.
	// every synthetic-fixture test in this package). SourceReportSHA256 is
	// the SHA-256 of the source report's own ON-DISK BYTES -- see
	// NewMarginCalibrationArtifact's own doc comment (codex r8 O3) for why
	// this must be the ACTUAL file content, not a re-marshalling of the
	// (reduced) CalibrationReport struct.
	SourceReportPath   string `json:"source_report_path,omitempty"`
	SourceReportSHA256 string `json:"source_report_sha256,omitempty"`
}

// NewMarginCalibrationArtifact builds the provenance-carrying wrapper a
// written artifact uses (see MarginCalibrationArtifact's doc comment) from a
// CalibrateMarginFromReport call's own inputs and result -- reportPath is
// the SOURCE report's path on disk (empty when the report was built in
// memory rather than read from a file).
//
// reportBytes is CHAOS-3829 codex r8 O3's (accepted) fix: the RAW bytes the
// caller actually read from reportPath, hashed DIRECTLY when present. The
// ORIGINAL implementation hashed json.Marshal(report) instead -- report is
// CalibrationReport, a DELIBERATELY REDUCED mirror of the real oracle report
// JSON (CalibrationReport's own doc comment: "only the fields the
// calibration math reads"), so that re-marshalling drops every field the
// harness actually wrote but this package's struct doesn't declare, and
// reorders/reformats what survives -- the resulting hash identified a
// LOSSY RE-SERIALIZATION, never the actual file on disk, defeating the
// entire point of a content hash (a caller re-reading the artifact days
// later cannot verify it against the source file's own sha256sum, because
// the two were never computed from the same bytes). reportBytes==nil falls
// back to the original marshal-based hash -- this is the SYNTHETIC-FIXTURE
// path (every non-live test in this package builds a CalibrationReport
// directly in memory, with no real file to hash the bytes of at all; H3's
// own reportPath="fake, need not exist on disk" tests rely on this
// fallback staying non-empty), not a regression of the fix -- the fix only
// changes behavior when real bytes exist to prefer.
func NewMarginCalibrationArtifact(result MarginCalibrationResult, report CalibrationReport, opts MarginCalibrationOptions, reportPath string, reportBytes []byte) MarginCalibrationArtifact {
	artifact := MarginCalibrationArtifact{
		MarginCalibrationResult: result,
		TargetEmbedIdentity:     opts.TargetEmbedIdentity,
		TargetDimension:         opts.TargetDimension,
		TargetTau:               opts.TargetTau,
		TargetTopK:              opts.TargetTopK,
		SourceReportPath:        reportPath,
	}
	if len(reportBytes) > 0 {
		sum := sha256.Sum256(reportBytes)
		artifact.SourceReportSHA256 = hex.EncodeToString(sum[:])
	} else if encoded, err := json.Marshal(report); err == nil {
		sum := sha256.Sum256(encoded)
		artifact.SourceReportSHA256 = hex.EncodeToString(sum[:])
	}
	return artifact
}

// CalibrateMarginFromReport computes a recommended VectorMarginCommitThreshold
// (M) from one EXTENDED oracle report (CHAOS-3829 Phase 1/2(c)'s
// VectorTop1/VectorTop2/VectorMargin fields on both report.Cases (scored) and
// report.ControlCases) -- the calibration input for the ratified commit-path
// carve-out in graphrank.ResolveFromMergedCandidates: corroborated top-1 +
// margin(top1,top2) >= M.
//
// ELIGIBLE POPULATION (Phase 2(c) revision -- see the section doc comment
// above for why vectorSearchComplete is no longer part of this predicate):
//
//   - SCORED cases (report.Cases): eligible when the vector arm's top-1
//     subject was corroborated by a second mechanism (the EXACT
//     corroboration precondition the carve-out also checks) AND a margin
//     was measurable (VectorTop1 AND VectorTop2 both present, i.e. at least
//     two distinct vector-arm subjects were proposed). CORRECT when
//     VectorTop1 names the case's own expected subject (ExpectKind/
//     ExpectID), WRONG otherwise. This is a property of the RANKING (is the
//     vector arm's own top choice right), not of retrievability (a case can
//     be "hit" -- the correct answer is SOMEWHERE in the oracle's top-K --
//     while VectorTop1 is a different, wrong subject with a higher raw
//     similarity).
//   - CONTROL cases (report.ControlCases): eligible under the SAME
//     corroborated + measurable-margin predicate, and ALWAYS WRONG when
//     eligible -- a no-match control has no correct subject at all, so a
//     corroborated top-1 is wrong by definition (team-lead dispatch,
//     2026-08-16: "(corroborated AND top1 != expect_id) over scored cases
//     UNION (corroborated) over controls"). ControlsInReport/
//     ControlsWithVectorArmData/ControlsCorroborated/
//     ControlsCorroboratedWithoutMargin are recorded UNCONDITIONALLY (never
//     silently) so a report where zero controls were even corroborated
//     shows that as a measured zero, not an absent field.
//
// A case/control outside this population never reaches the carve-out in
// production either, so its margin says nothing about how M should be set --
// including one whose vector arm found only ONE distinct subject
// (VectorMargin nil): there is no competitor to measure a gap against, which
// is a different situation from a measured, arbitrarily small gap, and
// folding it in as "infinite margin" or "zero margin" would both be
// fabricating a number this report never measured.
//
// codex r6 L1 (accepted): a case/control with UsedTermFallback set is ALSO
// excluded from the eligible population, over and above the predicate
// above -- a fallback case embedded the ENTIRE question, not this
// resolution's per-term Search calls, but production's carve-out is
// TERM-SOURCED ONLY (codex r1 F3 excludes the question-level pass from
// vectorArmSimilarity entirely), so a fallback case's margin measures
// geometry the carve-out can never actually see. ExcludedFallbackCases/
// ExcludedFallbackControls record how many otherwise-eligible cases this
// cost, so a caller can see the trim rather than infer it. This is
// population-TRIMMING, not sample-hiding: if excluding every fallback case
// empties the wrong population, the existing ApplyReady=false vacuous-truth
// guard below fires on its own -- no separate guard is needed for that.
//
// codex r6 L2 (accepted): report.Controls (the harness's own declared
// control-case count) must equal len(report.ControlCases) or this function
// refuses with ErrMarginReportControlsCountMismatch before reading a single
// control case -- see that error's doc comment for why a truncated report
// must never silently shrink the negative-gate population.
//
// M: the smallest value such that EVERY WRONG-top1 margin in the eligible
// population (scored-wrong UNION control-corroborated) falls STRICTLY below
// it -- one float64 ULP above the largest observed wrong margin
// (math.Nextafter(max, +Inf); recallGateThreshold uses the same
// boundary-inclusion idiom for tau, in the opposite direction -- there the
// boundary sample must clear the gate, here it must NOT). This is a
// ZERO-TOLERANCE construction, not a percentile: CHAOS-3829's ratified
// sequencing gate requires 0 wrong commits before merge (the AC-3778-4
// re-gate), so M is picked to reject every wrong-top1 case this report ever
// measured, not merely most of them. A small WrongSampleSize means this
// empirical bound is correspondingly less trustworthy -- WrongSampleSize
// (and its WrongSampleSizeFromControls breakdown) is reported precisely so a
// caller can judge that, rather than this function silently padding the
// estimate with an invented safety margin.
//
// FAIL CLOSED (ApplyReady=false, ThresholdM nil) when no wrong-top1 case was
// measured in the eligible population at all: see ApplyReady's doc comment.
//
// A report with ZERO eligible cases in the population (not just the wrong
// half -- CorrectSampleSize+WrongSampleSize == 0) returns ErrNoMarginSamples:
// there is nothing here to calibrate M from, not even an unsafe
// recommendation.
func CalibrateMarginFromReport(report CalibrationReport, opts MarginCalibrationOptions) (MarginCalibrationResult, error) {
	if opts.TargetEmbedIdentity == "" || opts.TargetDimension <= 0 ||
		report.EmbedIdentity == "" || report.EmbedDimension <= 0 ||
		report.EmbedIdentity != opts.TargetEmbedIdentity || report.EmbedDimension != opts.TargetDimension {
		return MarginCalibrationResult{}, ErrEmbeddingIdentityMismatch
	}
	// codex r1 F7: pin report.Tau/report.TopK the SAME way identity/dimension
	// are pinned above -- see TargetTau/TargetTopK's own doc comment for why
	// a mismatch here invalidates M exactly as an identity/dimension mismatch
	// would.
	if opts.TargetTau <= 0 || opts.TargetTopK <= 0 ||
		report.Tau != opts.TargetTau || report.TopK != opts.TargetTopK {
		return MarginCalibrationResult{}, ErrMarginReportConfigMismatch
	}
	// codex r6 L2 (accepted): report.Controls is the harness's own DECLARED
	// control-case count, written in lockstep with ControlCases (oracle_live_test.go
	// increments both in the same loop iteration) -- under an honest,
	// complete write the two are ALWAYS equal by construction. A mismatch
	// means this report was truncated (a partial write, a size-limited
	// fetch, a corrupted file) after the harness produced it, silently
	// shrinking the negative-gate population to however many control cases
	// happened to survive -- the "impossible total" idiom
	// hardNegativeCaseCount's decision table uses, applied here at the
	// whole-report level: a declared count that disagrees with the actual
	// serialized array length cannot be trusted for ANY of it, so this
	// hard-fails before a single control case is read, rather than silently
	// calibrating M from a partial population.
	if report.Controls != len(report.ControlCases) {
		return MarginCalibrationResult{}, fmt.Errorf("%w: report declares %d control cases but control_cases carries %d", ErrMarginReportControlsCountMismatch, report.Controls, len(report.ControlCases))
	}
	// codex r7 M3 (accepted): EXACT symmetric check for the SCORED
	// population -- see CalibrationReport.Scored's doc comment and
	// ErrMarginReportControlsCountMismatch's identical rationale above,
	// applied here to report.Cases instead of report.ControlCases.
	if report.Scored != len(report.Cases) {
		return MarginCalibrationResult{}, fmt.Errorf("%w: report declares %d scored cases but cases carries %d", ErrMarginReportScoredCountMismatch, report.Scored, len(report.Cases))
	}
	// codex r3 H2 (accepted): report.Cases[i].VectorMargin is REDUNDANT with
	// VectorTop1/VectorTop2's own Similarity values -- the oracle harness
	// always computes it as their difference (oracle_live_test.go) and
	// never as an independently-sourced number. Trusting the STORED field
	// without cross-checking it against the values it was supposedly
	// derived from would let a corrupted, hand-edited, or buggy report
	// silently mis-measure M; same class of hazard F6 closed for the
	// harness-vs-gate arithmetic, extended here to the calibration
	// report's own internal consistency. Checked BEFORE any margin is
	// trusted, over BOTH populations (scored and control cases) -- a single
	// inconsistency anywhere hard-fails the whole calibration, the same
	// "reject the impossible cell" idiom hardNegativeCaseCount's decision
	// table uses (this package, below), scaled up to whole-report rejection
	// because a report that fails its own internal arithmetic cannot be
	// trusted piecemeal.
	if err := checkMarginConsistency(report.Cases, "Cases"); err != nil {
		return MarginCalibrationResult{}, err
	}
	if err := checkMarginConsistency(report.ControlCases, "ControlCases"); err != nil {
		return MarginCalibrationResult{}, err
	}

	var correctMargins, wrongMargins []float64
	excludedFallbackCases := 0
	for _, c := range report.Cases {
		if c.VectorTop1 == nil || c.VectorTop2 == nil || c.VectorMargin == nil {
			continue
		}
		if !c.VectorTop1.Corroborated {
			continue
		}
		// codex r6 L1 (accepted): a case that used the whole-question
		// fallback embedded the ENTIRE question text, not this resolution's
		// per-term Search calls -- but production's carve-out is
		// TERM-SOURCED ONLY (codex r1 F3 deliberately excludes the
		// question-level SearchQuestion pass from vectorArmSimilarity; a
		// term-less resolution has an empty side-map and the rescue simply
		// cannot fire). A fallback case's margin therefore measures a
		// geometry the carve-out can NEVER see in production, and must be
		// excluded from the eligible population -- counted here (not
		// silently dropped) so a caller can see how much of the report this
		// cost. If excluding every fallback case empties the wrong
		// population entirely, the EXISTING ApplyReady=false vacuous-truth
		// guard below fires on its own; no separate guard is needed for
		// that case.
		if c.UsedTermFallback {
			excludedFallbackCases++
			continue
		}
		// H2: use the RECOMPUTED margin (single authority), not the stored
		// field -- checkMarginConsistency above already proved the two are
		// numerically identical for every case that reaches this loop, so
		// this is not a behavior change, only which of two equal values is
		// read.
		margin := c.VectorTop1.Similarity - c.VectorTop2.Similarity
		if c.VectorTop1.Kind == c.ExpectKind && c.VectorTop1.CanonicalID == c.ExpectID {
			correctMargins = append(correctMargins, margin)
		} else {
			wrongMargins = append(wrongMargins, margin)
		}
	}

	// Phase 2(c): the control-sourced half of the wrong-top1 UNION -- a
	// corroborated control top-1 is wrong BY DEFINITION (no correct subject
	// exists for it to match). Every counter here is recorded
	// unconditionally, per team-lead's explicit "record as a measured fact,
	// not silently" instruction.
	controlsInReport := len(report.ControlCases)
	controlsWithVectorArmData, controlsCorroborated, controlsCorroboratedWithoutMargin := 0, 0, 0
	wrongFromControls := 0
	excludedFallbackControls := 0
	for _, c := range report.ControlCases {
		if c.VectorTop1 == nil {
			continue
		}
		controlsWithVectorArmData++
		if !c.VectorTop1.Corroborated {
			continue
		}
		controlsCorroborated++
		if c.VectorTop2 == nil || c.VectorMargin == nil {
			controlsCorroboratedWithoutMargin++
			continue
		}
		// codex r6 L1 (accepted): same exclusion as the scored-case loop
		// above -- a fallback control's margin measures whole-question
		// geometry the term-sourced-only carve-out never sees. Checked
		// AFTER controlsCorroborated/controlsCorroboratedWithoutMargin are
		// already tallied (those are raw, measured facts about the case
		// independent of eligibility, per Phase 2(c)'s own "record
		// unconditionally" discipline) and BEFORE the case can feed
		// wrongMargins.
		if c.UsedTermFallback {
			excludedFallbackControls++
			continue
		}
		// H2: recomputed margin, single authority -- see the scored-case
		// loop above.
		wrongMargins = append(wrongMargins, c.VectorTop1.Similarity-c.VectorTop2.Similarity)
		wrongFromControls++
	}

	if len(correctMargins)+len(wrongMargins) == 0 {
		return MarginCalibrationResult{}, ErrNoMarginSamples
	}

	result := MarginCalibrationResult{
		SafetyMeasured:                    len(wrongMargins) > 0,
		ReachMeasured:                     len(correctMargins) > 0,
		CorrectSampleSize:                 len(correctMargins),
		WrongSampleSize:                   len(wrongMargins),
		WrongSampleSizeFromControls:       wrongFromControls,
		ControlsInReport:                  controlsInReport,
		ControlsWithVectorArmData:         controlsWithVectorArmData,
		ControlsCorroborated:              controlsCorroborated,
		ControlsCorroboratedWithoutMargin: controlsCorroboratedWithoutMargin,
		ExcludedFallbackCases:             excludedFallbackCases,
		ExcludedFallbackControls:          excludedFallbackControls,
	}

	if !result.SafetyMeasured {
		result.Note = fmt.Sprintf(
			"M UNMEASURED: no wrong-top1 case was found in the eligible population (%d correct-top1 case(s), 0 wrong-top1; %d/%d controls corroborated) -- ThresholdM cannot be validated against a single wrong commit without vacuously trusting an empty sample. Re-run the oracle harness against a larger/different corpus slice, or size M by hand from a larger offline sample.",
			result.CorrectSampleSize, controlsCorroborated, controlsInReport,
		)
		return result, nil
	}

	maxWrong := wrongMargins[0]
	for _, m := range wrongMargins[1:] {
		if m > maxWrong {
			maxWrong = m
		}
	}
	threshold := math.Nextafter(maxWrong, math.Inf(1))
	result.ThresholdM = &threshold
	result.WrongMarginMax = &maxWrong
	result.ApplyReady = true

	clearing := 0
	if result.ReachMeasured {
		for _, m := range correctMargins {
			if m >= threshold {
				clearing++
			}
		}
		result.AchievedReach = float64(clearing) / float64(len(correctMargins))
	}

	smallSampleCaveat := ""
	if len(wrongMargins) < 3 {
		smallSampleCaveat = fmt.Sprintf(" CAVEAT: only %d wrong-top1 sample(s) -- this bound may not generalize past this report's small sample; re-run against a broader corpus before treating M as final.", len(wrongMargins))
	}
	controlBreakdown := fmt.Sprintf(" (%d of %d wrong-top1 sample(s) from a corroborated control.)", wrongFromControls, len(wrongMargins))
	switch {
	case !result.ReachMeasured:
		result.Note = fmt.Sprintf(
			"M=%.6f rejects all %d measured wrong-top1 case(s); no correct-top1 case was measured in the eligible population, so AchievedReach is unmeasured (reported as 0).%s%s",
			threshold, len(wrongMargins), controlBreakdown, smallSampleCaveat,
		)
	case clearing == 0:
		result.Note = fmt.Sprintf(
			"M=%.6f rejects all %d measured wrong-top1 case(s), but 0 of %d correct-top1 case(s) clear it -- this carve-out would have zero reach at the recommended M on this sample.%s%s",
			threshold, len(wrongMargins), len(correctMargins), controlBreakdown, smallSampleCaveat,
		)
	default:
		result.Note = fmt.Sprintf(
			"M=%.6f rejects all %d measured wrong-top1 case(s); %d of %d correct-top1 case(s) (%.1f%%) clear it.%s%s",
			threshold, len(wrongMargins), clearing, len(correctMargins), result.AchievedReach*100, controlBreakdown, smallSampleCaveat,
		)
	}
	return result, nil
}

// hardNegativeCaseCount is codex round-5 FIX B's EXHAUSTIVE decision table
// for ONE scored case's near-duplicate count -- the value CalibrateFromReport
// feeds into percentileInt's K-sizing distribution. Restructured as an
// explicit table (not nested ad-hoc conditionals) after round-4's
// len(similarities)>0 guard turned out to silently bypass BOTH the
// total-consumption AND the refusal branch for a specific, reachable input
// shape it never considered: ACR_TEST_ORACLE_HARD_NEGATIVES=0 makes the
// harness stamp truncated=true with an EMPTY capped list and a VALID
// nonzero total -- the guard's blanket "empty list means safe, count=0"
// assumption was wrong for exactly that cell, silently reporting
// KApplyReady=true with K unchanged despite arbitrarily many real
// near-duplicates.
//
// Dimensions and every reachable combination (truncated is already resolved
// from the tri-state nil/true/false HardNegativesTruncated field to a bool
// one level up in CalibrateFromReport -- nil resolves to true, "assume
// truncated"; this function does not re-derive that, it only consumes the
// resolved value):
//
//	truncated | list      | saturated  | verdict
//	----------|-----------|------------|--------------------------------------
//	false     | empty     | (n/a)      | SAFE: count=0. Genuinely zero hard
//	          |           |            | negatives were harvested and the
//	          |           |            | harness explicitly said this is
//	          |           |            | complete -- real density signal
//	          |           |            | ("nothing crowds this correct
//	          |           |            | answer"), trusted as-is.
//	false     | nonempty  | either     | SAFE: count=local. The harness
//	          |           |            | explicitly reported this harvest as
//	          |           |            | complete -- trusted even if every
//	          |           |            | local entry happens to clear tau
//	          |           |            | (round-2 P2's original intent,
//	          |           |            | round-4's explicit-false test pins
//	          |           |            | this).
//	true      | empty     | (n/a)      | UNSAFE by itself: an empty list under
//	          |           |            | "truncated" carries ZERO local
//	          |           |            | information (there is nothing to
//	          |           |            | check saturation against) -- this is
//	          |           |            | the round-5 regression cell.
//	          |           |            | Resolved via total (see below).
//	true      | nonempty  | no         | SAFE: count=local. dedupeHardNegatives
//	          |           |            | sorts the full harvest DESCENDING
//	          |           |            | before capping, so if the smallest
//	          |           |            | SERIALIZED entry already falls
//	          |           |            | at-or-below tau, every entry beyond
//	          |           |            | the cap does too (they can only be
//	          |           |            | smaller) -- the sort-order-exactness
//	          |           |            | shortcut, count is already exact
//	          |           |            | regardless of the cap.
//	true      | nonempty  | yes        | UNSAFE by itself: every serialized
//	          |           |            | entry clears tau, so entries beyond
//	          |           |            | the cap could ALSO clear it -- the
//	          |           |            | true count is unknown, not merely
//	          |           |            | small (round-2 P2's original case).
//	          |           |            | Resolved via total (see below).
//
// The two UNSAFE rows share ONE resolution (round-3 P2's cross-tau guard):
// aboveTauCount is trusted ONLY when present, measured at EXACTLY this run's
// recommended tau (reportTau == tau), AND (codex round-8 P2) PLAUSIBLE --
// neither negative nor smaller than local, the count of serialized entries
// this function just verified independently clear tau. A total measured at a
// DIFFERENT tau (typically the report's original, higher default floor)
// cannot see negatives sitting between the two floors, so trusting it
// unconditionally would silently under-size K again; a negative or
// sub-`local` total is not a measurement-integrity nuance at all, it is
// IMPOSSIBLE on its face -- aboveTauCount is documented (CalibrationCase's
// doc comment) as the total count above tau across the FULL harvest, of
// which the serialized, capped `similarities` this function counted `local`
// entries out of is a PREFIX (dedupeHardNegatives sorts descending before
// capping); the full-harvest total can never be smaller than a count taken
// from a prefix of it, and never negative at all. Present-matching-AND-
// plausible -> count=total, sufficient=true ("truncated + total -> size
// from the total"). Every other combination -> count=0, sufficient=false
// ("truncated without a [matching, plausible] total -> refuse"), and
// CalibrateFromReport's caller sees KApplyReady=false -- a malformed report
// must never silently size K off a number that cannot be correct.
func hardNegativeCaseCount(similarities []float64, tau float64, truncated bool, aboveTauCount *int, reportTau float64) (count int, sufficient bool) {
	local := 0
	for _, s := range similarities {
		// A hard negative only competes for a top-K slot if it would
		// actually be RETRIEVED at tau -- aboveSimilarityFloor, the same
		// strict predicate production's vectorSearchNodesWithOverFetch
		// applies (see the doc comment there for why this must be the
		// single shared authority).
		if aboveSimilarityFloor(s, tau) {
			local++
		}
	}
	// saturated is deliberately false whenever the list is empty (rather
	// than the vacuously-true 0==0) -- see the table above: an empty list
	// is its OWN row, not a degenerate case of saturation, precisely
	// because treating it as such was round-4's bug.
	saturated := len(similarities) > 0 && local == len(similarities)
	needsTotal := truncated && (len(similarities) == 0 || saturated)
	if !needsTotal {
		return local, true
	}
	if aboveTauCount != nil && reportTau == tau {
		total := *aboveTauCount
		// codex round-8 P2: validate the total is PLAUSIBLE before trusting
		// it -- see the function doc comment's "present-matching-AND-
		// plausible" resolution. Falls through to the shared refusal below
		// on either impossible shape, exactly like "no matching total at
		// all".
		if total >= 0 && total >= local {
			return total, true
		}
	}
	return 0, false
}

// overFetchMultiplierOrUnchanged maps a computed multiplier of 1 (no
// widening recommended) to the RetrievalPolicy zero-value sentinel for
// "unchanged", so a caller populating retrieval_policy.go from this result
// gets the same byte-identical-default behavior a hand-written 0 entry
// would, rather than an explicit-but-redundant 1.
func overFetchMultiplierOrUnchanged(multiplier int) int {
	if multiplier <= 1 {
		return 0
	}
	return multiplier
}

// recallGateThreshold returns (tau, achievedRecall) for samples under the
// nearest-rank recall-gate method described in CalibrateFromReport's doc
// comment. samples must be non-empty.
//
// codex round-7 P2 ENDS the quantile-boundary epsilon/ULP guessing game
// STRUCTURALLY -- this was round 3 of the same enumeration: round 1 used a
// single fixed epsilon (1e-9) that was ~1e6x coarser than genuine float64
// noise, silently swallowing a genuinely-different, merely-close
// TargetRecall; round 2 replaced it with a magnitude-relative ULP-scale
// snap, which then OVERCORRECTED in the opposite direction for a
// TargetRecall one ULP ABOVE a clean decimal (Nextafter(0.9, +Inf)), where
// the raw product is already a few ULPs BELOW 1.0 for entirely legitimate
// reasons and the snap wrongly rounded it up anyway. No predictive
// epsilon/ULP reasoning survives here at all now: excludeCount starts from
// the SIMPLEST possible candidate (math.Round, no tolerance window, no
// judgment call about "is this noise") and the documented invariant
// (AchievedRecall >= TargetRecall, ALWAYS) is enforced POST-HOC BY
// MEASUREMENT below -- while the ACTUAL achieved recall (recallAtTau, the
// SAME strict predicate production applies) falls short of the target, the
// candidate is relaxed by one and re-measured. This makes the invariant
// true BY CONSTRUCTION for every float64 input, not by predicting which
// way float64 rounding went: the loop can only ever REDUCE excludeCount
// (bounded below by 0), and excludeCount=0 always achieves recall 1.0 >=
// any valid TargetRecall in (0,1], so both termination and correctness
// follow from the loop's own shape.
//
// tau is NOT the nearest-rank sample's exact value -- it is nudged one
// float64 ULP below it via math.Nextafter (codex round-1 P1: production's
// floor predicate, aboveSimilarityFloor, is STRICT, so a candidate whose
// similarity exactly equals tau is DROPPED, never retrieved; setting tau to
// the sample's own value therefore claimed that sample -- and made a 100%
// TargetRecall provably unachievable, since the minimum sample IS tau and
// tau can never clear itself). Nextafter is exact regardless of magnitude,
// and moves tau just far enough that the boundary sample -- and any sample
// tied with it -- legitimately clears the SAME strict predicate production
// applies, which is what recallAtTau counts directly (never derived from
// rank position, so ties at the boundary are never split across
// "counted"/"not counted" by an arbitrary index cut).
func recallGateThreshold(samples []float64, targetRecall float64) (float64, float64) {
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	n := len(sorted)
	excludeCount := int(math.Round((1 - targetRecall) * float64(n)))
	if excludeCount >= n {
		excludeCount = n - 1
	}
	if excludeCount < 0 {
		excludeCount = 0
	}
	tau := math.Nextafter(sorted[excludeCount], math.Inf(-1))
	achievedRecall := recallAtTau(sorted, tau)
	for achievedRecall < targetRecall && excludeCount > 0 {
		excludeCount--
		tau = math.Nextafter(sorted[excludeCount], math.Inf(-1))
		achievedRecall = recallAtTau(sorted, tau)
	}
	return tau, achievedRecall
}

// recallAtTau is the exact measurement recallGateThreshold's post-hoc
// invariant-enforcement loop uses: the ACTUAL fraction of sorted (assumed
// already ascending) that clears tau under aboveSimilarityFloor, the SAME
// strict predicate production's vectorSearchNodesWithOverFetch applies --
// so this can never disagree with what production would actually retrieve,
// and never relies on rank position or any float64 rounding assumption.
func recallAtTau(sorted []float64, tau float64) float64 {
	survived := 0
	for _, s := range sorted {
		if aboveSimilarityFloor(s, tau) {
			survived++
		}
	}
	return float64(survived) / float64(len(sorted))
}

// percentileInt returns the CONVENTIONAL nearest-rank fraction-th percentile
// of counts: ascending sort, rank = ceil(fraction*n) (clamped into
// [1, n]), value = sorted[rank-1] -- the value at or below which
// approximately `fraction` of the samples sit. This is the ordinary
// statistical percentile, and deliberately NOT the same direction as
// recallGateThreshold's method (that one solves "at least `fraction`
// samples are >= the threshold", a LOW value; this solves "at least
// `fraction` samples are <= the threshold", a HIGH value) -- the two
// callers need opposite ends of their respective distributions. Returns 0
// for an empty input (no cases carried hard negatives at all -- no density
// signal, no widening).
//
// No epsilon/ULP correction here (codex round-7 P2 removed the shared
// snapNearIntegerBoundary helper this used to call): unlike
// recallGateThreshold, this function has no documented, tested correctness
// invariant to defend -- it is a density ESTIMATE K is sized from, not a
// hard guarantee -- so a plain ceil is the simplest, most defensible
// choice, and fraction is always CalibrateFromReport's own hardcoded 0.90
// constant, for which fraction*n is exact float64 arithmetic at every n
// this package's tests exercise (verified: 0.9*10, *20, *30... all land on
// the exact integer, no representation error to correct for).
func percentileInt(counts []int, fraction float64) int {
	if len(counts) == 0 {
		return 0
	}
	sorted := append([]int(nil), counts...)
	sort.Ints(sorted)
	n := len(sorted)
	rank := int(math.Ceil(fraction * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}
