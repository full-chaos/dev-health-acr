package directread_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// captureClient records the text acr sends and answers null data.
type captureClient struct{ sent []directread.QueryCall }

func (c *captureClient) Execute(_ context.Context, call directread.QueryCall) (directread.QueryResult, error) {
	c.sent = append(c.sent, call)
	return directread.QueryResult{Body: []byte(`{"data":{}}`), StatusCode: 200}, nil
}

var nonName = regexp.MustCompile(`[^A-Za-z0-9_]`)

// rebuildSeeds are hostile shapes for the rebuilt-query path: string,
// block-string, enum and number literals and variables carrying GraphQL
// syntax, aliases, repeated roots, defaults, nested lists and objects.
var rebuildSeeds = []struct{ query, vars string }{
	{`{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z\") { __schema { types { name } } } #", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, ``},
	{`query($s: DateTime!) { hotspots(input: {sinceUtc: $s, untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`, `{"s":"x\") } mutation { deleteSavedReport(orgId: \"o\", id: \"1\") } #"}`},
	{`{ securityOverview(filters: {search: """ ") } mutation { x } """}) { kpis { openTotal } } }`, ``},
	{`query($q: String = "} mutation { triggerReport }") { securityOverview(filters: {search: $q}) { kpis { openTotal } } }`, ``},
	{`{ a: catalog(dimension: TEAM) { values { value } } b: catalog(dimension: REPO) { values { count } } }`, ``},
	{`{ x_y1: catalog(dimension: THEME) { values { value } } }`, ``},
	{`query($ids: [String!]) { hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z", repoIds: $ids}) { rows { repoId } } }`, `{"ids":["repository:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]}`},
	{`{ compoundingRisk(filter: {breakout: REPO, trendDays: 30}) { rows { scopeId } trend { day } } }`, ``},
	{`{ catalog(dimension: "REPO") { values { value } } }`, ``},
	{`{ workGraphEdges(filters: {nodeId: "a\nb\u0000c } { __typename", limit: 5}) { edges { edgeId } } }`, ``},
}

// For every seed (and every input the fuzzer finds), whenever acr sends a
// query: the sent text parses as exactly one query named AcrGraphQLQuery;
// every argument value in it is a variable or a literal of a registered
// document; no client string that could not be a GraphQL name appears in
// the text; and the org on the wire is the principal's.
func FuzzGraphQLRebuildNeverPrintsClientValues(f *testing.F) {
	for _, s := range rebuildSeeds {
		f.Add(s.query, s.vars)
	}
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		f.Fatal(err)
	}
	literals := registeredLiterals(f, policy)
	f.Fuzz(func(t *testing.T, query, vars string) {
		client := &captureClient{}
		runner, err := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{Policy: policy, Gate: directread.NewSubjectGate(newOpGraph(), nil), Client: client, Grants: opDefaultGrants(), Now: func() time.Time { return opNow }})
		if err != nil {
			t.Fatal(err)
		}
		var raw json.RawMessage
		if json.Valid([]byte(vars)) {
			raw = json.RawMessage(vars)
		}
		_, _ = runner.Run(context.Background(), opUnrestricted(opOrgA), directread.GraphQLRequest{Query: query, Variables: raw})
		_, _ = runner.Run(context.Background(), opRestrictedA(), directread.GraphQLRequest{Query: query, Variables: raw})
		for _, call := range client.sent {
			checkSentQuery(t, call, query, vars, literals)
		}
	})
}

func registeredLiterals(tb testing.TB, policy *directread.GraphQLPolicy) map[string]bool {
	tb.Helper()
	out := map[string]bool{}
	for _, root := range policy.Roots() {
		for _, name := range root.Operations() {
			op, _ := policy.Catalogue().Lookup(name)
			doc, err := parser.ParseQuery(&ast.Source{Input: op.DocumentText})
			if err != nil {
				tb.Fatal(err)
			}
			for _, a := range doc.Operations[0].SelectionSet[0].(*ast.Field).Arguments {
				if a.Value.Kind != ast.Variable {
					out[a.Value.String()] = true
				}
			}
		}
	}
	return out
}

func checkSentQuery(t *testing.T, call directread.QueryCall, query, vars string, literals map[string]bool) {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Input: call.Document})
	if err != nil {
		t.Fatalf("sent text does not parse: %v\n%s", err, call.Document)
	}
	if len(doc.Operations) != 1 || len(doc.Fragments) != 0 || doc.Operations[0].Operation != ast.Query || doc.Operations[0].Name != "AcrGraphQLQuery" {
		t.Fatalf("sent text is not one AcrGraphQLQuery query:\n%s", call.Document)
	}
	var walk func(set ast.SelectionSet)
	walk = func(set ast.SelectionSet) {
		for _, sel := range set {
			f, ok := sel.(*ast.Field)
			if !ok {
				t.Fatalf("sent text has a fragment:\n%s", call.Document)
			}
			if len(f.Directives) > 0 {
				t.Fatalf("sent text has a directive:\n%s", call.Document)
			}
			for _, a := range f.Arguments {
				if a.Value.Kind != ast.Variable && !literals[a.Value.String()] {
					t.Fatalf("sent text prints a non-registered literal %s:\n%s", a.Value.String(), call.Document)
				}
			}
			walk(f.SelectionSet)
		}
	}
	walk(doc.Operations[0].SelectionSet)
	if call.OrgID != opOrgA {
		t.Fatalf("wire org %q", call.OrgID)
	}
	// Every client string that could not be a GraphQL name must stay out of
	// the text (it may only travel as a variable value).
	for _, s := range clientStrings(query, vars) {
		if len(s) >= 3 && nonName.MatchString(s) && strings.Contains(call.Document, s) {
			t.Fatalf("client string %q reached the sent text:\n%s", s, call.Document)
		}
	}
}

func clientStrings(query, vars string) []string {
	var out []string
	if doc, err := parser.ParseQuery(&ast.Source{Input: query}); err == nil {
		var value func(v *ast.Value)
		value = func(v *ast.Value) {
			if v == nil {
				return
			}
			if v.Kind == ast.StringValue || v.Kind == ast.BlockValue {
				out = append(out, v.Raw)
			}
			for _, c := range v.Children {
				value(c.Value)
			}
		}
		var walk func(set ast.SelectionSet)
		walk = func(set ast.SelectionSet) {
			for _, sel := range set {
				if f, ok := sel.(*ast.Field); ok {
					for _, a := range f.Arguments {
						value(a.Value)
					}
					walk(f.SelectionSet)
				}
			}
		}
		for _, op := range doc.Operations {
			for _, v := range op.VariableDefinitions {
				value(v.DefaultValue)
			}
			walk(op.SelectionSet)
		}
	}
	var decoded any
	if json.Unmarshal([]byte(vars), &decoded) == nil {
		var walk func(v any)
		walk = func(v any) {
			switch t := v.(type) {
			case string:
				out = append(out, t)
			case []any:
				for _, c := range t {
					walk(c)
				}
			case map[string]any:
				for _, c := range t {
					walk(c)
				}
			}
		}
		walk(decoded)
	}
	return out
}

// The seeds that are admitted do reach the wire (the fuzz property is not
// vacuous): at least the alias, repeated-root, forced-scope and quoted-enum
// seeds are dispatched.
func TestGraphQLRebuildSeedsReachTheWire(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	literals := registeredLiterals(t, policy)
	dispatched := 0
	for _, s := range rebuildSeeds {
		client := &captureClient{}
		runner, _ := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{Policy: policy, Gate: directread.NewSubjectGate(newOpGraph(), nil), Client: client, Grants: opDefaultGrants(), Now: func() time.Time { return opNow }})
		var raw json.RawMessage
		if s.vars != "" {
			raw = json.RawMessage(s.vars)
		}
		_, _ = runner.Run(context.Background(), opUnrestricted(opOrgA), directread.GraphQLRequest{Query: s.query, Variables: raw})
		for _, call := range client.sent {
			checkSentQuery(t, call, s.query, s.vars, literals)
			dispatched++
		}
	}
	if dispatched < 6 {
		t.Fatalf("only %d seeds reached the wire; the property measured too little", dispatched)
	}
}
