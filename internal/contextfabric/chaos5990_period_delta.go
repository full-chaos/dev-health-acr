package contextfabric

// CHAOS-5990: the period-delta obligation, at TEAM grain, for the
// CHAOS-4347 composed status category.
//
// A bare FactStatus requirement for a team or project subject is expanded
// by composeStatusCategoryRequirements into the CHAOS-4347 composed
// fact-kind set (health, workload, readiness, investment, flow,
// landscape). That composed set is read ONCE, at the request's own as-of
// instant. When the frame derives ObligationPeriodDelta (GoalExplainChange
// or TemporalIntentPeriodComparison, frame_obligations.go), there is no
// SECOND read to diff against -- status_shadow.go's obligationObservations
// entry for period_delta says so explicitly ("no distinct result field").
// The turn still SERVES (the composed set answers `state`/`health`/etc.),
// but it degrades on period_delta with a fact_provider_reported
// limitation.
//
// Data semantics this file implements:
//
//   - "project status" for this comparison is the OWNING TEAM's aggregate
//     posture, served as a DISCLOSED stand-in -- never a silent
//     substitution, and never literal project.state lifecycle history
//     (project_declared_state_history/_floor carry at most one recorded
//     observation per project, so no second point exists at any date for
//     that literal fact). health.go's project rollup (readProjectHealth,
//     queryProjectHealthSeverityMax) already promotes the WORST known band
//     across a project's own team+repo ownership joins under the closed
//     `severity_basis` disclosure -- that disclosure is reused verbatim as
//     the project-grain posture rather than a new ownership-resolution
//     path, and it is itself what states the substitution: a project's
//     period-delta is read off its already-disclosed non-literal-state
//     rollup, never off project_declared_state_history.
//   - "changed" is a BAND TRANSITION -- the SAME closed
//     {low, elevated, high, unknown} severity vocabulary health.go already
//     emits (the freshness-window reader), never a raw numeric delta on
//     compounding_risk.
//   - "last month" is the ROLLING PeriodDeltaWindowDays days before the
//     request's own as-of instant (never wall-clock time and never a
//     calendar month) -- see periodDeltaCurrentAsOf.
//
// This file computes the delta from bands already on the primary read's
// CanonicalFacts (the current point) plus one additional as-of read issued
// through the SAME FactCapabilityRegistry.ReadFacts port the primary read
// used (the prior point) -- see applyPeriodDelta below, called from
// engine.go. It adds no new ClickHouse SQL and no new FactProvider: both
// reads run the existing, unmodified HealthProvider.ReadFacts, at two
// different TimeContext values. That is what keeps this a "two reads, one
// comparison" change rather than a new fact-provider surface.

