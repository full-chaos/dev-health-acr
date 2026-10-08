package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func mustPolicy(t *testing.T) *directread.GraphQLPolicy {
	t.Helper()
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatalf("root policy: %v", err)
	}
	return policy
}

func mustShapes(t *testing.T) []Shape {
	t.Helper()
	shapes, err := Shapes(mustPolicy(t))
	if err != nil {
		t.Fatalf("shapes: %v", err)
	}
	return shapes
}

func TestTypedLeavesNeverMatchAcrossTypes(t *testing.T) {
	leaf := func(kind string, raw string) Leaf {
		t.Helper()
		value, err := decodeJSON([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		l, err := typedLeaf(kind, value)
		if err != nil {
			t.Fatalf("typedLeaf(%s, %s): %v", kind, raw, err)
		}
		return l
	}
	if leaf(LeafInt, "3") == leaf(LeafFloat, "3") {
		t.Fatal("an integer and an integral float are one leaf")
	}
	if leaf(LeafInt, "9007199254740992") == leaf(LeafInt, "9007199254740993") {
		t.Fatal("two integers that collide in float64 are one leaf")
	}
	if leaf(LeafFloat, "3") != leaf(LeafFloat, "3.0") {
		t.Fatal("one float in two spellings is two leaves")
	}
	if leaf(LeafTime, `"2026-09-14 04:00:09.098"`) != leaf(LeafTime, `"2026-09-14T04:00:09.098Z"`) {
		t.Fatal("one instant in the ClickHouse and the RFC 3339 form is two leaves")
	}
	if leaf(LeafTime, `"2026-09-14T04:00:09Z"`) == leaf(LeafString, `"2026-09-14T04:00:09Z"`) {
		t.Fatal("a time and a string that looks like it are one leaf")
	}
	if leaf(LeafFloat, "null").T != LeafNull {
		t.Fatal("null is not typed null")
	}
	for kind, raw := range map[string]string{LeafInt: "3.5", LeafFloat: `"3"`, LeafString: "3", LeafBool: `"true"`, LeafDate: `"yesterday"`} {
		value, _ := decodeJSON([]byte(raw))
		if _, err := typedLeaf(kind, value); err == nil {
			t.Errorf("typedLeaf(%s, %s) coerced a value of another type", kind, raw)
		}
	}
	if got, err := factInteger("602"); err != nil || got != (Leaf{T: LeafInt, V: "602"}) {
		t.Fatalf("a read_facts integer string is %v, %v", got, err)
	}
	if _, err := factInteger("6.5"); err == nil {
		t.Fatal("a non-integer string was typed as an integer")
	}
}

// Every allowed root field has generated shapes, each is a selection of its
// operation's output allowlist, and each renders a query the ops SDL accepts.
func TestShapesAreGeneratedFromThePolicyForEveryRoot(t *testing.T) {
	policy := mustPolicy(t)
	shapes := mustShapes(t)
	roots, operations := map[string]bool{}, map[string]bool{}
	for _, shape := range shapes {
		roots[shape.Root], operations[shape.Operation] = true, true
		op, refusal := policy.Catalogue().Lookup(shape.Operation)
		if refusal != nil {
			t.Fatalf("%s: operation is not served", shape.ID())
		}
		allowed := map[string]bool{}
		inDocument := 0
		for _, o := range op.Outputs {
			allowed[o.Path] = true
			if !o.BeyondDocument {
				inDocument++
			}
		}
		if len(shape.Paths) == 0 {
			t.Errorf("%s selects nothing", shape.ID())
		}
		for _, path := range shape.Paths {
			if !allowed[path] {
				t.Errorf("%s selects %s, which the policy does not allow", shape.ID(), path)
			}
		}
		if shape.Name == "all" && len(shape.Paths) != inDocument {
			t.Errorf("%s selects %d of %d allowed paths the registered document selects", shape.ID(), len(shape.Paths), inDocument)
		}
		query, _, err := shape.Query(nil)
		if err != nil {
			t.Fatalf("%s: %v", shape.ID(), err)
		}
		if _, errs := gqlparser.LoadQuery(policy.Schema(), query); len(errs) > 0 {
			// A required argument that is not given is the only allowed
			// complaint: the query has no variable here.
			for _, e := range errs {
				if !strings.Contains(e.Message, "argument") && !strings.Contains(e.Message, "required") {
					t.Errorf("%s: the SDL refuses the generated query: %v", shape.ID(), e)
				}
			}
		}
	}
	if len(roots) != len(policy.Roots()) || len(roots) != 13 {
		t.Fatalf("shapes cover %d roots, the policy allows %d, want 13", len(roots), len(policy.Roots()))
	}
	if len(operations) != 16 {
		t.Fatalf("shapes cover %d operations, want 16 (one per served operation behind an allowed root)", len(operations))
	}
	for root := range roots {
		if _, ok := rootPairs[root]; !ok {
			t.Errorf("root %s has no pair", root)
		}
	}
	for root, pair := range rootPairs {
		if !roots[root] {
			t.Errorf("pair for %s, which is not an allowed root", root)
		}
		if pair.Mode == ModeShape && pair.Reason == "" || pair.Mode == ModeValue && pair.compare == nil {
			t.Errorf("pair for %s states no reason or has no compare", root)
		}
	}
}

func TestABindingCannotSetAPathThePolicyRefuses(t *testing.T) {
	shapes := mustShapes(t)
	for id, variables := range map[string]map[string]any{
		"hotspots/hotspots/all":             {"input": map[string]any{"teamIds": []any{"team:x"}}},
		"analytics/investmentBreakdown/all": {"batch": map[string]any{"sankey": map[string]any{"maxNodes": 5}}},
		"cognitiveLoad/cognitiveLoad/all":   {"input": map[string]any{"orgId": "another"}},
		"catalog/acrRepositoryScopes/all":   {"dimension": "TEAM"},
	} {
		shape, ok := ShapeByID(shapes, id)
		if !ok {
			t.Fatalf("shape %s is not generated", id)
		}
		if err := checkVariablePaths(shape, "", variables); err == nil {
			t.Errorf("%s: a refused variable path passed the binding check", id)
		}
	}
	shape, _ := ShapeByID(shapes, "analytics/investmentBreakdown/all")
	if err := checkVariablePaths(shape, "", investmentVariables(Window{}, "THEME")); err != nil {
		t.Fatalf("the investment binding is refused: %v", err)
	}
}

type fakePlanes struct {
	graphQL   func(shape Shape, variables map[string]any) (json.RawMessage, error)
	operation func(shape Shape, variables map[string]any) (json.RawMessage, error)
	facts     func(request FactsRequest) (json.RawMessage, error)
}

func (f fakePlanes) GraphQL(_ context.Context, shape Shape, variables map[string]any) (json.RawMessage, error) {
	return f.graphQL(shape, variables)
}

func (f fakePlanes) Operation(_ context.Context, shape Shape, variables map[string]any) (json.RawMessage, error) {
	if f.operation == nil {
		return json.RawMessage(`{"call":"operation_unavailable"}`), nil
	}
	return f.operation(shape, variables)
}

func (f fakePlanes) Facts(_ context.Context, request FactsRequest) (json.RawMessage, error) {
	if f.facts == nil {
		return nil, context.Canceled
	}
	return f.facts(request)
}

// noFacts is a read_facts answer in which no subject has a fact.
func noFacts(request FactsRequest) (json.RawMessage, error) {
	coverage := []any{}
	for _, s := range request.Subjects {
		coverage = append(coverage, map[string]any{"kind": request.Kinds[0], "subject": s, "outcome": "read_no_fact"})
	}
	return json.Marshal(map[string]any{"status": "partial", "facts": []any{}, "coverage": coverage, "versions": map[string]any{"kinds": map[string]any{}}})
}

func unavailable(Shape, map[string]any) (json.RawMessage, error) {
	return json.RawMessage(`{"call":"operation_unavailable"}`), nil
}

// fakeRun runs one root of the oracle on the cases of the capture.
func fakeRun(t *testing.T, root string, planes Planes, configure func(*Oracle)) *Report {
	t.Helper()
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, ShapeCases: manifest.ShapeCases, OnlyRoots: []string{root}, Planes: planes}
	if configure != nil {
		configure(oracle)
	}
	report, err := oracle.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// A generated shape that is not run is not a pass: the run stops.
func TestRunStopsWhenAGeneratedShapeHasNoCase(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	const dropped = "hotspots/hotspots/branch:rows"
	var cases []ShapeCase
	for _, c := range manifest.ShapeCases {
		if c.ShapeID != dropped {
			cases = append(cases, c)
		}
	}
	if len(cases) == len(manifest.ShapeCases) {
		t.Fatalf("the capture has no case for %s", dropped)
	}
	oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, ShapeCases: cases, OnlyRoots: []string{"hotspots"},
		Planes: fakePlanes{graphQL: func(Shape, map[string]any) (json.RawMessage, error) {
			return json.RawMessage(`{"call":"operation_unavailable"}`), nil
		}}}
	_, err = oracle.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), dropped+" has no case") {
		t.Fatalf("a run with a shape that has no case ended with %v", err)
	}
	oracle.ShapeCases = manifest.ShapeCases
	if _, err := oracle.Run(context.Background()); err != nil {
		t.Fatalf("the full case list does not run: %v", err)
	}
	oracle.OnlyRoots = []string{"home"}
	if _, err := oracle.Run(context.Background()); err == nil {
		t.Fatal("a run limited to a root that is not allowed did not stop")
	}
}

func themes(values ...float64) map[string]float64 {
	names := []string{"feature_delivery", "maintenance", "quality"}
	out := map[string]float64{}
	for i, v := range values {
		out[names[i]] = v
	}
	return out
}

