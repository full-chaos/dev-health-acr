package v1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Every value a producer mints into a repository authorization list under the
// reserved namespace must be one the graph gate names as repository-absence,
// and a producer must take it from this package rather than spell it.
func TestEveryMintedRepositoryAbsenceSentinelIsNamedHere(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the source tree")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	declaring := filepath.Join(filepath.Dir(thisFile), "context_fabric_types.go")
	const namespace = "acr-context-fabric:"
	found := 0
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, isLit := n.(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				return true
			}
			value, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !strings.HasPrefix(value, namespace) {
				return true
			}
			found++
			if path != declaring {
				t.Errorf("%s spells %q: use the constant declared in contracts/v1", fset.Position(lit.Pos()), value)
			}
			if !strings.HasPrefix(value, ContextFabricReservedOrganizationScopePrefix) && !ContextFabricIsRepositoryAbsenceSentinel(value) {
				t.Errorf("%s: %q is minted under the reserved namespace but is not in the repository-absence set", fset.Position(lit.Pos()), value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 7 {
		t.Fatalf("scan saw %d namespace literals; expected the 7 declared here, the scan did not run as intended", found)
	}
}
