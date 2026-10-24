package falkorgraph

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// discoverEmbedproviderEnvVars returns the value of every exported Env*
// string constant of the embedprovider package, read from the type checker's
// own constant evaluator, so literals, concatenations, aliases, iota
// continuations and constants declared in a sibling file all resolve.
func discoverEmbedproviderEnvVars(t *testing.T) []string {
	t.Helper()
	names, err := discoverEnvConstValues("", nil, embedproviderImportPath)
	if err != nil {
		t.Fatalf("discover Env* constants of %s: %v", embedproviderImportPath, err)
	}
	return names
}

// discoverEnvConstValues selects the package's files with the build's own
// file selection (GOOS, GOARCH, build tags) and type-checks them without
// compiling or importing any dependency. packages.Load with NeedTypes ran
// `go list -export`, which compiled the whole dependency closure of the
// package under the race detector's load; that cost swung from 19 s to 169 s
// for a check of a handful of constants.
//
// An Env* constant whose value needs an imported package cannot be evaluated
// here; that is an error naming the constant, never a silent omission.
func discoverEnvConstValues(dir string, env []string, pattern string) ([]string, error) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles, Dir: dir, Env: env}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return nil, fmt.Errorf("packages.Load(%s): %w", pattern, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("packages.Load(%s) returned %d packages, want exactly 1", pattern, len(pkgs))
	}
	if len(pkgs[0].Errors) > 0 {
		return nil, fmt.Errorf("packages.Load(%s): %v", pattern, pkgs[0].Errors[0])
	}
	if len(pkgs[0].GoFiles) == 0 {
		return nil, fmt.Errorf("packages.Load(%s) selected no Go files", pattern)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range pkgs[0].GoFiles {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	conf := types.Config{
		IgnoreFuncBodies: true,
		FakeImportC:      true,
		Importer:         refusingImporter{},
		Error:            func(error) {},
	}
	pkg, _ := conf.Check(pkgs[0].PkgPath, fset, files, nil)
	if pkg == nil {
		return nil, errors.New("type check produced no package")
	}
	scope := pkg.Scope()
	var names []string
	for _, name := range scope.Names() {
		if !ast.IsExported(name) || !strings.HasPrefix(name, "Env") {
			continue
		}
		constObj, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		if constObj.Val().Kind() == constant.Unknown {
			return nil, fmt.Errorf("constant %s has no value without compiling its imports; declare it from literals or constants of this package", name)
		}
		if constObj.Val().Kind() != constant.String {
			continue
		}
		names = append(names, constant.StringVal(constObj.Val()))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("loaded %s but found zero exported Env*-prefixed string constants", pattern)
	}
	sort.Strings(names)
	return names, nil
}

type refusingImporter struct{}

func (refusingImporter) Import(path string) (*types.Package, error) {
	return nil, fmt.Errorf("import %q is not resolved by Env* discovery", path)
}

// linuxAMD64Env pins the platform the plant fixtures' expected files assume, so
// the table holds when the test binary runs on another GOOS.
func linuxAMD64Env() []string {
	return append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
}

func writePlantPackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module plant\n\ngo 1.22\n"
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiscoverEnvConstValuesReadsEveryDeclarationForm(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("go toolchain not found: %v", err)
	}
	dir := writePlantPackage(t, map[string]string{
		"a.go": `package plant

const prefix = "P_"

const (
	EnvLiteral = "LITERAL"
	EnvConcat  = prefix + "CONCAT"
)

type name string

const EnvAlias name = "ALIAS"

const (
	EnvIota0 = "IOTA" + string(rune('0'+iota))
	EnvIota1
)

const EnvNumber = 7

var EnvVariable = "VAR"

const envUnexported = "UNEXPORTED"

func f() { _ = undefinedInBody }
`,
		"b.go":         "package plant\n\nconst EnvSecondFile = prefix + \"SECOND\"\n",
		"c_linux.go":   "package plant\n\nconst EnvOnLinux = \"LINUX\"\n",
		"d_windows.go": "package plant\n\nconst EnvOnWindows = \"WINDOWS\"\n",
		"e.go":         "//go:build linux\n\npackage plant\n\nconst EnvTagged = \"TAGGED\"\n",
		"f.go":         "//go:build !linux\n\npackage plant\n\nconst EnvNotLinux = \"NOTLINUX\"\n",
	})
	got, err := discoverEnvConstValues(dir, linuxAMD64Env(), "plant")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ALIAS", "IOTA0", "IOTA1", "LINUX", "LITERAL", "P_CONCAT", "P_SECOND", "TAGGED"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("discovered %v, want %v", got, want)
	}
}

func TestDiscoverEnvConstValuesFailsLoudlyOnAnImportValuedConstant(t *testing.T) {
	dir := writePlantPackage(t, map[string]string{
		"a.go": "package plant\n\nimport \"os\"\n\nconst EnvOK = \"OK\"\n\nconst EnvFromImport = os.DevNull\n",
	})
	_, err := discoverEnvConstValues(dir, linuxAMD64Env(), "plant")
	if err == nil || !strings.Contains(err.Error(), "EnvFromImport") {
		t.Fatalf("err = %v, want an error naming EnvFromImport", err)
	}
}
