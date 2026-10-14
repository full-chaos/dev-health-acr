package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const commitmentOrg = "org-commitment-store"

type commitmentSeed struct {
	t      *testing.T
	ctx    context.Context
	direct clickhousedriver.Conn
	at     time.Time
}

func (s commitmentSeed) exec(statement string, args ...any) {
	s.t.Helper()
	if err := s.direct.Exec(s.ctx, statement, args...); err != nil {
		s.t.Fatalf("seed: %v\n%s", err, statement)
	}
}

func (s commitmentSeed) project(provider, id string, key, target any, state, stateSource string) {
	s.t.Helper()
	s.exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, state_source, target_date, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, commitmentOrg, provider, key, id, uint8(1), state, stateSource, target, "", s.at)
}

func (s commitmentSeed) item(provider, id, typ, status, project string, due any, sprint string, synced time.Time) {
	s.t.Helper()
	s.itemIn("repo-commitment", provider, id, typ, status, project, due, sprint, synced)
}

func (s commitmentSeed) itemIn(repo, provider, id, typ, status, project string, due any, sprint string, synced time.Time) {
	s.t.Helper()
	s.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced, due_at, sprint_id) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, '', ?, ?, NULL, ?, ?, ?)`,
		id, repoUUID(repo), commitmentOrg, "title "+id, typ, status, synced, synced, provider, project, synced, due, sprint)
}

func (s commitmentSeed) sprint(org, provider, id string, ended time.Time) {
	s.t.Helper()
	s.exec(`INSERT INTO sprints (provider, sprint_id, ended_at, last_synced, org_id) VALUES (?, ?, ?, ?, ?)`, provider, id, ended, s.at, org)
}

func commitmentDay(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func TestCommitmentServedAtTheReadFactsBoundaryForEveryProviderOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_graph_issue_pr", "git_pull_requests",
		"projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions", "sprints",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	s := commitmentSeed{t: t, ctx: ctx, direct: direct, at: commitmentDay(2026, 10, 12)}
	for _, repo := range []string{"repo-commitment", "repo-hidden"} {
		s.exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo), commitmentOrg, "acme/"+repo, "github", s.at)
	}

	s.project("linear", "lin-p", nil, commitmentDay(2026, 11, 1), "started", "linear.project.state")
	s.project("jira", "10001", "JP", nil, "started", "jira.project.category")
	s.project("gitlab", "gl-p", nil, nil, "", "")
	s.project("github", "gh-p", nil, nil, "", "")
	s.project("gitlab", "gl-none", nil, nil, "", "")
	s.project("github", "gh-empty", nil, nil, "", "")
	s.project("gitlab", "gl-nullend", nil, nil, "", "")
	s.project("jira", "jr-missing", nil, nil, "", "")
	s.project("linear", "lin-mixed", nil, nil, "", "")

	older, newer := commitmentDay(2026, 10, 1), commitmentDay(2026, 10, 11)
	s.item("linear", "L1", "issue", "in_progress", "lin-p", commitmentDay(2026, 10, 1), "linear:cycle:c1", newer)
	s.item("linear", "L2", "issue", "done", "lin-p", commitmentDay(2026, 10, 1), "", newer)
	s.item("linear", "L3", "issue", "canceled", "lin-p", commitmentDay(2026, 10, 5), "", newer)
	s.item("linear", "L4", "issue", "todo", "lin-p", commitmentDay(2026, 12, 1), "linear:cycle:c2", newer)
	s.item("linear", "L5", "issue", "backlog", "lin-p", nil, "linear:cycle:c3", newer)
	s.item("linear", "L6", "pr", "in_progress", "lin-p", commitmentDay(2026, 10, 1), "", newer)
	s.item("linear", "L7", "issue", "in_progress", "lin-p", commitmentDay(2026, 10, 2), "", older)
	s.item("linear", "L7", "issue", "done", "lin-p", commitmentDay(2026, 10, 2), "", newer)
	s.item("linear", "L8", "issue", "unknown", "lin-p", commitmentDay(2026, 10, 1), "", newer)
	s.item("linear", "L9", "issue", "", "lin-p", nil, "", newer)
	s.sprint(commitmentOrg, "linear", "linear:cycle:c1", commitmentDay(2026, 10, 6))
	s.sprint(commitmentOrg, "linear", "linear:cycle:c2", commitmentDay(2026, 10, 27))
	s.sprint(commitmentOrg, "linear", "linear:cycle:c3", commitmentDay(2026, 10, 20))
	s.sprint("org-other", "linear", "linear:cycle:c2", commitmentDay(2027, 1, 1))

	s.item("jira", "JP-1", "issue", "in_progress", "JP", commitmentDay(2026, 10, 10), "42", newer)
	s.item("jira", "JP-2", "issue", "todo", "JP", nil, "", newer)
	s.item("jira", "JP-9", "issue", "in_progress", "", commitmentDay(2026, 10, 1), "", newer)
	s.sprint(commitmentOrg, "jira", "42", commitmentDay(2026, 10, 15))

	s.item("gitlab", "G1", "issue", "todo", "gl-p", nil, "gitlab:acme/x:milestone:7", newer)
	s.item("gitlab", "G2", "issue", "done", "gl-p", nil, "", newer)
	s.sprint(commitmentOrg, "gitlab", "gitlab:acme/x:milestone:7", commitmentDay(2026, 9, 30))

	s.item("github", "H1", "issue", "todo", "gh-p", nil, "ghms:acme/x:3", newer)
	s.sprint(commitmentOrg, "github", "ghms:acme/x:3", commitmentDay(2026, 11, 15))

	s.item("gitlab", "N1", "issue", "todo", "gl-none", nil, "", newer)
	s.item("gitlab", "E1", "issue", "todo", "gl-nullend", nil, "gitlab:acme/x:milestone:99", newer)
	s.item("jira", "R1", "issue", "todo", "jr-missing", nil, "77", newer)
	s.item("linear", "X1", "issue", "todo", "lin-mixed", commitmentDay(2026, 12, 1), "", newer)
	s.item("linear", "X2", "issue", "todo", "lin-mixed", nil, "linear:cycle:missing", newer)
	s.itemIn("repo-hidden", "gitlab", "N2", "issue", "todo", "gl-none", nil, "", newer)
	s.exec(`INSERT INTO sprints (provider, sprint_id, ended_at, last_synced, org_id) VALUES (?, ?, NULL, ?, ?)`, "gitlab", "gitlab:acme/x:milestone:99", s.at, commitmentOrg)

	linear, jira, gitlab, github := projectSubject("linear", "lin-p"), projectSubject("jira", "10001"), projectSubject("gitlab", "gl-p"), projectSubject("github", "gh-p")
	none, empty := projectSubject("gitlab", "gl-none"), projectSubject("github", "gh-empty")
	nullEnd, missing, mixed := projectSubject("gitlab", "gl-nullend"), projectSubject("jira", "jr-missing"), projectSubject("linear", "lin-mixed")
	response := readCommitmentFactsAs(t, query, storage.Principal{OrgID: commitmentOrg, Subject: "commitment", CredentialID: "commitment"}, linear, jira, gitlab, github, none, empty, nullEnd, missing, mixed)

	for _, subject := range []contextfabric.SubjectRef{nullEnd, missing} {
		fact := servedCommitment(t, response, subject)
		wantServed(t, fact, map[string]any{"items_total": "1", "items_open": "1", "items_with_due_at": "0", "sprint_refs_unresolved": "1", "sprint_refs_unresolved_reason": "sprint_not_found_or_without_end", "items_unreadable_count": "0"})
		wantAbsent(t, fact, "commitment_status", "sprint_kind", "next_sprint_end", "latest_sprint_end")
	}
	fact := servedCommitment(t, response, mixed)
	wantServed(t, fact, map[string]any{"items_total": "2", "items_with_due_at": "1", "sprint_refs_unresolved": "1", "sprint_refs_unresolved_reason": "sprint_not_found_or_without_end", "commitment_status": "stated"})
	wantAbsent(t, fact, "sprint_kind")

	fact = servedCommitment(t, response, linear)
	wantServed(t, fact, map[string]any{
		"as_of_day": "2026-10-13", "commitment_status": "stated",
		"target_date": "2026-11-01", "target_date_provider": "linear", "days_to_target": "19",
		"project_state": "started", "state_source": "linear.project.state",
		"items_total": "8", "items_open": "3", "items_closed": "3", "unknown_status_count": "2",
		"items_with_due_at": "6", "items_without_due_at": "2", "items_past_due_open": "1", "items_unreadable_count": "0",
		"sprint_kind": "cycle", "next_sprint_end": "2026-10-20T00:00:00Z", "latest_sprint_end": "2026-10-27T00:00:00Z",
	})
	wantAbsent(t, fact, "items_unavailable_reason", "items_unreadable_reason", "iteration_basis")

	fact = servedCommitment(t, response, jira)
	wantServed(t, fact, map[string]any{
		"commitment_status": "stated", "items_total": "2", "items_open": "2", "items_closed": "0",
		"items_with_due_at": "1", "items_without_due_at": "1", "items_past_due_open": "1",
		"sprint_kind": "sprint", "next_sprint_end": "2026-10-15T00:00:00Z", "latest_sprint_end": "2026-10-15T00:00:00Z",
	})
	wantAbsent(t, fact, "target_date", "target_date_provider", "days_to_target")

	fact = servedCommitment(t, response, gitlab)
	wantServed(t, fact, map[string]any{
		"commitment_status": "stated", "items_total": "2", "items_with_due_at": "0", "items_without_due_at": "2",
		"items_past_due_open": "0", "sprint_kind": "milestone", "latest_sprint_end": "2026-09-30T00:00:00Z",
		"project_state": nil, "state_source": nil,
	})
	wantAbsent(t, fact, "target_date", "next_sprint_end")

	fact = servedCommitment(t, response, github)
	wantServed(t, fact, map[string]any{
		"commitment_status": "stated", "items_total": "1", "sprint_kind": "milestone",
		"next_sprint_end": "2026-11-15T00:00:00Z", "latest_sprint_end": "2026-11-15T00:00:00Z",
		"iteration_basis": "not_collected",
	})
	wantAbsent(t, fact, "target_date")

	fact = servedCommitment(t, response, none)
	wantServed(t, fact, map[string]any{"commitment_status": "none_stated", "items_total": "2", "items_with_due_at": "0", "items_past_due_open": "0", "items_unreadable_count": "0", "sprint_refs_unresolved": "0"})
	wantAbsent(t, fact, "sprint_refs_unresolved_reason")
	wantAbsent(t, fact, "target_date", "sprint_kind", "next_sprint_end", "latest_sprint_end")

	fact = servedCommitment(t, response, empty)
	wantServed(t, fact, map[string]any{"iteration_basis": "not_collected", "items_total": "0", "items_open": "0", "items_closed": "0"})
	wantAbsent(t, fact, "commitment_status")

	denied := readCommitmentFactsAs(t, query, storage.Principal{OrgID: commitmentOrg, Subject: "commitment", CredentialID: "commitment", RepositoryScopes: []string{"acme/elsewhere"}}, linear, none, empty)
	wantCommitmentDenied(t, denied, linear, none, empty)

	restricted := readCommitmentFactsThrough(t, query, ownershipReachGraph{repositories: []string{"acme/repo-commitment"}}, storage.Principal{OrgID: commitmentOrg, Subject: "commitment", CredentialID: "commitment", RepositoryScopes: []string{"acme/repo-commitment"}}, linear, none, empty)
	fact = servedCommitment(t, restricted, none)
	wantServed(t, fact, map[string]any{"items_total": "1", "items_unreadable_count": "1", "items_unreadable_reason": "items_not_readable_for_caller"})
	wantAbsent(t, fact, "commitment_status")
	fact = servedCommitment(t, restricted, empty)
	wantServed(t, fact, map[string]any{"iteration_basis": "not_collected", "items_unreadable_count": "0"})
	wantAbsent(t, fact, "commitment_status")
	fact = servedCommitment(t, restricted, linear)
	wantServed(t, fact, map[string]any{"target_date": "2026-11-01", "commitment_status": "stated", "items_total": "8", "items_past_due_open": "1", "items_unreadable_count": "0"})
	if fact.PopulationScope != "caller_authorized_items" {
		t.Errorf("restricted population_scope = %q, want caller_authorized_items", fact.PopulationScope)
	}
}

type ownershipReachGraph struct {
	admitAllGraph
	repositories []string
}

func (g ownershipReachGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	out := make([][]string, len(subjects))
	for index := range out {
		out[index] = append([]string(nil), g.repositories...)
	}
	return out, nil
}
