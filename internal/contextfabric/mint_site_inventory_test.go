package contextfabric_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The mint-site inventory (CHAOS-7252, from CHAOS-7226 r3 P3): every place
// production code mints an acr:v1 evidence ref, DERIVED from the source, so
// the grammar sweep (TestSourceRowGrammarsAreInjective) covers every site
// instead of a hand-kept sample, and a new site fails until it is listed.
//
// A mint site is a call that passes a contractsv1.ContextFabricEvidenceEntity*
// constant to a minting function:
//
//   - contractsv1.EvidenceRefID, and every function that forwards one of its
//     own parameters as the kind of a minting call (a wrapper, found by a
//     fixpoint: devhealthfacts.evidenceRefID, evidenceref.Mint, a provider's
//     readScope, ...). The site's shape is its id expression: "concat/N" for N
//     operands joined by ":" literals (the id a wrapper computes itself is
//     read inside the wrapper), "mint/N" for evidenceref.Mint's N components;
//   - evidenceref.SQL, the ClickHouse spelling of the same grammar
//     ("sqlmint/N");
//   - a string(<kind>) spliced into a SQL literal ("sql/N": the packet
//     catalog's locators, N components read off the SQL concat).
//
// Every other call that passes a kind constant must be a known non-minting
// use (a comparison, evidenceref.IDSQL's id-only column); an unknown one
// fails, because an unclassified use is where a new mint hides.

// mintSite is one derived site: the file (repo-relative), the function or
// package-level var it sits in, the kind constant's name, and its shape.
type mintSite struct {
	file, owner, kind, shape string
}

func (s mintSite) key() string { return s.file + "|" + s.owner }

const moduleImportPrefix = "github.com/full-chaos/dev-health-acr/"

type goFile struct {
	rel, dir string
	ast      *ast.File
	// imports maps an import name to its repo-relative directory.
	imports map[string]string
}

// wrapper is a function that mints with the kind it is given.
type wrapper struct {
	kindParam int
	// idParam is the parameter forwarded as the id, or -1 when the
	// function builds the id itself (innerShape is then its shape).
	idParam    int
	innerShape string
	sql        bool
}

func loadGoFiles(t *testing.T) []goFile {
	t.Helper()
	var files []goFile
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			files = append(files, newGoFile(strings.TrimPrefix(filepath.ToSlash(path), "../../"), parsed))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) < 100 {
		t.Fatalf("read %d files: the walk matched nothing", len(files))
	}
	return files
}

// kindConstant returns the name of a contractsv1.ContextFabricEvidenceEntity*
// constant expression, or "".
func kindConstant(expr ast.Expr) string {
	var name string
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		name = e.Sel.Name
	case *ast.Ident:
		name = e.Name
	}
	if !strings.HasPrefix(name, "ContextFabricEvidenceEntity") || name == "ContextFabricEvidenceEntityType" || strings.HasSuffix(name, "Vocabulary") || strings.HasSuffix(name, "Count") {
		return ""
	}
	return name
}

// callee resolves a call to (directory, function name). A method call on a
// value resolves to the caller's own directory (wrappers are unexported
// helpers of the same package).
func callee(file goFile, call *ast.CallExpr) (string, string) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return file.dir, fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			if dir, imported := file.imports[x.Name]; imported {
				return dir, fn.Sel.Name
			}
		}
		return file.dir, fn.Sel.Name
	}
	return "", ""
}

// concatShape flattens an id expression's + chain: "concat/N" for N
// operands joined only by ":" literals.
func concatShape(expr ast.Expr) string {
	var operands []ast.Expr
	var flatten func(ast.Expr)
	flatten = func(e ast.Expr) {
		if binary, ok := e.(*ast.BinaryExpr); ok && binary.Op == token.ADD {
			flatten(binary.X)
			flatten(binary.Y)
			return
		}
		operands = append(operands, e)
	}
	flatten(expr)
	literals := 0
	for _, operand := range operands {
		if _, ok := operand.(*ast.BasicLit); ok {
			literals++
		}
	}
	if literals == len(operands) {
		// A constant id names a fixture, never a producer's row.
		return "literal"
	}
	count := 0
	for _, operand := range operands {
		if literal, ok := operand.(*ast.BasicLit); ok {
			if value, _ := strconv.Unquote(literal.Value); value != ":" {
				return "concat/?"
			}
			continue
		}
		count++
	}
	return fmt.Sprintf("concat/%d", count)
}

func paramIndex(fn *ast.FuncDecl, name string) int {
	index := 0
	for _, field := range fn.Type.Params.List {
		for _, param := range field.Names {
			if param.Name == name {
				return index
			}
			index++
		}
	}
	return -1
}

