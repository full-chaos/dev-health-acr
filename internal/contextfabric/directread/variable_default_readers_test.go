package directread

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const directreadImportPath = "github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"

// allowedRawDefaultReaders are the only non-test places that touch the raw
// VariableRule.Default: the helper itself, the clamp that compares against
// it, the forced-value reader (a forced variable is not an advertised
// default) and the generator that writes the field.
var allowedRawDefaultReaders = map[string]bool{
	"internal/contextfabric/directread/operation_policy.go:EffectiveDefault": true,
	"internal/contextfabric/directread/operation_edge.go:applyCostClamps":    true,
	"internal/contextfabric/directread/operation_edge.go:applyAcrValues":     true,
	"cmd/operationpolicy/generate.go:":                                       true,
}

// rawDefaultUses lists every use of VariableRule.Default in the non-test code
// of the packages that import directread, as "file:enclosing function".
func rawDefaultUses(t *testing.T, extra ...string) []string {
	t.Helper()
	patterns := []string{
		directreadImportPath,
		"github.com/full-chaos/dev-health-acr/cmd/operationpolicy",
		"github.com/full-chaos/dev-health-acr/internal/api",
		"github.com/full-chaos/dev-health-acr/internal/contextfabric/factoracle",
		"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph",
		"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow",
		"github.com/full-chaos/dev-health-acr/internal/mcp/guide/guidegen",
		"github.com/full-chaos/dev-health-acr/internal/runtime/hosted",
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps}
	pkgs, err := packages.Load(cfg, append(patterns, extra...)...)
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}
	var field *types.Var
	for _, pkg := range pkgs {
		if pkg.PkgPath != directreadImportPath {
			continue
		}
		if obj, ok := pkg.Types.Scope().Lookup("VariableRule").(*types.TypeName); ok {
			if st, ok := obj.Type().Underlying().(*types.Struct); ok {
				for i := 0; i < st.NumFields(); i++ {
					if st.Field(i).Name() == "Default" {
						field = st.Field(i)
					}
				}
			}
		}
	}
	if field == nil {
		t.Fatal("VariableRule.Default was not found: the guard measured nothing")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			name := pkg.Fset.Position(file.Pos()).Filename
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			rel, err := filepath.Rel(root, name)
			if err != nil {
				t.Fatal(err)
			}
			rel = filepath.ToSlash(rel)
			for _, decl := range file.Decls {
				fn, _ := decl.(*ast.FuncDecl)
				funcName := ""
				if fn != nil {
					funcName = fn.Name.Name
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					id, ok := n.(*ast.Ident)
					if ok && pkg.TypesInfo.Uses[id] == field {
						seen[rel+":"+funcName] = true
					}
					return true
				})
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestOnlyTheEffectiveDefaultHelperReadsTheRawVariableDefault(t *testing.T) {
	uses := rawDefaultUses(t)
	if len(uses) < 3 {
		t.Fatalf("found %d uses of VariableRule.Default; the guard measured too little: %v", len(uses), uses)
	}
	for _, use := range uses {
		file, _, _ := strings.Cut(use, ":")
		if !allowedRawDefaultReaders[use] && !allowedRawDefaultReaders[file+":"] {
			t.Errorf("%s reads VariableRule.Default directly; use EffectiveDefault for anything a client is shown or that a clamp applies", use)
		}
	}
}
