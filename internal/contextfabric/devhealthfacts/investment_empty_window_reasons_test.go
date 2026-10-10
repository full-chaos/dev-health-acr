package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// An empty team window names its cause and states that ownership is as synced,
// and a restricted organization subject in the same request does not erase them.
func TestInvestmentEmptyTeamWindowKeepsItsReasonBesideARestrictedOrganization(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(60 * 24 * time.Hour)
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{}), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/repo-a"}}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-none"), organizationSubject("org-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNotApplicable {
		t.Fatalf("facts=%d state=%q, want no fact and not_applicable", len(result.Facts), result.State)
	}
	for _, want := range []string{"repository-bound", "investment_team_no_owned_repository", "investment_ownership_as_synced"} {
		if !strings.Contains(result.Reason, want) {
			t.Errorf("reason = %q, want it to keep %q", result.Reason, want)
		}
	}
}
