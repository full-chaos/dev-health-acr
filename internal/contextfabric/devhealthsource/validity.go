package devhealthsource

import "time"

// Validity windows (CHAOS-3781, TRD §19.8).
//
// Before CHAOS-3781 this producer set no ValidFrom/ValidTo on anything it
// emitted, so every canonical node and edge in a projected graph carried
// an ABSENT validity window. That made "graph reads respect validity
// windows" (AC-3781-4) a no-op: a historical read had nothing to filter
// on and would have returned the current graph under a historical label,
// which is exactly the false historical answer the H6 refusal existed to
// prevent. Populating the window here is therefore a precondition of the
// read-side filtering in falkorgraph, not an enhancement of it.
//
// The window is VALID time -- when the thing was true in the world --
// taken from the source row's own immutable interval columns, never from
// ObservedAt. ObservedAt is this producer's own observation time; a
// rebuild resets it, so using it as a validity proxy would make every
// historical read go empty after any rebuild.
//
// Half-open [ValidFrom, ValidTo) throughout, matching the read-side
// admission predicate: an element that ends exactly at T is not returned
// at T, so adjacent intervals partition with no double-count.
//
// A nil ValidTo means open-ended (still valid), which is the ordinary
// state of an open work item, an open pull request, or an unresolved
// incident. A nil ValidFrom means the source recorded no start; under
// CHAOS-3785 the owned write asserts that nil explicitly rather than
// leaving a stale value behind.

// nullableTimestamp renders the SELECT fragment for one nullable
// timestamp expression as the (isNotNull, ifNull) column PAIR this
// package already uses everywhere else -- see devhealthfacts/workitems.go's
// identical convention for completed_at. A bare Nullable(DateTime64) is
// never scanned into Go.
//
// expr is always an internal Go string literal built from column names
// this file controls, never a caller-supplied value, so inlining it into
// the statement is safe -- the same rule the row-limit constants follow.
func nullableTimestamp(expr string) string {
	return "isNotNull(" + expr + "), ifNull(" + expr + ", toDateTime64(0, 6, 'UTC'))"
}

// optionalTime rebuilds a *time.Time from the (present, value) pair
// nullableTimestamp selects. A zero present means the source column was
// NULL, which becomes a nil pointer -- an unbounded end of the interval,
// not a zero timestamp.
func optionalTime(present uint8, value time.Time) *time.Time {
	if present == 0 {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// requiredTime wraps a non-nullable source column as a *time.Time. Kept
// separate from optionalTime so a reader can tell at each call site
// whether the underlying column can actually be NULL, rather than
// inferring it from the SQL.
func requiredTime(value time.Time) *time.Time {
	utc := value.UTC()
	return &utc
}

// edgeValidity derives one relationship's validity window from its two
// endpoints: an association is valid only while BOTH ends are. That is
// the later of the two starts, and the earlier of the two ends.
//
// A nil start on either side means "no recorded lower bound", so it
// cannot make the window later and is skipped. A nil end means
// open-ended, so it cannot make the window earlier and is skipped too.
// Both nil on both sides yields (nil, nil) -- an unbounded edge, which
// the read side admits at every time and counts as unbounded rather than
// silently trusting.
//
// CHAOS-3825: the two endpoint windows can be DISJOINT, and then that
// intersection is EMPTY -- the later start falls after the earlier end,
// so the naive pair is inverted. This is ordinary data, not corruption: a
// pull request review submitted after its pull request merged is a
// post-merge approval, which GitHub allows and dev ClickHouse holds today
// (35 rows for one organization, tables.go:610's shape).
//
// Left inverted, that pair is rejected by
// ContextFabricRelationshipProjection ("valid_to precedes valid_from"),
// and because ContextFabricProjectionBatch.Validate() is all-or-nothing,
// ONE such row poisons the whole batch: NextProjectionBatch errors, the
// coordinator holds its checkpoint, and the same poisoned page rebuilds
// every tick -- the organization's projection is wedged forever. That is
// the identical wedge shape queryWorkItemHierarchy's self-reference
// filter (tables.go) already exists to prevent, reached through the
// temporal axis instead.
//
// An empty intersection is represented as the DEGENERATE half-open
// window [later-start, later-start): the association never held while
// both endpoints were valid, and a zero-width half-open interval states
// exactly that. The contract accepts it (only a STRICTLY earlier end is
// rejected), no time-filtered read admits it (no instant satisfies
// valid_from <= t < valid_to when the bounds are equal), and a structural
// read that ignores the temporal axis still sees the edge.
//
// The collapse is the ONLY bound this function invents. It never widens a
// window into an interval the source did not assert -- taking either
// endpoint's own end would claim the association held while the other end
// did not exist -- and it never drops the edge, which would silently lose
// a real association. Note the guard is deliberately strict (Before, not
// !After): a window that is ALREADY zero-width because the endpoints
// merely touch is untouched, since it is already the representation this
// returns.
func edgeValidity(fromValidFrom, fromValidTo, toValidFrom, toValidTo *time.Time) (validFrom, validTo *time.Time) {
	validFrom, validTo = laterTime(fromValidFrom, toValidFrom), earlierTime(fromValidTo, toValidTo)
	if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
		// A COPY, not the valid_from pointer itself: callers pass
		// pointers they also hold as an endpoint entity's own window
		// (tables.go:610 passes the review entity's ValidFrom straight
		// in), so aliasing the two bounds would let an adjustment to one
		// silently move the other.
		collapsed := *validFrom
		validTo = &collapsed
	}
	return validFrom, validTo
}

func laterTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	default:
		return a
	}
}

func earlierTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Before(*a):
		return b
	default:
		return a
	}
}
