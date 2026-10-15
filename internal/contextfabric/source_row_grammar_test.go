package contextfabric_test

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7226 r2 P1 class, CHAOS-7252: a source-row read keyed by a ref
// string is correct only when that string names ONE row. Two properties
// decide it:
//
//  1. The producer's grammar is injective: no two distinct component tuples
//     serialize to the same id. Work item, team, project and relation ids
//     hold ':', so a grammar that joins two or more of them with a bare ':'
//     is not (the fixed-length repository UUID as the first component is the
//     one exception: a UUID holds no ':').
//  2. The id carries its table's key within the organization (every ORDER
//     BY column but org_id), so the grant filter never chooses between
//     distinct rows: it decides serve or refuse for the one row the id names.
//     The only exceptions are named below, each with its reason.
//
// Every mint site is covered: TestEveryMintSiteIsInTheSweep DERIVES the
// sites from the source (mint_site_inventory_test.go) and fails on a site no
// entry lists, on a listed site that no longer exists, and on a site whose
// shape differs from its entry's. Each entry's grammar model follows from
// the shape the inventory reads -- a bare ':' join of N operands, a
// catalog locator of N SQL components, or evidenceref's escaped grammar
// (minted through evidenceref.Mint, the function every ".v2" producer
// calls) -- so the injectivity search runs on what the code does. A kind is
// on a source-row route exactly when every one of its entries passes.

const grammarRepoID = "20000000-0000-4000-8000-000000000002"

type producerGrammar struct {
	kind contractsv1.ContextFabricEvidenceEntityType
	// shape is the inventory's shape of every listed site: "concat/N",
	// "mint/N", "sqlmint/N", "sql/N" or "literal".
	shape string
	// sites lists the mint sites ("<file>|<function or var>") and how many
	// mints of this kind and shape each holds.
	sites map[string]int
	// repoAnchored: the id's first component is the repository UUID.
	repoAnchored bool
	// rowQuery names the statement that reads the row (a packet catalog or
	// source-row-only statement id); the row's table is DERIVED from its
	// FROM clause, never written here (a test naming several declared
	// tables as literals would be a second physical source, which
	// devhealthschema's TestNoSecondPhysicalSourceOutsideTheDeclaration
	// forbids). keyByColumns instead derives the table as the ONE declared
	// table whose key is exactly keyColumns plus the exception columns (a
	// kind whose read is not built yet). The id must carry every ORDER BY
	// column of that table but org_id (keyExceptions names the ones it
	// carries in another form, and why).
	rowQuery     string
	keyByColumns bool
	keyColumns   []string
	// columns names, in order, the column each id component of a bare-join
	// shape ("concat/N", "sql/N") carries; a component carrying several
	// columns joins them with '+'. A ".v2" shape's columns are its
	// evidenceref grammar's, never written here. carriedGaps mints a ref
	// from a synthetic row through these and requires every key column's
	// value among the ref's segments.
	columns       []string
	keyExceptions map[string]string
	// providerFirst: the first component is a project provider, a closed
	// colon-free enum (CHAOS-7227: the project statements read only
	// providers matching ^[a-z][a-z0-9_-]*$, and the resolver refuses any
	// other), so a project id splits at its first ':'.
	providerFirst bool
	// retired: a pre-CHAOS-7252 grammar of a retired kind.
	retired bool
	// fixture: a constant id in a test harness compiled into production
	// packages; it names a fixture row and is no grammar.
	fixture bool
	// onRecord says why a kind whose entries all pass is not routed.
	onRecord string
}

// arityAndMint builds the entry's grammar model from its shape.
func (g producerGrammar) arityAndMint(t *testing.T) (int, func([]string) string) {
	t.Helper()
	form, count, _ := strings.Cut(g.shape, "/")
	n, err := strconv.Atoi(count)
	if err != nil {
		t.Fatalf("%s: shape %q has no component count", g.kind, g.shape)
	}
	switch form {
	case "concat", "sql":
		// A bare ':' join. A catalog locator ("sql") is read inside the
		// one repository its statement binds, so it is repo-anchored; an
		// organization row statement names its table through rowQuery.
		if g.repoAnchored && form == "concat" {
			return n - 1, func(c []string) string { return strings.Join(append([]string{grammarRepoID}, c...), ":") }
		}
		return n, func(c []string) string { return strings.Join(c, ":") }
	case "mint", "sqlmint":
		grammar, ok := evidenceref.Lookup(g.kind)
		if !ok || len(grammar.Segments) != n {
			t.Fatalf("%s: shape %q does not match its evidenceref grammar", g.kind, g.shape)
		}
		uuids := 0
		for _, segment := range grammar.Segments {
			if segment.Form == evidenceref.UUID {
				uuids++
			}
		}
		return n - uuids, v2Mint(g.kind, uuids)
	}
	t.Fatalf("%s: unknown shape %q", g.kind, g.shape)
	return 0, nil
}

