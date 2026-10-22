package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The local sidecar reads one credential family and talks to one hosted API.
// These scans read the production source of the packages that make up acr-mcp.

var environmentName = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$`)

// operatingSystemEnvironment holds the non-ACR_ names the sidecar reads for
// platform facts (proxy routing, desktop session, locale, build identity).
// None names a credential.
var operatingSystemEnvironment = map[string]bool{
	"COMMIT_SHA":               true,
	"GIT_SHA":                  true,
	"LC_ALL":                   true,
	"DBUS_SESSION_BUS_ADDRESS": true,
	"WAYLAND_DISPLAY":          true,
	"XDG_CURRENT_DESKTOP":      true,
	"XDG_RUNTIME_DIR":          true,
	"XDG_SESSION_TYPE":         true,
}

// workloadExchangeNames are the identifiers and environment names of the
// workload token exchange source, which only the panel harness wires.
var workloadExchangeNames = map[string]bool{
	"NewWorkloadCredentialSource":     true,
	"WorkloadCredentialSourceOptions": true,
	"SubjectTokenFileEnvironment":     true,
	"TokenEndpointEnvironment":        true,
	"ACR_SUBJECT_TOKEN_FILE":          true,
	"ACR_TOKEN_ENDPOINT":              true,
}

var forbiddenImportFragments = []string{"internal/panelharness", "cmd/acr-panel-harness"}

func productionFiles(t *testing.T, directory string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var production []string
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") {
			production = append(production, file)
		}
	}
	if len(production) == 0 {
		t.Fatalf("no production files under %s; the scan would be vacuous", directory)
	}
	return production
}

func parseSourceFile(t *testing.T, fileSet *token.FileSet, file string) *ast.File {
	t.Helper()
	parsed, err := parser.ParseFile(fileSet, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return parsed
}

func TestCommandAndToolPackagesNeverWireTheWorkloadExchangeSource(t *testing.T) {
	for _, directory := range []string{".", filepath.Join("..", "..", "internal", "mcp")} {
		for _, file := range productionFiles(t, directory) {
			fileSet := token.NewFileSet()
			parsed := parseSourceFile(t, fileSet, file)
			for _, spec := range parsed.Imports {
				path, _ := strconv.Unquote(spec.Path.Value)
				for _, fragment := range forbiddenImportFragments {
					if strings.Contains(path, fragment) {
						t.Errorf("%s imports %s", file, path)
					}
				}
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.Ident:
					if workloadExchangeNames[value.Name] {
						t.Errorf("%s references %s", fileSet.Position(value.Pos()), value.Name)
					}
				case *ast.BasicLit:
					if value.Kind == token.STRING {
						literal, _ := strconv.Unquote(value.Value)
						if workloadExchangeNames[literal] {
							t.Errorf("%s names %s", fileSet.Position(value.Pos()), literal)
						}
					}
				}
				return true
			})
		}
	}
}

func TestCommandAndSidecarPackagesReadOnlyACREnvironmentNamesAndNamedPlatformFacts(t *testing.T) {
	sawCredentialName := false
	for _, directory := range []string{".", filepath.Join("..", "..", "internal", "sidecar")} {
		for _, file := range productionFiles(t, directory) {
			fileSet := token.NewFileSet()
			parsed := parseSourceFile(t, fileSet, file)
			ast.Inspect(parsed, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil || !environmentName.MatchString(value) {
					return true
				}
				sawCredentialName = sawCredentialName || value == "ACR_API_TOKEN"
				if strings.HasPrefix(value, "ACR_") || operatingSystemEnvironment[value] {
					return true
				}
				t.Errorf("%s: environment-shaped name %q is neither ACR_-prefixed nor a named platform fact", fileSet.Position(literal.Pos()), value)
				return true
			})
		}
	}
	if !sawCredentialName {
		t.Fatal("the scan never saw ACR_API_TOKEN; it is not reading the credential loader")
	}
}