func TestClassifyInvestment(t *testing.T) {
	ops := themes(1000, 500, 200)
	witnesses := Witnesses{ClassSupersession: themes(50, 20, 0), ClassMembershipScope: themes(400, 300, 100), ClassNullableArgmax: themes(0, 0, 0)}
	classesOfVerdict := func(v investmentVerdict) []Class {
		var out []Class
		for _, d := range v.Differences {
			out = append(out, d.Class)
		}
		return out
	}

	equal := classifyInvestment(ops, themes(1000, 500, 200), themes(1000, 500, 200), witnesses)
	if equal.Matches != 3 || len(equal.Differences) != 0 || len(equal.Findings) != 0 {
		t.Fatalf("equal planes: %+v", equal)
	}

	// acr equals what the store rows give and is below ops: effort that
	// reaches no repository.
	below := classifyInvestment(ops, themes(900, 500, 200), themes(900, 500, 200), witnesses)
	if got := classesOfVerdict(below); len(got) != 1 || got[0] != ClassAttributionBasis || below.Matches != 2 || len(below.Findings) != 0 {
		t.Fatalf("acr below ops, as the store rows give: %+v", below)
	}
	if below.Residual["feature_delivery"] != 100 {
		t.Fatalf("residual %v, want 100", below.Residual)
	}

	// acr equals the store rows and is above ops: the two readings of the
	// store disagree; never accepted.
	above := classifyInvestment(ops, themes(1100, 500, 200), themes(1100, 500, 200), witnesses)
	if len(above.Findings) != 1 || len(above.Differences) != 0 {
		t.Fatalf("acr above ops: %+v", above)
	}

	// acr is off the store rows by exactly one witness.
	exact := classifyInvestment(ops, themes(950, 520, 200), themes(900, 500, 200), witnesses)
	if got := classesOfVerdict(exact); len(got) != 1 || got[0] != ClassSupersession || !exact.Differences[0].Exact || len(exact.Findings) != 0 {
		t.Fatalf("a difference equal to the supersession witness: %+v", exact)
	}

	// A difference a witness only bounds is not named.
	bounded := classifyInvestment(ops, themes(1100, 600, 250), themes(900, 500, 200), witnesses)
	if len(bounded.Findings) != 1 || len(bounded.Differences) != 0 {
		t.Fatalf("a difference below the membership witness was named: %+v", bounded)
	}

	// Equal to the witness in one theme only: not named.
	oneTheme := classifyInvestment(ops, themes(950, 500, 200), themes(900, 500, 200), witnesses)
	if len(oneTheme.Findings) != 1 || len(oneTheme.Differences) != 0 {
		t.Fatalf("a difference equal to a witness in one theme only was named: %+v", oneTheme)
	}

	// Two classes with the same witness: not named.
	twins := Witnesses{ClassSupersession: themes(50, 20, 0), ClassMembershipScope: themes(50, 20, 0)}
	ambiguous := classifyInvestment(ops, themes(950, 520, 200), themes(900, 500, 200), twins)
	if len(ambiguous.Findings) != 1 || len(ambiguous.Differences) != 0 {
		t.Fatalf("a difference two witnesses equal was named: %+v", ambiguous)
	}

	// A tiny effort is not rounded to nothing: 5e-7 on the acr side and in the
	// store rows, 0 on the ops side, is a difference (the compare was once
	// held to an absolute 1e-6).
	tiny := classifyInvestment(themes(1000, 500, 0), themes(1000, 500, 5e-7), themes(1000, 500, 5e-7), witnesses)
	if len(tiny.Findings) != 1 || len(tiny.Differences) != 0 || tiny.Matches != 2 {
		t.Fatalf("a tiny effort that ops does not hold was a match: %+v", tiny)
	}

	// acr against the store rows is held to the sum tolerance (1e-9), not to
	// the Float32 tolerance of the ops side: a drift of 1e-7 of the value is a
	// finding.
	drift := classifyInvestment(ops, themes(1000.0001, 500, 200), themes(1000, 500, 200), witnesses)
	if len(drift.Findings) != 1 || len(drift.Differences) != 0 {
		t.Fatalf("acr off the store rows by 1e-7 relative was accepted: %+v", drift)
	}

	// acr lost effort: no class explains a loss, and a zero witness explains
	// nothing.
	lost := classifyInvestment(ops, themes(800, 500, 200), themes(900, 500, 200), witnesses)
	if len(lost.Findings) != 1 || len(lost.Differences) != 0 {
		t.Fatalf("acr below what the store rows give: %+v", lost)
	}
}

func TestScrubberKeepsJoinsAndDropsText(t *testing.T) {
	const org = "11111111-2222-4333-8444-555555555555"
	s, err := NewScrubber(org)
	if err != nil {
		t.Fatal(err)
	}
	name := "Full-Chaos/Dev_Health.Ops2"
	slug := s.Slug(name)
	if slug == name || len(slug) != len(name) || slug[10] != '/' || slug[4] != '-' || strings.ToLower(slug) != s.Slug(strings.ToLower(name)) {
		t.Fatalf("slug %q does not keep the length, the separators and the lower-case rule of %q", slug, name)
	}
	if slug[0] < 'A' || slug[0] > 'Z' || slug[1] < 'a' || slug[1] > 'z' {
		t.Fatalf("slug %q does not keep the letter case of %q", slug, name)
	}
	id := "7b9583ee-0000-4000-8000-0123456789ab"
	if got := s.UUID(id); got == id || !uuidShape.MatchString(got) || got != s.UUID(id) {
		t.Fatalf("uuid pseudonym %q", got)
	}
	if mapped, ok := s.SubjectID("repository:" + id); !ok || mapped != "repository:"+s.UUID(id) {
		t.Fatalf("subject id pseudonym %q", mapped)
	}
	evidence := s.Evidence(`{"prs":["` + id + `#pr12","not a ref"],"issues":["ghpr:Full-Chaos/ops#7","gitlab:grp/sub/proj!3","jira:SEC-1"],"title":"a person wrote this","authors":["someone@example.com"]}`)
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(evidence), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys["prs"] == nil || keys["issues"] == nil {
		t.Fatalf("evidence rewrite kept a key beside prs and issues: %s", evidence)
	}
	var out map[string][]string
	if err := json.Unmarshal([]byte(evidence), &out); err != nil {
		t.Fatal(err)
	}
	if len(out["prs"]) != 1 || len(out["issues"]) != 2 || out["prs"][0] != s.UUID(id)+"#pr12" ||
		out["issues"][0] != "ghpr:"+s.Slug("Full-Chaos/ops")+"#7" || out["issues"][1] != "gitlab:"+s.Slug("grp/sub/proj")+"!3" {
		t.Fatalf("evidence rewrite kept or lost the wrong parts: %s", evidence)
	}
	if strings.Contains(evidence, "person") || strings.Contains(evidence, "example.com") || strings.Contains(evidence, "SEC-1") {
		t.Fatalf("evidence rewrite kept free text: %s", evidence)
	}
	if _, err := s.Org("another-org"); err == nil {
		t.Fatal("an org column with a foreign value was accepted")
	}
	if got, err := s.Org(org); err != nil || got != FixtureOrgID {
		t.Fatalf("org pseudonym %q, %v", got, err)
	}
	if !s.Leaks([]byte(`{"x":"`+strings.ToUpper(org)+`"}`)) || s.Leaks([]byte(FixtureOrgID)) {
		t.Fatal("the leak check does not see the organization id")
	}
}

func TestEveryDeclaredColumnOfTheExtractHasAScrubRule(t *testing.T) {
	if err := checkSpecs(); err != nil {
		t.Fatal(err)
	}
	if len(extractTables) != 11 {
		t.Fatalf("%d extract tables, want 11", len(extractTables))
	}
	// A declared column with no rule stops the capture.
	missing := tableSpec{Table: tableRepos, Rules: map[string]columnRule{}}
	for column, rule := range extractTables[4].Rules {
		if column != "tags" {
			missing.Rules[column] = rule
		}
	}
	if extractTables[4].Table != tableRepos {
		t.Fatal("extract table order changed; fix this test")
	}
	if err := checkSpecList([]tableSpec{missing}); err == nil || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("a declared column with no rule passed: %v", err)
	}
	extra := tableSpec{Table: tableRepos, Rules: map[string]columnRule{"not_a_column": ruleKeep}}
	for column, rule := range extractTables[4].Rules {
		extra.Rules[column] = rule
	}
	if err := checkSpecList([]tableSpec{extra}); err == nil {
		t.Fatal("a rule for a column that is not declared passed")
	}
	// No text column is kept as it is.
	textColumns := map[string]bool{"repo": true, "repo_full_name": true, "name": true, "description": true, "structural_evidence_json": true,
		"tags": true, "ref": true, "native_team_key": true, "node_id": true, "category": true}
	for _, spec := range extractTables {
		for column, rule := range spec.Rules {
			if textColumns[column] && rule == ruleKeep {
				t.Errorf("table %s keeps the text column %s", spec.Table, column)
			}
		}
	}
}

// What a client may send for an enum variable comes from the policy.
func TestTheValuesOfAnEnumVariableComeFromThePolicy(t *testing.T) {
	rule := directread.VariableRule{Enum: []string{"A", "B", "C"}, RefusedValues: []directread.ValueRefusal{{Value: "B"}}}
	if got := sendableValues(rule); strings.Join(got, ",") != "A,C" {
		t.Fatalf("enum values without the refused ones: %v", got)
	}
	rule.AllowedValues = []string{"C"}
	if got := sendableValues(rule); strings.Join(got, ",") != "C" {
		t.Fatalf("allowed values: %v", got)
	}
	o := &Oracle{Policy: mustPolicy(t)}
	got, err := o.variableValues("catalogValues", "dimension")
	if err != nil {
		t.Fatal(err)
	}
	op, _ := o.Policy.Catalogue().Lookup("catalogValues")
	for _, v := range op.Variables {
		if v.Path == "dimension" && strings.Join(got, ",") != strings.Join(sendableValues(v), ",") {
			t.Fatalf("catalog dimensions %v, the policy sends %v", got, sendableValues(v))
		}
	}
	for _, dimension := range got {
		if dimension == "AUTHOR" {
			t.Fatalf("a refused dimension is run: %v", got)
		}
	}
	if len(got) < 5 {
		t.Fatalf("catalog dimensions: %v", got)
	}
}

