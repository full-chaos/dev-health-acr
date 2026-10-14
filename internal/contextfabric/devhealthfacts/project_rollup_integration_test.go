package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

const projectRollupOrg = "6a2d9e41-0f7b-4c58-93e1-5b8c2d7f4a06"

type projectRollupSeed struct {
	direct clickhousedriver.Conn
	ctx    context.Context
	t      *testing.T
	at     time.Time
}

func (s projectRollupSeed) exec(statement string, args ...any) {
	s.t.Helper()
	if err := s.direct.Exec(s.ctx, statement, args...); err != nil {
		s.t.Fatalf("seed: %v\n%s", err, statement)
	}
}

func (s projectRollupSeed) workItem(id, repo, typ, status, project string) {
	s.t.Helper()
	s.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, '', 'linear', ?, NULL, ?)`,
		id, repoUUID(repo), projectRollupOrg, "title "+id, typ, status, s.at, s.at, project, s.at)
}

// pullRequest seeds a pull request whose stored state agrees with its
// timestamps, as ops writes it: merged wins over closed.
func (s projectRollupSeed) pullRequest(repo string, number uint32, created time.Time, merged, closed any) {
	s.t.Helper()
	state := "open"
	switch {
	case merged != nil:
		state = "merged"
	case closed != nil:
		state = "closed"
	}
	s.pullRequestWithState(repo, number, state, created, merged, closed)
}

func (s projectRollupSeed) pullRequestWithState(repo string, number uint32, state any, created time.Time, merged, closed any) {
	s.t.Helper()
	s.exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		repoUUID(repo), projectRollupOrg, number, "pr", state, s.at, created, merged, closed, "feature", "")
}

func (s projectRollupSeed) link(repo, workItem string, number uint32, provenance string) {
	s.t.Helper()
	s.exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID(repo), workItem, number, float32(1), provenance, "", s.at, projectRollupOrg)
}

func (s projectRollupSeed) blocks(source, target string) {
	s.t.Helper()
	s.exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, last_synced) VALUES (?, ?, ?, ?, ?)`,
		projectRollupOrg, source, target, "blocks", s.at)
}

func (s projectRollupSeed) blocksSince(source, target string, started time.Time) {
	s.t.Helper()
	s.exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectRollupOrg, source, target, "blocks", "blocks", s.at, started)
}

func newProjectRollupSeed(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) projectRollupSeed {
	t.Helper()
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_relations_read", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	s := projectRollupSeed{direct: direct, ctx: ctx, t: t, at: time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)}
	for _, repo := range []string{"repo-a", "repo-secret"} {
		s.exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo), projectRollupOrg, "acme/"+repo, "github", s.at)
	}
	for _, project := range []struct{ id, key string }{{"proj-p", ""}, {"proj-q1", "DUP"}, {"proj-q2", "DUP"}} {
		var key any
		if project.key != "" {
			key = project.key
		}
		s.exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project.id, projectRollupOrg, "linear", key, project.id, uint8(1), "active", "", s.at)
	}
	return s
}

func projectRollupRef(t *testing.T, id string) contextfabric.SubjectRef {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindProject, []string{"linear", id}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: %v", err)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonical, Label: id}
}

