package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func actionFindingsFor(t *testing.T, admit []string, ownership [][]any, fired [][]any) contextpacket.ActionFindingsResult {
	t.Helper()
	client := &fakeClient{tables: []fakeTable{
		{match: "AS own WHERE repo_key", rows: ownership},
		{match: "WHERE rn = 1 AND fired = 1", rows: fired},
	}}
	authorizer := func(_ context.Context, _ storage.Principal, teams []contextfabric.SubjectRef) (map[string]bool, error) {
		allowed := map[string]bool{}
		for _, team := range teams {
			for _, id := range admit {
				if team.CanonicalID == "team:"+id {
					allowed[team.CanonicalID] = true
				}
			}
		}
		return allowed, nil
	}
	source := devhealthfacts.NewActionFindingsSource(client, authorizer)
	result, err := source.ActionFindings(context.Background(), storage.Principal{OrgID: "org-1"}, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("ActionFindings: %v", err)
	}
	return result
}

func TestActionFindingsServeTheOwningTeamsStoredFinding(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, []string{"CHAOS"}, [][]any{{"CHAOS"}}, [][]any{deficiencyRow("CHAOS")})
	if len(result.Teams) != 1 || result.Teams[0].TeamID != "CHAOS" || len(result.Teams[0].Findings) != 1 || result.Teams[0].Findings[0].RuleID != "compounding-risk" {
		t.Fatalf("served = %#v", result)
	}
}

func TestActionFindingsAreWithheldWhenTheGateAdmitsNoOwningTeam(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, nil, [][]any{{"CHAOS"}}, [][]any{deficiencyRow("CHAOS")})
	if len(result.Teams) != 0 || result.Reason != contextpacket.ActionUnavailableNoGrant {
		t.Fatalf("served = %#v", result)
	}
}

func TestActionFindingsNameNoOwnerWhenNoTeamOwnsTheRepository(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, []string{"CHAOS"}, nil, [][]any{deficiencyRow("CHAOS")})
	if len(result.Teams) != 0 || result.Reason != contextpacket.ActionUnavailableNoOwner {
		t.Fatalf("served = %#v", result)
	}
}
