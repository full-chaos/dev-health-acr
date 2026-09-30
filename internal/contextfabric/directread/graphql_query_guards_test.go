package directread_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// --------------------------------------------------------------- R2

// Lead ruling R2: the two roots served by two operations each resolve an
// admitted shape to the STRICTER operation, and every admitted shape maps
// to one pinned operation. The candidate order is pinned too: swapping it
// (the plant) turns the REPO and breakdowns-only rows red.
func TestGraphQLTwoOperationRootsResolveToTheStricterOperation(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	catalog, _ := h.policy.Root("catalog")
	analytics, _ := h.policy.Root("analytics")
	if got := catalog.Operations(); !slices.Equal(got, []string{"acrRepositoryScopes", "catalogValues"}) {
		t.Fatalf("catalog candidate order %v", got)
	}
	if got := analytics.Operations(); !slices.Equal(got, []string{"investmentBreakdown", "investmentFull"}) {
		t.Fatalf("analytics candidate order %v", got)
	}
	batch := `{breakdowns: [{dimension: THEME, measure: COUNT, dateRange: {startDate: "2026-09-01", endDate: "2026-09-28"}}]}`
	cases := []struct {
		name  string
		query string
		vars  map[string]any
		want  string // operation, or a refusal code
	}{
		{"catalog REPO literal -> the fixed-literal operation", `{ catalog(dimension: REPO) { values { value count } } }`, nil, "acrRepositoryScopes"},
		{"catalog REPO via variable", `query($d: DimensionInput!) { catalog(dimension: $d) { values { value } } }`, map[string]any{"d": "REPO"}, "acrRepositoryScopes"},
		{"catalog TEAM", `{ catalog(dimension: TEAM) { values { value } } }`, nil, "catalogValues"},
		{"catalog THEME", `{ catalog(dimension: THEME) { values { count } } }`, nil, "catalogValues"},
		{"catalog AUTHOR refused by both", `{ catalog(dimension: AUTHOR) { values { value } } }`, nil, string(directread.RefusalPersonScopeNotServed)},
		{"catalog filters argument bound by neither", `{ catalog(dimension: TEAM, filters: {}) { values { value } } }`, nil, string(directread.RefusalVariableNotAllowed)},
		{"analytics breakdowns only -> narrower allowlist", `{ analytics(batch: ` + batch + `) { breakdowns { dimension items { key value } } } }`, nil, "investmentBreakdown"},
		{"analytics evidence quality -> only Breakdown admits", `{ analytics(batch: ` + batch + `) { evidenceQualityDistribution } }`, nil, "investmentBreakdown"},
		{"analytics sankey -> only Full admits", `{ analytics(batch: ` + batch + `) { sankey { unit } } }`, nil, "investmentFull"},
		{"analytics both -> no candidate", `{ analytics(batch: ` + batch + `) { sankey { unit } evidenceQualityDistribution } }`, nil, string(directread.RefusalFieldNotAllowed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.listener.reset()
			resp := h.run(t, opUnrestricted(opOrgA), tc.query, tc.vars)
			if strings.Contains(tc.want, "_") {
				h.wantRefused(t, resp, directread.RefusalCode(tc.want))
				if strings.Contains(tc.name, "filters argument") && resp.Refusal.Path != "filters" {
					t.Fatalf("refusal path %q, want the argument name", resp.Refusal.Path)
				}
				return
			}
			h.wantServed(t, resp)
			if got := resp.RootFields[0].Operation; got != tc.want {
				t.Fatalf("mapped to %s, want %s", got, tc.want)
			}
		})
	}
	// Both catalog candidates admit REPO; the stricter one (the fixed
	// literal) has an equal output allowlist and is refused to restricted
	// callers like the other, so the mapping cannot change what is served.
	repoScopes, _ := h.policy.Catalogue().Lookup("acrRepositoryScopes")
	values, _ := h.policy.Catalogue().Lookup("catalogValues")
	for _, out := range repoScopes.Outputs {
		if !values.OutputAllowed(out.Path) {
			t.Fatalf("acrRepositoryScopes output %s is wider than catalogValues", out.Path)
		}
	}
}

// --------------------------------------------------------------- denied roots

// Every SDL Query field that is not allowed, the introspection fields, and
// every Mutation field is refused BEFORE dispatch, with zero requests. The
// lists are read from the SDL at test time.
func TestGraphQLEveryDeniedRootFieldIsRefusedBeforeDispatch(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	schema := h.policy.Schema()
	refused := h.policy.RefusedRootFields()
	if len(refused) < 20 {
		t.Fatalf("only %d refused root fields read from the SDL", len(refused))
	}
	checked := 0
	for _, field := range append(refused, "__schema", "__type", "__typename") {
		t.Run("query/"+field, func(t *testing.T) {
			h.listener.reset()
			resp := h.run(t, opUnrestricted(opOrgA), "{ "+field+" { __typename } }", nil)
			h.wantRefused(t, resp, directread.RefusalRootFieldNotAllowed)
			checked++
		})
	}
	if schema.Mutation == nil || len(schema.Mutation.Fields) == 0 {
		t.Fatal("the SDL has no mutation to plant")
	}
	for _, f := range schema.Mutation.Fields {
		t.Run("mutation/"+f.Name, func(t *testing.T) {
			h.listener.reset()
			resp := h.run(t, opUnrestricted(opOrgA), "mutation { "+f.Name+" { __typename } }", nil)
			h.wantRefused(t, resp, directread.RefusalOperationTypeNotAllowed)
			checked++
		})
	}
	if want := len(refused) + 3 + len(schema.Mutation.Fields); checked != want {
		t.Fatalf("checked %d denied roots, want %d", checked, want)
	}
}

// Planted leak per denied root field: the listener answers an allowed query
// with an extra top-level key carrying the denied field's data. The key is
// removed, the removal is ERROR-logged and counted, and no value leaves.
func TestGraphQLPlantedDeniedRootInTheAnswerIsRemoved(t *testing.T) {
	for _, field := range []string{"busFactor", "pr", "reviewEdges", "savedReports", "dataHealth"} {
		t.Run(field, func(t *testing.T) {
			h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) {
				cfg.Plant = func(data map[string]any) {
					data[field] = map[string]any{"topMaintainers": []any{map[string]any{"author": "person-leak"}}}
				}
			}})
			resp := h.run(t, opUnrestricted(opOrgA), `{ catalog(dimension: TEAM) { values { value } } }`, nil)
			h.wantServed(t, resp)
			if strings.Contains(string(resp.Data), "person-leak") || strings.Contains(string(resp.Data), field) {
				t.Fatalf("planted denied root left acr: %s", resp.Data)
			}
			if !strings.Contains(h.logs.String(), `"level":"ERROR","msg":"`+directread.GraphQLPathsRemovedLog+`"`) {
				t.Fatalf("no ERROR line for the removed key:\n%s", h.logs.String())
			}
			if h.runner.Counters().PathsRemoved == 0 {
				t.Fatal("paths_removed counter not raised")
			}
		})
	}
}