// A project's pull requests are the ones linked to its issues, each counted
// once under its strongest tier; a link to a missing pull request, a link from
// a pull-request-typed item, and a project key that names two projects count
// nowhere. Its blockers are its issues' open blocker edges over the caller's
// authorized items.
func TestProjectRollupsCountThroughTheEntityTreeOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	s := newProjectRollupSeed(t, ctx, direct)
	for _, item := range []struct{ id, repo, typ, status, project string }{
		{"i-native", "repo-a", "issue", "open", "proj-p"},
		{"i-heuristic", "repo-a", "issue", "open", "proj-p"},
		{"i-text", "repo-a", "issue", "open", "proj-p"},
		{"i-ghost", "repo-a", "issue", "open", "proj-p"},
		{"pr-item", "repo-a", "pr", "open", "proj-p"},
		{"i-dup", "repo-a", "issue", "open", "DUP"},
		{"b-open", "repo-a", "issue", "open", ""},
		{"b-done", "repo-a", "issue", "done", ""},
		{"b-secret", "repo-secret", "issue", "open", ""},
	} {
		s.workItem(item.id, item.repo, item.typ, item.status, item.project)
	}
	inWindow := s.at
	s.pullRequest("repo-a", 1, inWindow, nil, nil)
	s.pullRequest("repo-a", 2, inWindow, inWindow, inWindow)
	s.pullRequest("repo-a", 4, inWindow, nil, nil)
	s.pullRequest("repo-a", 5, inWindow, nil, nil)
	s.link("repo-a", "i-native", 1, "native")
	s.link("repo-a", "i-heuristic", 1, "heuristic")
	s.link("repo-a", "i-text", 2, "explicit_text")
	s.link("repo-a", "i-ghost", 9, "native")
	s.link("repo-a", "pr-item", 4, "native")
	s.link("repo-a", "i-dup", 5, "native")
	s.blocksSince("b-open", "i-native", time.Now().UTC().Add(-12*24*time.Hour-3*time.Hour))
	s.blocks("b-done", "i-heuristic")
	s.blocks("b-secret", "i-text")
	s.blocks("b-open", "pr-item")
	s.blocks("b-open", "i-dup")

	project, ambiguous := projectRollupRef(t, "proj-p"), projectRollupRef(t, "proj-q1")
	providers := devhealthfacts.NewProviders(query)
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}

	pullRequests, err := findProvider(t, providers, contextfabric.FactPullRequests).ReadFacts(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{project, ambiguous}, Time: current,
	})
	if err != nil {
		t.Fatalf("pull requests: %v: %s", err, readFailureCause(err))
	}
	if len(pullRequests.Facts) != 1 {
		t.Fatalf("pull request facts = %#v, want the unambiguous project only", pullRequests.Facts)
	}
	fields := factFor(t, pullRequests, project).Fields
	wantInt(t, fields, "pull_requests_open_current", 1)
	wantInt(t, fields, "pull_requests_opened_window", 2)
	wantInt(t, fields, "pull_requests_merged_window", 1)
	wantInt(t, fields, "pull_requests_closed_unmerged_window", 0)
	if basis := fields["pull_request_link_basis"]; basis.String == nil || *basis.String != "explicit_text" {
		t.Fatalf("pull_request_link_basis = %#v, want explicit_text", basis)
	}
	tiers := map[string]map[string]contextfabric.FactValue{}
	for _, row := range fields["link_tier_breakdown"].Table.Rows {
		tiers[*row.Fields["link_tier"].String] = row.Fields
	}
	if len(tiers) != 2 || *tiers["native"]["pull_requests_open_current"].Integer != 1 || *tiers["explicit_text"]["pull_requests_merged_window"].Integer != 1 {
		t.Fatalf("link_tier_breakdown = %#v, want native (pull request 1 once) and explicit_text (pull request 2)", tiers)
	}

	blockers, err := findProvider(t, providers, contextfabric.FactBlockers).ReadFacts(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"acme/repo-a"}}, contextfabric.FactQuery{
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{project, ambiguous}, Time: current,
	})
	if err != nil {
		t.Fatalf("blockers: %v: %s", err, readFailureCause(err))
	}
	if len(blockers.Facts) != 1 {
		t.Fatalf("blocker facts = %#v, want the unambiguous project only", blockers.Facts)
	}
	fields = factFor(t, blockers, project).Fields
	wantInt(t, fields, "blocked_work_items_current", 1)
	wantInt(t, fields, "blocker_dependencies_current", 1)
	wantInt(t, fields, "blocker_dependencies_excluded_by_status_current", 1)
	wantAgeDays(t, "project row", fields, 12)
	wantAgeString(t, "project row", fields, "oldest_blocked_age_basis", "exact")
}

// A pull request is open when its stored state is exactly open (ops stores the
// state normalized to lowercase and acr compares it exactly); a closed state
// with no close timestamp, a state in another spelling and an unknown state
// are not open.
func TestProjectOpenPullRequestsFollowTheStoredStateOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	s := newProjectRollupSeed(t, ctx, direct)
	for i, state := range []any{"open", " OPEN ", "closed", nil} {
		id := fmt.Sprintf("i-%d", i+1)
		s.workItem(id, "repo-a", "issue", "open", "proj-p")
		s.pullRequestWithState("repo-a", uint32(i+1), state, s.at, nil, nil)
		s.link("repo-a", id, uint32(i+1), "native")
	}
	project := projectRollupRef(t, "proj-p")
	result, err := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests).ReadFacts(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
		Kind: contextfabric.FactPullRequests, Subjects: []contextfabric.SubjectRef{project}, Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
	}
	wantInt(t, factFor(t, result, project).Fields, "pull_requests_open_current", 1)
}

