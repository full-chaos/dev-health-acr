package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestIsReservedIdentityProjectID pins step 5 (CHAOS-3884, team-lead
// amendment 2026-08-17, adjustment 4): a project claimant whose canonical
// id falls in the reserved organization-scope namespace is filtered, a
// project claimant with an ordinary id is not, and NEITHER a repository nor
// a team claimant is ever filtered by this check regardless of id shape --
// see isReservedIdentityProjectID's own doc comment for why the collision
// this guards against is structurally impossible for those two kinds.
func TestIsReservedIdentityProjectID(t *testing.T) {
	reservedID := contractsv1.ContextFabricReservedOrganizationScopePrefix + "org_1"

	cases := []struct {
		name string
		row  graphrank.IdentityRow
		want bool
	}{
		{
			name: "reserved-namespace project id is filtered",
			row:  graphrank.IdentityRow{Kind: contextfabric.SubjectProject, CanonicalID: "project:" + reservedID},
			want: true,
		},
		{
			name: "ordinary project id is not filtered",
			row:  graphrank.IdentityRow{Kind: contextfabric.SubjectProject, CanonicalID: "project:ask-dev"},
			want: false,
		},
		{
			name: "a repository id shaped like the reserved namespace is never filtered",
			row:  graphrank.IdentityRow{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + reservedID},
			want: false,
		},
		{
			name: "a team id shaped like the reserved namespace is never filtered",
			row:  graphrank.IdentityRow{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + reservedID},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReservedIdentityProjectID(tc.row); got != tc.want {
				t.Errorf("isReservedIdentityProjectID(%+v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}
