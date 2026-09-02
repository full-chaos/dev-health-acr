package contextfabric_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The declaration/emission pair, derived from the PROVIDER SOURCE rather
// than from the doc comment beside the declaration.
//
// WHY THIS EXISTS. Round 2 found two declaration defects that every other
// oracle in this package was structurally blind to, because every other
// oracle reads the DECLARATIONS and nothing reads what the queries emit:
//
//   - FactWork declared {completion, remaining_work} under the comment
//     "Raw work counts answer both how much landed and how much is left."
//     WorkProvider emits {"title"} and no counts at all.
//   - FactFlow withheld every repository obligation because it "emits no
//     repository table", though readRepositoryFlow reads repositories and
//     only two obligations in the vocabulary require a table.
//
// Both are the same shape: a sentence about a query, sitting next to a
// declaration, that nothing checks against the query. A comment is not an
// oracle. This walks devhealthfacts' AST, collects the field keys each
// provider actually puts in a CanonicalFact, and pairs them with what that
// producer declares.
//
// WHAT IT DOES AND DOES NOT PROVE. It does not decide whether a given field
// is SUFFICIENT for a given obligation -- that is the semantic judgement
// round 1 bounded (F1) and no test settles it. What it removes is the
// invisibility: the pair is regenerated and DIFFED, so a declaration that
// drifts from its emission shows up as an artifact change in review, which
// is the control. That is the same bar testdata/obligation_seed.txt meets,
// applied to the half of the picture that had no artifact at all.
const emissionArtifact = "testdata/declared_vs_emitted.txt"

const providerSourceDir = "devhealthfacts"

type providerEmission struct {
	kind   string
	fields []string
}

// collectProviderEmissions parses the provider package and returns, per
// fact kind, the field keys its provider emits.
//
// The join is receiver type -> fact kind (from the newCapability call in
// that type's Capability method) and receiver type -> emitted keys (from
// every `Fields: map[string]contextfabric.FactValue{...}` literal inside
// that type's methods). Parsing rather than running is deliberate: the
// provider package starts real ClickHouse containers, and this assertion
// needs no data -- only the code.
func collectProviderEmissions(t *testing.T) map[string]*providerEmission {
	t.Helper()
	return collectEmissionsFrom(t, providerSourceDir, ".go")
}

// collectEmissionsFrom is the collector proper, pointed at a directory, so
// it can be exercised against a fixture as well as the real package.
func collectEmissionsFrom(t *testing.T, dir, suffix string) map[string]*providerEmission {
	t.Helper()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading provider source dir: %v", err)
	}

	kindOfType := map[string]string{}
	fieldsOfType := map[string]map[string]bool{}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, suffix) || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			receiver := receiverTypeName(fn.Recv.List[0].Type)
			if receiver == "" {
				continue
			}
			// PASS 1: which locals in this function hold the fact-field
			// map? Round 3 found the guard reading map LITERALS only, so
			// every field written AFTERWARDS was invisible -- flow.go
			// writes 26 that way, readiness.go 6 (estimate_coverage_ratio,
			// daily_readiness and friends), and the artifact under-reported
			// most providers while claiming to record what they emit. A
			// guard that reads emissions differently from how the code
			// writes them is the third instance of this file's own class.
			factValueVars := map[string]bool{}
			ast.Inspect(fn, func(node ast.Node) bool {
				assign, isAssign := node.(*ast.AssignStmt)
				if !isAssign {
					return true
				}
				for index, rhs := range assign.Rhs {
					composite, isComposite := rhs.(*ast.CompositeLit)
					if !isComposite || !isFactValueMap(composite.Type) || index >= len(assign.Lhs) {
						continue
					}
					if name := identName(assign.Lhs[index]); name != "" && name != "_" {
						factValueVars[name] = true
					}
				}
				return true
			})

			ast.Inspect(fn, func(node ast.Node) bool {
				if call, isCall := node.(*ast.CallExpr); isCall {
					if identName(call.Fun) == "newCapability" && len(call.Args) > 0 {
						if kind := selectorName(call.Args[0]); kind != "" {
							kindOfType[receiver] = kind
						}
					}
				}

				// PASS 2: fields written after initialisation --
				// fields["estimate_coverage_ratio"] = ... -- scoped to the
				// locals pass 1 proved hold a fact-field map, so an
				// unrelated string-keyed map cannot leak in.
				if assign, isAssign := node.(*ast.AssignStmt); isAssign {
					for _, lhs := range assign.Lhs {
						index, isIndex := lhs.(*ast.IndexExpr)
						if !isIndex || !factValueVars[identName(index.X)] {
							continue
						}
						lit, isLit := index.Index.(*ast.BasicLit)
						if !isLit || lit.Kind != token.STRING {
							continue
						}
						key, unquoteErr := strconv.Unquote(lit.Value)
						if unquoteErr != nil {
							continue
						}
						if fieldsOfType[receiver] == nil {
							fieldsOfType[receiver] = map[string]bool{}
						}
						fieldsOfType[receiver][key] = true
					}
				}
				// Match the fact-field map by its TYPE, not by the
				// `Fields:` key. Nine of the twenty-one providers build
				// the map into a local (`fields := map[string]...{...}`)
				// and assign it later, so keying on the struct field name
				// silently reported "(none)" for all nine -- the exact
				// vacuity this file exists to close, reproduced inside the
				// guard itself on its first run.
				if composite, isComposite := node.(*ast.CompositeLit); isComposite && isFactValueMap(composite.Type) {
					for _, key := range mapLiteralKeys(composite) {
						if fieldsOfType[receiver] == nil {
							fieldsOfType[receiver] = map[string]bool{}
						}
						fieldsOfType[receiver][key] = true
					}
				}
				return true
			})
		}
	}

	if len(kindOfType) == 0 {
		t.Fatal("parsed no provider capabilities; this guard would be vacuous")
	}

	out := map[string]*providerEmission{}
	for receiver, kind := range kindOfType {
		keys := make([]string, 0, len(fieldsOfType[receiver]))
		for key := range fieldsOfType[receiver] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out[kind] = &providerEmission{kind: kind, fields: keys}
	}
	return out
}

