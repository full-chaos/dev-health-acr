//go:build unix

package emitscan_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// fakeGo plants a `go` first on PATH that answers `env GOVERSION` with version and records every call (arguments and the
// GOTOOLCHAIN it was given) in the returned log file.
func fakeGo(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$* GOTOOLCHAIN=$GOTOOLCHAIN\" >> '" + logFile + "'\nif [ \"$1 $2\" = \"env GOVERSION\" ]; then echo '" + version + "'; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	emitscan.ResetGoTool()
	t.Cleanup(emitscan.ResetGoTool)
	return logFile
}

func readCalls(t *testing.T, logFile string) string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// captureStderr redirects standard error to a file for the test and returns a reader of what was written.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = file
	t.Cleanup(func() { os.Stderr = saved })
	return func() string {
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

// A go command whose toolchain is not the one that built the scanner is refused by name, before any `go list` or
// `go build` runs with it, and the refusal is printed once however many steps ask.
func TestGoToolchainOtherThanTheScannersIsRefusedByName(t *testing.T) {
	calls := fakeGo(t, "go1.0.0")
	stderr := captureStderr(t)
	_, err := emitscan.GoBinary()
	if err == nil {
		t.Fatal("GoBinary() accepted a go command of another toolchain")
	}
	for _, want := range []string{"go1.0.0", runtime.Version(), "put the matching toolchain first on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
	if err := emitscan.ExportFiles(t.TempDir()); err == nil || !strings.Contains(err.Error(), "go1.0.0") {
		t.Fatalf("ExportFiles() error = %v, want the same refusal", err)
	}
	if got := readCalls(t, calls); strings.Contains(got, "list") || strings.Contains(got, "build") {
		t.Fatalf("the refused go command ran a step: %q", got)
	}
	if _, err := emitscan.GoBinary(); err == nil {
		t.Fatal("a second GoBinary() call accepted the go command")
	}
	if n := strings.Count(stderr(), "put the matching toolchain first on PATH"); n != 1 {
		t.Fatalf("the refusal was printed %d times, want once", n)
	}
}

// With the matching toolchain, `go list` runs through the resolved command and with GOTOOLCHAIN=local, whatever the
// caller's environment says.
func TestGoListRunsWithTheResolvedGoAndALocalToolchain(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "auto")
	calls := fakeGo(t, runtime.Version())
	resolved, err := emitscan.GoBinary()
	if err != nil {
		t.Fatalf("GoBinary() error = %v", err)
	}
	if filepath.Dir(resolved) != filepath.Dir(calls) {
		t.Fatalf("GoBinary() = %s, want the fake go at %s", resolved, filepath.Dir(calls))
	}
	// With the fake gone from PATH, only a go command run by its resolved path still reaches it.
	t.Setenv("PATH", t.TempDir())
	if err := emitscan.ExportFiles(t.TempDir()); err != nil {
		t.Fatalf("ExportFiles() error = %v", err)
	}
	got := readCalls(t, calls)
	listing := strings.Join(emitscan.ExportListArgs(), " ")
	if !strings.Contains(got, "list -export -deps") || !strings.Contains(got, listing+" -f {{if .Export}}{{.ImportPath}}\t{{.Export}}{{end}} ./... GOTOOLCHAIN=local") {
		t.Fatalf("go list calls = %q, want the export listing with GOTOOLCHAIN=local", got)
	}
	if !strings.Contains(got, "env GOVERSION GOTOOLCHAIN=local") {
		t.Fatalf("version query calls = %q, want GOTOOLCHAIN=local", got)
	}
}