// The theme and subcategory maps of the extract are keyed by taxonomy terms
// only: a key of another shape is replaced at capture, and the committed
// extract holds none.
func TestTheExtractHoldsTaxonomyKeysOnly(t *testing.T) {
	s, err := NewScrubber("11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	got, err := scrubValue(s, ruleTaxonomyMap, map[string]float64{"jane_doe_x": 1, "Jane Doe": 2, "feature_delivery.roadmap": 3}, Row{})
	if err != nil {
		t.Fatal(err)
	}
	keys := got.(map[string]any)
	if _, kept := keys["feature_delivery.roadmap"]; !kept || len(keys) != 3 {
		t.Fatalf("taxonomy keys: %v", keys)
	}
	if _, kept := keys["Jane Doe"]; kept {
		t.Fatalf("a key of another shape was kept: %v", keys)
	}
	_, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	defer func() {
		if checked == 0 {
			t.Error("the audit of the committed extract read no key")
		}
	}()
	for _, row := range extract.Tables[tableWorkUnitInvestments] {
		for _, column := range []string{"theme_distribution_json", "subcategory_distribution_json"} {
			m, _ := row[column].(map[string]any)
			if m == nil && row[column] != nil {
				t.Fatalf("%s holds a %T, not a map", column, row[column])
			}
			checked += len(m)
			for key := range m {
				if !taxonomyShape.MatchString(key) || len(key) > 64 {
					t.Fatalf("the committed extract holds the key %q in %s", key, column)
				}
			}
		}
	}
}

// A taxonomy term that is not shaped like one is text, and is replaced.
func TestATaxonomyTermOfAnotherShapeIsReplaced(t *testing.T) {
	s, err := NewScrubber("11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"feature_delivery", "feature_delivery.new_capability", "maintenance"} {
		if got := taxonomyTerm(s, kept); got != kept {
			t.Errorf("term %q was replaced by %q", kept, got)
		}
	}
	long := strings.Repeat("a", 65)
	if got := taxonomyTerm(s, long); got == long {
		t.Errorf("a term of 65 characters was kept")
	}
	for _, text := range []string{"Jane Doe", "https://example.org/x", "jane@example.org", "Feature Delivery", "9lives"} {
		if got := taxonomyTerm(s, text); got == text || strings.Contains(got, "example") {
			t.Errorf("text %q was kept as %q", text, got)
		}
	}
}

func TestScrubReplyKeepsTypesAndReplacesText(t *testing.T) {
	s, err := NewScrubber("11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t)
	shape, _ := ShapeByID(mustShapes(t), "compoundingRisk/compoundingRisk/all")
	raw := `{"compoundingRisk":{"__typename":"CompoundingRiskResult","orgId":"11111111-2222-4333-8444-555555555555","breakout":"REPO","generatedAt":"2026-10-01T16:00:00Z",
	 "rows":[{"__typename":"CompoundingRiskPoint","day":"2026-09-18","scope":"REPO","scopeId":"7b9583ee-0000-4000-8000-0123456789ab","scopeLabel":"full-chaos/secret-name","score":0.5,"severity":"ELEVATED","computedAt":"2026-09-18T04:00:00Z",
	   "components":null,"weights":null,"thresholds":null}],"trend":[]}}`
	data, err := decodeJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	clean, err := scrubReply(s, policy.Schema(), shape, nil, data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(clean)
	text := string(encoded)
	for _, gone := range []string{"secret-name", "11111111-2222", "7b9583ee"} {
		if strings.Contains(text, gone) {
			t.Errorf("the scrubbed reply still holds %q: %s", gone, text)
		}
	}
	for _, kept := range []string{`"severity":"ELEVATED"`, `"day":"2026-09-18"`, `"score":0.5`, `"__typename":"CompoundingRiskPoint"`, FixtureOrgID, s.UUID("7b9583ee-0000-4000-8000-0123456789ab")} {
		if !strings.Contains(text, kept) {
			t.Errorf("the scrubbed reply lost %q: %s", kept, text)
		}
	}
	// A string on a path the shape did not select stops the capture.
	extra, _ := decodeJSON([]byte(`{"compoundingRisk":{"notSelected":"text"}}`))
	if _, err := scrubReply(s, policy.Schema(), shape, nil, extra); err == nil {
		t.Fatal("a string outside the selection was scrubbed and kept")
	}
}

func TestReplayListenerAnswersOnlyAnArmedRecord(t *testing.T) {
	listener := NewReplayListener(FixtureOrgID)
	server := httptest.NewServer(listener)
	defer server.Close()
	post := func(orgID, authorization, query string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": query})
		req, _ := http.NewRequest(http.MethodPost, server.URL+directread.GraphQLListenerPath, strings.NewReader(string(body)))
		req.Header.Set(directread.HeaderInternalOrgID, orgID)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(FixtureOrgID, "", "query { hotspots { __typename } }"); got != http.StatusInternalServerError {
		t.Fatalf("a request with no armed record was answered %d", got)
	}
	listener.arm("hotspots", RecordedReply{Status: 200, Data: json.RawMessage(`{"hotspots":{"__typename":"HotspotsResult"}}`)})
	if got := post(FixtureOrgID, "", "query { catalog { __typename } }"); got != http.StatusInternalServerError {
		t.Fatalf("a request for another root was answered %d", got)
	}
	if got := post("another-org", "", "query { hotspots { __typename } }"); got != http.StatusInternalServerError {
		t.Fatalf("a request of another organization was answered %d", got)
	}
	if got := post(FixtureOrgID, "Bearer x", "query { hotspots { __typename } }"); got != http.StatusInternalServerError {
		t.Fatalf("a request with an Authorization header was answered %d", got)
	}
	if got := post(FixtureOrgID, "", "query { hotspots { __typename } }"); got != http.StatusOK {
		t.Fatalf("the armed request was answered %d", got)
	}
	listener.arm("hotspots", RecordedReply{Status: 404, Reason: directread.ListenerNotFoundRootNotEnabled})
	if got := post(FixtureOrgID, "", "query { hotspots { __typename } }"); got != http.StatusNotFound {
		t.Fatalf("a root that is not enabled was answered %d", got)
	}
	if got := len(listener.Failures()); got != 4 {
		t.Fatalf("%d failures recorded, want 4", got)
	}
}

// A capture whose extract lost rows is refused, not read as a smaller store.
func TestLoadCaptureRefusesAnExtractThatDoesNotMatchItsManifest(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{ManifestFile, RepliesFile} {
		content, err := os.ReadFile(filepath.Join(captureDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadCapture(dir); err == nil {
		t.Fatal("a capture with no extract was loaded")
	}
	short := extract.Clone()
	short.Tables[tableRepos] = short.Tables[tableRepos][1:]
	if err := short.Write(filepath.Join(dir, ExtractDir)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadCapture(dir); err == nil || !strings.Contains(err.Error(), tableRepos) {
		t.Fatalf("an extract with a missing row was loaded: %v", err)
	}
}

func mustDay(t *testing.T, day string) time.Time {
	t.Helper()
	at, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestStoreReadsScopeSupersessionAndTheNullGeneration(t *testing.T) {
	unit := func(id, at string, repo any) Row {
		return Row{"work_unit_id": id, "computed_at": at, "structural_evidence_json": `{"prs":[],"issues":[]}`, "from_ts": "2026-09-10 00:00:00.000", "to_ts": "2026-09-11 00:00:00.000",
			"repo_id": repo, "effort_value": json.Number("10"), "theme_distribution_json": map[string]any{"quality": json.Number("1")},
			"subcategory_distribution_json": map[string]any{"quality.bugfix": json.Number("1")}}
	}
	extract := &Extract{Tables: map[string][]Row{
		tableWorkUnitInvestments: {
			unit("live", "2026-09-12 00:00:00.000", "r1"),
			unit("superseded", "2026-09-12 00:00:00.000", "r1"),
			unit("outside", "2026-09-12 00:00:00.000", "r1"),
			unit("nulled", "2026-09-13 00:00:00.000", nil), unit("nulled", "2026-09-12 00:00:00.000", "r1"),
		},
		tableRepos:                  {{"id": "r1", "repo": "o/r1", "provider": "github", "last_synced": "2026-09-01 00:00:00.000"}},
		tableWorkUnitSupersessions:  {{"superseded_work_unit_id": "superseded"}},
		tableWorkUnitMembershipRuns: {{"run_id": "old", "completed_at": "2026-09-01 00:00:00.000"}, {"run_id": "run", "completed_at": "2026-09-20 00:00:00.000"}},
		tableWorkUnitMembership: {{"run_id": "run", "work_unit_id": "live"}, {"run_id": "run", "work_unit_id": "superseded"},
			{"run_id": "run", "work_unit_id": "nulled"}, {"run_id": "old", "work_unit_id": "outside"}},
	}}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	window := Window{Start: mustDay(t, "2026-09-01"), End: mustDay(t, "2026-10-01")}
	if got := store.OrgThemeEffort(window)["quality"]; got != 20 {
		t.Fatalf("ops organization reading is %v, want 20 (the live and the nulled unit)", got)
	}
	w := store.Witnesses(window)
	if w[ClassSupersession]["quality"] != 10 || w[ClassMembershipScope]["quality"] != 10 || w[ClassNullableArgmax]["quality"] != 10 {
		t.Fatalf("witnesses %+v, want 10 for each class", w)
	}
	// Only the live unit reaches the repository: the nulled one has no
	// reference and no repo_id in its latest generation.
	if got := store.ExpectedRepositoryEffort(window); len(got) != 1 || got["r1"]["quality"] != 10 {
		t.Fatalf("expected repository mix %+v, want r1 quality 10", got)
	}
	if got := store.OrgThemeEffort(Window{Start: mustDay(t, "2026-09-12"), End: mustDay(t, "2026-10-01")})["quality"]; got != 0 {
		t.Fatalf("a unit that ended before the window is read: %v", got)
	}
}

func servedAnswer(t *testing.T, root, operation, data string, returned, max int) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"call": "served", "completeness": "unknown", "result": "data",
		"root_fields": []any{map[string]any{"key": root, "field": root, "operation": operation}},
		"data":        json.RawMessage(data),
		"page":        map[string]any{"returned_bytes": returned, "max_bytes": max},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// The shape pass finds an answer of the wrong type, a path that was not
// selected, a wrong echo, a broken byte limit and a call that is not served.
func TestShapePassFindsTypeEchoAndLimitProblems(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	good := `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null,"rows":[{"__typename":"WorkGraphFlowRow","nodeType":"ISSUE","inflow":3,"outflow":4}]}}`
	run := func(answer json.RawMessage) *RootReport {
		t.Helper()
		oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, ShapeCases: manifest.ShapeCases, OnlyRoots: []string{"workGraphFlow"},
			Planes: fakePlanes{graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
				switch shape.Name {
				case "branch:(root)":
					return servedAnswer(t, "workGraphFlow", "workGraphFlow", `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null}}`, 20, 32768), nil
				case "branch:rows":
					return servedAnswer(t, "workGraphFlow", "workGraphFlow", `{"workGraphFlow":{"rows":[{"__typename":"WorkGraphFlowRow","nodeType":"ISSUE","inflow":3,"outflow":4}]}}`, 20, 32768), nil
				}
				return answer, nil
			}}}
		report, rerr := oracle.Run(context.Background())
		if rerr != nil {
			t.Fatal(rerr)
		}
		return report.Root("workGraphFlow")
	}
	if rr := run(servedAnswer(t, "workGraphFlow", "workGraphFlow", good, len(good), 32768)); len(rr.Findings) != 0 || rr.Leaves != 12 {
		t.Fatalf("a well-formed answer: %d leaves, findings %+v", rr.Leaves, rr.Findings)
	}
	for name, answer := range map[string]json.RawMessage{
		"an integer served as a string":   servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":"3"`, 1), 100, 32768),
		"a float where an integer is":     servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":3.5`, 1), 100, 32768),
		"a path that was not selected":    servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":3,"evidence":"text"`, 1), 100, 32768),
		"a null in a non-null field":      servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":null`, 1), 100, 32768),
		"a selected field that is absent": servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3,`, ``, 1), 100, 32768),
		"a selected block that is absent": servedAnswer(t, "workGraphFlow", "workGraphFlow", `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null}}`, 100, 32768),
		"no root field":                   servedAnswer(t, "workGraphFlow", "workGraphFlow", `{}`, 100, 32768),
		"another operation in the echo":   servedAnswer(t, "workGraphFlow", "hotspots", good, 100, 32768),
		"another root in the echo":        servedAnswer(t, "hotspots", "workGraphFlow", good, 100, 32768),
		"more bytes than the limit":       servedAnswer(t, "workGraphFlow", "workGraphFlow", good, 40000, 32768),
		"no byte count":                   servedAnswer(t, "workGraphFlow", "workGraphFlow", good, 0, 32768),
		"a refusal":                       json.RawMessage(`{"call":"refused","refusal":{"code":"response_budget"}}`),
		"an upstream error":               json.RawMessage(`{"call":"upstream_error","errors":[{"class":"server_error"}]}`),
	} {
		if rr := run(answer); len(rr.Findings) == 0 {
			t.Errorf("%s: the shape pass found nothing", name)
		}
	}
	// A root that is not enabled on the listener is stated, not found. That
	// such a run is not a measurement unless the venue declares the root dark
	// is TestARootMustBeMeasuredOnEveryPathTheVenueServes.
	if rr := run(json.RawMessage(`{"call":"operation_unavailable","errors":[{"class":"not_found"}]}`)); len(rr.Findings) != 0 || rr.Listener != "operation_unavailable" {
		t.Fatalf("a root that is not enabled: listener %s, findings %+v", rr.Listener, rr.Findings)
	}
}

func operationAnswer(t *testing.T, operation, data string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"call": "served", "result": "data", "operation": operation, "data": json.RawMessage(data),
		"page": map[string]any{"returned_bytes": len(data), "max_bytes": 32768},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// run_operation and graphql_query must give the same leaves for the same
// variables; a value made for each request is the only thing left out.
func TestTheTwoPathsMustAnswerTheSame(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	flow := `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null,"rows":[{"__typename":"WorkGraphFlowRow","nodeType":"ISSUE","inflow":3,"outflow":4},{"__typename":"WorkGraphFlowRow","nodeType":"PR","inflow":1,"outflow":2}]}}`
	run := func(root, graphQL, operation string) *RootReport {
		t.Helper()
		oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, ShapeCases: manifest.ShapeCases, OnlyRoots: []string{root},
			Planes: fakePlanes{
				graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
					if shape.Name != "all" {
						return json.RawMessage(`{"call":"operation_unavailable"}`), nil
					}
					return servedAnswer(t, root, shape.Operation, graphQL, len(graphQL), 32768), nil
				},
				operation: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
					return operationAnswer(t, shape.Operation, operation), nil
				},
			}}
		report, rerr := oracle.Run(context.Background())
		if rerr != nil {
			t.Fatal(rerr)
		}
		return report.Root(root)
	}
	crossFindings := func(rr *RootReport) int {
		n := 0
		for _, f := range rr.Findings {
			if f.Pair == "cross_path" {
				n++
			}
		}
		return n
	}
	same := run("workGraphFlow", flow, flow)
	if crossFindings(same) != 0 || same.CrossPaths != 10 || same.CrossMatches != 10 || same.RunOperation != "served" || same.OperationsRun != 1 {
		t.Fatalf("equal answers: %d of %d leaves equal, findings %+v", same.CrossMatches, same.CrossPaths, same.Findings)
	}
	// The order of a list is not a difference.
	reordered := strings.Replace(strings.Replace(flow, `"nodeType":"ISSUE","inflow":3,"outflow":4`, `"nodeType":"X"`, 1), `"nodeType":"PR","inflow":1,"outflow":2`, `"nodeType":"ISSUE","inflow":3,"outflow":4`, 1)
	reordered = strings.Replace(reordered, `"nodeType":"X"`, `"nodeType":"PR","inflow":1,"outflow":2`, 1)
	if rr := run("workGraphFlow", flow, reordered); crossFindings(rr) != 0 {
		t.Fatalf("a reordered list is a finding: %+v", rr.Findings)
	}
	// Two rows that swap their values: every field has the same values, the
	// rows are not the same rows.
	swapped := `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null,"rows":[{"__typename":"WorkGraphFlowRow","nodeType":"ISSUE","inflow":1,"outflow":2},{"__typename":"WorkGraphFlowRow","nodeType":"PR","inflow":3,"outflow":4}]}}`
	if rr := run("workGraphFlow", flow, swapped); crossFindings(rr) == 0 {
		t.Fatalf("rows that swapped their values between them are not a finding: %+v", rr.Findings)
	}
	if rr := run("workGraphFlow", flow, strings.Replace(flow, `"inflow":3`, `"inflow":5`, 1)); crossFindings(rr) != 1 {
		t.Fatalf("another value on the other path: %d cross-path findings, want 1: %+v", crossFindings(rr), rr.Findings)
	}
	if rr := run("workGraphFlow", flow, strings.Replace(flow, `,{"__typename":"WorkGraphFlowRow","nodeType":"PR","inflow":1,"outflow":2}`, ``, 1)); crossFindings(rr) == 0 {
		t.Fatalf("a row missing on the other path is not a finding")
	}
	// run_operation must name the operation that was asked.
	echo := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, ShapeCases: manifest.ShapeCases, OnlyRoots: []string{"workGraphFlow"},
		Planes: fakePlanes{
			graphQL: func(Shape, map[string]any) (json.RawMessage, error) {
				return json.RawMessage(`{"call":"operation_unavailable"}`), nil
			},
			operation: func(Shape, map[string]any) (json.RawMessage, error) { return operationAnswer(t, "hotspots", flow), nil },
		}}
	echoReport, err := echo.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rr := echoReport.Root("workGraphFlow"); len(rr.Findings) == 0 || rr.Findings[0].Pair != "run_operation" {
		t.Fatalf("a run_operation answer that names another operation is not a finding: %+v", rr.Findings)
	}

	// A float aggregate may differ in its last digits, and in nothing more.
	stats := func(stddev string) string {
		return `{"analytics":{"__typename":"AnalyticsResult","breakdowns":[],"evidenceQualityDistribution":{"low":1},"evidenceQualityStats":{"__typename":"EvidenceQualityStats","mean":0.4,"stddev":` + stddev + `,"total":10,"bandCounts":{"low":1}}}}`
	}
	if rr := run("analytics", stats("0.21024065010042445"), stats("0.21024065010042448")); crossFindings(rr) != 0 {
		t.Fatalf("a last-digit difference of a float aggregate is a finding: %+v", rr.Findings)
	}
	if rr := run("analytics", stats("0.21024065010042445"), stats("0.2103")); crossFindings(rr) == 0 {
		t.Fatal("another standard deviation on the other path is not a finding")
	}

	// A value made for each request is not compared.
	risk := func(at string) string {
		return `{"compoundingRisk":{"__typename":"CompoundingRiskResult","orgId":"o","breakout":"REPO","generatedAt":"` + at + `","rows":[],"trend":[]}}`
	}
	if rr := run("compoundingRisk", risk("2026-10-01T16:00:00Z"), risk("2026-10-01T16:00:05Z")); crossFindings(rr) != 0 {
		t.Fatalf("the request time is compared across the two paths: %+v", rr.Findings)
	}
	if rr := run("compoundingRisk", risk("2026-10-01T16:00:00Z"), strings.Replace(risk("2026-10-01T16:00:00Z"), `"REPO"`, `"TEAM"`, 1)); crossFindings(rr) == 0 {
		t.Fatalf("another breakout on the other path is not a finding")
	}
}

func TestTemporaryAllowanceExpiresWithItsPaths(t *testing.T) {
	policy := mustPolicy(t)
	for root, spec := range temporaryAllowances {
		rootPolicy, ok := policy.Root(root)
		if !ok {
			t.Fatalf("temporary paths for %s, which is not an allowed root", root)
		}
		outputs := rootOutputs(policy, rootPolicy)
		if missing := temporaryPathsMissing(spec.Paths, outputs); len(missing) != 0 {
			t.Fatalf("root %s: temporary paths %v are not in the policy", root, missing)
		}
		// Each path or block, taken out of the policy, expires the allowance.
		for _, gone := range spec.Paths {
			var rest []string
			for _, path := range outputs {
				if path != gone && !strings.HasPrefix(path, gone+".") {
					rest = append(rest, path)
				}
			}
			if missing := temporaryPathsMissing(spec.Paths, rest); len(missing) != 1 || missing[0] != gone {
				t.Errorf("root %s: without %s the missing list is %v", root, gone, missing)
			}
		}
		// The allowance is read against the contract this build pins.
		op, refusal := policy.Catalogue().Lookup(spec.Operation)
		if refusal != nil {
			t.Fatalf("root %s: operation %s is not served", root, spec.Operation)
		}
		if got := contractDigest(op); got != spec.Contract {
			t.Errorf("root %s: the contract of %s is %s, the allowance pins %s: read the resolver again, then renew the digest or remove the allowance", root, spec.Operation, got, spec.Contract)
		}
		if spec.Window != "" {
			if rule, ok := op.Variable(spec.Window); !ok || !rule.Allowed || rule.Source != directread.SourceClient {
				t.Errorf("root %s: the window variable %s is not a client variable of %s", root, spec.Window, spec.Operation)
			}
			if !op.OutputAllowed(spec.Echo) {
				t.Errorf("root %s: the echo path %s is not an output of %s", root, spec.Echo, spec.Operation)
			}
		}
	}
	if len(TemporaryClasses()) != 1 || len(Classes()) != 6 {
		t.Fatalf("%d temporary and %d design classes, want 1 and 6", len(TemporaryClasses()), len(Classes()))
	}
	for _, class := range Classes() {
		if class == ClassLatestDayVsWindow {
			t.Fatal("the temporary class is listed as a design class")
		}
	}
}

func TestARunWithAnExpiredAllowanceIsAnError(t *testing.T) {
	report := &Report{Roots: []*RootReport{{Root: "a", Findings: []Finding{{Detail: "a measured difference"}}}, {Root: "b"}}}
	if err := report.Err(); err != nil {
		t.Fatalf("a run with findings only is an error: %v", err)
	}
	report.Roots[1].Expired = []string{"class latest_day_vs_window: gone"}
	if err := report.Err(); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("a run with an expired allowance is not an error: %v", err)
	}
	report.Roots[1].Expired = nil
	report.Roots[0].Invalid = []string{"no path served the root"}
	if err := report.Err(); err == nil || !strings.Contains(err.Error(), "a: no path served the root") {
		t.Fatalf("a run that is not a measurement is not an error: %v", err)
	}
}

func TestFlowWindowCheck(t *testing.T) {
	fact := func(startedWindow, startedLatest any) ServedFact {
		fields := map[string]any{"items_completed_window": "5", "items_completed_latest_day": "2"}
		if startedWindow != nil {
			fields["items_started_window"] = startedWindow
		}
		if startedLatest != nil {
			fields["items_started_latest_day"] = startedLatest
		}
		return ServedFact{Fields: fields, Tables: map[string]ServedTable{
			"daily_flow":      {Columns: []string{"day", "items_started", "items_completed"}, Rows: [][]any{{"2026-09-02", "4", "3"}, {"2026-09-01", "6", "2"}}},
			"scope_breakdown": {Columns: []string{"work_scope_id", "items_started", "items_completed"}, Rows: [][]any{{"a", "3", "2"}, {"b", "1", "0"}}},
		}}
	}
	compared, matches, findings, joined, err := flowWindowCheck(fact("10", "4"))
	if err != nil || !joined || compared != 4 || matches != 4 || len(findings) != 0 {
		t.Fatalf("a consistent flow fact: %d of %d, findings %+v, joined %t, %v", matches, compared, findings, joined, err)
	}
	// The window count is the latest-day sum: the defect the fields were
	// named for.
	if _, matches, findings, _, _ := flowWindowCheck(fact("4", "4")); matches != 3 || len(findings) != 1 || findings[0].Path != "acr:flow.items_started_window" {
		t.Fatalf("a window count that is the latest-day sum: %d matches, findings %+v", matches, findings)
	}
	if _, matches, findings, _, _ := flowWindowCheck(fact("10", "10")); matches != 3 || len(findings) != 1 || findings[0].Path != "acr:flow.items_started_latest_day" {
		t.Fatalf("a latest-day count that is the window sum: %d matches, findings %+v", matches, findings)
	}
	if _, _, findings, _, _ := flowWindowCheck(fact("10", nil)); len(findings) != 1 {
		t.Fatalf("a fact with no latest-day count: findings %+v", findings)
	}
	// A fact from before the counts were named is not joined, and is not a
	// finding.
	if _, _, findings, joined, err := flowWindowCheck(ServedFact{Fields: map[string]any{"items_started": "4"}}); joined || err != nil || len(findings) != 0 {
		t.Fatalf("a fact with no window-named count: joined %t, findings %+v, %v", joined, findings, err)
	}
	cut := fact("10", "4")
	table := cut.Tables["daily_flow"]
	table.TruncatedBy = "provider_row_cap"
	cut.Tables["daily_flow"] = table
	if _, _, findings, _, _ := flowWindowCheck(cut); len(findings) != 2 {
		t.Fatalf("a cut daily series must not be summed: findings %+v", findings)
	}
}

const flowAnswer = `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null,"rows":[{"__typename":"WorkGraphFlowRow","nodeType":"ISSUE","inflow":3,"outflow":4}]}}`

// flowPlanes answers every shape of workGraphFlow with the fields it selects.
func flowPlanes(t *testing.T) fakePlanes {
	t.Helper()
	answer := func(shape Shape) string {
		switch shape.Name {
		case "branch:(root)":
			return `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null}}`
		case "branch:rows":
			return `{"workGraphFlow":{"rows":[{"__typename":"WorkGraphFlowRow","nodeType":"ISSUE","inflow":3,"outflow":4}]}}`
		}
		return flowAnswer
	}
	return fakePlanes{
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			data := answer(shape)
			return servedAnswer(t, shape.Root, shape.Operation, data, len(data), 32768), nil
		},
		operation: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return operationAnswer(t, shape.Operation, answer(shape)), nil
		},
	}
}

// An answer that holds none of the selected fields, with a valid echo and
// valid limits, on both paths: every absent field is a finding, and the run
// is not a measurement.
func TestAnAnswerWithNoSelectedFieldIsNotAPass(t *testing.T) {
	report := fakeRun(t, "workGraphFlow", fakePlanes{
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return servedAnswer(t, shape.Root, shape.Operation, `{}`, 2, 32768), nil
		},
		operation: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return operationAnswer(t, shape.Operation, `{}`), nil
		},
	}, nil)
	rr := report.Root("workGraphFlow")
	if len(rr.Findings) == 0 {
		t.Fatalf("an answer with no root field is not a finding")
	}
	err := report.Err()
	if err == nil || !strings.Contains(err.Error(), "gave no leaf") {
		t.Fatalf("a run that measured no leaf is not an error: %v", err)
	}
	// An object that lost one selected field is a finding of its own.
	lost := fakeRun(t, "workGraphFlow", fakePlanes{
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			data := `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null,"rows":[{"__typename":"WorkGraphFlowRow","nodeType":"ISSUE","outflow":4}]}}`
			return servedAnswer(t, shape.Root, shape.Operation, data, len(data), 32768), nil
		},
		operation: unavailable,
	}, nil).Root("workGraphFlow")
	found := false
	for _, f := range lost.Findings {
		if strings.Contains(f.Detail, "workGraphFlow.rows[*].inflow is absent") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a row with no inflow field is not a finding: %+v", lost.Findings)
	}
	// A selected path under a null parent or an empty list is stated.
	empty := fakeRun(t, "workGraphFlow", fakePlanes{
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			data := `{"workGraphFlow":{"__typename":"WorkGraphFlowResult","degradedReason":null,"rows":[]}}`
			return servedAnswer(t, shape.Root, shape.Operation, data, len(data), 32768), nil
		},
		operation: unavailable,
	}, nil).Root("workGraphFlow")
	if strings.Join(empty.Unmeasured, " ") != "workGraphFlow.rows[*].__typename workGraphFlow.rows[*].inflow workGraphFlow.rows[*].nodeType workGraphFlow.rows[*].outflow" {
		t.Fatalf("the paths under an empty list are not stated as paths with no value: %v", empty.Unmeasured)
	}
}

// A root is measured on a path, or the venue declares it dark there.
func TestARootMustBeMeasuredOnEveryPathTheVenueServes(t *testing.T) {
	served := flowPlanes(t)
	invalid := func(planes fakePlanes, listenerDark, operationDark []string) string {
		t.Helper()
		report := fakeRun(t, "workGraphFlow", planes, func(o *Oracle) {
			o.ListenerDark, o.OperationDark = listenerDark, operationDark
		})
		if err := report.Err(); err != nil {
			return err.Error()
		}
		return ""
	}
	root := []string{"workGraphFlow"}
	if got := invalid(served, nil, nil); got != "" {
		t.Fatalf("a root served on both paths is not a measurement: %s", got)
	}
	if got := invalid(fakePlanes{graphQL: unavailable, operation: unavailable}, nil, nil); !strings.Contains(got, "no path served the root") {
		t.Fatalf("a root no path serves: %q", got)
	}
	if got := invalid(fakePlanes{graphQL: unavailable, operation: unavailable}, root, root); !strings.Contains(got, "no path served the root") {
		t.Fatalf("a root declared dark on both paths is still not measured: %q", got)
	}
	if got := invalid(fakePlanes{graphQL: unavailable, operation: served.operation}, nil, nil); !strings.Contains(got, "graphql_query is unavailable for the root and the venue does not declare the root dark") {
		t.Fatalf("a root that is dark on the listener and not declared: %q", got)
	}
	if got := invalid(fakePlanes{graphQL: unavailable, operation: served.operation}, root, nil); got != "" {
		t.Fatalf("a root declared dark on the listener and served through run_operation is refused: %s", got)
	}
	if got := invalid(fakePlanes{graphQL: served.graphQL, operation: unavailable}, nil, nil); !strings.Contains(got, "run_operation is unavailable for the root and the venue does not declare the root dark") {
		t.Fatalf("a root that run_operation does not serve and that is not declared: %q", got)
	}
	if got := invalid(served, root, nil); !strings.Contains(got, "declares the root dark on graphql_query and it is served") {
		t.Fatalf("a root declared dark and served: %q", got)
	}
	partly := fakePlanes{operation: served.operation, graphQL: func(shape Shape, variables map[string]any) (json.RawMessage, error) {
		if shape.Name == "branch:rows" {
			return unavailable(shape, variables)
		}
		return served.graphQL(shape, variables)
	}}
	if got := invalid(partly, nil, nil); !strings.Contains(got, "graphql_query served 2 cases of the root and was unavailable for 1") {
		t.Fatalf("a root served for some cases only: %q", got)
	}
	refused := fakePlanes{operation: served.operation, graphQL: func(Shape, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"call":"refused","refusal":{"code":"response_budget"}}`), nil
	}}
	if got := invalid(refused, nil, nil); !strings.Contains(got, "graphql_query served no case of the root") {
		t.Fatalf("a root every case of which is refused: %q", got)
	}
	// Two served paths that give no leaf to compare.
	null := `{"workGraphFlow":null}`
	nulls := fakePlanes{
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return servedAnswer(t, shape.Root, shape.Operation, null, len(null), 32768), nil
		},
		operation: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return operationAnswer(t, shape.Operation, null), nil
		},
	}
	if got := invalid(nulls, nil, nil); !strings.Contains(got, "gave no leaf") || !strings.Contains(got, "no leaf was compared between them") {
		t.Fatalf("two served paths with a null root: %q", got)
	}
}

