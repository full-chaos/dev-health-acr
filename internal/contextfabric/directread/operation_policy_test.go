package directread

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	contractCopyPath   = "../../../contracts/mcp/operations.v1.json"
	vendoredRegistry   = "../../../contracts/mcp/ops-catalogue/registry.v1.json"
	restrictedServedOp = "compoundingRisk"
)

func loadDefault(t *testing.T) *Catalogue {
	t.Helper()
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatalf("embedded catalogue does not load: %v", err)
	}
	return cat
}

func catalogueFile(t *testing.T) CatalogueFile {
	t.Helper()
	var file CatalogueFile
	if err := json.Unmarshal(EmbeddedCatalogueJSON(), &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func encodeFile(t *testing.T, file CatalogueFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func operationIndex(t *testing.T, file CatalogueFile, name string) int {
	t.Helper()
	for i, op := range file.Operations {
		if op.Name == name {
			return i
		}
	}
	t.Fatalf("no served operation %q", name)
	return -1
}

type vendoredRow struct {
	Operation string `json:"operation"`
	Document  string `json:"document"`
	Kind      string `json:"kind"`
}

func vendoredRows(t *testing.T) map[string]vendoredRow {
	t.Helper()
	data, err := os.ReadFile(vendoredRegistry)
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Rows []vendoredRow `json:"rows"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	out := map[string]vendoredRow{}
	for _, r := range reg.Rows {
		out[r.Operation] = r
	}
	return out
}

func TestEmbeddedCopyIsByteIdenticalToContract(t *testing.T) {
	contract, err := os.ReadFile(contractCopyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contract, EmbeddedCatalogueJSON()) {
		t.Fatal("internal/contextfabric/directread/operations.v1.json differs from contracts/mcp/operations.v1.json; run go run ./cmd/operationpolicy")
	}
}

func TestCatalogueServesSixteenAndThree(t *testing.T) {
	cat := loadDefault(t)
	if got := len(cat.Operations(CallerUnrestricted)); got != 19 {
		t.Fatalf("unrestricted caller: %d operations, want 19", got)
	}
	var restricted []string
	for _, op := range cat.Operations(CallerRestricted) {
		restricted = append(restricted, op.Name)
	}
	if !slices.Equal(restricted, []string{"compoundingRisk", "hotspots", "securityAlerts"}) {
		t.Fatalf("restricted caller: %v, want compoundingRisk, hotspots, securityAlerts", restricted)
	}
	for _, class := range PrincipalClassVocabulary() {
		want := CallerRestricted
		if class != ClassRestricted {
			want = CallerUnrestricted
		}
		if got := CallerClassFor(class); got != want {
			t.Errorf("CallerClassFor(%s) = %s, want %s", class, got, want)
		}
	}
	if got := CallerClassFor(PrincipalClass("bogus")); got != CallerRestricted {
		t.Errorf("an unknown principal class maps to %s, want restricted (fail closed)", got)
	}
	for _, op := range cat.Operations(CallerRestricted) {
		scope := op.Scope(CallerRestricted)
		rule, ok := op.Variable(scope.ForcedVariablePath)
		if !ok || !rule.Allowed || rule.Subject == nil || rule.Subject.Kind != SubjectKindRepository {
			t.Errorf("%s: forced path %q is not an allowed repository variable", op.Name, scope.ForcedVariablePath)
		}
		for _, row := range scope.RowIDPaths {
			if !op.OutputAllowed(row) {
				t.Errorf("%s: row id path %q is not an allowed output", op.Name, row)
			}
		}
	}
	_, _, refusal := cat.LookupFor("securityOverview", CallerRestricted)
	if refusal == nil || refusal.Code != RefusalOperationNotServedForCaller || !strings.Contains(refusal.Reason, "overview.go") {
		t.Fatalf("securityOverview for a restricted caller: %+v, want operation_not_served_for_caller with file:line evidence", refusal)
	}
}

// T11 (load half and lookup path). The mutation list is read from the
// catalogue (kind mutation), and cross-checked against the vendored ops dump.
func TestT11MutationsAndMalformedDocumentsRefused(t *testing.T) {
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	cat := loadDefault(t)
	rows := vendoredRows(t)
	var mutations []string
	for _, ns := range cat.NotServed() {
		if ns.Kind == DocumentKindMutation {
			mutations = append(mutations, ns.Name)
		}
	}
	var dumped []string
	for name, row := range rows {
		if row.Kind == DocumentKindMutation {
			dumped = append(dumped, name)
		}
	}
	slices.Sort(dumped)
	if len(mutations) != 5 || !slices.Equal(mutations, dumped) {
		t.Fatalf("catalogue mutations %v, vendored dump mutations %v; the design names 5", mutations, dumped)
	}

	for _, name := range append(slices.Clone(mutations), "busFactor", "operatingReview", "noSuchOperation") {
		op, refusal := cat.Lookup(name)
		if op != nil || refusal == nil || refusal.Code != RefusalUnknownOperation {
			t.Errorf("Lookup(%s) = %v, %+v; want unknown_operation", name, op, refusal)
		}
		for _, class := range CallerClassVocabulary() {
			if _, _, refusal := cat.LookupFor(name, class); refusal == nil || refusal.Code != RefusalUnknownOperation {
				t.Errorf("LookupFor(%s, %s) = %+v; want unknown_operation", name, class, refusal)
			}
		}
		for _, class := range CallerClassVocabulary() {
			for _, op := range cat.Operations(class) {
				if op.Name == name {
					t.Errorf("%s is servable to %s", name, class)
				}
			}
		}
	}

	base := catalogueFile(t)
	if _, err := LoadCatalogue(encodeFile(t, base)); err != nil {
		t.Fatalf("baseline artifact re-encoded does not load: %v", err)
	}
	hotspots := operationIndex(t, base, "hotspots")
	withText := func(file *CatalogueFile, i int, text, declaredName string) {
		op := &file.Operations[i]
		op.DocumentText = text
		op.DocumentOperationName = declaredName
		op.Digest = DocumentDigest(text) // recomputed: the digest check must not be what fails
	}
	clone := func() CatalogueFile {
		var file CatalogueFile
		if err := json.Unmarshal(encodeFile(t, base), &file); err != nil {
			t.Fatal(err)
		}
		return file
	}

	type loadCase struct {
		name string
		edit func(file *CatalogueFile)
		want string
	}
	var cases []loadCase
	for _, m := range mutations {
		cases = append(cases, loadCase{"mutation_served_" + m, func(file *CatalogueFile) {
			parsed, err := ParseRegisteredDocument(rows[m].Document)
			if err != nil {
				t.Fatal(err)
			}
			served := file.Operations[operationIndex(t, *file, "acrRepositoryScopes")]
			served.Name = m
			served.DocumentText = rows[m].Document
			served.DocumentOperationName = parsed.Name
			served.Digest = DocumentDigest(rows[m].Document)
			file.Operations = append(file.Operations, served)
			file.NotServed = slices.DeleteFunc(file.NotServed, func(ns NotServedOperation) bool { return ns.Name == m })
		}, "document parses as mutation, want query"})
	}
	cases = append(cases,
		loadCase{"two_operations", func(file *CatalogueFile) {
			withText(file, hotspots, rows["hotspots"].Document+"\n"+rows["acrRepositoryScopes"].Document, "Hotspots")
		}, "document has 2 operations, want exactly 1"},
		loadCase{"subscription", func(file *CatalogueFile) {
			withText(file, hotspots, `subscription Hotspots { hotspots(input: {}) { rows { filePath } } }`, "Hotspots")
		}, "is not served by query-api"},
		loadCase{"query_under_wrong_name", func(file *CatalogueFile) {
			withText(file, hotspots, rows["securityOverview"].Document, "Hotspots")
		}, "document operation name \"SecurityOverview\", declared \"Hotspots\""},
		loadCase{"mutation_text_under_query_name", func(file *CatalogueFile) {
			withText(file, hotspots, rows["triggerReport"].Document, "triggerReport")
		}, "document parses as mutation, want query"},
		loadCase{"declared_kind_mutation", func(file *CatalogueFile) {
			file.Operations[hotspots].Kind = DocumentKindMutation
		}, "declared kind \"mutation\", want query"},
		loadCase{"digest_mismatch", func(file *CatalogueFile) {
			file.Operations[hotspots].DocumentText = strings.Replace(file.Operations[hotspots].DocumentText, "filePath", "filePath riskScore", 1)
		}, "recomputed"},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := clone()
			tc.edit(&file)
			_, err := LoadCatalogue(encodeFile(t, file))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load: %v, want an error containing %q", err, tc.want)
			}
		})
	}

	if got := requests.Load(); got != 0 {
		t.Fatalf("the query service received %d requests; loading and lookup must dial nothing", got)
	}
}

// TestLoaderStructuralGuards plants one structural defect per load rule.
func TestLoaderStructuralGuards(t *testing.T) {
	base := catalogueFile(t)
	clone := func() CatalogueFile {
		var file CatalogueFile
		if err := json.Unmarshal(encodeFile(t, base), &file); err != nil {
			t.Fatal(err)
		}
		return file
	}
	cr := operationIndex(t, base, restrictedServedOp)
	ib := operationIndex(t, base, "investmentBreakdown")
	restrictedScope := func(file *CatalogueFile) *CallerScope {
		for i := range file.Operations[cr].Scopes {
			if file.Operations[cr].Scopes[i].Caller == CallerRestricted {
				return &file.Operations[cr].Scopes[i]
			}
		}
		t.Fatal("no restricted scope")
		return nil
	}
	cases := []struct {
		name string
		edit func(file *CatalogueFile)
		want string
	}{
		{"person_output_without_exception", func(f *CatalogueFile) {
			f.Operations[cr].Outputs = append(f.Operations[cr].Outputs, OutputPath{Path: "compoundingRisk.rows[*].author", Type: "String", Leaf: LeafScalar})
		}, "person-named output"},
		{"json_output_without_exception", func(f *CatalogueFile) {
			for i, o := range f.Operations[ib].Outputs {
				if o.Leaf == LeafJSON {
					f.Operations[ib].Outputs[i].Exception = ""
				}
			}
		}, "free JSON output"},
		{"person_variable_allowed", func(f *CatalogueFile) {
			for i, v := range f.Operations[ib].Variables {
				if v.Path == "batch.filters.who.developers" {
					f.Operations[ib].Variables[i].Allowed = true
					f.Operations[ib].Variables[i].Refusal = nil
					f.Operations[ib].Variables[i].Source = SourceClient
				}
			}
		}, "is allowed"},
		{"person_value_allowed", func(f *CatalogueFile) {
			for i, v := range f.Operations[ib].Variables {
				if v.Path == "batch.breakdowns[*].dimension" {
					f.Operations[ib].Variables[i].AllowedValues = append(f.Operations[ib].Variables[i].AllowedValues, "AUTHOR")
					f.Operations[ib].Variables[i].RefusedValues = nil
				}
			}
		}, "AUTHOR is allowed"},
		{"restricted_without_row_id", func(f *CatalogueFile) { restrictedScope(f).RowIDPaths = nil }, "restricted scope served without"},
		{"restricted_without_forced_path", func(f *CatalogueFile) { restrictedScope(f).ForcedVariablePath = "" }, "restricted scope served without"},
		{"restricted_wrong_subject_kind", func(f *CatalogueFile) { restrictedScope(f).SubjectKind = SubjectKindTeam }, "restricted scope served without"},
		{"restricted_row_id_not_output", func(f *CatalogueFile) {
			restrictedScope(f).RowIDPaths = []string{"compoundingRisk.rows[*].nope"}
		}, "is not an allowed scalar output"},
		{"restricted_forced_non_subject_variable", func(f *CatalogueFile) { restrictedScope(f).ForcedVariablePath = "filter.day" }, "is not an allowed repository variable"},
		{"restricted_forced_team_variable", func(f *CatalogueFile) { restrictedScope(f).ForcedVariablePath = "filter.teamIds" }, "is not an allowed repository variable"},
		{"restricted_forced_unknown_variable", func(f *CatalogueFile) { restrictedScope(f).ForcedVariablePath = "filter.nope" }, "is not a listed variable"},
		{"restricted_forced_refused_variable", func(f *CatalogueFile) {
			i := operationIndex(t, *f, "hotspots")
			for j := range f.Operations[i].Scopes {
				if f.Operations[i].Scopes[j].Caller == CallerRestricted {
					f.Operations[i].Scopes[j].ForcedVariablePath = "input.teamIds"
				}
			}
		}, "is not an allowed repository variable"},
		{"restricted_forced_disallowed_repository_variable", func(f *CatalogueFile) {
			for i, v := range f.Operations[cr].Variables {
				if v.Path == "filter.repoIds" {
					f.Operations[cr].Variables[i].Allowed = false
					f.Operations[cr].Variables[i].Refusal = &Refusal{Code: RefusalVariableNotAllowed, Reason: "x"}
				}
			}
		}, "is not an allowed repository variable"},
		{"variable_neither_allowed_nor_refused", func(f *CatalogueFile) {
			for i, v := range f.Operations[cr].Variables {
				if v.Path == "filter.teamIds" {
					f.Operations[cr].Variables[i].Allowed = false
				}
			}
		}, "allowed and refusal must be exclusive"},
		{"variable_allowed_and_refused", func(f *CatalogueFile) {
			for i, v := range f.Operations[cr].Variables {
				if v.Path == "filter.teamIds" {
					f.Operations[cr].Variables[i].Refusal = &Refusal{Code: RefusalVariableNotAllowed, Reason: "x"}
				}
			}
		}, "allowed and refusal must be exclusive"},
		{"variable_allowed_with_source_none", func(f *CatalogueFile) {
			for i, v := range f.Operations[cr].Variables {
				if v.Path == "filter.teamIds" {
					f.Operations[cr].Variables[i].Source = SourceNotSettable
				}
			}
		}, "allowed with source none"},
		{"enum_allowed_without_values", func(f *CatalogueFile) {
			for i, v := range f.Operations[cr].Variables {
				if v.Path == "filter.breakout" {
					f.Operations[cr].Variables[i].AllowedValues = nil
				}
			}
		}, "allowed with no allowed values"},
		{"not_served_without_reason", func(f *CatalogueFile) { f.NotServed[0].Reason = "" }, "lacks a name, a reason or a known code"},
		{"not_served_without_name", func(f *CatalogueFile) { f.NotServed[0].Name = "" }, "lacks a name, a reason or a known code"},
		{"not_served_unknown_code", func(f *CatalogueFile) { f.NotServed[0].Code = "maybe" }, "lacks a name, a reason or a known code"},
		{"unclassified_registry_row", func(f *CatalogueFile) { f.NotServed = f.NotServed[1:] }, "classified"},
		{"served_and_not_served", func(f *CatalogueFile) {
			f.NotServed = append(f.NotServed, NotServedOperation{Name: "hotspots", Code: RefusalUnknownOperation, Reason: "x"})
			f.Source.RegistryRows++
		}, "both served and not served"},
		{"unknown_refusal_code", func(f *CatalogueFile) {
			for i, v := range f.Operations[ib].Variables {
				if v.Refusal != nil {
					f.Operations[ib].Variables[i].Refusal.Code = "maybe"
					break
				}
			}
		}, "not in vocabulary"},
		{"person_tokens_changed", func(f *CatalogueFile) { f.PersonNameTokens = f.PersonNameTokens[1:] }, "person_name_tokens"},
		{"unknown_field", nil, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var data []byte
			if tc.edit == nil {
				data = bytes.Replace(encodeFile(t, clone()), []byte(`"contract":`), []byte(`"surprise": 1, "contract":`), 1)
			} else {
				file := clone()
				tc.edit(&file)
				data = encodeFile(t, file)
			}
			_, err := LoadCatalogue(data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load: %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestK14AInvestmentShapeIsMachineChecked pins table K14-A in the artifact.
func TestK14AInvestmentShapeIsMachineChecked(t *testing.T) {
	cat := loadDefault(t)
	for _, name := range []string{"investmentBreakdown", "investmentFull"} {
		op, refusal := cat.Lookup(name)
		if refusal != nil {
			t.Fatalf("%s not served: %+v", name, refusal)
		}
		rule := func(path string) VariableRule {
			r, ok := op.Variable(path)
			if !ok {
				t.Fatalf("%s: no rule for %s", name, path)
			}
			return r
		}
		refusedValue := func(r VariableRule, value string) ValueRefusal {
			for _, rv := range r.RefusedValues {
				if rv.Value == value {
					return rv
				}
			}
			t.Fatalf("%s: %s=%s is not refused", name, r.Path, value)
			return ValueRefusal{}
		}
		dim := rule("batch.breakdowns[*].dimension")
		if !dim.Allowed || !slices.Equal(dim.AllowedValues, []string{"THEME", "SUBCATEGORY", "WORK_TYPE"}) {
			t.Errorf("%s: breakdown dimensions %v, want THEME SUBCATEGORY WORK_TYPE", name, dim.AllowedValues)
		}
		for _, v := range []string{"TEAM", "REPO"} {
			if rv := refusedValue(dim, v); rv.Code != RefusalBasisDependentShape || !strings.Contains(rv.Reason, BasisDependentShapeText) {
				t.Errorf("%s: dimension %s refusal %+v", name, v, rv)
			}
		}
		if rv := refusedValue(dim, "AUTHOR"); rv.Code != RefusalPersonScopeNotServed {
			t.Errorf("%s: dimension AUTHOR refusal %+v", name, rv)
		}
		level := rule("batch.filters.scope.level")
		if !slices.Equal(level.AllowedValues, []string{"ORG"}) {
			t.Errorf("%s: scope levels %v, want ORG only", name, level.AllowedValues)
		}
		for _, v := range []string{"TEAM", "REPO"} {
			if rv := refusedValue(level, v); rv.Code != RefusalBasisDependentShape {
				t.Errorf("%s: scope level %s refusal %+v", name, v, rv)
			}
		}
		if rv := refusedValue(level, "DEVELOPER"); rv.Code != RefusalPersonScopeNotServed {
			t.Errorf("%s: scope level DEVELOPER refusal %+v", name, rv)
		}
		for _, path := range []string{"batch.sankey", "batch.filters.what.repos"} {
			r := rule(path)
			if r.Allowed || r.Refusal == nil || r.Refusal.Code != RefusalBasisDependentShape || !strings.Contains(r.Refusal.Reason, BasisDependentShapeText) {
				t.Errorf("%s: %s = %+v, want refused basis_dependent_shape", name, path, r)
			}
		}
		if r := rule("batch.filters.who.developers"); r.Allowed || r.Refusal.Code != RefusalPersonScopeNotServed {
			t.Errorf("%s: who.developers = %+v", name, r)
		}
		if !slices.ContainsFunc(op.Constraints, func(c Constraint) bool {
			return c.Kind == ConstraintEmptyOrAbsent && c.Path == "batch.filters.scope.ids" && c.Code == RefusalBasisDependentShape
		}) {
			t.Errorf("%s: no empty_or_absent constraint on batch.filters.scope.ids", name)
		}
		if r := rule("batch.useInvestment"); r.Source != SourceForced || string(r.ForcedValue) != "true" {
			t.Errorf("%s: useInvestment = %+v, want forced true", name, r)
		}
		if op.Scope(CallerRestricted).Served {
			t.Errorf("%s served to a restricted caller", name)
		}
	}
	if _, refusal := cat.Lookup("operatingReview"); refusal == nil {
		t.Error("operatingReview is served")
	}
}

// T13 (policy half, loader view): every person variable the artifact lists
// is refused by its rule.
func TestT13EveryPersonVariableIsRefused(t *testing.T) {
	cat := loadDefault(t)
	count := 0
	for _, op := range cat.Operations(CallerUnrestricted) {
		for _, pv := range op.PersonVariables {
			count++
			rule, ok := op.Variable(pv.Path)
			if !ok {
				t.Errorf("%s: person path %s has no rule", op.Name, pv.Path)
				continue
			}
			if pv.Value == "" && (rule.Allowed || rule.Refusal.Code != RefusalPersonScopeNotServed) {
				t.Errorf("%s: person path %s not refused", op.Name, pv.Path)
			}
			if pv.Value != "" && slices.Contains(rule.AllowedValues, pv.Value) {
				t.Errorf("%s: person value %s=%s allowed", op.Name, pv.Path, pv.Value)
			}
		}
	}
	if count < 13 {
		t.Fatalf("only %d person variables in the artifact", count)
	}
}

// T17 (runtime half): an unlisted response path is removed and counted; no
// person value survives; an allowlisted scalar path cannot smuggle an object.
func TestT17FilterResponseRemovesUnlistedPaths(t *testing.T) {
	cat := loadDefault(t)
	hotspots, _ := cat.Lookup("hotspots")
	resp := []byte(`{"hotspots":{"__typename":"HotspotsResult","rows":[
		{"filePath":"a.go","repoId":"r1","repoName":"o/r","riskScore":0.5,"author":"Ada Lovelace","topMaintainers":[{"author":"Grace Hopper"}]},
		{"filePath":"b.go","repoId":"r1","repoName":{"email":"alan@example.com"},"riskScore":null}
	],"reviewer":"Linus"},"extraRoot":{"login":"ken"}}`)
	res, err := FilterResponse(hotspots, resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, person := range []string{"Ada Lovelace", "Grace Hopper", "alan@example.com", "Linus", "ken"} {
		if bytes.Contains(res.Data, []byte(person)) {
			t.Errorf("person value %q survived the filter: %s", person, res.Data)
		}
	}
	wantRemoved := []string{"extraRoot", "hotspots.reviewer", "hotspots.rows[*].author", "hotspots.rows[*].repoName", "hotspots.rows[*].topMaintainers"}
	if !slices.Equal(res.RemovedPaths, wantRemoved) {
		t.Errorf("removed paths %v, want %v", res.RemovedPaths, wantRemoved)
	}
	if res.RemovedValues != 5 {
		t.Errorf("removed %d values, want 5", res.RemovedValues)
	}
	if !bytes.Contains(res.Data, []byte(`"filePath":"b.go"`)) || !bytes.Contains(res.Data, []byte(`"riskScore":null`)) || !bytes.Contains(res.Data, []byte(`"__typename":"HotspotsResult"`)) {
		t.Errorf("allowlisted values lost: %s", res.Data)
	}

	edges, _ := cat.Lookup("workGraphEdges")
	res, err = FilterResponse(edges, []byte(`{"workGraphEdges":{"edges":[{"edgeId":"e","evidence":"reviewed by Ada"}],"degradedReason":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(res.Data, []byte("Ada")) || !slices.Equal(res.RemovedPaths, []string{"workGraphEdges.edges[*].evidence"}) {
		t.Errorf("withheld evidence not removed: %s %v", res.Data, res.RemovedPaths)
	}

	breakdown, _ := cat.Lookup("investmentBreakdown")
	res, err = FilterResponse(breakdown, []byte(`{"analytics":{"evidenceQualityDistribution":{"high":3,"low":1},"evidenceQualityStats":{"bandCounts":{"a":1},"mean":1},"breakdowns":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedValues != 0 || !bytes.Contains(res.Data, []byte(`"evidenceQualityDistribution":{"high":3,"low":1}`)) {
		t.Errorf("excepted JSON leaf not kept whole: %s %v", res.Data, res.RemovedPaths)
	}

	// A list where the allowlist expects an object, and an unexpected
	// element list under a scalar leaf, are removed.
	res, err = FilterResponse(hotspots, []byte(`{"hotspots":{"rows":[{"filePath":["x",{"user":"u"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(res.Data, []byte(`"u"`)) || !slices.Equal(res.RemovedPaths, []string{"hotspots.rows[*].filePath"}) {
		t.Errorf("list under a scalar leaf kept: %s %v", res.Data, res.RemovedPaths)
	}
	if _, err := FilterResponse(hotspots, []byte(`[1]`)); err == nil {
		t.Error("a non-object data value was accepted")
	}
	if res, err := FilterResponse(hotspots, []byte(`null`)); err != nil || string(res.Data) != "null" {
		t.Errorf("null data: %s %v", res.Data, err)
	}
}

// TestFilterKeepsScalarListLeaves: a list of scalars under an allowlisted
// "[*]" leaf is kept element by element; an object element is removed.
func TestFilterKeepsScalarListLeaves(t *testing.T) {
	file := catalogueFile(t)
	i := operationIndex(t, file, "hotspots")
	file.Operations[i].Outputs = append(file.Operations[i].Outputs, OutputPath{Path: "hotspots.rows[*].tags[*]", Type: "[String!]", Leaf: LeafScalar})
	cat, err := LoadCatalogue(encodeFile(t, file))
	if err != nil {
		t.Fatal(err)
	}
	op, _ := cat.Lookup("hotspots")
	res, err := FilterResponse(op, []byte(`{"hotspots":{"rows":[{"tags":["a","b",{"author":"x"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(res.Data, []byte(`"tags":["a","b"]`)) || !slices.Equal(res.RemovedPaths, []string{"hotspots.rows[*].tags[*]"}) || res.RemovedValues != 1 {
		t.Fatalf("scalar list leaf: %s %v %d", res.Data, res.RemovedPaths, res.RemovedValues)
	}
}

func TestSubjectConversion(t *testing.T) {
	repo := SubjectConversion{Kind: SubjectKindRepository, AcrPrefix: "repository:", OpsForm: "bare_uuid"}
	team := SubjectConversion{Kind: SubjectKindTeam, AcrPrefix: "team:", OpsForm: "bare_id"}
	const uuid = "7b9583ee-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	if got, err := repo.ToOps("repository:" + uuid); err != nil || got != uuid {
		t.Errorf("repository conversion = %q, %v", got, err)
	}
	if got, err := team.ToOps("team:platform"); err != nil || got != "platform" {
		t.Errorf("team conversion = %q, %v", got, err)
	}
	for _, bad := range []struct {
		conv SubjectConversion
		id   string
	}{
		{repo, uuid},                                              // no prefix: a client string is never forwarded
		{repo, "repository:"},                                     // empty remainder
		{repo, "repository: " + uuid},                             // padded remainder
		{repo, "repository:acme/web"},                             // not a uuid
		{repo, "team:" + uuid},                                    // wrong kind
		{team, "team:"},                                           // empty
		{team, "team: x"},                                         // padded
		{SubjectConversion{}, "anything"},                         // empty prefix never converts
		{repo, "repository:" + uuid + "x"},                        // wrong length
		{repo, "repository:7b9583ee_1c2d-4e5f-8a9b-0c1d2e3f4a5b"}, // wrong separator
	} {
		if got, err := bad.conv.ToOps(bad.id); err == nil {
			t.Errorf("%s ToOps(%q) = %q, want a refusal", bad.conv.Kind, bad.id, got)
		}
	}
	cat := loadDefault(t)
	cr, _ := cat.Lookup("compoundingRisk")
	if r, _ := cr.Variable("filter.repoIds"); r.Subject == nil || *r.Subject != repo {
		t.Errorf("compoundingRisk filter.repoIds subject = %+v", r.Subject)
	}
	if r, _ := cr.Variable("filter.teamIds"); r.Subject == nil || *r.Subject != team {
		t.Errorf("compoundingRisk filter.teamIds subject = %+v", r.Subject)
	}
}

func TestStatusVocabularyAndCompleteness(t *testing.T) {
	if got := CallStatusVocabulary(); got != [5]CallStatus{"served", "refused", "operation_unavailable", "upstream_error", "upstream_timeout"} {
		t.Errorf("call vocabulary %v", got)
	}
	if got := CompletenessVocabulary(); got != [3]Completeness{"declared_complete", "declared_partial", "unknown"} {
		t.Errorf("completeness vocabulary %v", got)
	}
	if got := ResultStateVocabulary(); got != [3]ResultState{"data", "empty_unverified", "empty_declared"} {
		t.Errorf("result vocabulary %v", got)
	}
	for _, tc := range []struct {
		empty bool
		c     Completeness
		want  ResultState
	}{
		{false, CompletenessUnknown, ResultData},
		{true, CompletenessUnknown, ResultEmptyUnverified},
		{true, CompletenessDeclaredPartial, ResultEmptyUnverified},
		{true, CompletenessDeclaredComplete, ResultEmptyDeclared},
	} {
		if got := ResultStateFor(tc.empty, tc.c); got != tc.want {
			t.Errorf("ResultStateFor(%v, %s) = %s, want %s", tc.empty, tc.c, got, tc.want)
		}
	}

	cat := loadDefault(t)
	edges, _ := cat.Lookup("workGraphEdges")
	tf, _ := cat.Lookup("throughputForecast")
	hs, _ := cat.Lookup("hotspots")
	full, _ := cat.Lookup("investmentFull")
	for _, tc := range []struct {
		op   *OperationPolicy
		data string
		want Completeness
	}{
		{edges, `{"workGraphEdges":{"degradedReason":"MEMBERSHIP_NOT_MATERIALIZED","edges":[]}}`, CompletenessDeclaredPartial},
		{edges, `{"workGraphEdges":{"degradedReason":null,"edges":[]}}`, CompletenessUnknown},
		{tf, `{"throughputForecast":{"insufficientHistory":true}}`, CompletenessDeclaredPartial},
		{tf, `{"throughputForecast":{"insufficientHistory":false}}`, CompletenessUnknown},
		{full, `{"analytics":{"sankey":{"coverage":{"teamCoverage":0.4,"repoCoverage":1}}}}`, CompletenessDeclaredPartial},
		{full, `{"analytics":{"sankey":null}}`, CompletenessUnknown},
		{hs, `{"hotspots":{"rows":[]}}`, CompletenessUnknown},
	} {
		if got := tc.op.Completeness([]byte(tc.data)); got != tc.want {
			t.Errorf("%s Completeness(%s) = %s, want %s", tc.op.Name, tc.data, got, tc.want)
		}
	}
}

func TestRefusalVocabularyIsClosed(t *testing.T) {
	want := []string{"unknown_operation", "variable_not_allowed", "variable_out_of_range", "person_scope_not_served", "basis_dependent_shape", "no_granted_scope", "operation_not_served_for_caller", "response_budget", "invalid_request", "scope_required", "row_outside_grant", "policy_stale"}
	got := RefusalCodeVocabulary()
	for i, code := range got {
		if string(code) != want[i] {
			t.Errorf("refusal %d = %s, want %s", i, code, want[i])
		}
	}
	file := catalogueFile(t)
	for _, op := range file.Operations {
		for _, v := range op.Variables {
			if v.Refusal != nil && !knownRefusalCode(v.Refusal.Code) {
				t.Errorf("%s %s: code %s", op.Name, v.Path, v.Refusal.Code)
			}
		}
	}
}
