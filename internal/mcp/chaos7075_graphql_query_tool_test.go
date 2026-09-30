package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func registeredToolNames(t *testing.T, boot *Bootstrap) []string {
	t.Helper()
	client, closeFn := connectedClient(t, boot)
	defer closeFn()
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if len(names) < 2 {
		t.Fatalf("tools/list returned %d tools; the measurement did not happen", len(names))
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// CHAOS-7075: graphql_query as an MCP tool. The hosted API is the fake TLS
// server of data_tools_test.go; the handler, the sidecar client and the
// render are real.

const dtGQLBody = `{"call":"served","completeness":"unknown","result":"data","source":{"path":"graphql","service":"dho query-api","listener":"mcp","schema_digest":"sha256:0","query_digest":"q"},"root_fields":[{"key":"hotspots","field":"hotspots","operation":"hotspots","completeness":"unknown","effective_scope":{"repo_ids":[],"forced_by_grant":false},"added_paths":[]}],"data":{"hotspots":{"rows":[{"filePath":"a.go","riskScore":12345678901234567890}]}},"errors":[],"page":{"returned_bytes":80,"max_bytes":32768},"consistency":"best_effort","untrusted_content":{"untrusted":true,"fields":["data"]},"request":{"max_bytes":32768}}`

const dtGQLRefusedBody = `{"call":"refused","completeness":"unknown","refusal":{"code":"read_budget_exceeded","reason":"the query service refused the query on its read budget","read_budget":"bytes_ceiling"},"source":{"path":"graphql","service":"dho query-api","listener":"mcp","schema_digest":"sha256:0"},"root_fields":[],"errors":[],"page":{"returned_bytes":0,"max_bytes":32768},"consistency":"best_effort","untrusted_content":{"untrusted":true,"fields":["data"]},"request":{"max_bytes":32768}}`

// Registered only when advertised; the call forwards the query and the
// variables unchanged (numbers keep their text), returns the API JSON
// verbatim as structured content, and a refusal is an answer, not an error.
func TestGraphQLQueryToolForwardsAndReturnsTheAnswerVerbatim(t *testing.T) {
	h := newDTHosted(t)
	if names := registeredToolNames(t, h.boot(t, toolRunOperation)); containsName(names, toolGraphQLQuery) {
		t.Fatalf("graphql_query registered without being advertised: %v", names)
	}
	boot := h.boot(t, toolGraphQLQuery)
	if names := registeredToolNames(t, boot); !containsName(names, toolGraphQLQuery) {
		t.Fatalf("graphql_query not registered when advertised: %v", names)
	}
	args := `{"query":"query($n: Int) { hotspots(input: {limit: $n}) { rows { filePath } } }","variables":{"n":12345678901234567890},"max_bytes":4096}`
	result := dtCall(t, boot, toolGraphQLQuery, args)
	if result.IsError {
		t.Fatalf("served answer is a tool error: %s", dtText(t, result))
	}
	if h.last.path != "/api/v1/context-fabric/data/graphql" || !strings.Contains(string(h.last.body), "12345678901234567890") {
		t.Fatalf("forwarded %s %s", h.last.path, h.last.body)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), `"root_fields"`) || !strings.Contains(string(raw), "12345678901234567890") {
		t.Fatalf("structured content changed: %s", raw)
	}
	if text := dtText(t, result); !strings.Contains(text, "graphql_query: call=served") || !strings.Contains(text, "Completeness unknown means unknown") {
		t.Fatalf("text summary: %s", text)
	}

	h.graphql = func(w http.ResponseWriter, _ *http.Request) { dtRaw(w, http.StatusOK, dtGQLRefusedBody) }
	refused := dtCall(t, boot, toolGraphQLQuery, `{"query":"{ hotspots { rows { filePath } } }"}`)
	if refused.IsError || !strings.Contains(dtText(t, refused), "read budget bytes_ceiling") {
		t.Fatalf("refusal: error=%v %s", refused.IsError, dtText(t, refused))
	}
	for name, bad := range map[string]string{
		"no query":        `{}`,
		"empty query":     `{"query":""}`,
		"query too long":  `{"query":"` + strings.Repeat("x", 8193) + `"}`,
		"max_bytes high":  `{"query":"{ x }","max_bytes":262145}`,
		"variables array": `{"query":"{ x }","variables":[1]}`,
	} {
		calls := h.calls.Load()
		if r := dtCall(t, boot, toolGraphQLQuery, bad); !r.IsError || h.calls.Load() != calls {
			t.Fatalf("%s: accepted or forwarded (error=%v)", name, r.IsError)
		}
	}
}
