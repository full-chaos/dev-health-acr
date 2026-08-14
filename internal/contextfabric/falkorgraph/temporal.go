package falkorgraph

import (
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Temporal admission (CHAOS-3781, TRD §19.8, AC-3781-4/AC-3781-7).
//
// Before this, every read query here filtered on org_id and identity only:
// valid_from/valid_to were read back and passed to graphrank as display
// data, and nothing ever dropped an element whose validity window had
// closed. A historical question therefore had no way to see a different
// graph than a current one.
//
// The predicate below is INTERVAL OVERLAP, and a point-in-time request is
// expressed as the degenerate interval [T, T] rather than as a separate
// predicate -- one shape, one place to get right:
//
//	point-in-time T: valid_from <= T   AND valid_to >  T
//	range [S, E]:    valid_from <= E   AND valid_to >  S
//
// Both are half-open [valid_from, valid_to), so an element that ends
// exactly at T is NOT returned at T. Adjacent intervals therefore
// partition cleanly, and a subject that ended and was replaced at the same
// instant cannot be counted twice.
//
// AC-3781-7: every comparison is on the epoch-nanosecond `_ns` property,
// never the RFC3339Nano string half. See nsTimestamp's doc comment for the
// live-verified reason string comparison is wrong here -- Go's
// time.Format trims trailing zeros, so a whole-second and a sub-second
// timestamp render at different lengths and compare lexicographically in
// the wrong order.

// temporalParamStart and temporalParamEnd name the two Cypher parameters
// the predicate binds. They are parameters, never interpolated values, so
// a requested instant can never reach the query text itself.
const (
	temporalParamStart = "tStart"
	temporalParamEnd   = "tEnd"
)

// temporalFilter is one request's admission window, resolved from the
// interpreted question's time context. The zero value is inactive, which
// is exactly what a current-axis request needs: no predicate is added and
// every query keeps the text it had before CHAOS-3781.
type temporalFilter struct {
	active  bool
	startNs int64
	endNs   int64
}

// newTemporalFilter builds the filter for one time context. A current axis
// (or any context missing the bounds its axis requires) yields an inactive
// filter rather than an error: request validation upstream already proved
// the shape, and failing open to "no temporal predicate" here would be
// wrong only if the axis were historical -- which resolveTimeContext in
// the engine refuses to let through unbounded.
func newTemporalFilter(timeContext contextfabric.TimeContext) temporalFilter {
	switch timeContext.Axis {
	case contextfabric.TemporalValidTime, contextfabric.TemporalObservedTime:
		if timeContext.AsOf == nil {
			return temporalFilter{}
		}
		instant := nsTimestamp(*timeContext.AsOf)
		return temporalFilter{active: true, startNs: instant, endNs: instant}
	case contextfabric.TemporalRange:
		if timeContext.Start == nil || timeContext.End == nil {
			return temporalFilter{}
		}
		return temporalFilter{active: true, startNs: nsTimestamp(*timeContext.Start), endNs: nsTimestamp(*timeContext.End)}
	default:
		return temporalFilter{}
	}
}

// predicate renders the admission clause for one node or edge alias,
// prefixed with " AND " so it appends onto an existing WHERE. It returns
// the empty string when inactive, so a current-axis query is textually
// identical to what it was before CHAOS-3781.
//
// A NULL bound is UNBOUNDED, not unknown, on both sides:
//
//   - valid_to NULL means open-ended -- an open work item, an unmerged
//     pull request, a running CI job -- so it is valid at every time at or
//     after its start. Treating it as "ended" would drop exactly the live
//     elements a historical question most often asks about.
//   - valid_from NULL means no recorded lower bound. CHAOS-3785 made the
//     owned write assert a nil explicitly rather than leave a stale value,
//     so a NULL here is the canonical source SAYING there is no bound, not
//     an omission.
//
// An element with BOTH bounds NULL is therefore admitted at every
// requested time. That is deliberate, and it is the honest-but-uncomfortable
// case: excluding it would empty the graph for any organization projected
// before the validity-window producer landed, while admitting it silently
// would answer a historical question with an element that was never shown
// to have been true then. It is admitted AND counted -- see
// countUnboundedValidity, whose count reaches the answer's coverage and
// limitations so a reader can see how much of it rests on unbounded
// elements.
func (f temporalFilter) predicate(alias string) string {
	if !f.active {
		return ""
	}
	return fmt.Sprintf(
		" AND (%[1]s.%[2]s IS NULL OR %[1]s.%[2]s <= $%[3]s) AND (%[1]s.%[4]s IS NULL OR %[1]s.%[4]s > $%[5]s)",
		alias, propValidFromNs, temporalParamEnd, propValidToNs, temporalParamStart,
	)
}

// bind adds the predicate's parameters to a query's parameter map. It is a
// no-op when inactive, so no unused parameter is ever sent. The map is
// mutated and returned for call-site brevity.
func (f temporalFilter) bind(params map[string]interface{}) map[string]interface{} {
	if !f.active {
		return params
	}
	if params == nil {
		params = map[string]interface{}{}
	}
	params[temporalParamStart] = f.startNs
	params[temporalParamEnd] = f.endNs
	return params
}

// hasUnboundedValidity reports whether an element carries NO validity
// bound at all -- the case predicate admits at every requested time. Both
// the `_ns` keys must be absent or nil: a half-bounded element (an open
// work item, say) is genuinely bounded on the side that matters and is
// not counted here.
//
// Reading the `_ns` half rather than the RFC3339Nano half keeps this
// consistent with what the predicate actually compared.
func hasUnboundedValidity(attributes map[string]interface{}) bool {
	return attributes[propValidFromNs] == nil && attributes[propValidToNs] == nil
}
