package directread_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// ------------------------------------------------------ R2, real catalogue

// The two two-operation roots are NOT disjoint on the real catalogue:
// catalog(dimension: REPO) is admitted by acrRepositoryScopes and by
// catalogValues, and a breakdowns-only analytics selection by
// investmentBreakdown and investmentFull. This test pins that overlap
// exactly, so a catalogue change that widens either candidate fails loudly
// here: equal variable rules (reason text and fixed literals aside), the
// restricted class refused by both, and the known output overlap.
func TestGraphQLTwoOperationRootOverlapIsPinned(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	cat := policy.Catalogue()
	lookup := func(name string) *directread.OperationPolicy {
		op, refusal := cat.Lookup(name)
		if refusal != nil {
			t.Fatalf("%s: %v", name, refusal)
		}
		return op
	}
	type pair struct {
		root, strict, other       string
		onlyStrict, onlyOther     []string // outputs admitted by one candidate only
		strictOnlyVars, otherOnly []string // variable paths only one candidate has
	}
	pairs := []pair{
		{root: "capacityForecast", strict: "capacityCompletionDistribution", other: "capacityForecast", onlyOther: []string{"capacityForecast.backlogSize", "capacityForecast.completionDistribution.days[*].cumulativeShare", "capacityForecast.completionDistribution.horizonDays", "capacityForecast.completionDistribution.items[*].cumulativeShare", "capacityForecast.completionDistribution.runs", "capacityForecast.completionDistribution.unfinishedRuns", "capacityForecast.computedAt", "capacityForecast.forecastId", "capacityForecast.highVariance", "capacityForecast.historyDays", "capacityForecast.insufficientHistory", "capacityForecast.p50Date", "capacityForecast.p50Days", "capacityForecast.p50Items", "capacityForecast.p85Date", "capacityForecast.p85Days", "capacityForecast.p85Items", "capacityForecast.p95Date", "capacityForecast.p95Days", "capacityForecast.p95Items", "capacityForecast.targetDate", "capacityForecast.targetItems", "capacityForecast.teamId", "capacityForecast.throughputMean", "capacityForecast.throughputStddev", "capacityForecast.workScopeId"}},
		{root: "catalog", strict: "acrRepositoryScopes", other: "catalogValues", otherOnly: []string{"dimension"}},
		{
			root: "analytics", strict: "investmentBreakdown", other: "investmentFull",
			onlyStrict: []string{
				"analytics.evidenceQualityDistribution", "analytics.evidenceQualityStats.__typename",
				"analytics.evidenceQualityStats.bandCounts", "analytics.evidenceQualityStats.mean",
				"analytics.evidenceQualityStats.stddev", "analytics.evidenceQualityStats.total",
			},
			onlyOther: []string{
				"analytics.sankey.__typename", "analytics.sankey.coverage.__typename", "analytics.sankey.coverage.repoCoverage",
				"analytics.sankey.coverage.teamCoverage", "analytics.sankey.edges[*].__typename", "analytics.sankey.edges[*].source",
				"analytics.sankey.edges[*].target", "analytics.sankey.edges[*].value", "analytics.sankey.nodes[*].__typename",
				"analytics.sankey.nodes[*].dimension", "analytics.sankey.nodes[*].id", "analytics.sankey.nodes[*].label",
				"analytics.sankey.nodes[*].value", "analytics.sankey.unit",
			},
		},
	}
	if len(pairs) != countTwoOpRoots(policy) {
		t.Fatalf("the catalogue has %d multi-operation roots; this test pins %d", countTwoOpRoots(policy), len(pairs))
	}
	for _, p := range pairs {
		root, _ := policy.Root(p.root)
		if got := root.Operations(); !slices.Equal(got, []string{p.strict, p.other}) {
			t.Fatalf("%s candidates %v, want [%s %s]", p.root, got, p.strict, p.other)
		}
		a, b := lookup(p.strict), lookup(p.other)
		for _, op := range []*directread.OperationPolicy{a, b} {
			if op.Scope(directread.CallerRestricted).Served {
				t.Fatalf("%s is served to a restricted caller: the overlap now differs by caller class", op.Name)
			}
		}
		// The root is charged the MAX cost weight of its candidates, whichever
		// one maps the shape (lead ruling on overlaps).
		if want := max(directread.GraphQLCostWeight(a.CostClass), directread.GraphQLCostWeight(b.CostClass)); directread.GraphQLCostWeight(root.CostClass()) != want {
			t.Fatalf("%s charged weight %d, want the max %d", p.root, directread.GraphQLCostWeight(root.CostClass()), want)
		}
		if a.CostClass != b.CostClass || a.DeadlineSeconds != b.DeadlineSeconds || a.MaxInFlightPerOrg != b.MaxInFlightPerOrg {
			t.Fatalf("%s: cost/deadline/concurrency differ between candidates", p.root)
		}
		va, vb := normalizedRules(a), normalizedRules(b)
		if got := keyDiff(va, vb); !slices.Equal(got, p.strictOnlyVars) {
			t.Errorf("%s: variable paths only in %s: %v, want %v", p.root, a.Name, got, p.strictOnlyVars)
		}
		if got := keyDiff(vb, va); !slices.Equal(got, p.otherOnly) {
			t.Errorf("%s: variable paths only in %s: %v, want %v", p.root, b.Name, got, p.otherOnly)
		}
		for path, rule := range va {
			if other, ok := vb[path]; ok && !reflect.DeepEqual(rule, other) {
				t.Errorf("%s: variable rule %s differs between %s and %s", p.root, path, a.Name, b.Name)
			}
		}
		if !reflect.DeepEqual(normalizedConstraints(a.Constraints), normalizedConstraints(b.Constraints)) {
			t.Errorf("%s: constraints differ between candidates", p.root)
		}
		oa, ob := outputSet(a), outputSet(b)
		if got := keyDiff(oa, ob); !slices.Equal(got, p.onlyStrict) {
			t.Errorf("%s: outputs only in %s: %v", p.root, a.Name, got)
		}
		if got := keyDiff(ob, oa); !slices.Equal(got, p.onlyOther) {
			t.Errorf("%s: outputs only in %s: %v", p.root, b.Name, got)
		}
	}
}

