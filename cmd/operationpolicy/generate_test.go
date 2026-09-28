package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const repoRoot = "../.."

func vendoredInputs(t *testing.T) inputs {
	t.Helper()
	in, err := readInputs(repoRoot)
	if err != nil {
		t.Fatalf("read vendored inputs: %v", err)
	}
	return in
}

func rowIndex(t *testing.T, in inputs, name string) int {
	t.Helper()
	for i, row := range in.Registry.Rows {
		if row.Operation == name {
			return i
		}
	}
	t.Fatalf("registry has no %q", name)
	return -1
}

// cloneInputs deep-copies the parts a test mutates.
func cloneInputs(in inputs) inputs {
	out := in
	out.Registry.Rows = slices.Clone(in.Registry.Rows)
	out.Policy = policyDeclaration{Served: map[string]operationDecl{}, NotServed: map[string]notServedDecl{}}
	for k, v := range in.Policy.Served {
		out.Policy.Served[k] = v
	}
	for k, v := range in.Policy.NotServed {
		out.Policy.NotServed[k] = v
	}
	return out
}

func mutationRows(t *testing.T, in inputs) []registryRow {
	t.Helper()
	var out []registryRow
	for _, row := range in.Registry.Rows {
		if row.Kind == directread.DocumentKindMutation {
			out = append(out, row)
		}
	}
	if len(out) != 5 {
		t.Fatalf("vendored catalogue has %d mutations, the design names 5", len(out))
	}
	return out
}

// TestVendoredDigestsRecompute proves acr's digest and kind copies agree
// with ops: every vendored row's digest (ops registrydump output) equals
// acr's recomputation, and every row's kind equals acr's parse.
func TestVendoredDigestsRecompute(t *testing.T) {
	in := vendoredInputs(t)
	if len(in.Registry.Rows) != 55 {
		t.Fatalf("vendored registry has %d rows, the dump at ops 9dffd5f77 has 55", len(in.Registry.Rows))
	}
	for _, row := range in.Registry.Rows {
		if got := directread.DocumentDigest(row.Document); got != row.Digest {
			t.Errorf("%s: acr digest %s, ops dump %s", row.Operation, got, row.Digest)
		}
		parsed, err := directread.ParseRegisteredDocument(row.Document)
		if err != nil || parsed.Kind != row.Kind {
			t.Errorf("%s: acr kind %q (%v), ops dump %q", row.Operation, parsed.Kind, err, row.Kind)
		}
	}
	// ops internal/queryapi/digest/digest_test.go pins, copied by value.
	pins := map[string]string{
		"": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"\n\n  " + opsPinnedFeatureFlags + "  \n\n": "555bc9f82339b8321f309a26d310c4a7e41e79b9b155da41f62d8e97b50da8b7",
	}
	for text, want := range pins {
		if got := directread.DocumentDigest(text); got != want {
			t.Errorf("DocumentDigest(%q) = %s, want %s", text, got, want)
		}
	}
	if got := directread.SchemaDigestOf(nil); got != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("SchemaDigestOf(empty) = %s", got)
	}
}

// opsPinnedFeatureFlags is the featureFlags text ops pins in
// internal/queryapi/digest/digest_test.go (value 555bc9f8...).
const opsPinnedFeatureFlags = `query FeatureFlagRegistry($orgId: String!, $provider: String, $project: String, $includeArchived: Boolean, $limit: Int!) {
  featureFlags(orgId: $orgId, provider: $provider, project: $project, includeArchived: $includeArchived, limit: $limit) {
    flags {
      flagId
      flagKey
      provider
      projectKey
      flagType
      createdAt
      archivedAt
    }
    totalCount
    degradedReason
  }
}`

func TestCommittedArtifactsAreFresh(t *testing.T) {
	if err := run(repoRoot, true); err != nil {
		t.Fatalf("-check: %v", err)
	}
}

