package factoracle

import (
	"reflect"
	"strings"
	"testing"
)

func TestTheCaptureClassesEveryRawRepoScopeIDThatIsNotAnExtractRepositoryID(t *testing.T) {
	const (
		repo  = "3f1c2b6a-0d4e-4a7b-9c11-2e5f6a7b8c90"
		risk  = "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d"
		team  = "team-alpha"
		other = "11111111-2222-4333-8444-555555555555"
	)
	extract := &Extract{Tables: map[string][]Row{
		tableRepos:                {{"id": repo, "repo": "acme/api"}},
		tableTeams:                {{"id": team}},
		tableCompoundingRiskDaily: {{"scope": "repo", "scope_id": risk}, {"scope": "team", "scope_id": team}},
	}}
	row := func(scope string, id any) any { return map[string]any{"scope": scope, "scopeId": id} }
	answers := []any{
		map[string]any{"compoundingRisk": map[string]any{"rows": []any{
			row("REPO", repo), row("REPO", risk),
			row("REPO", strings.ToUpper(repo)),
			row("REPO", "repository:"+repo), row("REPO", "repo:"+repo),
			row("REPO", "{"+strings.ReplaceAll(repo, "-", "")+"}"),
			row("REPO", team), row("REPO", "acme/api"),
			row("REPO", other),
			row("REPO", 42),
			row("TEAM", other),
		}}},
		map[string]any{"teamRollup": map[string]any{"rows": []any{row("REPO", other)}}},
	}
	got := checkRepoScopeIDs(extract, answers)
	want := ScopeIDCheck{Compared: 10, Matched: 3, Misses: map[string]int{
		scopeIDCase: 1, scopeIDPrefix: 1, scopeIDFormat: 1, scopeIDKind: 2, scopeIDOtherOrg: 1, scopeIDNotString: 1,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("check = %+v, want %+v", got, want)
	}
	summary := got.Summary()
	for _, value := range []string{repo, risk, team, other, "acme"} {
		if strings.Contains(strings.ToLower(summary), strings.ToLower(value)) {
			t.Fatalf("the summary prints a raw value: %q", summary)
		}
	}
	clean := checkRepoScopeIDs(extract, answers[:0])
	if clean.Misses != nil || clean.Compared != 0 {
		t.Fatalf("no answers: %+v", clean)
	}
}

func TestTheReplyScrubGivesARepoScopeIDTheExtractPseudonymOfItsRepository(t *testing.T) {
	s, err := NewScrubber("0d0d0d0d-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	const repo = "3f1c2b6a-0d4e-4a7b-9c11-2e5f6a7b8c90"
	value, err := scrubValue(s, ruleScopeID, repo, Row{"scope": "repo"})
	if err != nil {
		t.Fatal(err)
	}
	extracted, _ := value.(string)
	for _, raw := range []string{"repository:" + repo, repo} {
		got := scrubRiskScopeID(s, "REPO", raw)
		if opsScopeKey(repoScopeKind, got) != extracted {
			t.Fatalf("%s scrubbed to a key that is not the extract pseudonym of its repository", raw)
		}
	}
	if got := scrubRiskScopeID(s, "REPO", "repository:"+repo); !strings.HasPrefix(got, repoScopeKind+":") {
		t.Fatalf("the kind prefix is lost: %s", got)
	}
	if scrubRiskScopeID(s, "TEAM", "team-alpha") != s.Token("team", "team-alpha") {
		t.Fatal("a TEAM scopeId is not scrubbed like a team id")
	}
}
