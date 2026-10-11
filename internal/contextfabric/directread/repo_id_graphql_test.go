package directread_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestGraphQLCompoundingRiskScopeIDFormFollowsTheRequestBreakout(t *testing.T) {
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	op, _ := cat.Lookup("compoundingRisk")
	for _, tc := range []struct {
		name     string
		breakout any
		want     string
	}{
		{"repo", "REPO", "repository:" + opRepoA},
		{"absent_defaults_to_repo", nil, "repository:" + opRepoA},
		{"team", "TEAM", opRepoA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) { cfg.RowID = func() string { return opRepoA } }})
			vars := opMinimalVariables(t, op)
			filter, _ := vars["filter"].(map[string]any)
			if filter == nil {
				filter = map[string]any{}
			}
			delete(filter, "breakout")
			if tc.breakout != nil {
				filter["breakout"] = tc.breakout
			}
			if tc.breakout == "TEAM" {
				delete(filter, "repoIds")
			}
			vars["filter"] = filter
			q := gqlQueryFor(t, op, vars, []string{"compoundingRisk.rows[*].scopeId"}, "")
			resp := h.run(t, opUnrestricted(opOrgA), q.text, q.vars)
			h.wantServed(t, resp)
			if !strings.Contains(string(resp.Data), `"`+tc.want+`"`) || (tc.want == opRepoA && strings.Contains(string(resp.Data), "repository:")) {
				t.Fatalf("scopeId = %s, want %q", resp.Data, tc.want)
			}
		})
	}
}
