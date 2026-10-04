package api

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-7075 (S1b): the graphql_query route, its capability, and the
// data_catalog schema section, over the real app and the real runner with
// a fake MCP listener.

type c7075Options struct {
	noGraphQL bool
	noGraph   bool
}

func newC7075Harness(t *testing.T, opts c7075Options) *c7072Harness {
	t.Helper()
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	upstream := newC7072Upstream(t)
	configure := func(deps *RuntimeDependencies) {
		deps.DataCatalogue = catalogue
		graph := c7072Graph{}
		var gate *directread.SubjectGate
		var grants directread.GrantedRepositories
		if !opts.noGraph {
			gate = directread.NewSubjectGate(graph, nil)
			deps.DirectReadGate = gate
			deps.DataSubjects = directread.NewSubjectLookup(graph, gate, nil)
			grants = directread.NewGrantedRepositories(directread.NewSubjectLookup(graph, gate, nil))
		}
		if opts.noGraphQL {
			return
		}
		client, err := directread.NewHTTPGraphQLClientWithHTTP(upstream.server.URL, 5*time.Second, upstream.server.Client())
		if err != nil {
			t.Fatal(err)
		}
		runnerGate := gate
		if runnerGate == nil {
			runnerGate = directread.NewSubjectGate(nil, nil)
		}
		runner, err := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{Policy: policy, Gate: runnerGate, Client: client, Grants: grants})
		if err != nil {
			t.Fatal(err)
		}
		deps.DataGraphQL = runner
	}
	return &c7072Harness{chaos7071Harness: newChaos7071Harness(t, 100, configure), upstream: upstream}
}

func graphqlBody(query string, extra string) string {
	q, _ := json.Marshal(query)
	if extra != "" {
		return `{"query":` + string(q) + `,` + extra + `}`
	}
	return `{"query":` + string(q) + `}`
}

func hotspotsRowsAnswer(key string, ids ...string) func(map[string]any) string {
	return func(map[string]any) string {
		rows := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, map[string]any{"repoId": id, "filePath": "internal/x.go"})
		}
		encoded, _ := json.Marshal(map[string]any{"data": map[string]any{key: map[string]any{"rows": rows}}})
		return string(encoded)
	}
}

const c7075Hotspots = `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { repoId filePath } } }`

