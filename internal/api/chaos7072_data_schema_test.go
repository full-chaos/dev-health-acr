package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// CHAOS-7072 (S1a): the published response schemas of the three MCP data
// tools are anchored to the REAL producers. The Go response types live in
// directread and this package (both import contracts/v1), so the
// parity registry in internal/contracts/v1 exempts these documents and this
// test stands in for it: every answer the real routes emit, in every state the
// harness can reach, must validate against the canonical schema. The schemas
// are additionalProperties:false, so an emitted member nobody published fails
// here; a published-required member a producer omits fails here too.

func dataSchemaFor(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "contracts", "jsonschema", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func assertValidAgainstSchema(t *testing.T, schema *jsonschema.Resolved, label string, body []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if err := schema.Validate(value); err != nil {
		t.Errorf("%s: the real route answer does not validate against its published schema: %v\n%s", label, err, body)
	}
}

func TestChaos7072RealRouteAnswersValidateAgainstThePublishedSchemas(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	restricted := h.issueFor(t, c7072Data, []string{hostedTestRepository}, nil).Token
	unrestricted := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	contextOnly := h.issueFor(t, c7072Context, c7072AllRepos, nil).Token
	answers := 0

	// data_catalog: every caller class, section filters, and the
	// scope_missing state.
	catalog := dataSchemaFor(t, "mcp_data_catalog_response.v1.schema.json")
	for label, target := range map[string]struct{ token, query string }{
		"unrestricted":        {unrestricted, ""},
		"restricted":          {restricted, ""},
		"no data:read":        {contextOnly, ""},
		"operations only":     {unrestricted, "?sections=operations"},
		"limits and subjects": {restricted, "?sections=limits,subjects"},
		"relationships":       {unrestricted, "?sections=relationships"},
		"facts":               {unrestricted, "?sections=facts"},
	} {
		response := h.get(ContextFabricDataCatalogPath+target.query, target.token)
		if response.Code != http.StatusOK {
			t.Fatalf("catalog %s: %d %s", label, response.Code, response.Body.String())
		}
		assertValidAgainstSchema(t, catalog, "catalog/"+label, response.Body.Bytes())
		answers++
	}

	// find_subjects: list, name (found), name (no match), unknown kind list.
	find := dataSchemaFor(t, "mcp_find_subjects_response.v1.schema.json")
	for label, body := range map[string]string{
		"list":            `{"kind":"repository"}`,
		"list small page": `{"kind":"repository","limit":1}`,
		"name found":      `{"query":"` + hostedTestRepository + `","kinds":["repository"]}`,
		"name default":    `{"query":"` + hostedTestRepository + `"}`,
		"name no match":   `{"query":"nothing-matches-this","kinds":["repository","team"]}`,
		"list empty kind": `{"kind":"incident"}`,
	} {
		response := h.post(ContextFabricDataSubjectsPath, unrestricted, body)
		if response.Code != http.StatusOK {
			t.Fatalf("subjects %s: %d %s", label, response.Code, response.Body.String())
		}
		assertValidAgainstSchema(t, find, "subjects/"+label, response.Body.Bytes())
		answers++
	}
	restrictedList := h.post(ContextFabricDataSubjectsPath, restricted, `{"kind":"repository"}`)
	assertValidAgainstSchema(t, find, "subjects/restricted list", restrictedList.Body.Bytes())
	answers++

	// run_operation: served, empty, refusals of several codes, an upstream
	// error, over-budget.
	operation := dataSchemaFor(t, "mcp_run_operation_response.v1.schema.json")
	h.upstream.respond = func(map[string]any) string { return compoundingRiskAnswer(c7072RepoA) }
	risk := `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO","repoIds":["repository:` + c7072RepoA + `"]}}}`
	cases := map[string]struct{ token, body string }{
		"served":            {unrestricted, risk},
		"served restricted": {restricted, risk},
		"unknown operation": {unrestricted, `{"operation":"createSavedReport"}`},
		"person operation":  {unrestricted, `{"operation":"busFactor"}`},
		"variable refused":  {unrestricted, `{"operation":"hotspots","variables":{"input":{"orgId":"org_2"}}}`},
		"basis dependent":   {unrestricted, `{"operation":"investmentBreakdown","variables":{"batch":{"breakdowns":[{"dimension":"TEAM","measure":"COUNT","dateRange":{"startDate":"2026-06-29","endDate":"2026-09-28"},"topN":10}]}}}`},
		"foreign id":        {restricted, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO","repoIds":["repository:` + c7072RepoB + `"]}}}`},
		"over budget":       {unrestricted, `{"operation":"compoundingRisk","max_bytes":10,"variables":{"filter":{"breakout":"REPO"}}}`},
		"restricted team":   {restricted, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"TEAM"}}}`},
	}
	for label, tc := range cases {
		response := h.post(ContextFabricDataOperationsPath, tc.token, tc.body)
		if response.Code != http.StatusOK {
			t.Fatalf("operation %s: %d %s", label, response.Code, response.Body.String())
		}
		assertValidAgainstSchema(t, operation, "operations/"+label, response.Body.Bytes())
		answers++
	}
	h.upstream.respond = func(map[string]any) string { return `{"errors":[{"message":"boom secret"}],"data":null}` }
	response := h.post(ContextFabricDataOperationsPath, unrestricted, risk)
	assertValidAgainstSchema(t, operation, "operations/upstream error", response.Body.Bytes())
	answers++
	h.upstream.respond = func(map[string]any) string {
		return `{"data":{"compoundingRisk":{"orgId":"org_1","breakout":"REPO","rows":[],"trend":[]}}}`
	}
	response = h.post(ContextFabricDataOperationsPath, unrestricted, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO"}}}`)
	assertValidAgainstSchema(t, operation, "operations/empty", response.Body.Bytes())
	answers++
	if answers < 25 {
		t.Fatalf("only %d real answers were validated; the measurement did not happen", answers)
	}
}
