package contextfabric

import (
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
const RankingFormulaVersion = "cohort-ranking.v1"

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
func RankCohort(cohort *Cohort, facts []CanonicalFact, coverage Coverage) (*Cohort, CohortRankedEvent) {
	event := CohortRankedEvent{FormulaVersion: RankingFormulaVersion, SignalsAvailable: map[string]int{}}
	if cohort == nil || len(cohort.Members) == 0 {
		return cohort, event
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
	for _, member := range cohort.Members {
		key := canonicalFactSubjectKey(member.Subject)
		if days, ok := workloadWorstDays(bySubject[key], coverage); ok {
			rawWorkload[key] = days
			haveWorkload[key] = true
		}
	}
	workloadMin, workloadMax := minMax(rawWorkload)

	type memberResult struct {
		// score is nil exactly when ZERO signal families were available
		// (design doc §5b): the weight denominator is empty, so a number
		// cannot be honestly computed, and assigning 0 would render the
		// least-observed team as the healthiest -- the opposite of what
		// this formula exists to prevent.
		score        *float64
		basis        []string
		completeness CohortDataCompleteness
		contributed  []string
		drivers      []CohortMemberDriver
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

		score, basis, completeness, contributed, drivers := scoreMember(memberFacts, coverage, workloadValue, hasWorkload)
		results[i] = memberResult{score: score, basis: basis, completeness: completeness, contributed: contributed, drivers: drivers}
		if completeness == CohortDataDegraded {
			degradedCount++
		}
		for _, name := range contributed {
			event.SignalsAvailable[name]++
		}
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

	for i := range cohort.Members {
		cohort.Members[i].RankingComputed = true
		cohort.Members[i].Score = results[i].score
		cohort.Members[i].AttentionRank = attentionRank[i]
		cohort.Members[i].RankingBasis = results[i].basis
		cohort.Members[i].DataCompleteness = results[i].completeness
		cohort.Members[i].Drivers = results[i].drivers
	}

	event.MemberCount = len(cohort.Members)
	event.DegradedMemberCount = degradedCount
	return cohort, event
}

// scoreMember computes ONE member's Score/RankingBasis/DataCompleteness
// from its own already-read facts, the shared investigation Coverage, and
// its (already cohort-wide-normalized) workload signal. contributed is the
// list of top-level family names this call actually used, for the
// caller's telemetry histogram -- always a subset of (and in the same
// order as) basis's family-name entries. score is nil iff zero families
// were available (see memberResult's own doc comment above).
func scoreMember(facts []CanonicalFact, coverage Coverage, workloadValue float64, workloadAvailable bool) (score *float64, basis []string, completeness CohortDataCompleteness, contributed []string, drivers []CohortMemberDriver) {
	mixValue, mixLabels, mixUsedPriorWindow, mixAvailable := investmentMixSignal(facts, coverage)
	healthValue, healthAvailable := healthRiskSignal(facts, coverage)
	deficiencyValue, deficiencyAvailable := deficiencySeveritySignal(facts, coverage)
	readinessValue, readinessAvailable := readinessGapSignal(facts, coverage)

	type signal struct {
		name            string
		weight          float64
		value           float64
		available       bool
		thresholdLabels []string
		usedPriorWindow bool
	}
	signals := [...]signal{
		{RankingSignalInvestmentMix, weightInvestmentMix, mixValue, mixAvailable, mixLabels, mixUsedPriorWindow},
		{RankingSignalHealthRisk, weightHealthRisk, healthValue, healthAvailable, nil, false},
		{RankingSignalDeficiencySeverity, weightDeficiencySeverity, deficiencyValue, deficiencyAvailable, nil, false},
		{RankingSignalReadinessGap, weightReadinessGap, readinessValue, readinessAvailable, nil, false},
		{RankingSignalWorkloadPressure, weightWorkloadPressure, workloadValue, workloadAvailable, nil, false},
	}

	var weightedSum, availableWeight float64
	availableCount := 0
	for _, s := range signals {
		if !s.available {
			continue
		}
		weightedSum += s.weight * s.value
		availableWeight += s.weight
		basis = append(basis, s.name)
		contributed = append(contributed, s.name)
		availableCount++
	}
	// Investment-mix driver labels ride AFTER the family name, only when
	// the family itself was available (mixLabels is always nil when
	// mixAvailable is false -- investmentMixSignal never fires a threshold
	// off data it does not have).
	basis = append(basis, mixLabels...)

	if availableWeight > 0 {
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
			drivers = append(drivers, CohortMemberDriver{
				Signal:            s.name,
				Value:             s.value,
				Weight:            s.weight,
				WeightContributed: 100 * s.weight * s.value / availableWeight,
				Window:            window,
				ThresholdLabels:   s.thresholdLabels,
			})
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
	return score, basis, completeness, contributed, drivers
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

func maxShare(shares map[string]float64) float64 {
	max := 0.0
	for _, v := range shares {
		if v > max {
			max = v
		}
	}
	return max
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
func investmentMixSignal(facts []CanonicalFact, coverage Coverage) (value float64, driverLabels []string, usedPriorWindow bool, available bool) {
	if !familyBatchAdmits(coverage, FactInvestment) {
		return 0, nil, false, false
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
		return 0, nil, false, false
	}
	current, ok := themeShares(fact, FactFieldTheme)
	if !ok {
		return 0, nil, false, false
	}
	bugfixShare, _ := numberField(fact, FactFieldThemeQualityBugfix)

	reactiveShare := current[ThemeOperational] + bugfixShare
	deliberateShare := current[ThemeFeatureDelivery]
	concentration := maxShare(current)

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
	return value, driverLabels, usedPriorWindow, true
}

// healthRiskSignal reads FactHealth's severity band (compounding_risk_daily's
// Enum8: unknown/low/elevated/high) rather than the raw compounding_risk
// float -- a fixed, closed 4-value vocabulary needs no arbitrary scaling
// assumption the way an unbounded risk score would. "unknown" (or a
// missing severity) is treated as unavailable, not as a low-risk 0: an
// unclassified severity is a data gap, not a favorable observation.
func healthRiskSignal(facts []CanonicalFact, coverage Coverage) (value float64, available bool) {
	if !familyBatchAdmits(coverage, FactHealth) {
		return 0, false
	}
	fact, ok := findFact(facts, FactHealth)
	if !ok {
		return 0, false
	}
	severity, ok := stringField(fact, "severity")
	if !ok {
		return 0, false
	}
	switch severity {
	case "low":
		return 0.0, true
	case "elevated":
		return 0.5, true
	case "high":
		return 1.0, true
	default: // "unknown" or any unrecognized value
		return 0, false
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
func deficiencySeveritySignal(facts []CanonicalFact, coverage Coverage) (value float64, available bool) {
	max := 0.0
	found := false
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
		found = true
		if v > max {
			max = v
		}
	}
	if found {
		if !familyBatchAdmits(coverage, FactOperationalDeficiencies) {
			return 0, false
		}
		return max, true
	}
	state, foundState := coverageState(coverage, FactOperationalDeficiencies)
	if !foundState || state == SourceAvailable {
		return 0, true
	}
	return 0, false
}

// readinessGapSignal aggregates the WORST (lowest estimate_coverage_ratio)
// across every FactReadiness fact for this member -- readiness partitions
// by provider and work scope, so a team can carry several, and the design
// doc's own "worst case governs" philosophy (matching deficiency severity's
// MAX-across-fired-rules choice) picks the least-covered scope to drive the
// gap, never an average or an arbitrary first row.
func readinessGapSignal(facts []CanonicalFact, coverage Coverage) (value float64, available bool) {
	if !familyBatchAdmits(coverage, FactReadiness) {
		return 0, false
	}
	minRatio := math.Inf(1)
	found := false
	for _, fact := range facts {
		if fact.Kind != FactReadiness {
			continue
		}
		ratio, ok := numberField(fact, "estimate_coverage_ratio")
		if !ok {
			continue
		}
		found = true
		if ratio < minRatio {
			minRatio = ratio
		}
	}
	if !found {
		return 0, false
	}
	gap := 1 - minRatio
	if gap < 0 {
		gap = 0
	}
	if gap > 1 {
		gap = 1
	}
	return gap, true
}

// workloadWorstDays aggregates the WORST (longest) forecast_p50_days
// across every FactWorkload fact for this member -- workload partitions by
// work scope, so a team can carry several; the longest forecast drives the
// pressure (design doc §5's own "worst case governs" philosophy).
func workloadWorstDays(facts []CanonicalFact, coverage Coverage) (float64, bool) {
	if !familyBatchAdmits(coverage, FactWorkload) {
		return 0, false
	}
	maxDays := 0.0
	found := false
	for _, fact := range facts {
		if fact.Kind != FactWorkload {
			continue
		}
		days, ok := integerField(fact, "forecast_p50_days")
		if !ok {
			continue
		}
		found = true
		value := float64(days)
		if value > maxDays {
			maxDays = value
		}
	}
	if !found {
		return 0, false
	}
	return maxDays, true
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
