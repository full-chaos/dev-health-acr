package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func actionFindingsFor(t *testing.T, scopes []string, ownership [][]any, fired [][]any) contextpacket.ActionFindingsResult {
	t.Helper()
	client := &fakeClient{tables: []fakeTable{
		{match: "AS mine WHERE repo_key", rows: ownership},
		{match: "WHERE rn = 1 AND fired = 1", rows: fired},
	}}
	source := devhealthfacts.NewActionFindingsSource(client)
	result, err := source.ActionFindings(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: scopes}, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("ActionFindings: %v", err)
	}
	return result
}

func TestActionFindingsServeTheOwningTeamsStoredFinding(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, []string{"org/widget"}, [][]any{{"CHAOS", "org/widget"}}, [][]any{deficiencyRow("CHAOS")})
	if len(result.Teams) != 1 || result.Teams[0].TeamID != "CHAOS" || len(result.Teams[0].Findings) != 1 || result.Teams[0].Findings[0].RuleID != "compounding-risk" {
		t.Fatalf("served = %#v", result)
	}
}

func TestActionFindingsAreWithheldWhenTheCallerCannotReadEveryOwnedRepository(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, []string{"org/widget"}, [][]any{{"CHAOS", "org/widget"}, {"CHAOS", "org/secret"}}, [][]any{deficiencyRow("CHAOS")})
	if len(result.Teams) != 0 || result.Reason != contextpacket.ActionUnavailableNoGrant {
		t.Fatalf("served = %#v", result)
	}
}

func TestActionFindingsNameNoOwnerWhenNoTeamOwnsTheRepository(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, []string{"org/widget"}, nil, [][]any{deficiencyRow("CHAOS")})
	if len(result.Teams) != 0 || result.Reason != contextpacket.ActionUnavailableNoOwner {
		t.Fatalf("served = %#v", result)
	}
}
