package falkorgraph

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// sizeproofSeedSymbols is the one list of production-size seeding symbols. A
// Test function that is not tagged sizeproof must not reference any of them.
var sizeproofSeedSymbols = []string{
	"edgePageCostItems",
	"costLargestLinks",
	"costPastBoundLinks",
	"walkMemoryLinks",
	"walkMemorySeed",
	"seedScopedOrganization",
	"scopedFillers",
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

// sizeproofFileTagged evaluates the file's //go:build line: true only when the
// constraint holds with the tag sizeproof and fails without it.
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
		with := expr.Eval(func(tag string) bool { return tag == sizeproofTag })
		without := expr.Eval(func(string) bool { return false })
		return with && !without
	}
	return false
}

func sizeproofViolations(sources []sizeproofSource) []string {
	var out []string
	declared := map[string]bool{}
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
			ast.Inspect(fn, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				for _, sym := range sizeproofSeedSymbols {
					if id.Name == sym {
						out = append(out, s.name+": "+fn.Name.Name+" uses production-size seed symbol "+sym+" without `//go:build "+sizeproofTag+"`; tag the file (production-size seed symbols: "+strings.Join(sizeproofSeedSymbols, ", ")+", defined in sizeproofSeedSymbols)")
					}
				}
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
	write("a_test.go", "//go:build sizeproof\n\npackage falkorgraph\n\nfunc TestTheScopedCensusCostAtProductionSize() {}\nfunc TestEdgePageReadsOnlyTheOriginsEdges() {}\nfunc TestOneRepositoryWalkStaysSmallOnAWideLinkPopulation() {}\nfunc TestTheScopedCensusAndTheDeploymentWalkFollowTheLinkOnRealStores() {}\nfunc TestEmbedproviderEnvVarsListMatchesPackageSource() {}\n")
	if v := sizeproofViolations(readSizeproofSources(t, dir)); len(v) != 0 {
		t.Fatalf("a fully tagged fixture must pass, got %v", v)
	}
	write("b_test.go", "package falkorgraph\n\nfunc TestFifthProof() { _ = walkMemoryLinks }\n")
	v := sizeproofViolations(readSizeproofSources(t, dir))
	if len(v) != 1 || !strings.Contains(v[0], "walkMemoryLinks") || !strings.Contains(v[0], "sizeproofSeedSymbols") {
		t.Fatalf("an untagged test using a seed symbol must fail naming the list, got %v", v)
	}
	write("b_test.go", "//go:build !race\n\npackage falkorgraph\n\nfunc TestFifthProof() { _ = edgePageCostItems }\n")
	if v := sizeproofViolations(readSizeproofSources(t, dir)); len(v) != 1 {
		t.Fatalf("a tag other than sizeproof must not count, got %v", v)
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
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	scripts, _ := filepath.Glob(filepath.Join(root, "scripts", "ci", "*.sh"))
	for _, p := range append(files, scripts...) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, sizeproofTag) && !strings.HasSuffix(p, "sizeproof_guard_test.go") {
				t.Errorf("%s:%d names %s; only the Makefile plain unit targets pass it (a workflow or script line could reach a race build)", p, i+1, sizeproofTag)
			}
		}
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
