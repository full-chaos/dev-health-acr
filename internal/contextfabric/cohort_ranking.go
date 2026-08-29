package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
)

// RankingFormulaVersion travels in every CohortRankedEvent (telemetry) and
// pins the exact scoring behavior cohort_ranking_test.go asserts against. A
// later formula revision (new weights, a new signal family, a changed
// threshold) MUST bump this string -- the same discipline
// cf-standing-rules.md's "prompt changes are behavior changes" applies to
// every model-facing text, applied here to a deterministic function: the
// version is what makes a formula change a counted, diagnosable event
// instead of a silent drift in what "struggling" means.
//
// This formula is the RATIFIED design from
// docs/design/context-fabric-subject-model-and-cohort-answers.md §5 (four
// codex review rounds, merged to main as #316) -- not the earlier plan
// draft. Where the two disagree, this file follows the doc: Rank stays
// pool-order-only (AttentionRank is the new score-derived order), workload
// pressure is min-max normalized within the cohort (not z-scored), mix
// shift uses a SIGNED per-theme delta with a three-way direction label
// (not a magnitude sum with a two-way guess), readiness/workload aggregate
// the WORST across a member's multiple scope-partitioned facts, and
// deficiency severity's "no fired rules" case is a defined available-zero
// exception, never a blanket "missing".
// v2 (CHAOS-4398 PR3, design doc §8): a member's Score is no longer set
// whenever ANY signal weight is available -- it now requires available
// weight >=50 of the 100-point total (Outcome qualified/provisional).
// Below that (Outcome insufficient_evidence/not_applicable), Score/
// RankingBasis/Drivers all stay empty, replacing the old "any nonzero
// weight gets a real score" behavior. A real, counted formula change, not
// a contract-only addition -- see cf-standing-rules.md's own mandate on
// this constant.
const RankingFormulaVersion = "cohort-ranking.v2"

// Top-level signal-family names -- closed vocabulary. These are exactly the
// values RankCohort can add to a member's RankingBasis, and exactly the keys
// CohortRankedEvent.SignalsAvailable is keyed by.
const (
	RankingSignalInvestmentMix      = "investment_mix"
	RankingSignalHealthRisk         = "health.compounding_risk"
	RankingSignalDeficiencySeverity = "operational_deficiencies.severity"
	RankingSignalReadinessGap       = "readiness.coverage_gap"
	RankingSignalWorkloadPressure   = "workload.forecast_pressure"
)

// Investment-mix sub-signal driver labels -- closed vocabulary, threshold-
// crossing flags only, appended to RankingBasis alongside the plain
// "investment_mix" family name whenever they fire. The PATTERN (a
// deterministic, threshold-based, closed-vocabulary label -- never model
// prose) is borrowed from ops's investment_mix_explain.py `quality_drivers`
// list; the labels and thresholds themselves are new to this formula.
const (
	DriverReactiveShareHigh         = "investment_mix.reactive_share_high"
	DriverDeliberateShareLow        = "investment_mix.deliberate_share_low"
	DriverMixConcentrated           = "investment_mix.mix_concentrated"
	DriverMixShiftTowardOperational = "investment_mix.mix_shift_toward_operational"
	DriverMixShiftTowardFeature     = "investment_mix.mix_shift_toward_feature"
	// DriverMixShiftOther (subject-model-and-cohort-answers.md §5) fires
	// when the largest-magnitude signed theme delta belongs to maintenance,
	// quality, or risk -- the two-label vocabulary an earlier plan draft
	// used could not represent a shift that moves mass into neither named
	// theme.
	DriverMixShiftOther = "investment_mix.mix_shift_other"
)

// ConcentrationMethodMaxShare (CHAOS-4398 PR3) names the CURRENT
// concentration measure investmentMixSignal uses (the largest single theme
// share). CHAOS-4414 will add an "hhi" method computing a real
// Herfindahl-Hirschman Index instead -- both are closed-vocabulary values
// of the SAME field, not a rename, so a consumer switching over reads a
// changed method value rather than a changed field name.
const ConcentrationMethodMaxShare = "max_share"

// Top-level formula weights (design doc §5). Sum to 100 by construction;
// Score renormalizes over whichever subset is available for a given member
// (missing -> excluded from BOTH the numerator and the denominator, never
// zero-filled -- CHAOS-3781 degrade-not-fabricate posture applied to a
// deterministic score).
const (
	weightInvestmentMix      = 30.0
	weightHealthRisk         = 25.0
	weightDeficiencySeverity = 20.0
	weightReadinessGap       = 15.0
	weightWorkloadPressure   = 10.0
)

// Investment-mix term-1 sub-weights and thresholds (design doc §5's
// sub-formula table). The four sub-weights sum to exactly 1.0, so the
// term-1 value this produces is already in [0,1] before the top-level
// formula applies weightInvestmentMix to it. Every threshold here is a
// PROPOSED starting point, not yet calibrated against a wide org set (this
// PR reports real values for exactly one real team with investment data,
// per the PR body) -- report real values before treating them as
// load-bearing.
const (
	subWeightReactiveShare   = 0.35
	subWeightDeliberateShare = 0.30
	subWeightConcentration   = 0.15
	subWeightMixShift        = 0.20

	reactiveShareThreshold   = 0.40
	deliberateShareThreshold = 0.20
	concentrationThreshold   = 0.55
	mixShiftThreshold        = 0.15
)

