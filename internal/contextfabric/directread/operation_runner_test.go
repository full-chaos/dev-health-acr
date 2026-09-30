package directread_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// ---------------------------------------------------------------- T12

// t12Case is one restricted-caller branch of one operation.
type t12Case struct {
	name string
	vars map[string]any
	// want: the refusal code, or "" when the call is dispatched.
	want directread.RefusalCode
}

// t12Cases generates the branches of an operation FROM ITS POLICY: the
// forced path absent, null, empty, own id, foreign id, mixed, a name in
// place of a uuid, an id of another organization, a guessed id, and one
// violating case per caller-scope constraint.
func t12Cases(t *testing.T, op *directread.OperationPolicy, scope directread.CallerScope) []t12Case {
	t.Helper()
	path := scope.ForcedVariablePath
	cases := []t12Case{
		{name: "absent", vars: map[string]any{}},
		{name: "null", vars: opBuild(path, nil)},
		{name: "empty_list", vars: opBuild(path, []any{}), want: directread.RefusalNoGrantedScope},
		{name: "own_id", vars: opBuild(path, []any{opRepo(opRepoA)})},
		{name: "foreign_id", vars: opBuild(path, []any{opRepo(opRepoB)}), want: directread.RefusalDeniedOrNotFound},
		{name: "mixed_list", vars: opBuild(path, []any{opRepo(opRepoA), opRepo(opRepoB)}), want: directread.RefusalDeniedOrNotFound},
		{name: "name_not_uuid", vars: opBuild(path, []any{"acme/a"}), want: directread.RefusalDeniedOrNotFound},
		{name: "prefixed_name", vars: opBuild(path, []any{"repository:acme/a"}), want: directread.RefusalDeniedOrNotFound},
		{name: "bare_uuid", vars: opBuild(path, []any{opRepoA}), want: directread.RefusalDeniedOrNotFound},
		{name: "other_org_id", vars: opBuild(path, []any{opRepo(opRepoD)}), want: directread.RefusalDeniedOrNotFound},
		{name: "guessed_id", vars: opBuild(path, []any{opRepo(opRepoC)}), want: directread.RefusalDeniedOrNotFound},
	}
	for i, con := range scope.Constraints {
		switch con.Kind {
		case directread.ConstraintRequiresValue:
			rule, ok := op.Variable(con.Other)
			if !ok {
				t.Fatalf("%s: scope constraint names unknown path %s", op.Name, con.Other)
			}
			var bad string
			for _, v := range rule.AllowedValues {
				if !slices.Contains(con.Values, v) {
					bad = v
					break
				}
			}
			if bad == "" {
				t.Fatalf("%s: no value outside %v to plant for %s", op.Name, con.Values, con.Other)
			}
			vars := opBuild(con.Other, bad)
			cases = append(cases, t12Case{name: fmt.Sprintf("scope_constraint_%d_%s=%s", i, con.Other, bad), vars: vars, want: con.Code})
		case directread.ConstraintEmptyOrAbsent:
			cases = append(cases, t12Case{name: fmt.Sprintf("scope_constraint_%d_%s", i, con.Path), vars: opBuild(con.Path, []any{opTeamT}), want: con.Code})
		default:
			t.Fatalf("%s: scope constraint kind %s has no generated case", op.Name, con.Kind)
		}
	}
	return cases
}

// t12Answer is a fake query service. honest=true returns one row per id in
// the forced filter (all repositories when the filter is absent); honest=
// false IGNORES the filter and always returns a granted and a foreign row.
func t12Answer(op *directread.OperationPolicy, scope directread.CallerScope, honest bool) func(rec opRecorded) (int, string) {
	return func(rec opRecorded) (int, string) {
		ids := []any{opRepoA, opRepoB}
		if honest {
			if list, ok := opLookup(rec.variables(), scope.ForcedVariablePath); ok && list != nil {
				ids, _ = list.([]any)
			}
		}
		return 200, opRowsAnswer(scope.RowIDPaths[0], ids)
	}
}

