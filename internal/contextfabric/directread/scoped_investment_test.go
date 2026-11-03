package directread_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The team and repository investment breakdowns (CHAOS-12691) at the edge and
// in graphql_query, over the embedded catalogue (ops 0b763a85 registers their
// documents, CHAOS-12698).

func defaultCatalogueOf(t *testing.T) *directread.Catalogue {
	t.Helper()
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// scopedBreakdownVars is a THEME breakdown with the given scope; a nil scope
// leaves batch.filters out.
func scopedBreakdownVars(scope map[string]any) map[string]any {
	batch := map[string]any{"breakdowns": []any{scopedBreakdown()}}
	if scope != nil {
		batch["filters"] = map[string]any{"scope": scope}
	}
	return map[string]any{"batch": batch}
}

func scopedBreakdown() map[string]any {
	return map[string]any{
		"dimension": "THEME", "measure": "COUNT", "topN": 5,
		"dateRange": map[string]any{"startDate": "2026-09-01", "endDate": "2026-09-28"},
	}
}

type scopedEntry struct {
	name, level, otherLevel, otherEntry string
	owned, ownedBare, second            string
	outside, wrongKind                  []string
}

func scopedEntries() []scopedEntry {
	return []scopedEntry{
		{
			name: "investmentBreakdownTeam", level: "TEAM", otherLevel: "REPO", otherEntry: "investmentBreakdownRepository",
			owned: opTeamT, ownedBare: "t1", second: "team:t2",
			// t9 is in no graph; a team id is checked against the caller's
			// organization graph by the subject gate.
			outside:   []string{"team:t9"},
			wrongKind: []string{opRepo(opRepoA)},
		},
		{
			name: "investmentBreakdownRepository", level: "REPO", otherLevel: "TEAM", otherEntry: "investmentBreakdownTeam",
			owned: opRepo(opRepoA), ownedBare: opRepoA, second: opRepo(opRepoB),
			// D is org B's; C is in no graph.
			outside:   []string{opRepo(opRepoD), opRepo(opRepoC)},
			wrongKind: []string{opTeamT},
		},
	}
}

func TestScopedInvestmentEntriesAtTheEdge(t *testing.T) {
	cat := defaultCatalogueOf(t)
	orgOp, _ := cat.Lookup("investmentBreakdown")
	k14Restricted := orgOp.Scope(directread.CallerRestricted).Refusal
	if k14Restricted == nil || k14Restricted.Code != directread.RefusalOperationNotServedForCaller {
		t.Fatalf("investmentBreakdown's restricted scope is %+v, want the K14-A refusal", k14Restricted)
	}
	for _, e := range scopedEntries() {
		op, refusal := cat.Lookup(e.name)
		if refusal != nil {
			t.Fatalf("%s: %v", e.name, refusal)
		}
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
		ids := func(v ...string) []any {
			out := make([]any, len(v))
			for i, s := range v {
				out[i] = s
			}
			return out
		}
		type tc struct {
			label     string
			principal storage.Principal
			vars      map[string]any
			want      directread.RefusalCode
			reasonHas string
		}
		cases := []tc{
			{label: "one_owned_id", vars: scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned)})},
			{label: "two_ids", vars: scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned, e.second)}), want: directread.RefusalVariableOutOfRange},
			{label: "empty_ids", vars: scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids()}), want: directread.RefusalVariableOutOfRange},
			{label: "null_ids", vars: scopedBreakdownVars(map[string]any{"level": e.level, "ids": nil}), want: directread.RefusalVariableOutOfRange},
			{label: "ids_omitted", vars: scopedBreakdownVars(map[string]any{"level": e.level}), want: directread.RefusalVariableOutOfRange},
			{label: "filters_omitted", vars: scopedBreakdownVars(nil), want: directread.RefusalVariableOutOfRange},
			{label: "level_ORG", vars: scopedBreakdownVars(map[string]any{"level": "ORG", "ids": ids(e.owned)}), want: directread.RefusalVariableNotAllowed, reasonHas: "investmentBreakdown"},
			{label: "level_" + e.otherLevel, vars: scopedBreakdownVars(map[string]any{"level": e.otherLevel, "ids": ids(e.owned)}), want: directread.RefusalVariableNotAllowed, reasonHas: e.otherEntry},
			{label: "level_absent", vars: scopedBreakdownVars(map[string]any{"ids": ids(e.owned)}), want: directread.RefusalVariableNotAllowed, reasonHas: "SDL default ORG"},
			{label: "level_null", vars: scopedBreakdownVars(map[string]any{"level": nil, "ids": ids(e.owned)}), want: directread.RefusalVariableNotAllowed, reasonHas: "SDL default ORG"},
			{label: "two_breakdowns", vars: opMerge(scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned)}), "batch.breakdowns", []any{scopedBreakdown(), scopedBreakdown()}), want: directread.RefusalVariableOutOfRange},
			{label: "dimension_TEAM", vars: opMerge(scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned)}), "batch.breakdowns", []any{map[string]any{"dimension": "TEAM", "measure": "COUNT", "dateRange": map[string]any{"startDate": "2026-09-01", "endDate": "2026-09-28"}}}), want: directread.RefusalBasisDependentShape},
			{label: "sankey", vars: opMerge(scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned)}), "batch.sankey", map[string]any{"path": []any{"THEME"}, "measure": "COUNT"}), want: directread.RefusalBasisDependentShape},
			{label: "what_repos", vars: opMerge(scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned)}), "batch.filters.what.repos", []any{"acme/a"}), want: directread.RefusalBasisDependentShape},
			{label: "use_investment_from_client", vars: opMerge(scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned)}), "batch.useInvestment", false), want: directread.RefusalVariableNotAllowed},
			{label: "restricted_caller", principal: opRestrictedA(), vars: scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(e.owned)}), want: directread.RefusalOperationNotServedForCaller, reasonHas: k14Restricted.Reason},
		}
		for i, id := range e.outside {
			cases = append(cases, tc{label: "outside_grant_" + string(rune('a'+i)), vars: scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(id)}), want: directread.RefusalDeniedOrNotFound})
		}
		for i, id := range e.wrongKind {
			cases = append(cases, tc{label: "wrong_kind_" + string(rune('a'+i)), vars: scopedBreakdownVars(map[string]any{"level": e.level, "ids": ids(id)}), want: directread.RefusalDeniedOrNotFound})
		}
		for _, c := range cases {
			t.Run(e.name+"/"+c.label, func(t *testing.T) {
				h.upstream.reset()
				principal := c.principal
				if principal.OrgID == "" {
					principal = opUnrestricted(opOrgA)
				}
				resp := h.run(t, principal, e.name, c.vars)
				raw, _ := json.Marshal(resp)
				if c.want == "" {
					if resp.Call != directread.CallServed || len(h.upstream.requests()) != 1 {
						t.Fatalf("want served, got %s", raw)
					}
					sent := h.upstream.requests()[0].variables()
					if got, _ := opLookup(sent, "batch.useInvestment"); got != true {
						t.Fatalf("forced batch.useInvestment = %v, want true", got)
					}
					if got, _ := opLookup(sent, "batch.filters.scope.level"); got != e.level {
						t.Fatalf("sent scope level %v, want %s", got, e.level)
					}
					if got, _ := opLookup(sent, "batch.filters.scope.ids"); !jsonEqual(got, []any{e.ownedBare}) {
						t.Fatalf("sent scope ids %v, want [%s] (the ops form)", got, e.ownedBare)
					}
					return
				}
				if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != c.want || len(h.upstream.requests()) != 0 {
					t.Fatalf("want %s with zero upstream, got %s (%d requests)", c.want, raw, len(h.upstream.requests()))
				}
				if c.reasonHas != "" && !strings.Contains(resp.Refusal.Reason, c.reasonHas) {
					t.Fatalf("reason %q lacks %q", resp.Refusal.Reason, c.reasonHas)
				}
			})
		}
	}
}

