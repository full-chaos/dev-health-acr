package devhealthfacts_test

// owning_teams on a repository identity fact, against a real ClickHouse
// ownership table: the oracle is the seeded team_repo_ownership rows.

import (
	"context"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestRepositoryIdentityFactCarriesItsCurrentOwningTeams(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-owning-teams")
	for _, team := range []struct {
		id, name string
		active   uint8
	}{{"team-a", "Alpha", 1}, {"team-b", "Beta", 1}, {"team-off", "Dormant", 0}, {"team-old", "Former", 1}} {
		f.exec("team "+team.id, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?,?,?,?,?,?,?,?)`,
			team.id, team.name, "", f.at, f.orgID, "github", team.id, team.active)
	}
	for _, label := range []string{"shared", "solo", "unowned", "byname"} {
		f.exec("repo "+label, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), f.orgID, "acme/"+label, "github", f.at)
	}
	own := func(teamID, fullName string, repoID any, validTo any) {
		f.t.Helper()
		f.exec("ownership "+teamID, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.orgID, "github", teamID, repoID, fullName, "exact", "native", uint8(1), uint16(100), int32(0), f.at, validTo, f.at)
	}
	own("team-a", "acme/shared", repoUUID("shared"), nil)
	own("team-b", "acme/shared", repoUUID("shared"), nil)
	own("team-a", "acme/solo", repoUUID("solo"), nil)
	own("team-off", "acme/solo", repoUUID("solo"), nil)
	own("team-old", "acme/solo", repoUUID("solo"), f.at.Add(1))
	own("team-b", "ACME/ByName", nil, nil)

	provider := findProvider(t, f.providers, contextfabric.FactIdentity)
	subjects := make([]contextfabric.SubjectRef, 0, 4)
	for _, label := range []string{"shared", "solo", "unowned", "byname"} {
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label})
	}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: f.orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactIdentity, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	owners := map[string][]string{}
	for _, fact := range result.Facts {
		if value, ok := fact.Fields["owning_teams"]; ok && value.Table != nil {
			for _, row := range value.Table.Rows {
				owners[fact.Subject.Label] = append(owners[fact.Subject.Label], *row.Fields["team_id"].String)
			}
			sort.Strings(owners[fact.Subject.Label])
		}
	}
	want := map[string][]string{"shared": {"team-a", "team-b"}, "solo": {"team-a"}, "byname": {"team-b"}}
	if len(owners) != len(want) {
		t.Fatalf("owners = %v, want %v (unowned carries none; inactive and ended owners are excluded)", owners, want)
	}
	for label, teams := range want {
		got := owners[label]
		if len(got) != len(teams) || (len(got) > 0 && got[0] != teams[0]) || (len(got) > 1 && got[1] != teams[1]) {
			t.Fatalf("%s owners = %v, want %v", label, got, teams)
		}
	}
}