// TestT12ForcedScopeRestrictedCallerNeverReceivesAForeignRow is T12: for
// every operation served to a restricted caller and every branch generated
// from its policy, with an honest and a filter-ignoring query service.
func TestT12ForcedScopeRestrictedCallerNeverReceivesAForeignRow(t *testing.T) {
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	ops := cat.Operations(directread.CallerRestricted)
	if len(ops) == 0 {
		t.Fatal("no operation is served to a restricted caller: T12 measured nothing")
	}
	executed := map[string]int{}
	for _, op := range ops {
		scope := op.Scope(directread.CallerRestricted)
		cases := t12Cases(t, op, scope)
		for _, honest := range []bool{true, false} {
			h := newOpHarness(t, t12Answer(op, scope, honest), opHarnessOptions{})
			for _, tc := range cases {
				name := fmt.Sprintf("%s/%s/honest=%v", op.Name, tc.name, honest)
				t.Run(name, func(t *testing.T) {
					h.upstream.reset()
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
					resp := h.run(t, opRestrictedA(), op.Name, vars)
					executed[op.Name]++
					raw, _ := json.Marshal(resp)
					if strings.Contains(string(raw), opRepoB) || strings.Contains(string(raw), opRepoD) {
						t.Fatalf("a foreign repository id left acr: %s", raw)
					}
					reqs := h.upstream.requests()
					if tc.want != "" {
						if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != tc.want {
							t.Fatalf("want refused %s, got %s", tc.want, raw)
						}
						if len(reqs) != 0 {
							t.Fatalf("a refused branch sent %d upstream request(s)", len(reqs))
						}
						if tc.want == directread.RefusalDeniedOrNotFound && strings.Contains(resp.Refusal.Reason, "repository:") {
							t.Fatalf("refusal echoes an id: %s", resp.Refusal.Reason)
						}
						return
					}
					if len(reqs) != 1 {
						t.Fatalf("dispatched branch sent %d requests, want 1: %s", len(reqs), raw)
					}
					forced, ok := opLookup(reqs[0].variables(), scope.ForcedVariablePath)
					list, isList := forced.([]any)
					if !ok || !isList || len(list) != 1 || list[0] != opRepoA {
						t.Fatalf("forced path %s on the wire = %#v, want exactly [%s]", scope.ForcedVariablePath, forced, opRepoA)
					}
					if honest {
						if resp.Call != directread.CallServed {
							t.Fatalf("honest upstream: want served, got %s", raw)
						}
						if !resp.EffectiveScope.ForcedByGrant || !slices.Equal(resp.EffectiveScope.RepoIDs, []string{opRepo(opRepoA)}) {
							t.Fatalf("effective scope = %+v", resp.EffectiveScope)
						}
						return
					}
					if resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalRowOutsideGrant || len(resp.Data) != 0 {
						t.Fatalf("filter-ignoring upstream: want the whole answer refused row_outside_grant with zero data, got %s", raw)
					}
					if !strings.Contains(h.logs.String(), `"level":"ERROR","msg":"`+directread.OperationRowsForeignLog+`"`) {
						t.Fatalf("no ERROR line for the foreign row:\n%s", h.logs.String())
					}
				})
			}
		}
		if want := 2 * len(cases); executed[op.Name] != want {
			t.Fatalf("%s: executed %d T12 cases, want %d (a case did not run)", op.Name, executed[op.Name], want)
		}
	}
	for _, op := range ops {
		if executed[op.Name] == 0 {
			t.Errorf("restricted operation %s has no T12 case", op.Name)
		}
	}
}

// TestT12RowCheckClauses plants each kind of uncheckable or foreign row.
func TestT12RowCheckClauses(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, _ := cat.Lookup("hotspots")
	scope := op.Scope(directread.CallerRestricted)
	vars := opMerge(opMinimalVariables(t, op), scope.ForcedVariablePath, []any{opRepo(opRepoA)})
	for _, tc := range []struct {
		name string
		ids  []any
		want directread.CallStatus
	}{
		{"all_granted", []any{opRepoA, strings.ToUpper(opRepoA)}, directread.CallServed},
		{"missing_id", []any{opRepoA, opMissing}, directread.CallRefused},
		{"null_id", []any{opRepoA, nil}, directread.CallRefused},
		{"number_id", []any{opRepoA, json.Number("7")}, directread.CallRefused},
		{"foreign_id", []any{opRepoA, opRepoB}, directread.CallRefused},
		{"guessed_id", []any{opRepoC}, directread.CallRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opRowsAnswer(scope.RowIDPaths[0], tc.ids) }, opHarnessOptions{})
			resp := h.run(t, opRestrictedA(), op.Name, vars)
			if resp.Call != tc.want {
				raw, _ := json.Marshal(resp)
				t.Fatalf("call = %s, want %s: %s", resp.Call, tc.want, raw)
			}
			if tc.want == directread.CallRefused {
				if resp.Refusal.Code != directread.RefusalRowOutsideGrant || resp.Data != nil {
					t.Fatalf("refusal = %+v, data = %s", resp.Refusal, resp.Data)
				}
				if got := h.runner.Counters().RowsForeign; got == 0 {
					t.Fatal("rows_foreign counter not raised")
				}
			}
		})
	}
}

// TestT12RestrictedWithoutGrantPortAndEmptyIntersection covers the two
// pre-dispatch ends of the forced scope.
func TestT12RestrictedWithoutGrantPortAndEmptyIntersection(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	for _, op := range cat.Operations(directread.CallerRestricted) {
		t.Run(op.Name+"/no_grant_port", func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{noGrants: true})
			resp := h.run(t, opRestrictedA(), op.Name, opMinimalVariables(t, op))
			if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalScopeRequired || len(h.upstream.requests()) != 0 {
				t.Fatalf("want scope_required with zero upstream, got %+v (%d requests)", resp.Refusal, len(h.upstream.requests()))
			}
		})
		t.Run(op.Name+"/grant_reaches_only_foreign", func(t *testing.T) {
			foreignOnly := opGrants{refs: map[string][]contextfabric.SubjectRef{opOrgA: {repoRef(opRepoB), repoRef(opRepoC)}}}
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{grants: foreignOnly})
			resp := h.run(t, opRestrictedA(), op.Name, opMinimalVariables(t, op))
			if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalNoGrantedScope || len(h.upstream.requests()) != 0 {
				t.Fatalf("want no_granted_scope with zero upstream, got %+v (%d requests)", resp.Refusal, len(h.upstream.requests()))
			}
		})
	}
}

