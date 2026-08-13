package contextfabric

import (
	"fmt"
	"strings"
	"time"
)

// Historical time axis (CHAOS-3781, TRD §19.8).
//
// This file holds what replaced requireCurrentTimeAxis: the engine's
// definition of which historical questions it can honestly answer, and how
// the resulting answer states the time it speaks for.
//
// The refusal it replaces was not a bug. While no layer below could bound
// itself to a past time, answering a historical question at all would have
// meant presenting current data under a historical label. What changed is
// the layers, not the principle: the graph now admits by validity window
// and the fact providers now bound or honestly decline, so the engine can
// stop refusing and start labeling.

// maxHistoricalRangeDays bounds how wide a range question may be.
//
// A range is answered by reading every source across the window, so an
// unbounded width is an unbounded read. 400 days sits above the ~230-day
// span the canonical rollup tables actually hold, so it rejects only
// requests no source could answer anyway.
const maxHistoricalRangeDays = 400

// futureSkewTolerance is how far past `now` a requested instant may sit
// before it is refused. A caller's clock is not this service's clock, and
// a few seconds of skew is not a prediction request; a minute is generous
// for that without admitting a genuine question about the future.
const futureSkewTolerance = time.Minute

// ErrInvalidTimeBound identifies a historical request whose bounds this
// engine will not answer: a time in the future, or a range wider than
// maxHistoricalRangeDays.
//
// It is deliberately DISTINCT from the retired ErrUnsupportedTimeAxis.
// That error meant "this service cannot answer historical questions at
// all", which is no longer true of any axis. This one means "this
// particular window is not answerable", which is a bounds problem with the
// request. Both map to a non-retryable 400, but conflating them would tell
// a caller their whole class of question is unsupported when only their
// bounds were wrong.
//
// A time merely EARLIER than any retained data is not an error and must
// never become one: "we have nothing that far back" is a real answer, and
// it is delivered as an answer whose sources all report no data.
var ErrInvalidTimeBound = fmt.Errorf("context fabric time bounds are not answerable")

