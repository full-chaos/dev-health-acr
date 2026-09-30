package contextpacket_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// sourceRowStatements is every statement of source_rows.go, by name.
func sourceRowStatements(t *testing.T) map[string]string {
	t.Helper()
	statements := map[string]string{
		"repository_by_id": contextpacket.RepositoryByIDQueryV1,
	}
	for _, discovery := range []contextpacket.SourceRowDiscovery{contextpacket.SourceRowDiscoveryIncident} {
		statement, ok := contextpacket.SourceRowDiscoveryStatement(discovery)
		if !ok {
			t.Fatalf("discovery %s has no statement", discovery)
		}
		statements[string(discovery)] = statement
	}
	for _, query := range contextpacket.OrganizationRowQueriesV1 {
		statements[query.ID] = query.Statement
	}
	return statements
}

// CHAOS-4549's rule (every JOIN ON clause a plain column equality) holds for
// the source-row statements too.
func TestSourceRowStatementsJoinOnClausesArePortable(t *testing.T) {
	clauses := 0
	for name, statement := range sourceRowStatements(t) {
		violations, found, _ := chfixture.JoinONViolations(statement)
		clauses += found
		for _, violation := range violations {
			t.Errorf("%s: JOIN ON conjunct %q is not <operand> = <operand>\n%s", name, violation, statement)
		}
	}
	if clauses == 0 {
		t.Fatal("no JOIN ON clause found: the sweep matched nothing")
	}
}

// No source-row statement carries a caller grant: every caller runs the same
// text with the same bindings, so the grant is decided in Go only.
func TestSourceRowStatementsCarryNoGrant(t *testing.T) {
	for name, statement := range sourceRowStatements(t) {
		for _, forbidden := range []string{"repository_scopes", "arrayExists"} {
			if strings.Contains(statement, forbidden) {
				t.Errorf("%s carries %q: a grant inside SQL would split refusal from not-found by statement", name, forbidden)
			}
		}
	}
}

// A repository-level source row is read through its packet catalog
// statement (the same label, citation and provenance the packet gives it);
// an organization-level row through an OrganizationRowQueriesV1 statement,
// which the packet catalog does not carry (it is the packet's read set).
func TestSourceRowQueryIDsAreTheCatalogAndTheOrganizationRows(t *testing.T) {
	ids := contextpacket.SourceRowQueryIDs()
	want := []string{}
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		want = append(want, query.ID)
	}
	for _, query := range contextpacket.OrganizationRowQueriesV1 {
		for _, catalog := range contextpacket.SourceQueryCatalogV1 {
			if catalog.ID == query.ID {
				t.Fatalf("%s is also a packet catalog query", query.ID)
			}
		}
		want = append(want, query.ID)
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("SourceRowQueryIDs = %v, want %v", ids, want)
	}
}

// The organization-level read binds the organization and the locator only.
func TestResolveOrganizationRowBindsOrganizationAndLocator(t *testing.T) {
	observed := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	client := &recordingClient{rows: [][]any{{"acr:v1:team:team-a", "dev_health", "team", "team-a", "Team A", "", "native", 1.0, "provider=jira, active=1", observed, "team-a"}}}
	rows := contextpacket.NewCatalogClickHouseRows(client)
	references, err := rows.ResolveOrganizationRow(context.Background(), "org_1", contextpacket.SourceRowRead{QueryID: "teams.v1", Locator: "acr:v1:team:team-a"})
	if err != nil || len(references) != 1 || references[0].Reference.Evidence.SourceVersion != "teams.v1" || strings.Join(references[0].Key, "|") != "team-a" {
		t.Fatalf("references = %+v, err %v", references, err)
	}
	if len(client.bindings[0]) != 2 {
		t.Fatalf("bindings = %v", client.bindings[0])
	}
	for name, want := range map[string]any{"org_id": "org_1", "evidence_locator": "acr:v1:team:team-a"} {
		if got, ok := sourceRowBinding(client.bindings[0], name); !ok || got != want {
			t.Fatalf("binding %s = %v", name, got)
		}
	}
	if _, err := rows.ResolveOrganizationRow(context.Background(), "org_1", contextpacket.SourceRowRead{QueryID: "pull_requests.v1", Locator: "x"}); !errors.Is(err, contextpacket.ErrUnknownSourceRowQuery) {
		t.Fatalf("a repository statement was accepted as an organization row: %v", err)
	}
}

// recordingClient records every statement and its bindings and answers from
// a fixed row set.
type recordingClient struct {
	statements []string
	bindings   [][]contextpacket.ClickHouseBinding
	rows       [][]any
	err        error
}

func (c *recordingClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	c.bindings = append(c.bindings, bindings)
	if c.err != nil {
		return nil, c.err
	}
	return &rowScanner{rows: c.rows}, nil
}

func sourceRowBinding(bindings []contextpacket.ClickHouseBinding, name string) (any, bool) {
	for _, binding := range bindings {
		if binding.Name == name {
			return binding.Value, true
		}
	}
	return nil, false
}

