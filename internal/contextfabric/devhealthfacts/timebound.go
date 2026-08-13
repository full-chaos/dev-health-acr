package devhealthfacts

import (
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Historical time bounds (CHAOS-3781, TRD §19.8).
//
// This replaces checkCurrentTimeOnly, which refused every non-current axis
// outright. The refusal was honest while nothing here could answer for a
// past time; it is replaced -- not merely deleted -- by a per-provider
// answer to the same question, because the providers in this package are
// NOT uniform:
//
//   - Tier A, as-of native: the daily/periodic rollup tables carry a `day`
//     or window column and already pick the latest row. An as-of query only
//     moves the cut. resolveValidTimeBound serves these.
//   - Tier B, derivable: an entity table whose fact is a pure function of
//     immutable event timestamps the row already carries (a pull request
//     merged at T, a work item completed at T). Also resolveValidTimeBound,
//     with the derivation written into the provider's own SQL.
//   - Tier C, no history: a mutable attribute with no recorded history at
//     all -- a work item's status vocabulary, a title, a dependency row
//     that carries only last_synced. refuseHistoricalFact serves these; they
//     degrade honestly in coverage rather than answering with current data.
//
// §19.8.3 governs the split: reconstructing a fact that was never recorded
// is out of scope, and an absent record is unknown, never zero.

// observedTimeUnsupportedReason is returned for the observed_time axis by
// EVERY provider in this package, Tier A included.
//
// This is a measured limit, not a gap waiting to be filled. Observed time
// asks what Dev Health KNEW at a past instant, and no canonical source
// retains that:
//
//   - The daily rollup tables' computed_at is a RECOMPUTE stamp, not a
//     first-known stamp. Verified against live ClickHouse: repo_metrics_daily
//     holds `day` values spanning 2025-12-27 to 2026-08-13 while every
//     computed_at falls in 2026-07-02 to 2026-08-13, because the rollups
//     were backfilled. Filtering on computed_at <= a date before that
//     window returns zero rows -- not a smaller answer, a wrong one.
//   - The entity tables' only observation column is last_synced, and they
//     are ReplacingMergeTree: a re-sync DESTROYS the previous version, so
//     no prior observation survives to be queried.
//
// This is drift item D15 (event-time cursors miss backfilled and corrected
// rows) surfacing as a hard limit. The honest answer is that the fact
// sources cannot speak on this axis, which AC-3781-5 requires be reported
// as a limitation while the rest of the answer survives.
const observedTimeUnsupportedReason = "devhealthfacts: observed-time questions cannot be answered; no canonical source retains observation history, so what was known at a past instant is not recoverable"

// noHistoryUnsupportedReason is returned by a Tier C provider on any
// historical axis: the fact it reads is a mutable attribute whose previous
// values were overwritten, so there is nothing to answer FROM. Answering
// with the current value under a historical label is the exact false
// historical answer this issue exists to remove.
const noHistoryUnsupportedReason = "devhealthfacts: this fact has no recorded history, so it cannot answer for a past time; only its current value exists"

// incidentSeverityOmittedReason names the one field a historical incident
// fact drops (round-1 F2). Severity is revised in place with no recorded
// history, so it cannot be reported for a past time; the fact still
// carries the status it CAN derive, and this states what is missing rather
// than letting a reader assume the fact is complete.
//
// A fixed literal like every other reason in this package -- it never
// interpolates the query or the requested time (finding M6).
const incidentSeverityOmittedReason = "devhealthfacts: incident severity is omitted for a past time; it is revised in place with no recorded history, so only the derivable status is reported"

// outOfRetentionReason is reported when a bounded historical query
// returns NO rows (round-1 F8).
//
// A source that answered zero rows for a past time was previously still
// reported `available`, which reads as "this source answered and there was
// nothing to say" -- indistinguishable from a genuine empty result at a
// time the corpus does cover. The two are different answers: one means
// "nothing happened then", the other means "we do not retain data that far
// back". The design's service-bound section makes the second answerable
// rather than refusable, which only works if it is LABELED.
//
// This is `no_data`, not a failure: an absent record is unknown, never
// zero (§19.8.3, §3.5). The rest of the answer survives, exactly as
// AC-3781-5 requires for any single-source limitation.
//
// A fixed literal like every other reason here -- it never interpolates
// the requested time (finding M6).
const outOfRetentionReason = "devhealthfacts: no rows were retained at or before the requested time; this may predate the retained corpus rather than mean nothing happened"

// retentionState classifies a bounded query's row count. On the CURRENT
// axis it changes nothing -- zero rows has always meant an ordinary empty
// read there, and only a historical question raises the retention
// question at all.
func (b factTimeBound) retentionState(rowCount int) (contextfabric.SourceState, string) {
	if !b.active || rowCount > 0 {
		return contextfabric.SourceAvailable, ""
	}
	return contextfabric.SourceNoData, outOfRetentionReason
}

// factTimeBound is a resolved valid-time window for one provider query.
// The zero value is inactive, which is what the current axis needs: no
// predicate is added and the SQL keeps the exact text it had before
// CHAOS-3781.
type factTimeBound struct {
	active bool
	// hasStart distinguishes a range (both ends) from a point-in-time
	// request (upper bound only). A point-in-time query must NOT gain a
	// lower bound: "the state as of T" means the latest row at or before
	// T, however old that row is.
	hasStart bool
	start    time.Time
	end      time.Time
}

// resolveTimeBound is the single decision every Tier A and Tier B provider
// makes before querying. It returns either a bound to apply, or a
// degradation to return verbatim as the whole ReadFacts result.
//
// When degraded is true the caller MUST return (result, nil) immediately,
// without ever calling clickhouseFacts.query -- the same contract
// checkCurrentTimeOnly had.
//
// A zero-value Axis is treated as unsupported rather than as an implicit
// "current", exactly as before: buildFactQuery copies the axis from an
// already-validated request, and ContextFabricTimeContext.Validate rejects
// an empty axis, so an empty one arriving here is evidence of an
// unvalidated caller, never evidence the caller wants now.
func resolveTimeBound(query contextfabric.FactQuery) (bound factTimeBound, result contextfabric.FactProviderResult, degraded bool) {
	switch query.Time.Axis {
	case contextfabric.TemporalCurrent:
		return factTimeBound{}, contextfabric.FactProviderResult{}, false
	case contextfabric.TemporalValidTime:
		if query.Time.AsOf == nil {
			return factTimeBound{}, unsupportedTimeResult(noHistoryUnsupportedReason), true
		}
		return factTimeBound{active: true, end: query.Time.AsOf.UTC()}, contextfabric.FactProviderResult{}, false
	case contextfabric.TemporalRange:
		if query.Time.Start == nil || query.Time.End == nil {
			return factTimeBound{}, unsupportedTimeResult(noHistoryUnsupportedReason), true
		}
		return factTimeBound{active: true, hasStart: true, start: query.Time.Start.UTC(), end: query.Time.End.UTC()},
			contextfabric.FactProviderResult{}, false
	case contextfabric.TemporalObservedTime:
		return factTimeBound{}, unsupportedTimeResult(observedTimeUnsupportedReason), true
	default:
		return factTimeBound{}, unsupportedTimeResult(noHistoryUnsupportedReason), true
	}
}

// refuseHistoricalFact is the Tier C counterpart: a provider whose fact has
// no recorded history calls this and returns the result verbatim on any
// non-current axis.
func refuseHistoricalFact(query contextfabric.FactQuery) (contextfabric.FactProviderResult, bool) {
	if query.Time.Axis == contextfabric.TemporalCurrent {
		return contextfabric.FactProviderResult{}, false
	}
	if query.Time.Axis == contextfabric.TemporalObservedTime {
		return unsupportedTimeResult(observedTimeUnsupportedReason), true
	}
	return unsupportedTimeResult(noHistoryUnsupportedReason), true
}

// unsupportedTimeResult builds the degradation both paths return.
//
// State is not_applicable, NOT unconfigured (which is what the pre-CHAOS-3781
// refusal returned). unconfigured was wrong on its own terms: the source is
// present, reachable, and healthy -- it simply cannot speak for the
// requested time. §7.6 keeps those states distinct, and a dashboard reading
// unconfigured would send an operator looking for missing configuration
// that does not exist.
//
// Reason is one of this file's fixed literals and NEVER interpolates the
// requested time or any part of the query. fact_registry.go's
// classifyFactReadError copies it straight into the public result's
// coverage.sources[].reason (finding M6), so it must stay a constant.
func unsupportedTimeResult(reason string) contextfabric.FactProviderResult {
	return contextfabric.FactProviderResult{State: contextfabric.SourceNotApplicable, Reason: reason}
}

// dayPredicate bounds a DATE-grained column (the rollup tables' `day`).
// The parameter is a timestamp, so it is narrowed with toDate to compare
// against a Date column at that column's own grain -- which is exactly the
// day-grain effect the answer's temporal label reports.
func (b factTimeBound) dayPredicate(column string) string {
	if !b.active {
		return ""
	}
	predicate := fmt.Sprintf(" AND %s <= toDate({%s:DateTime64(6,'UTC')})", column, boundEndParam)
	if b.hasStart {
		predicate += fmt.Sprintf(" AND %s >= toDate({%s:DateTime64(6,'UTC')})", column, boundStartParam)
	}
	return predicate
}

// timestampPredicate bounds a DateTime64 column (computed_at, created_at,
// submitted_at, and the interval columns Tier B derives from).
func (b factTimeBound) timestampPredicate(column string) string {
	if !b.active {
		return ""
	}
	predicate := fmt.Sprintf(" AND %s <= {%s:DateTime64(6,'UTC')}", column, boundEndParam)
	if b.hasStart {
		predicate += fmt.Sprintf(" AND %s >= {%s:DateTime64(6,'UTC')}", column, boundStartParam)
	}
	return predicate
}

// existencePredicate bounds an entity's own START column -- "did this
// exist yet at the requested time".
//
// It applies ONLY the upper bound, never the lower one, even for a range.
// That asymmetry is the point: an entity created BEFORE a requested window
// still existed during it, so bounding its creation below would silently
// drop exactly the long-lived subjects a historical question is usually
// about. Contrast dayPredicate/timestampPredicate, which bound a
// PERIOD row -- there, a row outside the window genuinely describes a
// different period and must be excluded.
func (b factTimeBound) existencePredicate(column string) string {
	if !b.active {
		return ""
	}
	return fmt.Sprintf(" AND %s <= {%s:DateTime64(6,'UTC')}", column, boundEndParam)
}

// asOfExpression renders the requested instant for use inside a derived
// expression -- Tier B's "was it merged at T" comparisons, which are not a
// simple column bound. It is the END of the window in every case: a range
// question about a derived state is answered at the end of the range, the
// same instant the temporal label reports as effective.
func (b factTimeBound) asOfExpression() string {
	return fmt.Sprintf("{%s:DateTime64(6,'UTC')}", boundEndParam)
}

// bindings returns the parameters the predicates above reference. Empty
// when inactive, so no unused parameter is ever sent.
func (b factTimeBound) bindings() []timeBinding {
	if !b.active {
		return nil
	}
	bindings := []timeBinding{{Name: boundEndParam, Value: b.end}}
	if b.hasStart {
		bindings = append(bindings, timeBinding{Name: boundStartParam, Value: b.start})
	}
	return bindings
}

// Provider grains (CHAOS-3781 round-1 F1). Each provider declares the
// precision it can actually answer a historical question at, because the
// providers in this package are NOT uniform and composing one answer-level
// grain from a single assumption was observably wrong: a pull request
// merged at 14:00Z reported under a day grain reads as though the answer
// only knew about midnight.
//
// grainDaily -- the `*_daily` rollup tables. A row describes a whole DAY,
// so the best this can say is which day's row was current at the
// requested time. It cannot speak for an instant within that day.
//
// grainExact -- everything whose answer comes from a timestamp that is
// itself exact: Tier B's derivations from immutable event columns (merged
// at, resolved at, finished at, completed at), and the Tier A sources
// whose rows are point observations rather than daily buckets
// (capacity_forecasts' computed_at, backfill_log's created_at). "The
// latest such row at or before T" is exactly true at T, with no bucket
// rounding.
const (
	grainDaily = contextfabric.GrainDay
	grainExact = contextfabric.GrainInstant
)

// effectiveGrain reports the grain this bound answers at for one provider,
// so the engine can compose the answer's temporal label without each
// provider restating the composition rule.
//
// An INACTIVE bound is the current axis, which is exact by construction --
// "now" needs no bucket -- so it reports grainExact regardless of what the
// provider would answer historically. The registry only counts a grain
// from a provider that actually contributed facts, so a current-axis
// answer's grain never reaches a temporal label anyway (there is none).
func (b factTimeBound) effectiveGrain(providerGrain contextfabric.TemporalGrain) contextfabric.TemporalGrain {
	if !b.active {
		return grainExact
	}
	return providerGrain
}

type timeBinding struct {
	Name  string
	Value time.Time
}

const (
	boundStartParam = "time_start"
	boundEndParam   = "time_end"
)