// --------------------------------------------------------------- T11

// T11 extended to free queries: mutations, subscriptions, two operations,
// a mutation under a query-like name, and an anonymous query naming a
// mutation field are all refused with zero requests.
func TestT11GraphQLOnlyOneQueryOperationIsServed(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	cases := []struct {
		name, query string
		want        directread.RefusalCode
	}{
		{"mutation", `mutation { deleteSavedReport(orgId: "o", id: "x") }`, directread.RefusalOperationTypeNotAllowed},
		{"mutation named like a query", `mutation compoundingRisk { triggerReport(orgId: "o", reportId: "x") { __typename } }`, directread.RefusalOperationTypeNotAllowed},
		{"subscription", `subscription { hotspots { __typename } }`, directread.RefusalOperationTypeNotAllowed},
		{"two operations", `query A { catalog(dimension: TEAM) { values { value } } } query B { catalog(dimension: REPO) { values { value } } }`, directread.RefusalQueryInvalid},
		{"query plus mutation", `query A { catalog(dimension: TEAM) { values { value } } } mutation B { deleteSavedReport(orgId: "o", id: "x") }`, directread.RefusalQueryInvalid},
		{"mutation field in a query", `{ triggerReport(orgId: "o", reportId: "x") { __typename } }`, directread.RefusalRootFieldNotAllowed},
		{"unparseable", `{ catalog(`, directread.RefusalQueryInvalid},
		{"empty", `   `, directread.RefusalQueryInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.listener.reset()
			h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), tc.query, nil), tc.want)
		})
	}
}

// --------------------------------------------------------------- T12

// T12 extended: for every root served to a restricted caller, every branch
// generated from its policy (t12Cases), sent as a free query, with an
// honest and a filter-ignoring listener. The forced scope on the wire is
// exactly the grant ∩ request; a foreign row refuses the whole answer.
func TestT12GraphQLForcedScopeRestrictedCallerNeverReceivesAForeignRow(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	executed := map[string]int{}
	restrictedRoots := 0
	for field, ops := range gqlRootOps(t, policy) {
		for _, op := range ops {
			scope := op.Scope(directread.CallerRestricted)
			if !scope.Served {
				continue
			}
			restrictedRoots++
			cases := t12Cases(t, op, scope)
			for _, honest := range []bool{true, false} {
				rowID := opRepoA
				if !honest {
					rowID = opRepoB
				}
				h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) { cfg.RowID = func() string { return rowID } }})
				for _, tc := range cases {
					t.Run(fmt.Sprintf("%s/%s/honest=%v", field, tc.name, honest), func(t *testing.T) {
						h.listener.reset()
						h.logs.Reset()
						vars := opMinimalVariables(t, op)
						for k, v := range tc.vars {
							if existing, ok := vars[k].(map[string]any); ok {
								if add, ok := v.(map[string]any); ok {
									for kk, vv := range add {
										existing[kk] = vv
									}
									continue
								}
							}
							vars[k] = v
						}
						q := gqlQueryFor(t, op, vars, nil, "")
						resp := h.run(t, opRestrictedA(), q.text, q.vars)
						executed[op.Name]++
						raw, _ := json.Marshal(resp)
						if strings.Contains(string(raw), opRepoB) || strings.Contains(string(raw), opRepoD) {
							t.Fatalf("a foreign repository id left acr: %s", raw)
						}
						if tc.want != "" {
							h.wantRefused(t, resp, tc.want)
							return
						}
						reqs := h.listener.requests()
						if len(reqs) != 1 {
							t.Fatalf("dispatched branch sent %d requests: %s", len(reqs), raw)
						}
						forced, ok := opLookup(reqs[0].Variables, "r0_"+scope.ForcedVariablePath)
						list, isList := forced.([]any)
						if !ok || !isList || len(list) != 1 || list[0] != opRepoA {
							t.Fatalf("forced path on the wire = %#v, want exactly [%s]", forced, opRepoA)
						}
						if honest {
							h.wantServed(t, resp)
							eff := resp.RootFields[0].EffectiveScope
							if eff == nil || !eff.ForcedByGrant || !slices.Equal(eff.RepoIDs, []string{opRepo(opRepoA)}) {
								t.Fatalf("effective scope %+v", eff)
							}
							return
						}
						if resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalRowOutsideGrant || len(resp.Data) != 0 {
							t.Fatalf("filter-ignoring listener: want row_outside_grant with zero data, got %s", raw)
						}
						if !strings.Contains(h.logs.String(), `"level":"ERROR","msg":"`+directread.GraphQLRowsForeignLog+`"`) {
							t.Fatalf("no ERROR line for the foreign row:\n%s", h.logs.String())
						}
					})
				}
			}
			if want := 2 * len(cases); executed[op.Name] != want {
				t.Fatalf("%s: executed %d T12 cases, want %d", op.Name, executed[op.Name], want)
			}
		}
	}
	if restrictedRoots != 3 {
		t.Fatalf("%d roots served to a restricted caller, want 3 (compoundingRisk, hotspots, securityAlerts)", restrictedRoots)
	}
}

