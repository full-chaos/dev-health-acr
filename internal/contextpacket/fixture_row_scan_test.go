package contextpacket_test

import (
	"fmt"
	"testing"
	"time"
)

// scanFixtureColumn assigns raw into destination the way a real ClickHouse
// driver's Scan populates a typed destination pointer. It backs every
// package fixture ClickHouseRowScanner in this package (rowScanner in
// source_executor_test.go, locatorQueryRows in evidence_production_test.go)
// so the column-mapping logic exists in exactly one place instead of being
// copy-pasted per fixture.
//
// Every case fails loudly on a type mismatch instead of silently leaving the
// destination at its zero value -- including the **time.Time (Nullable
// event_at) case. A naive `value, _ := raw.(*time.Time)` comma-ok assertion
// there would swallow a mistyped fixture value (e.g. a bare string, or an
// untyped nil) into a silent nil, which is indistinguishable from a
// legitimately absent event_at ((*time.Time)(nil), used throughout this
// package's fixtures to mean "no real event time"). Requiring the ok here
// keeps that one legitimate case working while turning every other mistake
// into a loud test failure instead of a quietly wrong assertion later.
func scanFixtureColumn(destination any, raw any) error {
	switch dest := destination.(type) {
	case *string:
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("fixture scan: want string, got %T (%#v)", raw, raw)
		}
		*dest = value
	case *float64:
		value, ok := raw.(float64)
		if !ok {
			return fmt.Errorf("fixture scan: want float64, got %T (%#v)", raw, raw)
		}
		*dest = value
	case *time.Time:
		value, ok := raw.(time.Time)
		if !ok {
			return fmt.Errorf("fixture scan: want time.Time, got %T (%#v)", raw, raw)
		}
		*dest = value
	case **time.Time:
		value, ok := raw.(*time.Time)
		if !ok {
			return fmt.Errorf("fixture scan: want *time.Time (nullable event_at; use (*time.Time)(nil) for absent), got %T (%#v)", raw, raw)
		}
		*dest = value
	default:
		return fmt.Errorf("fixture scan: unsupported destination %T", destination)
	}
	return nil
}

// TestScanFixtureColumn_failsLoudlyOnMistypedEventAt is the regression test
// for the comma-ok bug fixed above: a fixture row whose event_at column is
// the wrong type (a plain string, or an untyped nil rather than a typed
// (*time.Time)(nil)) must produce a scan error, never a silently absent
// event_at. Before this fix, `value, _ := raw.(*time.Time)` accepted either
// mistake and left the destination nil -- indistinguishable from the
// legitimate "no real event time" case.
func TestScanFixtureColumn_failsLoudlyOnMistypedEventAt(t *testing.T) {
	var eventAt *time.Time

	if err := scanFixtureColumn(&eventAt, "not-a-time-pointer"); err == nil {
		t.Fatalf("want error scanning a string into **time.Time, got nil (eventAt=%v)", eventAt)
	}
	if err := scanFixtureColumn(&eventAt, nil); err == nil {
		t.Fatalf("want error scanning an untyped nil into **time.Time, got nil (eventAt=%v)", eventAt)
	}

	// The one legitimate nullable case must still work: a typed nil pointer
	// means "no real event time", not a mistake.
	if err := scanFixtureColumn(&eventAt, (*time.Time)(nil)); err != nil {
		t.Fatalf("typed nil *time.Time must scan cleanly as an absent event_at: %v", err)
	}
	if eventAt != nil {
		t.Fatalf("eventAt = %v, want nil", eventAt)
	}

	present := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := scanFixtureColumn(&eventAt, &present); err != nil {
		t.Fatalf("*time.Time must scan cleanly: %v", err)
	}
	if eventAt == nil || !eventAt.Equal(present) {
		t.Fatalf("eventAt = %v, want %v", eventAt, present)
	}
}