// Canonical investment theme keys (ops/src/dev_health_ops/investment_taxonomy.py's
// THEMES -- fixed, no synonyms/overrides, AGENTS.md). Mirrored here as the
// exact FactValue field-name suffixes the FactInvestment producer
// (devhealthfacts/investment.go's readTeamThemeMix) writes and this file
// reads.
const (
	ThemeFeatureDelivery = "feature_delivery"
	ThemeOperational     = "operational"
	ThemeMaintenance     = "maintenance"
	ThemeQuality         = "quality"
	ThemeRisk            = "risk"
)

// canonicalThemes is the fixed taxonomy iteration order -- ALSO the mix-
// shift tie-break order the design doc requires (§5, "Tie-break (P2)"): two
// themes landing on the same largest positive delta resolve to whichever
// comes first in this array, never map iteration or row order.
var canonicalThemes = [...]string{ThemeFeatureDelivery, ThemeOperational, ThemeMaintenance, ThemeQuality, ThemeRisk}

// FactFieldTheme/FactFieldPriorTheme/FactFieldThemeQualityBugfix name the
// FactInvestment fields the theme-mix producer writes for a team subject:
// "theme_<canonical theme>" for the current window, "prior_theme_<theme>"
// for the prior comparable window (mix-shift's own explicit second query,
// CHAOS-4040 -- never a model-inferred date), and "theme_quality_bugfix"
// for the one tracked subcategory share (quality.bugfix) the reactive-share
// sub-signal needs.
func FactFieldTheme(theme string) string      { return "theme_" + theme }
func FactFieldPriorTheme(theme string) string { return "prior_theme_" + theme }

const FactFieldThemeQualityBugfix = "theme_quality_bugfix"

// RankCohort computes a deterministic attention score for every member of
// cohort from already-read canonical facts and the SAME investigation's
// fact-read Coverage (needed for the per-family availability rule below),
// and returns the mutated cohort. It NEVER reads a fact itself, calls a
// model, or reorders cohort.Members / touches Rank -- the engine wires it
// between ReadFacts and Synthesize (see engine.go's Investigate), the same
// "server computes, model narrates" discipline attachCanonicalRows already
// applies to ClaimedFact.Rows.
//
// cohort == nil or len(cohort.Members) == 0 is a no-op (offers-only
// discovery, or a request that never reached a real cohort) -- returns
// cohort unchanged, so a caller can call this unconditionally without its
// own nil/empty guard.
//
// Members stays in POOL order always (design doc §4: Rank is unchanged by
// this ticket). RankCohort instead sets AttentionRank (1 = highest Score,
// ties broken by pool order) on each member IN PLACE.
//
// Purely a function of cohort, facts, and coverage -- no clock, no I/O, no
// model call -- so it is deterministic and safe to call inline in
// Engine.Investigate.
// cohortMemberSignalCitations carries every signalCitation RankCohort
// computed but did NOT mint into a ClaimedFact -- keyed [member subject
// CanonicalID][signal family name]. Team-lead ruling (CHAOS-4398 PR3b):
// "minting follows citation, not ranking" -- a ClaimedFact is minted ONLY
// for a driver a narrated ContextFabricDriverJudgment actually cites
// (narrateCohortDriverJudgments, post-synthesis), never for every available
// signal regardless of whether anything ever narrates it (that unconditional
// version measured ~708 KB worst-case for a 250-member fully-qualified
// cohort, ~2.8x the 256 KB default budget, from provenance alone). RankCohort
// itself never mints; it only hands this map forward so the caller
// (engine.go) can thread it to narrateCohortDriverJudgments after synthesis.
type cohortMemberSignalCitations map[string]map[string]*signalCitation

