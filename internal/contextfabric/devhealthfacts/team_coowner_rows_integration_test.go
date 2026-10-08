package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const coOwnerTeamID = "COOWNER"

// A work item with a primary row for one team and a co-owner row
// (is_primary = 2) for another is in BOTH teams' cohorts, and the primary
// team's cohort does not change.
func TestScopeExpander_TeamScopeReadsCoOwnerRows(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)
	if err := direct.Exec(ctx, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4099OrgID, chaos4101RepoBID, "wi-b-heuristic", coOwnerTeamID, "project_ownership", uint8(2), "high", at); err != nil {
		t.Fatalf("seed co-owner row: %v", err)
	}
	expander := devhealthfacts.NewScopeExpander(query)
	team := func(id string) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: id}
	}
	expand := func(id string, policy contextfabric.FactScopePolicy, target contextfabric.SubjectKind) contextfabric.FactScopeExpansionResult {
		t.Helper()
		result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
			RequirementKind: contextfabric.FactMetrics,
			Origins:         []contextfabric.SubjectRef{team(id)},
			Policy:          policy,
			TargetKind:      target,
			Limit:           20,
		})
		if err != nil {
			t.Fatalf("ExpandFactScope(%s, %s): %v", id, policy, err)
		}
		return result
	}

	repos := expand(coOwnerTeamID, contextfabric.FactScopePolicyTeamPrimaryAttributionRepository, contextfabric.SubjectRepository)
	if len(repos.Targets) != 1 || repos.Targets[0].Label != chaos4101RepoBSlug {
		t.Fatalf("co-owner team repositories = %+v, want only %s", repos.Targets, chaos4101RepoBSlug)
	}
	items := expand(coOwnerTeamID, contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemWork, contextfabric.SubjectWorkItem)
	if len(items.Targets) != 1 || items.Targets[0].Label != "wi-b-heuristic" {
		t.Fatalf("co-owner team work items = %+v, want only wi-b-heuristic", items.Targets)
	}
	primary := expand(chaos4101TeamID, contextfabric.FactScopePolicyTeamPrimaryAttributionRepository, contextfabric.SubjectRepository)
	if len(primary.Targets) != 2 {
		t.Fatalf("primary team repositories = %+v, want repo A and repo B unchanged", primary.Targets)
	}
	var orgRows uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_item_team_attributions FINAL WHERE org_id = ? AND work_item_id = 'wi-b-heuristic' AND is_primary = 1`, chaos4099OrgID).Scan(&orgRows); err != nil || orgRows != 1 {
		t.Fatalf("primary rows of the co-owned item = %d (%v), want exactly 1", orgRows, err)
	}
}