// ResolveSourceRow filters the kind's statement to one catalog evidence id,
// with every other plan field at its zero value (the current row), and keeps
// two rows so the caller can refuse them as ambiguous.
func TestResolveSourceRowReadsOneLocatorWithCurrentPlan(t *testing.T) {
	observed := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	row := func(id string) []any {
		return []any{id, "dev_health", "work_item", "jira:ABC-1", "Title", "", "native", 1.0, "open", observed}
	}
	client := &recordingClient{rows: [][]any{row("acr:v1:work-item:jira:ABC-1"), row("acr:v1:work-item:jira:ABC-1")}}
	rows := contextpacket.NewCatalogClickHouseRows(client)
	scope := contractsv1.ResolvedScope{RepoID: "20000000-0000-4000-8000-000000000002", RepoSlug: "acme/api"}
	references, err := rows.ResolveSourceRow(context.Background(), "org_1", scope, contextpacket.SourceRowRead{QueryID: "work_items.v1", Locator: "acr:v1:work-item:jira:ABC-1", TaskRef: "jira:ABC-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 2 {
		t.Fatalf("references = %d, want both rows kept", len(references))
	}
	if references[0].Evidence.SourceVersion != "work_items.v1" || references[0].RepoSlug != "acme/api" {
		t.Fatalf("reference = %+v", references[0])
	}
	statement := client.statements[0]
	if !strings.HasSuffix(statement, `WHERE evidence_ref_id = {evidence_locator:String} LIMIT 2`) {
		t.Fatalf("statement does not filter one locator: %s", statement)
	}
	for name, want := range map[string]any{"evidence_locator": "acr:v1:work-item:jira:ABC-1", "task_ref": "jira:ABC-1", "repo_id": scope.RepoID, "repo_slug": "acme/api", "org_id": "org_1", "branch": "", "time_window_days": uint16(0)} {
		if got, ok := sourceRowBinding(client.bindings[0], name); !ok || got != want {
			t.Fatalf("binding %s = %v, want %v", name, got, want)
		}
	}
	if asOf, _ := sourceRowBinding(client.bindings[0], "as_of"); asOf != nil {
		t.Fatalf("as_of = %v, want nil (the current row)", asOf)
	}
}

func TestResolveSourceRowRefusesAnUnknownStatement(t *testing.T) {
	rows := contextpacket.NewCatalogClickHouseRows(&recordingClient{})
	_, err := rows.ResolveSourceRow(context.Background(), "org_1", contractsv1.ResolvedScope{RepoID: "r"}, contextpacket.SourceRowRead{QueryID: "nope.v1", Locator: "x"})
	if !errors.Is(err, contextpacket.ErrUnknownSourceRowQuery) {
		t.Fatalf("err = %v", err)
	}
}

// The lookups bind the organization and the id only: no grant.
func TestSourceRowLookupsBindNoGrant(t *testing.T) {
	client := &recordingClient{rows: [][]any{{"20000000-0000-4000-8000-000000000002", "acme/api"}}}
	rows := contextpacket.NewCatalogClickHouseRows(client)
	ctx := context.Background()
	if _, err := rows.RepositoryByID(ctx, "org_1", "20000000-0000-4000-8000-000000000002"); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.SourceRowRepositories(ctx, "org_1", contextpacket.SourceRowDiscoveryIncident, "INC-1"); err != nil {
		t.Fatal(err)
	}
	for index, bindings := range client.bindings {
		for _, binding := range bindings {
			switch binding.Name {
			case "org_id", "repo_id", "entity_id":
			default:
				t.Fatalf("lookup %d binds %q", index, binding.Name)
			}
		}
	}
	if _, err := rows.SourceRowRepositories(ctx, "org_1", contextpacket.SourceRowDiscovery("nope"), "x"); !errors.Is(err, contextpacket.ErrUnknownSourceRowQuery) {
		t.Fatalf("unknown discovery err = %v", err)
	}
}

// CHAOS-7226 codex r1 P1, over the source-row statements: every table of
// every statement source_rows.go adds (the repository lookup, the incident
// discovery and the team/project rows) is scoped to the caller's organization, by the same sweep
// CHAOS-7237 holds every catalog statement to
// (TestEveryCatalogStatementScopesEveryTableToTheOrganization).
func TestSourceRowReadsScopeEveryTableToTheOrganization(t *testing.T) {
	for name, statement := range sourceRowStatements(t) {
		violations, parsed := orgScopeViolations(statement)
		if !parsed {
			t.Fatalf("%s: no table found: the sweep matched nothing", name)
		}
		for _, violation := range violations {
			t.Errorf("%s: %s is not scoped to the organization: another organization's rows can join\n%s", name, violation, statement)
		}
	}
}

// #742 r1 P1: an organization-level statement returns the row's own key
// columns after the catalog's ten, and the project statement reads only rows
// whose provider fits the colon-free grammar the ref splits on.
func TestOrganizationRowQueriesReturnTheirKeyColumns(t *testing.T) {
	want := map[string]string{"teams.v1": "id", "projects.v1": "provider,id"}
	for _, query := range contextpacket.OrganizationRowQueriesV1 {
		if strings.Join(query.KeyColumns, ",") != want[query.ID] {
			t.Fatalf("%s key columns = %v, want %s", query.ID, query.KeyColumns, want[query.ID])
		}
		if !strings.Contains(query.Statement, "org_id = {org_id:String}") {
			t.Fatalf("%s is not organization-scoped", query.ID)
		}
	}
	project := contextpacket.OrganizationRowQueriesV1[1]
	if project.ID != "projects.v1" || !strings.Contains(project.Statement, `match(p.provider, '^[a-z][a-z0-9_-]*$')`) {
		t.Fatalf("projects.v1 does not enforce the provider grammar: %s", project.Statement)
	}
}