// Supporting the project subject kind directly must not switch the project's
// per-pull-request expansion off: the engine serves both.
func TestProjectPullRequestsAreServedAsRollupBesideTheExpandedPullRequestsOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	s := newProjectRollupSeed(t, ctx, direct)
	s.workItem("i-one", "repo-a", "issue", "open", "proj-p")
	s.workItem("i-two", "repo-a", "issue", "open", "proj-p")
	s.pullRequest("repo-a", 1, s.at, nil, nil)
	s.pullRequest("repo-a", 2, s.at, nil, nil)
	s.link("repo-a", "i-one", 1, "native")
	s.link("repo-a", "i-two", 2, "native")

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{
		ScopeExpander: devhealthfacts.NewScopeExpander(query),
	})
	if err != nil {
		t.Fatal(err)
	}
	project := projectRollupRef(t, "proj-p")
	bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}, contextfabric.CanonicalFactRequest{
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     []contextfabric.SubjectRef{project},
		Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	var rollup *contextfabric.CanonicalFact
	perPullRequest := 0
	for index, fact := range bundle.Facts {
		switch fact.Subject.Kind {
		case contextfabric.SubjectProject:
			rollup = &bundle.Facts[index]
		case contextfabric.SubjectPullRequest:
			perPullRequest++
		}
	}
	if perPullRequest != 2 {
		t.Fatalf("per-pull-request facts = %d, want the 2 the project expansion reaches (facts: %+v)", perPullRequest, bundle.Facts)
	}
	if rollup == nil {
		t.Fatalf("no project rollup beside the expanded pull requests: %+v", bundle.Facts)
	}
	wantInt(t, rollup.Fields, "pull_requests_open_current", 2)
}

// The project rollups' cost follows the links and edges they serve, not the
// size of the project: growing the project's unlinked, edge-free work items
// leaves each statement's peak memory flat and under the statement cap.
func TestProjectRollupsServeAProjectWithManyWorkItemsOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	s := newProjectRollupSeed(t, ctx, direct)
	s.workItem("i-linked", "repo-a", "issue", "open", "proj-p")
	s.workItem("b-open", "repo-a", "issue", "open", "")
	s.pullRequest("repo-a", 1, s.at, nil, nil)
	s.link("repo-a", "i-linked", 1, "native")
	s.blocks("b-open", "i-linked")
	filler := func(prefix string, count int) {
		t.Helper()
		s.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat(?, repeat('x', 96), '-', toString(number)), ?, ?, 'filler', 'issue', 'open', '', ?, ?, '', 'linear', 'proj-p', NULL, ? FROM numbers(?)`,
			prefix, repoUUID("repo-a"), projectRollupOrg, s.at, s.at, s.at, uint64(count))
	}
	project := projectRollupRef(t, "proj-p")
	providers := devhealthfacts.NewProviders(query)
	principal := storage.Principal{OrgID: projectRollupOrg, RepositoryScopes: []string{"*"}}
	rollups := []struct {
		label, field string
		filter       statementFilter
		kind         contextfabric.FactKind
	}{
		{"project pull request rollup", "pull_requests_open_current", statementFilter{Table: "work_graph_issue_pr", QueryContains: "lp.project_key"}, contextfabric.FactPullRequests},
		{"project blocker rollup", "blocked_work_items_current", statementFilter{Table: "work_item_dependencies", QueryContains: "uniqExactIf(tgt, is_open = 1)"}, contextfabric.FactBlockers},
	}
	read := func() []statementMemory {
		t.Helper()
		out := make([]statementMemory, len(rollups))
		for i, rollup := range rollups {
			mark := serverNowMicros(t, ctx, direct)
			result, err := findProvider(t, providers, rollup.kind).ReadFacts(ctx, principal, contextfabric.FactQuery{
				Kind: rollup.kind, Subjects: []contextfabric.SubjectRef{project}, Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			})
			out[i] = statementMemorySince(t, ctx, direct, rollup.filter, mark)
			if err != nil {
				t.Fatalf("%s: %v: %s", rollup.label, err, readFailureCause(err))
			}
			wantInt(t, factFor(t, result, project).Fields, rollup.field, 1)
		}
		return out
	}
	filler("filler-", blockerMemoryFillerItems)
	// The first read of a fresh server peaks high on cold caches; it is not
	// measured.
	read()
	small := read()
	filler("filler-more-", blockerMemoryMoreFillerItems)
	large := read()
	for i, rollup := range rollups {
		assertStatementMemoryFollowsEvidence(t, ctx, direct, rollup.label, small[i], large[i], 1)
	}
}