// The protection chain: 401 without a bearer, 403 without data:read, 501
// data_graphql_not_configured without a runner, 503 without a gate; a
// served query and a refusal are both 200.
func TestChaos7075GraphQLRouteProtectionAndStates(t *testing.T) {
	h := newC7075Harness(t, c7075Options{})
	contextOnly := h.issueFor(t, c7072Context, c7072AllRepos, nil).Token
	unrestricted := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	assertErrorResponse(t, h.post(ContextFabricDataGraphQLPath, "", graphqlBody(c7075Hotspots, "")), http.StatusUnauthorized, "invalid_token")
	assertErrorResponse(t, h.post(ContextFabricDataGraphQLPath, contextOnly, graphqlBody(c7075Hotspots, "")), http.StatusForbidden, "insufficient_scope")
	assertErrorResponse(t, h.post(ContextFabricDataGraphQLPath, unrestricted, `{"query":"{ x }","extra":1}`), http.StatusBadRequest, "invalid_request")

	h.upstream.respond = hotspotsRowsAnswer("hotspots", c7072RepoA)
	served := decodeC7072(t, h.post(ContextFabricDataGraphQLPath, unrestricted, graphqlBody(c7075Hotspots, "")))
	if served["call"] != "served" || h.upstream.count() != 1 {
		t.Fatalf("served: %v (%d upstream)", served, h.upstream.count())
	}
	if req := served["request"].(map[string]any); len(req) != 1 || req["max_bytes"] == nil {
		t.Fatalf("request echo carries more than max_bytes: %v", req)
	}
	if sent := h.upstream.bodies[0]["query"].(string); !strings.HasPrefix(sent, "query AcrGraphQLQuery") {
		t.Fatalf("the client text reached the listener: %s", sent)
	}
	// pr2 r3 P2: the route never echoes a client alias.
	if raw := h.post(ContextFabricDataGraphQLPath, unrestricted, graphqlBody(`{ PrivateMarker: catalog(dimension: TEAM) { values { value } } PrivateMarker: catalog(dimension: TEAM) { values { count } } }`, "")).Body.String(); strings.Contains(raw, "PrivateMarker") || !strings.Contains(raw, "repeated_root_key") {
		t.Fatalf("the route echoes a client alias: %s", raw)
	}
	refused := decodeC7072(t, h.post(ContextFabricDataGraphQLPath, unrestricted, graphqlBody(`mutation { deleteSavedReport(orgId: "o", id: "x") }`, "")))
	if refused["call"] != "refused" || refused["refusal"].(map[string]any)["code"] != "operation_type_not_allowed" || h.upstream.count() != 1 {
		t.Fatalf("refused: %v", refused)
	}

	off := newC7075Harness(t, c7075Options{noGraphQL: true})
	offToken := off.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	response := off.post(ContextFabricDataGraphQLPath, offToken, graphqlBody(c7075Hotspots, ""))
	assertErrorResponse(t, response, http.StatusNotImplemented, "feature_not_enabled")
	if !strings.Contains(response.Body.String(), "data_graphql_not_configured") {
		t.Fatalf("501 without the reason: %s", response.Body.String())
	}

	noGate := newC7075Harness(t, c7075Options{noGraph: true})
	noGateToken := noGate.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	assertErrorResponse(t, noGate.post(ContextFabricDataGraphQLPath, noGateToken, graphqlBody(c7075Hotspots, "")), http.StatusServiceUnavailable, "upstream_unavailable")
	// pr2 r2: the catalog agrees with the route: no gate = no schema or
	// operation detail, reason subject_gate_unavailable.
	body := decodeC7072(t, noGate.get(ContextFabricDataCatalogPath+"?sections=schema", noGateToken))
	sec := body["schema"].(map[string]any)
	if sec["available"] != false || sec["reason"] != "subject_gate_unavailable" || len(sec["roots"].([]any)) != 0 || sec["sdl"] != "" {
		t.Fatalf("no gate: schema section %v %v roots=%d", sec["available"], sec["reason"], len(sec["roots"].([]any)))
	}
}

// graphql_query is advertised exactly when run_operation's rule holds for
// its own runner: entitled, data:read, runner composed, gate composed.
func TestChaos7075CapabilitiesAdvertiseGraphQLQuery(t *testing.T) {
	tools := func(h *c7072Harness, scopes []string) []string {
		t.Helper()
		token := h.issueFor(t, scopes, c7072AllRepos, nil).Token
		body := decodeC7072(t, h.get("/api/v1/agent-context/capabilities", token))
		var out []string
		for _, name := range body["enabled_tools"].([]any) {
			out = append(out, name.(string))
		}
		return out
	}
	full := newC7075Harness(t, c7075Options{})
	if got := tools(full, c7072Data); !slices.Contains(got, "graphql_query") {
		t.Fatalf("composed + data:read: %v", got)
	}
	if got := tools(full, c7072Context); slices.Contains(got, "graphql_query") {
		t.Fatalf("no data:read: %v", got)
	}
	if got := tools(newC7075Harness(t, c7075Options{noGraphQL: true}), c7072Data); slices.Contains(got, "graphql_query") {
		t.Fatalf("no runner: %v", got)
	}
	if got := tools(newC7075Harness(t, c7075Options{noGraph: true}), c7072Data); slices.Contains(got, "graphql_query") {
		t.Fatalf("no gate: %v", got)
	}
}

