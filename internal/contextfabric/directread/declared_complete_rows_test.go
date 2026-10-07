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

func TestDeclaredCompleteRowsRunnerUsesTheRequestLimit(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	body := opRowsAnswer("hotspots.rows[*].repoId", []any{opRepoA, opRepoA})
	for _, tc := range []struct {
		limit  int
		want   directread.Completeness
		reason directread.CompletenessReason
	}{
		{5, directread.CompletenessDeclaredComplete, ""},
		{2, directread.CompletenessDeclaredPartial, directread.ReasonBounded},
	} {
		vars := opMinimalVariables(t, hot)
		opMerge(vars, "input.limit", tc.limit)
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, body }, opHarnessOptions{})
		raw, _ := json.Marshal(vars)
		resp, err := h.runner.Run(t.Context(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "hotspots", Variables: raw})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Completeness != tc.want || resp.CompletenessReason != tc.reason {
			t.Fatalf("limit %d: completeness=%s reason=%s", tc.limit, resp.Completeness, resp.CompletenessReason)
		}
	}
}

func TestDeclaredCompleteRowsGraphQLUsesTheDocumentVariables(t *testing.T) {
	for _, tc := range []struct {
		topN   int
		items  string
		want   directread.Completeness
		reason directread.CompletenessReason
	}{
		{10, `[{"key":"a","value":1}]`, directread.CompletenessDeclaredComplete, ""},
		{1, `[{"key":"a","value":1}]`, directread.CompletenessDeclaredPartial, directread.ReasonBounded},
	} {
		h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) {
			cfg.Status = func() (int, string) {
				return 200, `{"data":{"analytics":{"breakdowns":[{"dimension":"THEME","measure":"COUNT","items":` + tc.items + `}]}}}`
			}
		}})
		op, _ := h.policy.Catalogue().Lookup("investmentBreakdown")
		vars := rootVars(t, op)
		vars["batch"] = map[string]any{"breakdowns": []any{map[string]any{
			"dimension": "THEME", "measure": "COUNT", "topN": tc.topN,
			"dateRange": map[string]any{"startDate": "2026-09-01", "endDate": "2026-09-28"},
		}}}
		q := gqlQueryFor(t, op, vars, nil, "")
		resp := h.run(t, opUnrestricted(opOrgA), q.text, q.vars)
		h.wantServed(t, resp)
		if resp.Completeness != tc.want || resp.CompletenessReason != tc.reason || len(resp.RootFields) != 1 || resp.RootFields[0].Completeness != tc.want {
			t.Fatalf("topN %d: completeness=%s reason=%s roots=%+v", tc.topN, resp.Completeness, resp.CompletenessReason, resp.RootFields)
		}
	}
}
