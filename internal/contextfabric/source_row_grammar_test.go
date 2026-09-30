package contextfabric_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7226 r2 P1 class: a source-row read keyed by a ref string is correct
// only when that string names ONE row. Two properties decide it:
//
//  1. The producer's grammar is injective: no two distinct component tuples
//     serialize to the same id. Work item, team and relation ids hold ':',
//     so a grammar that joins two or more of them with ':' is not.
//  2. A row-anchored kind (its repositories come from the row, and the grant
//     filters them) is read only when its id is the table's key within the
//     organization, so the grant never chooses between distinct rows. A
//     repository-anchored kind reads inside the ONE repository its id names;
//     two rows there are refused as ambiguous.
//
// Every grammar a Context Fabric producer mints for a kind is listed with its
// producer site. A kind is on a source-row route exactly when every one of
// its grammars passes; a kind that fails stays on the persisted record.

const grammarRepoID = "20000000-0000-4000-8000-000000000002"

type producerGrammar struct {
	kind contractsv1.ContextFabricEvidenceEntityType
	site string
	// arity is how many colon-capable components the id joins.
	arity int
	// mint builds the id segment from the components, as the producer does.
	mint func(c []string) string
	// repoAnchored: the id opens with the fixed-length repository UUID.
	repoAnchored bool
	// table and keyColumns locate a row-anchored id's row; the id's
	// components must be the table's whole key within the organization
	// (ORDER BY (org_id, keyColumns...)). No table: no single-row key.
	table      string
	keyColumns []string
	// values, when set, is each component's value set (a closed, colon-free
	// provider enum); otherwise every component takes grammarComponents.
	values [][]string
}

func producerGrammars() []producerGrammar {
	repo := func(rest string) string { return grammarRepoID + ":" + rest }
	return []producerGrammar{
		{kind: contractsv1.ContextFabricEvidenceEntityRepository, site: "devhealthsource/tables.go:219", arity: 0, repoAnchored: true, mint: func([]string) string { return grammarRepoID }},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItem, site: "devhealthsource/tables.go:317", arity: 1, repoAnchored: true, mint: func(c []string) string { return repo(c[0]) }},
		{kind: contractsv1.ContextFabricEvidenceEntityPullRequest, site: "devhealthsource/tables.go:397", arity: 1, repoAnchored: true, mint: func(c []string) string { return repo(c[0]) }},
		{kind: contractsv1.ContextFabricEvidenceEntityReview, site: "devhealthsource/tables.go:998", arity: 1, repoAnchored: true, mint: func(c []string) string { return repo(c[0]) }},
		{kind: contractsv1.ContextFabricEvidenceEntityCI, site: "devhealthsource/tables.go:1076", arity: 1, repoAnchored: true, mint: func(c []string) string { return repo(c[0]) }},
		{kind: contractsv1.ContextFabricEvidenceEntityDeployment, site: "devhealthsource/tables.go:458", arity: 1, repoAnchored: true, mint: func(c []string) string { return repo(c[0]) }},
		{kind: contractsv1.ContextFabricEvidenceEntityIncident, site: "devhealthsource/tables.go:526", arity: 1, table: "operational_incidents", keyColumns: []string{"id"}, mint: func(c []string) string { return c[0] }},
		// edge_id hashes (deployment_id, incident_id) without the repository,
		// and the edge table is keyed (org_id, deployment_id, incident_id,
		// source) (devhealthschema/schema.go:662): no single-row key.
		{kind: contractsv1.ContextFabricEvidenceEntityDeploymentIncident, site: "devhealthsource/tables.go:927", arity: 1, mint: func(c []string) string { return c[0] }},
		// CHAOS-7227: a team is keyed (org_id, id); a project (org_id,
		// provider, id), its provider a closed colon-free enum split at the
		// first ':' (sourcerow providerPattern).
		{kind: contractsv1.ContextFabricEvidenceEntityTeam, site: "devhealthsource/teams_projects.go:1283", arity: 1, table: "teams", keyColumns: []string{"id"}, mint: func(c []string) string { return c[0] }},
		{kind: contractsv1.ContextFabricEvidenceEntityProject, site: "devhealthsource/teams_projects.go:1384", arity: 2, table: projectRegistration().Table, keyColumns: projectRegistration().Columns, values: [][]string{grammarProviders, grammarComponents}, mint: func(c []string) string { return c[0] + ":" + c[1] }},
		// project-team joins two colon-capable ids after the provider, and
		// team_project_ownership is keyed wider than the pair.
		{kind: contractsv1.ContextFabricEvidenceEntityProjectTeam, site: "devhealthsource/teams_projects_edges.go:1505", arity: 3, values: [][]string{grammarProviders, grammarComponents, grammarComponents}, mint: func(c []string) string { return c[0] + ":" + c[1] + ":" + c[2] }},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependency, site: "devhealthsource/tables.go:637", arity: 3, repoAnchored: true, mint: func(c []string) string { return repo(c[0] + ":" + c[1] + ":" + c[2]) }},
		// Row-anchored with NO single-row key: the id spans three columns of
		// the dependency table (and fails injectivity before that matters).
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependency, site: "devhealthfacts/dependencies.go:291", arity: 3, mint: func(c []string) string { return c[0] + ":" + c[1] + ":" + c[2] }},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy, site: "devhealthsource/tables.go:847", arity: 2, repoAnchored: true, mint: func(c []string) string { return repo(c[0] + ":" + c[1]) }},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemTeam, site: "devhealthsource/teams_projects_edges.go:624", arity: 2, repoAnchored: true, mint: func(c []string) string { return repo(c[0] + ":" + c[1]) }},
	}
}

