//go:build fixturegraph

package fixturegraph

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func jsonLen(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}

func synthesisInputOf(d doc) map[string]any {
	in, _ := structured(d).(map[string]any)["synthesis_input"].(map[string]any)
	return in
}

// ownerTurn asks "which team owns project P?" on a client synthesis turn at a
// caller byte budget through the real MCP tool. A budget refusal is returned
// as isError.
func ownerTurn(t *testing.T, c *client, project string, budget int) (doc, string, bool) {
	t.Helper()
	args := doc{
		"question":        fmt.Sprintf("Which team owns the project %s?", project),
		"expected_kinds":  []string{"project"},
		"evidence_window": doc{"relative_id": "trailing_365d"},
		"interpretation": doc{
			"shape": "single_subject", "requested_judgment": "the team that owns the project",
			"subject_terms": []string{project}, "requested_subject_kind": "project",
			"time_context": doc{"axis": "current"}, "fact_requirements": []doc{{"kind": "identity"}},
			"clarification_needed": false,
		},
		"contract":  c.interpretContract(),
		"synthesis": "client",
		"budget":    doc{"max_serialized_bytes": budget},
	}
	d, raw, isErr := c.tryCall("investigate_with_interpretation", args)
	if isErr || str(structured(d), "status") != "clarification_required" {
		return d, raw, isErr
	}
	for _, o := range list(structured(d), "structure_needs", "kind_options") {
		if str(o, "kind") == "project" {
			rid := str(structured(d), "result_id")
			args["parent_result_id"] = rid
			args["prior_kind_receipts"] = []doc{{"result_id": rid, "receipt_id": str(o, "receipt_id")}}
			return c.tryCall("investigate_with_interpretation", args)
		}
	}
	return d, raw, isErr
}

// A caller byte budget on a client synthesis turn reaches the input cut through
// the real MCP tool and the real API. The smallest input of a one-project turn
// is about 35 KB: it is served whole at 60000, and at a budget it cannot share
// with its answer the turn is a typed budget refusal, never an answer over the
// budget.
func TestCallerByteBudgetBoundsTheClientSynthesisTurnThroughTheMCPTool(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	rows := ch(t, fmt.Sprintf(`SELECT p.name FROM team_project_ownership AS o FINAL
INNER JOIN projects AS p FINAL ON p.org_id = o.org_id AND p.id = o.project_id
WHERE o.org_id = %[1]s AND o.valid_to IS NULL AND p.is_active = 1
AND o.project_id IN (SELECT project_id FROM team_project_ownership FINAL WHERE org_id = %[1]s AND valid_to IS NULL GROUP BY project_id HAVING uniqExact(team_id) = 1)
ORDER BY p.name LIMIT 1`, sqlStr(orgID(t))))
	if len(rows) != 1 {
		t.Fatalf("no active project with exactly one open owner: %v", rows)
	}
	project := rows[0][0]
	whole, raw, isErr := ownerTurn(t, c, project, 1048576)
	if isErr || synthesisInputOf(whole) == nil {
		t.Fatalf("the generous-budget call carries no synthesis_input: %.1500s", raw)
	}
	wholeSize := jsonLen(structured(whole))
	t.Logf("whole structured response %d bytes, synthesis_input %d bytes", wholeSize, jsonLen(synthesisInputOf(whole)))
	// One budget the whole answer cannot fit, one it can.
	over, fits := wholeSize-2000, wholeSize+25000
	served := 0
	for _, budget := range []int{fits, over} {
		d, raw, isErr := ownerTurn(t, c, project, budget)
		if isErr {
			if !strings.Contains(raw, "budget_refusal") {
				t.Errorf("budget %d: a tool error that is not a budget refusal: %.800s", budget, raw)
			}
			t.Logf("budget %d: budget refusal", budget)
			continue
		}
		served++
		if size := jsonLen(structured(d)); size > budget {
			t.Errorf("budget %d: served %d bytes, over the caller budget", budget, size)
		}
	}
	if served == 0 {
		t.Errorf("the budget that fits the whole answer was not served")
	}
}
