package falkorgraph

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// CHAOS-12299 (shared with CHAOS-12229): a walk reads a node as a projection
// map, so a property left out of walkNodeProperties never reaches the
// membership predicate. A walked work item must carry its stated project list,
// and a node without one must read as Unstated.
func TestAWalkedWorkItemCarriesItsStatedProjectMembership(t *testing.T) {
	t.Parallel()
	column := walkProjection("b", walkNodeProperties)
	if want := propAuthzProjectIdentities + ": b." + propAuthzProjectIdentities; !strings.Contains(column, want) {
		t.Fatalf("walk projection %q does not read %q", column, want)
	}
	wanted := []string{"project.v2:github:P-1"}
	stated := walkNode(map[string]interface{}{
		propKind: "work_item", propCanonicalID: "work_item.v2:r:W-1", propLabel: "W-1",
		propAuthzProjects:          "*",
		propAuthzProjectIdentities: []interface{}{"project.v2:github:P-1"},
	})
	if got := graphrank.ProjectMembershipOf(stated.Properties, wanted); got != graphrank.ProjectMembershipMember {
		t.Fatalf("walked work item with a stated list: ProjectMembershipOf = %v, want Member", got)
	}
	// The projection map returns null for a node that never had the property.
	unstated := walkNode(map[string]interface{}{
		propKind: "work_item", propCanonicalID: "work_item.v2:r:W-2", propLabel: "W-2",
		propAuthzProjects:          "*",
		propAuthzProjectIdentities: nil,
	})
	if got := graphrank.ProjectMembershipOf(unstated.Properties, wanted); got != graphrank.ProjectMembershipUnstated {
		t.Fatalf("walked work item without the property: ProjectMembershipOf = %v, want Unstated", got)
	}
}
