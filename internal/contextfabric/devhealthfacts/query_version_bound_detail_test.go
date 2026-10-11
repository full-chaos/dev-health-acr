package devhealthfacts_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// A bounded synthesis input now serves per-kind coverage details, so an answer
// stored under an earlier version holds only the prose limitation and must not
// be reused.
func TestQueryVersionMovedPastTheBoundedInputDisclosure(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 146 {
		t.Fatalf("QueryVersion = %q: an answer saved before the bounded-input details (v145 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}