// RankCohort's third return value is cohortMemberSignalCitations -- see
// that type's own doc comment for why RankCohort computes but does not
// mint citations, and engine.go's own call site comment for how it
// threads this map to narrateCohortDriverJudgments after synthesis.
func RankCohort(cohort *Cohort, facts []CanonicalFact, coverage Coverage) (*Cohort, CohortRankedEvent, cohortMemberSignalCitations) {
	event := CohortRankedEvent{FormulaVersion: RankingFormulaVersion, SignalsAvailable: map[string]int{}, OutcomeCounts: map[string]int{}}
	if cohort == nil || len(cohort.Members) == 0 {
		return cohort, event, nil
	}

	bySubject := make(map[string][]CanonicalFact, len(cohort.Members))
	for _, fact := range facts {
		key := canonicalFactSubjectKey(fact.Subject)
		bySubject[key] = append(bySubject[key], fact)
	}

	// Workload pressure is min-max normalized WITHIN the cohort (§5), so
	// every member's own worst-case (longest) forecast is gathered BEFORE
	// any per-member score is computed.
	rawWorkload := make(map[string]float64, len(cohort.Members))
	haveWorkload := make(map[string]bool, len(cohort.Members))
	workloadCitation := make(map[string]*signalCitation, len(cohort.Members))
	for _, member := range cohort.Members {
		key := canonicalFactSubjectKey(member.Subject)
		if days, ok, citation := workloadWorstDays(bySubject[key], coverage); ok {
			rawWorkload[key] = days
			haveWorkload[key] = true
			workloadCitation[key] = citation
		}
	}
	workloadMin, workloadMax := minMax(rawWorkload)

	type memberResult struct {
		// score is nil exactly when Outcome is insufficient_evidence or
		// not_applicable (design doc §8): the weight denominator either
		// does not clear the qualification threshold or is empty, so a
		// number cannot be honestly computed, and assigning one would
		// misrepresent an unqualified team as ranked.
		score          *float64
		basis          []string
		completeness   CohortDataCompleteness
		contributed    []string
		drivers        []CohortMemberDriver
		outcome        CohortMemberOutcome
		missingSignals []string
		// citations is length-for-length and order-for-order with drivers
		// (scoreMember's own invariant) -- see mintCohortDriverClaims.
		citations []*signalCitation
	}
	results := make([]memberResult, len(cohort.Members))
	degradedCount := 0
	for i, member := range cohort.Members {
		key := canonicalFactSubjectKey(member.Subject)
		memberFacts := bySubject[key]
		hasWorkload := haveWorkload[key]
		var workloadValue float64
		if hasWorkload {
			workloadValue = normalizeWorkloadMinMax(rawWorkload[key], workloadMin, workloadMax)
		}

		score, basis, completeness, contributed, drivers, outcome, missingSignals, citations := scoreMember(memberFacts, coverage, workloadValue, hasWorkload, workloadCitation[key])
		results[i] = memberResult{score: score, basis: basis, completeness: completeness, contributed: contributed, drivers: drivers, outcome: outcome, missingSignals: missingSignals, citations: citations}
		if completeness == CohortDataDegraded {
			degradedCount++
		}
		for _, name := range contributed {
			event.SignalsAvailable[name]++
		}
		event.OutcomeCounts[string(outcome)]++
	}

	// AttentionRank: score-sorted position over the ORIGINAL pool-order
	// indices, nil-Score members placed LAST (design doc §5b: "a null-Score
	// member's AttentionRank is placed deterministically last... ties among
	// null-Score members broken by Rank (pool order)"). sort.SliceStable's
	// own stability guarantee is what makes both the real-score ties AND
	// the null-score ties keep pool order, without this file needing a
	// second tie-break rule.
	order := make([]int, len(cohort.Members))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		scoreA, scoreB := results[order[a]].score, results[order[b]].score
		if scoreA == nil {
			return false // nil never sorts before anything (covers nil-vs-nil too: stable keeps pool order)
		}
		if scoreB == nil {
			return true // a real score always sorts before a nil one
		}
		return *scoreA > *scoreB
	})
	attentionRank := make([]int, len(cohort.Members))
	for rank, memberIndex := range order {
		attentionRank[memberIndex] = rank + 1
	}

	citationsByMember := make(cohortMemberSignalCitations, len(cohort.Members))
	for i := range cohort.Members {
		cohort.Members[i].RankingComputed = true
		cohort.Members[i].Score = results[i].score
		cohort.Members[i].AttentionRank = attentionRank[i]
		cohort.Members[i].RankingBasis = results[i].basis
		cohort.Members[i].DataCompleteness = results[i].completeness
		cohort.Members[i].Drivers = results[i].drivers
		cohort.Members[i].Outcome = results[i].outcome
		cohort.Members[i].MissingSignals = results[i].missingSignals
		// Citations are collected but NOT minted here -- see
		// cohortMemberSignalCitations' own doc comment. A nil citation
		// (the producer-bug case scoreMember's own comment documents) is
		// skipped entirely: that ONE driver simply has no citation to
		// resolve later, which narrateCohortDriverJudgments treats as
		// "cannot narrate this driver", never a fabricated one.
		bySignal := make(map[string]*signalCitation, len(results[i].drivers))
		for j, driver := range results[i].drivers {
			if j < len(results[i].citations) && results[i].citations[j] != nil {
				bySignal[driver.Signal] = results[i].citations[j]
			}
		}
		if len(bySignal) > 0 {
			citationsByMember[cohort.Members[i].Subject.CanonicalID] = bySignal
		}
	}

	event.MemberCount = len(cohort.Members)
	event.DegradedMemberCount = degradedCount
	return cohort, event, citationsByMember
}

// cohortDriverClaimID mints a deterministic, unique-per-ranking-pass
// ClaimID for one (member, signal family) pair, over (member subject,
// signal family, window, RankingFormulaVersion) -- NOT random, and not
// derived from anything time- or process-specific: two RankCohort calls
// over identical facts/coverage for the same member always mint the SAME
// ClaimID for the SAME signal, so a replay or an answer-reuse hit that
// serves a stored result verbatim reproduces the exact citation a fresh
// ranking pass would also mint (see
// TestRankCohort_MintedClaimIDsAreDeterministicAcrossRepeatedRuns). Window
// and RankingFormulaVersion are both part of the hashed tuple (not just
// subject+signal) because either one changing legitimately changes what
// this claim MEANS for the same member+signal (a current-vs-prior
// mix-shift citation is a different claim than a current-only one; a
// formula revision can change which raw field a signal even reads) -- the
// ReuseKey.RankingFormulaVersion dimension already fences REUSE on a
// formula bump; this fences the CLAIM ID itself on the same dimension, so
// two differently-versioned rankings of the same member/signal never
// collide on one ID.
//
// Codex R1 (CHAOS-4398 PR3b) caught the earlier concatenation-based ID
// ("claim_cohort_" + CanonicalID + "_" + signal + "_" + window + "_" +
// RankingFormulaVersion) exceeding ContextFabricModelMintedIDMaxLength
// (256) for a legal-but-long CanonicalID: SubjectRef.CanonicalID alone may
// be up to 256 characters, so the concatenation could reach ~330+ and
// reject an otherwise-valid cohort at result.Validate(). Hashing the same
// tuple keeps the ID fully deterministic and replay-stable (same input,
// same digest, every time) while bounding its length regardless of how
// long a legal CanonicalID gets.
func cohortDriverClaimID(subject SubjectRef, signal string, window CohortMemberDriverWindow) string {
	digest := sha256.Sum256([]byte(subject.CanonicalID + "\x00" + signal + "\x00" + string(window) + "\x00" + RankingFormulaVersion))
	return "claim_cohort_" + hex.EncodeToString(digest[:])[:32]
}

