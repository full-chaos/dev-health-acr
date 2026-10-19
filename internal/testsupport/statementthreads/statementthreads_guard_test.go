package statementthreads

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

var peakColumn = regexp.MustCompile(`(^|[^_A-Za-z])memory_usage\b`)

// memoryPeakReader reports whether a test file reads a statement's peak
// memory from the server's query log.
func memoryPeakReader(source string) bool {
	return strings.Contains(source, "system.query_log") && peakColumn.MatchString(source)
}

// threadBound reports whether a peak-reading file checks the thread count its
// measured statements ran at: through this package, through the shared
// devhealthfacts reader that does (statementMemorySince), or by reading
// query_log's max_threads setting itself.
func threadBound(source string) bool {
	return strings.Contains(source, "statementthreads.RequireThreadBound(") ||
		strings.Contains(source, "statementMemorySince(") ||
		strings.Contains(source, "Settings['max_threads']")
}

func TestEveryMemoryPeakReaderChecksTheStatementThreads(t *testing.T) {
	root := repopath.Root(t)
	var unbound []string
	readers := 0
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
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(body)
		if !memoryPeakReader(source) {
			return nil
		}
		readers++
		if !threadBound(source) {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			unbound = append(unbound, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if readers == 0 {
		t.Fatal("no test file reads memory_usage from system.query_log: the walk measured nothing")
	}
	sort.Strings(unbound)
	if len(unbound) > 0 {
		t.Fatalf("%d of %d test files read a statement's peak memory from system.query_log without checking the thread count it ran at; ClickHouse's default max_threads follows the server's cores, so the peak measures the runner. Measure through statementthreads.API and call statementthreads.RequireThreadBound:\n%s", len(unbound), readers, strings.Join(unbound, "\n"))
	}
	t.Logf("%d memory peak readers, every one checks the statement threads", readers)
}

func TestMemoryPeakReaderGuardRejectsAnUnboundReader(t *testing.T) {
	reader := "rows := direct.Query(ctx, `SELECT max(memory_usage) FROM system.query_log`)"
	if !memoryPeakReader(reader) {
		t.Fatal("a max(memory_usage) read of system.query_log is not seen as a peak reader")
	}
	if threadBound(reader) {
		t.Fatal("a peak reader with no thread check passes the guard")
	}
	if memoryPeakReader("SELECT Settings['max_memory_usage'] FROM system.query_log") {
		t.Fatal("a read of the max_memory_usage setting is taken for a peak reader")
	}
	if !threadBound(reader + "\nstatementthreads.RequireThreadBound(t, label, n)") {
		t.Fatal("a peak reader that checks the threads fails the guard")
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