func countTwoOpRoots(policy *directread.GraphQLPolicy) int {
	n := 0
	for _, root := range policy.Roots() {
		if len(root.Operations()) > 1 {
			n++
		}
	}
	return n
}

// normalizedRules keys an operation's variable rules by path, with the
// free-text refusal reasons blanked (they name the operation).
func normalizedRules(op *directread.OperationPolicy) map[string]directread.VariableRule {
	out := map[string]directread.VariableRule{}
	for _, rule := range op.Variables {
		r := rule
		if r.Refusal != nil {
			copied := *r.Refusal
			copied.Reason = ""
			r.Refusal = &copied
		}
		out[r.Path] = r
	}
	return out
}

func normalizedConstraints(in []directread.Constraint) []directread.Constraint {
	out := append([]directread.Constraint(nil), in...)
	for i := range out {
		out[i].Reason = ""
	}
	return out
}

func outputSet(op *directread.OperationPolicy) map[string]directread.VariableRule {
	out := map[string]directread.VariableRule{}
	for _, o := range op.Outputs {
		out[o.Path] = directread.VariableRule{}
	}
	return out
}

func keyDiff[T any](a, b map[string]T) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// On every shape both candidates admit, the query acr sends is the same
// whichever candidate maps it: the mapping decides only the policy name.
func TestGraphQLOverlapShapesSendTheSameQueryUnderEitherCandidate(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	batch := `{breakdowns: [{dimension: THEME, measure: COUNT, dateRange: {startDate: "2026-09-01", endDate: "2026-09-28"}}]}`
	for _, q := range []struct{ query, want string }{
		{`{ catalog(dimension: REPO) { values { value count } } }`, "acrRepositoryScopes"},
		{`{ analytics(batch: ` + batch + `) { breakdowns { dimension items { key value } } } }`, "investmentBreakdown"},
	} {
		h.listener.reset()
		resp := h.run(t, opUnrestricted(opOrgA), q.query, nil)
		h.wantServed(t, resp)
		if resp.RootFields[0].Operation != q.want {
			t.Fatalf("mapped to %s, want %s", resp.RootFields[0].Operation, q.want)
		}
	}
}