// scoreMember computes ONE member's Score/RankingBasis/DataCompleteness
// from its own already-read facts, the shared investigation Coverage, and
// its (already cohort-wide-normalized) workload signal. contributed is the
// list of top-level family names this call actually used, for the
// caller's telemetry histogram -- ALWAYS reflects true technical
// availability, independent of Outcome (design doc §8's operational-
// visibility requirement: how often is family X available at all, whether
// or not the member ultimately qualified). score/basis/drivers are nil/
// empty iff Outcome is insufficient_evidence or not_applicable (see
// memberResult's own doc comment above and Outcome's design doc §8
// thresholds).
func scoreMember(facts []CanonicalFact, coverage Coverage, workloadValue float64, workloadAvailable bool, workloadCitation *signalCitation) (score *float64, basis []string, completeness CohortDataCompleteness, contributed []string, drivers []CohortMemberDriver, outcome CohortMemberOutcome, missingSignals []string, citations []*signalCitation) {
	mixValue, mixLabels, mixUsedPriorWindow, mixConcentration, mixConcentrationMethod, mixAvailable, mixCitation := investmentMixSignal(facts, coverage)
	healthValue, healthAvailable, healthCitation := healthRiskSignal(facts, coverage)
	deficiencyValue, deficiencyAvailable, deficiencyCitation := deficiencySeveritySignal(facts, coverage)
	readinessValue, readinessAvailable, readinessCitation := readinessGapSignal(facts, coverage)

	type signal struct {
		name                string
		weight              float64
		value               float64
		available           bool
		thresholdLabels     []string
		usedPriorWindow     bool
		concentration       float64
		concentrationMethod string
		hasConcentration    bool
		// citation (CHAOS-4398 PR3b) is the raw canonical field this
		// signal's value was actually read from -- see signalCitation's
		// own doc comment. Always non-nil when available is true (every
		// signal function's own contract).
		citation *signalCitation
	}
	signals := [...]signal{
		{RankingSignalInvestmentMix, weightInvestmentMix, mixValue, mixAvailable, mixLabels, mixUsedPriorWindow, mixConcentration, mixConcentrationMethod, mixAvailable, mixCitation},
		{RankingSignalHealthRisk, weightHealthRisk, healthValue, healthAvailable, nil, false, 0, "", false, healthCitation},
		{RankingSignalDeficiencySeverity, weightDeficiencySeverity, deficiencyValue, deficiencyAvailable, nil, false, 0, "", false, deficiencyCitation},
		{RankingSignalReadinessGap, weightReadinessGap, readinessValue, readinessAvailable, nil, false, 0, "", false, readinessCitation},
		{RankingSignalWorkloadPressure, weightWorkloadPressure, workloadValue, workloadAvailable, nil, false, 0, "", false, workloadCitation},
	}

	var weightedSum, availableWeight float64
	availableCount := 0
	for _, s := range signals {
		if !s.available {
			missingSignals = append(missingSignals, s.name)
			continue
		}
		weightedSum += s.weight * s.value
		availableWeight += s.weight
		contributed = append(contributed, s.name)
		availableCount++
	}

	// Outcome (design doc §8, replacing the contract doc §4.2 binary
	// qualify/does-not-qualify): a DETERMINISTIC verdict over the SAME
	// availableWeight/availableCount scoreMember already computes for the
	// formula itself -- not applicable (zero signals at all), insufficient
	// evidence (available weight below half the 100-point total, or fewer
	// than 2 families -- the latter is subsumed by the weight check today
	// since no single family's weight reaches 50, but is checked
	// explicitly per the ratified rule rather than relying on that
	// coincidence), provisional (50-99), or qualified (all 5, 100).
	switch {
	case availableWeight == 0:
		outcome = CohortOutcomeNotApplicable
	case availableWeight < 50 || availableCount < 2:
		outcome = CohortOutcomeInsufficientEvidence
	case availableWeight < 100:
		outcome = CohortOutcomeProvisional
	default:
		outcome = CohortOutcomeQualified
	}

	// Score/RankingBasis/Drivers are populated ONLY for a qualified or
	// provisional Outcome -- an insufficient_evidence or not_applicable
	// member gets none of the three (mirrors the existing nil-Score
	// null-vs-omit rule the write-path validator enforces), and instead
	// states WHY via Outcome + MissingSignals.
	if outcome == CohortOutcomeQualified || outcome == CohortOutcomeProvisional {
		basis = append(basis, contributed...)
		// Investment-mix driver labels ride AFTER the family name, only
		// when the family itself was available (mixLabels is always nil
		// when mixAvailable is false -- investmentMixSignal never fires a
		// threshold off data it does not have).
		basis = append(basis, mixLabels...)

		value := 100 * weightedSum / availableWeight
		score = &value
		// Drivers (CHAOS-4398 PR2) is built AFTER availableWeight is known
		// -- WeightContributed is exactly this signal's own share of value
		// (100*weight*s.value/availableWeight), so Sum(WeightContributed)
		// across every driver reconstructs *score exactly, the traceability
		// invariant internal/contracts/v1's validateDrivers enforces.
		for _, s := range signals {
			if !s.available {
				continue
			}
			window := DriverWindowCurrent
			if s.usedPriorWindow {
				window = DriverWindowCurrentVsPrior
			}
			driverEntry := CohortMemberDriver{
				Signal:            s.name,
				Value:             s.value,
				Weight:            s.weight,
				WeightContributed: 100 * s.weight * s.value / availableWeight,
				Window:            window,
				ThresholdLabels:   s.thresholdLabels,
			}
			if s.hasConcentration {
				concentration := s.concentration
				driverEntry.Concentration = &concentration
				driverEntry.ConcentrationMethod = s.concentrationMethod
			}
			drivers = append(drivers, driverEntry)
			// citations is built in LOCKSTEP with drivers (same loop, same
			// filter, same order, same length always) -- RankCohort mints
			// one ClaimID per non-nil entry and assigns it back to
			// drivers[same index] by position, so the two slices must
			// never diverge in length or order. Every available signal's
			// citation is non-nil by every signal function's own contract
			// (see signalCitation's own doc comment); a nil entry here
			// would be a producer bug -- RankCohort's own assembly step
			// leaves that ONE driver's SourceClaimedFactIDs empty rather
			// than fabricate a citation, which the write-path validator
			// then correctly flags as a real defect instead of silently
			// producing an invalid claim.
			citations = append(citations, s.citation)
		}
	}
	switch {
	case availableCount == len(signals):
		completeness = CohortDataComplete
	case availableCount <= 2:
		completeness = CohortDataDegraded
	default:
		completeness = CohortDataPartial
	}
	return score, basis, completeness, contributed, drivers, outcome, missingSignals, citations
}