// grammarComponents are component values with and without ':' in every
// position a real id can hold one (jira:ABC-1, ari:cloud:..., blocks:fwd).
var grammarComponents = []string{"a", "b", "a:b", "b:a", ":", "a:", ":b"}

// grammarProviders are provider values: a closed, colon-free enum.
var grammarProviders = []string{"jira", "linear", "gitlab"}

func projectRegistration() identity.Registration {
	registration, _ := identity.Lookup(identity.KindProject)
	return registration
}

// collision returns two distinct component tuples that mint the same id, or
// false when the grammar is injective over the component set.
func collision(g producerGrammar) ([]string, []string, bool) {
	seen := map[string][]string{}
	var walk func(prefix []string) ([]string, []string, bool)
	walk = func(prefix []string) ([]string, []string, bool) {
		if len(prefix) == g.arity {
			id := g.mint(prefix)
			if earlier, found := seen[id]; found {
				return earlier, append([]string(nil), prefix...), true
			}
			seen[id] = append([]string(nil), prefix...)
			return nil, nil, false
		}
		values := grammarComponents
		if g.values != nil {
			values = g.values[len(prefix)]
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

var engineOrderBy = regexp.MustCompile(`ORDER BY \(([^)]*)\)`)

// organizationKeyed reports whether table's ReplacingMergeTree key is exactly
// (org_id, columns...): one row per id within an organization.
func organizationKeyed(table string, columns []string) bool {
	match := engineOrderBy.FindStringSubmatch(devhealthschema.EngineFull[table])
	if match == nil || len(columns) == 0 {
		return false
	}
	return strings.ReplaceAll(match[1], " ", "") == "org_id,"+strings.Join(columns, ",")
}

func TestSourceRowGrammarsAreInjective(t *testing.T) {
	expandable := map[contractsv1.ContextFabricEvidenceEntityType]bool{}
	for _, g := range producerGrammars() {
		ok := true
		if a, b, found := collision(g); found {
			ok = false
			t.Logf("%s (%s) is not injective: %q and %q both mint %q", g.kind, g.site, a, b, g.mint(a))
		}
		switch {
		case g.repoAnchored:
		case g.table == "":
			ok = false
			t.Logf("%s (%s) is row-anchored with no single-row key", g.kind, g.site)
		case !organizationKeyed(g.table, g.keyColumns):
			ok = false
			t.Logf("%s (%s) is row-anchored but %s is not keyed (org_id, %v): %s", g.kind, g.site, g.table, g.keyColumns, devhealthschema.EngineFull[g.table])
		}
		if previous, seen := expandable[g.kind]; seen {
			ok = ok && previous
		}
		expandable[g.kind] = ok
	}
	plans := contextfabric.SourceRowPlans()
	for kind, plan := range plans {
		routed := plan.Route != contextfabric.SourceRowRouteRecord
		passes, listed := expandable[kind]
		switch {
		case routed && !listed:
			t.Errorf("%s is on route %s with no producer grammar listed: list it before routing it", kind, plan.Route)
		case routed && !passes:
			t.Errorf("%s is on route %s but a producer grammar fails injectivity or row-key uniqueness", kind, plan.Route)
		case !routed && listed && passes:
			t.Errorf("%s passes every check but stays on the record; route it or drop it from the list", kind)
		}
	}
	// The sweep sees the class it exists for.
	for _, kind := range []contractsv1.ContextFabricEvidenceEntityType{
		contractsv1.ContextFabricEvidenceEntityWorkItemDependency, contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy,
		contractsv1.ContextFabricEvidenceEntityWorkItemTeam, contractsv1.ContextFabricEvidenceEntityDeploymentIncident,
		contractsv1.ContextFabricEvidenceEntityProjectTeam,
	} {
		if expandable[kind] {
			t.Errorf("%s passed the sweep: the collision search or the key check no longer sees it", kind)
		}
	}
}
