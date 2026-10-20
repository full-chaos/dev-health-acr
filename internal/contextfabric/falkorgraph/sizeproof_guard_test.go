package falkorgraph

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The production-size proofs seed a real store at production size and cost
// minutes under -race. They carry the build tag sizeproof; the Makefile passes
// the tag to the plain unit targets only. This is a static source census: no
// container, no timing.

const sizeproofTag = "sizeproof"

// sizeproofTests are the tests that must sit only in files tagged sizeproof.
var sizeproofTests = []string{
	"TestTheScopedCensusCostAtProductionSize",
	"TestEdgePageReadsOnlyTheOriginsEdges",
	"TestOneRepositoryWalkStaysSmallOnAWideLinkPopulation",
	"TestTheScopedCensusAndTheDeploymentWalkFollowTheLinkOnRealStores",
	"TestEmbedproviderEnvVarsListMatchesPackageSource",
}

type sizeproofSource struct {
	name   string
	tagged bool
	file   *ast.File
}

func readSizeproofSources(t *testing.T, dir string) []sizeproofSource {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no test files in %s (err %v)", dir, err)
	}
	fset := token.NewFileSet()
	var out []sizeproofSource
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, p, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, sizeproofSource{name: filepath.Base(p), tagged: sizeproofFileTagged(string(src)), file: f})
	}
	return out
}

// sizeproofFileTagged is true only when the file's //go:build line is exactly
// the single tag sizeproof. Any other expression (`sizeproof || race`,
// `sizeproof || linux`, `!race`) can admit the file to a race build.
func sizeproofFileTagged(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false
		}
		tag, ok := expr.(*constraint.TagExpr)
		return ok && tag.Tag == sizeproofTag
	}
	return false
}

// sizeproofTaggedSymbols returns every top-level name declared in a file
// tagged sizeproof: the production-size seeds, helpers and constants. It is
// derived from the source, so a new seed constant needs no list entry.
func sizeproofTaggedSymbols(sources []sizeproofSource) map[string]bool {
	names := map[string]bool{}
	for _, s := range sources {
		if !s.tagged {
			continue
		}
		for _, d := range s.file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch spec := spec.(type) {
					case *ast.ValueSpec:
						for _, n := range spec.Names {
							names[n.Name] = true
						}
					case *ast.TypeSpec:
						names[spec.Name.Name] = true
					}
				}
			}
		}
	}
	delete(names, "_")
	return names
}

func sizeproofViolations(sources []sizeproofSource) []string {
	var out []string
	declared := map[string]bool{}
	tagged := sizeproofTaggedSymbols(sources)
	for _, s := range sources {
		for _, d := range s.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			declared[fn.Name.Name] = true
			for _, want := range sizeproofTests {
				if fn.Name.Name == want && !s.tagged {
					out = append(out, s.name+": "+want+" must sit in a file with `//go:build "+sizeproofTag+"`")
				}
			}
			if s.tagged {
				continue
			}
			seen := map[string]bool{}
			ast.Inspect(fn, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || !tagged[id.Name] || seen[id.Name] {
					return true
				}
				seen[id.Name] = true
				out = append(out, s.name+": "+fn.Name.Name+" uses "+id.Name+", declared in a file tagged `//go:build "+sizeproofTag+"` (production-size seeds and helpers; derived by sizeproofTaggedSymbols, no list to maintain); tag this file too")
				return true
			})
		}
	}
	for _, want := range sizeproofTests {
		if !declared[want] {
			out = append(out, want+" is declared nowhere: renamed or deleted, update sizeproofTests")
		}
	}
	sort.Strings(out)
	return out
}

func TestSizeproofTestsCarryTheTag(t *testing.T) {
	if v := sizeproofViolations(readSizeproofSources(t, ".")); len(v) > 0 {
		t.Fatalf("size-proof tag census failed:\n%s", strings.Join(v, "\n"))
	}
}