func historyOf(variables map[string]any, name string) int {
	input, _ := variables["input"].(map[string]any)
	switch v := input[name].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return -1
}

// recordedThroughput answers throughputForecast from the recorded reply of
// the capture, changed by mutate, cut to the fields each shape selects.
func recordedThroughput(t *testing.T, mutate func(variables map[string]any, forecast map[string]any)) fakePlanes {
	t.Helper()
	_, recording, _, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	var template map[string]any
	keys := make([]string, 0, len(recording.Replies))
	for key := range recording.Replies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		reply := recording.Replies[key]
		if !strings.HasPrefix(key, "throughputForecast/throughputForecast/all#") || reply.Status != 200 {
			continue
		}
		var candidate map[string]any
		if err := json.Unmarshal(reply.Data, &candidate); err != nil {
			t.Fatal(err)
		}
		if forecast, _ := candidate["throughputForecast"].(map[string]any); forecast != nil && forecast["estimateCoverage"] != nil {
			template = candidate
			break
		}
	}
	if template == nil {
		t.Fatal("the capture has no served throughputForecast reply with an estimate coverage")
	}
	answer := func(shape Shape, variables map[string]any) string {
		var data map[string]any
		encoded, _ := json.Marshal(template)
		_ = json.Unmarshal(encoded, &data)
		forecast := data["throughputForecast"].(map[string]any)
		mutate(variables, forecast)
		selected := map[string]bool{}
		for _, path := range shape.Paths {
			selected[strings.Split(strings.ReplaceAll(path, "[*]", ""), ".")[1]] = true
		}
		for field := range forecast {
			if !selected[field] {
				delete(forecast, field)
			}
		}
		out, _ := json.Marshal(data)
		return string(out)
	}
	return fakePlanes{
		facts: noFacts,
		graphQL: func(shape Shape, variables map[string]any) (json.RawMessage, error) {
			data := answer(shape, variables)
			return servedAnswer(t, shape.Root, shape.Operation, data, len(data), 32768), nil
		},
		operation: func(shape Shape, variables map[string]any) (json.RawMessage, error) {
			return operationAnswer(t, shape.Operation, answer(shape, variables)), nil
		},
	}
}