// T12, free-query shapes run_operation cannot express: a second aliased
// root with a foreign id refuses the whole query before dispatch; a row
// list selected without its id gets the id added (disclosed) and checked;
// a root refused to the class is refused.
func TestT12GraphQLAliasesAddedRowIDsAndClassRefusal(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	window := `sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"`
	aliased := fmt.Sprintf(`{ a: hotspots(input: {%s, repoIds: [%q]}) { rows { filePath repoId } } b: hotspots(input: {%s, repoIds: [%q]}) { rows { filePath repoId } } }`,
		window, opRepo(opRepoA), window, opRepo(opRepoB))
	h.wantRefused(t, h.run(t, opRestrictedA(), aliased, nil), directread.RefusalDeniedOrNotFound)

	h.listener.reset()
	noID := fmt.Sprintf(`{ hotspots(input: {%s}) { rows { filePath } } }`, window)
	resp := h.run(t, opRestrictedA(), noID, nil)
	h.wantServed(t, resp)
	if got := resp.RootFields[0].AddedPaths; !slices.Equal(got, []string{"hotspots.rows[*].repoId"}) {
		t.Fatalf("added paths %v", got)
	}
	if sent := h.listener.requests()[0].Query; !strings.Contains(sent, "repoId") {
		t.Fatalf("the row id was not added to the wire query:\n%s", sent)
	}
	if !strings.Contains(string(resp.Data), opRepoA) {
		t.Fatalf("the checked row id is not disclosed: %s", resp.Data)
	}

	// A selection with no row list needs no id: nothing is added.
	h.listener.reset()
	trendOnly := `{ compoundingRisk(filter: {breakout: REPO}) { trend { day __typename } } }`
	resp = h.run(t, opRestrictedA(), trendOnly, nil)
	h.wantServed(t, resp)
	if got := resp.RootFields[0].AddedPaths; len(got) != 0 {
		t.Fatalf("added paths %v for a selection with no row list", got)
	}

	h.listener.reset()
	h.wantRefused(t, h.run(t, opRestrictedA(), `{ catalog(dimension: TEAM) { values { value } } }`, nil), directread.RefusalOperationNotServedForCaller)
	h.listener.reset()
	h.wantRefused(t, h.run(t, opRestrictedA(), `{ compoundingRisk(filter: {breakout: TEAM}) { rows { scopeId } } }`, nil), directread.RefusalOperationNotServedForCaller)
}

// --------------------------------------------------------------- T13

// T13 extended: every person variable path of every allowed root, sent as
// a GraphQL variable of the free query, is refused with zero requests; two
// inline-literal positions as well.
func TestT13GraphQLEveryPersonArgumentIsRefused(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	checked := 0
	for field, ops := range gqlRootOps(t, h.policy) {
		for _, op := range ops {
			for _, pv := range op.PersonVariables {
				rule, ok := op.Variable(pv.Path)
				if !ok {
					t.Fatalf("%s: person path %s has no rule", op.Name, pv.Path)
				}
				var leaf any = "person-x"
				if pv.Value != "" {
					leaf = pv.Value
				}
				if strings.HasPrefix(rule.Type, "[") && rule.Kind != directread.VariableKindObject {
					leaf = []any{leaf}
				}
				vars := rootVars(t, op)
				opMerge(vars, pv.Path, leaf)
				t.Run(field+"/"+op.Name+"/"+pv.Path+"="+pv.Value, func(t *testing.T) {
					h.listener.reset()
					q := gqlQueryFor(t, op, vars, nil, "")
					resp := h.run(t, opUnrestricted(opOrgA), q.text, q.vars)
					checked++
					h.wantRefused(t, resp, directread.RefusalPersonScopeNotServed)
				})
			}
		}
	}
	if checked == 0 {
		t.Fatal("no person variable was checked: T13 measured nothing")
	}
	batch := `breakdowns: [{dimension: THEME, measure: COUNT, dateRange: {startDate: "2026-09-01", endDate: "2026-09-28"}}]`
	for name, query := range map[string]string{
		"inline who.developers":  `{ analytics(batch: {` + batch + `, filters: {who: {developers: ["person-x"]}}}) { breakdowns { dimension } } }`,
		"inline scope DEVELOPER": `{ analytics(batch: {` + batch + `, filters: {scope: {level: DEVELOPER, ids: ["person-x"]}}}) { breakdowns { dimension } } }`,
	} {
		t.Run(name, func(t *testing.T) {
			h.listener.reset()
			h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), query, nil), directread.RefusalPersonScopeNotServed)
		})
	}
}

// --------------------------------------------------------------- T17

