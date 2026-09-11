package contextfabric

import (
	"math"
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
