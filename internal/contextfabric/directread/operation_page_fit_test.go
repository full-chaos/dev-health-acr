package directread_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// prodArtifactsBody builds the workGraphArtifacts answer the way prod
// returned it at the advertised default limit: 200 rows of the selected
// fields, 33285 serialized bytes once the output allowlist ran (the
// readback of the run refused with response_budget measured 33285 against
// 32768). evidence is selected by the document and withheld by the policy.
func prodArtifactsBody(t *testing.T, rows, target int) string {
	t.Helper()
	build := func(pad int) []map[string]any {
		out := make([]map[string]any, rows)
		for i := range out {
			out[i] = map[string]any{
				"nodeType":    "PR",
				"nodeId":      fmt.Sprintf("pr:full-chaos/dev-health-ops#%04d", i),
				"displayName": fmt.Sprintf("fix(workers): CHAOS-%04d %s", i, strings.Repeat("x", pad)),
				"degree":      i%17 + 1,
				"__typename":  "WorkGraphArtifactRow",
			}
		}
		return out
	}
	size := func(pad int) int {
		raw, _ := json.Marshal(map[string]any{"workGraphArtifacts": map[string]any{"rows": build(pad), "degradedReason": nil, "__typename": "WorkGraphArtifactConnection"}})
		return len(raw)
	}
	pad := 0
	for size(pad+1) <= target {
		pad++
	}
	base := size(pad)
	if base > target {
		t.Fatalf("base %d above target %d", base, target)
	}
	full := build(pad)
	if target > base {
		full = build(pad)
		for i := 0; i < target-base; i++ {
			full[i]["displayName"] = full[i]["displayName"].(string) + "x"
		}
	}
	for _, row := range full {
		row["evidence"] = map[string]any{"source": "work_graph_issue_pr", "tier": "native"}
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"workGraphArtifacts": map[string]any{"rows": full, "degradedReason": nil, "__typename": "WorkGraphArtifactConnection"}}})
	return string(raw)
}

func runArtifacts(t *testing.T, body string, maxBytes int) directread.OperationResponse {
	t.Helper()
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, body }, opHarnessOptions{})
	raw, _ := json.Marshal(map[string]any{"filters": map[string]any{"limit": 200}})
	resp, err := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "workGraphArtifacts", Variables: raw, MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAdvertisedDefaultLimitAnswersWithTheLargestWholeRowPage(t *testing.T) {
	body := prodArtifactsBody(t, 200, 33285)
	resp := runArtifacts(t, body, 0)
	if resp.Call != directread.CallServed {
		out, _ := json.Marshal(resp.Refusal)
		t.Fatalf("call %s refusal %s", resp.Call, out)
	}
	if len(resp.Data) > 32768 || resp.Page.ReturnedBytes != len(resp.Data) {
		t.Fatalf("data %d bytes, page %+v", len(resp.Data), resp.Page)
	}
	var got struct {
		WorkGraphArtifacts struct {
			Rows []map[string]any `json:"rows"`
		} `json:"workGraphArtifacts"`
	}
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatal(err)
	}
	n := len(got.WorkGraphArtifacts.Rows)
	if n < 150 || n >= 200 || resp.Page.RowsReturned != n || resp.Page.RowsRead != 200 {
		t.Fatalf("rows %d, page %+v", n, resp.Page)
	}
	if !strings.Contains(resp.Page.Cut, fmt.Sprintf("%d of 200", n)) || resp.Completeness != directread.CompletenessUnknown {
		t.Fatalf("cut statement %q completeness %s", resp.Page.Cut, resp.Completeness)
	}
	// Largest: one more row would not fit.
	bigger := runArtifacts(t, body, len(resp.Data)+1)
	if bigger.Page.RowsReturned != n {
		t.Fatalf("a budget of +1 byte changed the page: %d", bigger.Page.RowsReturned)
	}
	tight := runArtifacts(t, body, len(resp.Data))
	if tight.Page.RowsReturned != n {
		t.Fatalf("a budget of exactly the data size dropped rows: %d", tight.Page.RowsReturned)
	}
	// The first rows are whole and in order.
	if got.WorkGraphArtifacts.Rows[0]["nodeId"] != "pr:full-chaos/dev-health-ops#0000" {
		t.Fatalf("first row %v", got.WorkGraphArtifacts.Rows[0])
	}
}

func TestAnAnswerUnderTheBudgetIsServedUncut(t *testing.T) {
	body := prodArtifactsBody(t, 20, 4000)
	resp := runArtifacts(t, body, 0)
	if resp.Call != directread.CallServed || resp.Page.Cut != "" || resp.Page.RowsReturned != 0 || resp.Page.RowsRead != 0 {
		t.Fatalf("%+v", resp.Page)
	}
	out, _ := json.Marshal(resp.Page)
	if strings.Contains(string(out), "cut") || strings.Contains(string(out), "rows_") {
		t.Fatalf("page carries cut fields: %s", out)
	}
}

func TestASingleRowAboveTheBudgetStillRefuses(t *testing.T) {
	body := prodArtifactsBody(t, 3, 3000)
	resp := runArtifacts(t, body, 500)
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalResponseBudget || resp.Data != nil {
		t.Fatalf("%+v", resp)
	}
	if resp.Refusal.MeasuredBytes <= 500 || resp.Refusal.MaxBytes != 500 {
		t.Fatalf("%+v", resp.Refusal)
	}
}