// TestCheckModeFailsOnStaleCopy: -check must fail when a committed copy
// differs from a fresh generation (here: one byte appended to the embedded
// copy in a scratch tree).
func TestCheckModeFailsOnStaleCopy(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{registryPath, schemaPath, artifactPath, embeddedCopyPath} {
		data, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		if rel == embeddedCopyPath {
			data = append(data, '\n')
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := run(dir, true)
	if err == nil || !strings.Contains(err.Error(), embeddedCopyPath) {
		t.Fatalf("-check on a stale embedded copy: %v, want a stale error naming %s", err, embeddedCopyPath)
	}
}

func TestServedCounts(t *testing.T) {
	out, err := generate(vendoredInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := directread.LoadCatalogue(out)
	if err != nil {
		t.Fatal(err)
	}
	names := func(ops []*directread.OperationPolicy) []string {
		var n []string
		for _, op := range ops {
			n = append(n, op.Name)
		}
		return n
	}
	wantUnrestricted := []string{"acrRepositoryScopes", "capacityForecast", "capacityForecasts", "catalogValues", "cognitiveLoad", "complexityTimeseries", "compoundingRisk", "hotspots", "investmentBreakdown", "investmentFull", "securityAlerts", "securityOverview", "throughputForecast", "workGraphArtifacts", "workGraphEdges", "workGraphFlow"}
	if got := names(cat.Operations(directread.CallerUnrestricted)); !slices.Equal(got, wantUnrestricted) {
		t.Fatalf("unrestricted = %v (%d), want the 16 of design D.3", got, len(got))
	}
	if got := names(cat.Operations(directread.CallerRestricted)); !slices.Equal(got, []string{"compoundingRisk", "hotspots", "securityAlerts"}) {
		t.Fatalf("restricted = %v, want the 3 of POLICY-ARTIFACT-v0", got)
	}
}

// T11 at generation: the mutation list is read from the vendored catalogue.
func TestT11GenerationRefusesNonQueries(t *testing.T) {
	base := vendoredInputs(t)
	if _, err := generate(base); err != nil {
		t.Fatalf("baseline generation: %v", err)
	}

	for _, m := range mutationRows(t, base) {
		t.Run("mutation_served_"+m.Operation, func(t *testing.T) {
			in := cloneInputs(base)
			delete(in.Policy.NotServed, m.Operation)
			parsed, err := directread.ParseRegisteredDocument(m.Document)
			if err != nil {
				t.Fatal(err)
			}
			decl := in.Policy.Served["acrRepositoryScopes"]
			decl.DocumentName = parsed.Name
			decl.Variables = map[string]variableDecl{}
			in.Policy.Served[m.Operation] = decl
			_, err = generate(in)
			if err == nil || !strings.Contains(err.Error(), "only queries are served") {
				t.Fatalf("serving mutation %s: %v, want the parsed-kind refusal", m.Operation, err)
			}
		})
	}

	hotspots := rowIndex(t, base, "hotspots")
	replaceText := func(in *inputs, text string) {
		row := in.Registry.Rows[hotspots]
		row.Document = text
		row.Digest = directread.DocumentDigest(text)
		in.Registry.Rows[hotspots] = row
	}
	scopes := base.Registry.Rows[rowIndex(t, base, "acrRepositoryScopes")].Document
	overview := base.Registry.Rows[rowIndex(t, base, "securityOverview")].Document
	trigger := base.Registry.Rows[rowIndex(t, base, "triggerReport")].Document

	cases := []struct {
		name string
		edit func(in *inputs)
		want string
	}{
		{"two_operations", func(in *inputs) {
			replaceText(in, base.Registry.Rows[hotspots].Document+"\n"+scopes)
		}, "want exactly 1"},
		{"subscription", func(in *inputs) {
			replaceText(in, "subscription Hotspots { hotspots(input: {orgId: \"o\", sinceUtc: \"x\", untilUtc: \"y\"}) { rows { filePath } } }")
		}, "not served by query-api"},
		{"query_under_wrong_name", func(in *inputs) {
			replaceText(in, overview)
		}, "document operation name \"SecurityOverview\", declared \"Hotspots\""},
		{"mutation_text_under_query_name_row_kind_query", func(in *inputs) {
			replaceText(in, trigger)
		}, "declared kind \"query\", document parses as \"mutation\""},
		{"mutation_text_under_query_name_row_kind_mutation", func(in *inputs) {
			replaceText(in, trigger)
			row := in.Registry.Rows[hotspots]
			row.Kind = directread.DocumentKindMutation
			in.Registry.Rows[hotspots] = row
			decl := in.Policy.Served["hotspots"]
			decl.DocumentName = "triggerReport"
			in.Policy.Served["hotspots"] = decl
		}, "only queries are served"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := cloneInputs(base)
			tc.edit(&in)
			_, err := generate(in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("generation: %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestClassificationIsExact: every registry row is declared exactly once.
func TestClassificationIsExact(t *testing.T) {
	base := vendoredInputs(t)
	t.Run("unclassified_row", func(t *testing.T) {
		in := cloneInputs(base)
		delete(in.Policy.NotServed, "busFactor")
		if _, err := generate(in); err == nil || !strings.Contains(err.Error(), "\"busFactor\" must be declared exactly once") {
			t.Fatalf("unclassified busFactor: %v", err)
		}
	})
	t.Run("both_served_and_not_served", func(t *testing.T) {
		in := cloneInputs(base)
		in.Policy.NotServed["hotspots"] = notServedDecl{Reason: "x"}
		if _, err := generate(in); err == nil || !strings.Contains(err.Error(), "\"hotspots\" must be declared exactly once") {
			t.Fatalf("hotspots twice: %v", err)
		}
	})
	t.Run("digest_mismatch", func(t *testing.T) {
		in := cloneInputs(base)
		i := rowIndex(t, in, "hotspots")
		row := in.Registry.Rows[i]
		row.Document = strings.Replace(row.Document, "filePath", "filePath ", 1)
		in.Registry.Rows[i] = row
		if _, err := generate(in); err == nil || !strings.Contains(err.Error(), "recomputed") {
			t.Fatalf("digest drift: %v", err)
		}
	})
}

// T17: the real busFactor text in the served table fails generation on
// topMaintainers.author, even when every other path is excepted and the
// variable gate is fully closed (a variable-only gate is not enough).
func TestT17BusFactorFailsOnPersonOutput(t *testing.T) {
	base := vendoredInputs(t)
	busFactor := base.Registry.Rows[rowIndex(t, base, "busFactor")]
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: string(base.SDL)})
	if err != nil {
		t.Fatal(err)
	}
	doc, errs := gqlparser.LoadQuery(schema, busFactor.Document)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	nodes, err := walkOutputs(doc.Operations[0])
	if err != nil {
		t.Fatal(err)
	}
	authorPaths := []string{}
	exceptAllButAuthor := map[string]string{}
	for _, n := range nodes {
		if strings.HasSuffix(n.Path, ".author") {
			authorPaths = append(authorPaths, n.Path)
			continue
		}
		// "busFactor" contains the token "actor": the rule over-matches and
		// fails closed; these exceptions isolate the author paths.
		exceptAllButAuthor[n.Path] = "test exception"
	}
	sort.Strings(authorPaths)
	if !slices.Equal(authorPaths, []string{"busFactor.repos[*].topMaintainers[*].author", "busFactor.topMaintainers[*].author"}) {
		t.Fatalf("author paths from the document walk = %v", authorPaths)
	}

	serve := func(exceptions map[string]string) error {
		in := cloneInputs(base)
		delete(in.Policy.NotServed, "busFactor")
		in.Policy.Served["busFactor"] = operationDecl{
			DocumentName:     "BusFactor",
			Cost:             directread.CostSeries,
			Variables:        map[string]variableDecl{"orgId": principalOrg}, // variable gate fully closed
			Unrestricted:     served("test"),
			Restricted:       refusedFor("test"),
			OutputExceptions: exceptions,
		}
		_, err := generate(in)
		return err
	}

	t.Run("variable_gate_alone_is_not_enough", func(t *testing.T) {
		err := serve(exceptAllButAuthor)
		if !errors.Is(err, errPersonOutput) {
			t.Fatalf("busFactor served with a closed variable gate: %v, want the person output refusal", err)
		}
		msg := err.Error()
		idx := strings.Index(msg, errPersonOutput.Error()+": ")
		failing := strings.Split(msg[idx+len(errPersonOutput.Error())+2:], ", ")
		sort.Strings(failing)
		if !slices.Equal(failing, authorPaths) {
			t.Fatalf("failing paths %v, want exactly the author paths %v", failing, authorPaths)
		}
	})
	t.Run("written_exception_admits", func(t *testing.T) {
		all := map[string]string{}
		for k, v := range exceptAllButAuthor {
			all[k] = v
		}
		for _, p := range authorPaths {
			all[p] = "test exception with a reason"
		}
		if err := serve(all); err != nil {
			t.Fatalf("busFactor with written exceptions: %v", err)
		}
	})
	t.Run("free_json_needs_exception", func(t *testing.T) {
		in := cloneInputs(base)
		decl := in.Policy.Served["investmentBreakdown"]
		decl.OutputExceptions = map[string]string{"analytics.evidenceQualityDistribution": "x"}
		in.Policy.Served["investmentBreakdown"] = decl
		_, err := generate(in)
		if !errors.Is(err, errPersonOutput) || !strings.Contains(err.Error(), "analytics.evidenceQualityStats.bandCounts") {
			t.Fatalf("free JSON bandCounts without exception: %v", err)
		}
	})
}

// T13 (policy half): the artifact's person variable paths are exactly the
// ones the SDL walk generates, the list is not empty where the schema has
// person inputs, and every one is refused by the generated rules.
func TestT13PersonVariablesComeFromTheSchema(t *testing.T) {
	base := vendoredInputs(t)
	out, err := generate(base)
	if err != nil {
		t.Fatal(err)
	}
	var file directread.CatalogueFile
	if err := json.Unmarshal(out, &file); err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: string(base.SDL)})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, op := range file.Operations {
		doc, errs := gqlparser.LoadQuery(schema, op.DocumentText)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		nodes, err := walkVariables(schema, doc.Operations[0])
		if err != nil {
			t.Fatal(err)
		}
		want := personVariables(nodes)
		if !slices.Equal(op.PersonVariables, want) {
			t.Errorf("%s: artifact person variables %v, schema walk %v", op.Name, op.PersonVariables, want)
		}
		rules := map[string]directread.VariableRule{}
		for _, v := range op.Variables {
			rules[v.Path] = v
		}
		for _, pv := range op.PersonVariables {
			total++
			rule := rules[pv.Path]
			if pv.Value == "" {
				if rule.Allowed || rule.Refusal == nil || rule.Refusal.Code != directread.RefusalPersonScopeNotServed {
					t.Errorf("%s %s: person path not refused as person scope: %+v", op.Name, pv.Path, rule)
				}
				continue
			}
			if slices.Contains(rule.AllowedValues, pv.Value) {
				t.Errorf("%s %s=%s: person value allowed", op.Name, pv.Path, pv.Value)
			}
			if !slices.ContainsFunc(rule.RefusedValues, func(r directread.ValueRefusal) bool {
				return r.Value == pv.Value && r.Code == directread.RefusalPersonScopeNotServed
			}) {
				t.Errorf("%s %s=%s: person value not refused as person scope", op.Name, pv.Path, pv.Value)
			}
		}
	}
	// The schema has person inputs in the analytics batch and the catalog
	// dimension; an empty list would mean the walk found nothing.
	if total < 13 {
		t.Fatalf("only %d person variables found; the SDL walk is not reaching the person inputs", total)
	}
}

// TestPersonVariableOnAllowlistFailsGeneration plants each person input on
// an allowlist.
func TestPersonVariableOnAllowlistFailsGeneration(t *testing.T) {
	base := vendoredInputs(t)
	cases := []struct {
		name string
		edit func(d *operationDecl)
		want string
	}{
		{"who_developers_path", func(d *operationDecl) { d.Variables["batch.filters.who.developers"] = client }, "person variable \"batch.filters.who.developers\" is on the allowlist"},
		{"dimension_author_value", func(d *operationDecl) {
			v := d.Variables["batch.breakdowns[*].dimension"]
			v.AllowedValues = append(slices.Clone(v.AllowedValues), "AUTHOR")
			d.Variables["batch.breakdowns[*].dimension"] = v
		}, "person value batch.breakdowns[*].dimension=AUTHOR is on the allowlist"},
		{"scope_developer_value", func(d *operationDecl) {
			v := d.Variables["batch.filters.scope.level"]
			v.AllowedValues = []string{"ORG", "DEVELOPER"}
			d.Variables["batch.filters.scope.level"] = v
		}, "person value batch.filters.scope.level=DEVELOPER is on the allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := cloneInputs(base)
			decl := in.Policy.Served["investmentFull"]
			vars := map[string]variableDecl{}
			for k, v := range decl.Variables {
				vars[k] = v
			}
			decl.Variables = vars
			tc.edit(&decl)
			in.Policy.Served["investmentFull"] = decl
			_, err := generate(in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("generation: %v, want %q", err, tc.want)
			}
		})
	}
}

// TestPersonValueFoundByEnumOnly isolates the two clauses of the person
// variable rule: a person-named path segment, and a person-named enum
// value on a path whose segments name no person.
func TestPersonValueFoundByEnumOnly(t *testing.T) {
	nodes := []variableNode{
		{Path: "dimension", Kind: directread.VariableKindEnum, Enum: []string{"TEAM", "AUTHOR"}},
		{Path: "filters.who.developers", Kind: directread.VariableKindScalar},
		{Path: "filters.repoIds", Kind: directread.VariableKindScalar},
	}
	got := personVariables(nodes)
	want := []directread.PersonVariable{{Path: "dimension", Value: "AUTHOR"}, {Path: "filters.who.developers"}}
	if !slices.Equal(got, want) {
		t.Fatalf("personVariables = %v, want %v", got, want)
	}
}

// TestDeclarationNamesRealPaths: a declaration that names a path the
// document does not have fails (stale policy after an SDL change).
func TestDeclarationNamesRealPaths(t *testing.T) {
	base := vendoredInputs(t)
	cases := map[string]func(d *operationDecl){
		"variable": func(d *operationDecl) { d.Variables["input.noSuchField"] = client },
		"refused": func(d *operationDecl) {
			d.RefusedPaths["input.noSuchField"] = refuse(directread.RefusalVariableNotAllowed, "x")
		},
		"withheld":  func(d *operationDecl) { d.WithheldOutputs = map[string]string{"hotspots.rows[*].nope": "x"} },
		"exception": func(d *operationDecl) { d.OutputExceptions = map[string]string{"hotspots.rows[*].nope": "x"} },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			in := cloneInputs(base)
			decl := in.Policy.Served["hotspots"]
			decl.Variables = withVariables(decl.Variables, nil)
			decl.RefusedPaths = withRefusals(decl.RefusedPaths, nil)
			edit(&decl)
			in.Policy.Served["hotspots"] = decl
			if _, err := generate(in); err == nil {
				t.Fatal("generation accepted a declaration naming a path the document does not have")
			}
		})
	}
}

// TestGenerationIsDeterministic: two generations are byte-identical.
func TestGenerationIsDeterministic(t *testing.T) {
	a, err := generate(vendoredInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := generate(vendoredInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("generation is not deterministic")
	}
}