// coverageState looks up the fact-read Coverage entry for kind, matching
// the exact "canonical_fact:<kind>" Source format appendFactCoverage
// (fact_registry.go) writes. found is false when no entry exists at all --
// e.g. this FactKind was never in the investigation's requirements, or a
// caller (most unit tests) built a Coverage by hand without one.
func coverageState(coverage Coverage, kind FactKind) (state SourceState, found bool) {
	prefix := "canonical_fact:" + string(kind)
	for _, source := range coverage.Sources {
		if source.Source == prefix {
			return source.State, true
		}
	}
	return "", false
}

// familyBatchAdmits applies the general half of the design doc's §5
// per-(member,family) availability rule: the coverage batch state for kind
// must be SourceAvailable or SourceTruncated (a Pruned/Unavailable/errored
// batch never has valid rows to salvage, even if one happens to appear in
// facts by construction -- appendFactCoverage's own contract). A kind with
// NO coverage entry at all is treated as ADMITTING whatever rows are
// present: this function cannot demand telemetry a caller was never given
// (most unit tests build facts directly, with no Coverage), and production
// always carries a real entry for every ranking-formula kind once
// Engine.Investigate injects the five requirements for a cohort answer
// (engine.go).
func familyBatchAdmits(coverage Coverage, kind FactKind) bool {
	state, found := coverageState(coverage, kind)
	if !found {
		return true
	}
	return state == SourceAvailable || state == SourceTruncated
}

// findFact returns the first fact of kind in facts. Used only by signals
// whose producer emits AT MOST ONE fact of that kind per team subject
// (health, investment's theme-mix fact) -- readiness/workload/deficiency
// aggregate across every fact of their kind instead (see their own
// functions) because those producers can legitimately emit several.
func findFact(facts []CanonicalFact, kind FactKind) (CanonicalFact, bool) {
	for _, fact := range facts {
		if fact.Kind == kind {
			return fact, true
		}
	}
	return CanonicalFact{}, false
}

func numberField(fact CanonicalFact, field string) (float64, bool) {
	value, ok := fact.Fields[field]
	if !ok || value.Number == nil {
		return 0, false
	}
	return *value.Number, true
}

func stringField(fact CanonicalFact, field string) (string, bool) {
	value, ok := fact.Fields[field]
	if !ok || value.String == nil {
		return "", false
	}
	return *value.String, true
}

func integerField(fact CanonicalFact, field string) (int64, bool) {
	value, ok := fact.Fields[field]
	if !ok || value.Integer == nil {
		return 0, false
	}
	return *value.Integer, true
}

// themeShares reads every canonical theme's share off fact using prefix
// (FactFieldTheme for the current window, FactFieldPriorTheme for the
// prior one). ok is false when the producer wrote NO theme fields at all
// under that prefix (current: a team with zero WorkUnits in-window; prior:
// no prior-window data -- both legitimate, handled by the caller as
// "signal/sub-signal unavailable", never a fabricated 0).
func themeShares(fact CanonicalFact, prefix func(string) string) (map[string]float64, bool) {
	shares := make(map[string]float64, len(canonicalThemes))
	any := false
	for _, theme := range canonicalThemes {
		if value, ok := numberField(fact, prefix(theme)); ok {
			shares[theme] = value
			any = true
		}
	}
	if !any {
		return nil, false
	}
	return shares, true
}

// maxShare returns the largest theme share AND which canonical theme
// achieves it -- iterated in the FIXED canonicalThemes order (not map
// iteration) so a tie between two themes resolves deterministically to
// whichever comes first in the taxonomy, the same tie-break discipline the
// mix-shift direction label already uses. theme is "" only when shares is
// empty (never reached in production: investmentMixSignal only calls this
// after themeShares has already confirmed at least one theme is present).
func maxShare(shares map[string]float64) (share float64, theme string) {
	for _, candidate := range canonicalThemes {
		value, ok := shares[candidate]
		if !ok || value <= share {
			continue
		}
		share = value
		theme = candidate
	}
	return share, theme
}

// signalCitation names ONE canonical fact field a signal function actually
// read to compute its business value. RankCohort turns each available
// signal's citation into a persisted ContextFabricClaimedFact (Subject is
// the member's own Subject, attached by the caller, which is the only
// piece a signal function itself never has) and records the minted
// ClaimID on the driver's own SourceClaimedFactIDs -- CHAOS-4398 PR3b's
// R4-style ruling: "the narration cites, it never mints". This type and
// every signal function's citation return value below ARE the one-time
// mint, done here in the ranker itself, never re-derived or re-minted
// downstream (the narrator only resolves a driver to the IDs recorded
// here).
type signalCitation struct {
	kind  FactKind
	field string
	value FactValue
}