func TestSizeproofGuardFlagsAnUntaggedProductionSizeTest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a_test.go", "//go:build sizeproof\n\npackage falkorgraph\n\nfunc TestTheScopedCensusCostAtProductionSize() {}\nfunc TestEdgePageReadsOnlyTheOriginsEdges() {}\nfunc TestOneRepositoryWalkStaysSmallOnAWideLinkPopulation() {}\nfunc TestTheScopedCensusAndTheDeploymentWalkFollowTheLinkOnRealStores() {}\nfunc TestEmbedproviderEnvVarsListMatchesPackageSource() {}\nconst walkMemoryLinks = 1\nconst edgePageCostItems = 2\nconst edgePageCostRepos = 3\nconst walkMemoryDimension = 4\n")
	if v := sizeproofViolations(readSizeproofSources(t, dir)); len(v) != 0 {
		t.Fatalf("a fully tagged fixture must pass, got %v", v)
	}
	write("b_test.go", "package falkorgraph\n\nfunc TestFifthProof() { _ = walkMemoryLinks }\n")
	v := sizeproofViolations(readSizeproofSources(t, dir))
	if len(v) != 1 || !strings.Contains(v[0], "walkMemoryLinks") || !strings.Contains(v[0], "sizeproofTaggedSymbols") {
		t.Fatalf("an untagged test using a seed symbol must fail naming the list, got %v", v)
	}
	for _, sym := range []string{"edgePageCostRepos", "walkMemoryDimension", "edgePageCostItems"} {
		write("b_test.go", "package falkorgraph\n\nfunc TestFifthProof() { _ = "+sym+" }\n")
		if v := sizeproofViolations(readSizeproofSources(t, dir)); len(v) != 1 || !strings.Contains(v[0], sym) {
			t.Fatalf("an untagged test using the tagged symbol %s must fail, got %v", sym, v)
		}
	}
	for _, header := range []string{"//go:build !race\n\n", "//go:build sizeproof || race\n\n", "//go:build sizeproof || linux\n\n", "//go:build sizeproof && !race\n\n", ""} {
		write("b_test.go", header+"package falkorgraph\n\nfunc TestFifthProof() { _ = edgePageCostItems }\n")
		if v := sizeproofViolations(readSizeproofSources(t, dir)); len(v) != 1 {
			t.Fatalf("header %q must not count as exactly sizeproof, got %v", header, v)
		}
	}
	if err := os.Remove(filepath.Join(dir, "b_test.go")); err != nil {
		t.Fatal(err)
	}
	write("a_test.go", "package falkorgraph\n\nfunc TestEdgePageReadsOnlyTheOriginsEdges() {}\n")
	if v := sizeproofViolations(readSizeproofSources(t, dir)); len(v) < 5 {
		t.Fatalf("an untagged named proof and the missing four must fail, got %v", v)
	}
}

func TestSizeproofTagReachesOnlyThePlainUnitRuns(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if problems := sizeproofMakefileProblems(string(mk)); len(problems) > 0 {
		t.Fatalf("Makefile: %s", strings.Join(problems, "; "))
	}
	for _, hit := range sizeproofWorkflowHits(t, root) {
		t.Error(hit)
	}
}

func sizeproofWorkflowHits(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	for _, pattern := range []string{
		filepath.Join(root, ".github", "workflows", "*.yml"),
		filepath.Join(root, ".github", "workflows", "*.yaml"),
		filepath.Join(root, "scripts", "ci", "*.sh"),
	} {
		found, _ := filepath.Glob(pattern)
		paths = append(paths, found...)
	}
	var hits []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, sizeproofTag) {
				hits = append(hits, p+":"+strconv.Itoa(i+1)+" names "+sizeproofTag+"; only the Makefile plain unit targets pass it (a workflow or script line could reach a race build)")
			}
		}
	}
	return hits
}

func TestSizeproofWorkflowScanReadsYamlAndYmlAndScripts(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{".github/workflows", "scripts/ci"} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if hits := sizeproofWorkflowHits(t, root); len(hits) != 0 {
		t.Fatalf("an empty tree must pass, got %v", hits)
	}
	for _, rel := range []string{".github/workflows/x.yaml", ".github/workflows/x.yml", "scripts/ci/x.sh"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("run: go test -race -tags sizeproof ./...\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if hits := sizeproofWorkflowHits(t, root); len(hits) != 3 {
		t.Fatalf("want one hit per .yaml, .yml and .sh file, got %v", hits)
	}
}

var sizeproofRecipeHead = regexp.MustCompile(`^([A-Za-z0-9_.-]+):`)

func sizeproofMakefileProblems(mk string) []string {
	var problems []string
	if !regexp.MustCompile(`(?m)^GOTEST_PLAIN_TAGS\s*:?=\s*-tags ` + sizeproofTag + `\s*$`).MatchString(mk) {
		problems = append(problems, "GOTEST_PLAIN_TAGS must be `-tags "+sizeproofTag+"`")
	}
	target := ""
	seen := map[string]bool{}
	for _, line := range strings.Split(mk, "\n") {
		if m := sizeproofRecipeHead.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, "\t") {
			target = m[1]
		}
		if !strings.Contains(line, "go test") && !strings.Contains(line, "gotestsum") && !strings.Contains(line, "$(GOTEST_EXEC)") {
			continue
		}
		withTag := strings.Contains(line, "$(GOTEST_PLAIN_TAGS)") || strings.Contains(line, "-tags "+sizeproofTag)
		race := strings.Contains(line, "-race")
		switch {
		case race && withTag:
			problems = append(problems, "target "+target+": a -race line carries the "+sizeproofTag+" tag: "+strings.TrimSpace(line))
		case (target == "test" || target == "test-coverage") && strings.Contains(line, "$(GOTEST_EXEC)") && !withTag:
			problems = append(problems, "target "+target+": the plain unit run must pass $(GOTEST_PLAIN_TAGS): "+strings.TrimSpace(line))
		}
		if strings.Contains(line, "$(GOTEST_EXEC)") && (target == "test" || target == "test-coverage") && withTag && !race {
			seen[target] = true
		}
	}
	for _, want := range []string{"test", "test-coverage"} {
		if !seen[want] {
			problems = append(problems, "target "+want+" has no plain go test line with the tag")
		}
	}
	return problems
}

