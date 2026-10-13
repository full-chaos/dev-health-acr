//go:build fixturegraph

package fixturegraph

import (
	"encoding/json"
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

// A caller byte budget on a client synthesis turn reaches the input cut through
// the real MCP tool and the real API: a served answer stays within the budget
// and a cut input is a different, disclosed input; a turn whose smallest input
// cannot share the budget with its answer is a typed budget refusal.
func TestCallerByteBudgetCutsTheClientSynthesisInputThroughTheMCPTool(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	one, _ := slugs(t)
	whole, raw, isErr := walkAt(t, c, one, 1048576)
	if isErr || synthesisInputOf(whole) == nil {
		t.Fatalf("the generous-budget call carries no synthesis_input: %.1500s", raw)
	}
	wholeInput := synthesisInputOf(whole)
	wholeSize := jsonLen(wholeInput)
	t.Logf("whole synthesis_input %d bytes, whole structured response %d bytes", wholeSize, jsonLen(structured(whole)))
	served := 0
	for _, budget := range []int{60000, 8192} {
		d, raw, isErr := walkAt(t, c, one, budget)
		if isErr {
			if !strings.Contains(raw, "budget_refusal") {
				t.Errorf("budget %d: a tool error that is not a budget refusal: %.800s", budget, raw)
			}
			t.Logf("budget %d: budget refusal", budget)
			continue
		}
		in := synthesisInputOf(d)
		if in == nil {
			t.Errorf("budget %d: served without a synthesis_input: %.800s", budget, raw)
			continue
		}
		served++
		if size := jsonLen(structured(d)); size > budget {
			t.Errorf("budget %d: the structured response is %d bytes, over the budget", budget, size)
		}
		cut := str(in, "input_sha256") != str(wholeInput, "input_sha256")
		if cut && get(in, "bounded") != true {
			t.Errorf("budget %d: the input was cut and not disclosed as bounded", budget)
		}
		t.Logf("budget %d: served input %d bytes, structured %d bytes, cut=%v", budget, jsonLen(in), jsonLen(structured(d)), cut)
	}
	if served == 0 {
		t.Errorf("no budget served the turn")
	}
}
