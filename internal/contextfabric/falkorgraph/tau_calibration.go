package falkorgraph

import (
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
	// HardNegativeRejectRate falls below NegativeGateRejectThreshold. tau/K
	// above remain valid RECALL-CHANNEL diagnostics either way -- this does
	// NOT reopen tau as a precision knob, and CHAOS-3834's ratified design is
	// unchanged: tau is still picked from S+ recall alone (recallGateThreshold),
	// never re-derived from the negative pool. What changes is that a caller
	// can no longer treat Policy as apply-ready without checking this field
	// first: a recall-gate tau that also admits most impostors (the shipped
	// distribution's own reject rate is well below the threshold) needs an
	// explicit human decision that precision will come from hybrid ranking +
	// corroboration downstream, not a silent pass from this tool. The
	// hand-written calibratedIdentityText2Large entry in retrieval_policy.go
	// IS that explicit human decision -- it is not auto-applied output of
	// this function and is therefore not itself gated by ApplyReady; see its
	// doc comment for the sequencing-gate ruling recorded on CHAOS-3834.
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
	// ErrInvalidTargetRecall reports a TargetRecall outside (0, 1].
	ErrInvalidTargetRecall = errors.New("calibration target recall must be in (0, 1]")
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
	if opts.TargetRecall <= 0 || opts.TargetRecall > 1 {
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

	applyReady := rejectRate >= NegativeGateRejectThreshold
	note := fmt.Sprintf(
		"negative gate PASSED: hard-negative reject rate %.4f clears the %.4f threshold; recall-gate tau is apply-ready",
		rejectRate, NegativeGateRejectThreshold,
	)
	if !applyReady {
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
// aboveTauCount is trusted ONLY when present AND measured at EXACTLY this
// run's recommended tau (reportTau == tau) -- a total measured at a
// DIFFERENT tau (typically the report's original, higher default floor)
// cannot see negatives sitting between the two floors, so trusting it
// unconditionally would silently under-size K again. Present-and-matching
// -> count=total, sufficient=true ("truncated + total -> size from the
// total"). Otherwise -> count=0, sufficient=false ("truncated without a
// [matching] total -> refuse"), and CalibrateFromReport's caller sees
// KApplyReady=false.
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
		return *aboveTauCount, true
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

// quantileEpsilon nudges a floor()/ceil() boundary computed from a floating-
// point product back onto the intended integer when the "exact" fraction
// (e.g. 0.90 of 10 samples = 9.0 exactly, in real-number terms) lands just
// off an integer due to float64 rounding (0.9*10 can evaluate to
// 9.000000000000002 or 8.999999999999998 depending on the inputs). Both
// recallGateThreshold and percentileInt need this correction, in the
// direction that favors their own rounding function (floor adds it,
// ceil subtracts it), so an intended-exact boundary always resolves to the
// same integer regardless of which side of it float64 happened to land on.
const quantileEpsilon = 1e-9

// recallGateThreshold returns (tau, achievedRecall) for samples under the
// nearest-rank recall-gate method described in CalibrateFromReport's doc
// comment. samples must be non-empty.
//
// tau is NOT the nearest-rank sample's exact value -- it is nudged one
// float64 ULP below it via math.Nextafter (codex round-1 P1: production's
// floor predicate, aboveSimilarityFloor, is STRICT, so a candidate whose
// similarity exactly equals tau is DROPPED, never retrieved; setting tau to
// the sample's own value therefore claimed that sample -- and made a 100%
// TargetRecall provably unachievable, since the minimum sample IS tau and
// tau can never clear itself). Nextafter is exact regardless of magnitude
// (no epsilon-collision risk the way a fixed constant would have), and moves
// tau just far enough that the boundary sample -- and any sample tied with
// it -- legitimately clears the SAME strict predicate production applies,
// which is what achievedRecall below counts directly rather than deriving
// from rank position (so ties at the boundary are never split across
// "counted"/"not counted" by an arbitrary index cut).
func recallGateThreshold(samples []float64, targetRecall float64) (float64, float64) {
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	n := len(sorted)
	excludeCount := int(math.Floor((1-targetRecall)*float64(n) + quantileEpsilon))
	if excludeCount >= n {
		excludeCount = n - 1
	}
	if excludeCount < 0 {
		excludeCount = 0
	}
	tau := math.Nextafter(sorted[excludeCount], math.Inf(-1))
	survived := 0
	for _, s := range sorted {
		if aboveSimilarityFloor(s, tau) {
			survived++
		}
	}
	achievedRecall := float64(survived) / float64(n)
	return tau, achievedRecall
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
func percentileInt(counts []int, fraction float64) int {
	if len(counts) == 0 {
		return 0
	}
	sorted := append([]int(nil), counts...)
	sort.Ints(sorted)
	n := len(sorted)
	rank := int(math.Ceil(fraction*float64(n) - quantileEpsilon))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}
