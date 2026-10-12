package devhealthsource_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestTheAnchorOwnershipSourceIsTheOneSourceThatProjectsOwnership: the
// binding's freshness reads one source's watermark, so that source must be
// the one that projects the ownership and membership edges, and no other
// source may project either kind.
func TestTheAnchorOwnershipSourceIsTheOneSourceThatProjectsOwnership(t *testing.T) {
	if contextfabric.AnchorOwnershipSource != devhealthsource.TeamsProjectsSourceName {
		t.Fatalf("AnchorOwnershipSource = %q, want the teams-projects source %q", contextfabric.AnchorOwnershipSource, devhealthsource.TeamsProjectsSourceName)
	}
	ownership := map[contractsv1.ContextFabricRelationshipType]bool{
		contractsv1.ContextFabricRelationshipOwnedByTeam:      true,
		contractsv1.ContextFabricRelationshipBelongsToProject: true,
	}
	for _, kind := range devhealthsource.TeamsProjectsRelationshipTypes() {
		if !ownership[kind] {
			t.Errorf("the ownership source projects %q, which is not an ownership edge; its watermark would move for it", kind)
		}
	}
	for _, kind := range devhealthsource.ProducedRelationshipTypes() {
		if ownership[kind] {
			t.Errorf("the ClickHouse source projects the ownership edge %q, which the ownership watermark would not see move", kind)
		}
	}
}