// factKindConstantName maps a fact kind's WIRE VALUE ("actual_completion")
// to the Go constant the provider source names it by ("FactActualCompletion"),
// which is the join between the parsed AST and the live registry.
//
// Every capability must pair. An unpaired row would silently drop a producer
// out of the artifact -- the same vacuity class this file exists to close --
// so the caller fails on any miss rather than rendering "(not parsed)".
func factKindConstantName(wireValue string) string {
	var name strings.Builder
	name.WriteString("Fact")
	for _, part := range strings.Split(wireValue, "_") {
		if part == "" {
			continue
		}
		name.WriteString(strings.ToUpper(part[:1]))
		name.WriteString(part[1:])
	}
	return name.String()
}

func receiverTypeName(expr ast.Expr) string {
	if star, isStar := expr.(*ast.StarExpr); isStar {
		return identName(star.X)
	}
	return identName(expr)
}

func identName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// selectorName renders contextfabric.FactWork as "FactWork" so the parsed
// name lines up with the vocabulary constant, and tolerates a bare Ident
// for a same-package reference.
func selectorName(expr ast.Expr) string {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return identName(expr)
}

// isFactValueMap reports whether a composite literal's type is the
// map[string]contextfabric.FactValue that every provider fills to build a
// CanonicalFact, whether it is written inline or into a local first.
func isFactValueMap(expr ast.Expr) bool {
	mapType, isMap := expr.(*ast.MapType)
	if !isMap {
		return false
	}
	if identName(mapType.Key) != "string" {
		return false
	}
	return selectorName(mapType.Value) == "FactValue"
}

func mapLiteralKeys(composite *ast.CompositeLit) []string {
	var keys []string
	for _, element := range composite.Elts {
		kv, isKV := element.(*ast.KeyValueExpr)
		if !isKV {
			continue
		}
		lit, isLit := kv.Key.(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			continue
		}
		if unquoted, unquoteErr := strconv.Unquote(lit.Value); unquoteErr == nil {
			keys = append(keys, unquoted)
		}
	}
	return keys
}

