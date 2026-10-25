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

// A go command whose toolchain is not the one that built the scanner is refused by name, before any `go list` or
// `go build` runs with it.
func TestGoToolchainOtherThanTheScannersIsRefusedByName(t *testing.T) {
	calls := fakeGo(t, "go1.0.0")
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
}

// With the matching toolchain, `go list` runs through the resolved command and with GOTOOLCHAIN=local, whatever the
// caller's environment says.
func TestGoListRunsWithTheResolvedGoAndALocalToolchain(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "auto")
	calls := fakeGo(t, runtime.Version())
	if err := emitscan.ExportFiles(t.TempDir()); err != nil {
		t.Fatalf("ExportFiles() error = %v", err)
	}
	got := readCalls(t, calls)
	if !strings.Contains(got, "list -export -deps") || !strings.Contains(got, "list -export -deps -f {{if .Export}}{{.ImportPath}}\t{{.Export}}{{end}} ./... GOTOOLCHAIN=local") {
		t.Fatalf("go list calls = %q, want the export listing with GOTOOLCHAIN=local", got)
	}
	if !strings.Contains(got, "env GOVERSION GOTOOLCHAIN=local") {
		t.Fatalf("version query calls = %q, want GOTOOLCHAIN=local", got)
	}
}