// --------------------------------------------- R2, synthetic catalogues

// syntheticCatalogue builds a catalogue from copies of real operations:
// each spec names a source operation, a new name and an edit. The registry
// row count is set to the number of operations. The SDL digest is kept, so
// NewGraphQLPolicy derives over the embedded SDL.
func syntheticCatalogue(t *testing.T, specs []synthSpec) *directread.Catalogue {
	t.Helper()
	var file map[string]any
	dec := json.NewDecoder(bytes.NewReader(directread.EmbeddedCatalogueJSON()))
	dec.UseNumber()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, raw := range file["operations"].([]any) {
		op := raw.(map[string]any)
		byName[op["name"].(string)] = op
	}
	var ops []any
	for _, spec := range specs {
		src, ok := byName[spec.from]
		if !ok {
			t.Fatalf("no operation %s", spec.from)
		}
		var clone map[string]any
		b, _ := json.Marshal(src)
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&clone); err != nil {
			t.Fatal(err)
		}
		clone["name"] = spec.name
		if spec.edit != nil {
			spec.edit(clone)
		}
		ops = append(ops, clone)
	}
	file["operations"] = ops
	file["not_served"] = []any{}
	source := file["source"].(map[string]any)
	source["registry_rows"] = json.Number(fmt.Sprint(len(ops)))
	out, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := directread.LoadCatalogue(out)
	if err != nil {
		t.Fatalf("synthetic catalogue does not load: %v", err)
	}
	return cat
}

type synthSpec struct {
	from, name string
	edit       func(op map[string]any)
}

func refuseRestricted(op map[string]any) {
	for _, raw := range op["scopes"].([]any) {
		s := raw.(map[string]any)
		if s["caller"] == "restricted" {
			for k := range s {
				if k != "caller" {
					delete(s, k)
				}
			}
			s["served"] = false
			s["refusal"] = map[string]any{"code": "operation_not_served_for_caller", "reason": "synthetic"}
			s["basis"] = "synthetic"
		}
	}
}

func dropOutput(path string) func(op map[string]any) {
	return func(op map[string]any) {
		var kept []any
		for _, raw := range op["outputs"].([]any) {
			if raw.(map[string]any)["path"] != path {
				kept = append(kept, raw)
			}
		}
		op["outputs"] = kept
	}
}

