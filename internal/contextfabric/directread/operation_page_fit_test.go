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
	var src struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &src)
	var tree map[string]map[string]any
	_ = json.Unmarshal(src.Data, &tree)
	for _, row := range tree["workGraphArtifacts"]["rows"].([]any) {
		delete(row.(map[string]any), "evidence")
	}
	want, _ := json.Marshal(tree)
	if string(resp.Data) != string(want) || resp.Completeness != directread.CompletenessUnknown {
		t.Fatalf("under-budget data differs from the allowlisted upstream data:\n%.200s\n%.200s", resp.Data, want)
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
		if !strings.Contains(resp.Page.Cut, "totalCount is the count of the full read") || !strings.Contains(resp.Page.Cut, "pageInfo describes this page") {
			t.Fatalf("%s: statement %q", tc.name, resp.Page.Cut)
		}
	}
}

func TestTheProdShapeMeasures33285BytesAfterTheAllowlist(t *testing.T) {
	resp := runArtifacts(t, prodArtifactsBody(t, 200, 33285), 1)
	if resp.Refusal == nil || resp.Refusal.Code != directread.RefusalResponseBudget || resp.Refusal.MeasuredBytes != 33285 {
		t.Fatalf("%+v", resp.Refusal)
	}
}

func TestTheCutPageIsExactlyTheLargestPrefixAndHoldsNoWithheldField(t *testing.T) {
	body := prodArtifactsBody(t, 200, 33285)
	var src struct {
		Data struct {
			W struct {
				Rows []map[string]any `json:"rows"`
			} `json:"workGraphArtifacts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &src); err != nil {
		t.Fatal(err)
	}
	rows := src.Data.W.Rows
	for _, row := range rows {
		delete(row, "evidence")
	}
	encode := func(n int) []byte {
		out, _ := json.Marshal(map[string]any{"workGraphArtifacts": map[string]any{"rows": rows[:n], "degradedReason": nil, "__typename": "WorkGraphArtifactConnection"}})
		return out
	}
	want := 0
	for n := 1; n <= 200; n++ {
		if len(encode(n)) <= 32768 {
			want = n
		}
	}
	resp := runArtifacts(t, body, 0)
	if want < 1 || want >= 200 || resp.Page.RowsReturned != want || string(resp.Data) != string(encode(want)) {
		t.Fatalf("want %d rows, got %d: %.200s", want, resp.Page.RowsReturned, resp.Data)
	}
	if strings.Contains(string(resp.Data), "evidence") {
		t.Fatal("a withheld field reached the cut answer")
	}
}

func TestACutKeepsTheLargestPrefixWhenEndCursorsVaryInLength(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, refusal := cat.Lookup("capacityForecasts")
	if refusal != nil {
		t.Fatal(refusal)
	}
	// The first probe of a search over 300 rows is 150 rows: its end cursor
	// is long, the next row's is short, so 150 rows do not fit and 151 do.
	for _, maxBytes := range []int{4000, 4001, 4002, 4003, 4017, 4100, 9000, 20000, -151} {
		edges := make([]map[string]any, 300)
		for i := range edges {
			pad := (i * 7) % 40
			if maxBytes == -151 {
				pad = 0
				if i == 149 {
					pad = 200
				}
			}
			edges[i] = map[string]any{"node": map[string]any{"forecastId": fmt.Sprintf("f%d", i), "teamId": "t"}, "cursor": "c" + strings.Repeat("0", pad)}
		}
		page := func(n int) []byte {
			info := map[string]any{"hasNextPage": true, "hasPreviousPage": false, "startCursor": "c", "endCursor": edges[n-1]["cursor"]}
			out, _ := json.Marshal(map[string]any{"capacityForecasts": map[string]any{"edges": edges[:n], "totalCount": 300, "pageInfo": info}})
			return out
		}
		if maxBytes == -151 {
			maxBytes = len(page(151))
			if len(page(150)) <= maxBytes {
				t.Fatal("the planted case does not make 150 rows too big")
			}
		}
		want := 0
		for n := 1; n <= 300; n++ {
			if len(page(n)) <= maxBytes {
				want = n
			}
		}
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{"capacityForecasts": map[string]any{
			"edges": edges, "totalCount": 300,
			"pageInfo": map[string]any{"hasNextPage": false, "hasPreviousPage": false, "startCursor": "c", "endCursor": "x"},
		}}})
		h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{})
		vars, _ := json.Marshal(opMinimalVariables(t, op))
		resp, err := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "capacityForecasts", Variables: vars, MaxBytes: maxBytes})
		if err != nil || resp.Call != directread.CallServed || resp.Page.RowsReturned != want {
			t.Fatalf("max_bytes %d: want %d rows, got %+v err %v", maxBytes, want, resp.Page, err)
		}
	}
}

// Composite and nested shapes are out of scope by ruling: a refusal with a
// reason is the intended behaviour there. The refusal below is the one main
// gives: same code, same reason, the measured size of the whole answer.
func requireMainBudgetRefusal(t *testing.T, resp directread.OperationResponse, wantMeasured int) {
	t.Helper()
	out, _ := json.Marshal(resp)
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalResponseBudget ||
		resp.Refusal.Reason != "the serialized data exceeds max_bytes; narrow the window, the scope or the limit variable" ||
		resp.Refusal.MeasuredBytes != wantMeasured || resp.Refusal.MaxBytes != 32768 ||
		resp.Data != nil || resp.Page.Cut != "" || resp.Page.RowsRead != 0 || resp.Page.RowsReturned != 0 || resp.Page.ReturnedBytes != 0 {
		t.Fatalf("not main's refusal (measured want %d): %.500s", wantMeasured, out)
	}
}

func TestATwoListOperationAboveTheBudgetRefusesAsOnMain(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, refusal := cat.Lookup("compoundingRisk")
	if refusal != nil {
		t.Fatal(refusal)
	}
	rows := make([]map[string]any, 40)
	for i := range rows {
		rows[i] = map[string]any{"day": "2026-09-01", "scope": "REPO", "scopeId": fmt.Sprintf("s%d", i), "scopeLabel": strings.Repeat("l", 1000), "score": 1.5, "severity": "HIGH"}
	}
	trend := make([]map[string]any, 300)
	for i := range trend {
		trend[i] = map[string]any{"day": fmt.Sprintf("2026-08-%02d", i%28+1), "score": 1.5, "severity": "HIGH"}
	}
	data, _ := json.Marshal(map[string]any{"compoundingRisk": map[string]any{"rows": rows, "trend": trend}})
	raw, _ := json.Marshal(map[string]any{"data": json.RawMessage(data)})
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{})
	requireMainBudgetRefusal(t, h.run(t, opUnrestricted(opOrgA), "compoundingRisk", opMinimalVariables(t, op)), len(data))
}

func TestANestedListOperationAboveTheBudgetRefusesAsOnMain(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, refusal := cat.Lookup("capacityCompletionDistribution")
	if refusal != nil {
		t.Fatal(refusal)
	}
	points := func() []map[string]any {
		out := make([]map[string]any, 1500)
		for i := range out {
			out[i] = map[string]any{"value": i, "count": i % 9}
		}
		return out
	}
	data, _ := json.Marshal(map[string]any{"capacityForecast": map[string]any{"completionDistribution": map[string]any{"days": points(), "items": points()}}})
	raw, _ := json.Marshal(map[string]any{"data": json.RawMessage(data)})
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{})
	requireMainBudgetRefusal(t, h.run(t, opUnrestricted(opOrgA), "capacityCompletionDistribution", opMinimalVariables(t, op)), len(data))
}

// TestEveryOperationIsClassifiedForTheBudgetRule fails when an operation is
// added or changes shape without being classified here: primary-list
// operations are cut to the largest whole-row page, every other operation
// keeps the response_budget refusal.
func TestEveryOperationIsClassifiedForTheBudgetRule(t *testing.T) {
	want := map[string]string{
		"capacityForecasts":              "capacityForecasts.edges",
		"complexityTimeseries":           "complexityTimeseries.points",
		"hotspots":                       "hotspots.rows",
		"workGraphArtifacts":             "workGraphArtifacts.rows",
		"workGraphEdges":                 "workGraphEdges.edges",
		"acrRepositoryScopes":            "",
		"capacityCompletionDistribution": "",
		"capacityForecast":               "",
		"catalogValues":                  "",
		"cognitiveLoad":                  "",
		"compoundingRisk":                "",
		"home":                           "",
		"investmentBreakdown":            "",
		"investmentFull":                 "",
		"recommendations":                "",
		"securityOverview":               "",
		"throughputForecast":             "",
		"workGraphFlow":                  "",
		"workItemTeamAttributions":       "",
	}
	cat, _ := directread.DefaultCatalogue()
	ops := cat.Operations(directread.CallerUnrestricted)
	if len(ops) != len(want) {
		t.Fatalf("%d operations served, %d classified", len(ops), len(want))
	}
	for _, op := range ops {
		list, classified := want[op.Name]
		if !classified {
			t.Fatalf("%s is not classified for the budget rule", op.Name)
		}
		got, ok := op.PrimaryList()
		if got != list || ok != (list != "") {
			t.Fatalf("%s: primary list %q (%v), classified %q", op.Name, got, ok, list)
		}
	}
}

func TestAForeignRowBeyondTheReturnedPrefixStillRefusesTheWholeAnswer(t *testing.T) {
	rows := make([]map[string]any, 300)
	for i := range rows {
		rows[i] = map[string]any{"filePath": strings.Repeat("p", 120), "repoId": opRepoA}
	}
	rows[299]["repoId"] = opRepoB
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"hotspots": map[string]any{"rows": rows}}})
	cat, _ := directread.DefaultCatalogue()
	op, _ := cat.Lookup("hotspots")
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{})
	resp := h.run(t, opRestrictedA(), "hotspots", opMinimalVariables(t, op))
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalRowOutsideGrant || len(resp.Data) != 0 {
		out, _ := json.Marshal(resp)
		t.Fatalf("%.400s", out)
	}
}

func TestACutKeepsTheLargestPrefixWhenCursorsNeedEscapes(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, refusal := cat.Lookup("capacityForecasts")
	if refusal != nil {
		t.Fatal(refusal)
	}
	// Every cursor has the same raw length; those at index 149 and 150 differ
	// in escapes (a control character expands to six bytes), so the first probe of a
	// search (150 rows, end cursor of row 149) is long and 151 rows are short.
	edges := make([]map[string]any, 300)
	for i := range edges {
		cursor := strings.Repeat("a", 300)
		if i == 149 {
			cursor = strings.Repeat("\x01", 300)
		}
		edges[i] = map[string]any{"node": map[string]any{"forecastId": fmt.Sprintf("f%d", i), "teamId": "t"}, "cursor": cursor}
	}
	page := func(n int) []byte {
		info := map[string]any{"hasNextPage": true, "hasPreviousPage": false, "startCursor": "c", "endCursor": edges[n-1]["cursor"]}
		out, _ := json.Marshal(map[string]any{"capacityForecasts": map[string]any{"edges": edges[:n], "totalCount": 300, "pageInfo": info}})
		return out
	}
	maxBytes := len(page(151))
	if len(page(150)) <= maxBytes {
		t.Fatal("the planted case does not make 150 rows too big")
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"capacityForecasts": map[string]any{
		"edges": edges, "totalCount": 300,
		"pageInfo": map[string]any{"hasNextPage": false, "hasPreviousPage": false, "startCursor": "c", "endCursor": "x"},
	}}})
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(raw) }, opHarnessOptions{})
	vars, _ := json.Marshal(opMinimalVariables(t, op))
	resp, err := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "capacityForecasts", Variables: vars, MaxBytes: maxBytes})
	if err != nil || resp.Call != directread.CallServed || resp.Page.RowsReturned != 151 {
		t.Fatalf("want 151 rows, got %+v err %v", resp.Page, err)
	}
}
