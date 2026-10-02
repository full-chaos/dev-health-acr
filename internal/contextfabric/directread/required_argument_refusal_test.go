package directread_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func opDeletePath(vars map[string]any, path string) {
	parts := strings.Split(path, ".")
	cur := vars
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

func TestEveryRequiredConstraintRefusalNamesItsArgument(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	checked := 0
	for _, op := range cat.Operations(directread.CallerUnrestricted) {
		for _, con := range op.Constraints {
			if con.Kind != directread.ConstraintRequired {
				continue
			}
			checked++
			t.Run(op.Name+"/"+con.Path, func(t *testing.T) {
				h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
				vars := opMinimalVariables(t, op)
				opDeletePath(vars, con.Path)
				resp := h.run(t, opUnrestricted(opOrgA), op.Name, vars)
				if resp.Refusal == nil || resp.Refusal.Code != con.Code {
					t.Fatalf("want %s, got %+v", con.Code, resp.Refusal)
				}
				if !strings.Contains(resp.Refusal.Reason, "`"+con.Path+"`") {
					t.Fatalf("reason does not name %q: %q", con.Path, resp.Refusal.Reason)
				}
				if len(h.upstream.requests()) != 0 {
					t.Fatal("refused request reached upstream")
				}
			})
		}
	}
	if checked == 0 {
		t.Fatal("no required constraint was checked")
	}
}

func TestCognitiveLoadWithoutTeamIDNamesInputTeamID(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, refusal := cat.Lookup("cognitiveLoad")
	if refusal != nil {
		t.Fatalf("cognitiveLoad is not in the catalogue: %v", refusal)
	}
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
	vars := opMinimalVariables(t, op)
	opDeletePath(vars, "input.teamId")
	resp := h.run(t, opUnrestricted(opOrgA), "cognitiveLoad", vars)
	if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalScopeRequired || !strings.Contains(resp.Refusal.Reason, "`input.teamId`") {
		t.Fatalf("want scope_required naming input.teamId, got %+v", resp.Refusal)
	}
}

func TestCognitiveLoadCatalogueResponseNamesInputTeamID(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	sections, err := directread.ParseCatalogSections(directread.CatalogSectionOperations)
	if err != nil {
		t.Fatal(err)
	}
	built := directread.BuildDataCatalog(cat, directread.CatalogCaller{PrincipalClass: directread.ClassUnrestricted, Scopes: []string{"context:read", "data:read"}, DataRead: true, OperationsServable: true, GateComposed: true}, sections)
	if built.Operations == nil {
		t.Fatal("no operations section")
	}
	for _, op := range built.Operations.Operations {
		if op.Name != "cognitiveLoad" {
			continue
		}
		for _, n := range op.Notes {
			if strings.Contains(n, "input.teamId is required") {
				return
			}
		}
		t.Fatalf("no note in the data_catalog response names input.teamId: %v", op.Notes)
	}
	t.Fatal("cognitiveLoad missing from the data_catalog response")
}