func v2Mint(kind contractsv1.ContextFabricEvidenceEntityType, uuidSegments int) func([]string) string {
	return func(c []string) string {
		values := make([]string, 0, uuidSegments+len(c))
		for i := 0; i < uuidSegments; i++ {
			values = append(values, grammarRepoID)
		}
		values = append(values, c...)
		ref, _ := evidenceref.Mint(kind, values...)
		if ref == "" {
			panic("evidenceref refused a sweep tuple for " + string(kind))
		}
		return strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix+string(kind)+":")
	}
}

const (
	facts      = "internal/contextfabric/devhealthfacts/"
	source     = "internal/contextfabric/devhealthsource/"
	catalog    = "internal/contextpacket/source_queries.go|SourceQueryCatalogV1"
	sourceRows = "internal/contextpacket/source_rows.go|SourceRowOnlyQueriesV2"
	// organizationRows: the team and project row statements (CHAOS-7227).
	organizationRows = "internal/contextpacket/source_rows.go|OrganizationRowQueriesV1"
)

func sites(keys ...string) map[string]int {
	out := map[string]int{}
	for _, key := range keys {
		count := 1
		if name, times, ok := strings.Cut(key, "*"); ok {
			key = name
			count, _ = strconv.Atoi(times)
		}
		out[key] = count
	}
	return out
}

// readRepository: a catalog locator names the row inside the repository its
// read binds (repo_id), which is the ref's own repository.
var readRepository = map[string]string{"repo_id": "bound by the read: the catalog statement filters repo_id = the ref's repository"}

const (
	noSourceRow = "no Context Fabric producer mints this kind: a packet catalog locator inside ev2 handles only"
)

