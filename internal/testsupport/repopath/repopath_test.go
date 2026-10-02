package repopath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootFromNestedDirectory(t *testing.T) {
	root := Root(t)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("root has no go.mod: %v", err)
	}
	t.Chdir(filepath.Join(root, "internal", "testsupport", "repopath"))
	if got := Root(t); got != root {
		t.Fatalf("Root from nested dir = %q, want %q", got, root)
	}
	if got, want := Path(t, "contracts", "openapi"), filepath.Join(root, "contracts", "openapi"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if _, err := os.Stat(Path(t, "contracts", "openapi", "acr-v1.json")); err != nil {
		t.Fatalf("known file not reachable: %v", err)
	}
}

func TestFindRootRejectsForeignModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if root, ok := findRoot(nested); ok {
		t.Fatalf("findRoot accepted foreign module at %q", root)
	}
}

func TestFindRootAcceptsModuleLineWithComment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+modulePath+" // project module\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if root, ok := findRoot(dir); !ok || root != dir {
		t.Fatalf("findRoot = %q,%v want %q,true", root, ok, dir)
	}
}

func TestFindRootAcceptsAcrModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "x")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if root, ok := findRoot(nested); !ok || root != dir {
		t.Fatalf("findRoot = %q,%v want %q,true", root, ok, dir)
	}
}

type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Fatalf(string, ...any) { r.failed = true; panic(r) }

func TestRootFailsLoudlyOutsideModule(t *testing.T) {
	t.Chdir(t.TempDir())
	r := &recorder{TB: t}
	func() {
		defer func() {
			if v := recover(); v != r {
				panic(v)
			}
		}()
		Root(r)
		t.Fatal("Root returned outside a module")
	}()
	if !r.failed {
		t.Fatal("Root did not fail the test")
	}
}
