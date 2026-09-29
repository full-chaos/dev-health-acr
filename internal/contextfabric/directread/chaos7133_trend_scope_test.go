package directread_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// CHAOS-7133: compoundingRisk.trend carries no repository id, so the row
// check cannot see it. It is safe only because the trend query follows the
// forced filter.repoIds, and because breakout TEAM (where the trend no longer
// follows repoIds) and teamIds are refused for a restricted caller. This test
// pins both halves for every restricted operation that declares
// UncheckedPaths.
func TestChaos7133_UncheckedPathsFollowTheForcedRepoBinding(t *testing.T) {
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, op := range cat.Operations(directread.CallerRestricted) {
		scope := op.Scope(directread.CallerRestricted)
		if len(scope.UncheckedPaths) == 0 {
			continue
		}
		checked++
		op, scope := op, scope
		t.Run(op.Name, func(t *testing.T) {
			// Half 1: every dispatch carries exactly the granted repository at
			// the forced path, whether the client omitted it, sent null, or
			// named it.
			for name, vars := range map[string]map[string]any{
				"absent": {},
				"null":   opBuild(scope.ForcedVariablePath, nil),
				"own":    opBuild(scope.ForcedVariablePath, []any{opRepo(opRepoA)}),
			} {
				h := newOpHarness(t, t12Answer(op, scope, true), opHarnessOptions{})
				all := opMinimalVariables(t, op)
				for k, v := range vars {
					all[k] = v
				}
				resp := h.run(t, opRestrictedA(), op.Name, all)
				reqs := h.upstream.requests()
				if resp.Call != directread.CallServed || len(reqs) != 1 {
					t.Fatalf("%s: call=%s upstream=%d", name, resp.Call, len(reqs))
				}
				forced, ok := opLookup(reqs[0].variables(), scope.ForcedVariablePath)
				list, isList := forced.([]any)
				if !ok || !isList || len(list) != 1 || list[0] != opRepoA {
					t.Fatalf("%s: forced path %s on the wire = %#v, want [%s]", name, scope.ForcedVariablePath, forced, opRepoA)
				}
			}
			// Half 2: each scope constraint that keeps the unchecked path bound
			// to the forced filter refuses its violating value, with no upstream call.
			if len(scope.Constraints) == 0 {
				t.Fatalf("%s declares unchecked paths %v with no scope constraint keeping them bound", op.Name, scope.UncheckedPaths)
			}
			ran := map[string]bool{}
			for _, tc := range t12Cases(t, op, scope) {
				if tc.want != directread.RefusalOperationNotServedForCaller {
					continue
				}
				ran[tc.name] = true
				h := newOpHarness(t, t12Answer(op, scope, true), opHarnessOptions{})
				all := opMinimalVariables(t, op)
				for k, v := range tc.vars {
					if existing, ok := all[k].(map[string]any); ok {
						if add, ok := v.(map[string]any); ok {
							for kk, vv := range add {
								existing[kk] = vv
							}
							continue
						}
					}
					all[k] = v
				}
				resp := h.run(t, opRestrictedA(), op.Name, all)
				if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != tc.want {
					t.Fatalf("%s: want refused %s, got %+v", tc.name, tc.want, resp)
				}
				if n := len(h.upstream.requests()); n != 0 {
					t.Fatalf("%s: refused branch sent %d upstream request(s)", tc.name, n)
				}
			}
			// compoundingRisk: both ways the trend could leave the forced
			// repoIds (breakout TEAM, teamIds) must have run, by name.
			if op.Name == "compoundingRisk" {
				var team, ids bool
				for name := range ran {
					team = team || strings.Contains(name, "filter.breakout=TEAM")
					ids = ids || strings.HasSuffix(name, "filter.teamIds")
				}
				if !team || !ids {
					t.Fatalf("compoundingRisk: breakout TEAM case ran=%v, teamIds case ran=%v (cases %v)", team, ids, ran)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("no restricted operation declares unchecked paths: CHAOS-7133 pin measured nothing")
	}
}
