package mcp

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// T7 (design J.5): no person data. The scan reads the REAL definitions: the
// operations catalogue's variable and output allowlists, the Go response
// types of find_subjects and data_catalog and run_operation, and the six tool
// schemas. The person list is design H's: author, reviewer, assignee,
// maintainer, developer, login, email, identity, user, committer, actor,
// createdBy. It is deliberately NOT the loader's own substring rule
// (directread.IsPersonNamed): a second, independent reading is what makes the
// scan a check on the loader instead of a copy of it.

var personWords = []string{"author", "reviewer", "assignee", "maintainer", "developer", "login", "email", "identity", "user", "committer", "actor", "createdby"}

// personFieldExceptions is the list of explicit exceptions. The design says
// there must be none; the test below holds it empty.
var personFieldExceptions = map[string]string{}

var wordSplit = regexp.MustCompile(`[A-Z]?[a-z]+|[A-Z]+[a-z]*|[0-9]+`)

// personHit reports the person word a path segment names, or "". A segment
// matches when one of its words (split on case changes, underscores and
// hyphens) is a person word or its plural, or when the whole segment is
// createdBy. "authorization" is not "author".
func personHit(segment string) string {
	lower := strings.ToLower(segment)
	if lower == "createdby" || lower == "created_by" {
		return "createdby"
	}
	for _, word := range wordSplit.FindAllString(segment, -1) {
		word = strings.ToLower(word)
		for _, p := range personWords {
			if word == p || word == p+"s" {
				return p
			}
		}
	}
	return ""
}

func segmentsOf(path string) []string {
	path = strings.NewReplacer("[*]", "", "[", ".", "]", "").Replace(path)
	return strings.Split(path, ".")
}

func personHitsInPath(kind, path string) []string {
	var hits []string
	for _, segment := range segmentsOf(path) {
		if p := personHit(segment); p != "" {
			if _, excepted := personFieldExceptions[path]; !excepted {
				hits = append(hits, kind+": "+path+" names a person ("+p+")")
			}
			break
		}
	}
	return hits
}