func producerGrammars() []producerGrammar {
	return []producerGrammar{
		// repository: the UUID alone.
		{kind: contractsv1.ContextFabricEvidenceEntityRepository, shape: "concat/1", repoAnchored: true, rowQuery: "repository_freshness.v1", keyColumns: []string{"id"}, columns: []string{"id"}, sites: sites(
			facts+"ci.go|readRepositoryAggregate", facts+"deployments.go|readRepositoryAggregate", facts+"flow.go|readRepositoryFlow",
			facts+"health.go|ReadFacts", facts+"health.go|readProjectHealth*2", facts+"identity.go|ReadFacts*2", facts+"file_hotspots.go|ReadFacts", facts+"security_alerts.go|ReadFacts",
			facts+"investment_repo_mix.go|readRepositoryThemeMix", facts+"investment_units.go|readInvestmentUnits", facts+"investment_units.go|unitFact", facts+"metrics.go|readRepositoryMetrics*2",
			source+"tables.go|queryRepositories", source+"teams_projects_edges.go|queryRepositoryTeams")},
		{kind: contractsv1.ContextFabricEvidenceEntityRepository, shape: "sql/1", rowQuery: "repository_freshness.v1", keyColumns: []string{"id"}, columns: []string{"id"}, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityRepository, shape: "literal", fixture: true, sites: sites("internal/contextfabric/pginvestigation/paritytest/paritytest.go|RunCitedEvidenceSuite*2")},
		// work item, pull request, CI run, deployment: <repo>:<one id>; the
		// catalog's locator is the one id, read in the ref's repository.
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItem, shape: "concat/2", repoAnchored: true, rowQuery: "work_items.v1", keyColumns: []string{"repo_id", "work_item_id"}, columns: []string{"repo_id", "work_item_id"}, sites: sites(
			facts+"identity.go|ReadFacts*2", facts+"workitems.go|ReadFacts*3", source+"tables.go|queryWorkItems",
			source+"issue_pull_request_link.go|queryIssuePullRequestLinks", source+"teams_projects_edges.go|querySubjectProjectMemberships", "internal/contextfabric/work_item_payload.go|canonicalWorkItemEvidenceRef")},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItem, shape: "sql/1", rowQuery: "work_items.v1", keyColumns: []string{"work_item_id"}, columns: []string{"work_item_id"}, keyExceptions: readRepository, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityPullRequest, shape: "concat/2", repoAnchored: true, rowQuery: "pull_requests.v1", keyColumns: []string{"repo_id", "number"}, columns: []string{"repo_id", "number"}, sites: sites(
			facts+"investment_units.go|unitFact", facts+"pullrequests.go|ReadFacts", source+"issue_pull_request_link.go|queryIssuePullRequestLinks", source+"tables.go|scanPullRequestRow", source+"teams_projects_edges.go|querySubjectProjectMemberships")},
		{kind: contractsv1.ContextFabricEvidenceEntityPullRequest, shape: "sql/1", rowQuery: "pull_requests.v1", keyColumns: []string{"number"}, columns: []string{"number"}, keyExceptions: readRepository, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityCI, shape: "concat/2", repoAnchored: true, rowQuery: "ci_pipeline_runs.v1", keyColumns: []string{"repo_id", "run_id"}, columns: []string{"repo_id", "run_id"}, sites: sites(facts+"ci.go|readRunStatus", source+"tables.go|queryCIRuns")},
		{kind: contractsv1.ContextFabricEvidenceEntityCI, shape: "sql/1", rowQuery: "ci_pipeline_runs.v1", keyColumns: []string{"run_id"}, columns: []string{"run_id"}, keyExceptions: readRepository, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityDeployment, shape: "concat/2", repoAnchored: true, rowQuery: "deployments.v1", keyColumns: []string{"repo_id", "deployment_id"}, columns: []string{"repo_id", "deployment_id"}, sites: sites(facts+"deployments.go|readDeploymentStatus", source+"tables.go|queryDeployments")},
		{kind: contractsv1.ContextFabricEvidenceEntityDeployment, shape: "sql/1", rowQuery: "deployments.v1", keyColumns: []string{"deployment_id"}, columns: []string{"deployment_id"}, keyExceptions: readRepository, sites: sites(catalog)},
		// incident: the id alone, keyed (org_id, id).
		{kind: contractsv1.ContextFabricEvidenceEntityIncident, shape: "concat/1", rowQuery: "incidents.v1", keyColumns: []string{"id"}, columns: []string{"id"}, sites: sites(facts+"incidents.go|ReadFacts", source+"tables.go|queryIncidents")},
		{kind: contractsv1.ContextFabricEvidenceEntityIncident, shape: "sql/1", rowQuery: "incidents.v1", keyColumns: []string{"id"}, columns: []string{"id"}, sites: sites(catalog)},

		// team, project: organization rows authorized by ownership through
		// the subject gate (CHAOS-7227).
		{kind: contractsv1.ContextFabricEvidenceEntityTeam, shape: "concat/1", rowQuery: "teams.v1", keyColumns: []string{"id"}, columns: []string{"id"}, sites: sites(
			"internal/contextfabric/chaos5990_period_delta.go|periodDeltaSubjectEvidenceRef", facts+"deficiencies.go|ReadFacts",
			facts+"dependencies.go|readTeamRollup", facts+"deployments.go|readTeamRollup", facts+"incidents.go|readTeamRollup", facts+"pullrequests.go|readTeamRollup",
			facts+"flow.go|readProjectFlow", facts+"flow.go|readTeamFlow", facts+"health.go|ReadFacts", facts+"health.go|readProjectHealth*2",
			facts+"investment.go|readTeamThemeMix", facts+"investment_units.go|readInvestmentUnits", facts+"landscape.go|readProjectLandscape",
			facts+"landscape.go|readTeamLandscape", facts+"metrics.go|readProjectMetrics", facts+"metrics.go|readTeamMetrics*2",
			facts+"readiness.go|readProjectReadiness", facts+"readiness.go|readTeamReadiness", facts+"workload.go|readProjectWorkload*2",
			facts+"workload.go|readTeamWorkload", source+"teams_projects.go|fetchTeams", source+"teams_projects_edges.go|queryRepositoryTeams")},
		{kind: contractsv1.ContextFabricEvidenceEntityTeam, shape: "sql/1", rowQuery: "teams.v1", keyColumns: []string{"id"}, columns: []string{"id"}, sites: sites(organizationRows)},
		{kind: contractsv1.ContextFabricEvidenceEntityTeam, shape: "literal", fixture: true, sites: sites("internal/contextfabric/pginvestigation/paritytest/paritytest.go|RunCitedEvidenceSuite")},
		// A project's id is "<provider>:<project id>", the provider a closed
		// colon-free enum (CHAOS-7227). The facts pass the joined key in one
		// variable (concat/1 below: one opaque operand carrying both).
		{kind: contractsv1.ContextFabricEvidenceEntityProject, shape: "concat/2", providerFirst: true, rowQuery: "projects.v1", keyColumns: []string{"provider", "id"}, columns: []string{"provider", "id"}, sites: sites(
			"internal/contextfabric/chaos5990_period_delta.go|periodDeltaSubjectEvidenceRef", facts+"commitment.go|ReadFacts", facts+"pull_request_project_rows.go|pullRequestProjectBreakdown", facts+"project_rollups.go|readProjectRollup*2", source+"teams_projects.go|queryProjects")},
		{kind: contractsv1.ContextFabricEvidenceEntityProject, shape: "sql/2", providerFirst: true, rowQuery: "projects.v1", keyColumns: []string{"provider", "id"}, columns: []string{"provider", "id"}, sites: sites(organizationRows)},
		{kind: contractsv1.ContextFabricEvidenceEntityProject, shape: "concat/1", rowQuery: "projects.v1", keyColumns: []string{"provider", "id"}, columns: []string{"provider+id"}, sites: sites(
			facts+"flow.go|readProjectFlow", facts+"health.go|readProjectHealth", facts+"investment.go|mergeProjectInvestmentFact",
			facts+"landscape.go|readProjectLandscape", facts+"metrics.go|readProjectMetrics",
			facts+"readiness.go|readProjectReadiness", facts+"workitems.go|readProjectActualCompletion", facts+"workload.go|readProjectWorkload")},

		// organization, episode: no canonical row.
		{kind: contractsv1.ContextFabricEvidenceEntityOrganization, shape: "concat/1", sites: sites(facts+"investment_org_mix.go|readOrganizationThemeMix", facts+"investment_units.go|readInvestmentUnits", facts+"investment_units.go|unitFact", facts+"source_health.go|read", source+"clickhouse.go|organizationCandidate")},
		{kind: contractsv1.ContextFabricEvidenceEntityEpisode, shape: "concat/1", sites: sites(source + "episodes.go|episodeCandidate")},

		// Packet catalog locators of kinds no Context Fabric producer mints.
		{kind: contractsv1.ContextFabricEvidenceEntityCommit, shape: "sql/1", onRecord: noSourceRow, sites: sites(catalog, "internal/contextpacket/read_adapter.go|clickHouseEvidenceQueryV1")},
		{kind: contractsv1.ContextFabricEvidenceEntityCommitFile, shape: "sql/2", sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityGraph, shape: "sql/1", onRecord: noSourceRow, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityHotspot, shape: "sql/1", onRecord: noSourceRow, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityComplexity, shape: "sql/1", onRecord: noSourceRow, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityAIRun, shape: "sql/1", onRecord: noSourceRow, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityAIArtifact, shape: "sql/1", onRecord: noSourceRow, sites: sites(catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityReviewOutcome, shape: "sql/1", onRecord: noSourceRow, sites: sites(catalog)},

		// RETIRED (CHAOS-7252): no producer mints them any more. The packet
		// catalog's two locators (CHAOS-7261) now render the ".v2" grammar
		// below, so ev2 handles hash an injective locator.

		// The ".v2" successors: minted by evidenceref.Mint, read by the SQL
		// evidenceref.SQL renders from the same grammar.
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, shape: "mint/3", rowQuery: "work_item_dependencies.v2",
			keyColumns:    []string{"source_work_item_id", "target_work_item_id", "relation_key"},
			keyExceptions: map[string]string{"relationship_type": "relation_key: the raw spellings sharing one dependencyrelation.Key are one relation (CHAOS-7177); the read serves the catalog's representative"},
			sites:         sites(facts+"dependencies.go|dependencyEvidenceRefID", source+"tables.go|queryWorkItemDependencies")},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, shape: "sqlmint/3", rowQuery: "work_item_dependencies.v2",
			keyColumns:    []string{"source_work_item_id", "target_work_item_id", "relation_key"},
			keyExceptions: map[string]string{"relationship_type": "relation_key (CHAOS-7177), as above"},
			sites:         sites(sourceRows, catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, shape: "mint/3", repoAnchored: true, rowQuery: "work_item_hierarchy.v2",
			keyColumns: []string{"repo_id", "work_item_id", "parent_id"}, sites: sites(source + "tables.go|queryWorkItemHierarchy")},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, shape: "sqlmint/3", repoAnchored: true, rowQuery: "work_item_hierarchy.v2",
			keyColumns: []string{"repo_id", "work_item_id", "parent_id"}, sites: sites(sourceRows)},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, shape: "mint/4", rowQuery: "work_item_teams.v2",
			keyColumns: []string{"repo_id", "work_item_id", "team_id", "source"}, sites: sites(source + "teams_projects_edges.go|queryWorkItemTeams")},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, shape: "sqlmint/4", rowQuery: "work_item_teams.v2",
			keyColumns: []string{"repo_id", "work_item_id", "team_id", "source"}, sites: sites(sourceRows)},
		{kind: contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, shape: "mint/4", repoAnchored: true, rowQuery: "deployment_incident_edges.v2",
			keyColumns: []string{"repo_id", "deployment_id", "incident_id", "source"}, sites: sites(source + "tables.go|queryDeploymentIncidentEdges")},
		{kind: contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, shape: "sqlmint/4", repoAnchored: true, rowQuery: "deployment_incident_edges.v2",
			keyColumns: []string{"repo_id", "deployment_id", "incident_id", "source"}, sites: sites(sourceRows, catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityReviewV2, shape: "mint/3", repoAnchored: true, rowQuery: "pull_request_reviews.v2",
			keyColumns: []string{"repo_id", "number", "review_id"}, sites: sites(facts+"pullrequests.go|ReadFacts", source+"tables.go|queryPullRequestReviews")},
		{kind: contractsv1.ContextFabricEvidenceEntityReviewV2, shape: "sqlmint/3", repoAnchored: true, rowQuery: "pull_request_reviews.v2",
			keyColumns: []string{"repo_id", "number", "review_id"}, sites: sites(sourceRows, catalog)},
		{kind: contractsv1.ContextFabricEvidenceEntityProjectTeamV2, shape: "mint/4", keyByColumns: true,
			keyColumns:    []string{"provider", "project_id", "team_id", "source"},
			keyExceptions: map[string]string{"valid_from": "the ref names the projector's ownership GROUP: every assertion of (provider, project, team, source) aggregated"},
			sites:         sites(source + "teams_projects_edges.go|queryProjectTeams")},
		{kind: contractsv1.ContextFabricEvidenceEntityProjectTeamV2, shape: "sqlmint/4", keyByColumns: true,
			keyColumns:    []string{"provider", "project_id", "team_id", "source"},
			keyExceptions: map[string]string{"valid_from": "the ref names the projector's ownership GROUP: every assertion of (provider, project, team, source) aggregated"},
			sites:         sites(source + "teams_projects_edges.go|ProjectTeamSourceRowQuery")},
	}
}

