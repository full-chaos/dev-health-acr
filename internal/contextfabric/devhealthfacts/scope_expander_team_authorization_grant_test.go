package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// teamWorkItems decides from each call's own grant whether its authorization
// relation is narrowed: one expander serves an organization-wide grant, then
// the same principal with its grant narrowed to a repository, then the
// organization-wide grant again. Only the restricted call narrows; the
// project selection never does.
func TestTeamWorkItemsNarrowTheAuthorizationRelationFromEachCallsGrant(t *testing.T) {
	client := &fakeClient{}
	expander := devhealthfacts.NewScopeExpander(client)
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	orgWide := storage.Principal{OrgID: "org-team-grant", Subject: "s", CredentialID: "c"}
	restricted := orgWide
	restricted.RepositoryScopes = []string{"acme/granted"}

	narrowed := devhealthfacts.TeamWorkItemsAuthorizationJoinForTest(true)
	whole := devhealthfacts.TeamWorkItemsAuthorizationJoinForTest(false)
	selection := func(t *testing.T, principal storage.Principal, policy contextfabric.FactScopePolicy, origin contextfabric.SubjectRef, fragment string) string {
		t.Helper()
		client.queries = nil
		if _, err := expander.ExpandFactScope(context.Background(), contextfabric.FactScopeExpansionRequest{
			Principal: principal, RequirementKind: contextfabric.FactStatus, Origins: []contextfabric.SubjectRef{origin},
			Policy: policy, TargetKind: contextfabric.SubjectWorkItem, TimeContext: current, Limit: 200,
		}); err != nil {
			t.Fatalf("ExpandFactScope: %v", err)
		}
		for _, query := range client.queries {
			if strings.Contains(query.statement, fragment) {
				return query.statement
			}
		}
		t.Fatalf("no statement carrying %q was sent", fragment)
		return ""
	}
	team := func(t *testing.T, principal storage.Principal) string {
		return selection(t, principal, contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus, teamSubject("A"), "min(a.team_id) AS origin_id")
	}

	for _, step := range []struct {
		name      string
		principal storage.Principal
		narrow    bool
	}{
		{"organization-wide grant", orgWide, false},
		{"grant narrowed to a repository", restricted, true},
		{"organization-wide grant again", orgWide, false},
	} {
		statement := team(t, step.principal)
		if got := strings.Contains(statement, narrowed); got != step.narrow {
			t.Errorf("%s: teamWorkItems carries the narrowed relation = %v, want %v", step.name, got, step.narrow)
		}
		if got := strings.Contains(statement, whole); got == step.narrow {
			t.Errorf("%s: teamWorkItems carries the organization-wide relation = %v, want %v", step.name, got, !step.narrow)
		}
	}

	project := selection(t, restricted, contextfabric.FactScopePolicyProjectWorkItemStatus, projectSubject("linear", "P"), "p.id)) AS origin_id")
	if !strings.Contains(project, whole) {
		t.Error("projectWorkItems under a restricted grant does not carry the organization-wide relation")
	}
}
