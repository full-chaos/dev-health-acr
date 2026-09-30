package directread_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The data_catalog schema section lists exactly what the runner admits: for
// each caller class, every listed output path of every listed root, selected
// alone in a query built from the root's policy, is served through the fake
// MCP listener; every listed argument path is an allowed client path of a
// candidate; and a root the section refuses is refused by the runner.
func TestGraphQLSchemaSectionListsWhatTheRunnerAdmits(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	for _, class := range []struct {
		name      string
		caller    directread.CallerClass
		principal storage.Principal
	}{
		{"unrestricted", directread.CallerUnrestricted, opUnrestricted(opOrgA)},
		{"restricted", directread.CallerRestricted, opRestrictedA()},
	} {
		section := directread.BuildCatalogSchema(h.policy, class.caller, true, true)
		if !section.Available {
			t.Fatalf("%s: section unavailable: %s", class.name, section.Reason)
		}
		served := 0
		for _, root := range section.Roots {
			ops := gqlRootOps(t, h.policy)[root.Field]
			for _, path := range root.OutputPaths {
				var admitted bool
				for _, op := range ops {
					if !op.OutputAllowed(path) || !op.Scope(class.caller).Served {
						continue
					}
					h.listener.reset()
					q := gqlQueryFor(t, op, rootVars(t, op), []string{path}, "")
					resp := h.run(t, class.principal, q.text, q.vars)
					if resp.Call == directread.CallServed {
						admitted = true
						served++
						break
					}
					t.Logf("%s %s via %s: %+v", class.name, path, op.Name, resp.Refusal)
				}
				if !admitted {
					t.Errorf("%s: listed path %s of %s is not admitted by the runner", class.name, path, root.Field)
				}
			}
			for _, arg := range root.Arguments {
				for _, p := range arg.Paths {
					if !argumentPathAllowed(ops, arg.Name, p.Path) {
						t.Errorf("%s: listed argument path %s of %s is not an allowed client path", class.name, p.Path, root.Field)
					}
				}
			}
		}
		if served < 20 {
			t.Fatalf("%s: only %d listed paths were served; the measurement did not happen", class.name, served)
		}
		for _, refused := range section.RefusedRootFields {
			h.listener.reset()
			resp := h.run(t, class.principal, "{ "+refused+" { __typename } }", nil)
			if resp.Call != directread.CallRefused {
				t.Errorf("%s: section-refused root %s was not refused: %s", class.name, refused, resp.Call)
			}
		}
	}
}

// argumentPathAllowed maps an argument path back to a candidate's variable
// rule (the argument binds a document variable of the same name in every
// registered document) and requires an allowed client rule.
func argumentPathAllowed(ops []*directread.OperationPolicy, arg, path string) bool {
	for _, op := range ops {
		rule, ok := op.Variable(path)
		if !ok {
			continue
		}
		if rule.Allowed && rule.Source == directread.SourceClient {
			return true
		}
	}
	return false
}
