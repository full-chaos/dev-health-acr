// Package repopath resolves repository paths for tests from the working
// directory, which `go test` sets to the package directory. It does not use
// the caller's source path, which `-trimpath` rewrites to a module path.
package repopath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/full-chaos/dev-health-acr"

// Root returns the directory holding the acr go.mod, found by walking up
// from the working directory. It fails the test when none is found.
func Root(tb testing.TB) string {
	tb.Helper()
	wd, err := os.Getwd()
	if err != nil {
		tb.Fatalf("repopath: working directory: %v", err)
	}
	root, ok := findRoot(wd)
	if !ok {
		tb.Fatalf("repopath: no go.mod for module %s at or above %s", modulePath, wd)
	}
	return root
}

// Path joins elems onto Root.
func Path(tb testing.TB, elems ...string) string {
	tb.Helper()
	return filepath.Join(append([]string{Root(tb)}, elems...)...)
}

func findRoot(dir string) (string, bool) {
	for {
		if declaresModule(filepath.Join(dir, "go.mod")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func declaresModule(goMod string) bool {
	data, err := os.ReadFile(goMod)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1] == modulePath
		}
	}
	return false
}
