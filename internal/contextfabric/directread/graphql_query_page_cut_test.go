package directread_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const pageCutHotspotsQuery = `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z", limit: 150}) { rows { filePath } } }`

func plantHotspotRows(count int) func(cfg *fakeMCPConfig) {
	return func(cfg *fakeMCPConfig) {
		cfg.Plant = func(data map[string]any) {
			rows := make([]any, count)
			for i := range rows {
				rows[i] = map[string]any{"filePath": fmt.Sprintf("path/to/file-%04d.go", i)}
			}
			data["hotspots"].(map[string]any)["rows"] = rows
		}
	}
}

// An over-budget answer with one root field and one list is served as the
// largest whole-row page, said so, and counted; it is never refused.
func TestGraphQLQueryOverBudgetSingleListIsServedAsADisclosedPage(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{fake: plantHotspotRows(400)})
	const maxBytes = 4096
	resp, err := h.runner.Run(t.Context(), opUnrestricted(opOrgA), directread.GraphQLRequest{Query: pageCutHotspotsQuery, MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Call != directread.CallServed || resp.Completeness != directread.CompletenessDeclaredPartial || resp.CompletenessReason != directread.ReasonPageCut {
		t.Fatalf("want served declared_partial page_cut, got call=%s completeness=%s reason=%s refusal=%+v", resp.Call, resp.Completeness, resp.CompletenessReason, resp.Refusal)
	}
	var data struct {
		Hotspots struct {
			Rows []json.RawMessage `json:"rows"`
		} `json:"hotspots"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Hotspots.Rows) < 2 || len(data.Hotspots.Rows) >= 400 || len(resp.Data) > maxBytes {
		t.Fatalf("page holds %d of 400 rows in %d bytes (limit %d)", len(data.Hotspots.Rows), len(resp.Data), maxBytes)
	}
	if resp.Page.RowsReturned != len(data.Hotspots.Rows) || resp.Page.RowsRead != 400 || !strings.Contains(resp.Page.Cut, "cut to whole rows to fit max_bytes (4096)") {
		t.Fatalf("page disclosure %+v", resp.Page)
	}
	if len(resp.RootFields) != 1 || resp.RootFields[0].Completeness != directread.CompletenessDeclaredPartial || resp.RootFields[0].CompletenessReason != directread.ReasonPageCut {
		t.Fatalf("root fields %+v", resp.RootFields)
	}
	if got := h.runner.Counters().PageCuts; got != 1 {
		t.Fatalf("page cut counter = %d, want 1", got)
	}
	if !strings.Contains(h.logs.String(), `"level":"INFO","msg":"`+directread.GraphQLPageCutLog+`"`) || !strings.Contains(h.logs.String(), `"rows_read":400`) {
		t.Fatalf("no page cut Info line: %s", h.logs.String())
	}
}

// An answer that cannot be cut at one list keeps the budget refusal.
func TestGraphQLQueryOverBudgetWithTwoListsStaysRefused(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) {
		cfg.Plant = func(data map[string]any) {
			root := data["compoundingRisk"].(map[string]any)
			rows := make([]any, 200)
			trend := make([]any, 200)
			for i := range rows {
				rows[i] = map[string]any{"scopeId": fmt.Sprintf("scope-%04d", i)}
				trend[i] = map[string]any{"day": "2026-09-01"}
			}
			root["rows"], root["trend"] = rows, trend
		}
	}})
	resp, err := h.runner.Run(t.Context(), opUnrestricted(opOrgA), directread.GraphQLRequest{Query: `{ compoundingRisk(filter: {breakout: REPO}) { rows { scopeId } trend { day } } }`, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalResponseBudget {
		t.Fatalf("want response_budget refusal, got call=%s refusal=%+v", resp.Call, resp.Refusal)
	}
	if got := h.runner.Counters().PageCuts; got != 0 {
		t.Fatalf("page cut counter = %d, want 0", got)
	}
}
