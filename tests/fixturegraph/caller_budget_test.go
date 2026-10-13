//go:build fixturegraph

package fixturegraph

import (
	"encoding/json"
	"testing"
)

func teamQuestionArgs(c *client, budget int) doc {
	args := doc{
		"question":        "Which teams need attention?",
		"expected_kinds":  []string{"team"},
		"evidence_window": doc{"relative_id": "trailing_365d"},
		"interpretation": doc{
			"shape": "discovered_cohort", "requested_judgment": "teams_under_pressure",
			"time_context": doc{"axis": "current"}, "fact_requirements": []doc{{"kind": "health"}},
			"clarification_needed": false,
		},
		"contract":  c.interpretContract(),
		"synthesis": "client",
	}
	if budget > 0 {
		args["budget"] = doc{"max_serialized_bytes": budget}
	}
	return args
}

func jsonLen(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}

// A caller byte budget on a client synthesis turn reaches the input cut through
// the real MCP tool and the real API: the answer and its input stay within the
// budget, and a cut input is a different input, disclosed as bounded.
func TestCallerByteBudgetCutsTheClientSynthesisInputThroughTheMCPTool(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	whole, _ := c.call("investigate_with_interpretation", teamQuestionArgs(c, 0))
	wholeInput, _ := structured(whole).(doc)["synthesis_input"].(map[string]any)
	if wholeInput == nil {
		t.Fatalf("the unbudgeted call carries no synthesis_input: status=%s", str(structured(whole), "status"))
	}
	wholeSize := jsonLen(wholeInput)
	for _, budget := range []int{60000, max(8192, wholeSize)} {
		d, raw := c.call("investigate_with_interpretation", teamQuestionArgs(c, budget))
		in, _ := structured(d).(doc)["synthesis_input"].(map[string]any)
		if in == nil {
			t.Fatalf("budget %d: no synthesis_input: %.1500s", budget, raw)
		}
		if size := jsonLen(structured(d)); size > budget {
			t.Errorf("budget %d: the structured response is %d bytes", budget, size)
		}
		if size := jsonLen(in); size > budget/2 && wholeSize > budget/2 {
			t.Errorf("budget %d: synthesis_input is %d bytes, above its half of the budget", budget, size)
		}
		cut := str(in, "input_sha256") != str(wholeInput, "input_sha256")
		if wholeSize > budget/2 && !cut {
			t.Errorf("budget %d: the whole input (%d bytes) is above half the budget but the input was not cut (sha equal)", budget, wholeSize)
		}
		if cut && get(in, "bounded") != true {
			t.Errorf("budget %d: the input was cut and not disclosed as bounded", budget)
		}
		t.Logf("budget %d: whole input %d bytes, served input %d bytes, cut=%v", budget, wholeSize, jsonLen(in), cut)
	}
}