// ---------------------------------------------------------------- T13

// TestT13EveryPersonVariableRefusedAtTheEdge: every person variable path in
// the artifact (all operations, recursive paths) answers
// person_scope_not_served and sends nothing upstream.
func TestT13EveryPersonVariableRefusedAtTheEdge(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	checked := 0
	for _, op := range cat.Operations(directread.CallerUnrestricted) {
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
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
			vars := opMinimalVariables(t, op)
			opMerge(vars, pv.Path, leaf)
			t.Run(op.Name+"/"+pv.Path+"="+pv.Value, func(t *testing.T) {
				h.upstream.reset()
				resp := h.run(t, opUnrestricted(opOrgA), op.Name, vars)
				checked++
				if resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalPersonScopeNotServed {
					raw, _ := json.Marshal(resp)
					t.Fatalf("want person_scope_not_served, got %s", raw)
				}
				if n := len(h.upstream.requests()); n != 0 {
					t.Fatalf("%d upstream request(s) for a person variable", n)
				}
			})
		}
	}
	if checked == 0 {
		t.Fatal("no person variable was checked: T13 measured nothing")
	}
}

// ---------------------------------------------------------------- T2

// TestT2OrgIsolationEveryOperation: two organizations, all 19 operations.
// The orgId variable and X-DH-Internal-Org-Id are always the principal's
// organization; the service never sees the other one.
func TestT2OrgIsolationEveryOperation(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	ops := cat.Operations(directread.CallerUnrestricted)
	if len(ops) != 19 {
		t.Fatalf("unrestricted catalogue has %d operations, want 19", len(ops))
	}
	for _, op := range ops {
		var orgPaths []string
		for _, rule := range op.Variables {
			if rule.Source == directread.SourcePrincipalOrg {
				orgPaths = append(orgPaths, rule.Path)
			}
		}
		if len(orgPaths) == 0 {
			t.Fatalf("%s: no orgId path in the policy", op.Name)
		}
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
		for _, principal := range []struct {
			org, other string
		}{{opOrgA, opOrgB}, {opOrgB, opOrgA}} {
			vars := opMinimalVariables(t, op)
			if principal.org == opOrgB {
				// team T exists only in org A's graph.
				if needsTeam(op) {
					continue
				}
			}
			h.upstream.reset()
			resp := h.run(t, opUnrestricted(principal.org), op.Name, vars)
			reqs := h.upstream.requests()
			if resp.Call != directread.CallServed || len(reqs) != 1 {
				raw, _ := json.Marshal(resp)
				t.Fatalf("%s as %s: want one served call, got %s (%d requests)", op.Name, principal.org, raw, len(reqs))
			}
			if got := reqs[0].Header.Values("X-DH-Internal-Org-Id"); len(got) != 1 || got[0] != principal.org {
				t.Fatalf("%s: org header %v, want [%s]", op.Name, got, principal.org)
			}
			for _, p := range orgPaths {
				if got, _ := opLookup(reqs[0].variables(), p); got != principal.org {
					t.Fatalf("%s: variable %s = %v, want %s", op.Name, p, got, principal.org)
				}
			}
			if strings.Contains(string(reqs[0].Raw), principal.other) || strings.Contains(fmt.Sprint(reqs[0].Header), principal.other) {
				t.Fatalf("%s: the request of %s names %s", op.Name, principal.org, principal.other)
			}
		}
	}
}

func needsTeam(op *directread.OperationPolicy) bool {
	for _, con := range op.Constraints {
		if con.Kind == directread.ConstraintRequired {
			if rule, ok := op.Variable(con.Path); ok && rule.Subject != nil {
				return true
			}
		}
	}
	return false
}

