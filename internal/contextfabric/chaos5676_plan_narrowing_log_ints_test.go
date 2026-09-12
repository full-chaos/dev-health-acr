package contextfabric

import (
	"os"
	"strings"
	"testing"
)

// THE REQUEST-DERIVED INTEGERS ON THE PLAN-NARROWING LINE.
//
// Three request-derived integers reach this line besides max_serialized_bytes:
// before/after, from the caller's own cohort-member option through the plan,
// and the plan's own item ceiling. The invariant is that EVERY request-derived
// integer on a log line goes through the numeric barrier -- not only the one a
// flow analysis happened to trace first -- because the analysis reports the
// line, and one unwired value makes the whole line a finding.
//
// SOURCE-SHAPE PINS, and the reason is the same one the barrier's own tests
// give: reverting any of these call sites to the bare int is BEHAVIOURALLY
// INVISIBLE. A well-formed integer logs identically whether or not it round
// trips through the barrier, so no handler-level test can tell the two apart
// and only the wiring itself can be asserted. The behavioural half -- that
// the barrier does not change the value an operator reads -- is pinned
// separately below, because that IS observable and it is the property a
// reader of the line depends on.

// TestPlanNarrowingRequestDerivedIntsAreWiredThroughTheNumericBarrier asserts
// the wiring for every request-derived integer on this line, not only the
// ones a tool has named so far.
func TestPlanNarrowingRequestDerivedIntsAreWiredThroughTheNumericBarrier(t *testing.T) {
	src, err := os.ReadFile("telemetry.go")
	if err != nil {
		t.Fatalf("could not read telemetry.go: %v", err)
	}
	text := string(src)

	for _, field := range []struct {
		key    string
		wired  string
		bare   string
		origin string
	}{
		{"before", `"before", requestDerivedLogInt(event.Before)`, `"before", event.Before,`,
			"the cohort-member count before narrowing, derived from the caller's own MaxCohortMembers option"},
		{"after", `"after", requestDerivedLogInt(event.After)`, `"after", event.After,`,
			"the same count after narrowing, from the same option through the plan"},
		{"max_items", `"max_items", requestDerivedLogInt(event.MaxItems)`, `"max_items", event.MaxItems,`,
			"the plan's own item ceiling, which the request's options feed"},
	} {
		if !strings.Contains(text, field.wired) {
			t.Errorf("telemetry.go does not route %q through the numeric log barrier -- %s, and a request-derived integer reaching a logger is the whole of this class",
				field.key, field.origin)
		}
		if strings.Contains(text, field.bare) {
			t.Errorf("telemetry.go still logs %q bare somewhere -- a single unwired site is the whole finding, since the analysis reports the LINE and not the field",
				field.key)
		}
	}
}

// TestBothNumericLogBarriersLeaveEveryValueUnchanged is the behavioural half:
// an operator reading these lines must see the same numbers either way.
//
// BOTH BARRIERS, because both are on the path. The int64 barrier carries the
// response-budget ceiling; the int barrier carries every value on the plan,
// assertion, accounting and allowance lines. A pin exercising only one would
// leave the other free to regress on exactly the lines it guards.
//
// It matters because each barrier is a format/parse round trip with a defined
// failure value of zero. A regression returning zero for ordinary input would
// silence the analysis and destroy the line at the same time, while every
// source-shape pin in this file went on passing.
func TestBothNumericLogBarriersLeaveEveryValueUnchanged(t *testing.T) {
	t.Parallel()

	executed := 0
	for _, value := range []int64{0, 1, 2, 30, 45, -1, 1 << 20, 9223372036854775807, -9223372036854775808} {
		if got := SanitizeLogInt(value); got != value {
			t.Errorf("SanitizeLogInt(%d) = %d, want the value unchanged -- a barrier that alters the number replaces a log-injection finding with a lying log line",
				value, got)
		}
		executed++
	}
	for _, value := range []int{0, 1, 2, 30, 45, -1, 1 << 20, int(^uint(0) >> 1), -int(^uint(0)>>1) - 1} {
		if got := requestDerivedLogInt(value); got != value {
			t.Errorf("requestDerivedLogInt(%d) = %d, want the value unchanged -- this is the barrier the plan-narrowing, budget-assertion, item-accounting and member-allowance lines all read through",
				value, got)
		}
		executed++
	}
	if executed == 0 {
		t.Fatal("no barrier input was exercised")
	}
}

// TestEveryMaxItemsLogSiteUsesTheSameBarrier is the CLASS assertion.
//
// "max_items" is logged at four sites in this file. They are the same value,
// from the same request option, into the same kind of sink, so they are one
// class and hold one invariant: every one of them goes through the int-typed
// barrier. A pin that covered only the line a flow analysis names would pass
// with the other three bare.
//
// The count is asserted as well as the wiring so this cannot quietly stop
// covering the class: a fifth site added later fails here rather than waiting
// to be reported from outside.
func TestEveryMaxItemsLogSiteUsesTheSameBarrier(t *testing.T) {
	src, err := os.ReadFile("telemetry.go")
	if err != nil {
		t.Fatalf("could not read telemetry.go: %v", err)
	}
	text := string(src)

	total := strings.Count(text, `"max_items",`)
	wired := strings.Count(text, `"max_items", requestDerivedLogInt(event.MaxItems),`)
	if total == 0 {
		t.Fatal("no max_items log attribute found in telemetry.go -- this pin's anchor is gone, so it is silently covering nothing")
	}
	if wired != total {
		t.Errorf("telemetry.go logs max_items at %d site(s) but only %d go through the int barrier -- the unwired remainder is the same request-derived value reaching the same kind of sink, which is the entire class",
			total, wired)
	}
	if total != 4 {
		t.Errorf("telemetry.go has %d max_items site(s), want the 4 this sweep enumerated -- a new one must be wired deliberately, not inherit the count", total)
	}
}
