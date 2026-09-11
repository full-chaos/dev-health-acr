package contextfabric

import (
	"math"
	"os"
	"strings"
	"testing"
)

// TestSanitizeLogIntDomain pins SanitizeLogInt over its whole input domain:
// zero, positive, negative, and int64's own boundary values (MaxSerializedBytes
// is never range-validated before this barrier runs).
func TestSanitizeLogIntDomain(t *testing.T) {
	cases := []int64{0, 1, -1, 42, 1 << 40, -(1 << 40), math.MaxInt64, math.MinInt64}
	for _, want := range cases {
		if got := SanitizeLogInt(want); got != want {
			t.Errorf("SanitizeLogInt(%d) = %d, want %d unchanged (a well-formed int64 must round-trip)", want, got, want)
		}
	}
}

// TestSanitizeLogIntRoutesThroughSanitizeLogAttr is a SOURCE-SHAPE pin, the
// same class as chaos5544_log_sanitizer_test.go's
// TestSanitizeLogAttrUsesTheRecognizedReplacerShape and for the identical
// reason: no digit string strconv.Itoa/FormatInt ever produces can contain
// \n or \r, so a mutation that neuters the SanitizeLogAttr call in the
// middle of SanitizeLogInt's round-trip -- leaving a bare
// Itoa-then-Atoi/ParseInt identity -- is an EQUIVALENT MUTANT: byte-for-byte
// identical output on every input. CodeQL cares about which SHAPE the
// value passed through, not the output, so this reads the source directly.
func TestSanitizeLogIntRoutesThroughSanitizeLogAttr(t *testing.T) {
	src, err := os.ReadFile("chaos5558_log_int_sanitizer.go")
	if err != nil {
		t.Fatalf("could not read chaos5558_log_int_sanitizer.go: %v", err)
	}
	text := string(src)
	if !strings.Contains(text, "SanitizeLogAttr(strconv.FormatInt(value, 10))") {
		t.Fatal("SanitizeLogInt must round-trip through SanitizeLogAttr, not merely Itoa/Atoi -- " +
			"the CALL is the recognized barrier shape, not the value")
	}
}

// TestMaxSerializedBytesIsWiredThroughSanitizeLogIntAtBothAlertSites is
// ANOTHER source-shape pin, the wiring counterpart to the one above:
// alerts #61/#68 (RecordPlanNarrowing, RecordBudgetAssertion) are the two
// named sites this ticket closes, and a mutation reverting either call
// site's "max_serialized_bytes" field back to the bare event.MaxSerializedBytes
// int64 is -- like the SanitizeLogInt-internal mutation above -- BEHAVIORALLY
// invisible: a well-formed int64 field logs identically either way, so a
// real-handler test cannot distinguish the two. This asserts the SOURCE
// wiring directly: exactly two call sites in telemetry.go route
// max_serialized_bytes through SanitizeLogInt, matching the two alert
// locations by name, neither the bare field.
func TestMaxSerializedBytesIsWiredThroughSanitizeLogIntAtBothAlertSites(t *testing.T) {
	src, err := os.ReadFile("telemetry.go")
	if err != nil {
		t.Fatalf("could not read telemetry.go: %v", err)
	}
	text := string(src)
	wired := strings.Count(text, `"max_serialized_bytes", SanitizeLogInt(event.MaxSerializedBytes)`)
	if wired != 2 {
		t.Fatalf("telemetry.go wires max_serialized_bytes through SanitizeLogInt at %d site(s), want exactly 2 "+
			"(RecordPlanNarrowing alert #61, RecordBudgetAssertion alert #68)", wired)
	}
	if strings.Contains(text, `"max_serialized_bytes", event.MaxSerializedBytes,`) {
		t.Fatal("telemetry.go still logs event.MaxSerializedBytes bare (unwrapped) at some site")
	}
	// Anchor the count itself is not vacuous -- the field name really
	// appears exactly twice in the file, both wired.
	if total := strings.Count(text, `"max_serialized_bytes",`); total != 2 {
		t.Fatalf("telemetry.go has %d max_serialized_bytes field(s) total, want exactly 2 -- this test's own "+
			"count assumption is stale, update it", total)
	}
}
