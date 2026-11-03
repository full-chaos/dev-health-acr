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
// and an organization subject in the same request does not erase them. Until
// CHAOS-12585 the organization here was refused to the repository-bound caller
// with its own reason; now it is read, and its empty window serves no fact.
func TestInvestmentEmptyTeamWindowKeepsItsReasonBesideAnOrganization(t *testing.T) {
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
	if len(result.Facts) != 0 || result.State == contextfabric.SourceNotApplicable {
		t.Fatalf("facts=%d state=%q, want no fact and no kind-wide not_applicable: the team was read beside the organization", len(result.Facts), result.State)
	}
	served := contextfabric.CombineRequestNote(result.Reason, result.RequestNote)
	for _, want := range []string{"investment_team_no_owned_repository", "investment_ownership_as_synced"} {
		if !strings.Contains(served, want) {
			t.Errorf("reason = %q, want it to keep %q", served, want)
		}
	}
	if strings.Contains(served, "repository-bound") {
		t.Errorf("reason = %q: the organization is no longer refused to this caller", served)
	}
	if got := result.SubjectReasons[contextfabric.FactSubjectKey(organizationSubject("org-1"))]; strings.Contains(got, "owns no repository") {
		t.Errorf("organization own reason = %q", got)
	}
}

// CHAOS-12585: this pinned not_applicable for a repository-bound caller's empty
// organization window. Every caller now reads the organization, so that caller's
// empty window reads exactly as the full caller's does.
func TestAnEmptyOrganizationWindowReadsTheSameForARepositoryBoundCaller(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(60 * 24 * time.Hour)
	read := func(principal storage.Principal) contextfabric.FactProviderResult {
		t.Helper()
		provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{}), contextfabric.FactInvestment)
		result, err := provider.ReadFacts(context.Background(), principal, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{organizationSubject("org-1")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}
	got, want := read(storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/repo-a"}}), read(storage.Principal{OrgID: "org-1"})
	if len(got.Facts) != 0 || got.State != want.State || got.Reason != want.Reason || strings.Contains(got.Reason, "repository-bound") {
		t.Fatalf("facts=%d state=%q reason=%q, want the full caller's state=%q reason=%q", len(got.Facts), got.State, got.Reason, want.State, want.Reason)
	}
}
