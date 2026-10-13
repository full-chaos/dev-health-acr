package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// read_facts pull_requests for several teams in tables-only mode, through the
// production providers and the real scope expander on ClickHouse: every
// reached pull request that serves no field is left out and counted in one
// request note; a reached pull request that holds a project_breakdown table is
// still served.
func TestDirectReadPullRequestsForSeveralTeamsCountsFactsServingNoField(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "projects") {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC().Truncate(time.Second)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const perTeam = 5
	teams := []string{"PRROWS-A", "PRROWS-B", "PRROWS-C"}
	var repos []string
	for index, team := range teams {
		repo := repoUUID("pr-rows-" + team)
		repos = append(repos, repo)
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo, orgID, "acme/"+strings.ToLower(team), "github", at)
		item := "WI-" + team
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item, repo, orgID, "linear", "issue "+item, "open", "", "", "", at)
		exec(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, repo, item, team, "native_team", uint8(1), "high", at)
		for number := 1; number <= perTeam; number++ {
			exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
				repo, orgID, uint32(number+index*100), fmt.Sprintf("pr %d", number), "open", at, at, "feat/x", "")
		}
	}
	const project = "prrows-project"
	exec(`INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		project, orgID, "PR rows", project, "jira", "", "", uint8(1), at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at, provider) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-LINKED", repos[0], orgID, "linked", "open", "", "", project, at, "jira")
	seedIssueLink(t, ctx, direct, orgID, "WI-LINKED", repos[0], 900, "native", at)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{ScopeExpander: devhealthfacts.NewScopeExpander(query)})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(registry.DirectReadFactSource()), nil)
	var subjects []directread.RequestSubject
	for _, team := range teams {
		subjects = append(subjects, directread.RequestSubject{Kind: "team", CanonicalID: teamSubject(team).CanonicalID})
	}
	response, err := reader.Read(observability.WithRequestID(ctx, "req_0000000000000000000000000000f00d"),
		storage.Principal{OrgID: orgID, Subject: "pr-rows", CredentialID: "pr-rows"},
		directread.FactsRequest{Kinds: []string{"pull_requests"}, Tables: directread.TablesOnly, Subjects: subjects})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	for _, fact := range response.Facts {
		if fact.Subject.Kind == "pull_request" && len(fact.Fields) == 0 && len(fact.Tables) == 0 && len(fact.Withheld) == 0 {
			t.Fatalf("an empty pull request fact was served: %+v", fact)
		}
	}
	linkedServed := false
	for _, fact := range response.Facts {
		if fact.Subject.Kind == "pull_request" && strings.HasSuffix(fact.Subject.CanonicalID, ":900") && len(fact.Tables["project_breakdown"].Rows) == 1 {
			linkedServed = true
		}
	}
	if !linkedServed {
		t.Fatalf("the linked pull request with a project_breakdown table is not served: %+v", response.Facts)
	}
	wantCount := len(teams) * perTeam
	var note string
	for _, candidate := range response.RequestNotes {
		if candidate.Kind == "pull_requests" && strings.Contains(candidate.Note, "not listed") {
			note = candidate.Note
		}
	}
	if !strings.Contains(note, fmt.Sprintf("%d reached pull_requests facts are not listed", wantCount)) {
		t.Fatalf("counted note missing, want %d: %q (notes %+v)", wantCount, note, response.RequestNotes)
	}
}
