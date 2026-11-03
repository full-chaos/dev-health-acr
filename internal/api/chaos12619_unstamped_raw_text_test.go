package api

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12619: the projection's last tier is built from the answer as the engine or
// the store gave it, so a stored subject label that is its own raw id is restated
// there too: no tier serves a raw work item id.
func TestTheUnstampedProjectionRestatesAStoredRawSubjectLabel(t *testing.T) {
	itemID, _, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", "linear:CHAOS-2320"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	answer := threeRollupProjectStatusResult("result_unstamped_raw_12619")
	item := v1.ContextFabricSubjectRef{Kind: v1.ContextFabricSubjectWorkItem, CanonicalID: itemID, Label: "linear:CHAOS-2320"}
	answer.Drivers[0].AffectedSubjects = []v1.ContextFabricSubjectRef{item}
	projection, encoded, _, ok := unstampedProjection(answer, answerprojection.Budget{}, true)
	if !ok {
		t.Fatalf("the unstamped projection did not serve: %+v", projection)
	}
	if strings.Contains(string(encoded), "linear:CHAOS-2320") || !strings.Contains(string(encoded), `"label":"CHAOS-2320"`) {
		t.Fatalf("the unstamped projection serves a raw work item id or lost the subject:\n%s", encoded)
	}
}