// TestT2ClientOrgIDIsRefusedInEveryCasingAndNesting: a client orgId
// anywhere is refused, with zero upstream requests.
func TestT2ClientOrgIDIsRefusedInEveryCasingAndNesting(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	for _, op := range cat.Operations(directread.CallerUnrestricted) {
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
		parents := []string{""}
		for _, rule := range op.Variables {
			if rule.Kind == directread.VariableKindObject && rule.Allowed && rule.Source == directread.SourceClient {
				if strings.HasPrefix(rule.Type, "[") {
					parents = append(parents, rule.Path+"[*]")
					continue
				}
				parents = append(parents, rule.Path)
			}
		}
		for _, parent := range parents {
			for _, key := range []string{"orgId", "OrgId", "ORGID", "orgid", "org_id"} {
				for _, value := range []any{opOrgB, nil} {
					path := key
					if parent != "" {
						path = parent + "." + key
					}
					vars := opMerge(opMinimalVariables(t, op), path, value)
					h.upstream.reset()
					resp := h.run(t, opUnrestricted(opOrgA), op.Name, vars)
					if resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalVariableNotAllowed {
						raw, _ := json.Marshal(resp)
						t.Fatalf("%s: client %s=%v: want variable_not_allowed, got %s", op.Name, path, value, raw)
					}
					if n := len(h.upstream.requests()); n != 0 {
						t.Fatalf("%s: client %s: %d upstream request(s)", op.Name, path, n)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------- K14-A

func TestK14AInvestmentShapesAtTheEdge(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	breakdown := func(dim string) map[string]any {
		return map[string]any{"batch": map[string]any{"breakdowns": []any{map[string]any{
			"dimension": dim, "measure": "COUNT", "topN": 5,
			"dateRange": map[string]any{"startDate": "2026-09-01", "endDate": "2026-09-28"},
		}}}}
	}
	for _, name := range []string{"investmentBreakdown", "investmentFull"} {
		op, _ := cat.Lookup(name)
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
		for _, tc := range []struct {
			label string
			vars  map[string]any
			want  directread.RefusalCode
		}{
			{"dimension_TEAM", breakdown("TEAM"), directread.RefusalBasisDependentShape},
			{"dimension_REPO", breakdown("REPO"), directread.RefusalBasisDependentShape},
			{"dimension_AUTHOR", breakdown("AUTHOR"), directread.RefusalPersonScopeNotServed},
			{"dimension_THEME", breakdown("THEME"), ""},
			{"sankey_non_null", opMerge(breakdown("THEME"), "batch.sankey", map[string]any{"path": []any{"THEME"}, "measure": "COUNT"}), directread.RefusalBasisDependentShape},
			{"scope_level_TEAM", opMerge(breakdown("THEME"), "batch.filters.scope", map[string]any{"level": "TEAM", "ids": []any{"t1"}}), directread.RefusalBasisDependentShape},
			{"scope_ORG_with_ids", opMerge(breakdown("THEME"), "batch.filters.scope", map[string]any{"level": "ORG", "ids": []any{"x"}}), directread.RefusalBasisDependentShape},
			{"scope_ORG_no_ids", opMerge(breakdown("THEME"), "batch.filters.scope", map[string]any{"level": "ORG", "ids": []any{}}), ""},
			{"what_repos", opMerge(breakdown("THEME"), "batch.filters.what.repos", []any{"acme/a"}), directread.RefusalBasisDependentShape},
			{"use_investment_from_client", opMerge(breakdown("THEME"), "batch.useInvestment", false), directread.RefusalVariableNotAllowed},
		} {
			t.Run(name+"/"+tc.label, func(t *testing.T) {
				h.upstream.reset()
				resp := h.run(t, opUnrestricted(opOrgA), name, tc.vars)
				raw, _ := json.Marshal(resp)
				if tc.want == "" {
					if resp.Call != directread.CallServed || len(h.upstream.requests()) != 1 {
						t.Fatalf("want served, got %s", raw)
					}
					if got, _ := opLookup(h.upstream.requests()[0].variables(), "batch.useInvestment"); got != true {
						t.Fatalf("forced batch.useInvestment = %v, want true", got)
					}
					return
				}
				if resp.Call != directread.CallRefused || resp.Refusal.Code != tc.want || len(h.upstream.requests()) != 0 {
					t.Fatalf("want %s with zero upstream, got %s (%d requests)", tc.want, raw, len(h.upstream.requests()))
				}
				if tc.want == directread.RefusalBasisDependentShape && tc.label == "dimension_TEAM" && !strings.Contains(resp.Refusal.Reason, directread.BasisDependentShapeText) {
					t.Fatalf("reason %q lacks the K14-A text", resp.Refusal.Reason)
				}
			})
		}
	}
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, `{"data":{}}` }, opHarnessOptions{})
	for _, name := range []string{"operatingReview", "busFactor", "triggerReport", "notAnOperation"} {
		resp := h.run(t, opUnrestricted(opOrgA), name, map[string]any{})
		if resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalUnknownOperation || resp.Operation == "notAnOperation" {
			raw, _ := json.Marshal(resp)
			t.Fatalf("%s: want unknown_operation, got %s", name, raw)
		}
	}
	if n := len(h.upstream.requests()); n != 0 {
		t.Fatalf("%d upstream requests for refused operations", n)
	}
	// Restricted callers get no investment operation at all.
	op, _ := cat.Lookup("investmentBreakdown")
	h2 := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
	resp := h2.run(t, opRestrictedA(), "investmentBreakdown", breakdown("THEME"))
	if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalOperationNotServedForCaller || len(h2.upstream.requests()) != 0 {
		t.Fatalf("restricted investmentBreakdown: %+v", resp.Refusal)
	}
}

// ------------------------------------------------------ edge rule details

func TestDeepestRefusalWinsAndValuesAreNotEchoed(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, _ := cat.Lookup("investmentFull")
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
	vars := map[string]any{"batch": map[string]any{
		"filters":    map[string]any{"who": map[string]any{"developers": []any{"secret-person"}, "roles": []any{"x"}}},
		"flowMatrix": map[string]any{"dimension": "TEAM"},
	}}
	resp := h.run(t, opUnrestricted(opOrgA), op.Name, vars)
	if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalPersonScopeNotServed || resp.Refusal.Path != "batch.filters.who.developers" {
		t.Fatalf("refusal = %+v, want person_scope_not_served at batch.filters.who.developers", resp.Refusal)
	}
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "secret-person") {
		t.Fatalf("a client value is echoed: %s", raw)
	}
	resp = h.run(t, opUnrestricted(opOrgA), op.Name, map[string]any{"batch": map[string]any{"secret key": 1}})
	if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalVariableNotAllowed || resp.Refusal.Path != "" {
		t.Fatalf("unknown path refusal = %+v, want variable_not_allowed without an echoed path", resp.Refusal)
	}
	if len(h.upstream.requests()) != 0 {
		t.Fatal("refused requests reached upstream")
	}
}

func TestClampsConstraintsAndAcrSetValues(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	cases := []struct {
		op   string
		vars map[string]any
		want directread.RefusalCode
	}{
		{"securityAlerts", map[string]any{"pagination": map[string]any{"first": 201}}, directread.RefusalVariableOutOfRange},
		{"securityAlerts", map[string]any{"pagination": map[string]any{"first": 1.5}}, directread.RefusalInvalidRequest},
		{"securityAlerts", map[string]any{"filters": map[string]any{"severities": []any{"HIGH", "NOPE"}}}, directread.RefusalVariableNotAllowed},
		{"securityAlerts", map[string]any{"filters": map[string]any{"search": strings.Repeat("x", 201)}}, directread.RefusalVariableOutOfRange},
		{"securityAlerts", map[string]any{"filters": map[string]any{"since": "2026-01-01"}}, directread.RefusalVariableOutOfRange},
		{"securityAlerts", map[string]any{"filters": map[string]any{"since": "2026-09-01", "until": "2026-08-01"}}, directread.RefusalVariableOutOfRange},
		{"securityAlerts", map[string]any{"filters": map[string]any{"since": "yesterday"}}, directread.RefusalInvalidRequest},
		{"hotspots", map[string]any{}, directread.RefusalVariableOutOfRange},
		{"hotspots", map[string]any{"input": map[string]any{"teamIds": []any{opTeamT}}}, directread.RefusalVariableNotAllowed},
		{"cognitiveLoad", map[string]any{"input": map[string]any{"sinceDate": "2026-09-20", "untilDate": "2026-09-27"}}, directread.RefusalScopeRequired},
		{"capacityForecast", map[string]any{"input": map[string]any{"targetDate": "2028-01-01"}}, directread.RefusalVariableOutOfRange},
		{"capacityForecast", map[string]any{"input": map[string]any{"simulations": 10001}}, directread.RefusalVariableOutOfRange},
		{"capacityForecast", map[string]any{"input": map[string]any{"workScopeId": "w"}}, directread.RefusalVariableNotAllowed},
		{"compoundingRisk", map[string]any{"filter": map[string]any{"breakout": "REPO", "teamIds": []any{opTeamT}}}, directread.RefusalVariableNotAllowed},
		{"compoundingRisk", map[string]any{"filter": map[string]any{"repoIds": make([]any, 201)}}, directread.RefusalVariableOutOfRange},
		{"workGraphEdges", map[string]any{"filters": map[string]any{"allowScopedPartial": true}}, directread.RefusalVariableNotAllowed},
		{"catalogValues", map[string]any{"dimension": "AUTHOR"}, directread.RefusalPersonScopeNotServed},
	}
	for _, tc := range cases {
		op, _ := cat.Lookup(tc.op)
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
		resp := h.run(t, opUnrestricted(opOrgA), tc.op, tc.vars)
		if resp.Refusal == nil || resp.Refusal.Code != tc.want || len(h.upstream.requests()) != 0 {
			raw, _ := json.Marshal(resp)
			t.Errorf("%s %v: want %s with zero upstream, got %s", tc.op, tc.vars, tc.want, raw)
		}
	}
	// Default clamp and forced values on the wire.
	op, _ := cat.Lookup("workGraphEdges")
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), op.Name, map[string]any{})
	if resp.Call != directread.CallServed {
		t.Fatalf("workGraphEdges: %+v", resp.Refusal)
	}
	vars := h.upstream.requests()[0].variables()
	if got, _ := opLookup(vars, "filters.limit"); fmt.Sprint(got) != "200" {
		t.Fatalf("filters.limit = %v, want the clamp 200 in place of the SDL default 1000", got)
	}
	if got, _ := opLookup(vars, "filters.allowScopedPartial"); got != false {
		t.Fatalf("filters.allowScopedPartial = %v, want forced false", got)
	}
	// Unrestricted repository ids are gated and converted to bare uuids.
	op, _ = cat.Lookup("hotspots")
	h = newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
	vars2 := opMerge(opMinimalVariables(t, op), "input.repoIds", []any{opRepo(opRepoA), opRepo(opRepoB)})
	resp = h.run(t, opUnrestricted(opOrgA), op.Name, vars2)
	if resp.Call != directread.CallServed || resp.EffectiveScope.ForcedByGrant || len(resp.EffectiveScope.RepoIDs) != 2 {
		t.Fatalf("unrestricted scoped hotspots: %+v %+v", resp.Refusal, resp.EffectiveScope)
	}
	got, _ := opLookup(h.upstream.requests()[0].variables(), "input.repoIds")
	if fmt.Sprint(got) != fmt.Sprint([]any{opRepoA, opRepoB}) {
		t.Fatalf("input.repoIds on the wire = %v, want bare uuids", got)
	}
	gateCalls := h.graph.gateCalls()
	h.run(t, opUnrestricted(opOrgA), op.Name, opMerge(opMinimalVariables(t, op), "input.repoIds", []any{opRepo(opRepoD)}))
	if h.graph.gateCalls() != gateCalls+1 {
		t.Fatal("the gate was not called again for the second request")
	}
}

// ------------------------------------------------------------ D.7 status

func TestStatusMappingBudgetAndOutputAllowlist(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	tf, _ := cat.Lookup("throughputForecast")
	vars := opMinimalVariables(t, hot)
	for _, tc := range []struct {
		name       string
		op         *directread.OperationPolicy
		vars       map[string]any
		status     int
		body       string
		maxBytes   int
		call       directread.CallStatus
		result     directread.ResultState
		complete   directread.Completeness
		refusal    directread.RefusalCode
		errorClass directread.UpstreamErrorClass
	}{
		{name: "empty_rows", op: hot, vars: vars, status: 200, body: `{"data":{"hotspots":{"rows":[]}}}`, call: directread.CallServed, result: directread.ResultEmptyUnverified, complete: directread.CompletenessUnknown},
		{name: "rows", op: hot, vars: vars, status: 200, body: opRowsAnswer("hotspots.rows[*].repoId", []any{opRepoA}), call: directread.CallServed, result: directread.ResultData, complete: directread.CompletenessUnknown},
		{name: "declared_partial", op: tf, vars: map[string]any{}, status: 200, body: `{"data":{"throughputForecast":{"insufficientHistory":true}}}`, call: directread.CallServed, result: directread.ResultData, complete: directread.CompletenessDeclaredPartial},
		{name: "not_found", op: hot, vars: vars, status: 404, body: `SECRET-UPSTREAM not registered`, call: directread.CallOperationUnavailable, complete: directread.CompletenessUnknown, errorClass: directread.UpstreamNotFound},
		{name: "server_error", op: hot, vars: vars, status: 502, body: `SECRET-UPSTREAM stack trace`, call: directread.CallUpstreamError, complete: directread.CompletenessUnknown, errorClass: directread.UpstreamHTTPStatus},
		{name: "graphql_errors", op: hot, vars: vars, status: 200, body: `{"data":{"hotspots":{"rows":[{"repoId":"x"}]}},"errors":[{"message":"SECRET-UPSTREAM clickhouse"}]}`, call: directread.CallUpstreamError, complete: directread.CompletenessUnknown, errorClass: directread.UpstreamGraphQLErrors},
		{name: "not_json", op: hot, vars: vars, status: 200, body: `SECRET-UPSTREAM`, call: directread.CallUpstreamError, complete: directread.CompletenessUnknown, errorClass: directread.UpstreamDecode},
		{name: "over_budget", op: hot, vars: vars, status: 200, body: opRowsAnswer("hotspots.rows[*].repoId", []any{opRepoA, opRepoA, opRepoA}), maxBytes: 40, call: directread.CallRefused, complete: directread.CompletenessUnknown, refusal: directread.RefusalResponseBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return tc.status, tc.body }, opHarnessOptions{})
			raw, _ := json.Marshal(tc.vars)
			resp, err := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: tc.op.Name, Variables: raw, MaxBytes: tc.maxBytes})
			if err != nil {
				t.Fatal(err)
			}
			out, _ := json.Marshal(resp)
			if resp.Call != tc.call || resp.Result != tc.result || resp.Completeness != tc.complete {
				t.Fatalf("got %s", out)
			}
			if strings.Contains(string(out), "SECRET-UPSTREAM") || strings.Contains(string(out), "127.0.0.1") {
				t.Fatalf("upstream text or host in the response: %s", out)
			}
			if tc.refusal != "" && (resp.Refusal == nil || resp.Refusal.Code != tc.refusal || resp.Data != nil) {
				t.Fatalf("refusal: %s", out)
			}
			if tc.refusal == directread.RefusalResponseBudget && (resp.Refusal.MeasuredBytes <= tc.maxBytes || resp.Refusal.MaxBytes != tc.maxBytes) {
				t.Fatalf("budget refusal lacks the measured size: %s", out)
			}
			if tc.errorClass != "" && (len(resp.Errors) != 1 || resp.Errors[0].Class != tc.errorClass || resp.Data != nil) {
				t.Fatalf("errors: %s", out)
			}
			if resp.Consistency != "best_effort" || !resp.UntrustedContent.Untrusted || resp.Source.Path != "graphql" || resp.Source.Service != "dho query-api" || resp.Source.SchemaDigest == "" || resp.Source.DocumentDigest != tc.op.Digest {
				t.Fatalf("fixed fields: %s", out)
			}
			for _, word := range []string{"no data", "healthy", "no_data"} {
				if strings.Contains(string(out), word) {
					t.Fatalf("status says %q: %s", word, out)
				}
			}
		})
	}
	// Output allowlist: an unlisted path is removed, ERROR logged, counted.
	h := newOpHarness(t, func(opRecorded) (int, string) {
		return 200, `{"data":{"hotspots":{"rows":[{"repoId":"` + opRepoA + `","author":"SECRET-PERSON","riskScore":1}]}}}`
	}, opHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), "hotspots", vars)
	if resp.Call != directread.CallServed || strings.Contains(string(resp.Data), "SECRET-PERSON") || !strings.Contains(string(resp.Data), "riskScore") {
		t.Fatalf("filter: %s", resp.Data)
	}
	if !strings.Contains(h.logs.String(), `"level":"ERROR","msg":"`+directread.OperationPathsRemovedLog+`"`) || h.runner.Counters().PathsRemoved != 1 {
		t.Fatalf("removed path not ERROR-logged or counted:\n%s", h.logs.String())
	}
	if strings.Contains(h.logs.String(), "SECRET-PERSON") {
		t.Fatal("a removed value reached the log")
	}
	// Budget bounds.
	for _, tc := range []struct{ in, want int }{{0, 32768}, {1 << 30, 262144}, {1000, 1000}} {
		raw, _ := json.Marshal(vars)
		resp, _ := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw, MaxBytes: tc.in})
		if resp.Page.MaxBytes != tc.want {
			t.Fatalf("max_bytes %d -> %d, want %d", tc.in, resp.Page.MaxBytes, tc.want)
		}
	}
	raw, _ := json.Marshal(vars)
	resp, _ = h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw, MaxBytes: -1})
	if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalInvalidRequest {
		t.Fatalf("negative max_bytes: %+v", resp.Refusal)
	}
}

