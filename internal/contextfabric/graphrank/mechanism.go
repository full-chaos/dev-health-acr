package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The corroboration band (CHAOS-3778 / AC-3778-2 + AC-3778-3, ruled approved
// by the orchestrator 2026-08-13).
//
// AC-3778-2 wants a 25-point rise in the CORRECT-COMMIT rate. AC-3778-3 says a
// vector hit alone never commits a subject. Both can hold at once only if a
// vector hit can be CORROBORATED by a second, independent mechanism and commit
// on the strength of the pair. This band is where that happens.
//
// The two edges are chosen against the shipped commit thresholds in
// ResolveFromMergedCandidates, which CHAOS-3778 does not change:
//
//   - CorroboratedFloor (0.72) is exactly the lone-candidate gate, so a
//     corroborated candidate CAN auto-commit when it is unopposed. That is
//     where the AC-3778-2 lift comes from.
//   - CorroboratedCeiling (0.86) is strictly below the 0.88 top-of-two gate,
//     so TWO corroborated candidates can never auto-commit -- they still fall
//     to clarification. That preserves the orchestrator's 2026-08-13 ruling
//     that falling to clarification under genuine ambiguity is intended
//     behavior, not a regression.
//
// The vector band itself ([0.50, 0.70], falkorgraph.vectorRelevanceFloor /
// vectorRelevanceCeiling) sits strictly BELOW CorroboratedFloor, so a
// vector-ONLY candidate cannot reach the lone-candidate gate by arithmetic --
// AC-3778-3 holds without a single special case anywhere in this package.
const (
	CorroboratedFloor   = 0.72
	CorroboratedCeiling = 0.86
)

// MaxMatchMechanisms is the size of the closed enum. Used as the denominator
// of the mechanism-count term below, so adding an enum member deliberately
// changes the shape of the curve and cannot be done silently.
const MaxMatchMechanisms = 6

// canonicalMechanismOrder fixes the serialization order of a merged mechanism
// set, so two resolutions that found the same subject by the same mechanisms
// in a different ORDER produce byte-identical results. An InvestigationResult
// is immutable and content-addressed downstream (CHAOS-3782 answer reuse keys
// on it), so a set whose order depended on which query happened to return
// first would defeat reuse for no reason.
var canonicalMechanismOrder = []contextfabric.MatchMechanism{
	contextfabric.MatchExact,
	contextfabric.MatchAlias,
	contextfabric.MatchProviderKey,
	contextfabric.MatchLexical,
	contextfabric.MatchVector,
	contextfabric.MatchTraversalParent,
}

// MergeMechanisms unions two mechanism sets into canonical order, dropping any
// value the contract does not recognize.
//
// Dropping rather than passing through is deliberate: DistinctMechanismCount
// feeds a commit decision, so an unrecognized value that survived here would
// count as another distinct mechanism and could push a candidate over the
// lone-candidate gate on the strength of a typo. The contract's enum is the
// authority (contractsv1.ValidContextFabricSubjectMatchMechanism), not this
// package.
func MergeMechanisms(sets ...[]contextfabric.MatchMechanism) []contextfabric.MatchMechanism {
	present := make(map[contextfabric.MatchMechanism]struct{}, MaxMatchMechanisms)
	for _, set := range sets {
		for _, mechanism := range set {
			if !contractsv1.ValidContextFabricSubjectMatchMechanism(mechanism) {
				continue
			}
			present[mechanism] = struct{}{}
		}
	}
	if len(present) == 0 {
		return nil
	}
	merged := make([]contextfabric.MatchMechanism, 0, len(present))
	for _, mechanism := range canonicalMechanismOrder {
		if _, ok := present[mechanism]; ok {
			merged = append(merged, mechanism)
		}
	}
	return merged
}

// DistinctMechanismCount counts recognized, deduplicated mechanisms.
//
// "Distinct" means distinct ENUM MEMBERS, not distinct queries or distinct
// matched text. In particular a lexical hit and a vector hit over the SAME
// search_text corpus still count as two distinct mechanisms, and that is not
// double-counting: the corpus is deliberately identical on both paths (see
// docs/design/context-fabric-vector-retrieval.md §3) precisely so that the two
// paths differ ONLY in MECHANISM -- lexical answers "does this candidate
// literally contain the query's terms", vector answers "is this candidate's
// meaning close to the query's meaning". Those are independent failure modes:
// a paraphrase defeats the lexical test while passing the vector one, and a
// shared-vocabulary false friend does the reverse. Agreement between two
// independent tests over one corpus is exactly the evidence corroboration is
// supposed to measure. Had the two paths searched DIFFERENT corpora, agreement
// would confound "two mechanisms agree" with "two corpora contain it", which
// is the reading that WOULD be double-counting.
func DistinctMechanismCount(mechanisms []contextfabric.MatchMechanism) int {
	return len(MergeMechanisms(mechanisms))
}

// CorroboratedConfidence maps a candidate's own single-mechanism ("base")
// confidence and its merged mechanism set into a final confidence.
//
// It is applied EXACTLY ONCE per candidate, in ResolveFromMergedCandidates,
// after the full candidate set is assembled and before the commit decision --
// never incrementally during the merge. Applying it during the merge would
// feed an already-corroborated value back in as a base on the next merge,
// compounding a value that is only meaningful in the single-mechanism domain.
//
// Behavior, in order:
//
//  1. base >= 1 returns 1 unchanged. An exact canonical/alias/hint match is
//     already maximal and must never be DEMOTED into the corroborated band by
//     the act of also being found some other way.
//
//  2. Fewer than two distinct mechanisms returns base unchanged. A
//     single-mechanism candidate keeps whatever band its own adapter
//     normalized it into (lexical [0.50, 0.75], vector [0.50, 0.70]) --
//     AC-3778-3's "a vector hit alone never commits" is this line.
//
//  3. Two or more distinct mechanisms map into [CorroboratedFloor,
//     CorroboratedCeiling] by a function that is monotone non-decreasing in
//     BOTH base confidence and mechanism count, weighted evenly:
//
//     strength   := 0.5*base + 0.5*(distinct-2)/(MaxMatchMechanisms-2)
//     confidence := CorroboratedFloor + (Ceiling-Floor)*strength
//
//     Monotone in both arguments is the order-soundness property AC-3778-0
//     established for the lexical ladder, extended across this arm: a
//     candidate that is corroborated by strictly more mechanisms, or by the
//     same mechanisms with a strictly stronger base, never scores lower.
//
// The final guard returns base when the corroborated value would somehow be
// lower. Corroboration is evidence FOR a candidate; it must never cost one
// confidence. This guard is REACHABLE today, not merely defensive: an exact
// label match reached through observation traversal carries base 0.85
// (NodeCandidate's exact-match 1.0, times TraverseObservationToSubject's 0.85
// one-hop discount) with two distinct mechanisms, whose corroborated value is
// 0.7795 -- lower than the 0.85 the candidate had already earned on the
// strength of the exact match alone. The guard keeps 0.85. See
// TestCorroborationNeverDemotesAStrongerSingleMechanismCandidate.
//
// A consequence of that guard, stated so it is not mistaken for a leak: this
// function's output is bounded by max(base, CorroboratedCeiling), NOT by
// CorroboratedCeiling alone. A base at or above the 0.88 top-of-two gate
// therefore passes through at its own value. That is correct -- such a
// candidate reached the gate on its own single-mechanism strength and
// corroboration contributed nothing to it -- and it is unreachable on the
// shipped paths anyway, where the single-mechanism bands cap at 0.75
// (lexical), 0.70 (vector), and 0.85 (traversal of an exact match), and an
// exact hint at 1.0 returns early above. What corroboration must never do is
// LIFT a candidate to the gate; see
// TestCorroborationNeverLiftsACandidateToTheTopOfTwoGate.
func CorroboratedConfidence(mechanisms []contextfabric.MatchMechanism, base float64) float64 {
	base = Clamp(base)
	if base >= 1 {
		return 1
	}
	distinct := DistinctMechanismCount(mechanisms)
	if distinct < 2 {
		return base
	}
	mechanismWeight := Clamp(float64(distinct-2) / float64(MaxMatchMechanisms-2))
	strength := Clamp(0.5*base + 0.5*mechanismWeight)
	corroborated := CorroboratedFloor + (CorroboratedCeiling-CorroboratedFloor)*strength
	if corroborated < base {
		return base
	}
	return corroborated
}

// MergeCandidates combines two findings of the SAME subject.
//
// Before CHAOS-3778 this was a plain "keep whichever has the higher
// confidence" in ResolveSubjects, which threw away the loser entirely -- and
// with it the single most useful signal available here: that two independent
// mechanisms agreed on this subject. The higher-confidence finding still
// supplies the spine (receipt, state, and the base confidence), so every
// pre-CHAOS-3778 behavior is preserved for a single-mechanism candidate; what
// is new is that the loser's MECHANISMS, matched terms, reasons, and evidence
// references survive the merge instead of being discarded.
//
// Confidence stays the MAX of the two, which is the base confidence
// CorroboratedConfidence later consumes -- this function never applies the
// corroborated band itself.
func MergeCandidates(current, incoming contextfabric.SubjectCandidate) contextfabric.SubjectCandidate {
	winner, loser := current, incoming
	if incoming.Confidence > current.Confidence {
		winner, loser = incoming, current
	}
	merged := winner
	merged.MatchMechanisms = MergeMechanisms(winner.MatchMechanisms, loser.MatchMechanisms)
	merged.MatchedTerms = UniqueSorted(append(append([]string(nil), winner.MatchedTerms...), loser.MatchedTerms...))
	merged.MatchReasons = mergeReasons(winner.MatchReasons, loser.MatchReasons)
	merged.EvidenceRefIDs = UniqueSorted(append(append([]string(nil), winner.EvidenceRefIDs...), loser.EvidenceRefIDs...))
	return merged
}

// mergeReasons unions the human-readable reasons, preserving the winner's
// first (it describes the stronger match) and keeping the result bounded and
// deterministic. MatchReasons has a minItems of 1 in the contract, so an empty
// union falls back to the winner's own slice rather than producing an invalid
// candidate.
func mergeReasons(winner, loser []string) []string {
	seen := make(map[string]struct{}, len(winner)+len(loser))
	merged := make([]string, 0, len(winner)+len(loser))
	for _, reason := range append(append([]string(nil), winner...), loser...) {
		if reason == "" {
			continue
		}
		if _, exists := seen[reason]; exists {
			continue
		}
		seen[reason] = struct{}{}
		merged = append(merged, reason)
	}
	if len(merged) == 0 {
		return winner
	}
	return merged
}
