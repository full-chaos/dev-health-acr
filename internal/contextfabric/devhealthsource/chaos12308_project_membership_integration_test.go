package devhealthsource_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestLivePullRequestMembershipComesFromItsLinkedIssues is CHAOS-12308 against
// a real ClickHouse with the ops presence view, over the provider matrix: pull
// requests of github and gitlab, issues of github, gitlab, jira and linear.
//
//   - the set is the union over actual link rows, with the weakest tier;
//   - a pull request with no link, and one whose issue is in no project, are
//     unstated;
//   - a row a native link covers contributes nothing;
//   - a link row added with no pull request sync is picked up by the refresh;
//   - an issue's project column change, and an issue's transition, with no
//     pull request sync are picked up too.
func TestLivePullRequestMembershipComesFromItsLinkedIssues(t *testing.T) {
	ctx := context.Background()
	t1 := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	t2, t3, t4 := t1.Add(time.Hour), t1.Add(2*time.Hour), t1.Add(3*time.Hour)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	const org = "org-12308"
	const github, gitlab = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	mustExec(t, ctx, direct, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/hub', 'github', ?)`, github, org, t1)
	mustExec(t, ctx, direct, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/lab', 'gitlab', ?)`, gitlab, org, t1)
	project := func(id, provider string) {
		mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, '', ?, 'started', '', 1, ?)`,
			id, org, "Project "+id, provider, t1)
	}
	// Production shapes (the presence view): a github project id carries the
	// ghprojv2: prefix, and gitlab membership comes from transitions only.
	project("ghprojv2:GH", "github")
	project("ghprojv2:G2", "github")
	project("GL", "gitlab")
	project("GM", "gitlab")
	project("JI", "jira")
	project("LI", "linear")
	workItem := func(id, repo, provider, projectID string, at time.Time) {
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, created_at, last_synced) VALUES (?, ?, ?, ?, 'open', '', '', ?, ?, ?, ?, ?)`,
			id, repo, org, "Item "+id, provider, projectID, at, t1, at)
	}
	workItem("I-GH", github, "github", "ghprojv2:GH", t1)
	workItem("I-GL", gitlab, "gitlab", "", t1)
	transition := func(from, to, event string, at time.Time) {
		mustExec(t, ctx, direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', 'I-GL', 'gitlab', ?, ?, '', '', '', ?, ?, ?, ?)`,
			org, gitlab, from, to, at, at, event, at)
	}
	transition("", "GL", "ev-12308-1", t1)
	workItem("I-JI", zeroRepositoryUUID, "jira", "JI", t1)
	workItem("I-LI", zeroRepositoryUUID, "linear", "LI", t1)
	workItem("I-LX", zeroRepositoryUUID, "linear", "LI", t1)
	workItem("I-JX", zeroRepositoryUUID, "jira", "JI", t1)
	workItem("I-NONE", zeroRepositoryUUID, "linear", "", t1)
	pull := func(repo string, number uint32) {
		mustExec(t, ctx, direct, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, 'open', ?)`,
			repo, org, number, fmt.Sprintf("PR %d", number), t1)
	}
	link := func(repo, issue string, number uint32, tier string, at time.Time) {
		mustExec(t, ctx, direct, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (toUUID(?), ?, ?, 0.9, ?, '', ?, ?)`,
			repo, issue, number, tier, at, org)
	}
	for number := uint32(1); number <= 7; number++ {
		pull(github, number)
		pull(gitlab, number)
	}
	link(github, "I-GH", 1, "native", t1)
	link(github, "I-JI", 1, "native", t1)
	// gitlab#1's native link covers its lower-tier rows.
	link(gitlab, "I-GL", 1, "native", t1)
	link(gitlab, "I-LI", 1, "explicit_text", t1)
	link(gitlab, "I-JI", 1, "heuristic", t1)
	// gitlab#6 has lower-tier links only: the fallback, with the weakest tier.
	link(gitlab, "I-LX", 6, "explicit_text", t1)
	link(gitlab, "I-JX", 6, "heuristic", t1)
	link(github, "I-NONE", 2, "native", t1)
	// Across providers: the link is the relation, whatever the two ends are.
	link(gitlab, "I-GH", 2, "native", t1)
	link(github, "I-GL", 6, "native", t1)
	// Issue I-LI has a native link to github#4, so its lower-tier link to
	// github#3 is covered: it contributes nothing and #3 is unstated.
	link(github, "I-LI", 3, "heuristic", t1)
	link(github, "I-LI", 4, "native", t1)
	// One id held by two work items of the organization: work_items is keyed
	// by (org_id, repo_id, work_item_id), and a link row names no issue
	// repository. The row in the pull request's own repository is the issue;
	// with none, the link is ambiguous and names no project.
	workItem("I-DUP", github, "github", "ghprojv2:GH", t1)
	workItem("I-DUP", zeroRepositoryUUID, "jira", "JI", t1)
	link(github, "I-DUP", 7, "native", t1)
	link(gitlab, "I-DUP", 7, "native", t1)
	// A link row of another organization on the same key contributes nothing,
	// also when that organization holds an issue with the same id.
	mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, created_at, last_synced) VALUES ('I-LI', ?, 'org-other', 'Other', 'open', '', '', 'linear', 'LI', ?, ?, ?)`,
		zeroRepositoryUUID, t1, t1, t1)
	mustExec(t, ctx, direct, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (toUUID(?), 'I-LI', 5, 0.9, 'native', '', ?, 'org-other')`, github, t1)

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.WithWorkItemProjectMembership(true)
	type statement struct {
		set  []string
		tier string
	}
	drain := func(cursor string) (map[string]statement, string) {
		t.Helper()
		stated := map[string]statement{}
		for page := 0; page < 60; page++ {
			batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: org, Source: devhealthsource.SourceName, Cursor: cursor})
			if err != nil {
				t.Fatalf("page %d: %v", page, err)
			}
			if !available {
				return stated, cursor
			}
			if err := batch.Validate(); err != nil {
				t.Fatalf("page %d: batch failed contract validation: %v", page, err)
			}
			for _, entity := range batch.Entities {
				if entity.Subject.Kind != contractsv1.ContextFabricSubjectPullRequest {
					continue
				}
				got := statement{}
				if entity.ProjectIdentities != nil {
					got.set = append([]string{}, *entity.ProjectIdentities...)
				}
				if tier := entity.Properties[contractsv1.ContextFabricProjectMembershipTierProperty].String; tier != nil {
					got.tier = *tier
				}
				stated[entity.Subject.CanonicalID] = got
			}
			if batch.NextCursor == cursor {
				return stated, cursor
			}
			cursor = batch.NextCursor
		}
		t.Fatal("the source did not drain in 60 pages")
		return nil, ""
	}
	id := func(repo string, number int) string { return fmt.Sprintf("pull_request:%s:%d", repo, number) }

	stated, cursor := drain("")
	for pullRequest, want := range map[string]statement{
		id(github, 1): {[]string{"project.v2:github:ghprojv2%3AGH", "project.v2:jira:JI"}, "native"},
		id(gitlab, 1): {[]string{"project.v2:gitlab:GL"}, "native"},
		id(gitlab, 6): {[]string{"project.v2:jira:JI", "project.v2:linear:LI"}, "heuristic"},
		id(github, 2): {},
		id(github, 3): {},
		id(github, 4): {[]string{"project.v2:linear:LI"}, "native"},
		id(github, 5): {},
		id(gitlab, 2): {[]string{"project.v2:github:ghprojv2%3AGH"}, "native"},
		id(github, 7): {[]string{"project.v2:github:ghprojv2%3AGH"}, "native"},
		id(gitlab, 7): {},
		id(github, 6): {[]string{"project.v2:gitlab:GL"}, "native"},
	} {
		got, found := stated[pullRequest]
		if !found {
			t.Fatalf("first drain: %s not projected; stated=%v", pullRequest, stated)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("first drain: %s states %+v, want %+v", pullRequest, got, want)
		}
	}

	// A link row arrives; the pull request itself does not sync.
	link(gitlab, "I-JI", 5, "native", t2)
	stated, cursor = drain(cursor)
	if got := stated[id(gitlab, 5)]; !reflect.DeepEqual(got, statement{[]string{"project.v2:jira:JI"}, "native"}) {
		t.Fatalf("after a new link: %+v, want the refresh to state it; stated=%v", got, stated)
	}

	// A linked issue moves project; neither its pull request nor its link
	// syncs.
	workItem("I-GH", github, "github", "ghprojv2:G2", t3)
	stated, cursor = drain(cursor)
	if got := stated[id(github, 1)]; !reflect.DeepEqual(got, statement{[]string{"project.v2:github:ghprojv2%3AG2", "project.v2:jira:JI"}, "native"}) {
		t.Fatalf("after the issue moved: %+v; stated=%v", got, stated)
	}

	// A linked issue's transition: only the transition row is written.
	transition("GL", "GM", "ev-12308-2", t4)
	stated, _ = drain(cursor)
	if got := stated[id(gitlab, 1)]; !reflect.DeepEqual(got, statement{[]string{"project.v2:gitlab:GM"}, "native"}) {
		t.Fatalf("after the transition: %+v; stated=%v", got, stated)
	}
}