// TestTimeoutAndPerOrgConcurrency: an upstream that does not answer inside
// the client deadline is upstream_timeout; capacityForecast admits one call
// in flight per organization.
func TestTimeoutAndPerOrgConcurrency(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	block := make(chan struct{})
	h := newOpHarness(t, func(rec opRecorded) (int, string) {
		<-block
		return 200, `{"data":{"capacityForecast":null}}`
	}, opHarnessOptions{timeout: 300 * time.Millisecond})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	resp := h.run(t, opUnrestricted(opOrgA), hot.Name, opMinimalVariables(t, hot))
	if resp.Call != directread.CallUpstreamTimeout || resp.Errors[0].Class != directread.UpstreamTimeout {
		t.Fatalf("timeout: %+v", resp)
	}

	cf, _ := cat.Lookup("capacityForecast")
	if cf.MaxInFlightPerOrg != 1 {
		t.Fatalf("capacityForecast max_in_flight_per_org = %d, want 1", cf.MaxInFlightPerOrg)
	}
	release := make(chan struct{})
	var inFlight, peak int
	var mu sync.Mutex
	h2 := newOpHarness(t, func(rec opRecorded) (int, string) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return 200, `{"data":{"capacityForecast":null}}`
	}, opHarnessOptions{})
	// Registered after the harness, so it runs before the server closes: a
	// failing assertion below can never leave a handler blocked.
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	first := make(chan directread.OperationResponse, 1)
	go func() {
		resp, _ := h2.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: cf.Name, Variables: json.RawMessage(`{}`)})
		first <- resp
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(h2.upstream.requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	second, err := h2.runner.Run(ctx, opUnrestricted(opOrgA), directread.OperationRequest{Operation: cf.Name, Variables: json.RawMessage(`{}`)})
	cancel()
	if err != nil || second.Call != directread.CallUpstreamTimeout || second.Errors[0].Class != directread.UpstreamConcurrency {
		t.Fatalf("second in-flight capacityForecast of the same org: %+v %v", second, err)
	}
	// Another organization is not blocked by org A's call.
	otherDone := make(chan directread.OperationResponse, 1)
	go func() {
		resp, _ := h2.runner.Run(context.Background(), opUnrestricted(opOrgB), directread.OperationRequest{Operation: cf.Name, Variables: json.RawMessage(`{}`)})
		otherDone <- resp
	}()
	for len(h2.upstream.requests()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(h2.upstream.requests()); n != 2 {
		t.Fatalf("org B's call did not reach upstream while org A's was in flight (%d requests)", n)
	}
	releaseAll()
	if resp := <-first; resp.Call != directread.CallServed {
		t.Fatalf("first call: %+v", resp)
	}
	<-otherDone
	mu.Lock()
	defer mu.Unlock()
	if peak != 2 {
		t.Fatalf("peak in-flight = %d, want 2 (one per org)", peak)
	}
}

// ------------------------------------------------------------ telemetry

func TestOperationReadLineCertifiesAndLeaksNothing(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, _ := cat.Lookup("hotspots")
	scope := op.Scope(directread.CallerRestricted)
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opRowsAnswer(scope.RowIDPaths[0], []any{opRepoA}) }, opHarnessOptions{})
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	raw, _ := json.Marshal(opMinimalVariables(t, op))
	if _, err := h.runner.Run(ctx, opRestrictedA(), directread.OperationRequest{Operation: op.Name, Variables: raw}); err != nil {
		t.Fatal(err)
	}
	line := opLineOf(t, h.logs.String(), directread.OperationReadLogMessage)
	opCertify(t, line, map[string]any{
		"org_id": opOrgA, "operation": "hotspots", "caller_class": "restricted", "scope_class": "forced_grant",
		"decision": "served", "forced_by_grant": true, "variables_rejected": 0, "rows_checked": 1, "rows_foreign": 0,
		"paths_removed": 0, "completeness": "unknown", "result": "data", "schema_digest": cat.SchemaDigest(),
		"document_digest": op.Digest, "request_id": "req_0123456789abcdef0123456789abcdef",
	})
	h.logs.Reset()
	raw, _ = json.Marshal(opMerge(opMinimalVariables(t, op), "input.repoIds", []any{opRepo(opRepoB)}))
	if _, err := h.runner.Run(ctx, opRestrictedA(), directread.OperationRequest{Operation: "evil-name-" + opRepoB, Variables: raw}); err != nil {
		t.Fatal(err)
	}
	line = opLineOf(t, h.logs.String(), directread.OperationReadLogMessage)
	opCertify(t, line, map[string]any{"org_id": opOrgA, "operation": "unknown", "decision": "refused", "refusal_code": "unknown_operation", "scope_class": "not_reached"})
	h.logs.Reset()
	if _, err := h.runner.Run(ctx, opRestrictedA(), directread.OperationRequest{Operation: op.Name, Variables: raw}); err != nil {
		t.Fatal(err)
	}
	line = opLineOf(t, h.logs.String(), directread.OperationReadLogMessage)
	opCertify(t, line, map[string]any{"org_id": opOrgA, "decision": "refused", "refusal_code": "denied_or_not_found"})
	if strings.Contains(h.logs.String(), opRepoB) || strings.Contains(h.logs.String(), opRepoA) {
		t.Fatalf("a subject id reached the log:\n%s", h.logs.String())
	}
}