// citeFactField builds a signalCitation for one already-read field on
// fact -- the common shape every signal function but
// deficiencySeveritySignal's available-zero exception uses (that one has
// no fact row to cite at all; see its own doc comment).
func citeFactField(kind FactKind, fact CanonicalFact, field string) *signalCitation {
	value, ok := fact.Fields[field]
	if !ok {
		return nil
	}
	return &signalCitation{kind: kind, field: field, value: value}
}

// validateMintedClaimsGrounded is the structural half of codex R1's
// finding (CHAOS-4398 PR3b, team-lead ruling): every narration-minted
// ClaimedFact must pass the SAME grounding check a model-authored claim
// gets from SynthesisDraft.ValidateAgainst BEFORE it is appended to
// result.ClaimedFacts -- narration runs entirely AFTER ValidateAgainst has
// already run (see narrateCohortDriverJudgments' own doc comment on
// ordering), so nothing else ever checks these claims against the real
// canonical fact bundle.
//
// citeFactField already makes this hold BY CONSTRUCTION for every citation
// it builds (it reads fact.Fields[field] directly, so the value can never
// diverge from a real fact). This function is the defense-in-depth
// backstop, not a belt assumed redundant with that suspenders: it
// re-derives, from `facts` (the SAME canonical fact bundle RankCohort
// itself read), whether each minted claim's (Kind, Subject, Field, Value)
// tuple actually matches a real CanonicalFact -- so a FUTURE signal
// function that builds a signalCitation some way OTHER than citeFactField
// (the way the deficiency available-zero case's now-removed
// "fired_rules_count" citation once did) fails HERE, loudly, instead of
// silently shipping a fabricated claim.
func validateMintedClaimsGrounded(claims []ClaimedFact, facts []CanonicalFact) error {
	for _, claim := range claims {
		grounded := false
		for _, fact := range facts {
			if fact.Kind != claim.Kind {
				continue
			}
			if fact.Subject.Kind != claim.Subject.Kind || fact.Subject.CanonicalID != claim.Subject.CanonicalID {
				continue
			}
			value, ok := fact.Fields[claim.Field]
			if ok && factValueEqualsScalar(value, claim.Value) {
				grounded = true
				break
			}
		}
		if !grounded {
			return fmt.Errorf("minted claim %q (kind=%s field=%q subject=%q) does not match any canonical fact this ranking pass read",
				claim.ClaimID, claim.Kind, claim.Field, claim.Subject.CanonicalID)
		}
	}
	return nil
}

// investmentMixSignal is the term-1 sub-formula (design doc §5). value is
// already the term's own [0,1] contribution (the sum of whichever
// sub-weights crossed their threshold); available is false when the
// family's coverage batch does not admit it, or the FactInvestment
// producer wrote no theme data at all for this member (see themeShares).
// Mix-shift is a SEPARATE availability check (the prior-window query can
// legitimately come back empty even when the current window has data) and
// its absence does not make the whole signal unavailable -- it just means
// that one sub-weight never fires.
//
// citation (CHAOS-4398 PR3b) cites the SAME fact's max-share theme field
// (whichever canonical theme achieves concentration) -- concentration is
// already a real number this driver exposes on its own Concentration
// field, so citing the exact canonical field it came from adds a
// provenance pointer without inventing any new number.
func investmentMixSignal(facts []CanonicalFact, coverage Coverage) (value float64, driverLabels []string, usedPriorWindow bool, concentration float64, concentrationMethod string, available bool, citation *signalCitation) {
	if !familyBatchAdmits(coverage, FactInvestment) {
		return 0, nil, false, 0, "", false, nil
	}
	// A team subject can carry MULTIPLE FactInvestment facts -- one per
	// legacy (investment_area, project_stream) pair from readTeamInvestment
	// PLUS one dedicated fact carrying the canonical theme_* fields
	// (devhealthfacts/investment.go's readTeamThemeMix). findFact's
	// first-match behavior is not safe here: the theme-carrying fact is not
	// guaranteed to be first in the list, so this loop finds the fact that
	// actually HAS theme data rather than assuming position.
	var fact CanonicalFact
	found := false
	for _, candidate := range facts {
		if candidate.Kind != FactInvestment {
			continue
		}
		if _, has := candidate.Fields[FactFieldTheme(ThemeFeatureDelivery)]; has {
			fact = candidate
			found = true
			break
		}
	}
	if !found {
		return 0, nil, false, 0, "", false, nil
	}
	current, ok := themeShares(fact, FactFieldTheme)
	if !ok {
		return 0, nil, false, 0, "", false, nil
	}
	bugfixShare, _ := numberField(fact, FactFieldThemeQualityBugfix)

	reactiveShare := current[ThemeOperational] + bugfixShare
	deliberateShare := current[ThemeFeatureDelivery]
	// concentration/concentrationMethod (CHAOS-4398 PR3) make the
	// mix_concentrated threshold's own evidence checkable by number, the
	// same discipline Value/WeightContributed already apply to the family
	// as a whole -- concentrationMethod is named generically (not
	// "max_share" baked into the field name) so CHAOS-4414's HHI
	// concentration measure can later replace this computation without a
	// contract-breaking rename.
	var concentrationTheme string
	concentration, concentrationTheme = maxShare(current)
	concentrationMethod = ConcentrationMethodMaxShare
	// citation (CHAOS-4398 PR3b): cite the max-share theme's own field --
	// the exact canonical value Concentration already reports. Every real
	// theme distribution sums to ~1.0, so concentrationTheme is
	// empty only in a defensively-unreachable all-zero-shares producer bug;
	// falling back to the FIRST present canonical theme field (fixed
	// taxonomy order, same determinism discipline as maxShare/mix-shift)
	// keeps SourceClaimedFactIDs non-empty for this family whenever it is
	// available, which the write-path validator requires unconditionally.
	if concentrationTheme == "" {
		for _, candidate := range canonicalThemes {
			if _, ok := current[candidate]; ok {
				concentrationTheme = candidate
				break
			}
		}
	}
	if concentrationTheme != "" {
		citation = citeFactField(FactInvestment, fact, FactFieldTheme(concentrationTheme))
	}

	if reactiveShare > reactiveShareThreshold {
		value += subWeightReactiveShare
		driverLabels = append(driverLabels, DriverReactiveShareHigh)
	}
	if deliberateShare < deliberateShareThreshold {
		value += subWeightDeliberateShare
		driverLabels = append(driverLabels, DriverDeliberateShareLow)
	}
	if concentration > concentrationThreshold {
		value += subWeightConcentration
		driverLabels = append(driverLabels, DriverMixConcentrated)
	}
	if prior, ok := themeShares(fact, FactFieldPriorTheme); ok {
		// usedPriorWindow is true as soon as a real prior-window comparison
		// was MADE, regardless of whether shiftMagnitude crossed
		// mixShiftThreshold -- Window on the resulting driver states
		// whether this value is a single-point read or a two-window
		// comparison, independent of which sub-signals happened to fire.
		usedPriorWindow = true
		shiftMagnitude := 0.0
		bestTheme := ""
		bestDelta := math.Inf(-1)
		// Fixed taxonomy order (canonicalThemes): both the magnitude sum
		// and the tie-break for the largest positive delta depend on
		// iterating in this exact, deterministic order (design doc §5,
		// "Tie-break (P2)").
		for _, theme := range canonicalThemes {
			delta := current[theme] - prior[theme]
			shiftMagnitude += math.Abs(delta)
			if delta > bestDelta {
				bestDelta = delta
				bestTheme = theme
			}
		}
		if shiftMagnitude > mixShiftThreshold {
			value += subWeightMixShift
			switch bestTheme {
			case ThemeOperational:
				driverLabels = append(driverLabels, DriverMixShiftTowardOperational)
			case ThemeFeatureDelivery:
				driverLabels = append(driverLabels, DriverMixShiftTowardFeature)
			default:
				driverLabels = append(driverLabels, DriverMixShiftOther)
			}
		}
	}
	return value, driverLabels, usedPriorWindow, concentration, concentrationMethod, true, citation
}

