package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A caller authorized for the label reads the certified count while a same-label
// claimant has no graph node, and the same caller cannot read that claimant
// through the gate every direct read (read_facts, read_relationships) passes.
func TestAGraphLagClaimantIsNotReadableByTheCallerWhoseCountItNoLongerBlocks(t *testing.T) {
	claimant := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:other/not-projected"}
	for name, principal := range map[string]storage.Principal{
		"unrestricted":         {OrgID: "org-1"},
		"granted to the label": {OrgID: "org-1", RepositoryScopes: []string{routeOwnedSlug}},
	} {
		principal := principal
		t.Run(name, func(t *testing.T) {
			adapter := newFakeAdapter(t, seedOwnedRepository().conn())
			adapter.config.IdentityUniverse = func(context.Context, string) ([]graphrank.IdentityRow, time.Time, bool, error) {
				return []graphrank.IdentityRow{
					{Kind: contextfabric.SubjectRepository, CanonicalID: routeOwnedRepository, Label: routeOwnedSlug},
					{Kind: claimant.Kind, CanonicalID: claimant.CanonicalID, Label: routeOwnedSlug},
				}, time.Time{}, true, nil
			}
			answer := investigateAnchor(t, adapter, principal,
				projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true},
				"how many teams own repository "+routeOwnedSlug, routeOwnershipMessage)
			value, claimed, outcome := servedCount(answer)
			if !claimed || value != 1 || outcome != contractsv1.ContextFabricRequirementSatisfied {
				t.Fatalf("count = %d claimed=%t outcome=%q, want a certified 1 for the authorized caller", value, claimed, outcome)
			}

			gate := directread.NewSubjectGate(adapter, nil)
			ctx := observability.WithRequestID(context.Background(), "req-graph-lag")
			if _, control := gate.Authorize(ctx, principal, []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: routeOwnedRepository}}); control.Decision != directread.DecisionAdmitted {
				t.Fatalf("control: the gate refused the projected repository the caller is authorized for: %q", control.Decision)
			}
			authorized, decision := gate.Authorize(ctx, principal, []contextfabric.SubjectRef{claimant})
			if decision.Decision == directread.DecisionAdmitted || decision.Decision == directread.DecisionPartial || authorized.Len() != 0 {
				t.Fatalf("the gate admitted the claimant with no graph node: decision %q, %d authorized", decision.Decision, authorized.Len())
			}
			if len(decision.Outcomes) != 1 || decision.Outcomes[0].Outcome != directread.SubjectAbsent {
				t.Fatalf("outcomes %+v, want the claimant absent", decision.Outcomes)
			}
		})
	}
}

// With the claimant projected, the same caller reads it and the count is no
// longer certified: the premise the graph-lag admission rests on.
func TestAProjectedSameLabelClaimantIsReadableAndBlocksTheCount(t *testing.T) {
	const claimantID = "repository:github:other/not-projected"
	s := seedOwnedRepository()
	s.nodes = append(s.nodes, seededNode{kind: "repository", id: claimantID, label: routeOwnedSlug, repos: []string{"other/not-projected"}})
	s.text["repository|"+claimantID] = "acme alpha service"
	adapter := newFakeAdapter(t, s.conn())
	adapter.config.IdentityUniverse = func(context.Context, string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		return []graphrank.IdentityRow{
			{Kind: contextfabric.SubjectRepository, CanonicalID: routeOwnedRepository, Label: routeOwnedSlug},
			{Kind: contextfabric.SubjectRepository, CanonicalID: claimantID, Label: routeOwnedSlug},
		}, time.Time{}, true, nil
	}
	principal := storage.Principal{OrgID: "org-1"}
	answer := investigateAnchor(t, adapter, principal,
		projectDeploymentsInterpreter{name: routeOwnedSlug, kind: contextfabric.SubjectRepository, member: contextfabric.SubjectTeam, count: true},
		"how many teams own repository "+routeOwnedSlug, routeOwnershipMessage)
	if _, claimed, outcome := servedCount(answer); claimed || outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("count claimed=%t outcome=%q with a projected same-label repository, want none certified", claimed, outcome)
	}
	gate := directread.NewSubjectGate(adapter, nil)
	_, decision := gate.Authorize(observability.WithRequestID(context.Background(), "req-projected"), principal, []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: claimantID}})
	if decision.Decision != directread.DecisionAdmitted {
		t.Fatalf("gate decision %q for the projected claimant, want admitted", decision.Decision)
	}
}