func opLineOf(t *testing.T, logs, msg string) []byte {
	t.Helper()
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, `"msg":"`+msg+`"`) {
			return []byte(line)
		}
	}
	t.Fatalf("no %q line in:\n%s", msg, logs)
	return nil
}

func opCertify(t *testing.T, line []byte, want map[string]any) {
	t.Helper()
	parsed, err := certify.Parse(line)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.OperationRead, Want: want}); err != nil {
		t.Fatalf("certify: %v\n%s", err, line)
	}
}

// TestOperationReadEventVocabulariesMatchProducer holds the literal
// vocabularies of eventspec.OperationRead equal to the producer's own.
func TestOperationReadEventVocabulariesMatchProducer(t *testing.T) {
	strs := func(values ...any) []string {
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = fmt.Sprint(v)
		}
		sort.Strings(out)
		return out
	}
	callers := directread.CallerClassVocabulary()
	scopes := directread.ScopeClassVocabulary()
	calls := directread.CallStatusVocabulary()
	comp := directread.CompletenessVocabulary()
	results := directread.ResultStateVocabulary()
	classes := directread.UpstreamErrorClassVocabulary()
	var decisions, codes, callerList, scopeList, compList, resultList, classList []any
	for _, v := range callers {
		callerList = append(callerList, v)
	}
	for _, v := range scopes {
		scopeList = append(scopeList, v)
	}
	for _, v := range calls {
		decisions = append(decisions, v)
	}
	decisions = append(decisions, directread.OperationDecisionAuthorizationUnavailable)
	for _, v := range comp {
		compList = append(compList, v)
	}
	for _, v := range results {
		resultList = append(resultList, v)
	}
	for _, v := range directread.OperationRefusalCodes() {
		codes = append(codes, v)
	}
	for _, v := range classes {
		classList = append(classList, v)
	}
	want := map[string][]string{
		"caller_class": strs(callerList...), "scope_class": strs(scopeList...), "decision": strs(decisions...),
		"completeness": strs(compList...), "result": strs(resultList...), "refusal_code": strs(codes...), "error_class": strs(classList...),
	}
	seen := 0
	for _, field := range eventspec.OperationRead.Fields {
		expected, ok := want[field.Key]
		if !ok {
			continue
		}
		seen++
		got := append([]string{}, field.ClosedVocabulary...)
		sort.Strings(got)
		if !slices.Equal(got, expected) {
			t.Errorf("eventspec %s vocabulary %v, producer %v", field.Key, got, expected)
		}
	}
	if seen != len(want) {
		t.Fatalf("checked %d of %d vocabularies", seen, len(want))
	}
}
