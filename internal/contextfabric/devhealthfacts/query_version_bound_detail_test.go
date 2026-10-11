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

// A degraded answer for a failed model call is now recognised by its coverage
// detail, so an answer stored under an earlier version, which carries only the
// warning, must not be reused.
func TestQueryVersionMovedPastTheModelFailureDetail(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 151 {
		t.Fatalf("QueryVersion = %q: an answer saved before the model-failure detail (v150 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// A transient model call end is served as a degraded answer under its own
// failure class, so an answer stored before it (v161 and before) must not be
// reused.
func TestQueryVersionMovedPastTheTransientModelCallDegrade(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 162 {
		t.Fatalf("QueryVersion = %q: an answer saved before the transient-call degrade (v161 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// A flow bug_completed_ratio or story_points_completed the source does not
// track is omitted and named, where v162 and before served it as 0, so an
// answer stored before it must not be reused.
func TestQueryVersionMovedPastTheFlowNotTrackedMeasure(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 166 {
		t.Fatalf("QueryVersion = %q: an answer saved when a not-tracked flow measure served 0 (v165 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}
