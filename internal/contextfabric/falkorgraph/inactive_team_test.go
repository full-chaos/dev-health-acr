package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func inactiveTestTeamRow(id string, active bool, repos ...string) row {
	extra := map[string]interface{}{propPropertyPrefix + "is_active": active, propAuthzRepos: repos}
	return lookupRow("org-1", "team", id, "Platform", extra)
}

func inactiveTeamsAdapterRead(t *testing.T, principal storage.Principal, stored []row, named []row, ids ...string) []directread.InactiveTeam {
	t.Helper()
	fake := &fakeConn{queryFunc: func(_ context.Context, _, _ string, params map[string]interface{}, _ bool) ([]row, error) {
		if _, byKey := params["targets"]; byKey {
			return stored, nil
		}
		return named, nil
	}}
	subjects := make([]contextfabric.SubjectRef, 0, len(ids))
	for _, id := range ids {
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: id})
	}
	got, err := newFakeAdapter(t, fake).InactiveTeams(context.Background(), principal, lookupBinding, subjects)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestInactiveTeamsNamesTheOneActiveTwinTheCallerMayRead(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/api"}}
	old := inactiveTestTeamRow("team:platform", false, "acme/api")
	for _, tc := range []struct {
		name   string
		stored []row
		named  []row
		who    storage.Principal
		want   directread.InactiveTeam
	}{
		{"one readable active twin", []row{old}, []row{inactiveTestTeamRow("team:jira:platform", true, "acme/api")}, principal, directread.InactiveTeam{Inactive: true, ActiveTwinID: "team:jira:platform"}},
		{"two active twins are ambiguous", []row{old}, []row{inactiveTestTeamRow("team:jira:platform", true, "acme/api"), inactiveTestTeamRow("team:linear:platform", true, "acme/api")}, principal, directread.InactiveTeam{Inactive: true}},
		{"a readable twin beside an unreadable one is ambiguous", []row{old}, []row{inactiveTestTeamRow("team:jira:platform", true, "acme/api"), inactiveTestTeamRow("team:linear:platform", true, "acme/other")}, principal, directread.InactiveTeam{Inactive: true}},
		{"twin the caller may not read is not named", []row{old}, []row{inactiveTestTeamRow("team:jira:platform", true, "acme/other")}, principal, directread.InactiveTeam{Inactive: true}},
		{"a name query row that is inactive is no twin", []row{old}, []row{inactiveTestTeamRow("team:other", false, "acme/api")}, principal, directread.InactiveTeam{Inactive: true}},
		{"a row of another name is no twin", []row{old}, []row{lookupRow("org-1", "team", "team:jira:other", "Other", map[string]interface{}{propPropertyPrefix + "is_active": true})}, principal, directread.InactiveTeam{Inactive: true}},
		{"a row of another organization is no twin", []row{old}, []row{lookupRow("org-2", "team", "team:jira:platform", "Platform", map[string]interface{}{propPropertyPrefix + "is_active": true})}, principal, directread.InactiveTeam{Inactive: true}},
		{"a row of another kind is no twin", []row{old}, []row{lookupRow("org-1", "project", "project:platform", "Platform", nil)}, principal, directread.InactiveTeam{Inactive: true}},
		{"inactive team the caller may not read", []row{inactiveTestTeamRow("team:platform", false, "acme/other")}, nil, principal, directread.InactiveTeam{}},
		{"an active team is not inactive", []row{inactiveTestTeamRow("team:platform", true, "acme/api")}, nil, principal, directread.InactiveTeam{}},
		{"no node", nil, nil, principal, directread.InactiveTeam{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inactiveTeamsAdapterRead(t, tc.who, tc.stored, tc.named, "team:platform")
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