func TestSizeproofMakefileCheckFlagsATaggedRaceLine(t *testing.T) {
	good := "GOTEST_PLAIN_TAGS := -tags sizeproof\ntest:\n\tgo test $(GOTEST_EXEC) $(GOTEST_PLAIN_TAGS) -count=1 $(P)\ntest-coverage:\n\tgotestsum -- \\\n\t\t$(GOTEST_EXEC) $(GOTEST_PLAIN_TAGS) -count=1 $(P)\ntest-race:\n\tgo test $(GOTEST_EXEC) -count=1 -race $(P)\n"
	if p := sizeproofMakefileProblems(good); len(p) != 0 {
		t.Fatalf("good Makefile flagged: %v", p)
	}
	for name, bad := range map[string]string{
		"tag on race":      strings.Replace(good, "-count=1 -race", "$(GOTEST_PLAIN_TAGS) -count=1 -race", 1),
		"tag missing":      strings.Replace(good, "go test $(GOTEST_EXEC) $(GOTEST_PLAIN_TAGS)", "go test $(GOTEST_EXEC)", 1),
		"coverage missing": strings.Replace(good, "\t\t$(GOTEST_EXEC) $(GOTEST_PLAIN_TAGS)", "\t\t$(GOTEST_EXEC)", 1),
		"var missing":      strings.Replace(good, "GOTEST_PLAIN_TAGS := -tags sizeproof\n", "", 1),
	} {
		if p := sizeproofMakefileProblems(bad); len(p) == 0 {
			t.Errorf("%s: not flagged", name)
		}
	}
}

// sizeproofSelected asks the Go loader which test files a build with the given
// tags compiles into the package test binary. This applies every constraint
// Go applies, file-name suffixes (_windows_test.go) included.
func sizeproofSelected(t *testing.T, dir, tags, goarch string) map[string]bool {
	t.Helper()
	args := []string{"list"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-f", "{{range .TestGoFiles}}{{.}}\n{{end}}", ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	selected := map[string]bool{}
	for _, f := range strings.Fields(string(out)) {
		selected[f] = true
	}
	return selected
}

// sizeproofSelectionProblems: every file declaring a named proof must be
// compiled with the tag sizeproof and left out without it.
func sizeproofSelectionProblems(t *testing.T, dir string, sources []sizeproofSource) []string {
	t.Helper()
	var out []string
	for _, goarch := range sizeproofArches {
		out = append(out, sizeproofSelectionProblemsFor(t, dir, sources, goarch)...)
	}
	sort.Strings(out)
	return out
}

// sizeproofArches are the linux architectures CI builds on (hosted amd64,
// bigboy arm64): a file-name suffix such as _arm64_test.go drops the file on
// one of them only.
var sizeproofArches = []string{"amd64", "arm64"}

func sizeproofSelectionProblemsFor(t *testing.T, dir string, sources []sizeproofSource, goarch string) []string {
	t.Helper()
	with := sizeproofSelected(t, dir, sizeproofTag, goarch)
	without := sizeproofSelected(t, dir, "", goarch)
	var out []string
	for _, s := range sources {
		for _, d := range s.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			for _, want := range sizeproofTests {
				if fn.Name.Name != want {
					continue
				}
				if !with[s.name] {
					out = append(out, s.name+": "+want+" is not compiled with -tags "+sizeproofTag+" on linux/"+goarch+" (go list TestGoFiles; a file-name suffix such as _windows_test.go or a constraint drops it)")
				}
				if without[s.name] {
					out = append(out, s.name+": "+want+" is compiled without -tags "+sizeproofTag+" on linux/"+goarch)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestSizeproofTestsAreSelectedByGoOnlyWithTheTag(t *testing.T) {
	if v := sizeproofSelectionProblems(t, ".", readSizeproofSources(t, ".")); len(v) > 0 {
		t.Fatalf("go test file selection:\n%s", strings.Join(v, "\n"))
	}
}

func TestSizeproofSelectionCheckFlagsAFileNameSuffixAndAMissingTag(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.22\n")
	write("x.go", "package fixture\n")
	body := func(header string) string {
		return header + "package fixture\n\nfunc TestEdgePageReadsOnlyTheOriginsEdges() {}\n"
	}
	cases := []struct {
		file, header string
		problems     int
	}{
		{"a_test.go", "//go:build sizeproof\n\n", 0},
		{"a_windows_test.go", "//go:build sizeproof\n\n", 2},
		{"a_arm64_test.go", "//go:build sizeproof\n\n", 1},
		{"a_amd64_test.go", "//go:build sizeproof\n\n", 1},
		{"a_test.go", "", 2},
	}
	for _, c := range cases {
		for _, f := range []string{"a_test.go", "a_windows_test.go", "a_arm64_test.go", "a_amd64_test.go"} {
			os.Remove(filepath.Join(dir, f))
		}
		write(c.file, body(c.header))
		got := sizeproofSelectionProblems(t, dir, readSizeproofSources(t, dir))
		if len(got) != c.problems {
			t.Errorf("%s with header %q: want %d problems, got %v", c.file, c.header, c.problems, got)
		}
	}
}
