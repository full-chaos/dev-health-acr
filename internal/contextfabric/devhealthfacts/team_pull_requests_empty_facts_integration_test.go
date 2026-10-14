package devhealthfacts_test

import (
	"context"
	"fmt"
	"strconv"
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

// read_facts pull_requests for several teams, through the production providers
// and the real scope expander on ClickHouse: one fact per team carrying the
// pull_requests_by_state table, and no fact per reached pull request.
func TestDirectReadPullRequestsForSeveralTeamsServesCountsByStateNotOneFactPerPullRequest(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "projects", "team_repo_ownership") {
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
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", team, repo, "acme/"+strings.ToLower(team), "exact", "native", uint8(1), uint16(1), int32(1), at.Add(-time.Hour), nil, at)
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
		directread.FactsRequest{Kinds: []string{"pull_requests"}, Subjects: subjects})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	// A default team read serves the team facts alone: no fact per pull
	// request, the counts by state in one table on each team fact.
	teamsServed := 0
	for _, fact := range response.Facts {
		if fact.Subject.Kind == "pull_request" {
			t.Fatalf("a per pull request fact was served on a team read: %+v", fact)
		}
		if fact.Subject.Kind != "team" {
			continue
		}
		teamsServed++
		if basis, _ := fact.Fields["pull_requests_by_state_basis"].(string); !strings.Contains(basis, "find_subjects") {
			t.Fatalf("the table does not say how to reach the pull requests: %+v", fact.Fields)
		}
		if len(fact.Provenance.NaturalKeys) == 0 {
			t.Fatalf("the team fact cites no source entity a caller can open: %+v", fact.Provenance)
		}
		table := fact.Tables["pull_requests_by_state"]
		column := map[string]int{}
		for index, name := range table.Columns {
			column[name] = index
		}
		counts := map[string]int64{}
		for _, row := range table.Rows {
			if row[column["state"]] != "open" {
				t.Fatalf("unexpected state in %+v", table.Rows)
			}
			n, convErr := strconv.ParseInt(fmt.Sprint(row[column["pull_request_count"]]), 10, 64)
			if convErr != nil {
				t.Fatalf("count cell is not an integer: %+v", row)
			}
			counts["open"] += n
		}
		want := int64(perTeam)
		if fact.Subject.CanonicalID == teamSubject(teams[0]).CanonicalID {
			want++ // the linked pull request sits in this team's own repository
		}
		if counts["open"] != want {
			t.Fatalf("%s open pull requests = %d, want %d (table %+v)", fact.Subject.CanonicalID, counts["open"], want, table.Rows)
		}
	}
	if teamsServed != len(teams) {
		t.Fatalf("served %d team facts, want %d: %+v", teamsServed, len(teams), response.Facts)
	}
}

