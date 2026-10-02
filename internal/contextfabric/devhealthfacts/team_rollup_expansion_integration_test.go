package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A team question served through the engine's registry keeps its per-pull-
// request facts (the team -> pull request expansion) AND gains the team's own
// rollup fact with its owned-repository pointer. Supporting the team subject
// kind directly must not switch the expansion off.
func TestTeamPullRequestsAreServedAsRollupBesideTheExpandedPullRequests(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	now := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, now)
	for _, repo := range []struct{ id, slug string }{{chaos4101RepoAID, chaos4101RepoASlug}, {chaos4101RepoBID, chaos4101RepoBSlug}} {
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			chaos4099OrgID, "github", chaos4101TeamID, repo.id, repo.slug, "exact", "native", uint8(1), uint16(100), int32(0), now.AddDate(-1, 0, 0), nil, now); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{
		ScopeExpander: devhealthfacts.NewScopeExpander(query),
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{chaos4101RepoASlug, chaos4101RepoBSlug}}, contextfabric.CanonicalFactRequest{
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}},
	})
	if err != nil {
		t.Fatalf("ReadFacts error = %v", err)
	}

	var rollup *contextfabric.CanonicalFact
	pullRequests := 0
	for index, fact := range bundle.Facts {
		switch fact.Subject.Kind {
		case contextfabric.SubjectTeam:
			rollup = &bundle.Facts[index]
		case contextfabric.SubjectPullRequest:
			pullRequests++
		}
	}
	if pullRequests != 3 {
		t.Fatalf("per-pull-request facts = %d, want the 3 the expansion reaches (facts: %+v)", pullRequests, bundle.Facts)
	}
	if rollup == nil {
		t.Fatalf("no team rollup fact beside the expanded pull requests: %+v", bundle.Facts)
	}
	wantInt(t, rollup.Fields, "owned_repository_count", 2)
	if table := rollup.Fields["owned_repositories"]; table.Table == nil || len(table.Rows) != 2 {
		t.Fatalf("owned_repositories = %#v, want both owned repositories", table)
	}
	if _, ok := rollup.Fields["pull_requests_opened_window"]; !ok {
		t.Fatalf("rollup carries no window total: %#v", rollup.Fields)
	}
}