// When BOTH candidates admit a shape, the stricter one maps it, clause by
// clause of the R2 order: refused-to-restricted beats served, the narrower
// output allowlist beats the wider, more fixed literals beat fewer, and the
// name decides a full tie. Each clause has its own synthetic pair in which
// only that clause differs; swapping the clause (the plant) maps the shape
// to the other operation and turns its case red.
func TestGraphQLSyntheticTieBreakPinsEveryR2Clause(t *testing.T) {
	hotspotsQuery := `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`
	catalogQuery := `{ catalog(dimension: REPO) { values { value } } }`
	cases := []struct {
		name  string
		specs []synthSpec
		query string
		want  []string // candidate order
	}{
		{
			name:  "restricted clause: refused beats served (name would pick the other)",
			specs: []synthSpec{{from: "hotspots", name: "aHotspotsServed"}, {from: "hotspots", name: "zHotspotsRefused", edit: refuseRestricted}},
			query: hotspotsQuery,
			want:  []string{"zHotspotsRefused", "aHotspotsServed"},
		},
		{
			name:  "outputs clause: narrower beats wider (name would pick the other)",
			specs: []synthSpec{{from: "hotspots", name: "aHotspotsWide"}, {from: "hotspots", name: "zHotspotsNarrow", edit: dropOutput("hotspots.rows[*].riskScore")}},
			query: hotspotsQuery,
			want:  []string{"zHotspotsNarrow", "aHotspotsWide"},
		},
		{
			name:  "literals clause: more fixed literals beat fewer (name would pick the other)",
			specs: []synthSpec{{from: "catalogValues", name: "aCatalogBound"}, {from: "acrRepositoryScopes", name: "zCatalogFixed"}},
			query: catalogQuery,
			want:  []string{"zCatalogFixed", "aCatalogBound"},
		},
		{
			name:  "name clause: a full tie is decided by the name",
			specs: []synthSpec{{from: "hotspots", name: "zHotspotsTwin"}, {from: "hotspots", name: "aHotspotsTwin"}},
			query: hotspotsQuery,
			want:  []string{"aHotspotsTwin", "zHotspotsTwin"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := syntheticCatalogue(t, tc.specs)
			policy, err := directread.NewGraphQLPolicy(cat, directread.EmbeddedOpsSchema(), directread.DefaultGraphQLLimits())
			if err != nil {
				t.Fatal(err)
			}
			roots := policy.Roots()
			if len(roots) != 1 {
				t.Fatalf("synthetic policy has %d roots", len(roots))
			}
			if got := roots[0].Operations(); !slices.Equal(got, tc.want) {
				t.Fatalf("candidate order %v, want %v", got, tc.want)
			}
			// Both candidates admit the query: it maps to the first.
			resp := runSynthetic(t, policy, tc.query)
			if resp.Call != directread.CallServed || resp.RootFields[0].Operation != tc.want[0] {
				t.Fatalf("mapped to %+v, want %s", resp.RootFields, tc.want[0])
			}
		})
	}
}

func runSynthetic(t *testing.T, policy *directread.GraphQLPolicy, query string) directread.GraphQLResponse {
	t.Helper()
	cfg := defaultFakeMCPConfig(t)
	cfg.Roots = map[string]bool{}
	for _, r := range policy.Roots() {
		cfg.Roots[r.Field] = true
	}
	listener := newFakeMCPListener(t, policy, cfg)
	client, err := directread.NewHTTPGraphQLClient(listener.server.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{Policy: policy, Gate: directread.NewSubjectGate(newOpGraph(), nil), Client: client, Now: func() time.Time { return opNow }})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(t.Context(), opUnrestricted(opOrgA), directread.GraphQLRequest{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	if refused := listener.refusals(); len(refused) != 0 {
		t.Fatalf("listener refused: %v", refused)
	}
	_ = strings.TrimSpace
	return resp
}

// A synthetic root whose candidates have different cost classes is charged
// the heavier one even when the lighter candidate maps the shape.
func TestGraphQLOverlapIsChargedTheMaxCandidateWeight(t *testing.T) {
	cat := syntheticCatalogue(t, []synthSpec{
		{from: "hotspots", name: "aHotspotsCheap", edit: func(op map[string]any) { op["cost_class"] = "catalog" }},
		{from: "hotspots", name: "zHotspotsDear", edit: func(op map[string]any) { op["cost_class"] = "compute" }},
	})
	policy, err := directread.NewGraphQLPolicy(cat, directread.EmbeddedOpsSchema(), directread.DefaultGraphQLLimits())
	if err != nil {
		t.Fatal(err)
	}
	root := policy.Roots()[0]
	if root.Operations()[0] != "aHotspotsCheap" || root.CostClass() != directread.CostCompute {
		t.Fatalf("order %v, charged %s; want the cheap op to map and compute to be charged", root.Operations(), root.CostClass())
	}
	resp := runSynthetic(t, policy, `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`)
	if resp.Call != directread.CallServed || resp.RootFields[0].Operation != "aHotspotsCheap" {
		t.Fatalf("mapped to %+v", resp.RootFields)
	}
	two := `{ a: hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } b: hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`
	if resp := runSynthetic(t, policy, two); resp.Call != directread.CallRefused || resp.Refusal.Code != directread.RefusalQueryLimitExceeded {
		t.Fatalf("two roots charged at the cheap weight: %+v", resp)
	}
}