// healthRiskSignal reads FactHealth's severity band (compounding_risk_daily's
// Enum8: unknown/low/elevated/high) rather than the raw compounding_risk
// float -- a fixed, closed 4-value vocabulary needs no arbitrary scaling
// assumption the way an unbounded risk score would. "unknown" (or a
// missing severity) is treated as unavailable, not as a low-risk 0: an
// unclassified severity is a data gap, not a favorable observation.
func healthRiskSignal(facts []CanonicalFact, coverage Coverage) (value float64, available bool, citation *signalCitation) {
	if !familyBatchAdmits(coverage, FactHealth) {
		return 0, false, nil
	}
	fact, ok := findFact(facts, FactHealth)
	if !ok {
		return 0, false, nil
	}
	severity, ok := stringField(fact, "severity")
	if !ok {
		return 0, false, nil
	}
	// citation (CHAOS-4398 PR3b): the raw "severity" field this value maps
	// from -- see signalCitation's own doc comment.
	citation = citeFactField(FactHealth, fact, "severity")
	switch severity {
	case "low":
		return 0.0, true, citation
	case "elevated":
		return 0.5, true, citation
	case "high":
		return 1.0, true, citation
	default: // "unknown" or any unrecognized value
		return 0, false, nil
	}
}

// deficiencySeveritySignal reads EVERY FactOperationalDeficiencies fact for
// this member (the producer emits one per fired rule, never one per team)
// and takes the MAX of the closed two-value severity vocabulary
// (ops/src/dev_health_ops/recommendations/schema.py: Severity =
// Literal["warning", "critical"] -- verified against both the ops source
// and live recommendations_daily data, not the design doc's speculative
// four-value placeholder it flagged as unconfirmed).
//
// Zero fired-rule facts is the documented "available-zero" exception
// (design doc §5): OperationalDeficienciesProvider returns a successful
// empty read when a team genuinely has no currently-fired rules, which
// this formula must score as a real 0 ("no risk"), not exclude as missing
// -- excluding it would drop the 20-point weight and renormalize the
// remaining, mostly-adverse signals upward, penalizing a healthy team for
// having nothing wrong. That exception applies ONLY when the batch read
// was a clean, unpruned success (SourceAvailable, or unknown/no coverage
// entry -- see familyBatchAdmits' own "no telemetry, don't demand it"
// convention) -- a Truncated batch cannot promise there were truly zero
// fired rules (one could exist past the truncation cap), so it does NOT
// get the zero exception.
func deficiencySeveritySignal(facts []CanonicalFact, coverage Coverage) (value float64, available bool, citation *signalCitation) {
	max := 0.0
	found := false
	var maxFact CanonicalFact
	for _, fact := range facts {
		if fact.Kind != FactOperationalDeficiencies {
			continue
		}
		severity, ok := stringField(fact, "severity")
		if !ok {
			continue
		}
		var v float64
		switch severity {
		case "critical":
			v = 1.0
		case "warning":
			v = 0.5
		default:
			continue
		}
		if !found || v > max {
			max = v
			maxFact = fact
		}
		found = true
	}
	if found {
		if !familyBatchAdmits(coverage, FactOperationalDeficiencies) {
			return 0, false, nil
		}
		// citation (CHAOS-4398 PR3b): the fired rule whose severity
		// actually produced max -- the same "worst case governs" fact this
		// value already came from.
		return max, true, citeFactField(FactOperationalDeficiencies, maxFact, "severity")
	}
	state, foundState := coverageState(coverage, FactOperationalDeficiencies)
	if !foundState || state == SourceAvailable {
		// The available-zero exception (this function's own doc comment):
		// a successful read with NO fired-rule row. This IS a real,
		// observed value for RANKING (0 -- "no risk"): the ranker actually
		// read the batch and found nothing fired, so Value/Weight above
		// still score it as a genuine zero, never as missing.
		//
		// It is NOT citable, though (codex R1, CHAOS-4398 PR3b, team-lead
		// ruling superseding this function's earlier "fired_rules_count"
		// citation): OperationalDeficienciesProvider (devhealthfacts/
		// deficiencies.go) only ever emits a CanonicalFact for a row Ops
		// already marked fired=1 -- zero fired rules means ZERO rows, not
		// one row with a count field set to zero. There is no real
		// CanonicalFact of this Kind for this subject at all when found is
		// false, so there is no field on any actual record this citation
		// could name -- "fired_rules_count" was a field invented by this
		// function, present on no real fact, which is exactly the
		// fabrication codex flagged: a claim minted from it could never
		// ground against the canonical fact bundle the way every other
		// citation here (built through citeFactField, reading a REAL
		// fact.Fields entry) provably does.
		//
		// So this branch returns a nil citation: available=true (the
		// value counts for scoring) but citation=nil (nothing to mint
		// from). narrateCohortDriverJudgments already skips any driver
		// whose citation is nil ("no citation to mint from -- never
		// narrate without one") -- this available-zero driver still
		// contributes its Value/Weight/WeightContributed to the member's
		// Score and RankingBasis, it just never becomes a narrated
		// ContextFabricDriverJudgment or a minted ClaimedFact. Consistent
		// with "minting follows citation, not ranking": a real ranking
		// signal that has no real fact to cite stays ranking-only.
		return 0, true, nil
	}
	return 0, false, nil
}

