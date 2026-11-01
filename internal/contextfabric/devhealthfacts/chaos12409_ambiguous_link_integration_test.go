package devhealthfacts_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// work_items is keyed by (org_id, repo_id, work_item_id) and a link row names
// no issue repository, so one link id can join two work items, and nothing
// says which one the link means. The rule of the link producer holds here too:
// such a link contributes to no fact read, and an id with one row stays, in any
// repository. An ambiguous native row still covers its pull request (the
// entity-tree rule), so a lower-tier link of that pull request stays dropped.
//
// Each read below is the served provider or scope expander on real ClickHouse.
// A row cannot say which issue it means, so the swapped shape holds the link's
// meant issue repository-less and the unrelated row in a repository.
func TestAnAmbiguousLinkIdContributesToNoFactRead(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const zero = "00000000-0000-0000-0000-000000000000"
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	window := contextfabric.TimeContext{Axis: contextfabric.TemporalRange}
	start, end := at.Add(-time.Hour), at.Add(time.Hour)
	window.Start, window.End = &start, &end
	type row struct{ repo, project, itemType string }
	for _, pullRequestProvider := range []string{"github", "gitlab"} {
		for _, provider := range []string{"github", "gitlab", "jira", "linear"} {
			for _, shape := range []string{"two-repositories", "repository-less", "swapped", "pull-request-typed"} {
				t.Run(pullRequestProvider+"/"+provider+"/"+shape, func(t *testing.T) {
					org := fmt.Sprintf("org-12409-%s-%s-%s", pullRequestProvider, provider, shape)
					local, foreign := repoUUID(org+"local"), repoUUID(org+"foreign")
					exec := func(what, statement string, args ...any) {
						t.Helper()
						if err := direct.Exec(ctx, statement, args...); err != nil {
							t.Fatalf("seed %s: %v", what, err)
						}
					}
					exec("repository", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/local', ?, ?)`, local, org, pullRequestProvider, at)
					exec("repository", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/foreign', ?, ?)`, foreign, org, pullRequestProvider, at)
					for _, project := range []string{"OURS", "THEIRS", "TIER"} {
						exec("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', ?, 'started', '', 1, ?)`,
							project, org, project, provider, at)
					}
					exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES ('team-ours', 'team-ours', NULL, ?, ?, ?, [], 1)`, at, org, provider)
					exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, ?, 'team-ours', 'OURS', NULL, 'native', ?, NULL, ?)`,
						org, provider, at.Add(-24*time.Hour), at)
					issue := func(id string, rows ...row) {
						t.Helper()
						for _, r := range rows {
							exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, type, url, parent_id, provider, project_id, updated_at, created_at, last_synced) VALUES (?, ?, ?, ?, 'open', ?, '', '', ?, ?, ?, ?, ?)`,
								id, r.repo, org, "Item "+id, r.itemType, provider, r.project, at, at, at)
						}
					}
					link := func(id string, number uint32, tier string) {
						t.Helper()
						exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, 'PR', 'open', ?, ?, NULL, NULL, 'feature', '')`, local, org, number, at, at)
						exec("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, 0.9, ?, '', ?, ?)`,
							local, id, number, tier, at, org)
					}
					// colliding is the two rows of an id, one in the project
					// named, one in another.
					colliding := func(id, project string) {
						t.Helper()
						switch shape {
						case "two-repositories":
							issue(id, row{foreign, "THEIRS", "issue"}, row{local, project, "issue"})
						case "repository-less":
							issue(id, row{foreign, "THEIRS", "issue"}, row{zero, project, "issue"})
						case "swapped":
							issue(id, row{zero, "THEIRS", "issue"}, row{foreign, project, "issue"})
						case "pull-request-typed":
							// A pull-request-typed row counts: it can be what the
							// link names.
							issue(id, row{foreign, "THEIRS", "pr"}, row{zero, project, "issue"})
						default:
							t.Fatalf("unknown shape %q", shape)
						}
					}
					colliding("X-1", "OURS")
					link("X-1", 1, "native")
					// Controls: an id with one row is a true link, and one in
					// another repository is a true link too.
					issue("Y-1", row{local, "OURS", "issue"})
					link("Y-1", 2, "native")
					issue("Q-1", row{foreign, "OURS", "issue"})
					link("Q-1", 5, "native")
					// The tier reads: an ambiguous row beside a one-row lower-tier
					// link. Only a native row covers the pull request.
					colliding("X-2", "TIER")
					link("X-2", 3, "explicit_text")
					issue("Z-1", row{local, "TIER", "issue"})
					link("Z-1", 3, "heuristic")
					colliding("X-3", "TIER")
					link("X-3", 4, "native")
					issue("Z-2", row{local, "TIER", "issue"})
					link("Z-2", 4, "heuristic")
					// The cover keeps its other rules: a link with no work item
					// row covers, a one-row pull-request-typed link does not.
					link("G-1", 6, "explicit_text")
					issue("Z-3", row{local, "TIER", "issue"})
					link("Z-3", 6, "heuristic")
					issue("T-1", row{local, "THEIRS", "pr"})
					link("T-1", 7, "native")
					issue("Z-4", row{local, "TIER", "issue"})
					link("Z-4", 7, "heuristic")
					// The team's issue read keeps its issue rule.
					issue("T-2", row{local, "OURS", "pr"})
					link("T-2", 8, "native")

					principal := storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}
					provide := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
					read := func(timeContext contextfabric.TimeContext, subject contextfabric.SubjectRef) []contextfabric.CanonicalFact {
						t.Helper()
						result, err := provide.ReadFacts(ctx, principal, contextfabric.FactQuery{Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{subject}, Time: timeContext})
						if err != nil {
							t.Fatalf("ReadFacts(%s): %v", subject.CanonicalID, err)
						}
						return result.Facts
					}
					current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
					project := func(id string) contextfabric.SubjectRef {
						return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectCanonicalID(t, provider, id), Label: id}
					}
					pullRequest := func(number uint32) contextfabric.SubjectRef {
						return contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:%s:%d", local, number), Label: "PR"}
					}
					names := map[string]string{}
					for _, id := range []string{"OURS", "THEIRS", "TIER"} {
						names[project(id).CanonicalID] = id
					}
					breakdown := func(number uint32) []string {
						t.Helper()
						facts := read(current, pullRequest(number))
						if len(facts) != 1 {
							t.Fatalf("pull request %d: %d facts, want 1: the read did not happen", number, len(facts))
						}
						out := []string{}
						if table := facts[0].Fields["project_breakdown"].Table; table != nil {
							for _, r := range table.Rows {
								out = append(out, names[*r.Fields["scope_id"].String])
							}
						}
						sort.Strings(out)
						return out
					}

					// project_rollups.go: the project counts the one-row links only.
					if facts := read(current, project("OURS")); len(facts) != 1 {
						t.Errorf("project OURS: %d facts, want 1", len(facts))
					} else if got := integerOf(facts[0].Fields["pull_requests_open_current"]); got != 2 {
						t.Errorf("project OURS counts %d open pull requests, want 2: Y-1 and Q-1, not the ambiguous X-1", got)
					}
					if facts := read(current, project("THEIRS")); len(facts) != 0 {
						t.Errorf("project THEIRS: %d facts, want none: its only link is ambiguous", len(facts))
					}

					// The shared link rows (issueLinkRowsAmongSQL): a pull request's projects.
					if got := fmt.Sprint(breakdown(1)); got != "[]" {
						t.Errorf("pull request 1 states projects %s, want none: its only link is ambiguous", got)
					}
					if got := fmt.Sprint(breakdown(2)); got != "[OURS]" {
						t.Errorf("pull request 2 states projects %s, want [OURS]", got)
					}

					// The team reach (teamProjectLinkRowsSQL) is the same chain.
					if facts := read(window, teamSubject("team-ours")); len(facts) != 1 {
						t.Errorf("team: %d facts, want 1", len(facts))
					} else if got := integerOf(facts[0].Fields["pull_requests_opened_window"]); got != 2 {
						t.Errorf("team counts %d linked pull requests, want 2: Y-1 and Q-1, not the ambiguous X-1", got)
					}

					// strongestLinkTierOfPullRequestsSQL: an ambiguous lower-tier
					// row covers nothing, an ambiguous native row covers its pull
					// request.
					root := project("TIER")
					expanded := expandPullRequests(t, ctx, query, org, 20, root)
					if tier, _ := tierOf(expanded, local, 3, root); tier != "heuristic" {
						t.Errorf("pull request 3 tier = %q, want heuristic: the ambiguous explicit_text row gives no link and covers nothing", tier)
					}
					if tier, n := tierOf(expanded, local, 4, root); tier != "" || n != 0 {
						t.Errorf("pull request 4 tier = %q (%d), want none: the ambiguous native row covers the pull request and gives no link", tier, n)
					}
					if tier, n := tierOf(expanded, local, 6, root); tier != "" || n != 0 {
						t.Errorf("pull request 6 tier = %q (%d), want none: a link with no work item row covers it", tier, n)
					}
					if tier, _ := tierOf(expanded, local, 7, root); tier != "heuristic" {
						t.Errorf("pull request 7 tier = %q, want heuristic: a pull-request-typed work item covers nothing", tier)
					}
				})
			}
		}
	}
}

// integerOf is an integer fact value, -1 when it is absent.
func integerOf(value contextfabric.FactValue) int64 {
	if value.Integer == nil {
		return -1
	}
	return *value.Integer
}