// grammarComponents are component values with and without ':' and the
// codec's escape texts in every position a real id can hold one (jira:ABC-1,
// ari:cloud:..., blocks:fwd, a literal "%3A").
var grammarComponents = []string{"a", "b", "a:b", "b:a", ":", "a:", ":b", "%", "%3A", "%25"}

// grammarProviders are project provider values: a closed, colon-free enum.
var grammarProviders = []string{"jira", "linear", "gitlab"}

// collision returns two distinct component tuples that mint the same id, or
// false when the grammar is injective over the component set.
func collision(arity int, mint func([]string) string, first []string) ([]string, []string, bool) {
	seen := map[string][]string{}
	var walk func(prefix []string) ([]string, []string, bool)
	walk = func(prefix []string) ([]string, []string, bool) {
		if len(prefix) == arity {
			id := mint(prefix)
			if earlier, found := seen[id]; found {
				return earlier, append([]string(nil), prefix...), true
			}
			seen[id] = append([]string(nil), prefix...)
			return nil, nil, false
		}
		values := grammarComponents
		if len(prefix) == 0 && first != nil {
			values = first
		}
		for _, value := range values {
			if a, b, found := walk(append(prefix, value)); found {
				return a, b, true
			}
		}
		return nil, nil, false
	}
	return walk(nil)
}