// T17 extended: busFactor's ranked persons are unreachable (root refused);
// a person-named field or a withheld field under an allowed root is
// refused before dispatch; an unselected, person-named path in the answer
// is removed with an ERROR line and no person value leaves.
func TestT17GraphQLNoPersonOutputLeavesAcr(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), `{ busFactor(orgId: "o") { topMaintainers { author sharePercent } } }`, nil), directread.RefusalRootFieldNotAllowed)

	h.listener.reset()
	evidence := `{ workGraphEdges(filters: {limit: 5}) { edges { edgeId evidence } } }`
	resp := h.run(t, opUnrestricted(opOrgA), evidence, nil)
	h.wantRefused(t, resp, directread.RefusalFieldNotAllowed)
	if !strings.Contains(resp.Refusal.Reason, "withheld") {
		t.Fatalf("reason %q does not name the withheld rule", resp.Refusal.Reason)
	}

	// A person-named field reachable in the SDL under an allowed root.
	person := personFieldUnderAllowedRoot(t, h.policy)
	h.listener.reset()
	resp = h.run(t, opUnrestricted(opOrgA), person.query, person.vars)
	h.wantRefused(t, resp, directread.RefusalFieldNotAllowed)
	if !strings.Contains(resp.Refusal.Reason, "person") {
		t.Fatalf("reason %q does not name the person rule (%s)", resp.Refusal.Reason, person.path)
	}

	// The answer carries unselected paths, one person-named.
	leaky := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) {
		cfg.Plant = func(data map[string]any) {
			root := data["hotspots"].(map[string]any)
			for _, row := range root["rows"].([]any) {
				row.(map[string]any)["authorEmail"] = "person@example.test"
				row.(map[string]any)["churnLoc30d"] = 99 // allowlisted, not selected
			}
		}
	}})
	resp = leaky.run(t, opUnrestricted(opOrgA), `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, nil)
	leaky.wantServed(t, resp)
	if strings.Contains(string(resp.Data), "person@example.test") || strings.Contains(string(resp.Data), "churnLoc30d") {
		t.Fatalf("an unselected path left acr: %s", resp.Data)
	}
	if !strings.Contains(leaky.logs.String(), `"level":"ERROR","msg":"`+directread.GraphQLPathsRemovedLog+`"`) {
		t.Fatalf("no ERROR line:\n%s", leaky.logs.String())
	}
}

type personCase struct {
	path  string
	query string
	vars  map[string]any
}

// personFieldUnderAllowedRoot finds, from the SDL, a person-named field
// reachable below an allowed root that the root's output allowlist does not
// list, and builds a query selecting it. None found = the test measured
// nothing and fails.
func personFieldUnderAllowedRoot(t *testing.T, policy *directread.GraphQLPolicy) personCase {
	t.Helper()
	schema := policy.Schema()
	for field, ops := range gqlRootOps(t, policy) {
		op := ops[0]
		def := schema.Query.Fields.ForName(field)
		var found []string
		var walk func(typeName, path string, depth int)
		walk = func(typeName, path string, depth int) {
			if depth > 3 || len(found) > 0 {
				return
			}
			typ := schema.Types[typeName]
			if typ == nil {
				return
			}
			for _, f := range typ.Fields {
				if len(f.Arguments) > 0 || strings.HasPrefix(f.Name, "__") {
					continue
				}
				child := path + "." + f.Name
				list := ""
				for tt := f.Type; tt.Elem != nil; tt = tt.Elem {
					list += "[*]"
				}
				sub := schema.Types[f.Type.Name()]
				if sub != nil && len(sub.Fields) > 0 {
					walk(f.Type.Name(), child+list, depth+1)
					continue
				}
				if directread.IsPersonNamed(f.Name) && !op.OutputAllowed(child+list) {
					found = append(found, child+list)
					return
				}
			}
		}
		rootList := ""
		for tt := def.Type; tt.Elem != nil; tt = tt.Elem {
			rootList += "[*]"
		}
		walk(def.Type.Name(), field+rootList, 1)
		if len(found) == 0 {
			continue
		}
		q := gqlQueryFor(t, op, rootVars(t, op), found, "")
		return personCase{path: found[0], query: q.text, vars: q.vars}
	}
	t.Fatal("no person-named field is reachable below an allowed root: T17 measured nothing")
	return personCase{}
}

// --------------------------------------------------------------- T18

// T18 (new, design J.6): for EACH allowed root, a generated set of queries
// (an unlisted field, an alias below the root, an alias flood, a depth over
// the limit, a mutation, a second operation, a fragment, a directive and an
// argument at a nested position that would widen scope). Each is refused
// before dispatch with zero requests. Plant (rule 2): validate the root
// field only -- every sub-root case turns red.
func TestT18GraphQLEveryRootRefusesEveryGeneratedAttack(t *testing.T) {
	limits := directread.DefaultGraphQLLimits()
	limits.MaxDepth = 1
	shallow := newGQLHarness(t, gqlHarnessOptions{limits: &limits})
	h := newGQLHarness(t, gqlHarnessOptions{})
	executed, withUnlisted := 0, 0
	for field, ops := range gqlRootOps(t, h.policy) {
		op := ops[0]
		vars := rootVars(t, op)
		base := gqlQueryFor(t, op, vars, nil, "")
		// One allowed leaf path and its parent object, from the policy.
		leaf := op.Outputs[0].Path
		leafName := leaf[strings.LastIndex(leaf, ".")+1:]
		attacks := []struct {
			name  string
			h     *gqlHarness
			query string
			want  directread.RefusalCode
		}{
			{"alias below the root", h, strings.Replace(base.text, leafName, leafName+" leak: __typename", 1), directread.RefusalFieldNotAllowed},
			{"mutation", h, strings.Replace(base.text, "query Client", "mutation Client", 1), directread.RefusalOperationTypeNotAllowed},
			{"second operation", h, base.text + " query Other { catalog(dimension: TEAM) { values { value } } }", directread.RefusalQueryInvalid},
			{"fragment", h, strings.Replace(base.text, "query Client", "fragment F on Query { __typename } query Client", 1), directread.RefusalFragmentNotAllowed},
			{"directive", h, strings.Replace(base.text, leafName, leafName+" @include(if: true)", 1), directread.RefusalDirectiveNotAllowed},
			{"nested widening argument", h, strings.Replace(base.text, leafName, leafName+`(repoIds: ["`+opRepo(opRepoB)+`"])`, 1), directread.RefusalVariableNotAllowed},
			{"alias flood", h, aliasFlood(t, op, vars, limits.MaxAliases+1), directread.RefusalQueryLimitExceeded},
			{"depth over the limit", shallow, base.text, directread.RefusalQueryLimitExceeded},
		}
		if !strings.Contains(leaf, ".") {
			t.Fatalf("%s: first output %q is not below the root", field, leaf)
		}
		if unlisted := unlistedFieldUnder(t, h.policy, field, op); unlisted != "" {
			q := gqlQueryFor(t, op, vars, append(outputPaths(op), unlisted), "")
			attacks = append(attacks, struct {
				name  string
				h     *gqlHarness
				query string
				want  directread.RefusalCode
			}{"unlisted field " + unlisted, h, q.text, directread.RefusalFieldNotAllowed})
			withUnlisted++
		}
		for _, a := range attacks {
			t.Run(field+"/"+a.name, func(t *testing.T) {
				a.h.listener.reset()
				resp := a.h.run(t, opUnrestricted(opOrgA), a.query, base.vars)
				a.h.wantRefused(t, resp, a.want)
				executed++
			})
		}
	}
	if want := 8*14 + withUnlisted; executed != want {
		t.Fatalf("executed %d T18 cases, want %d (8 per allowed root + %d unlisted-field cases)", executed, want, withUnlisted)
	}
	if withUnlisted < 6 {
		t.Fatalf("only %d of 14 roots have an unlisted SDL field to plant", withUnlisted)
	}
}

// aliasFlood repeats one root n times under distinct aliases.
func aliasFlood(t *testing.T, op *directread.OperationPolicy, vars map[string]any, n int) string {
	t.Helper()
	var parts []string
	var head string
	for i := 0; i < n; i++ {
		q := gqlQueryFor(t, op, vars, nil, fmt.Sprintf("a%d", i))
		open := strings.Index(q.text, "{")
		head = q.text[:open]
		parts = append(parts, strings.TrimSuffix(strings.TrimSpace(q.text[open+1:]), "}"))
	}
	return head + "{ " + strings.Join(parts, " ") + " }"
}

// --------------------------------------------------------------- caps

// acr's default caps are at or below GWC's listener caps (CHAOS-7085), and
// acr's derived roots are exactly the listener's allowlist.
func TestGraphQLAcrCapsAndRootsFitTheListener(t *testing.T) {
	acr := directread.DefaultGraphQLLimits()
	if acr.MaxDepth > gwcMCPLimits.MaxDepth || acr.MaxAliases > gwcMCPLimits.MaxAliases || acr.MaxFields > gwcMCPLimits.MaxComplexity {
		t.Fatalf("acr caps %+v exceed the listener's %+v (fields vs complexity)", acr, gwcMCPLimits)
	}
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	var derived []string
	for _, r := range policy.Roots() {
		derived = append(derived, r.Field)
	}
	if !slices.Equal(sortedStrings(derived), sortedStrings(gwcMCPRoots)) {
		t.Fatalf("acr roots %v, listener allowlist %v", sortedStrings(derived), sortedStrings(gwcMCPRoots))
	}
}

// acr refuses before the wire whatever the listener would refuse: with the
// listener's caps set to the SAME GraphQLLimits values, a query one over
// each cap is refused by acr with zero requests, and a query at the cap is
// served and admitted by the listener.
func TestGraphQLCapsRefuseBeforeTheListenerWould(t *testing.T) {
	strict := directread.GraphQLLimits{MaxQueryBytes: 4096, MaxDepth: 3, MaxAliases: 1, MaxRootFields: 2, MaxFields: 8, MaxComplexity: 2, MaxComputeRoots: 0}
	listener := strict
	listener.MaxComplexity = strict.MaxFields // the listener's metric is the field count
	h := newGQLHarness(t, gqlHarnessOptions{limits: &strict, fake: func(cfg *fakeMCPConfig) { cfg.Limits = listener }})
	cat := `catalog(dimension: TEAM) { values { value } }`
	cases := []struct {
		name  string
		query string
		want  directread.RefusalCode // "" = served
	}{
		{"two roots, one alias: at the caps", `{ ` + cat + ` b: ` + cat + ` }`, ""},
		{"three roots", `{ ` + cat + ` b: ` + cat + ` c: ` + cat + ` }`, directread.RefusalQueryLimitExceeded},
		{"two aliases", `{ a: ` + cat + ` b: ` + cat + ` }`, directread.RefusalQueryLimitExceeded},
		{"depth 3: at the cap", `{ catalog(dimension: TEAM) { values { value count } } }`, ""},
		{"complexity over", `{ ` + cat + ` hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, directread.RefusalQueryLimitExceeded},
		{"compute root", `{ capacityForecast(input: {teamId: "team:t1"}) { forecastId } }`, directread.RefusalQueryLimitExceeded},
		{"fields over", `{ ` + cat + ` b: catalog(dimension: REPO) { values { value count __typename } __typename } }`, directread.RefusalQueryLimitExceeded},
		{"text too long", `{ ` + cat + strings.Repeat(" ", 4100) + ` }`, directread.RefusalQueryLimitExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.listener.reset()
			resp := h.run(t, opUnrestricted(opOrgA), tc.query, nil)
			if tc.want == "" {
				h.wantServed(t, resp)
				return
			}
			h.wantRefused(t, resp, tc.want)
		})
	}
}

