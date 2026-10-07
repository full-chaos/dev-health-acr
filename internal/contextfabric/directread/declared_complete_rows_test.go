package directread_test

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestDeclaredCompleteRowsFullPayload(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	bodies := map[string]string{
		"investmentBreakdown": `{"data":{"analytics":{"breakdowns":[{"dimension":"THEME","measure":"COUNT","items":[{"key":"a","value":1}]}]}}}`,
		"home":                `{"data":{"home":{}}}`,
		"compoundingRisk":     `{"data":{"compoundingRisk":{"rows":[]}}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			op, rf := cat.Lookup(name)
			if rf != nil {
				t.Fatal("no op")
			}
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, body }, opHarnessOptions{})
			raw, _ := json.Marshal(opMinimalVariables(t, op))
			resp, err := h.runner.Run(t.Context(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: name, Variables: raw})
			if err != nil {
				t.Fatal(err)
			}
			out, _ := json.Marshal(resp)
			t.Logf("%s", out)
			if resp.Call != directread.CallServed || resp.Completeness != directread.CompletenessDeclaredComplete {
				t.Fatalf("want served+declared_complete, got call=%s completeness=%s", resp.Call, resp.Completeness)
			}
		})
	}
}

func TestDeclaredCompleteRowsCutPage(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	vars := opMinimalVariables(t, hot)
	h := newOpHarness(t, func(opRecorded) (int, string) {
		return 200, opRowsAnswer("hotspots.rows[*].repoId", []any{opRepoA, opRepoA, opRepoA, opRepoA})
	}, opHarnessOptions{})
	raw, _ := json.Marshal(vars)
	resp, err := h.runner.Run(t.Context(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw, MaxBytes: 120})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(resp)
	t.Logf("%s", out)
	if resp.Page.Cut == "" || resp.Completeness != directread.CompletenessDeclaredPartial {
		t.Fatalf("want cut + declared_partial, got completeness=%s cut=%v", resp.Completeness, resp.Page.Cut)
	}
}