// The data_catalog schema section is built for the caller class from the
// same root policy: 14 roots for an unrestricted credential, the 3 roots
// served to a restricted one (every other root listed as refused), and the
// availability reasons.
func TestChaos7075CatalogSchemaSectionPerCallerClass(t *testing.T) {
	h := newC7075Harness(t, c7075Options{})
	unrestricted := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	restricted := h.issueFor(t, c7072Data, []string{hostedTestRepository}, nil).Token
	contextOnly := h.issueFor(t, c7072Context, c7072AllRepos, nil).Token
	section := func(token string, h *c7072Harness) map[string]any {
		body := decodeC7072(t, h.get(ContextFabricDataCatalogPath+"?sections=schema", token))
		s, ok := body["schema"].(map[string]any)
		if !ok {
			t.Fatalf("no schema section: %v", body)
		}
		return s
	}
	roots := func(s map[string]any) []string {
		var out []string
		for _, r := range s["roots"].([]any) {
			out = append(out, r.(map[string]any)["field"].(string))
		}
		return out
	}
	u := section(unrestricted, h)
	if u["available"] != true || len(roots(u)) != 14 || !strings.Contains(u["sdl"].(string), "type Query {") {
		t.Fatalf("unrestricted: available=%v roots=%v", u["available"], roots(u))
	}
	if strings.Contains(u["sdl"].(string), "busFactor") || strings.Contains(u["sdl"].(string), "topMaintainers") {
		t.Fatal("the schema view names a refused root or a person field")
	}
	r := section(restricted, h)
	if got := roots(r); !slices.Equal(got, []string{"compoundingRisk", "hotspots"}) {
		t.Fatalf("restricted roots %v", got)
	}
	for _, root := range r["roots"].([]any) {
		entry := root.(map[string]any)
		if entry["scope_class"] != "forced_grant" || entry["forced_argument"] == "" || len(entry["row_id_paths"].([]any)) == 0 {
			t.Fatalf("restricted root without forced scope: %v", entry)
		}
	}
	if refusedRoots := r["refused_root_fields"].([]any); !slices.ContainsFunc(refusedRoots, func(v any) bool { return v == "catalog" }) {
		t.Fatalf("catalog not listed as refused for a restricted caller: %v", refusedRoots)
	}
	noDetail := func(label string, s map[string]any) {
		t.Helper()
		if len(s["roots"].([]any)) != 0 || s["sdl"] != "" || len(s["refused_root_fields"].([]any)) != 0 {
			t.Fatalf("%s: an unavailable section carries schema detail: roots=%d sdl=%d refused=%d", label, len(s["roots"].([]any)), len(s["sdl"].(string)), len(s["refused_root_fields"].([]any)))
		}
	}
	c := section(contextOnly, h)
	if c["available"] != false || c["reason"] != "scope_missing_data_read" {
		t.Fatalf("no data:read: %v %v", c["available"], c["reason"])
	}
	noDetail("no data:read", c)
	off := newC7075Harness(t, c7075Options{noGraphQL: true})
	offToken := off.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	o := section(offToken, off)
	if o["available"] != false || o["reason"] != "data_graphql_not_configured" {
		t.Fatalf("no runner: %v %v", o["available"], o["reason"])
	}
	noDetail("no runner", o)
	// Every argument path the section lists for hotspots is one the runner
	// accepts; orgId is never listed.
	for _, root := range u["roots"].([]any) {
		entry := root.(map[string]any)
		for _, arg := range entry["arguments"].([]any) {
			a := arg.(map[string]any)
			for _, p := range a["paths"].([]any) {
				if strings.HasSuffix(p.(map[string]any)["path"].(string), "orgId") {
					t.Fatalf("%s lists an orgId path", entry["field"])
				}
			}
		}
	}
}