// --------------------------------------------------------------- misc guards

func TestGraphQLRequestShapeGuards(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	cases := []struct {
		name  string
		query string
		vars  map[string]any
		want  directread.RefusalCode
	}{
		{"alias below the root", `{ catalog(dimension: TEAM) { v: values { value } } }`, nil, directread.RefusalFieldNotAllowed},
		{"inline fragment", `{ catalog(dimension: TEAM) { ... on CatalogResult { values { value } } } }`, nil, directread.RefusalFragmentNotAllowed},
		{"operation directive", `query Q @skip(if: false) { catalog(dimension: TEAM) { values { value } } }`, nil, directread.RefusalDirectiveNotAllowed},
		{"undeclared variable in variables", `{ catalog(dimension: TEAM) { values { value } } }`, map[string]any{"extra": "x"}, directread.RefusalVariableNotAllowed},
		{"unknown field", `{ catalog(dimension: TEAM) { nope } }`, nil, directread.RefusalQueryInvalid},
		{"client orgId literal", `{ catalog(orgId: "` + opOrgB + `", dimension: TEAM) { values { value } } }`, nil, directread.RefusalVariableNotAllowed},
		{"client orgId inside input", `{ hotspots(input: {orgId: "` + opOrgB + `", sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, nil, directread.RefusalVariableNotAllowed},
		{"required root argument missing", `{ throughputForecast { forecastId } }`, nil, directread.RefusalQueryInvalid},
		{"required input field missing", `{ complexityTimeseries(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { totalScope } }`, nil, directread.RefusalQueryInvalid},
		{"window over the clamp", `{ hotspots(input: {sinceUtc: "2026-01-01T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, nil, directread.RefusalVariableOutOfRange},
		{"unknown argument name", `{ catalog(dimension: TEAM, extra: 1) { values { value } } }`, nil, directread.RefusalQueryInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.listener.reset()
			h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), tc.query, tc.vars), tc.want)
		})
	}
	// The org on the wire is the principal's, whatever the client wrote.
	h.listener.reset()
	resp := h.run(t, opUnrestricted(opOrgA), `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, nil)
	h.wantServed(t, resp)
	sent := h.listener.requests()[0]
	if org, _ := opLookup(sent.Variables, "r0_input.orgId"); org != opOrgA {
		t.Fatalf("orgId on the wire %v", org)
	}
}

func TestGraphQLBudgetAndUpstreamMapping(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	resp, err := h.runner.Run(t.Context(), opUnrestricted(opOrgA), directread.GraphQLRequest{Query: `{ catalog(dimension: TEAM) { values { value } } }`, MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalResponseBudget || resp.Refusal.MeasuredBytes <= 10 {
		t.Fatalf("budget: %+v", resp.Refusal)
	}
	for name, status := range map[string]func() (int, string){
		"graphql errors": func() (int, string) { return 200, `{"errors":[{"message":"secret upstream text"}]}` },
		"http 500":       func() (int, string) { return 500, `boom` },
		"not found":      func() (int, string) { return 404, `` },
	} {
		t.Run(name, func(t *testing.T) {
			bad := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) { cfg.Status = status }})
			resp := bad.run(t, opUnrestricted(opOrgA), `{ catalog(dimension: TEAM) { values { value } } }`, nil)
			if resp.Call == directread.CallServed || resp.Data != nil || len(resp.Errors) != 1 {
				t.Fatalf("upstream failure served: %+v", resp)
			}
			raw, _ := json.Marshal(resp)
			if strings.Contains(string(raw), "secret upstream text") {
				t.Fatal("upstream text echoed")
			}
		})
	}
}

func outputPaths(op *directread.OperationPolicy) []string {
	out := make([]string, 0, len(op.Outputs))
	for _, o := range op.Outputs {
		out = append(out, o.Path)
	}
	return out
}

// unlistedFieldUnder finds, from the SDL, a leaf field below the root that
// takes no argument and is not on the operation's output allowlist.
func unlistedFieldUnder(t *testing.T, policy *directread.GraphQLPolicy, field string, op *directread.OperationPolicy) string {
	t.Helper()
	schema := policy.Schema()
	def := schema.Query.Fields.ForName(field)
	var found string
	var walk func(typeName, path string, depth int)
	walk = func(typeName, path string, depth int) {
		typ := schema.Types[typeName]
		if typ == nil || depth > 3 || found != "" {
			return
		}
		for _, f := range typ.Fields {
			if found != "" {
				return
			}
			if len(f.Arguments) > 0 || strings.HasPrefix(f.Name, "__") {
				continue
			}
			list := ""
			for tt := f.Type; tt.Elem != nil; tt = tt.Elem {
				list += "[*]"
			}
			child := path + "." + f.Name + list
			if sub := schema.Types[f.Type.Name()]; sub != nil && len(sub.Fields) > 0 {
				walk(f.Type.Name(), child, depth+1)
				continue
			}
			if !op.OutputAllowed(child) {
				found = child
			}
		}
	}
	walk(def.Type.Name(), field, 1)
	return found
}

// Two roots of one operation capped at one call in flight per org share the
// slot: the query is served, it does not wait on itself.
func TestGraphQLTwoRootsOfOneCappedOperationDoNotWaitOnEachOther(t *testing.T) {
	limits := directread.DefaultGraphQLLimits()
	limits.MaxComputeRoots, limits.MaxComplexity = 2, 20
	h := newGQLHarness(t, gqlHarnessOptions{limits: &limits})
	start := time.Now()
	resp := h.run(t, opUnrestricted(opOrgA), `{ a: capacityForecast(input: {teamId: "team:t1"}) { forecastId } b: capacityForecast(input: {teamId: "team:t1", simulations: 100}) { forecastId } }`, nil)
	h.wantServed(t, resp)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("served after %s: the second root waited on the first one's slot", elapsed)
	}
}

// The listener's typed read-budget refusal (MCP_READ_BUDGET_EXCEEDED), at
// HTTP 200 or 4xx, with reason bytes_ceiling or time_ceiling (or anything
// else = unknown), becomes the closed refusal read_budget_exceeded with the
// closed read_budget sub-value; no upstream text is passed through.
func TestGraphQLReadBudgetRefusalIsTyped(t *testing.T) {
	for _, status := range []int{200, 400, 422} {
		for reason, want := range map[string]directread.ReadBudgetReason{
			"bytes_ceiling": directread.ReadBudgetBytes, "rows_ceiling": directread.ReadBudgetRows, "time_ceiling": directread.ReadBudgetTime,
			"": directread.ReadBudgetUnknown, "free text 5368709120": directread.ReadBudgetUnknown,
		} {
			t.Run(fmt.Sprintf("%d/%q", status, reason), func(t *testing.T) {
				h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) { cfg.ReadBudget, cfg.ReadBudgetReason = status, reason }})
				resp := h.run(t, opUnrestricted(opOrgA), `{ catalog(dimension: TEAM) { values { value } } }`, nil)
				if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalReadBudgetExceeded || resp.Data != nil {
					t.Fatalf("want read_budget_exceeded, got %+v", resp)
				}
				if resp.Refusal.ReadBudget != want {
					t.Fatalf("read_budget %q, want %q", resp.Refusal.ReadBudget, want)
				}
				raw, _ := json.Marshal(resp)
				if strings.Contains(string(raw), "5368709120") || strings.Contains(string(raw), directread.MCPReadBudgetExceededCode) {
					t.Fatalf("upstream text passed through: %s", raw)
				}
				if !strings.Contains(h.logs.String(), `"read_budget":"`+string(want)+`"`) {
					t.Fatalf("telemetry line lacks read_budget %s:\n%s", want, h.logs.String())
				}
			})
		}
	}
}

// acr's own deadline cutting the call first (a client timeout at or below
// the listener's 10 s ClickHouse limit) is upstream_timeout with the
// distinct error class acr_deadline, never a read-budget refusal.
func TestGraphQLAcrDeadlineIsADistinctErrorClass(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{clientTimeout: 200 * time.Millisecond, fake: func(cfg *fakeMCPConfig) { cfg.Delay = time.Second }})
	resp := h.run(t, opUnrestricted(opOrgA), `{ catalog(dimension: TEAM) { values { value } } }`, nil)
	if resp.Call != directread.CallUpstreamTimeout || len(resp.Errors) != 1 || resp.Errors[0].Class != directread.UpstreamAcrDeadline || resp.Refusal != nil {
		t.Fatalf("want upstream_timeout/acr_deadline, got %+v", resp)
	}
	if !strings.Contains(h.logs.String(), `"error_class":"acr_deadline"`) {
		t.Fatalf("telemetry line lacks acr_deadline:\n%s", h.logs.String())
	}
}

// Every org argument on the wire equals the header org (the listener
// refuses otherwise): orgId is set from the principal at the root and
// inside input objects, and a client org anywhere is refused before the
// wire.
func TestGraphQLOrgArgumentsOnTheWireAreThePrincipals(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	for _, q := range []string{
		`{ catalog(dimension: TEAM) { values { value } } }`,
		`{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`,
		`query($i: HotspotsInput!) { hotspots(input: $i) { rows { filePath } } }`,
	} {
		h.listener.reset()
		vars := map[string]any(nil)
		if strings.Contains(q, "$i") {
			vars = map[string]any{"i": map[string]any{"sinceUtc": "2026-09-21T00:00:00Z", "untilUtc": "2026-09-28T00:00:00Z"}}
		}
		h.wantServed(t, h.run(t, opUnrestricted(opOrgA), q, vars))
	}
	h.listener.reset()
	vars := map[string]any{"i": map[string]any{"orgId": opOrgB, "sinceUtc": "2026-09-21T00:00:00Z", "untilUtc": "2026-09-28T00:00:00Z"}}
	h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), `query($i: HotspotsInput!) { hotspots(input: $i) { rows { filePath } } }`, vars), directread.RefusalVariableNotAllowed)
}

// The listener answers an elevated claim with 403 elevated_claim; acr never
// sends one (role viewer, superuser and impersonation false), so every
// served test above passed the check. This pins the fake's own refusal.
func TestGraphQLFakeListenerRefusesElevatedClaims(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	for name, set := range map[string][2]string{
		"superuser":     {directread.HeaderInternalSuperuser, "true"},
		"impersonation": {directread.HeaderInternalImpersonationActive, "true"},
		"admin role":    {directread.HeaderInternalRole, "admin"},
		"operator role": {directread.HeaderInternalRole, "operator"},
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, h.listener.server.URL+directread.GraphQLListenerPath, strings.NewReader(`{"query":"{ __typename }"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(directread.HeaderInternalOrgID, opOrgA)
			req.Header.Set(directread.HeaderInternalRole, directread.InternalRoleLeast)
			req.Header.Set(directread.HeaderInternalSuperuser, "false")
			req.Header.Set(directread.HeaderInternalImpersonationActive, "false")
			req.Header.Set(set[0], set[1])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403 elevated_claim", resp.StatusCode)
			}
		})
	}
	if directread.InternalRoleLeast != "viewer" {
		t.Fatalf("acr sends role %q", directread.InternalRoleLeast)
	}
}