// The temporary class is counted only where the run measured it, and the
// allowance expires when the measured value starts to follow the window.
// probeInvalid lists the reasons a window probe could not measure.
func probeInvalid(rr *RootReport) []string {
	var out []string
	for _, reason := range rr.Invalid {
		if strings.Contains(reason, "the window probe could not measure it") {
			out = append(out, reason)
		}
	}
	return out
}

func TestTemporaryAllowanceIsMeasuredNotAssumed(t *testing.T) {
	echo := func(variables map[string]any, forecast map[string]any) {
		forecast["historyWeeks"] = historyOf(variables, "historyWeeks")
	}
	count := func(rr *RootReport) int { return rr.ByClass[ClassLatestDayVsWindow] }

	// The covered values are the same for both histories: four counted
	// differences, one per covered path, nothing expired.
	same := fakeRun(t, "throughputForecast", recordedThroughput(t, echo), nil)
	rr := same.Root("throughputForecast")
	if count(rr) != 4 || len(same.Expired()) != 0 || len(rr.CodeRead) != 0 {
		t.Fatalf("a latest-day value under two histories: %d differences, expired %v, code read %v", count(rr), same.Expired(), rr.CodeRead)
	}
	for _, d := range rr.Differences {
		if d.Class == ClassLatestDayVsWindow && (d.Pair != "window_probe" || !d.Exact) {
			t.Fatalf("a measured difference of the class is not marked as measured: %+v", d)
		}
	}

	// The backlog now follows the requested history: that path expires, the
	// other three are still measured.
	follows := fakeRun(t, "throughputForecast", recordedThroughput(t, func(variables map[string]any, forecast map[string]any) {
		echo(variables, forecast)
		forecast["backlogSize"] = 100 + historyOf(variables, "historyWeeks")
	}), nil)
	expired := follows.Expired()
	if len(expired) != 1 || !strings.Contains(expired[0], "throughputForecast.backlogSize is another value") || count(follows.Root("throughputForecast")) != 3 {
		t.Fatalf("a backlog that follows the history: expired %v, %d differences", expired, count(follows.Root("throughputForecast")))
	}
	if follows.Err() == nil {
		t.Fatal("a run with an allowance that expired by measurement is not an error")
	}

	// A value inside a covered block that follows the history expires the block.
	block := fakeRun(t, "throughputForecast", recordedThroughput(t, func(variables map[string]any, forecast map[string]any) {
		echo(variables, forecast)
		forecast["staleWip"] = map[string]any{"__typename": "StaleWipSignal", "p50AgeHours": float64(historyOf(variables, "historyWeeks")), "p90AgeHours": 9.5}
	}), nil)
	if expired := block.Expired(); len(expired) != 1 || !strings.Contains(expired[0], "throughputForecast.staleWip is another value") {
		t.Fatalf("a block that follows the history: expired %v", expired)
	}

	// The answer states the same history for both requests: the probe did
	// not change what the resolver read, so nothing is measured or counted.
	blind := fakeRun(t, "throughputForecast", recordedThroughput(t, func(_ map[string]any, forecast map[string]any) {
		forecast["historyWeeks"] = 12
	}), nil)
	rr = blind.Root("throughputForecast")
	if count(rr) != 0 || len(blind.Expired()) != 0 || len(rr.CodeRead) != 0 || len(probeInvalid(rr)) != 1 || !strings.Contains(probeInvalid(rr)[0], "the answer states a history") || blind.Err() == nil {
		t.Fatalf("a probe that changed nothing: %d differences, expired %v, code read %v, invalid %v", count(rr), blind.Expired(), rr.CodeRead, rr.Invalid)
	}

	// The answer states a history other than the one asked for: 8 weeks for a
	// request of 12, and 4 for the probe. The two echoes differ and the covered
	// values are equal, but the answer does not say that the probe changed what
	// was read: nothing is counted.
	wrongEcho := fakeRun(t, "throughputForecast", recordedThroughput(t, func(variables map[string]any, forecast map[string]any) {
		if historyOf(variables, "historyWeeks") == 12 {
			forecast["historyWeeks"] = 8
		} else {
			forecast["historyWeeks"] = historyOf(variables, "historyWeeks")
		}
	}), nil)
	rr = wrongEcho.Root("throughputForecast")
	if count(rr) != 0 || len(wrongEcho.Expired()) != 0 || len(rr.CodeRead) != 0 || len(probeInvalid(rr)) != 1 || !strings.Contains(probeInvalid(rr)[0], `states a history of "8"`) || wrongEcho.Err() == nil {
		t.Fatalf("an echo that is not the history asked for: %d differences, expired %v, code read %v, invalid %v", count(rr), wrongEcho.Expired(), rr.CodeRead, rr.Invalid)
	}

	// The answer for the second history states no history: not measured.
	silent := fakeRun(t, "throughputForecast", recordedThroughput(t, func(variables map[string]any, forecast map[string]any) {
		echo(variables, forecast)
		if historyOf(variables, "historyWeeks") != 12 {
			forecast["historyWeeks"] = nil
		}
	}), nil)
	rr = silent.Root("throughputForecast")
	if count(rr) != 0 || len(silent.Expired()) != 0 || len(rr.CodeRead) != 0 || len(probeInvalid(rr)) != 1 || silent.Err() == nil {
		t.Fatalf("a second answer that states no history: %d differences, expired %v, code read %v, invalid %v", count(rr), silent.Expired(), rr.CodeRead, rr.Invalid)
	}

	// A covered block with no value is not counted.
	null := fakeRun(t, "throughputForecast", recordedThroughput(t, func(variables map[string]any, forecast map[string]any) {
		echo(variables, forecast)
		forecast["estimateCoverage"] = nil
	}), nil)
	rr = null.Root("throughputForecast")
	if count(rr) != 3 || len(rr.CodeRead) != 0 || len(probeInvalid(rr)) != 1 || !strings.Contains(probeInvalid(rr)[0], "throughputForecast.estimateCoverage") || null.Err() == nil {
		t.Fatalf("a covered block that is null: %d differences, code read %v, invalid %v", count(rr), rr.CodeRead, rr.Invalid)
	}

	// The contract of the operation is not the one the allowance was read
	// against: the allowance expires before anything is measured.
	renewed := fakeRun(t, "throughputForecast", recordedThroughput(t, echo), func(o *Oracle) {
		spec := temporaryAllowances["throughputForecast"]
		spec.Contract = "sha256:another"
		o.Allowances = map[string]temporaryAllowance{"throughputForecast": spec}
	})
	if expired := renewed.Expired(); len(expired) != 1 || !strings.Contains(expired[0], "the contract of operation throughputForecast") || count(renewed.Root("throughputForecast")) != 0 {
		t.Fatalf("an allowance read against another contract: expired %v", expired)
	}

	// A root with no window argument is stated as read from code.
	flow := fakeRun(t, "workGraphFlow", flowPlanes(t), nil).Root("workGraphFlow")
	if count(flow) != 0 || len(flow.CodeRead) != 1 || !strings.Contains(flow.CodeRead[0], "takes no window argument") || len(flow.Expired) != 0 {
		t.Fatalf("a root with no window argument: %d differences, code read %v, expired %v", count(flow), flow.CodeRead, flow.Expired)
	}
}