// A team that owns a repository holding pull requests outside the window is
// served a measured zero for the window counts beside its counts by state; a
// team whose owned repository holds no pull request row at all is served
// without data, with no by-state table (absent, never zero rows).
func TestTeamPullRequestsByStateSeparatesMeasuredZeroFromNoData(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("team_repo_ownership") {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC().Truncate(time.Second)
	old := at.AddDate(-3, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const oldTeam, emptyTeam = "ZERO-OLD", "ZERO-NONE"
	for _, team := range []string{oldTeam, emptyTeam} {
		repo := repoUUID("zero-" + team)
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo, orgID, "acme/"+strings.ToLower(team), "github", at)
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", team, repo, "acme/"+strings.ToLower(team), "exact", "native", uint8(1), uint16(1), int32(1), at.Add(-time.Hour), nil, at)
	}
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID("zero-"+oldTeam), orgID, uint32(1), "old", "merged", at, old, old.Add(time.Hour), old.Add(time.Hour), "feat/x", "")

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{ScopeExpander: devhealthfacts.NewScopeExpander(query)})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.CanonicalFactRequest{
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     []contextfabric.SubjectRef{teamSubject(oldTeam), teamSubject(emptyTeam)},
		Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	facts := map[string]contextfabric.CanonicalFact{}
	for _, fact := range bundle.Facts {
		facts[fact.Subject.CanonicalID] = fact
	}
	oldFact, ok := facts[teamSubject(oldTeam).CanonicalID]
	if !ok {
		t.Fatalf("the team with pull requests outside the window has no fact: %+v", bundle.Facts)
	}
	if v := oldFact.Fields["pull_requests_opened_window"]; v.Integer == nil || *v.Integer != 0 {
		t.Fatalf("window count is not a measured zero: %+v", v)
	}
	table := oldFact.Fields["pull_requests_by_state"]
	if table.Table == nil || len(table.Rows) != 1 || table.Rows[0].Fields["state"].String == nil || *table.Rows[0].Fields["state"].String != "merged" {
		t.Fatalf("counts by state = %+v, want one merged row", table)
	}
	if emptyFact, served := facts[teamSubject(emptyTeam).CanonicalID]; served {
		if _, has := emptyFact.Fields["pull_requests_by_state"]; has {
			t.Fatalf("a team with no pull request row has a by-state table: %+v", emptyFact.Fields)
		}
		if v := emptyFact.Fields["repositories_without_data_count"]; v.Integer == nil || *v.Integer != 1 {
			t.Fatalf("no-data repository not counted as without data: %+v", emptyFact.Fields)
		}
	}
}

// The counts of a repository a team reaches only through an owned project
// cover the linked pull requests alone, and a historical read leaves out the
// pull requests created after the requested instant.
func TestTeamPullRequestStatesProjectReachedCountLinkedPullRequestsAsOfTheInstant(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("teams", "work_items", "work_graph_issue_pr", "projects", "team_project_ownership", "team_repo_ownership") {
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
	const team, project, item = "REACH-TEAM", "reach-project", "WI-REACH"
	reached := repoUUID("reach-only")
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, reached, orgID, "acme/reach-only", "github", at)
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`, team, team, at, orgID, "linear", uint8(1))
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, project, orgID, "linear", nil, project, uint8(1), "active", "", at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", team, project, nil, "native", at.Add(-48*time.Hour), nil, at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
		item, reached, orgID, "linked", at, project)
	for number := uint32(1); number <= 2; number++ {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			reached, orgID, number, "pr", "open", at, at, "feat/x", "")
	}
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		reached, item, uint32(1), float32(1), "native", "", at, orgID)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{ScopeExpander: devhealthfacts.NewScopeExpander(query)})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	read := func(timeContext contextfabric.TimeContext) contextfabric.CanonicalFact {
		t.Helper()
		bundle, readErr := registry.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.CanonicalFactRequest{
			Question:     contextfabric.InterpretedQuestion{TimeContext: timeContext},
			Subjects:     []contextfabric.SubjectRef{teamSubject(team)},
			Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}},
		})
		if readErr != nil {
			t.Fatalf("ReadFacts: %v", readErr)
		}
		for _, fact := range bundle.Facts {
			if fact.Kind == contextfabric.FactPullRequests && fact.Subject.Kind == contextfabric.SubjectTeam {
				return fact
			}
		}
		t.Fatalf("no team fact: %+v", bundle.Facts)
		return contextfabric.CanonicalFact{}
	}
	current := read(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	table := current.Fields["pull_requests_by_state"]
	if table.Table == nil || len(table.Rows) != 1 || table.Rows[0].Fields["pull_request_count"].Integer == nil || *table.Rows[0].Fields["pull_request_count"].Integer != 1 {
		t.Fatalf("project-reached counts = %+v, want one open row of 1 (the linked pull request only)", table)
	}
	before := at.AddDate(0, 0, -30)
	past := read(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &before})
	if v, has := past.Fields["pull_requests_by_state"]; has && v.Table != nil && len(v.Rows) > 0 {
		t.Fatalf("a pull request created after the requested instant is counted: %+v", v)
	}
}