// The field cap holds on the query acr SENDS: a restricted query at the cap
// whose row id acr must add is refused before the wire.
func TestGraphQLFieldCapCountsTheRowIDsAcrAdds(t *testing.T) {
	limits := directread.DefaultGraphQLLimits()
	limits.MaxFields = 3 // hotspots, rows, filePath
	h := newGQLHarness(t, gqlHarnessOptions{limits: &limits})
	q := `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`
	h.wantServed(t, h.run(t, opUnrestricted(opOrgA), q, nil))
	h.listener.reset()
	h.wantRefused(t, h.run(t, opRestrictedA(), q, nil), directread.RefusalQueryLimitExceeded)
}

// ops PR #3425: a typed MCP listener refusal (MCP_REFUSED with a reason)
// is never passed through. A carrier reason (invalid_org, no_carrier,
// authorization_header, elevated_claim) is upstream_error / carrier_refused;
// any other (a cap, the allowlist, the org arguments) is upstream_error /
// listener_refused; both ERROR-logged. A disabled root (404
// root_field_not_enabled) is operation_unavailable.
func TestGraphQLListenerRefusalsMapToClosedClasses(t *testing.T) {
	cases := []struct {
		status int
		reason string
		call   directread.CallStatus
		class  directread.UpstreamErrorClass
	}{
		{401, "invalid_org", directread.CallUpstreamError, directread.UpstreamCarrierRefused},
		{401, "no_carrier", directread.CallUpstreamError, directread.UpstreamCarrierRefused},
		{401, "authorization_header", directread.CallUpstreamError, directread.UpstreamCarrierRefused},
		{403, "elevated_claim", directread.CallUpstreamError, directread.UpstreamCarrierRefused},
		{400, "depth_limit", directread.CallUpstreamError, directread.UpstreamListenerRefused},
		{403, "root_field_not_allowed", directread.CallUpstreamError, directread.UpstreamListenerRefused},
		{403, "invalid_org_argument", directread.CallUpstreamError, directread.UpstreamListenerRefused},
		{404, "root_field_not_enabled", directread.CallOperationUnavailable, directread.UpstreamNotFound},
		// r2 P2: the same typed envelope on a 2xx answer.
		{200, "elevated_claim", directread.CallUpstreamError, directread.UpstreamCarrierRefused},
		{200, "depth_limit", directread.CallUpstreamError, directread.UpstreamListenerRefused},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) { cfg.RefuseStatus, cfg.RefuseReason = tc.status, tc.reason }})
			resp := h.run(t, opUnrestricted(opOrgA), `{ catalog(dimension: TEAM) { values { value } } }`, nil)
			if resp.Call != tc.call || len(resp.Errors) != 1 || resp.Errors[0].Class != tc.class || resp.Data != nil {
				t.Fatalf("want %s/%s, got %+v", tc.call, tc.class, resp)
			}
			raw, _ := json.Marshal(resp)
			if strings.Contains(string(raw), tc.reason) && tc.class != directread.UpstreamNotFound {
				t.Fatalf("the listener reason passed through: %s", raw)
			}
			if tc.class != directread.UpstreamNotFound && !strings.Contains(h.logs.String(), `"level":"ERROR","msg":"`+directread.GraphQLListenerRefusedLog+`"`) {
				t.Fatalf("no ERROR line for the listener refusal:\n%s", h.logs.String())
			}
		})
	}
}

