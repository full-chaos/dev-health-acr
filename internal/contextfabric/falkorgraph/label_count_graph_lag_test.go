package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestACountOverALabelIsCertifiedWhenTheOnlyOtherClaimantIsMissingFromTheGraph(t *testing.T) {
	s := seedOwnedRepository()
	adapter := newFakeAdapter(t, s.conn())
	adapter.config.IdentityUniverse = func(context.Context, string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return []graphrank.IdentityRow{
			{Kind: contextfabric.SubjectRepository, CanonicalID: routeOwnedRepository, Label: routeOwnedSlug},
			{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:other/not-projected", Label: routeOwnedSlug},
		}, time.Time{}, true, nil
	}
	answer := investigateAnchor(t, adapter, storage.Principal{OrgID: "org-1"},
		projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true},
		"how many teams own repository "+routeOwnedSlug, routeOwnershipMessage)
	if len(answer.committed) != 1 {
		t.Fatalf("committed %+v, want the repository", answer.committed)
	}
	value, claimed, outcome := servedCount(answer)
	if !claimed || value != 1 || outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count = %d claimed=%t outcome=%q, want a certified 1", value, claimed, outcome)
	}
}