func jsonEqual(a, b any) bool {
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ra) == string(rb)
}

// With the scoped entries served, the ORG entries still serve ORG only and
// name the scoped entry in their TEAM and REPO refusals.
func TestOrgInvestmentEntriesNameTheScopedEntries(t *testing.T) {
	cat := defaultCatalogueOf(t)
	for _, name := range []string{"investmentBreakdown", "investmentFull"} {
		op, refusal := cat.Lookup(name)
		if refusal != nil {
			t.Fatalf("%s: %v", name, refusal)
		}
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
		for _, c := range []struct {
			label string
			scope map[string]any
			want  directread.RefusalCode
			has   string
		}{
			{"ORG", map[string]any{"level": "ORG", "ids": []any{}}, "", ""},
			{"absent", nil, "", ""},
			{"TEAM", map[string]any{"level": "TEAM", "ids": []any{opTeamT}}, directread.RefusalBasisDependentShape, "investmentBreakdownTeam"},
			{"REPO", map[string]any{"level": "REPO", "ids": []any{opRepo(opRepoA)}}, directread.RefusalBasisDependentShape, "investmentBreakdownRepository"},
			{"ORG_with_ids", map[string]any{"level": "ORG", "ids": []any{opTeamT}}, directread.RefusalBasisDependentShape, ""},
		} {
			t.Run(name+"/"+c.label, func(t *testing.T) {
				h.upstream.reset()
				resp := h.run(t, opUnrestricted(opOrgA), name, scopedBreakdownVars(c.scope))
				raw, _ := json.Marshal(resp)
				if c.want == "" {
					if resp.Call != directread.CallServed {
						t.Fatalf("want served, got %s", raw)
					}
					return
				}
				if resp.Call != directread.CallRefused || resp.Refusal.Code != c.want || !strings.Contains(resp.Refusal.Reason, c.has) || len(h.upstream.requests()) != 0 {
					t.Fatalf("want %s naming %q, got %s", c.want, c.has, raw)
				}
				if !strings.Contains(resp.Refusal.Reason, directread.BasisDependentShapeText) {
					t.Fatalf("reason %q lacks the K14-A text", resp.Refusal.Reason)
				}
			})
		}
	}
}

