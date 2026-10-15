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

func TestActionFindingsCountTheOwningTeamsTheGateWithheld(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, []string{"CHAOS"}, [][]any{{"CHAOS"}, {"OTHER"}}, [][]any{deficiencyRow("CHAOS")})
	if result.WithheldTeams != 1 || len(result.Teams) != 1 || result.Teams[0].TeamID != "CHAOS" {
		t.Fatalf("served = %#v", result)
	}
}

func actionFindingsWithCoverage(t *testing.T, admit []string, ownership, fired, coverage [][]any) contextpacket.ActionFindingsResult {
	t.Helper()
	client := &fakeClient{tables: []fakeTable{
		{match: "AS own WHERE repo_key", rows: ownership},
		{match: "WHERE rn = 1 AND fired = 1", rows: fired},
		{match: "GROUP BY team_id, mx", rows: coverage},
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
	result, err := devhealthfacts.NewActionFindingsSource(client, authorizer).ActionFindings(context.Background(), storage.Principal{OrgID: "org-1"}, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("ActionFindings: %v", err)
	}
	return result
}

func TestActionFindingsCountAFiredOwningTeamAsEvaluated(t *testing.T) {
	t.Parallel()
	result := actionFindingsFor(t, []string{"CHAOS"}, [][]any{{"CHAOS"}}, [][]any{deficiencyRow("CHAOS")})
	if result.NotEvaluatedTeams != 0 || result.Reason != "" || len(result.Teams) != 1 {
		t.Fatalf("a fired owning team is evaluated: not_evaluated=%d reason=%q teams=%d", result.NotEvaluatedTeams, result.Reason, len(result.Teams))
	}
}

func TestActionFindingsCountOnlyTheOwningTeamsNeverEvaluated(t *testing.T) {
	t.Parallel()
	measuredZero := [][]any{{"CLEAR", "2026-10-14", uint32(2), uint8(1), uint8(1)}}
	cases := []struct {
		name         string
		owners       [][]any
		fired        [][]any
		notEvaluated int
		teams        int
		reason       string
	}{
		{"fired_and_measured_zero", [][]any{{"CHAOS"}, {"CLEAR"}}, [][]any{deficiencyRow("CHAOS")}, 0, 1, ""},
		{"fired_and_never_evaluated", [][]any{{"CHAOS"}, {"GHOST"}}, [][]any{deficiencyRow("CHAOS")}, 1, 1, ""},
		{"fired_measured_zero_and_never_evaluated", [][]any{{"CHAOS"}, {"CLEAR"}, {"GHOST"}}, [][]any{deficiencyRow("CHAOS")}, 1, 1, ""},
		{"never_evaluated_only", [][]any{{"GHOST"}}, nil, 1, 0, contextpacket.ActionUnavailableNotEvaluated},
		{"measured_zero_only", [][]any{{"CLEAR"}}, nil, 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := actionFindingsWithCoverage(t, []string{"CHAOS", "CLEAR", "GHOST"}, tc.owners, tc.fired, measuredZero)
			if result.NotEvaluatedTeams != tc.notEvaluated || len(result.Teams) != tc.teams || result.Reason != tc.reason {
				t.Fatalf("not_evaluated=%d teams=%d reason=%q, want %d %d %q", result.NotEvaluatedTeams, len(result.Teams), result.Reason, tc.notEvaluated, tc.teams, tc.reason)
			}
		})
	}
}