// validateTimeContext is the single definition of what this engine can
// honestly answer, shared by the wire-request check and the
// post-interpretation check so the two can never diverge -- the same role
// requireCurrentTimeAxis played, with a different verdict.
//
// The current axis is always answerable. A historical axis is answerable
// when its bounds are shaped correctly (already proved by the contract),
// sit at or before now, and describe a window this service will read.
func validateTimeContext(timeContext TimeContext, now time.Time) error {
	horizon := now.Add(futureSkewTolerance)
	switch timeContext.Axis {
	case TemporalCurrent:
		return nil
	case TemporalValidTime, TemporalObservedTime:
		if timeContext.AsOf == nil {
			return fmt.Errorf("%w: %s requires an as-of time", ErrInvalidTimeBound, timeContext.Axis)
		}
		if timeContext.AsOf.After(horizon) {
			// §19.8.3: the axis is historical, not speculative.
			return fmt.Errorf("%w: as-of time is in the future", ErrInvalidTimeBound)
		}
		return nil
	case TemporalRange:
		if timeContext.Start == nil || timeContext.End == nil {
			return fmt.Errorf("%w: range requires a start and an end", ErrInvalidTimeBound)
		}
		if timeContext.End.Before(*timeContext.Start) {
			return fmt.Errorf("%w: range end precedes its start", ErrInvalidTimeBound)
		}
		if timeContext.End.After(horizon) {
			return fmt.Errorf("%w: range end is in the future", ErrInvalidTimeBound)
		}
		if timeContext.End.Sub(*timeContext.Start) > maxHistoricalRangeDays*24*time.Hour {
			return fmt.Errorf("%w: range is wider than %d days", ErrInvalidTimeBound, maxHistoricalRangeDays)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown time axis %q", ErrInvalidTimeBound, timeContext.Axis)
	}
}

// composeTemporalLabel builds the answer's statement of what time it speaks
// for (AC-3781-2), from the interpreted question and the coverage the
// sources actually returned.
//
// Returns nil on the current axis, which keeps every current-axis result
// byte-identical to what it was before CHAOS-3781.
//
// The effective time is derived from the sources that ANSWERED, not from
// what was requested: a day-grain rollup cannot speak for an instant, so
// an answer built from one says so rather than implying precision it does
// not have.
func composeTemporalLabel(interpretation InterpretedQuestion, coverage Coverage) *TemporalLabel {
	requested := interpretation.TimeContext
	if requested.Axis == TemporalCurrent {
		return nil
	}
	grain, complete := temporalCoverage(coverage)
	if requested.Axis == TemporalObservedTime {
		// No source in this system retains observation history: the
		// canonical rollups' computed_at is a recompute stamp and the
		// entity tables are ReplacingMergeTree (see devhealthfacts'
		// observedTimeUnsupportedReason), while the graph's own
		// observed_at is reset by any rebuild, which is why the graph
		// admits on the VALID-time window on this axis too.
		//
		// That approximation is defensible -- a valid-time window is far
		// closer to the truth than no filtering at all -- but it must
		// never be presented AS observed time. So the label reports what
		// is actually true of this answer: no source spoke on the
		// requested axis, and coverage is incomplete, whatever the graph
		// returned. temporalLimitationsFor states the substitution in
		// words alongside it.
		return &TemporalLabel{
			Requested:        requested,
			Effective:        effectiveTimeContext(requested, GrainNone),
			Grain:            GrainNone,
			CoverageComplete: false,
		}
	}
	return &TemporalLabel{
		Requested:        requested,
		Effective:        effectiveTimeContext(requested, grain),
		Grain:            grain,
		CoverageComplete: complete,
	}
}

// temporalCoverage reads the answer's own coverage to decide the grain it
// achieved and whether every source could speak for the requested time.
//
// A source that reported not_applicable for a temporal reason is what
// makes coverage incomplete (AC-3781-5). Sources are matched on the fixed
// reason literals the providers and the graph adapter emit -- never on
// free text, and never on the source name, which is not a stable
// vocabulary.
func temporalCoverage(coverage Coverage) (TemporalGrain, bool) {
	complete := true
	answered := false
	for _, source := range coverage.Sources {
		switch {
		case isTemporalDegradation(source):
			complete = false
		case source.State == SourceAvailable:
			answered = true
		}
	}
	if !answered {
		// Nothing spoke for the requested time. The honest grain is
		// none: the answer carries no fact coverage on this axis, which
		// is the steady state for observed_time.
		return GrainNone, false
	}
	// Any contributing canonical fact source is day-grained at best (the
	// rollup tables are the only ones that answer a valid-time question
	// natively), so an answer that used one speaks for a day, not an
	// instant. Claiming instant precision here would be the smaller,
	// quieter version of the same false-precision problem this issue
	// exists to remove.
	return GrainDay, complete
}

// temporalDegradationMarkers are the fixed substrings a source's Reason
// carries when it could not speak for the requested time. They are matched
// rather than compared whole so a provider may prefix its own package name,
// which devhealthfacts already does.
var temporalDegradationMarkers = []string{
	"cannot answer for a past time",
	"observed-time questions cannot be answered",
	"no validity window",
}

func isTemporalDegradation(source SourceObservation) bool {
	if source.State != SourceNotApplicable {
		return false
	}
	for _, marker := range temporalDegradationMarkers {
		if strings.Contains(source.Reason, marker) {
			return true
		}
	}
	return false
}

// effectiveTimeContext narrows the requested context to what the achieved
// grain can actually speak for.
//
// At day grain a point-in-time answer speaks for the START of the
// requested day, because that is the last instant a day-grained row is
// known to describe -- rounding forward would claim the answer covers time
// it never read. Effective therefore only ever moves EARLIER, which is the
// direction ContextFabricTemporalLabel.Validate enforces.
func effectiveTimeContext(requested TimeContext, grain TemporalGrain) TimeContext {
	effective := TimeContext{Axis: requested.Axis}
	switch requested.Axis {
	case TemporalValidTime, TemporalObservedTime:
		asOf := *requested.AsOf
		if grain == GrainDay {
			asOf = truncateToDay(asOf)
		}
		effective.AsOf = &asOf
	case TemporalRange:
		start, end := *requested.Start, *requested.End
		if grain == GrainDay {
			// The start moves FORWARD to the first day the window covers
			// in full, and the end BACKWARD to the last one, so the
			// effective window stays inside the requested one at both
			// ends.
			//
			// A start already ON a day boundary does NOT move: that day
			// is covered in full, and rounding it up anyway would
			// under-report a whole day of coverage the answer genuinely
			// has. Only a start partway through a day rounds up, because
			// that day is not covered in full.
			start = ceilToDay(start)
			end = truncateToDay(end)
			// A window spanning less than one whole day covers no full
			// day at all -- rounding then leaves start after end.
			//
			// Collapsing to the rounded `end` would be WRONG here, and
			// not merely imprecise: truncateToDay can push that end
			// EARLIER than the requested start (09:00-17:00 on one day
			// truncates to 00:00 that day), producing an effective window
			// that starts before the caller asked about. Effective must
			// only ever narrow, so collapse to the requested END instead
			// -- a zero-width window that is inside the requested one by
			// construction, and the honest statement that no day-grain
			// source could speak for this span.
			if start.After(end) {
				start, end = *requested.End, *requested.End
			}
		}
		effective.Start = &start
		effective.End = &end
	}
	return effective
}

func truncateToDay(value time.Time) time.Time {
	utc := value.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

// ceilToDay rounds up to the next day boundary, leaving a value already ON
// one unchanged -- see effectiveTimeContext for why that distinction
// matters.
func ceilToDay(value time.Time) time.Time {
	truncated := truncateToDay(value)
	if truncated.Equal(value.UTC()) {
		return truncated
	}
	return truncated.Add(24 * time.Hour)
}

// temporalLimitations are the standing disclosures every historical answer
// carries. They describe limits of the SYSTEM, not of one request, so they
// are stated on every historical answer rather than inferred from coverage.
var temporalLimitations = []string{
	// The graph holds only the CURRENT projection, so a subject deleted
	// at source since the requested time is simply gone: what a
	// historical read can return is bounded by what still exists now.
	// Unfixable without an append-only graph history, which is out of
	// scope -- but never silent.
	"Subjects deleted at source since the requested time are not recoverable from the projected graph.",
}

// observedTimeLimitation states, in the answer itself, that the
// observed-time axis was not answered on its own terms.
//
// Without this the substitution is invisible: the caller asked what was
// KNOWN at a past instant, the graph answered from what was TRUE then, and
// nothing in the response would distinguish the two. That is the same
// shape of quiet mislabel the H6 refusal existed to prevent, one axis
// down, so it is disclosed rather than reasoned about only in code
// comments.
const observedTimeLimitation = "Observed-time questions cannot be answered on their own terms: no canonical source retains observation history, so this answer reflects what was TRUE at the requested time, not what was KNOWN then."

// temporalLimitationsFor returns the standing disclosures for one
// historical axis. They describe limits of the SYSTEM, not of one request,
// so they are stated on every historical answer rather than inferred from
// coverage.
func temporalLimitationsFor(axis TemporalAxis) []string {
	if axis == TemporalCurrent {
		return nil
	}
	limitations := append([]string(nil), temporalLimitations...)
	if axis == TemporalObservedTime {
		limitations = append(limitations, observedTimeLimitation)
	}
	return limitations
}

// appendTemporalLimitations adds the standing historical disclosures to a
// result's own limitations, skipping any already present so a re-composed
// result never accumulates duplicates (Limitations is bounded and required
// to be unique by the result contract).
func appendTemporalLimitations(limitations []string, interpretation InterpretedQuestion) []string {
	axis := interpretation.TimeContext.Axis
	if axis == TemporalCurrent {
		return limitations
	}
	existing := make(map[string]struct{}, len(limitations))
	for _, limitation := range limitations {
		existing[limitation] = struct{}{}
	}
	for _, limitation := range temporalLimitationsFor(axis) {
		if _, ok := existing[limitation]; ok {
			continue
		}
		limitations = append(limitations, limitation)
		existing[limitation] = struct{}{}
	}
	return limitations
}