// catalogueHits scans an operations catalogue artifact (JSON) for person data
// in what a caller may SET (allowed variable paths and allowed enum values)
// and what a caller may RECEIVE (allowlisted output paths).
func catalogueHits(t *testing.T, artifact []byte) (hits []string, inspected int) {
	t.Helper()
	var file struct {
		Operations []struct {
			Name      string `json:"name"`
			Variables []struct {
				Path          string   `json:"path"`
				Allowed       bool     `json:"allowed"`
				AllowedValues []string `json:"allowed_values"`
			} `json:"variables"`
			Outputs []struct {
				Path string `json:"path"`
			} `json:"outputs"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(artifact, &file); err != nil {
		t.Fatal(err)
	}
	for _, op := range file.Operations {
		for _, v := range op.Variables {
			if !v.Allowed {
				continue
			}
			inspected++
			hits = append(hits, personHitsInPath(op.Name+" variable", v.Path)...)
			for _, value := range v.AllowedValues {
				inspected++
				if p := personHit(value); p != "" {
					hits = append(hits, op.Name+" variable "+v.Path+" allows the value "+value+" ("+p+")")
				}
			}
		}
		for _, o := range op.Outputs {
			inspected++
			hits = append(hits, personHitsInPath(op.Name+" output", o.Path)...)
		}
	}
	return hits, inspected
}

// structHits walks a Go type's exported fields by their JSON names.
func structHits(t reflect.Type, path string, seen map[reflect.Type]bool, inspected *int) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return nil
	}
	seen[t] = true
	var hits []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		*inspected++
		child := path + "." + name
		if p := personHit(name); p != "" {
			hits = append(hits, "type "+child+" names a person ("+p+")")
		}
		hits = append(hits, structHits(field.Type, child, seen, inspected)...)
	}
	return hits
}

func schemaPropertyHits(node any, path string, inspected *int) []string {
	var hits []string
	switch v := node.(type) {
	case map[string]any:
		if props, ok := v["properties"].(map[string]any); ok {
			names := make([]string, 0, len(props))
			for name := range props {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				*inspected++
				if p := personHit(name); p != "" {
					hits = append(hits, "schema "+path+"."+name+" names a person ("+p+")")
				}
				hits = append(hits, schemaPropertyHits(props[name], path+"."+name, inspected)...)
			}
		}
		for _, key := range []string{"items", "additionalProperties"} {
			if child, ok := v[key]; ok {
				hits = append(hits, schemaPropertyHits(child, path+"."+key, inspected)...)
			}
		}
	}
	return hits
}

func TestNoPersonDataInAnyDataToolDefinition(t *testing.T) {
	if len(personFieldExceptions) != 0 {
		t.Fatalf("design J.5 T7: there must be no exceptions, found %d", len(personFieldExceptions))
	}
	var all []string
	total := 0

	// 1. The operations catalogue: what a caller may set and receive.
	root := findRepoRoot(t)
	artifact, err := os.ReadFile(filepath.Join(root, "contracts", "mcp", "operations.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(artifact) != string(directread.EmbeddedCatalogueJSON()) {
		t.Fatal("contracts/mcp/operations.v1.json differs from the artifact the runner embeds; the scan would read a file the runner does not use")
	}
	hits, n := catalogueHits(t, artifact)
	all, total = append(all, hits...), total+n
	if n < 200 {
		t.Fatalf("the catalogue scan inspected %d paths; the measurement did not happen", n)
	}

	// 2. The Go response and request types of the three tools.
	for name, typ := range map[string]reflect.Type{
		"FindResponse":      reflect.TypeOf(directread.FindResponse{}),
		"DataCatalog":       reflect.TypeOf(directread.DataCatalog{}),
		"OperationResponse": reflect.TypeOf(directread.OperationResponse{}),
		"FindRequest":       reflect.TypeOf(contractsv1.MCPFindSubjectsRequest{}),
		"CatalogRequest":    reflect.TypeOf(contractsv1.MCPDataCatalogRequest{}),
		"OperationRequest":  reflect.TypeOf(contractsv1.MCPRunOperationRequest{}),
	} {
		var inspected int
		all = append(all, structHits(typ, name, map[reflect.Type]bool{}, &inspected)...)
		if inspected == 0 {
			t.Fatalf("%s: the type walk inspected no field; the measurement did not happen", name)
		}
		total += inspected
	}

	// 3. The six published tool schemas.
	for _, file := range []string{dataCatalogRequestSchemaFile, dataCatalogResponseSchemaFile, findSubjectsRequestSchemaFile, findSubjectsResponseSchemaFile, runOperationRequestSchemaFile, runOperationResponseSchemaFile} {
		var inspected int
		all = append(all, schemaPropertyHits(readEmbeddedJSON(t, file), file, &inspected)...)
		if inspected < 1 {
			t.Fatalf("%s: the schema walk inspected no property; the measurement did not happen", file)
		}
		total += inspected
	}
	if total < 400 {
		t.Fatalf("T7 inspected %d names in all; the measurement did not happen", total)
	}
	t.Logf("T7 inspected %d names", total)
	if len(all) != 0 {
		t.Fatalf("person data in a data tool definition (design K1/K18):\n  %s", strings.Join(all, "\n  "))
	}
}

// The scan's own guards: each planted person path, value and property is
// found. Without these, an empty result of the scan above could mean the
// scan reads nothing.
func TestPersonScanFindsEveryPlantedDefect(t *testing.T) {
	for _, segment := range []string{"author", "authors", "topMaintainers", "createdBy", "reviewer_id", "assignee", "Developer", "loginName", "userId", "email", "identity", "committerName", "actor"} {
		if personHit(segment) == "" {
			t.Errorf("personHit missed %q", segment)
		}
	}
	for _, segment := range []string{"authorization_repositories", "repoIds", "filePath", "riskScore", "scopeLabel", "canonical_id"} {
		if personHit(segment) != "" {
			t.Errorf("personHit flagged %q, which names no person", segment)
		}
	}

	base, err := os.ReadFile(filepath.Join(findRepoRoot(t), "contracts", "mcp", "operations.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := catalogueHits(t, base); len(hits) != 0 {
		t.Fatalf("the real catalogue already has hits: %v", hits)
	}
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	ops := doc["operations"].([]any)
	first := ops[0].(map[string]any)
	first["variables"] = append(first["variables"].([]any), map[string]any{"path": "filter.author", "type": "String", "kind": "scalar", "allowed": true, "source": "client"})
	first["outputs"] = append(first["outputs"].([]any), map[string]any{"path": "rows[*].reviewer.login", "type": "String", "leaf": "scalar"})
	ops[1].(map[string]any)["variables"] = append(ops[1].(map[string]any)["variables"].([]any), map[string]any{"path": "dimension", "type": "X", "kind": "enum", "allowed": true, "source": "client", "allowed_values": []any{"THEME", "AUTHOR"}})
	planted, _ := json.Marshal(doc)
	hits, _ := catalogueHits(t, planted)
	if len(hits) != 3 {
		t.Fatalf("the three planted person definitions produced %d hits: %v", len(hits), hits)
	}
	// The loader refuses the same plant too; the scan does not depend on that.
	if _, err := directread.LoadCatalogue(planted); err == nil {
		t.Log("note: LoadCatalogue accepted the planted artifact (it must not; the scan is the second guard)")
	}
	var inspected int
	type leaky struct {
		Author string `json:"author"`
	}
	if len(structHits(reflect.TypeOf(leaky{}), "leaky", map[reflect.Type]bool{}, &inspected)) != 1 {
		t.Fatal("the type walk missed a planted author field")
	}
	if len(schemaPropertyHits(map[string]any{"properties": map[string]any{"createdBy": map[string]any{"type": "string"}}}, "x", &inspected)) != 1 {
		t.Fatal("the schema walk missed a planted createdBy property")
	}
}

// T8 (design J.5, A1.7): the direct data tools are model-free on our side.
// A1.8 dropped the "planted model call" test; what remains is structural:
// the files that implement the tools import no interpreter, synthesizer,
// embedder or model client, and name none. The end-to-end half (real MCP
// server, sidecar, API, with the API's model seam armed to panic) is
// TestDataToolsEndToEndAreModelFreeAndScopedToTheCredential.

// forbiddenImports are import paths (exact) and prefixes a data tool file may
// not have. The engine root package holds the interpreter and synthesizer;
// the rest are model runtimes, providers and embedding stacks.
var forbiddenImports = []string{
	"github.com/full-chaos/dev-health-acr/internal/contextfabric", // exact: the engine root
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelconfigcrypto",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memorymodelconfig",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelconfig",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgmodelreceipts",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedcache",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretseedbench",
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph", // holds the vector arm
	"github.com/firebase/genkit",
	"github.com/openai/openai-go",
	"github.com/anthropics/",
	"google.golang.org/genai",
}

func forbiddenImport(path string) bool {
	for _, f := range forbiddenImports {
		if path == f {
			return true
		}
		if f == "github.com/full-chaos/dev-health-acr/internal/contextfabric" {
			continue
		}
		if strings.HasPrefix(path, f+"/") || (strings.HasSuffix(f, "/") && strings.HasPrefix(path, f)) {
			return true
		}
	}
	return false
}

// modelSubstrings and modelExactWords name a model-facing seam in an
// identifier. A short word (llm, vector, embed) must be a whole word so that
// "smallmap" and "embeddedCatalogue" (a go:embed file) are not flagged.
var (
	modelSubstrings = regexp.MustCompile(`(?i)interpret|synthes|embedder|embedding|genkit|openai|anthropic|prompt|investigator|modelruntime`)
	modelExactWords = map[string]bool{"llm": true, "vector": true, "embed": true, "embeddings": true}
)

func namesModelSeam(identifier string) bool {
	if modelSubstrings.MatchString(identifier) {
		return true
	}
	for _, word := range wordSplit.FindAllString(identifier, -1) {
		if modelExactWords[strings.ToLower(word)] {
			return true
		}
	}
	return false
}

func parseGo(t *testing.T, path string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return file, fset
}

// modelSeams returns every import and identifier in a Go source file that
// reaches a model-facing seam. rootAllowed permits the engine root import
// (directread uses its plain data types); identifiers are always checked.
func modelSeams(t *testing.T, path string, rootAllowed bool) []string {
	t.Helper()
	file, _ := parseGo(t, path)
	var found []string
	for _, imp := range file.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if rootAllowed && p == "github.com/full-chaos/dev-health-acr/internal/contextfabric" {
			continue
		}
		if forbiddenImport(p) {
			found = append(found, "imports "+p)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if namesModelSeam(x.Name) {
				found = append(found, "names "+x.Name)
			}
		}
		return true
	})
	return found
}

func TestDataToolFilesReferenceNoModelSeam(t *testing.T) {
	root := findRepoRoot(t)
	files := []string{
		"internal/mcp/data_catalog.go", "internal/mcp/find_subjects.go", "internal/mcp/run_operation.go", "internal/mcp/data_tools.go",
		"internal/sidecar/api_client_data.go", "internal/sidecar/render_data.go",
	}
	for _, rel := range files {
		if seams := modelSeams(t, filepath.Join(root, rel), false); len(seams) != 0 {
			t.Errorf("%s reaches a model-facing seam: %v", rel, seams)
		}
	}
	// The directread package behind the routes: every non-test file, with the
	// engine root allowed for its plain data types only.
	entries, err := os.ReadDir(filepath.Join(root, "internal", "contextfabric", "directread"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// read_facts.go belongs to the read_facts tool, not to these three; it
		// builds a typed fact request that names the plain data struct
		// InterpretedQuestion (no model runs). That tool's own checks are its
		// lane's; the three tools here reach no such file.
		if strings.HasPrefix(name, "read_facts") {
			continue
		}
		checked++
		if seams := modelSeams(t, filepath.Join(root, "internal", "contextfabric", "directread", name), true); len(seams) != 0 {
			t.Errorf("directread/%s reaches a model-facing seam: %v", name, seams)
		}
	}
	if checked < 8 {
		t.Fatalf("scanned only %d directread files; the measurement did not happen", checked)
	}
}

// The scan's own guard: each planted seam is found.
func TestModelSeamScanFindsPlantedSeams(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string]string{
		"engine root import":  "package x\nimport _ \"github.com/full-chaos/dev-health-acr/internal/contextfabric\"\n",
		"model provider":      "package x\nimport _ \"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider\"\n",
		"embed provider":      "package x\nimport _ \"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider\"\n",
		"genkit":              "package x\nimport _ \"github.com/firebase/genkit/go/genkit\"\n",
		"interpreter symbol":  "package x\nvar interpreter int\n",
		"synthesizer symbol":  "package x\nfunc Synthesize() {}\n",
		"embedder symbol":     "package x\ntype Embedder interface{}\n",
		"vector arm symbol":   "package x\nvar vectorArm int\n",
		"investigator symbol": "package x\nvar i Investigator\n",
	}
	for name, src := range cases {
		if len(modelSeams(t, write(strings.ReplaceAll(name, " ", "_")+".go", src), false)) == 0 {
			t.Errorf("the scan missed a planted %s", name)
		}
	}
	if len(modelSeams(t, write("clean.go", "package x\nimport \"encoding/json\"\nvar _ json.RawMessage\n"), false)) != 0 {
		t.Error("the scan flagged clean code")
	}
}

// The scan reads text; this reads the real import graph of the sidecar (the
// hosted API client every data tool goes through), which must not link the
// engine, a model runtime or an embedder even transitively.
func TestSidecarClosureLinksNoModelPackage(t *testing.T) {
	root := findRepoRoot(t)
	sidecarDir := filepath.Join(root, "internal", "sidecar")
	entries, err := os.ReadDir(sidecarDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, _ := parseGo(t, filepath.Join(sidecarDir, name))
		checked++
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if seen[p] {
				continue
			}
			seen[p] = true
			if forbiddenImport(p) {
				t.Errorf("internal/sidecar/%s imports %s", name, p)
			}
		}
	}
	if checked < 30 || len(seen) < 20 {
		t.Fatalf("read %d sidecar files and %d imports; the measurement did not happen", checked, len(seen))
	}
}