// A subject with rows in the store and no answer is a finding on every value
// pair: it is never skipped and never counted as a match.
func TestASubjectOfTheStoreWithNoAnswerIsAFinding(t *testing.T) {
	findings := func(rr *RootReport, pair, detail string) int {
		n := 0
		for _, f := range rr.Findings {
			if f.Pair == pair && strings.Contains(f.Detail, detail) {
				n++
			}
		}
		return n
	}
	_, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := store.LatestEstimateCoverage()
	if err != nil {
		t.Fatal(err)
	}
	flowTeams := map[string]bool{}
	for _, row := range extract.Tables[tableWorkItemMetricsDaily] {
		if team, ok := rowString(row, "team_id"); ok && team != "" {
			flowTeams[team] = true
		}
	}
	if len(coverage) == 0 || len(flowTeams) == 0 {
		t.Fatal("the extract has no team with estimate coverage or work item metrics rows")
	}

	// Readiness: no coverage in the ops answer and no acr fact.
	rr := fakeRun(t, "throughputForecast", recordedThroughput(t, func(_ map[string]any, forecast map[string]any) {
		forecast["estimateCoverage"] = nil
	}), nil).Root("throughputForecast")
	if got := findings(rr, "readiness", "on neither side"); got != len(coverage) {
		t.Fatalf("%d teams have no estimate coverage on either side, %d findings; %d compared, %d matches", len(coverage), got, rr.Compared, rr.Matches)
	}
	if rr.Matches != 0 {
		t.Fatalf("%d matches were counted with no answer on either side", rr.Matches)
	}
	if got := findings(rr, "readiness_store", "no acr readiness fact"); got != len(coverage) {
		t.Fatalf("%d teams have store rows and no acr readiness fact, %d findings", len(coverage), got)
	}
	// Flow: a team with work item metrics rows and no flow fact.
	if got := findings(rr, "flow_window", "no acr flow fact"); got != len(flowTeams) {
		t.Fatalf("%d teams have work item metrics rows and no flow fact, %d findings", len(flowTeams), got)
	}

	// Health: a subject with compounding risk rows and no health fact.
	risk := `{"compoundingRisk":{"__typename":"CompoundingRiskResult","orgId":"` + FixtureOrgID + `","breakout":"REPO","generatedAt":"2026-10-01T16:00:00Z","rows":[],"trend":[]}}`
	health := fakeRun(t, "compoundingRisk", fakePlanes{
		facts: noFacts,
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return servedAnswer(t, shape.Root, shape.Operation, risk, len(risk), 32768), nil
		},
		operation: unavailable,
	}, nil).Root("compoundingRisk")
	want := 0
	for _, scope := range []struct {
		name string
		ids  []string
	}{{"repo", store.RepositoryIDs()}, {"team", store.TeamIDs()}} {
		subjects := store.RiskSubjects(scope.name)
		for _, id := range scope.ids {
			if subjects[strings.ToLower(id)] {
				want++
			}
		}
	}
	if got := findings(health, "health_rows", "has no acr health fact"); want == 0 || got != want {
		t.Fatalf("%d subjects have compounding risk rows and no health fact, %d findings", want, got)
	}

	// Health: a fact that states a day for a subject the store has no row of.
	stranger := ""
	for _, id := range store.RepositoryIDs() {
		if !store.RiskSubjects("repo")[id] {
			stranger = id
			break
		}
	}
	if stranger == "" {
		t.Fatal("every repository of the extract has a compounding risk row")
	}
	invented := fakeRun(t, "compoundingRisk", fakePlanes{
		facts: func(request FactsRequest) (json.RawMessage, error) {
			coverage, facts := []any{}, []any{}
			for _, s := range request.Subjects {
				coverage = append(coverage, map[string]any{"kind": "health", "subject": s, "outcome": "read_no_fact"})
				if s.CanonicalID == "repository:"+stranger {
					facts = append(facts, map[string]any{"kind": "health", "subject": s, "fields": map[string]any{"severity_as_of": "2026-09-18", "severity": "elevated"}})
				}
			}
			return json.Marshal(map[string]any{"status": "partial", "facts": facts, "coverage": coverage, "versions": map[string]any{"kinds": map[string]any{}}})
		},
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return servedAnswer(t, shape.Root, shape.Operation, risk, len(risk), 32768), nil
		},
		operation: unavailable,
	}, nil).Root("compoundingRisk")
	if got := findings(invented, "health_rows", "no compounding risk row in the store"); got != 1 {
		t.Fatalf("a health fact with a day for a subject with no store row: %d findings", got)
	}

	// Workload: a team with forecast rows, an empty ops list and no fact.
	forecastTeams := map[string]bool{}
	for _, row := range extract.Tables[tableCapacityForecasts] {
		if team, ok := rowString(row, "team_id"); ok && team != "" {
			forecastTeams[team] = true
		}
	}
	list := `{"capacityForecasts":{"__typename":"CapacityForecastConnection","edges":[],"pageInfo":{"__typename":"PageInfo","hasNextPage":false,"hasPreviousPage":false,"startCursor":null,"endCursor":null},"totalCount":0}}`
	workload := fakeRun(t, "capacityForecasts", fakePlanes{
		facts: noFacts,
		graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
			return servedAnswer(t, shape.Root, shape.Operation, list, len(list), 32768), nil
		},
		operation: unavailable,
	}, nil).Root("capacityForecasts")
	if got := findings(workload, "workload", "no row and no acr workload fact"); len(forecastTeams) == 0 || got != len(forecastTeams) {
		t.Fatalf("%d teams have forecast rows and no answer on either side, %d findings", len(forecastTeams), got)
	}
	if workload.Matches != 0 {
		t.Fatalf("%d matches were counted with no answer on either side", workload.Matches)
	}
}