// acr never sends an empty or padded org header (the listener's 401
// invalid_org): such a principal is refused before any work, zero requests.
func TestGraphQLNeverSendsAnEmptyOrPaddedOrg(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	for _, org := range []string{"", "   ", " " + opOrgA, opOrgA + " ", opOrgA + "\t"} {
		p := opUnrestricted(opOrgA)
		p.OrgID = org
		if _, err := h.runner.Run(t.Context(), p, directread.GraphQLRequest{Query: `{ catalog(dimension: TEAM) { values { value } } }`}); !errors.Is(err, directread.ErrOperationPrincipalInvalid) {
			t.Fatalf("org %q: err %v", org, err)
		}
	}
	if n := len(h.listener.requests()); n != 0 {
		t.Fatalf("%d requests sent for an invalid org", n)
	}
}

// r1 P2: a repeated field's sub-selections are merged, not dropped, at every
// depth: both paths are sent and both come back.
func TestGraphQLRepeatedSelectionsAreMerged(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), `{ catalog(dimension: TEAM) { values { value } values { count } values { value __typename } } }`, nil)
	h.wantServed(t, resp)
	sent := h.listener.requests()[0].Query
	for _, field := range []string{"value", "count", "__typename"} {
		if !strings.Contains(sent, field) {
			t.Fatalf("the sent query dropped %s:\n%s", field, sent)
		}
	}
	if strings.Count(sent, "values") != 1 {
		t.Fatalf("the repeated field was not merged:\n%s", sent)
	}
	row := gqlDecode(t, resp.Data)["catalog"].(map[string]any)["values"].([]any)[0].(map[string]any)
	for _, field := range []string{"value", "count", "__typename"} {
		if _, ok := row[field]; !ok {
			t.Fatalf("the answer lacks %s: %v", field, row)
		}
	}
	// Two levels deep.
	h.listener.reset()
	deep := `{ securityAlerts { edges { node { alertId } } edges { node { repoId } cursor } } }`
	resp = h.run(t, opUnrestricted(opOrgA), deep, nil)
	h.wantServed(t, resp)
	if sent := h.listener.requests()[0].Query; !strings.Contains(sent, "alertId") || !strings.Contains(sent, "repoId") || !strings.Contains(sent, "cursor") {
		t.Fatalf("a nested repeated selection was dropped:\n%s", sent)
	}
}

