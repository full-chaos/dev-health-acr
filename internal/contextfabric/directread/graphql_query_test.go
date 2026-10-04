package directread_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The root allowlist is DERIVED from the run_operation catalogue (one fact):
// the allowed roots are exactly the root fields of the served operations,
// each candidate is a served operation, and the committed
// contracts/mcp/graphql_roots.v1.json and the embedded SDL equal what the
// derivation and the vendored SDL give (lead ruling R7).
func TestGraphQLRootPolicyIsDerivedFromTheCatalogue(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	cat := policy.Catalogue()
	want := map[string][]string{}
	excluded := map[string]bool{}
	for _, name := range directread.GraphQLDesignExcludedRoots() {
		excluded[name] = true
	}
	servedRoots := map[string]bool{}
	for _, class := range directread.CallerClassVocabulary() {
		for _, op := range cat.Operations(class) {
			doc, err := parser.ParseQuery(&ast.Source{Input: op.DocumentText})
			if err != nil {
				t.Fatal(err)
			}
			root := doc.Operations[0].SelectionSet[0].(*ast.Field).Name
			servedRoots[root] = true
			if excluded[root] {
				continue // design D.8: not a graphql_query root
			}
			if !slices.Contains(want[root], op.Name) {
				want[root] = append(want[root], op.Name)
			}
		}
	}
	got := map[string][]string{}
	for _, root := range policy.Roots() {
		got[root.Field] = sortedStrings(root.Operations())
	}
	if len(got) != len(want) {
		t.Fatalf("derived %d roots, catalogue has %d: %v vs %v", len(got), len(want), got, want)
	}
	for root, ops := range want {
		if !slices.Equal(got[root], sortedStrings(ops)) {
			t.Errorf("root %s: derived %v, catalogue %v", root, got[root], sortedStrings(ops))
		}
	}
	for _, refused := range policy.RefusedRootFields() {
		if _, ok := got[refused]; ok {
			t.Errorf("root %s both allowed and refused", refused)
		}
	}
	// The design exclusion cannot go stale: each excluded root is still a
	// served run_operation root, and is refused by graphql_query.
	for name := range excluded {
		if !servedRoots[name] {
			t.Errorf("design-excluded root %s is no longer a served run_operation root: drop it from the exclusion", name)
		}
		if !slices.Contains(policy.RefusedRootFields(), name) {
			t.Errorf("design-excluded root %s is not refused", name)
		}
	}
	for _, name := range []string{"busFactor", "pr", "reviewEdges", "savedReports", "dataHealth", "productTelemetryDashboard"} {
		if !slices.Contains(policy.RefusedRootFields(), name) {
			t.Errorf("%s is not refused", name)
		}
	}

	root := filepath.Join("..", "..", "..")
	rendered, err := policy.RootsFileJSON("cmd/operationpolicy")
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join(root, "contracts", "mcp", "graphql_roots.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rendered, committed) {
		t.Fatal("contracts/mcp/graphql_roots.v1.json differs from the derived policy: run go run ./cmd/operationpolicy")
	}
	sdl, err := os.ReadFile(filepath.Join(root, "contracts", "mcp", "ops-catalogue", "schema.graphql"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sdl, directread.EmbeddedOpsSchema()) {
		t.Fatal("embedded ops_schema.graphql differs from the vendored SDL")
	}
}

// A derivation over an SDL other than the catalogue's is refused (policy
// stale), not silently used.
func TestGraphQLPolicyRefusesAStaleSDL(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	stale := append(directread.EmbeddedOpsSchema(), []byte("\n# drift\n")...)
	if _, err := directread.NewGraphQLPolicy(policy.Catalogue(), stale, directread.DefaultGraphQLLimits()); err == nil {
		t.Fatal("a drifted SDL derived a policy")
	}
}

// rootVars is a served request per operation: opMinimalVariables plus the
// values an operation needs that its policy does not generate.
func rootVars(t *testing.T, op *directread.OperationPolicy) map[string]any {
	t.Helper()
	vars := opMinimalVariables(t, op)
	switch op.Name {
	case "catalogValues":
		vars["dimension"] = "TEAM"
	case "complexityTimeseries":
		opMerge(vars, "input.granularity", "DAY")
		opMerge(vars, "input.scope", "REPO")
	case "throughputForecast":
		vars["input"] = map[string]any{}
	case "investmentBreakdown", "investmentFull":
		vars["batch"] = map[string]any{"breakdowns": []any{map[string]any{
			"dimension": "THEME", "measure": "COUNT",
			"dateRange": map[string]any{"startDate": "2026-09-01", "endDate": "2026-09-28"},
		}}}
	}
	return vars
}

// Per-root proof (design D.8 "a proof per ROOT FIELD"): for EVERY allowed
// root field and every candidate operation, a query generated from the
// policy (every allowed output selected) round-trips through the fake MCP
// listener: served, one request, nothing the listener refused, the answer
// keyed by the root, only selected paths, and the rebuilt text carries no
// client text. A root with no case fails.
func TestGraphQLEveryAllowedRootFieldRoundTrips(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	cases := 0
	for field, ops := range gqlRootOps(t, h.policy) {
		for _, op := range ops {
			t.Run(field+"/"+op.Name, func(t *testing.T) {
				h.listener.reset()
				q := gqlQueryFor(t, op, rootVars(t, op), nil, "")
				resp := h.run(t, opUnrestricted(opOrgA), q.text, q.vars)
				h.wantServed(t, resp)
				if len(resp.RootFields) != 1 || resp.RootFields[0].Field != field || resp.RootFields[0].Key != field || resp.RootFields[0].Operation != op.Name {
					t.Fatalf("root_fields %+v", resp.RootFields)
				}
				data := gqlDecode(t, resp.Data)
				if _, ok := data[field]; !ok || len(data) != 1 {
					t.Fatalf("data keys %v, want only %s", data, field)
				}
				sent := h.listener.requests()[0]
				if strings.Contains(sent.Query, "query Client") || !strings.HasPrefix(sent.Query, "query AcrGraphQLQuery") {
					t.Fatalf("the client text reached the wire:\n%s", sent.Query)
				}
				if sent.Header.Get(directread.HeaderInternalOrgID) != opOrgA {
					t.Fatal("org header is not the principal's")
				}
			})
			cases++
		}
	}
	if cases != 15 {
		t.Fatalf("ran %d root/operation cases, want 15 (one per served operation)", cases)
	}
}