// A readiness fact the current read lost shows against the store rows, even
// when the two planes agree with each other.
func TestAReadinessFactTheReadLostShowsAgainstTheStore(t *testing.T) {
	_, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := store.LatestEstimateCoverage()
	if err != nil {
		t.Fatal(err)
	}
	// Each team answers one fact on its latest day, with one item less than
	// the store rows hold. lose is what the read lost.
	run := func(lose int64) *RootReport {
		t.Helper()
		planes := recordedThroughput(t, func(variables map[string]any, forecast map[string]any) {
			input, _ := variables["input"].(map[string]any)
			teams, _ := input["teamIds"].([]any)
			team, _ := teams[0].(string)
			stored, ok := coverage[strings.TrimPrefix(team, "team:")]
			if !ok {
				return
			}
			estimated, backlog := stored.Counts["estimated_count"]-lose, stored.Counts["backlog_size"]
			block := map[string]any{"__typename": "EstimateCoverageSignal", "estimatedCount": estimated, "unestimatedCount": stored.Counts["unestimated_count"], "backlogSize": backlog, "ratio": nil}
			if backlog != 0 {
				block["ratio"] = float64(estimated) / float64(backlog)
			}
			forecast["estimateCoverage"] = block
		})
		planes.facts = func(request FactsRequest) (json.RawMessage, error) {
			if request.Kinds[0] != "readiness" {
				return noFacts(request)
			}
			subject := request.Subjects[0]
			stored := coverage[strings.TrimPrefix(subject.CanonicalID, "team:")]
			fact := map[string]any{"kind": "readiness", "subject": subject, "fields": map[string]any{
				"day": stored.Day, "estimated_count": fmt.Sprint(stored.Counts["estimated_count"] - lose),
				"unestimated_count": fmt.Sprint(stored.Counts["unestimated_count"]), "backlog_size": fmt.Sprint(stored.Counts["backlog_size"])}}
			return json.Marshal(map[string]any{"status": "partial", "versions": map[string]any{"kinds": map[string]any{}}, "facts": []any{fact},
				"coverage": []any{map[string]any{"kind": "readiness", "subject": subject, "outcome": "truncated"}}})
		}
		return fakeRun(t, "throughputForecast", planes, nil).Root("throughputForecast")
	}
	count := func(rr *RootReport, pair, path string) int {
		n := 0
		for _, f := range rr.Findings {
			if f.Pair == pair && (path == "" || f.Path == path) {
				n++
			}
		}
		return n
	}
	whole := run(0)
	if got := count(whole, "readiness_store", "acr:readiness.estimated_count") + count(whole, "readiness_store", "acr:readiness.day"); got != 0 {
		t.Fatalf("facts that hold what the store holds: %d findings against the store", got)
	}
	cut := run(1)
	if got := count(cut, "readiness", ""); got != 0 {
		t.Fatalf("the two planes agree and the ops pair has %d findings: %+v", got, cut.Findings)
	}
	if got := count(cut, "readiness_store", "acr:readiness.estimated_count"); got != len(coverage) {
		t.Fatalf("%d teams lost an estimated item in the read, %d findings against the store", len(coverage), got)
	}
	// One fact for a team whose store rows are more than one work scope.
	several := 0
	for _, stored := range coverage {
		if stored.Scopes != 1 {
			several++
		}
	}
	if got := count(whole, "readiness_store", "acr:readiness.scopes"); several == 0 || got != several {
		t.Fatalf("%d teams have more than one work scope in the store, %d scope findings", several, got)
	}
}

// Every output path of a value root is compared or excluded with a reason,
// and the plan is held against the production policy.
func TestTheValuePathPlanCoversThePolicy(t *testing.T) {
	policy := mustPolicy(t)
	if err := checkValuePaths(policy); err != nil {
		t.Fatal(err)
	}
	valueRoots := 0
	for root, pair := range rootPairs {
		if pair.Mode == ModeValue {
			valueRoots++
			compared := 0
			for _, rule := range valuePaths[root] {
				if rule.Reason == "" {
					compared++
				}
			}
			if compared == 0 {
				t.Errorf("value root %s compares no output path", root)
			}
		}
	}
	if valueRoots != 5 || len(valuePaths) != 5 {
		t.Fatalf("%d value roots, %d plans, want 5 and 5", valueRoots, len(valuePaths))
	}
	saved := valuePaths["catalog"]
	defer func() { valuePaths["catalog"] = saved }()
	// An output path the plan does not name stops the run.
	valuePaths["catalog"] = saved[:1]
	if err := checkValuePaths(policy); err == nil || !strings.Contains(err.Error(), "catalog.values[*].count") {
		t.Fatalf("an output path with no rule passed: %v", err)
	}
	// A rule for a path the policy does not serve stops the run.
	valuePaths["catalog"] = append(append([]pathRule{}, saved...), pathRule{Path: "catalog.values[*].share"})
	if err := checkValuePaths(policy); err == nil || !strings.Contains(err.Error(), "covers no output path") {
		t.Fatalf("a rule for a path that is not served passed: %v", err)
	}
	// Two rules for one path stop the run.
	valuePaths["catalog"] = append(append([]pathRule{}, saved...), pathRule{Path: "catalog.values", Reason: "all of it"})
	if err := checkValuePaths(policy); err == nil || !strings.Contains(err.Error(), "has 2 rules") {
		t.Fatalf("an output path with two rules passed: %v", err)
	}
	valuePaths["catalog"] = saved

	// A planned path the run compared nothing on, and a compared path that
	// is not planned, both make the root invalid.
	rr := &RootReport{Root: "catalog"}
	checkTouched(rr)
	if len(rr.Invalid) != 1 || !strings.Contains(rr.Invalid[0], "catalog.values[*].value is planned as compared") {
		t.Fatalf("a planned path with no compare: %v", rr.Invalid)
	}
	rr = &RootReport{Root: "catalog"}
	rr.touch("catalog.values[*].value", "catalog.values[*].count", "acr:identity.name")
	checkTouched(rr)
	if len(rr.Invalid) != 1 || !strings.Contains(rr.Invalid[0], "catalog.values[*].count, which is not a compared path") {
		t.Fatalf("a compared path outside the plan: %v", rr.Invalid)
	}
}

// The fact plan is held against the providers' own field declaration: a field
// a provider declares and the plan does not name stops the run, and so does a
// plan entry no provider declares.
func TestTheFactPlanCoversTheProvidersDeclaration(t *testing.T) {
	providers := devhealthfacts.NewProviders(nil)
	if err := checkFactPlanDeclared(providers); err != nil {
		t.Fatal(err)
	}
	declared := declaredFactFields(providers)
	if len(declared) != len(factReads) || len(factReads) != 6 {
		t.Fatalf("%d declared kinds, %d read kinds, want 6 and 6", len(declared), len(factReads))
	}
	compared := 0
	for kind, plan := range factPlan {
		for name, reason := range plan {
			if reason == "" {
				compared++
				if !declared[kind][name] {
					t.Errorf("the plan compares %s.%s, which the provider does not declare", kind, name)
				}
			}
		}
	}
	if compared == 0 {
		t.Fatal("the plan compares no fact field")
	}
	saved := factPlan["readiness"]
	defer func() { factPlan["readiness"] = saved }()
	without := map[string]string{}
	for name, reason := range saved {
		if name != "daily_readiness_omitted_count" {
			without[name] = reason
		}
	}
	factPlan["readiness"] = without
	if err := checkFactPlanDeclared(providers); err == nil || !strings.Contains(err.Error(), "daily_readiness_omitted_count") {
		t.Fatalf("a declared field with no plan entry passed: %v", err)
	}
	extra := map[string]string{"confidence": "not declared"}
	for name, reason := range saved {
		extra[name] = reason
	}
	factPlan["readiness"] = extra
	if err := checkFactPlanDeclared(providers); err == nil || !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("a plan entry no provider declares passed: %v", err)
	}
}

// A fact field or a table column the plan does not name stops the read.
func TestAFactFieldOutsideThePlanStopsTheRead(t *testing.T) {
	fact := ServedFact{Kind: "readiness", Fields: map[string]any{"day": "2026-09-30", "estimated_count": "3"}}
	if err := checkFactPlan(fact); err != nil {
		t.Fatalf("a planned fact is refused: %v", err)
	}
	fact.Fields["confidence"] = "high"
	if err := checkFactPlan(fact); err == nil || !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("a field outside the plan passed: %v", err)
	}
	table := ServedFact{Kind: "flow", Tables: map[string]ServedTable{"daily_flow": {Columns: []string{"day", "items_started", "items_reopened"}}}}
	if err := checkFactPlan(table); err == nil || !strings.Contains(err.Error(), "daily_flow.items_reopened") {
		t.Fatalf("a table column outside the plan passed: %v", err)
	}
	if err := checkFactPlan(ServedFact{Kind: "delivery"}); err == nil {
		t.Fatal("a fact kind with no plan passed")
	}
	// A read that returns such a fact stops the run.
	oracle := &Oracle{Planes: fakePlanes{facts: func(request FactsRequest) (json.RawMessage, error) {
		return json.Marshal(map[string]any{"status": "complete", "versions": map[string]any{"kinds": map[string]any{}},
			"coverage": []any{map[string]any{"kind": "identity", "subject": request.Subjects[0], "outcome": "fact_served"}},
			"facts":    []any{map[string]any{"kind": "identity", "subject": request.Subjects[0], "fields": map[string]any{"name": "a/b", "visibility": "private"}}}})
	}}}
	if _, err := oracle.readFacts(context.Background(), "identity", "repository", []string{"r1"}, readCurrent); err == nil || !strings.Contains(err.Error(), "visibility") {
		t.Fatalf("a read with a field outside the plan passed: %v", err)
	}
}

// A current read the provider marks truncated is refused, unless the pair
// holds every compared fact against the store.
func TestATruncatedReadIsRefused(t *testing.T) {
	truncated := func(request FactsRequest) (json.RawMessage, error) {
		return json.Marshal(map[string]any{"status": "partial", "versions": map[string]any{"kinds": map[string]any{}},
			"coverage": []any{map[string]any{"kind": request.Kinds[0], "subject": request.Subjects[0], "outcome": "truncated"}}, "facts": []any{}})
	}
	oracle := &Oracle{Planes: fakePlanes{facts: truncated}, Window: Window{Start: mustDay(t, "2026-09-01"), End: mustDay(t, "2026-10-01")}}
	for name, mode := range map[string]int{"windowed": readWindowed, "current": readCurrent} {
		if _, err := oracle.readFacts(context.Background(), "identity", "repository", []string{"r1"}, mode); err == nil || !strings.Contains(err.Error(), "not measured whole") {
			t.Errorf("a truncated %s read passed: %v", name, err)
		}
	}
	if _, err := oracle.readFacts(context.Background(), "readiness", "team", []string{"t1"}, readCurrentHeldToStore); err != nil {
		t.Fatalf("a truncated read that the pair holds to the store is refused: %v", err)
	}
}

