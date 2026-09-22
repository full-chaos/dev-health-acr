package otelexport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// globalInstalls are the process-global telemetry setters. Any of them called
// with a real provider or handler would hand Genkit (which reads only the
// globals and slog.Default) an exporting pipeline -- see the package doc and
// modelprovider.suppressGenkitTelemetryExport.
var globalInstalls = map[string]map[string]bool{
	"go.opentelemetry.io/otel":            {"SetTracerProvider": true, "SetMeterProvider": true, "SetTextMapPropagator": true},
	"go.opentelemetry.io/otel/log/global": {"SetLoggerProvider": true},
	"log/slog":                            {"SetDefault": true},
	"log":                                 {"SetOutput": true},
}

// allowedGlobalInstalls are the only production call sites permitted: the
// Genkit suppression itself, which installs exporter-less providers.
var allowedGlobalInstalls = []string{
	"internal/contextfabric/modelprovider/provider.go: go.opentelemetry.io/otel.SetMeterProvider",
	"internal/contextfabric/modelprovider/provider.go: go.opentelemetry.io/otel.SetTracerProvider",
}

// TestNoGlobalTelemetryInstallOutsideModelProvider walks every production Go
// file of the module and lists each call of a global telemetry setter. The
// list must equal allowedGlobalInstalls exactly: a new call anywhere fails,
// and so does losing the suppression's own calls (the walk would then be
// measuring nothing).
func TestNoGlobalTelemetryInstallOutsideModelProvider(t *testing.T) {
	root := moduleRoot(t)
	var found []string
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		rel, _ := filepath.Rel(root, path)
		found = append(found, globalInstallCalls(t, path, filepath.ToSlash(rel))...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if files < 100 {
		t.Fatalf("walked only %d production files from %s: the walk is not seeing the module", files, root)
	}
	sort.Strings(found)
	if strings.Join(found, "\n") != strings.Join(allowedGlobalInstalls, "\n") {
		t.Fatalf("global telemetry setter calls differ from the allowed set.\nfound:\n  %s\nallowed:\n  %s",
			strings.Join(found, "\n  "), strings.Join(allowedGlobalInstalls, "\n  "))
	}
}

// globalInstallCalls lists the calls of a globalInstalls function in one
// file, resolving each call's package through the file's own imports (so an
// aliased import is still caught).
func globalInstallCalls(t *testing.T, path, rel string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	local := map[string]string{}
	// dotted holds the watched packages imported with `.`, whose setters are
	// then called as bare identifiers with no selector to match.
	dotted := map[string]bool{}
	for _, spec := range file.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		if _, watched := globalInstalls[importPath]; !watched {
			continue
		}
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "." {
			dotted[importPath] = true
			continue
		}
		local[name] = importPath
	}
	if len(local) == 0 && len(dotted) == 0 {
		return nil
	}
	var calls []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			ident, ok := node.X.(*ast.Ident)
			if !ok {
				return true
			}
			importPath, ok := local[ident.Name]
			if ok && globalInstalls[importPath][node.Sel.Name] {
				calls = append(calls, rel+": "+importPath+"."+node.Sel.Name)
			}
		case *ast.CallExpr:
			// A dot-imported setter is a bare identifier call.
			ident, ok := node.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			for importPath := range dotted {
				if globalInstalls[importPath][ident.Name] {
					calls = append(calls, rel+": "+importPath+"."+ident.Name)
				}
			}
		}
		return true
	})
	return calls
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}