// The real route answers validate against the published schemas, in every
// state the harness reaches (the response types live in directread and this
// package, so this stands in for the contracts/v1 parity registry).
func TestChaos7075RealGraphQLAnswersValidateAgainstThePublishedSchemas(t *testing.T) {
	h := newC7075Harness(t, c7075Options{})
	unrestricted := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	restricted := h.issueFor(t, c7072Data, []string{hostedTestRepository}, nil).Token
	schema := dataSchemaFor(t, "mcp_graphql_query_response.v1.schema.json")
	answers := 0
	check := func(label, token, body string) {
		t.Helper()
		response := h.post(ContextFabricDataGraphQLPath, token, body)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", label, response.Code, response.Body.String())
		}
		assertValidAgainstSchema(t, schema, "graphql/"+label, response.Body.Bytes())
		answers++
	}
	h.upstream.respond = hotspotsRowsAnswer("hotspots", c7072RepoA)
	check("served", unrestricted, graphqlBody(c7075Hotspots, ""))
	check("served restricted", restricted, graphqlBody(`{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, ""))
	check("mutation", unrestricted, graphqlBody(`mutation { deleteSavedReport(orgId: "o", id: "x") }`, ""))
	check("introspection", unrestricted, graphqlBody(`{ __schema { types { name } } }`, ""))
	check("person root", unrestricted, graphqlBody(`{ busFactor(orgId: "o") { topMaintainers { author } } }`, ""))
	check("fragment", unrestricted, graphqlBody(`fragment F on Query { __typename } { ...F }`, ""))
	check("foreign id", restricted, graphqlBody(`{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z", repoIds: ["repository:`+c7072RepoB+`"]}) { rows { filePath } } }`, ""))
	check("over budget", unrestricted, graphqlBody(c7075Hotspots, `"max_bytes":10`))
	h.upstream.respond = hotspotsRowsAnswer("hotspots", c7072RepoA)
	check("max_bytes 0 = default", unrestricted, graphqlBody(c7075Hotspots, `"max_bytes":0`))
	// pr2 r1 P1: max_bytes 0 is a valid MCP request (the default), as the
	// Go validator and the OpenAPI say.
	request := dataSchemaFor(t, "mcp_graphql_query_request.v1.schema.json")
	assertValidAgainstSchema(t, request, "request/max_bytes 0", []byte(graphqlBody(c7075Hotspots, `"max_bytes":0`)))
	check("class refused", restricted, graphqlBody(`{ catalog(dimension: TEAM) { values { value } } }`, ""))
	h.upstream.respond = func(map[string]any) string {
		return `{"errors":[{"message":"x","extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":"bytes_ceiling"}}],"data":null}`
	}
	check("read budget", unrestricted, graphqlBody(c7075Hotspots, ""))
	h.upstream.respond = func(map[string]any) string {
		return `{"errors":[{"message":"x","extensions":{"code":"MCP_READ_BUDGET_EXCEEDED","reason":"rows_ceiling"}}],"data":null}`
	}
	check("read budget rows", unrestricted, graphqlBody(c7075Hotspots, ""))
	h.upstream.respond = func(map[string]any) string { return `{"errors":[{"message":"boom secret"}],"data":null}` }
	check("upstream error", unrestricted, graphqlBody(c7075Hotspots, ""))
	h.upstream.respond = hotspotsRowsAnswer("hotspots")
	check("empty", unrestricted, graphqlBody(c7075Hotspots, ""))

	catalog := dataSchemaFor(t, "mcp_data_catalog_response.v1.schema.json")
	for label, token := range map[string]string{"unrestricted": unrestricted, "restricted": restricted} {
		response := h.get(ContextFabricDataCatalogPath+"?sections=schema,operations", token)
		assertValidAgainstSchema(t, catalog, "catalog schema/"+label, response.Body.Bytes())
		answers++
	}
	if answers < 14 {
		t.Fatalf("only %d real answers were validated", answers)
	}
	_ = auth.ScopeDataRead
}

// pr2 r2: the OpenAPI graphql_query response is as closed as the JSON
// Schema: every object the schema closes (additionalProperties:false) is
// closed at the same path in the OpenAPI component.
func TestChaos7075OpenAPIGraphQLResponseIsAsClosedAsTheSchema(t *testing.T) {
	read := func(parts ...string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(repopath.Path(t, parts...))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	schema := read("contracts", "jsonschema", "v1", "mcp_graphql_query_response.v1.schema.json")
	openapi := read("contracts", "openapi", "acr-v1.json")
	component := openapi["components"].(map[string]any)["schemas"].(map[string]any)["DataGraphQLResponse"].(map[string]any)
	closed := 0
	var walk func(s, o map[string]any, path string)
	walk = func(s, o map[string]any, path string) {
		if s["additionalProperties"] == false {
			closed++
			if o == nil || o["additionalProperties"] != false {
				t.Errorf("%s: closed in the JSON Schema, open in the OpenAPI", path)
			}
		}
		sp, _ := s["properties"].(map[string]any)
		op, _ := o["properties"].(map[string]any)
		for name, child := range sp {
			cs, _ := child.(map[string]any)
			co, _ := op[name].(map[string]any)
			walk(cs, co, path+"."+name)
		}
		if items, ok := s["items"].(map[string]any); ok {
			oi, _ := o["items"].(map[string]any)
			walk(items, oi, path+"[]")
		}
	}
	walk(schema, component, "response")
	if closed < 5 {
		t.Fatalf("only %d closed objects compared", closed)
	}
}