func mintIdentName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// ownedNodes calls visit with every top-level function and package-level
// var of the file, named.
func ownedNodes(file goFile, visit func(owner string, fn *ast.FuncDecl, node ast.Node)) {
	for _, decl := range file.ast.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			visit(d.Name.Name, d, d)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if value, ok := spec.(*ast.ValueSpec); ok {
					visit(value.Names[0].Name, nil, value)
				}
			}
		}
	}
}

// deriveWrappers finds, by fixpoint, every function that mints with a kind
// it receives as a parameter.
func deriveWrappers(files []goFile) map[string]wrapper {
	wrappers := map[string]wrapper{"internal/contracts/v1.EvidenceRefID": {kindParam: 0, idParam: 1}}
	for changed := true; changed; {
		changed = false
		for _, file := range files {
			ownedNodes(file, func(owner string, fn *ast.FuncDecl, node ast.Node) {
				if fn == nil || fn.Body == nil {
					return
				}
				self := file.dir + "." + owner
				if _, known := wrappers[self]; known {
					return
				}
				usesPrefix := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if selector, ok := n.(*ast.SelectorExpr); ok && selector.Sel.Name == "ContextFabricEvidenceRefPrefix" {
						usesPrefix = true
					}
					return true
				})
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					dir, name := callee(file, call)
					inner, isWrapper := wrappers[dir+"."+name]
					switch {
					case isWrapper && inner.kindParam < len(call.Args):
						kindParam := paramIndex(fn, mintIdentName(call.Args[inner.kindParam]))
						if kindParam < 0 {
							return true
						}
						found := wrapper{kindParam: kindParam, idParam: -1, sql: inner.sql}
						switch {
						case inner.idParam >= 0 && inner.idParam < len(call.Args) && paramIndex(fn, mintIdentName(call.Args[inner.idParam])) >= 0:
							found.idParam = paramIndex(fn, mintIdentName(call.Args[inner.idParam]))
						case inner.idParam >= 0 && inner.idParam < len(call.Args):
							found.innerShape = concatShape(call.Args[inner.idParam])
						default:
							found.innerShape = inner.innerShape
						}
						wrappers[self] = found
						changed = true
						return false
					case name == "string" && len(call.Args) == 1 && usesPrefix:
						// string(<kind parameter>) spliced next to the ref
						// prefix: the SQL spelling of a mint (evidenceref.SQL).
						if kindParam := paramIndex(fn, mintIdentName(call.Args[0])); kindParam >= 0 {
							wrappers[self] = wrapper{kindParam: kindParam, idParam: -1, innerShape: "sqlmint", sql: true}
							changed = true
							return false
						}
					}
					return true
				})
			})
		}
	}
	return wrappers
}

// sqlSpliceShape reads the component count of a string(<kind>) splice's
// SQL: the text after the splice, up to ") evidence_ref_id" or
// ") AS evidence_ref_id", with each non-literal operand as one expression.
func sqlSpliceShape(operands []ast.Expr, splice int) string {
	var text strings.Builder
	for _, operand := range operands[splice+1:] {
		if literal, ok := operand.(*ast.BasicLit); ok {
			value, _ := strconv.Unquote(literal.Value)
			text.WriteString(value)
		} else {
			text.WriteString("X")
		}
	}
	sql := text.String()
	if !strings.HasPrefix(sql, ":', ") {
		return "sql/?"
	}
	sql = strings.TrimPrefix(sql, ":', ")
	end := strings.Index(sql, ") evidence_ref_id")
	if alt := strings.Index(sql, ") AS evidence_ref_id"); alt >= 0 && (end < 0 || alt < end) {
		end = alt
	}
	if end < 0 {
		return "sql/?"
	}
	args, depth, quoted := 1, 0, false
	for _, char := range sql[:end] {
		switch {
		case char == '\'':
			quoted = !quoted
		case quoted:
		case char == '(':
			depth++
		case char == ')':
			depth--
		case char == ',' && depth == 0:
			args++
		}
	}
	return fmt.Sprintf("sql/%d", (args+1)/2)
}

// nonMintKindUses are the calls that may take a kind constant without
// minting a ref, by callee: an id-only SQL column beside evidenceref.SQL.
var nonMintKindUses = map[string]bool{
	"internal/contextfabric/evidenceref.IDSQL": true,
}

// derivedMintSites is the inventory of the repository's production Go.
func derivedMintSites(t *testing.T) []mintSite {
	t.Helper()
	sites, unclassified := inventory(loadGoFiles(t))
	for _, use := range unclassified {
		t.Error(use)
	}
	return sites
}