func TestEveryListOperationFitsItsPage(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	for _, name := range []string{"workGraphEdges", "hotspots", "capacityForecasts"} {
		op, refusal := cat.Lookup(name)
		if refusal != nil {
			t.Fatalf("%s not in the catalogue", name)
		}
		rows := make([]map[string]any, 400)
		pad := strings.Repeat("y", 120)
		var root string
		for i := range rows {
			switch name {
			case "workGraphEdges":
				root = "edges"
				rows[i] = map[string]any{"edgeId": fmt.Sprintf("e%d", i), "repoId": opRepoA, "sourceDisplayName": pad}
			case "capacityForecasts":
				root = "edges"
				rows[i] = map[string]any{"node": map[string]any{"forecastId": fmt.Sprintf("f%d", i), "teamId": pad}}
			default:
				root = "rows"
				rows[i] = map[string]any{"filePath": pad, "repoId": opRepoA}
			}
		}
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{name: map[string]any{root: rows}}})
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{})
		resp := h.run(t, opUnrestricted(opOrgA), name, opMinimalVariables(t, op))
		if resp.Call != directread.CallServed || len(resp.Data) > 32768 || resp.Page.RowsRead != 400 || resp.Page.RowsReturned < 1 || resp.Page.RowsReturned >= 400 {
			out, _ := json.Marshal(resp)
			t.Fatalf("%s: %.400s", name, out)
		}
	}
}

func TestACutPageAndTheRepeatedCallCoverEveryRowOnce(t *testing.T) {
	body := prodArtifactsBody(t, 200, 33285)
	first := runArtifacts(t, body, 0)
	var need int
	if _, err := fmt.Sscanf(first.Page.Cut[strings.Index(first.Page.Cut, "max_bytes of at least ")+len("max_bytes of at least "):], "%d", &need); err != nil || need <= 32768 {
		t.Fatalf("statement gives no exact size: %q", first.Page.Cut)
	}
	second := runArtifacts(t, body, need)
	if second.Call != directread.CallServed || second.Page.Cut != "" {
		t.Fatalf("repeat with the stated size: %+v", second.Page)
	}
	ids := func(data json.RawMessage) []string {
		var v struct {
			W struct {
				Rows []struct {
					NodeID string `json:"nodeId"`
				} `json:"rows"`
			} `json:"workGraphArtifacts"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(v.W.Rows))
		for i, r := range v.W.Rows {
			out[i] = r.NodeID
		}
		return out
	}
	a, b := ids(first.Data), ids(second.Data)
	seen := map[string]int{}
	for _, id := range b {
		seen[id]++
	}
	if len(b) != 200 || len(seen) != 200 {
		t.Fatalf("repeat holds %d rows, %d distinct", len(b), len(seen))
	}
	for i, id := range a {
		if b[i] != id {
			t.Fatalf("row %d differs: %s vs %s", i, id, b[i])
		}
	}
}

func TestACutMovesPagePositionToTheReturnedPage(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	pad := strings.Repeat("z", 150)
	for _, tc := range []struct {
		name, root string
		row        func(i int) map[string]any
		wantCursor bool
	}{
		{"workGraphEdges", "edges", func(i int) map[string]any {
			return map[string]any{"edgeId": fmt.Sprintf("e%d", i), "sourceDisplayName": pad}
		}, false},
		{"capacityForecasts", "edges", func(i int) map[string]any {
			return map[string]any{"node": map[string]any{"forecastId": fmt.Sprintf("f%d", i), "teamId": pad}, "cursor": fmt.Sprintf("c%d", i)}
		}, true},
	} {
		op, refusal := cat.Lookup(tc.name)
		if refusal != nil {
			t.Fatal(refusal)
		}
		rows := make([]map[string]any, 300)
		for i := range rows {
			rows[i] = tc.row(i)
		}
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{tc.name: map[string]any{
			tc.root: rows, "totalCount": 300,
			"pageInfo": map[string]any{"hasNextPage": false, "hasPreviousPage": false, "startCursor": "c0", "endCursor": "c299"},
		}}})
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{})
		resp := h.run(t, opUnrestricted(opOrgA), tc.name, opMinimalVariables(t, op))
		if resp.Call != directread.CallServed || resp.Page.RowsReturned < 1 || resp.Page.RowsReturned >= 300 {
			t.Fatalf("%s: %+v", tc.name, resp.Page)
		}
		var got map[string]struct {
			Rows       []map[string]any `json:"edges"`
			TotalCount int              `json:"totalCount"`
			PageInfo   struct {
				HasNextPage bool    `json:"hasNextPage"`
				StartCursor *string `json:"startCursor"`
				EndCursor   *string `json:"endCursor"`
			} `json:"pageInfo"`
		}
		if err := json.Unmarshal(resp.Data, &got); err != nil {
			t.Fatal(err)
		}
		d := got[tc.name]
		if !d.PageInfo.HasNextPage || d.TotalCount != 300 || len(d.Rows) != resp.Page.RowsReturned {
			t.Fatalf("%s: %s", tc.name, resp.Data)
		}
		if tc.wantCursor {
			if d.PageInfo.EndCursor == nil || *d.PageInfo.EndCursor != fmt.Sprintf("c%d", len(d.Rows)-1) {
				t.Fatalf("%s: end cursor %v after %d rows", tc.name, d.PageInfo.EndCursor, len(d.Rows))
			}
		} else if d.PageInfo.EndCursor != nil {
			t.Fatalf("%s: end cursor %v describes the upstream page", tc.name, *d.PageInfo.EndCursor)
		}
		if !strings.Contains(resp.Page.Cut, "totalCount is the count of the full read") || !strings.Contains(resp.Page.Cut, "no offset or cursor") {
			t.Fatalf("%s: statement %q", tc.name, resp.Page.Cut)
		}
	}
}