// orderByColumns parses the table's ReplacingMergeTree ORDER BY tuple,
// top-level commas only (a key column can be ifNull(team_id, ”)), with
// ifNull(<column>, ...) read as <column>.
func orderByColumns(table string) []string {
	engine := devhealthschema.EngineFull[table]
	start := strings.Index(engine, "ORDER BY (")
	if start < 0 {
		return nil
	}
	body := engine[start+len("ORDER BY ("):]
	var columns []string
	depth, from := 0, 0
	for index, char := range body {
		switch char {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				columns = append(columns, body[from:index])
				return normalizeKeyColumns(columns)
			}
			depth--
		case ',':
			if depth == 0 {
				columns = append(columns, body[from:index])
				from = index + 1
			}
		}
	}
	return nil
}

func normalizeKeyColumns(columns []string) []string {
	out := make([]string, 0, len(columns))
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if inner, ok := strings.CutPrefix(column, "ifNull("); ok {
			column, _, _ = strings.Cut(inner, ",")
		}
		out = append(out, strings.TrimSpace(column))
	}
	return out
}

var fromTable = regexp.MustCompile(`FROM ([a-z_]+)`)

// rowTable derives the entry's row table: from its statement's FROM clause
// (the first declared table it reads), or as the one declared table keyed
// exactly by its key and exception columns; "" when it has neither.
func rowTable(t *testing.T, g producerGrammar) string {
	t.Helper()
	switch {
	case g.rowQuery != "":
		statement := rowStatement(g)
		for _, match := range fromTable.FindAllStringSubmatch(statement, -1) {
			if _, declared := devhealthschema.EngineFull[match[1]]; declared {
				return match[1]
			}
		}
		t.Fatalf("%s %s: statement %q reads no declared table", g.kind, g.shape, g.rowQuery)
	case g.keyByColumns:
		want := map[string]bool{"org_id": true}
		for _, column := range g.keyColumns {
			want[column] = true
		}
		for column := range g.keyExceptions {
			want[column] = true
		}
		var found []string
		for table := range devhealthschema.EngineFull {
			columns := orderByColumns(table)
			if len(columns) != len(want) {
				continue
			}
			match := true
			for _, column := range columns {
				match = match && want[column]
			}
			if match {
				found = append(found, table)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s %s: %d declared tables are keyed %v: %v", g.kind, g.shape, len(found), want, found)
		}
		return found[0]
	}
	return ""
}

// rowStatement is the statement of the entry's row query, "" when it has none.
func rowStatement(g producerGrammar) string {
	statements := append(append([]contextpacket.SourceQuery(nil), contextpacket.SourceQueryCatalogV1...), contextpacket.SourceRowOnlyQueriesV2...)
	for _, query := range contextpacket.OrganizationRowQueriesV1 {
		statements = append(statements, query.SourceQuery)
	}
	var statement string
	for _, query := range statements {
		if query.ID == g.rowQuery {
			statement = query.Statement
		}
	}
	return statement
}

// currentRowRelations are the tables whose readers pick one row per
// (org_id, id): a row statement that reads through the relation is keyed
// (org_id, id), whatever the sorting key.
var currentRowRelations = map[string]string{
	"operational_incidents":                   devhealthschema.CurrentOperationalIncidents,
	"operational_service_repository_mappings": devhealthschema.CurrentServiceRepositoryMappings,
}

// keyGaps lists the table's key columns (org_id aside) the grammar neither
// carries nor names as an exception.
func keyGaps(t *testing.T, g producerGrammar) []string {
	t.Helper()
	carried := map[string]bool{}
	for _, column := range g.keyColumns {
		carried[column] = true
	}
	table := rowTable(t, g)
	key := orderByColumns(table)
	if relation, current := currentRowRelations[table]; current && strings.Contains(rowStatement(g), relation) {
		key = []string{"org_id", "id"}
	}
	var gaps []string
	for _, column := range key {
		if column == "org_id" || carried[column] {
			continue
		}
		if _, excepted := g.keyExceptions[column]; excepted {
			continue
		}
		gaps = append(gaps, column)
	}
	return gaps
}

// passes runs both checks on one entry and says why it fails.
func passes(t *testing.T, g producerGrammar) (bool, string) {
	t.Helper()
	arity, mint := g.arityAndMint(t)
	var first []string
	if g.providerFirst {
		first = grammarProviders
	}
	if a, b, found := collision(arity, mint, first); found {
		return false, fmt.Sprintf("not injective: %q and %q both mint %q", a, b, mint(a))
	}
	anchored := g.repoAnchored || strings.HasPrefix(g.shape, "sql/")
	table := rowTable(t, g)
	switch {
	case anchored && table == "":
		return false, "repository-anchored with no row key declared"
	case table == "":
		return false, "row-anchored with no single-row key"
	case len(orderByColumns(table)) == 0:
		t.Fatalf("%s %s: no ORDER BY key parsed for %s", g.kind, g.shape, table)
	}
	if gaps := keyGaps(t, g); len(gaps) > 0 {
		return false, fmt.Sprintf("does not carry key columns %v of %s: %s", gaps, table, devhealthschema.EngineFull[table])
	}
	if gaps, why := carriedGaps(g); why != "" || len(gaps) > 0 {
		return false, strings.TrimSpace(fmt.Sprintf("declares key columns its ref does not carry %v %s", gaps, why))
	}
	return true, ""
}

// carriedGaps mints the entry's ref from a synthetic row -- every column a
// distinct, colon-free value -- and lists the declared key columns whose
// value is not among the ref's segments: a key column the entry names that
// its grammar never writes into the ref. A ".v2" shape mints and parses
// through evidenceref; a bare join through its declared component columns,
// whose count must be the shape's (the shape the mint-site inventory reads
// off the source).
func carriedGaps(g producerGrammar) ([]string, string) {
	value := func(column string) string { return "v-" + strings.ReplaceAll(column, "_", "-") }
	form, count, _ := strings.Cut(g.shape, "/")
	var segments []string
	switch form {
	case "mint", "sqlmint":
		if len(g.columns) > 0 {
			return nil, "(a .v2 entry's columns are its grammar's, not declared)"
		}
		grammar, ok := evidenceref.Lookup(g.kind)
		if !ok {
			return nil, "(no grammar)"
		}
		values := make([]string, len(grammar.Segments))
		for index, segment := range grammar.Segments {
			values[index] = value(segment.Column)
		}
		id, ok := evidenceref.ID(g.kind, values...)
		if !ok {
			return nil, "(the grammar refused its own columns)"
		}
		if segments, ok = evidenceref.Parse(g.kind, id); !ok {
			return nil, "(the grammar does not parse its own id)"
		}
	default:
		if n, err := strconv.Atoi(count); err != nil || n != len(g.columns) {
			return nil, fmt.Sprintf("(columns %v for shape %s)", g.columns, g.shape)
		}
		parts := make([]string, 0, len(g.columns))
		for _, component := range g.columns {
			joined := strings.Split(component, "+")
			for index := range joined {
				joined[index] = value(joined[index])
			}
			parts = append(parts, strings.Join(joined, ":"))
		}
		segments = strings.Split(strings.Join(parts, ":"), ":")
	}
	carried := map[string]bool{}
	for _, segment := range segments {
		carried[segment] = true
	}
	var gaps []string
	for _, column := range g.keyColumns {
		if !carried[value(column)] {
			gaps = append(gaps, column)
		}
	}
	return gaps, ""
}

func TestSourceRowGrammarsAreInjective(t *testing.T) {
	expandable := map[contractsv1.ContextFabricEvidenceEntityType]bool{}
	onRecord := map[contractsv1.ContextFabricEvidenceEntityType]string{}
	for _, g := range producerGrammars() {
		if g.fixture {
			continue
		}
		ok, why := passes(t, g)
		if !ok {
			t.Logf("%s (%s at %d sites) %s", g.kind, g.shape, len(g.sites), why)
		}
		_, retired := contractsv1.RetiredEvidenceEntityType(g.kind)
		if g.retired != retired {
			t.Errorf("%s (%s): listed retired=%v, contracts says retired=%v", g.kind, g.shape, g.retired, retired)
		}
		if previous, seen := expandable[g.kind]; seen {
			ok = ok && previous
		}
		expandable[g.kind] = ok
		if g.onRecord != "" {
			onRecord[g.kind] = g.onRecord
		}
	}
	plans := contextfabric.SourceRowPlans()
	for kind, plan := range plans {
		routed := plan.Route != contextfabric.SourceRowRouteRecord
		ok, listed := expandable[kind]
		switch {
		case routed && !listed:
			t.Errorf("%s is on route %s with no producer grammar listed: list it before routing it", kind, plan.Route)
		case routed && !ok:
			t.Errorf("%s is on route %s but a producer grammar fails injectivity or row-key uniqueness", kind, plan.Route)
		case !routed && listed && ok && onRecord[kind] == "":
			t.Errorf("%s passes every check but stays on the record; route it or name why it stays", kind)
		case routed && onRecord[kind] != "":
			t.Errorf("%s is routed but still listed on the record for %s", kind, onRecord[kind])
		}
	}
	// The sweep sees the class it exists for: every retired kind fails, and
	// every ".v2" successor passes.
	for _, kind := range contractsv1.ContextFabricEvidenceEntityTypeVocabulary() {
		successor, retired := contractsv1.RetiredEvidenceEntityType(kind)
		if !retired {
			continue
		}
		if expandable[kind] {
			t.Errorf("retired %s passed the sweep: the collision search or the key check no longer sees it", kind)
		}
		if !expandable[successor] {
			t.Errorf("%s (successor of %s) fails the sweep", successor, kind)
		}
	}
}

// The key check sees a dropped key column: deployment-incident.v2 without
// its source would name one ref for a native and a heuristic row of one
// (deployment, incident). Planted here, seen failing.
func TestSourceRowKeyCheckSeesADroppedKeyColumn(t *testing.T) {
	for _, g := range producerGrammars() {
		if g.kind != contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2 || g.shape != "mint/4" {
			continue
		}
		if ok, why := passes(t, g); !ok {
			t.Fatalf("the real grammar fails: %s", why)
		}
		g.keyColumns = []string{"repo_id", "deployment_id", "incident_id"}
		if gaps := keyGaps(t, g); len(gaps) != 1 || gaps[0] != "source" {
			t.Fatalf("a grammar without source: gaps %v, want [source]", gaps)
		}
		if ok, _ := passes(t, g); ok {
			t.Fatal("a grammar without source passed")
		}
		return
	}
	t.Fatal("deployment-incident.v2 mint/4 is not listed")
}

// The key checks run on a repository-anchored grammar too, and they check
// what the grammar CARRIES, not only what an entry declares. The retired
// review ref "<repo>:<review_id>" is injective as a string but its two
// components are repo_id and review_id, while git_pull_request_reviews is
// keyed (org_id, repo_id, number, review_id): one ref named a review id on
// two pull requests of one repository.
func TestSourceRowKeyCheckSeesTheRetiredReviewGrammar(t *testing.T) {
	// Declared with the table's true key: the declaration covers the ORDER
	// BY, and the carried check sees that the grammar never writes number.
	retired := producerGrammar{kind: contractsv1.ContextFabricEvidenceEntityReview, shape: "concat/2", repoAnchored: true,
		rowQuery: "pull_request_reviews.v2", keyColumns: []string{"repo_id", "number", "review_id"}, columns: []string{"repo_id", "review_id"}}
	if gaps := keyGaps(t, retired); len(gaps) != 0 {
		t.Fatalf("the honest declaration: key gaps %v, want none", gaps)
	}
	if gaps, why := carriedGaps(retired); why != "" || len(gaps) != 1 || gaps[0] != "number" {
		t.Fatalf("the retired review grammar: carried gaps %v %s, want [number]", gaps, why)
	}
	ok, why := passes(t, retired)
	if ok {
		t.Fatal("the retired review grammar passed")
	}
	t.Logf("retired review grammar refused: %s", why)
	// Declared with only what it carries: the key check sees the gap.
	carriedOnly := retired
	carriedOnly.keyColumns = []string{"repo_id", "review_id"}
	if gaps := keyGaps(t, carriedOnly); len(gaps) != 1 || gaps[0] != "number" {
		t.Fatalf("the carried-only declaration: key gaps %v, want [number]", gaps)
	}
	// Declared with no row key: refused, never passed unchecked.
	undeclared := retired
	undeclared.rowQuery, undeclared.keyColumns, undeclared.columns = "", nil, nil
	if ok, _ := passes(t, undeclared); ok {
		t.Fatal("a repository-anchored grammar with no row key declared passed unchecked")
	}
	// A bare join whose declared components disagree with its shape.
	if _, why := carriedGaps(producerGrammar{kind: contractsv1.ContextFabricEvidenceEntityReview, shape: "concat/2", keyColumns: []string{"repo_id"}, columns: []string{"repo_id"}}); why == "" {
		t.Fatal("one declared component for a two-operand join was accepted")
	}
	for _, g := range producerGrammars() {
		if g.kind != contractsv1.ContextFabricEvidenceEntityReviewV2 {
			continue
		}
		if ok, why := passes(t, g); !ok {
			t.Fatalf("review.v2 (%s) fails: %s", g.shape, why)
		}
		// A .v2 entry naming a key column its grammar does not write fails.
		g.keyColumns = append(append([]string(nil), g.keyColumns...), "source")
		if gaps, _ := carriedGaps(g); len(gaps) != 1 || gaps[0] != "source" {
			t.Fatalf("review.v2 (%s) with an uncarried key column: gaps %v", g.shape, gaps)
		}
	}
}

// The collision search sees a bare join (codex r2's seed class), and the
// escaped grammar passes it. Planted here, seen failing.
func TestSourceRowCollisionSearchSeesABareJoin(t *testing.T) {
	bare := producerGrammar{kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, shape: "concat/3"}
	if ok, _ := passes(t, bare); ok {
		t.Fatal("a bare ':' join of three ids passed")
	}
	escaped := producerGrammar{kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, shape: "mint/3", rowQuery: "work_item_dependencies.v2",
		keyColumns:    []string{"source_work_item_id", "target_work_item_id", "relation_key"},
		keyExceptions: map[string]string{"relationship_type": "relation_key"}}
	if ok, why := passes(t, escaped); !ok {
		t.Fatalf("the escaped grammar fails: %s", why)
	}
}

// TestEveryMintSiteIsInTheSweep (CHAOS-7226 r3 P3): the sweep's entries
// cover exactly the mint sites the source holds, with the shape the source
// has. A new site, a moved one, a changed id expression or one more mint in
// the same function fails here until its entry says what grammar it mints.
func TestEveryMintSiteIsInTheSweep(t *testing.T) {
	type key struct{ kind, shape, site string }
	derived := map[key]int{}
	for _, site := range derivedMintSites(t) {
		derived[key{site.kind, site.shape, site.key()}]++
	}
	listed := map[key]int{}
	for _, g := range producerGrammars() {
		name := kindConstantName(t, g.kind)
		for site, count := range g.sites {
			k := key{name, g.shape, site}
			if _, dup := listed[k]; dup {
				t.Errorf("%s %s %s is listed twice", g.kind, g.shape, site)
			}
			listed[k] = count
		}
	}
	var problems []string
	for k, count := range derived {
		switch want, ok := listed[k]; {
		case !ok:
			problems = append(problems, fmt.Sprintf("UNLISTED mint site: %s mints %s with shape %s (%d time(s)); add it to producerGrammars with the grammar it mints", k.site, k.kind, k.shape, count))
		case want != count:
			problems = append(problems, fmt.Sprintf("%s mints %s (%s) %d time(s), the sweep lists %d", k.site, k.kind, k.shape, count, want))
		}
	}
	for k := range listed {
		if _, ok := derived[k]; !ok {
			problems = append(problems, fmt.Sprintf("STALE sweep entry: %s no longer mints %s with shape %s", k.site, k.kind, k.shape))
		}
	}
	sort.Strings(problems)
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(derived) < 60 {
		t.Fatalf("the inventory derived %d sites: the walk matched too little", len(derived))
	}
}

// No Go producer mints a retired kind: the inventory holds only its packet
// catalog locators (EvidenceRefID also panics on one at run time).
func TestNoProducerMintsARetiredKind(t *testing.T) {
	retired := map[string]bool{}
	for _, kind := range contractsv1.ContextFabricEvidenceEntityTypeVocabulary() {
		if _, isRetired := contractsv1.RetiredEvidenceEntityType(kind); isRetired {
			retired[kindConstantName(t, kind)] = true
		}
	}
	for _, site := range derivedMintSites(t) {
		if retired[site.kind] && !strings.HasPrefix(site.shape, "sql/") {
			t.Errorf("%s mints the retired kind %s (%s)", site.key(), site.kind, site.shape)
		}
	}
}

// The inventory sees what it exists for. Planted here, seen failing: a new
// mint in a function it never saw, a wrapper that forwards the kind, a
// changed id shape, and an unclassified use of a kind constant.
func TestTheMintSiteInventorySeesANewSite(t *testing.T) {
	files := []string{
		`package p
import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
func planted(repo, a, b string) string {
	return contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, repo+":"+a+":"+b)
}
func wrap(kind contractsv1.ContextFabricEvidenceEntityType, id string) string { return contractsv1.EvidenceRefID(kind, id) }
func viaWrapper(a string) string { return wrap(contractsv1.ContextFabricEvidenceEntityTeam, a) }
func unknownUse() { register(contractsv1.ContextFabricEvidenceEntityTeam) }
func register(contractsv1.ContextFabricEvidenceEntityType) {}
`,
	}
	got := plantedSites(t, files[0])
	want := []string{"planted|ContextFabricEvidenceEntityWorkItemHierarchyV2|concat/3", "viaWrapper|ContextFabricEvidenceEntityTeam|concat/1"}
	if strings.Join(got.sites, ",") != strings.Join(want, ",") {
		t.Fatalf("sites %v, want %v", got.sites, want)
	}
	if len(got.unclassified) != 1 || !strings.Contains(got.unclassified[0], "unknownUse") {
		t.Fatalf("unclassified %v, want the register call", got.unclassified)
	}
}

func kindConstantName(t *testing.T, kind contractsv1.ContextFabricEvidenceEntityType) string {
	t.Helper()
	for name, value := range evidenceKindConstants(t) {
		if contractsv1.ContextFabricEvidenceEntityType(value) == kind {
			return name
		}
	}
	t.Fatalf("no constant names %s", kind)
	return ""
}
