package answerprojection

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestPackageImportsStayPure enforces this package's binding purity
// constraint: the projection must depend on nothing but the contract types
// and the standard library.
//
// This is not style policing. Both the hosted API and the MCP sidecar must
// be able to call Project, and API/MCP answer parity rests on them calling
// exactly this code. An import of an HTTP, MCP, storage, or database
// package would either make one surface unable to use it or invite a second
// projection to appear beside it, and either outcome silently reopens the
// consumer-drift hole CHAOS-3746 closed.
func TestPackageImportsStayPure(t *testing.T) {
	const allowedInternal = "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}
	for name, pkg := range packages {
		for fileName, file := range pkg.Files {
			// Test files may reach for extra helpers; the constraint
			// binds the shipped package.
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatalf("%s: unquote import %s: %v", fileName, spec.Path.Value, err)
				}
				if !strings.Contains(path, "/") || !strings.HasPrefix(path, "github.com/") {
					continue // standard library
				}
				if path == allowedInternal {
					continue
				}
				t.Errorf("package %s file %s imports %q; answerprojection may import only the standard library and %s", name, fileName, path, allowedInternal)
			}
		}
	}
}
