package emitscan_test

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

func TestExportListUsesRaceExactlyWhenTheScannerIsRaceBuilt(t *testing.T) {
	has := false
	for _, arg := range emitscan.ExportListArgs() {
		if arg == "-race" {
			has = true
		}
	}
	if has != builtWithRace {
		t.Errorf("go list args %v: -race present = %v, the test binary is race-built = %v", emitscan.ExportListArgs(), has, builtWithRace)
	}
}

// raceTaggedFiles lists, as import path + "/" + file, every non-test Go file of the dependency set of patterns (standard
// library excluded) that a build constraint names the race tag in, whichever side of the constraint it is on.
func raceTaggedFiles(t *testing.T, dir string, patterns ...string) []string {
	t.Helper()
	args := append([]string{"list", "-deps", "-f", `{{if not .Standard}}{{.ImportPath}}|{{.Dir}}{{range .GoFiles}}|{{.}}{{end}}{{range .IgnoredGoFiles}}|{{.}}{{end}}{{end}}`}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			continue
		}
		for _, name := range fields[2:] {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			if namesRaceTag(t, filepath.Join(fields[1], name)) {
				found = append(found, fields[0]+"/"+name)
			}
		}
	}
	sort.Strings(found)
	return found
}

func namesRaceTag(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if rest, ok := strings.CutPrefix(line, "//go:build "); ok {
			for _, word := range strings.FieldsFunc(rest, func(r rune) bool {
				return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
			}) {
				if word == "race" {
					return true
				}
			}
		}
	}
	return false
}

// The export data of a race-built scanner is built with -race, so a file whose content depends on the race tag changes
// what the scan type-checks against. This pins every such file of the module and its dependencies; a new one needs a
// review of whether it changes any declaration a scanned package can use (today only unexported or constant-only ones).
var pinnedRaceTaggedFiles = []string{
	"github.com/full-chaos/dev-health-acr/internal/contracts/v1/answer_bound_norace.go",
	"github.com/full-chaos/dev-health-acr/internal/contracts/v1/answer_bound_race.go",
	"golang.org/x/sys/unix/race.go",
	"golang.org/x/sys/unix/race0.go",
	"google.golang.org/protobuf/internal/impl/bitmap.go",
	"google.golang.org/protobuf/internal/impl/bitmap_race.go",
}

func TestRaceTaggedFilesOfTheDependencySetArePinned(t *testing.T) {
	got := raceTaggedFiles(t, repoRoot(t), "./...")
	if strings.Join(got, "\n") != strings.Join(pinnedRaceTaggedFiles, "\n") {
		t.Errorf("race-tagged non-test files of the dependency set changed: a new or removed file needs a review (does it change a declaration a scanned package uses?) before this pin moves.\n got: %q\nwant: %q", got, pinnedRaceTaggedFiles)
	}
}

func TestRaceTaggedFileListSeesAFixtureFileOnEitherSideOfTheConstraint(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/fx\n\ngo 1.22\n")
	write("a.go", "package fx\n")
	write("b_race.go", "//go:build race\n\npackage fx\n")
	write("c_norace.go", "//go:build !race && linux\n\npackage fx\n")
	write("d_other.go", "//go:build linux\n\npackage fx\n")
	want := "example.com/fx/b_race.go,example.com/fx/c_norace.go"
	if got := strings.Join(raceTaggedFiles(t, dir, "./..."), ","); got != want {
		t.Errorf("race-tagged fixture files = %q, want %q", got, want)
	}
}
