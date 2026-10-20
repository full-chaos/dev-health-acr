package statementthreads

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

var peakColumn = regexp.MustCompile(`(^|[^_A-Za-z])memory_usage\b`)

// diagnosticPeakReads are query_log reads of memory_usage that only list or
// log statements and assert no bound, by file and enclosing function.
var diagnosticPeakReads = map[string]string{
	"internal/contextfabric/devhealthfacts/statement_memory_guard_integration_test.go:recentServerStatements":             "lists the last statements for a failure message",
	"internal/contextfabric/devhealthfacts/team_project_reach_org_links_memory_integration_test.go:failedStatementsSince": "lists failed statements for a failure message",
}

// peakReader is one function (declaration or literal) whose own string
// literals read memory_usage from system.query_log.
type peakReader struct {
	key     string
	checked bool
}

// peakReaders finds, in one Go source file, every function whose own string
// literals (not those of a nested function literal) read memory_usage from
// system.query_log, and whether that same function checks the statement
// threads: it calls RequireThreadBound. A literal that names query_log's
// max_threads setting is not a check: it may only display the value.
func peakReaders(rel string, src []byte) ([]peakReader, error) {
	file, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []peakReader
	var visit func(name string, body *ast.BlockStmt)
	visit = func(name string, body *ast.BlockStmt) {
		if body == nil {
			return
		}
		var queryLog, peak, calls bool
		ast.Inspect(body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncLit:
				visit(name+"/func", node.Body)
				return false
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(node.Value)
				if err != nil {
					return true
				}
				queryLog = queryLog || strings.Contains(text, "system.query_log")
				peak = peak || peakColumn.MatchString(text)
			case *ast.CallExpr:
				switch fn := node.Fun.(type) {
				case *ast.SelectorExpr:
					calls = calls || fn.Sel.Name == "RequireThreadBound"
				case *ast.Ident:
					calls = calls || fn.Name == "RequireThreadBound"
				}
			}
			return true
		})
		if queryLog && peak {
			out = append(out, peakReader{key: rel + ":" + name, checked: calls})
		}
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			visit(fn.Name.Name, fn.Body)
		}
	}
	return out, nil
}

func TestEveryMemoryPeakReaderChecksTheStatementThreads(t *testing.T) {
	root := repopath.Root(t)
	var unbound []string
	readers := 0
	seenDiagnostic := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "third_party", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "internal/testsupport/statementthreads/") {
			return nil
		}
		src, err := readFile(path)
		if err != nil {
			return err
		}
		found, err := peakReaders(rel, src)
		if err != nil {
			return err
		}
		for _, r := range found {
			readers++
			if _, ok := diagnosticPeakReads[r.key]; ok {
				seenDiagnostic[r.key] = true
				continue
			}
			if !r.checked {
				unbound = append(unbound, r.key)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if readers == 0 {
		t.Fatal("no test function reads memory_usage from system.query_log: the walk measured nothing")
	}
	for key := range diagnosticPeakReads {
		if !seenDiagnostic[key] {
			t.Errorf("allowlisted diagnostic read %s no longer exists; remove it from diagnosticPeakReads", key)
		}
	}
	sort.Strings(unbound)
	if len(unbound) > 0 {
		t.Fatalf("%d of %d query_log memory readers do not check the thread count their statements ran at; ClickHouse's default max_threads follows the server's cores, so the peak measures the runner. Measure through statementthreads.API and call statementthreads.RequireThreadBound in the same function:\n%s", len(unbound), readers, strings.Join(unbound, "\n"))
	}
	t.Logf("%d query_log memory readers, every one checks the statement threads or is an allowlisted diagnostic", readers)
}

func TestPeakReaderGuardBindsTheCheckToTheReadingFunction(t *testing.T) {
	for _, c := range []struct {
		name string
		src  string
		want []peakReader
	}{
		{"unchecked reader", `package p
func read() { q := "SELECT max(memory_usage) FROM system.query_log" ; _ = q }`, []peakReader{{"f.go:read", false}}},
		{"check named only in a comment", `package p
// statementthreads.RequireThreadBound(t, ctx, direct, label, n)
func read() { q := "SELECT max(memory_usage) FROM system.query_log" ; _ = q }`, []peakReader{{"f.go:read", false}}},
		{"check in another function", `package p
func read() { q := "SELECT max(memory_usage) FROM system.query_log" ; _ = q }
func other() { statementthreads.RequireThreadBound(nil, nil, nil, "", 0) }`, []peakReader{{"f.go:read", false}}},
		{"check in the outer function, read in a closure", `package p
func read() { f := func() { _ = "SELECT max(memory_usage) FROM system.query_log" }; f(); statementthreads.RequireThreadBound(nil, nil, nil, "", 0) }`, []peakReader{{"f.go:read/func", false}}},
		{"checked reader", `package p
func read() { q := "SELECT max(memory_usage), " + x + " FROM system.query_log"; _ = q; statementthreads.RequireThreadBound(nil, nil, nil, "", 0) }`, []peakReader{{"f.go:read", true}}},
		{"reader that only displays the setting", `package p
func read() { _ = "SELECT max(memory_usage), any(Settings['max_threads']) FROM system.query_log" }`, []peakReader{{"f.go:read", false}}},
		{"reader that filters on the setting without the check", `package p
func read() { _ = "SELECT max(memory_usage), countIf(Settings['max_threads'] != '1') FROM system.query_log" }`, []peakReader{{"f.go:read", false}}},
		{"max_memory_usage setting only", `package p
func read() { _ = "SELECT Settings['max_memory_usage'] FROM system.query_log" }`, nil},
	} {
		got, err := peakReaders("f.go", []byte(c.src))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %+v, want %+v", c.name, got[i], c.want[i])
			}
		}
	}
}

func TestParseThreadsReadsSentAndDefaultValues(t *testing.T) {
	for value, want := range map[string]uint64{"4": 4, "auto(4)": 4, "auto(64)": 64, "1": 1} {
		got, err := ParseThreads(value)
		if err != nil || got != want {
			t.Errorf("ParseThreads(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
	for _, value := range []string{"", "auto()", "auto(x)", "four"} {
		if _, err := ParseThreads(value); err == nil {
			t.Errorf("ParseThreads(%q) accepted a value that is not a thread count", value)
		}
	}
}

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }
