package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every function in this package that inserts a credential or revokes or
// rotates one must append its lifecycle outbox row. The sweep reads the
// producer (the package's own source), so a new mutating site that does not
// call the writer fails here without a database.
func TestEveryCredentialMutationSiteAppendsALifecycleRow(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]bool{"appendLifecycle": true, "appendRotationLifecycle": true}
	// The package-level rotateCredential helper runs the UPDATE and the insert
	// for the method of the same name, which appends the rows around it.
	exempt := map[string]bool{"rotateCredential": true}
	var mutating, covered []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				mutates, appends := false, false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.CallExpr:
						switch fun := node.Fun.(type) {
						case *ast.Ident:
							if fun.Name == "insertCredential" || fun.Name == "rotateCredential" {
								mutates = true
							}
						case *ast.SelectorExpr:
							if writers[fun.Sel.Name] {
								appends = true
							}
						}
					case *ast.BasicLit:
						if node.Kind != token.STRING {
							break
						}
						text, _ := strconv.Unquote(node.Value)
						if strings.Contains(text, "UPDATE acr.client_credentials") && (strings.Contains(text, "revoked_at =") || strings.Contains(text, "rotated_at =")) {
							mutates = true
						}
					}
					return true
				})
				if !mutates {
					continue
				}
				name := fn.Name.Name
				if fn.Recv != nil {
					name = "(method) " + name
				}
				mutating = append(mutating, name)
				if appends || (fn.Recv == nil && exempt[fn.Name.Name]) {
					covered = append(covered, name)
				}
			}
		}
	}
	sort.Strings(mutating)
	sort.Strings(covered)
	if strings.Join(mutating, ",") != strings.Join(covered, ",") {
		t.Fatalf("credential mutation sites without a lifecycle outbox append:\nmutating: %v\ncovered:  %v", mutating, covered)
	}
	for _, site := range []string{"(method) createCredential", "(method) revokeCredential", "(method) rotateCredential", "(method) rollbackCredentialRotation", "(method) redeem", "(method) revokeUnacknowledgedTx", "(method) rotateBrowserRefreshTx", "revokeBrowserFamilyTx"} {
		if !slices.Contains(mutating, site) {
			t.Errorf("the sweep did not find the mutation site %s (found %v)", site, mutating)
		}
	}
}
