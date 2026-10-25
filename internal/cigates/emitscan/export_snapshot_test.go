//go:build unix

package emitscan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// exportModule is a module whose scanned sample package logs dep.Value under the key "pass", which the scrubber exempts for ints only.
func exportModule(t *testing.T) (root, depFile string) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/probe\n\ngo 1.21\n")
	depFile = filepath.Join(root, "internal", "dep", "dep.go")
	writeFile(t, depFile, "package dep\n\nvar Value string\n")
	writeFile(t, filepath.Join(root, "internal", "sample", "sample.go"),
		"package sample\n\nimport (\n\t\"log/slog\"\n\n\t\"example.com/probe/internal/dep\"\n)\n\nfunc f(l *slog.Logger, v string) { l.Info(\"m\", \"pass\", dep.Value); _ = v }\n")
	return root, depFile
}

// goWrapper plants a go on PATH that runs the real go, except that around `go list` it runs the shell snippet around.
func goWrapper(t *testing.T, around string) {
	t.Helper()
	real, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = list ]; then\n" + around + "\nelse exec '" + real + "' \"$@\"; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	emitscan.ResetGoTool()
	t.Cleanup(emitscan.ResetGoTool)
}

func scanExportModule(t *testing.T, root string) string {
	t.Helper()
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
	findings, err := emitscan.Scan(root, []string{"internal"})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range findings {
		out = append(out, f.Rule+":"+f.Detail)
	}
	return strings.Join(out, ",")
}

// A scanned file that changes on disk only while `go list -export` runs must not change the result: the export data is
// built from the bytes the scan parsed.
func TestExportDataIsBuiltFromTheParsedBytesNotTheDisk(t *testing.T) {
	root, depFile := exportModule(t)
	control := scanExportModule(t, root)
	if !strings.Contains(control, "mismatch:pass") {
		t.Fatalf("control scan %q does not carry the mismatch finding", control)
	}
	emitscan.ResetGoTool()
	emitscan.SetHelperBinary("")()
	ran := filepath.Join(t.TempDir(), "go-list-ran")
	real, _ := exec.LookPath("go")
	goWrapper(t, "touch '"+ran+"'\ncp '"+depFile+"' '"+depFile+".orig'\nprintf 'package dep\\n\\nvar Value int\\n' > '"+depFile+"'\n'"+real+"' \"$@\"; rc=$?\nmv '"+depFile+".orig' '"+depFile+"'\nexit $rc")
	got := scanExportModule(t, root)
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("go list never ran in the second scan (an export cache served it): %v", err)
	}
	if got != control {
		t.Errorf("scan with dep.go changed only during go list = %q, want %q", got, control)
	}
	if data, _ := os.ReadFile(depFile); !strings.Contains(string(data), "string") {
		t.Errorf("the wrapper did not restore dep.go: %q", data)
	}
}

// Two scans of one module directory with different parsed bytes must not share an export set.
func TestExportCacheIsKeyedByTheParsedBytes(t *testing.T) {
	root, depFile := exportModule(t)
	first := scanExportModule(t, root)
	writeFile(t, depFile, "package dep\n\nvar Value int\n")
	second := scanExportModule(t, root)
	if first == second || strings.Contains(second, "mismatch:pass") {
		t.Errorf("second scan reused the first export set: first %q second %q", first, second)
	}
}

// The overlay directory is removed on the error path too.
func TestOverlayDirectoryIsRemovedAfterAGoListFailure(t *testing.T) {
	root, _ := exportModule(t)
	logFile := filepath.Join(t.TempDir(), "overlay-path")
	goWrapper(t, "while [ $# -gt 0 ]; do if [ \"$1\" = -overlay ]; then echo \"$2\" > '"+logFile+"'; fi; shift; done\nexit 1")
	err := emitscan.ExportFilesOf(root, root, []string{"internal/dep/dep.go"}, [][]byte{[]byte("package dep\n")})
	if err == nil {
		t.Fatal("a failing go list was not an error")
	}
	data, rerr := os.ReadFile(logFile)
	if rerr != nil {
		t.Fatalf("go list got no -overlay argument: %v", rerr)
	}
	overlay := strings.TrimSpace(string(data))
	if _, serr := os.Stat(filepath.Dir(overlay)); !os.IsNotExist(serr) {
		t.Errorf("overlay directory %s still exists after the failure (%v)", filepath.Dir(overlay), serr)
	}
}

// The snapshot key does not depend on the order the scan happened to hold its package directories in.
func TestSnapshotKeyDoesNotDependOnDirectoryOrder(t *testing.T) {
	var rels []string
	var srcs [][]byte
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		rels = append(rels, "internal/"+name+"/"+name+".go")
		srcs = append(srcs, []byte("package "+name+"\n"))
	}
	want := emitscan.SnapshotKey(rels, srcs)
	for i := 0; i < 50; i++ {
		if got := emitscan.SnapshotKey(rels, srcs); got != want {
			t.Fatalf("snapshot key changed between identical inputs: %s %s", got, want)
		}
	}
}