// newGoFile builds one parsed file's record.
func newGoFile(rel string, parsed *ast.File) goFile {
	file := goFile{rel: rel, dir: filepath.ToSlash(filepath.Dir(rel)), ast: parsed, imports: map[string]string{}}
	for _, spec := range parsed.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		if !strings.HasPrefix(importPath, moduleImportPrefix) {
			continue
		}
		dir := strings.TrimPrefix(importPath, moduleImportPrefix)
		name := filepath.Base(dir)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		file.imports[name] = dir
	}
	return file
}

// inventory derives the mint sites of files, and every use of a kind
// constant it cannot classify.
func inventory(files []goFile) (sites []mintSite, unclassified []string) {
	wrappers := deriveWrappers(files)
	for _, file := range files {
		if file.dir == "internal/contracts/v1" {
			continue // the vocabulary and EvidenceRefID itself
		}
		ownedNodes(file, func(owner string, fn *ast.FuncDecl, node ast.Node) {
			// Operands of == / != are comparisons, and a string(<kind>) in a
			// + chain is a SQL splice: index both before classifying calls.
			compared := map[ast.Node]bool{}
			spliced := map[ast.Node]string{}
			ast.Inspect(node, func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				if !ok {
					return true
				}
				switch binary.Op {
				case token.EQL, token.NEQ:
					compared[binary.X], compared[binary.Y] = true, true
				case token.ADD:
					var operands []ast.Expr
					var flatten func(ast.Expr)
					flatten = func(e ast.Expr) {
						if inner, ok := e.(*ast.BinaryExpr); ok && inner.Op == token.ADD {
							flatten(inner.X)
							flatten(inner.Y)
							return
						}
						operands = append(operands, e)
					}
					flatten(binary)
					for index, operand := range operands {
						if call, ok := operand.(*ast.CallExpr); ok && mintIdentName(call.Fun) == "string" && len(call.Args) == 1 && kindConstant(call.Args[0]) != "" {
							spliced[call] = sqlSpliceShape(operands, index)
						}
					}
					return false
				}
				return true
			})
			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				dir, name := callee(file, call)
				minting, isWrapper := wrappers[dir+"."+name]
				for index, argument := range call.Args {
					kind := kindConstant(argument)
					if kind == "" {
						continue
					}
					site := mintSite{file: file.rel, owner: owner, kind: kind}
					switch {
					case isWrapper && index == minting.kindParam:
						switch {
						case minting.sql:
							site.shape = fmt.Sprintf("sqlmint/%d", len(call.Args)-1)
						case name == "Mint" && dir == "internal/contextfabric/evidenceref":
							site.shape = fmt.Sprintf("mint/%d", len(call.Args)-1)
						case minting.idParam >= 0 && minting.idParam < len(call.Args):
							site.shape = concatShape(call.Args[minting.idParam])
						default:
							site.shape = minting.innerShape
						}
						sites = append(sites, site)
					case name == "string" && spliced[call] != "":
						site.shape = spliced[call]
						sites = append(sites, site)
					case name == "string" && compared[call]:
					case nonMintKindUses[dir+"."+name]:
					default:
						unclassified = append(unclassified, fmt.Sprintf("%s (%s): %s is passed to %s.%s, which is neither a known mint nor a known non-minting use: classify it in mint_site_inventory_test.go", file.rel, owner, kind, dir, name))
					}
				}
				return true
			})
		})
	}
	sort.Slice(sites, func(i, j int) bool { return fmt.Sprint(sites[i]) < fmt.Sprint(sites[j]) })
	return sites, unclassified
}

// plantedInventory runs the inventory over one planted source file.
type plantedInventory struct {
	sites        []string
	unclassified []string
}

func plantedSites(t *testing.T, src string) plantedInventory {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "planted.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	sites, unclassified := inventory([]goFile{newGoFile("planted/planted.go", parsed)})
	var out plantedInventory
	for _, site := range sites {
		out.sites = append(out.sites, site.owner+"|"+site.kind+"|"+site.shape)
	}
	sort.Strings(out.sites)
	out.unclassified = unclassified
	return out
}

// evidenceKindConstants maps every ContextFabricEvidenceEntity* constant of
// contracts/v1 to its value, read from the declaration.
func evidenceKindConstants(t *testing.T) map[string]string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), "../../internal/contracts/v1/context_fabric_types.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	constants := map[string]string{}
	for _, decl := range parsed.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Values) != 1 || kindConstant(value.Names[0]) == "" {
				continue
			}
			if literal, ok := value.Values[0].(*ast.BasicLit); ok {
				constants[value.Names[0].Name], _ = strconv.Unquote(literal.Value)
			}
		}
	}
	return constants
}
