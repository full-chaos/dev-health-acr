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
func edgeValidity(fromValidFrom, fromValidTo, toValidFrom, toValidTo *time.Time) (validFrom, validTo *time.Time) {
	return laterTime(fromValidFrom, toValidFrom), earlierTime(fromValidTo, toValidTo)
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