// TestDeclaredObligationsAreRecordedBesideWhatTheProviderEmits regenerates
// the declaration/emission artifact and diffs it, the same discipline the
// obligation seed snapshot uses.
func TestDeclaredObligationsAreRecordedBesideWhatTheProviderEmits(t *testing.T) {
	emissions := collectProviderEmissions(t)
	capabilities := liveCapabilities(t)

	var rendered strings.Builder
	rendered.WriteString("# GENERATED. Each row pairs a producer's DECLARED obligations with the\n")
	rendered.WriteString("# field keys its provider actually emits, parsed from the provider\n")
	rendered.WriteString("# source. DO NOT EDIT BY HAND: a test regenerates and diffs this.\n")
	rendered.WriteString("#\n")
	rendered.WriteString("# A row whose declarations do not look supportable by its emitted\n")
	rendered.WriteString("# fields is a REVIEW question, not an automatic failure -- sufficiency\n")
	rendered.WriteString("# is a semantic judgement. What this artifact removes is the case\n")
	rendered.WriteString("# where nobody can see the pair at all.\n")

	kinds := make([]string, 0, len(capabilities))
	for kind := range capabilities {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)

	paired := 0
	for _, name := range kinds {
		capability := capabilities[contextfabric.FactKind(name)]

		subjects := make([]string, 0, len(capability.Obligations))
		for subject := range capability.Obligations {
			subjects = append(subjects, string(subject))
		}
		sort.Strings(subjects)
		declared := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			obligations := capability.Obligations[contextfabric.SubjectKind(subject)]
			names := make([]string, 0, len(obligations))
			for _, obligation := range obligations {
				names = append(names, string(obligation))
			}
			sort.Strings(names)
			declared = append(declared, fmt.Sprintf("%s:[%s]", subject, strings.Join(names, " ")))
		}
		if len(declared) == 0 {
			declared = append(declared, "(none)")
		}

		emission, found := emissions[factKindConstantName(name)]
		if !found {
			t.Errorf("capability %q did not pair with any parsed provider (looked for %q): "+
				"an unpaired producer drops out of the artifact silently, which is the exact "+
				"blindness this guard exists to close", name, factKindConstantName(name))
			continue
		}
		paired++
		emitted := emission.fields
		if len(emitted) == 0 {
			emitted = []string{"(none)"}
		}
		fmt.Fprintf(&rendered, "%s\tdeclares=%s\temits=[%s]\n",
			name, strings.Join(declared, ","), strings.Join(emitted, " "))
	}

	if paired == 0 {
		t.Fatal("no capability was paired with parsed provider source; this guard would be vacuous")
	}
	t.Logf("paired %d of %d capabilities with parsed provider source", paired, len(kinds))

	if *updateSeed {
		if err := os.MkdirAll(filepath.Dir(emissionArtifact), 0o755); err != nil {
			t.Fatalf("creating testdata directory: %v", err)
		}
		if err := os.WriteFile(emissionArtifact, []byte(rendered.String()), 0o644); err != nil {
			t.Fatalf("writing %s: %v", emissionArtifact, err)
		}
		t.Logf("wrote %s", emissionArtifact)
		return
	}

	want, readErr := os.ReadFile(emissionArtifact)
	if readErr != nil {
		t.Fatalf("reading %s: %v (regenerate with -update-seed)", emissionArtifact, readErr)
	}
	if string(want) != rendered.String() {
		t.Errorf("the declaration/emission artifact is stale.\n"+
			"A producer's declarations or its emitted fields changed. This is not automatically wrong -- "+
			"it is the thing review must SEE. Regenerate %s and read the diff.\n\n--- got ---\n%s",
			emissionArtifact, rendered.String())
	}
}

// TestEmissionCollectorSeesFieldsWrittenAfterInitialisation is round 3's
// second finding as an executed assertion, against a fixture rather than
// the shipped package.
//
// The collector read map LITERALS only. flow.go writes 26 fields after
// initialisation and readiness.go 6 (estimate_coverage_ratio,
// daily_readiness...), so the artifact under-reported most providers while
// its own header claimed to record what they emit.
func TestEmissionCollectorSeesFieldsWrittenAfterInitialisation(t *testing.T) {
	emissions := collectEmissionsFrom(t, "testdata/emissionfixture", ".go.txt")

	synthetic, found := emissions["FactSynthetic"]
	if !found {
		t.Fatalf("the fixture provider was not collected at all (got %v); this assertion would be vacuous", emissions)
	}

	got := map[string]bool{}
	for _, field := range synthetic.fields {
		got[field] = true
	}
	for _, want := range []string{"in_literal_one", "in_literal_two", "written_after_init", "written_in_a_branch"} {
		if !got[want] {
			t.Errorf("the collector missed emitted field %q (collected: %v) -- "+
				"a field written after initialisation is emitted exactly as much as one in the literal",
				want, synthetic.fields)
		}
	}
}

// TestAProviderThatEmitsNothingDeclaresNothing is the one part of the pair
// that is decidable without a semantic judgement.
func TestAProviderThatEmitsNothingDeclaresNothing(t *testing.T) {
	emissions := collectProviderEmissions(t)
	capabilities := liveCapabilities(t)

	checked := 0
	for kind, capability := range capabilities {
		emission, found := emissions[factKindConstantName(string(kind))]
		if !found {
			continue
		}
		checked++
		if len(emission.fields) > 0 || len(capability.Obligations) == 0 {
			continue
		}
		t.Errorf("%s emits no fact fields at all and yet declares obligations for %d subject kind(s): "+
			"a producer that puts nothing in a fact cannot serve an answer obligation",
			kind, len(capability.Obligations))
	}
	if checked == 0 {
		t.Fatal("no provider was checked; this guard would be vacuous")
	}
}