// A read_facts answer is about the subjects that were asked.
func TestAFactAnswerMustNameTheSubjectsAsked(t *testing.T) {
	answerFor := func(coverage, fact string) func(FactsRequest) (json.RawMessage, error) {
		return func(request FactsRequest) (json.RawMessage, error) {
			kind := request.Kinds[0]
			return json.Marshal(map[string]any{"status": "complete", "versions": map[string]any{"kinds": map[string]any{}},
				"coverage": []any{map[string]any{"kind": kind, "subject": map[string]any{"kind": "repository", "canonical_id": coverage}, "outcome": "available"}},
				"facts":    []any{map[string]any{"kind": kind, "subject": map[string]any{"kind": "repository", "canonical_id": fact}, "fields": map[string]any{}}}})
		}
	}
	read := func(planes fakePlanes) error {
		oracle := &Oracle{Planes: planes, Window: Window{Start: mustDay(t, "2026-09-01"), End: mustDay(t, "2026-10-01")}}
		_, err := oracle.readFacts(context.Background(), "identity", "repository", []string{"r1"}, readCurrent)
		return err
	}
	if err := read(fakePlanes{facts: answerFor("repository:r2", "repository:r2")}); err == nil || !strings.Contains(err.Error(), "which was not asked") {
		t.Fatalf("an answer about another subject was read: %v", err)
	}
	// The same canonical id under another subject kind is another subject.
	kindless := func(request FactsRequest) (json.RawMessage, error) {
		kind := request.Kinds[0]
		return json.Marshal(map[string]any{"status": "complete", "versions": map[string]any{"kinds": map[string]any{}},
			"coverage": []any{map[string]any{"kind": "identity", "subject": map[string]any{"kind": "team", "canonical_id": "repository:r1"}, "outcome": "available"}},
			"facts":    []any{map[string]any{"kind": kind, "subject": map[string]any{"kind": "repository", "canonical_id": "repository:r1"}, "fields": map[string]any{}}}})
	}
	if err := read(fakePlanes{facts: kindless}); err == nil || !strings.Contains(err.Error(), "subject kind") {
		t.Fatalf("a coverage row of another subject kind was read: %v", err)
	}
	otherKind := func(request FactsRequest) (json.RawMessage, error) {
		return json.Marshal(map[string]any{"status": "complete", "versions": map[string]any{"kinds": map[string]any{}},
			"coverage": []any{map[string]any{"kind": "readiness", "subject": map[string]any{"kind": "repository", "canonical_id": "repository:r1"}, "outcome": "available"}},
			"facts":    []any{}})
	}
	if err := read(fakePlanes{facts: otherKind}); err == nil || !strings.Contains(err.Error(), "is of kind readiness") {
		t.Fatalf("a coverage row of another fact kind was read: %v", err)
	}
	factKind := func(request FactsRequest) (json.RawMessage, error) {
		kind := request.Kinds[0]
		return json.Marshal(map[string]any{"status": "complete", "versions": map[string]any{"kinds": map[string]any{}},
			"coverage": []any{map[string]any{"kind": kind, "subject": map[string]any{"kind": "repository", "canonical_id": "repository:r1"}, "outcome": "available"}},
			"facts":    []any{map[string]any{"kind": kind, "subject": map[string]any{"kind": "team", "canonical_id": "repository:r1"}, "fields": map[string]any{}}}})
	}
	if err := read(fakePlanes{facts: factKind}); err == nil || !strings.Contains(err.Error(), "a fact of") {
		t.Fatalf("a fact of another subject kind was read: %v", err)
	}
	// Two subjects asked, one covered twice and one not at all.
	twice := func(request FactsRequest) (json.RawMessage, error) {
		kind := request.Kinds[0]
		row := map[string]any{"kind": kind, "subject": map[string]any{"kind": "repository", "canonical_id": "repository:r1"}, "outcome": "available"}
		return json.Marshal(map[string]any{"status": "complete", "versions": map[string]any{"kinds": map[string]any{}}, "coverage": []any{row, row}, "facts": []any{}})
	}
	oracle := &Oracle{Planes: fakePlanes{facts: twice}, Window: Window{Start: mustDay(t, "2026-09-01"), End: mustDay(t, "2026-10-01")}}
	if _, err := oracle.readFacts(context.Background(), "identity", "repository", []string{"r1", "r2"}, readCurrent); err == nil || !strings.Contains(err.Error(), "coverage rows, want 1") {
		t.Fatalf("a subject with no coverage row was read: %v", err)
	}
	if err := read(fakePlanes{facts: answerFor("repository:r1", "repository:r2")}); err == nil || !strings.Contains(err.Error(), "a fact names") {
		t.Fatalf("a fact of another subject was read: %v", err)
	}
}

// The request a runner sends must be the request of the case.
func TestTheReplayedRequestIsBoundToItsCase(t *testing.T) {
	shape, ok := ShapeByID(mustShapes(t), "throughputForecast/throughputForecast/branch:staleWip")
	if !ok {
		t.Fatal("shape is not generated")
	}
	client := map[string]any{"input": map[string]any{"teamIds": []any{"team:t1"}, "historyWeeks": 12}}
	selection := []string{"throughputForecast.staleWip.p50AgeHours", "throughputForecast.staleWip.p90AgeHours", "throughputForecast.staleWip.__typename"}
	upstream := func(raw string) map[string]any {
		t.Helper()
		value, err := decodeJSON([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]any)
	}
	good := `{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["t1"],"historyWeeks":12}}`
	if problems := boundRequest(shape, false, client, FixtureOrgID, upstream(good), selection); len(problems) != 0 {
		t.Fatalf("the request of the case is refused: %v", problems)
	}
	for name, c := range map[string]struct {
		variables string
		selection []string
		want      string
	}{
		"a dropped variable":                     {`{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["t1"]}}`, selection, "input.historyWeeks did not reach the listener"},
		"a changed variable":                     {`{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["t1"],"historyWeeks":4}}`, selection, "input.historyWeeks reached the listener with another value"},
		"another subject":                        {`{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["t2"],"historyWeeks":12}}`, selection, "input.teamIds reached the listener with another value"},
		"a subject id not converted":             {`{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["team:t1"],"historyWeeks":12}}`, selection, "input.teamIds reached the listener with another value"},
		"a variable the client did not set":      {`{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["t1"],"historyWeeks":12,"backlogSize":5}}`, selection, "input.backlogSize, which the client did not set"},
		"another organization":                   {`{"orgId":"another","input":{"teamIds":["t1"],"historyWeeks":12}}`, selection, "orgId is not the caller's organization"},
		"an argument the document does not have": {`{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["t1"],"historyWeeks":12},"debug":true}`, selection, "the argument debug, which the registered document does not have"},
		"a variable with no rule":                {`{"orgId":"` + FixtureOrgID + `","input":{"teamIds":["t1"],"historyWeeks":12,"debug":true}}`, selection, "input.debug, which has no rule"},
		"a field that is not selected":           {good, selection[:2], "does not select throughputForecast.staleWip.__typename"},
		"a field outside the shape":              {good, append(append([]string{}, selection...), "throughputForecast.backlogSize"), "selects throughputForecast.backlogSize, which is not in the selection of the case"},
	} {
		problems := boundRequest(shape, false, client, FixtureOrgID, upstream(c.variables), c.selection)
		if len(problems) != 1 || !strings.Contains(problems[0], c.want) {
			t.Errorf("%s: problems %v, want one with %q", name, problems, c.want)
		}
	}
	// run_operation must select what the registered document selects: the
	// selection of one branch is not the request of that tool.
	if problems := boundRequest(shape, true, client, FixtureOrgID, upstream(good), selection); len(problems) == 0 {
		t.Fatal("a run_operation request that selects one branch of the registered document passed")
	}
	// A value the registered document writes itself (the REPO dimension of
	// the repository scope list) must arrive as that value.
	scopes, ok := ShapeByID(mustShapes(t), "catalog/acrRepositoryScopes/all")
	if !ok {
		t.Fatal("shape is not generated")
	}
	all := []string{"catalog.values.value", "catalog.values.count", "catalog.values.__typename", "catalog.__typename"}
	if problems := boundRequest(scopes, false, map[string]any{}, FixtureOrgID, upstream(`{"orgId":"`+FixtureOrgID+`","dimension":"REPO"}`), all); len(problems) != 0 {
		t.Fatalf("the request of the repository scope list is refused: %v", problems)
	}
	if problems := boundRequest(scopes, false, map[string]any{}, FixtureOrgID, upstream(`{"orgId":"`+FixtureOrgID+`","dimension":"TEAM"}`), all); len(problems) != 1 || !strings.Contains(problems[0], "dimension is not the value the registered document writes") {
		t.Fatalf("another dimension than the document's: %v", problems)
	}
}

// A denied read says which subjects it asked for and what each coverage row
// said, so the denial can be told apart from an empty read.
func TestADeniedFactsReadNamesTheSubjectAndItsOutcome(t *testing.T) {
	oracle := &Oracle{Planes: fakePlanes{facts: func(request FactsRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"denied","facts":[],"coverage":[{"kind":"readiness","subject":{"kind":"team","canonical_id":"team:t1"},"outcome":"denied_by_authorization"}]}`), nil
	}}}
	_, err := oracle.readFacts(context.Background(), "readiness", "team", []string{"t1"}, readCurrentHeldToStore)
	if err == nil || !strings.Contains(err.Error(), "status denied") || !strings.Contains(err.Error(), "team:t1=denied_by_authorization") || !strings.Contains(err.Error(), "asked 1 subjects") {
		t.Fatalf("denial error: %v", err)
	}
}

func readinessRun(t *testing.T, planes Planes) (*Report, error) {
	t.Helper()
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, ShapeCases: manifest.ShapeCases, OnlyRoots: []string{"throughputForecast"}, Planes: planes}
	return oracle.Run(context.Background())
}

func deniedFacts(request FactsRequest) (json.RawMessage, error) {
	coverage := []any{}
	for _, s := range request.Subjects {
		coverage = append(coverage, map[string]any{"kind": request.Kinds[0], "subject": s, "outcome": "denied_or_not_found"})
	}
	return json.Marshal(map[string]any{"status": "denied", "facts": []any{}, "coverage": coverage, "versions": map[string]any{"kinds": map[string]any{}}})
}

// A team the token has no grant for is recorded as not joined with its
// reason and the run goes on; the root fails only when no team joins.
func TestADeniedReadinessTeamIsNotJoinedAndTheRunContinues(t *testing.T) {
	echo := func(variables map[string]any, forecast map[string]any) {
		forecast["historyWeeks"] = historyOf(variables, "historyWeeks")
	}
	var asked []string
	planes := recordedThroughput(t, echo)
	planes.facts = func(request FactsRequest) (json.RawMessage, error) {
		if request.Kinds[0] == "readiness" {
			asked = append(asked, request.Subjects[0].CanonicalID)
			if len(asked) == 1 {
				return deniedFacts(request)
			}
		}
		return noFacts(request)
	}
	report, err := readinessRun(t, planes)
	if err != nil {
		t.Fatalf("one denied team stopped the run: %v", err)
	}
	if len(asked) < 2 {
		t.Fatalf("the test needs at least two teams, asked %v", asked)
	}
	var notJoined []string
	for _, n := range report.Root("throughputForecast").NotJoined {
		if strings.HasPrefix(n, "readiness: team ") {
			notJoined = append(notJoined, n)
		}
	}
	if len(notJoined) != 1 || !strings.Contains(notJoined[0], strings.TrimPrefix(asked[0], "team:")) || !strings.Contains(notJoined[0], "denied_or_not_found") {
		t.Fatalf("not joined: %v, first asked %s", notJoined, asked[0])
	}
}

func TestEveryReadinessTeamDeniedFailsTheRoot(t *testing.T) {
	echo := func(variables map[string]any, forecast map[string]any) {
		forecast["historyWeeks"] = historyOf(variables, "historyWeeks")
	}
	planes := recordedThroughput(t, echo)
	planes.facts = func(request FactsRequest) (json.RawMessage, error) { return deniedFacts(request) }
	report, err := readinessRun(t, planes)
	text := fmt.Sprint(err)
	if report != nil {
		text += fmt.Sprint(report.Root("throughputForecast").Invalid)
	}
	if !strings.Contains(text, "every one of") || !strings.Contains(text, "teams was denied") {
		t.Fatalf("all teams denied did not fail the root: %v", text)
	}
}