import (
	"context"
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// PeriodDeltaWindowDays is the comparison window: the prior point is
// exactly this many days before the current point's own as-of instant.
const PeriodDeltaWindowDays = 30

// PeriodDeltaBandUnknown is the closed sentinel this package's own
// `severity` field already uses for "no known band" (health.go). Reused,
// never re-spelled, so a transition computed here can never disagree with
// what the SAME point's own severity field says.
const PeriodDeltaBandUnknown = "unknown"

// periodDeltaBandOrder is the ordinal reading of the three real bands
// health.go emits. "unknown" is deliberately ABSENT: it is not a fourth
// rung below "low", it is the absence of a rung, and mapping it to a
// number would let it silently rank against a real reading (the same
// reasoning health.go's own severity-max aggregate already applies when it
// excludes "unknown" from MAX so a data gap can never win against, or
// stand in for, a real band).
var periodDeltaBandOrder = map[string]int{
	"low":      1,
	"elevated": 2,
	"high":     3,
}

// PeriodDeltaTransition is the closed vocabulary of a two-point band
// comparison (D2: a TRANSITION between bands, never a raw score
// difference). Every member is content-safe: no subject identifier, no
// question text, no canonical fact value.
type PeriodDeltaTransition string

const (
	// PeriodDeltaTransitionUnchanged: the same real band at both points.
	PeriodDeltaTransitionUnchanged PeriodDeltaTransition = "unchanged"
	// PeriodDeltaTransitionImproved: the current point's real band ranks
	// lower than the point PeriodDeltaWindowDays before it.
	PeriodDeltaTransitionImproved PeriodDeltaTransition = "improved"
	// PeriodDeltaTransitionWorsened: the current point's real band ranks
	// higher than the point PeriodDeltaWindowDays before it.
	PeriodDeltaTransitionWorsened PeriodDeltaTransition = "worsened"
	// PeriodDeltaTransitionUnknownPrior: the CURRENT point carries a real
	// band; the PRIOR point does not (never recorded, or recorded but
	// stale beyond CHAOS-5952's own freshness window). A transition FROM
	// unknown is not "improved" or "worsened" -- it is a comparison that
	// cannot be made, and North Star check 12 requires the missing side
	// say so rather than default to unchanged.
	PeriodDeltaTransitionUnknownPrior PeriodDeltaTransition = "unknown_prior"
	// PeriodDeltaTransitionUnknownCurrent: the mirror case -- a real prior
	// band, no real current band.
	PeriodDeltaTransitionUnknownCurrent PeriodDeltaTransition = "unknown_current"
	// PeriodDeltaTransitionUnknownBoth: neither point carries a real band.
	PeriodDeltaTransitionUnknownBoth PeriodDeltaTransition = "unknown_both"
)

// periodDeltaTransitions is the closed vocabulary in evaluation order, for
// the same "declared, not inferred" reason every other closed vocabulary in
// this package carries its own Vocabulary()/Valid() pair.
var periodDeltaTransitions = [...]PeriodDeltaTransition{
	PeriodDeltaTransitionUnchanged,
	PeriodDeltaTransitionImproved,
	PeriodDeltaTransitionWorsened,
	PeriodDeltaTransitionUnknownPrior,
	PeriodDeltaTransitionUnknownCurrent,
	PeriodDeltaTransitionUnknownBoth,
}

// PeriodDeltaTransitionVocabulary returns the closed vocabulary.
func PeriodDeltaTransitionVocabulary() [len(periodDeltaTransitions)]PeriodDeltaTransition {
	return periodDeltaTransitions
}

// ValidPeriodDeltaTransition reports membership. The empty value is not a
// member: classifyPeriodDeltaTransition is total over its two inputs.
func ValidPeriodDeltaTransition(value PeriodDeltaTransition) bool {
	for _, member := range periodDeltaTransitions {
		if member == value {
			return true
		}
	}
	return false
}

// classifyPeriodDeltaTransition compares two severity bands -- already the
// closed {low, elevated, high, unknown} vocabulary health.go emits -- into
// a TRANSITION. It is PURE and TOTAL: every (priorBand, currentBand) pair,
// including an out-of-vocabulary string (treated the same as "unknown",
// never as a crash or a silently-ranked value), maps to exactly one
// member.
func classifyPeriodDeltaTransition(priorBand, currentBand string) PeriodDeltaTransition {
	priorRank, priorKnown := periodDeltaBandOrder[priorBand]
	currentRank, currentKnown := periodDeltaBandOrder[currentBand]
	switch {
	case !priorKnown && !currentKnown:
		return PeriodDeltaTransitionUnknownBoth
	case !priorKnown:
		return PeriodDeltaTransitionUnknownPrior
	case !currentKnown:
		return PeriodDeltaTransitionUnknownCurrent
	case currentRank > priorRank:
		return PeriodDeltaTransitionWorsened
	case currentRank < priorRank:
		return PeriodDeltaTransitionImproved
	default:
		return PeriodDeltaTransitionUnchanged
	}
}

// PeriodDeltaGrain names which grain actually served the period-delta
// comparison for one subject -- SubjectTeam for a native team subject,
// SubjectProject when D1's disclosed team/repo-ownership rollup served a
// project subject. Never a third value: composeStatusCategoryRequirements
// only ever composes for these two kinds (statusCategoryFactKindComposition).
type PeriodDeltaGrain = SubjectKind

// PeriodDeltaCompositionEvent reports ONE period-delta
// comparison decision, mirroring CategoryFactCompositionEvent's own
// content-safe shape: closed enums and counts only -- no subject
// identifier, no question text, no canonical fact value.
type PeriodDeltaCompositionEvent struct {
	// RequirementKind is always FactStatus today, carried explicitly for
	// the same reason CategoryFactCompositionEvent carries it: a future
	// second composed category that also derives period_delta is visible
	// in the event shape the moment it exists, not inferred from "this
	// event fired at all".
	RequirementKind FactKind
	// SubjectKind is the resolved subject kind this decision applies to.
	SubjectKind SubjectKind
	// Grain is the grain that actually served the comparison for this
	// subject kind -- see PeriodDeltaGrain's doc comment. Equal to
	// SubjectKind for a native team subject; SubjectProject for a project
	// subject served via D1's disclosed rollup (never a silent
	// substitution to SubjectTeam, because no team subject is minted).
	Grain PeriodDeltaGrain
	// ComposedKinds is the CHAOS-4347 composed fact-kind set this
	// comparison was read over, in the same deterministic order
	// statusCategoryFactKindComposition declares.
	ComposedKinds []FactKind
	// PriorReadIssued is whether a genuine second, distinct as-of read ran
	// (true) or the comparison could not be attempted at all for this
	// subject kind this turn (false -- e.g. zero eligible subjects
	// resolved). It is NOT whether either point resolved a real band:
	// TransitionCounts below already reports that distribution, including
	// unknown_both.
	PriorReadIssued bool
	// TransitionCounts tallies every subject this decision covered by its
	// resulting PeriodDeltaTransition, INCLUDING ZERO MEMBERS -- the same
	// "a distribution that omits its empty members cannot be told apart
	// from one whose derivation never reaches them" rule
	// ServerStatusCounters already applies.
	TransitionCounts map[PeriodDeltaTransition]int
}

// newPeriodDeltaTransitionCounts returns a zeroed counts map covering the
// whole closed vocabulary.
func newPeriodDeltaTransitionCounts() map[PeriodDeltaTransition]int {
	counts := make(map[PeriodDeltaTransition]int, len(periodDeltaTransitions))
	for _, member := range periodDeltaTransitions {
		counts[member] = 0
	}
	return counts
}

// periodDeltaEligibleSubjectKinds is the closed set of subject kinds this
// ticket wires a period-delta comparison for -- exactly the two keys
// statusCategoryFactKindComposition carries (chaos4347_status_category_
// composition.go). A subject kind with no composition entry (work_item, or
// any future kind that table has not caught up to) is never eligible: its
// bare FactStatus requirement was never expanded into the composed set in
// the first place, so there is no composed read to diff.
func periodDeltaEligibleSubjectKinds() []SubjectKind {
	kinds := make([]SubjectKind, 0, len(statusCategoryFactKindComposition))
	for kind := range statusCategoryFactKindComposition {
		if kind == SubjectTeam || kind == SubjectProject {
			kinds = append(kinds, kind)
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// periodDeltaEligibleSubjects filters subjects down to the eligible kinds,
// preserving order, so the caller reads facts for exactly (and only) the
// subjects a comparison can legitimately be attempted for.
func periodDeltaEligibleSubjects(subjects []SubjectRef) []SubjectRef {
	eligible := make([]SubjectRef, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Kind == SubjectTeam || subject.Kind == SubjectProject {
			eligible = append(eligible, subject)
		}
	}
	return eligible
}

// periodDeltaFieldSeverity reads the closed severity band a CanonicalFact's
// Fields carry, defaulting to PeriodDeltaBandUnknown for a fact that has no
// "severity" field at all (never a crash, never a fabricated real band) --
// the same "missing is not healthy, and is distinct from a recorded
// unknown" posture as the rest of this package, though here the two
// collapse to the same disclosed transition member (unknown_prior/
// unknown_current/unknown_both) either way.
func periodDeltaFieldSeverity(fact CanonicalFact) string {
	value, ok := fact.Fields["severity"]
	if !ok || value.String == nil {
		return PeriodDeltaBandUnknown
	}
	return *value.String
}

// periodDeltaFieldAsOf reads the closed "severity_as_of" disclosure day a
// CanonicalFact's Fields carry, or "" when absent (an unknown band never
// carries an as-of day).
func periodDeltaFieldAsOf(fact CanonicalFact) string {
	value, ok := fact.Fields["severity_as_of"]
	if !ok || value.String == nil {
		return ""
	}
	return *value.String
}

// periodDeltaCurrentAsOf resolves the CURRENT point's own as-of instant
// from the investigation's own clamped, resolved TimeContext -- the
// request's own as-of, never a second, independently-taken instant. A
// current-axis request's as-of is wallClock (the engine's own injected
// clock); a historical request's as-of/end is used verbatim, so a
// period-delta computed for a historical question compares two points BOTH
// in the past, exactly as far apart as PeriodDeltaWindowDays, rather than
// silently pulling one point up to the present instant.
func periodDeltaCurrentAsOf(tc TimeContext, wallClock time.Time) time.Time {
	switch tc.Axis {
	case TemporalValidTime:
		if tc.AsOf != nil {
			return *tc.AsOf
		}
	case TemporalRange:
		if tc.End != nil {
			return *tc.End
		}
	}
	return wallClock
}

// applyPeriodDelta enriches the CHAOS-4347 composed team/
// project FactHealth facts already on currentFacts with a genuine two-as-of
// period comparison, when the frame demands ObligationPeriodDelta.
//
// It mutates currentFacts' own Fields maps IN PLACE, before cohort
// grouping and synthesis run (engine.go calls this immediately after the
// primary fact read) -- so the added fields flow through the SAME
// CanonicalFact objects every existing downstream consumer already reads,
// with no new result field and no schema migration.
//
// ADDITIVE AND BEST-EFFORT: a second-read failure leaves currentFacts
// untouched and returns silently. period_delta had NO working comparison
// before this ticket (status_shadow.go's own entry says so); a transient
// failure enriching it must never newly fail an otherwise-servable turn
// that this obligation already degraded on.
func (e *Engine) applyPeriodDelta(ctx context.Context, principal storage.Principal, frame QuestionFrame, subjects []SubjectRef, currentFacts []CanonicalFact, currentAsOf time.Time) {
	if !frame.HasObligation(ObligationPeriodDelta) {
		return
	}
	eligible := periodDeltaEligibleSubjects(subjects)
	if len(eligible) == 0 {
		return
	}
	// The prior point is exactly PeriodDeltaWindowDays before the CURRENT
	// point's own as-of instant -- never wall-clock time and never a
	// calendar month.
	priorAsOf := currentAsOf.Add(-PeriodDeltaWindowDays * 24 * time.Hour)
	priorBundle, err := e.facts.ReadFacts(ctx, principal, CanonicalFactRequest{
		Question: InterpretedQuestion{
			TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &priorAsOf},
		},
		Subjects:     eligible,
		Requirements: []FactRequirement{{Kind: FactHealth, Subjects: eligible}},
	})
	if err != nil {
		return
	}
	priorByCanonicalID := make(map[string]CanonicalFact, len(priorBundle.Facts))
	for _, fact := range priorBundle.Facts {
		if fact.Kind != FactHealth {
			continue
		}
		priorByCanonicalID[fact.Subject.CanonicalID] = fact
	}
	countsByKind := map[SubjectKind]map[PeriodDeltaTransition]int{
		SubjectTeam:    newPeriodDeltaTransitionCounts(),
		SubjectProject: newPeriodDeltaTransitionCounts(),
	}
	touchedKind := make(map[SubjectKind]bool, 2)
	for i := range currentFacts {
		fact := &currentFacts[i]
		if fact.Kind != FactHealth {
			continue
		}
		if fact.Subject.Kind != SubjectTeam && fact.Subject.Kind != SubjectProject {
			continue
		}
		priorFact, hasPrior := priorByCanonicalID[fact.Subject.CanonicalID]
		currentBand := periodDeltaFieldSeverity(*fact)
		priorBand := PeriodDeltaBandUnknown
		priorAsOfDay := ""
		if hasPrior {
			priorBand = periodDeltaFieldSeverity(priorFact)
			priorAsOfDay = periodDeltaFieldAsOf(priorFact)
		}
		transition := classifyPeriodDeltaTransition(priorBand, currentBand)
		// Additive fields only -- every field readScope/readProjectHealth
		// already set on this fact (severity, severity_as_of,
		// severity_basis, severity_unavailable_reason, risk_rules,
		// risk_breakdown, ...) is untouched.
		fact.Fields["period_delta_transition"] = StringFactValue(string(transition))
		fact.Fields["period_delta_window_days"] = IntegerFactValue(PeriodDeltaWindowDays)
		fact.Fields["period_delta_prior_band"] = StringFactValue(priorBand)
		if priorAsOfDay != "" {
			fact.Fields["period_delta_prior_as_of"] = StringFactValue(priorAsOfDay)
		}
		touchedKind[fact.Subject.Kind] = true
		countsByKind[fact.Subject.Kind][transition]++
	}
	for _, kind := range periodDeltaEligibleSubjectKinds() {
		if !touchedKind[kind] {
			continue
		}
		e.recordPeriodDeltaComposition(ctx, principal, PeriodDeltaCompositionEvent{
			RequirementKind:  FactStatus,
			SubjectKind:      kind,
			Grain:            kind,
			ComposedKinds:    statusCategoryFactKindComposition[kind],
			PriorReadIssued:  true,
			TransitionCounts: countsByKind[kind],
		})
	}
}

// recordPeriodDeltaComposition emits one PeriodDeltaCompositionEvent. Needs
// no type assertion, mirroring recordCategoryFactComposition's own doc
// comment: RecordPeriodDeltaComposition is a method on EngineTelemetry
// itself, so a sink that drops it fails to compile.
func (e *Engine) recordPeriodDeltaComposition(ctx context.Context, principal storage.Principal, event PeriodDeltaCompositionEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordPeriodDeltaComposition(ctx, principal, event)
}