// graphql_query maps an analytics query to the strictest candidate that
// admits it: ORG to investmentBreakdown, one owned team to
// investmentBreakdownTeam, one repository to investmentBreakdownRepository.
func TestGraphQLAnalyticsMapsTheScopeToItsEntry(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	breakdowns := `breakdowns: [{dimension: THEME, measure: COUNT, dateRange: {startDate: "2026-09-01", endDate: "2026-09-28"}}]`
	query := func(filters string) string {
		batch := "{" + breakdowns
		if filters != "" {
			batch += ", filters: " + filters
		}
		return `{ analytics(batch: ` + batch + `}) { breakdowns { dimension items { key value } } } }`
	}
	for _, c := range []struct {
		label, filters, want, sentIDs string
	}{
		{"no_scope", "", "investmentBreakdown", ""},
		{"ORG", `{scope: {level: ORG, ids: []}}`, "investmentBreakdown", ""},
		{"TEAM", `{scope: {level: TEAM, ids: ["` + opTeamT + `"]}}`, "investmentBreakdownTeam", `["t1"]`},
		{"REPO", `{scope: {level: REPO, ids: ["` + opRepo(opRepoA) + `"]}}`, "investmentBreakdownRepository", `["` + opRepoA + `"]`},
	} {
		t.Run(c.label, func(t *testing.T) {
			h.listener.reset()
			resp := h.run(t, opUnrestricted(opOrgA), query(c.filters), nil)
			h.wantServed(t, resp)
			if got := resp.RootFields[0].Operation; got != c.want {
				t.Fatalf("mapped to %s, want %s", got, c.want)
			}
			if c.sentIDs != "" {
				sent := h.listener.requests()[0]
				if !strings.Contains(sent.Query, c.sentIDs) && !strings.Contains(mustJSON(t, sent.Variables), c.sentIDs) {
					t.Fatalf("the query sent does not carry the ops ids %s: %s %v", c.sentIDs, sent.Query, sent.Variables)
				}
			}
		})
	}
	for _, c := range []struct {
		label, filters string
		principal      storage.Principal
		want           directread.RefusalCode
	}{
		{"TEAM_outside_grant", `{scope: {level: TEAM, ids: ["team:t9"]}}`, opUnrestricted(opOrgA), directread.RefusalDeniedOrNotFound},
		{"REPO_other_org", `{scope: {level: REPO, ids: ["` + opRepo(opRepoD) + `"]}}`, opUnrestricted(opOrgA), directread.RefusalDeniedOrNotFound},
		{"TEAM_two_ids", `{scope: {level: TEAM, ids: ["` + opTeamT + `", "team:t2"]}}`, opUnrestricted(opOrgA), ""},
		{"TEAM_empty_ids", `{scope: {level: TEAM, ids: []}}`, opUnrestricted(opOrgA), ""},
		{"TEAM_ids_omitted", `{scope: {level: TEAM}}`, opUnrestricted(opOrgA), ""},
		{"TEAM_restricted", `{scope: {level: TEAM, ids: ["` + opTeamT + `"]}}`, opRestrictedA(), directread.RefusalOperationNotServedForCaller},
		{"REPO_restricted", `{scope: {level: REPO, ids: ["` + opRepo(opRepoA) + `"]}}`, opRestrictedA(), directread.RefusalOperationNotServedForCaller},
	} {
		t.Run(c.label, func(t *testing.T) {
			h.listener.reset()
			resp := h.run(t, c.principal, query(c.filters), nil)
			if c.want == "" {
				if resp.Call != directread.CallRefused || len(h.listener.requests()) != 0 {
					t.Fatalf("want refused with zero upstream, got call=%s refusal=%+v", resp.Call, resp.Refusal)
				}
				return
			}
			h.wantRefused(t, resp, c.want)
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The loader admits MinItems only where the edge can apply it, an omitted
// list included: a client list outside any list, at most MaxItems, with an
// empty or absent SDL default.
func TestLoaderHoldsMinItemsToWhatTheEdgeApplies(t *testing.T) {
	raw := directread.EmbeddedCatalogueJSON()
	edit := func(t *testing.T, path string, change func(rule map[string]any)) []byte {
		t.Helper()
		var file map[string]any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&file); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, o := range file["operations"].([]any) {
			op := o.(map[string]any)
			if op["name"] != "investmentBreakdownTeam" {
				continue
			}
			for _, v := range op["variables"].([]any) {
				rule := v.(map[string]any)
				if rule["path"] == path {
					change(rule)
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("no rule %s", path)
		}
		out, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := directread.LoadCatalogue(edit(t, "batch.filters.scope.ids", func(map[string]any) {})); err != nil {
		t.Fatalf("the unedited fixture does not load: %v", err)
	}
	for _, c := range []struct {
		label, path string
		change      func(rule map[string]any)
	}{
		{"negative", "batch.filters.scope.ids", func(r map[string]any) { r["min_items"] = -1 }},
		{"above_max_items", "batch.filters.scope.ids", func(r map[string]any) { r["min_items"] = 2 }},
		{"non_empty_default", "batch.filters.scope.ids", func(r map[string]any) { r["default"] = `["t1"]` }},
		{"not_a_list", "batch.filters.scope.level", func(r map[string]any) { r["min_items"] = 1 }},
		{"inside_a_list", "batch.breakdowns[*].keys", func(r map[string]any) {
			r["allowed"], r["source"], r["min_items"] = true, "client", 1
			delete(r, "refusal")
		}},
		{"not_client_set", "orgId", func(r map[string]any) { r["type"], r["min_items"] = "[String!]", 1 }},
	} {
		t.Run(c.label, func(t *testing.T) {
			_, err := directread.LoadCatalogue(edit(t, c.path, c.change))
			if err == nil || !strings.Contains(err.Error(), "min_items") {
				t.Fatalf("LoadCatalogue admitted %s: %v", c.label, err)
			}
		})
	}
}

// data_catalog advertises each scoped entry's id rule: exactly one id of its
// subject kind.
func TestDataCatalogAdvertisesTheScopedIDRule(t *testing.T) {
	sections, err := directread.ParseCatalogSections("operations")
	if err != nil {
		t.Fatal(err)
	}
	catalog := directread.BuildDataCatalog(defaultCatalogueOf(t), directread.CatalogCaller{
		PrincipalClass: directread.ClassUnrestricted, Scopes: []string{"context:read"}, DataRead: true, OperationsServable: true, GateComposed: true,
	}, sections)
	want := map[string]string{"investmentBreakdownTeam": directread.SubjectKindTeam, "investmentBreakdownRepository": directread.SubjectKindRepository}
	for _, op := range catalog.Operations.Operations {
		kind, ok := want[op.Name]
		if !ok {
			continue
		}
		delete(want, op.Name)
		var ids *directread.CatalogVariable
		for i, v := range op.Variables {
			if v.Path == "batch.filters.scope.ids" {
				ids = &op.Variables[i]
			}
		}
		if ids == nil || ids.MinItems != 1 || ids.MaxItems != 1 || ids.SubjectKind != kind {
			t.Fatalf("%s advertises ids %+v, want min 1, max 1, kind %s", op.Name, ids, kind)
		}
		raw, _ := json.Marshal(ids)
		if !strings.Contains(string(raw), `"min_items":1`) {
			t.Fatalf("%s: min_items not on the wire: %s", op.Name, raw)
		}
	}
	if len(want) != 0 {
		t.Fatalf("data_catalog does not list %v", want)
	}
}

// ISSUE_SIZE is a breakdown measure of the scoped entries only (lead ruling,
// CHAOS-12691): investmentBreakdownTeam and investmentBreakdownRepository
// serve it, the ORG entries refuse it, and CHURN_LOC stays refused everywhere.
// graphql_query maps an ISSUE_SIZE query with one owned team to the team
// entry and refuses it at ORG scope.
func TestIssueSizeIsServedByTheScopedEntriesOnly(t *testing.T) {
	cat := defaultCatalogueOf(t)
	withMeasure := func(scope map[string]any, measure string) map[string]any {
		breakdown := scopedBreakdown()
		breakdown["measure"] = measure
		return opMerge(scopedBreakdownVars(scope), "batch.breakdowns", []any{breakdown})
	}
	for _, c := range []struct {
		operation, measure string
		scope              map[string]any
		want               directread.RefusalCode
	}{
		{"investmentBreakdownTeam", "ISSUE_SIZE", map[string]any{"level": "TEAM", "ids": []any{opTeamT}}, ""},
		{"investmentBreakdownRepository", "ISSUE_SIZE", map[string]any{"level": "REPO", "ids": []any{opRepo(opRepoA)}}, ""},
		{"investmentBreakdownTeam", "CHURN_LOC", map[string]any{"level": "TEAM", "ids": []any{opTeamT}}, directread.RefusalVariableNotAllowed},
		{"investmentBreakdownRepository", "CHURN_LOC", map[string]any{"level": "REPO", "ids": []any{opRepo(opRepoA)}}, directread.RefusalVariableNotAllowed},
		{"investmentBreakdown", "ISSUE_SIZE", nil, directread.RefusalVariableNotAllowed},
		{"investmentFull", "ISSUE_SIZE", nil, directread.RefusalVariableNotAllowed},
		{"investmentBreakdown", "COUNT", nil, ""},
	} {
		t.Run(c.operation+"/"+c.measure, func(t *testing.T) {
			op, refusal := cat.Lookup(c.operation)
			if refusal != nil {
				t.Fatalf("%s: %v", c.operation, refusal)
			}
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), c.operation, withMeasure(c.scope, c.measure))
			raw, _ := json.Marshal(resp)
			if c.want == "" {
				if resp.Call != directread.CallServed || len(h.upstream.requests()) != 1 {
					t.Fatalf("want served, got %s", raw)
				}
				sent := h.upstream.requests()[0].variables()
				breakdowns, _ := opLookup(sent, "batch.breakdowns")
				if !jsonEqual(breakdowns.([]any)[0].(map[string]any)["measure"], c.measure) {
					t.Fatalf("sent breakdowns %v, want measure %s", breakdowns, c.measure)
				}
				return
			}
			if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != c.want || len(h.upstream.requests()) != 0 {
				t.Fatalf("want %s with zero upstream, got %s", c.want, raw)
			}
		})
	}

	h := newGQLHarness(t, gqlHarnessOptions{})
	query := func(scope string) string {
		batch := `{breakdowns: [{dimension: THEME, measure: ISSUE_SIZE, dateRange: {startDate: "2026-09-01", endDate: "2026-09-28"}}]`
		if scope != "" {
			batch += `, filters: {scope: ` + scope + `}`
		}
		return `{ analytics(batch: ` + batch + `}) { breakdowns { dimension measure items { key value } } } }`
	}
	h.listener.reset()
	resp := h.run(t, opUnrestricted(opOrgA), query(`{level: TEAM, ids: ["`+opTeamT+`"]}`), nil)
	h.wantServed(t, resp)
	if got := resp.RootFields[0].Operation; got != "investmentBreakdownTeam" {
		t.Fatalf("an ISSUE_SIZE team query mapped to %s", got)
	}
	h.listener.reset()
	h.wantRefused(t, h.run(t, opUnrestricted(opOrgA), query(""), nil), directread.RefusalVariableNotAllowed)
}
