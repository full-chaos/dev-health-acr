package contextpacket_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// sourceRowStatements is every statement of source_rows.go, by name.
func sourceRowStatements(t *testing.T) map[string]string {
	t.Helper()
	statements := map[string]string{
		"repository_by_id":   contextpacket.RepositoryByIDQueryV1,
		"dependency_locator": contextpacket.DependencyLocatorQueryV1,
	}
	for _, discovery := range []contextpacket.SourceRowDiscovery{contextpacket.SourceRowDiscoveryIncident, contextpacket.SourceRowDiscoveryDeploymentIncident, contextpacket.SourceRowDiscoveryDependency} {
		statement, ok := contextpacket.SourceRowDiscoveryStatement(discovery)
		if !ok {
			t.Fatalf("discovery %s has no statement", discovery)
		}
		statements[string(discovery)] = statement
	}
	for _, query := range contextpacket.SourceRowOnlyQueriesV1 {
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

// The source-row-only statements are not in the packet catalog: adding them
// there would change every context packet's read set.
func TestSourceRowOnlyQueriesStayOutOfThePacketCatalog(t *testing.T) {
	for _, query := range contextpacket.SourceRowOnlyQueriesV1 {
		for _, catalog := range contextpacket.SourceQueryCatalogV1 {
			if catalog.ID == query.ID {
				t.Fatalf("%s is also a packet catalog query", query.ID)
			}
		}
	}
	ids := contextpacket.SourceRowQueryIDs()
	if len(ids) != len(contextpacket.SourceQueryCatalogV1)+len(contextpacket.SourceRowOnlyQueriesV1) {
		t.Fatalf("SourceRowQueryIDs = %d ids", len(ids))
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
	client.rows = [][]any{{"acr:v1:work-item-dependency:a:b:blocks:fwd"}}
	if _, err := rows.DependencyLocators(ctx, "org_1", "20000000-0000-4000-8000-000000000002", "a:b:blocks"); err != nil {
		t.Fatal(err)
	}
	for index, bindings := range client.bindings {
		for _, binding := range bindings {
			switch binding.Name {
			case "org_id", "repo_id", "entity_id", "entity_key":
			default:
				t.Fatalf("lookup %d binds %q", index, binding.Name)
			}
		}
	}
	if _, err := rows.SourceRowRepositories(ctx, "org_1", contextpacket.SourceRowDiscovery("nope"), "x"); !errors.Is(err, contextpacket.ErrUnknownSourceRowQuery) {
		t.Fatalf("unknown discovery err = %v", err)
	}
}

// organizationScopedStatements is every statement of the packet catalog
// (context_for_task reads all of them; the source-row route reads nine) and
// every statement of source_rows.go.
func organizationScopedStatements(t *testing.T) map[string]string {
	t.Helper()
	statements := sourceRowStatements(t)
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		statements[query.ID] = query.Statement
	}
	return statements
}

var (
	tableAliasPattern = regexp.MustCompile(`(?:FROM|JOIN)\s+([a-z_]+)(?:\s+AS\s+([a-z]+))?`)
	orgBindingPattern = regexp.MustCompile(`(?:toString\()?(?:([a-z]+)\.)?org_id\)?\s*=\s*\{org_id:String\}`)
	orgJoinPattern    = regexp.MustCompile(`(?:toString\()?([a-z]+)\.org_id\)?\s*=\s*(?:toString\()?([a-z]+)\.org_id\)?`)
)

// CHAOS-7226 codex r1 P1: every table of every catalog and source-row
// statement is scoped to the caller's organization, directly ({org_id}
// binding) or through an org_id join to a table that is. ops mints
// repos.id deterministically from the repository name (providersync
// repositoryIdentity), so two organizations syncing one repository share its
// UUID, and a join on repo_id alone admits the other organization's rows. A
// table devhealthschema does not declare is held to the rule too (the ops
// schema gives every one of them org_id: git_commits and git_commit_stats
// since ops migration 027); only a declared table without org_id is exempt.
func TestSourceRowReadsScopeEveryTableToTheOrganization(t *testing.T) {
	statements := organizationScopedStatements(t)
	if len(statements) != len(contextpacket.SourceQueryCatalogV1)+len(sourceRowStatements(t)) {
		t.Fatalf("statement ids collide: %d statements", len(statements))
	}
	for name, statement := range statements {
		constrained := map[string]bool{}
		unaliasedConstrained := false
		for _, match := range orgBindingPattern.FindAllStringSubmatch(statement, -1) {
			if match[1] == "" {
				unaliasedConstrained = true
			} else {
				constrained[match[1]] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for _, match := range orgJoinPattern.FindAllStringSubmatch(statement, -1) {
				a, b := match[1], match[2]
				if constrained[a] != constrained[b] {
					constrained[a], constrained[b], changed = true, true, true
				}
			}
		}
		tables := tableAliasPattern.FindAllStringSubmatch(statement, -1)
		if len(tables) == 0 {
			t.Fatalf("%s: no table found: the sweep matched nothing", name)
		}
		for _, table := range tables {
			columns, declared := devhealthschema.ProductionColumns[table[1]]
			hasOrg := !declared
			for _, column := range columns {
				hasOrg = hasOrg || column.Name == "org_id"
			}
			if !hasOrg {
				continue
			}
			alias := table[2]
			if (alias == "" && !unaliasedConstrained) || (alias != "" && !constrained[alias]) {
				t.Errorf("%s: table %s (alias %q) is not scoped to the organization: another organization's rows can join\n%s", name, table[1], alias, statement)
			}
		}
	}
}
