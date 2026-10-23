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
	if n, err := strconv.Atoi(version); err != nil || n < 167 {
		t.Fatalf("QueryVersion = %q: an answer saved when a not-tracked flow measure served 0 (v166 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// With the anchor binding count switch on, a follow-up counts, discovers and
// captures under the binding's anchor kind, so an answer stored before it
// (v174 and before) must not be reused.
func TestQueryVersionMovedPastTheBoundAnchorKind(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 175 {
		t.Fatalf("QueryVersion = %q: an answer saved before the bound anchor kind (v174 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// With the anchor binding carry switch on, a same-question follow-up carries
// its anchor only from the admitted binding, so an answer stored before it
// (v177 and before) must not be reused.
func TestQueryVersionMovedPastTheBindingCarry(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 178 {
		t.Fatalf("QueryVersion = %q: an answer saved before the binding carry (v177 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// A pending binding carries its anchor into the confirming turn, so an
// answer stored before it (v179 and before) must not be reused.
func TestQueryVersionMovedPastThePendingCarry(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 180 {
		t.Fatalf("QueryVersion = %q: an answer saved before the pending carry (v179 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// With the anchor binding follow-up switch on, a changed-question follow-up
// that names no subject offers the earlier answer's subject first, so an
// answer stored before it (v182 and before) must not be reused.
func TestQueryVersionMovedPastTheFollowUp(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 183 {
		t.Fatalf("QueryVersion = %q: an answer saved before the follow-up (v182 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// A team that owns no repository serves not_applicable with the cause on the
// health, deficiency and investment kinds, so an answer stored before it
// (v187 and before) holds no_data and must not be reused.
func TestQueryVersionMovedPastTheTeamOwnsNoRepository(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 188 {
		t.Fatalf("QueryVersion = %q: an answer saved before a team without repositories was served not_applicable (v187 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// An answer about a committed team the source proved gone at its provider
// carries the removed-team limitation, so an answer stored before it (v251 and
// before) holds none and must not be reused.
func TestQueryVersionMovedPastTheRemovedTeamLimitation(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 252 {
		t.Fatalf("QueryVersion = %q: an answer saved before the removed-team limitation (v251 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}

// The flow fact takes the organization and the scope views of work_item_metrics_daily, so an answer stored under
// v254 and before holds the sum of both views once the ops writer stores them and must not be reused.
func TestQueryVersionMovedPastTheFlowViews(t *testing.T) {
	const prefix = "devhealthfacts.clickhouse.v"
	version, ok := strings.CutPrefix(devhealthfacts.QueryVersion, prefix)
	if !ok {
		t.Fatalf("QueryVersion = %q, want the %q<n> shape", devhealthfacts.QueryVersion, prefix)
	}
	if n, err := strconv.Atoi(version); err != nil || n < 255 {
		t.Fatalf("QueryVersion = %q: an answer saved before the flow views (v254 and before) must not be reused", devhealthfacts.QueryVersion)
	}
}