// readinessGapSignal aggregates the WORST (lowest estimate_coverage_ratio)
// across every FactReadiness fact for this member -- readiness partitions
// by provider and work scope, so a team can carry several, and the design
// doc's own "worst case governs" philosophy (matching deficiency severity's
// MAX-across-fired-rules choice) picks the least-covered scope to drive the
// gap, never an average or an arbitrary first row.
func readinessGapSignal(facts []CanonicalFact, coverage Coverage) (value float64, available bool, citation *signalCitation) {
	if !familyBatchAdmits(coverage, FactReadiness) {
		return 0, false, nil
	}
	minRatio := math.Inf(1)
	found := false
	var minFact CanonicalFact
	for _, fact := range facts {
		if fact.Kind != FactReadiness {
			continue
		}
		ratio, ok := numberField(fact, "estimate_coverage_ratio")
		if !ok {
			continue
		}
		if !found || ratio < minRatio {
			minRatio = ratio
			minFact = fact
		}
		found = true
	}
	if !found {
		return 0, false, nil
	}
	gap := 1 - minRatio
	if gap < 0 {
		gap = 0
	}
	if gap > 1 {
		gap = 1
	}
	// citation (CHAOS-4398 PR3b): the least-covered scope's own raw ratio --
	// the same "worst case governs" row this gap already derives from.
	return gap, true, citeFactField(FactReadiness, minFact, "estimate_coverage_ratio")
}

// workloadWorstDays aggregates the WORST (longest) forecast_p50_days
// across every FactWorkload fact for this member -- workload partitions by
// work scope, so a team can carry several; the longest forecast drives the
// pressure (design doc §5's own "worst case governs" philosophy).
func workloadWorstDays(facts []CanonicalFact, coverage Coverage) (days float64, available bool, citation *signalCitation) {
	if !familyBatchAdmits(coverage, FactWorkload) {
		return 0, false, nil
	}
	maxDays := 0.0
	found := false
	var maxFact CanonicalFact
	for _, fact := range facts {
		if fact.Kind != FactWorkload {
			continue
		}
		value, ok := integerField(fact, "forecast_p50_days")
		if !ok {
			continue
		}
		floatValue := float64(value)
		if !found || floatValue > maxDays {
			maxDays = floatValue
			maxFact = fact
		}
		found = true
	}
	if !found {
		return 0, false, nil
	}
	// citation (CHAOS-4398 PR3b): the longest-forecast scope's own raw
	// day count -- the same "worst case governs" row this value already
	// derives from.
	return maxDays, true, citeFactField(FactWorkload, maxFact, "forecast_p50_days")
}

// minMax computes the population min and max over values' own float64s.
// Returns (0, 0) for an empty map.
func minMax(values map[string]float64) (min, max float64) {
	first := true
	for _, v := range values {
		if first {
			min, max = v, v
			first = false
			continue
		}
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

// normalizeWorkloadMinMax maps a raw forecast_p50_days value into [0,1]
// via min-max normalization WITHIN the cohort being ranked (design doc §5:
// "a z-score is unbounded and can be negative, so it cannot feed a
// weighted [0,100] sum directly"). A zero-spread cohort (every observed
// member ties, min == max) yields the neutral midpoint 0.5 for everyone --
// there is no meaningful spread to rank by, which is a different, honest
// statement from "high pressure" or "low pressure".
func normalizeWorkloadMinMax(raw, min, max float64) float64 {
	if max == min {
		return 0.5
	}
	return (raw - min) / (max - min)
}
