package factoracle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2"

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
		for _, o := range op.Outputs {
			allowed[o.Path] = true
		}
		if len(shape.Paths) == 0 {
			t.Errorf("%s selects nothing", shape.ID())
		}
		for _, path := range shape.Paths {
			if !allowed[path] {
				t.Errorf("%s selects %s, which the policy does not allow", shape.ID(), path)
			}
		}
		if shape.Name == "all" && len(shape.Paths) != len(op.Outputs) {
			t.Errorf("%s selects %d of %d allowed paths", shape.ID(), len(shape.Paths), len(op.Outputs))
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
	if len(roots) != len(policy.Roots()) || len(roots) != 14 {
		t.Fatalf("shapes cover %d roots, the policy allows %d, want 14", len(roots), len(policy.Roots()))
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
	graphQL func(shape Shape, variables map[string]any) (json.RawMessage, error)
}

func (f fakePlanes) GraphQL(_ context.Context, shape Shape, variables map[string]any) (json.RawMessage, error) {
	return f.graphQL(shape, variables)
}

func (fakePlanes) Facts(context.Context, FactsRequest) (json.RawMessage, error) {
	return nil, context.Canceled
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
	oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, ShapeCases: cases,
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
	var out map[string][]string
	if err := json.Unmarshal([]byte(evidence), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || len(out["prs"]) != 1 || len(out["issues"]) != 2 || out["prs"][0] != s.UUID(id)+"#pr12" ||
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
	if len(extractTables) != 10 {
		t.Fatalf("%d extract tables, want 10", len(extractTables))
	}
	// A declared column with no rule stops the capture.
	missing := tableSpec{Table: "repos", Rules: map[string]columnRule{}}
	for column, rule := range extractTables[4].Rules {
		if column != "tags" {
			missing.Rules[column] = rule
		}
	}
	if extractTables[4].Table != "repos" {
		t.Fatal("extract table order changed; fix this test")
	}
	if err := checkSpecList([]tableSpec{missing}); err == nil || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("a declared column with no rule passed: %v", err)
	}
	extra := tableSpec{Table: "repos", Rules: map[string]columnRule{"not_a_column": ruleKeep}}
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
	short.Tables["repos"] = short.Tables["repos"][1:]
	if err := short.Write(filepath.Join(dir, ExtractDir)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := LoadCapture(dir); err == nil || !strings.Contains(err.Error(), "repos") {
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
		"work_unit_investments": {
			unit("live", "2026-09-12 00:00:00.000", "r1"),
			unit("superseded", "2026-09-12 00:00:00.000", "r1"),
			unit("outside", "2026-09-12 00:00:00.000", "r1"),
			unit("nulled", "2026-09-13 00:00:00.000", nil), unit("nulled", "2026-09-12 00:00:00.000", "r1"),
		},
		"repos":                     {{"id": "r1", "repo": "o/r1", "provider": "github", "last_synced": "2026-09-01 00:00:00.000"}},
		"work_unit_supersessions":   {{"superseded_work_unit_id": "superseded"}},
		"work_unit_membership_runs": {{"run_id": "old", "completed_at": "2026-09-01 00:00:00.000"}, {"run_id": "run", "completed_at": "2026-09-20 00:00:00.000"}},
		"work_unit_membership": {{"run_id": "run", "work_unit_id": "live"}, {"run_id": "run", "work_unit_id": "superseded"},
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
				if shape.Name != "all" {
					return servedAnswer(t, "workGraphFlow", "workGraphFlow", `{"workGraphFlow":{}}`, 20, 32768), nil
				}
				return answer, nil
			}}}
		report, rerr := oracle.Run(context.Background())
		if rerr != nil {
			t.Fatal(rerr)
		}
		return report.Root("workGraphFlow")
	}
	if rr := run(servedAnswer(t, "workGraphFlow", "workGraphFlow", good, len(good), 32768)); len(rr.Findings) != 0 || rr.Leaves != 6 {
		t.Fatalf("a well-formed answer: %d leaves, findings %+v", rr.Leaves, rr.Findings)
	}
	for name, answer := range map[string]json.RawMessage{
		"an integer served as a string": servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":"3"`, 1), 100, 32768),
		"a float where an integer is":   servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":3.5`, 1), 100, 32768),
		"a path that was not selected":  servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":3,"evidence":"text"`, 1), 100, 32768),
		"a null in a non-null field":    servedAnswer(t, "workGraphFlow", "workGraphFlow", strings.Replace(good, `"inflow":3`, `"inflow":null`, 1), 100, 32768),
		"another operation in the echo": servedAnswer(t, "workGraphFlow", "hotspots", good, 100, 32768),
		"another root in the echo":      servedAnswer(t, "hotspots", "workGraphFlow", good, 100, 32768),
		"more bytes than the limit":     servedAnswer(t, "workGraphFlow", "workGraphFlow", good, 40000, 32768),
		"no byte count":                 servedAnswer(t, "workGraphFlow", "workGraphFlow", good, 0, 32768),
		"a refusal":                     json.RawMessage(`{"call":"refused","refusal":{"code":"response_budget"}}`),
		"an upstream error":             json.RawMessage(`{"call":"upstream_error","errors":[{"class":"server_error"}]}`),
	} {
		if rr := run(answer); len(rr.Findings) == 0 {
			t.Errorf("%s: the shape pass found nothing", name)
		}
	}
	// A root that is not enabled on the listener is stated, not found.
	if rr := run(json.RawMessage(`{"call":"operation_unavailable","errors":[{"class":"not_found"}]}`)); len(rr.Findings) != 0 || rr.Listener != "operation_unavailable" {
		t.Fatalf("a root that is not enabled: listener %s, findings %+v", rr.Listener, rr.Findings)
	}
}