// r1 P2-a disposition: the merge runs before every check, so a withheld or
// person-named field hidden in a repeated selection is refused before the
// wire (the checks see the merged tree), and the same response key with
// different arguments is refused by the SDL rule OverlappingFieldsCanBeMerged.
func TestGraphQLChecksSeeTheMergedTree(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), `{ workGraphEdges(filters: {limit: 5}) { edges { edgeId } edges { evidence } } }`, nil)
	h.wantRefused(t, resp, directread.RefusalFieldNotAllowed)
	if !strings.Contains(resp.Refusal.Reason, "withheld") {
		t.Fatalf("reason %q", resp.Refusal.Reason)
	}
	h.listener.reset()
	h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), `{ catalog(dimension: TEAM) { values { value } values { value } } a: catalog(dimension: TEAM) { values { value } } a: catalog(dimension: REPO) { values { value } } }`, nil), directread.RefusalQueryInvalid)
}

// r2 P2: each literal must match the SDL type of its position (the client
// rules leave out ValuesOfCorrectType so acr can set orgId): an enum as a
// string, a string as an enum, an integer as a string or a boolean as a
// string is refused before the wire; a single value in a list position is
// still accepted (GraphQL list coercion).
func TestGraphQLLiteralKindsMustMatchTheirPosition(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	window := `sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"`
	for name, q := range map[string]string{
		"enum as string":         `{ catalog(dimension: "TEAM") { values { value } } }`,
		"string as enum":         `{ hotspots(input: {sinceUtc: TODAY, untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`,
		"int as string":          `{ hotspots(input: {` + window + `, limit: "5"}) { rows { filePath } } }`,
		"enum in list as string": `{ securityAlerts(filters: {severities: ["HIGH"]}) { edges { node { alertId } } } }`,
		"bool as string":         `{ securityAlerts(filters: {openOnly: "true"}) { edges { node { alertId } } } }`,
		"default enum as string": `query($d: DimensionInput = "TEAM") { catalog(dimension: $d) { values { value } } }`,
	} {
		t.Run(name, func(t *testing.T) {
			h.listener.reset()
			h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), q, nil), directread.RefusalQueryInvalid)
		})
	}
	h.listener.reset()
	h.wantServed(t, h.run(t, opUnrestricted(opOrgA), `{ securityAlerts(filters: {severities: HIGH, openOnly: true}) { edges { node { alertId } } } }`, nil))
}

// r2 P2: a stricter candidate that admits the shape but refuses the caller
// class ends the search with its refusal; it never falls through to a less
// strict sibling served to the class. A shape only the served sibling
// admits is not ambiguous and is served by it.
func TestGraphQLStricterClassRefusalIsTerminal(t *testing.T) {
	cat := syntheticCatalogue(t, []synthSpec{
		{from: "hotspots", name: "aHotspotsServed"},
		{from: "hotspots", name: "zHotspotsStrict", edit: func(op map[string]any) {
			refuseRestricted(op)
			dropOutput("hotspots.rows[*].riskScore")(op)
		}},
	})
	policy, err := directread.NewGraphQLPolicy(cat, directread.EmbeddedOpsSchema(), directread.DefaultGraphQLLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.Roots()[0].Operations(); !slices.Equal(got, []string{"zHotspotsStrict", "aHotspotsServed"}) {
		t.Fatalf("order %v", got)
	}
	cfg := defaultFakeMCPConfig(t)
	cfg.Roots = map[string]bool{"hotspots": true}
	listener := newFakeMCPListener(t, policy, cfg)
	client, err := directread.NewHTTPGraphQLClient(listener.server.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{Policy: policy, Gate: directread.NewSubjectGate(newOpGraph(), nil), Client: client, Grants: opDefaultGrants(), Now: func() time.Time { return opNow }})
	if err != nil {
		t.Fatal(err)
	}
	window := `sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"`
	both, err := runner.Run(t.Context(), opRestrictedA(), directread.GraphQLRequest{Query: `{ hotspots(input: {` + window + `}) { rows { filePath } } }`})
	if err != nil {
		t.Fatal(err)
	}
	if both.Call != directread.CallRefused || both.Refusal.Code != directread.RefusalOperationNotServedForCaller || len(listener.requests()) != 0 {
		t.Fatalf("ambiguous shape fell through to the served sibling: %+v (%d requests)", both, len(listener.requests()))
	}
	only, err := runner.Run(t.Context(), opRestrictedA(), directread.GraphQLRequest{Query: `{ hotspots(input: {` + window + `}) { rows { filePath riskScore } } }`})
	if err != nil {
		t.Fatal(err)
	}
	if only.Call != directread.CallServed || only.RootFields[0].Operation != "aHotspotsServed" {
		t.Fatalf("a shape only the served sibling admits: %+v", only)
	}
	// An unrestricted caller is served by the stricter candidate.
	unres, err := runner.Run(t.Context(), opUnrestricted(opOrgA), directread.GraphQLRequest{Query: `{ hotspots(input: {` + window + `}) { rows { filePath } } }`})
	if err != nil || unres.Call != directread.CallServed || unres.RootFields[0].Operation != "zHotspotsStrict" {
		t.Fatalf("unrestricted: %v %+v", err, unres)
	}
}

// r2 P2 (a) on the real catalogue: both overlapping roots refuse a
// restricted caller in their stricter candidate, and that refusal is the
// answer (its own reason text), with zero upstream requests.
func TestGraphQLRealOverlapClassRefusalIsTheStricterCandidates(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	batch := `{breakdowns: [{dimension: THEME, measure: COUNT, dateRange: {startDate: "2026-09-01", endDate: "2026-09-28"}}]}`
	for _, tc := range []struct{ query, strict string }{
		{`{ catalog(dimension: REPO) { values { value } } }`, "acrRepositoryScopes"},
		{`{ analytics(batch: ` + batch + `) { breakdowns { dimension } } }`, "investmentBreakdown"},
	} {
		h.listener.reset()
		resp := h.run(t, opRestrictedA(), tc.query, nil)
		h.wantRefused(t, resp, directread.RefusalOperationNotServedForCaller)
		op, _ := h.policy.Catalogue().Lookup(tc.strict)
		if want := op.Scope(directread.CallerRestricted).Refusal.Reason; resp.Refusal.Reason != want {
			t.Fatalf("%s: refusal reason %q is not the stricter candidate's %q", tc.strict, resp.Refusal.Reason, want)
		}
	}
}
