package devhealthfacts_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

var (
	sharedHelperCall = regexp.MustCompile(`\bnewCHAOS3780IntegrationClient(?:WithOptions)?\(`)
	// statements that change or read the server itself; SQL keywords are matched
	// case-sensitively so ordinary prose does not trip them
	serverStatement = regexp.MustCompile(`\bKILL (?:QUERY|MUTATION)\b|\bSYSTEM (?:FLUSH|DROP|STOP|START|RELOAD|SYNC|RESTART)|"\s*SET (?:GLOBAL|[a-z_]+ *=)|\bALTER USER\b|\bCREATE (?:USER|ROLE|QUOTA|SETTINGS PROFILE)\b|\bGRANT [A-Z, ]+ ON\b`)
	// server accounting: system tables and the memory and rows-read figures read from them
	serverAccounting = regexp.MustCompile(`(?i)system\.[a-z_]+|memory_usage|peak_memory|read_rows|read_bytes|query_log|MaxMemory|log_comment|statementMemorySince\(`)
)

// callsSharedHelper reports whether source calls the shared helper: a
// definition or a comment line is not a call.
func callsSharedHelper(source string) bool {
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if sharedHelperCall.MatchString(line) {
			return true
		}
	}
	return false
}

// sharedHelperProblems is why a test file may not use the shared per-test-database
// helper: it measures or changes the server, which other tests on the same
// container would disturb or be disturbed by.
func sharedHelperProblems(name, source string) []string {
	if !callsSharedHelper(source) {
		return nil
	}
	var problems []string
	if m := serverStatement.FindString(source); m != "" {
		problems = append(problems, name+" uses the shared ClickHouse helper but runs "+strings.TrimSpace(m)+": use newDedicatedClickHouseIntegrationClient")
	}
	if m := serverAccounting.FindString(source); m != "" {
		problems = append(problems, name+" uses the shared ClickHouse helper but reads server accounting ("+m+"): use newDedicatedClickHouseIntegrationClient")
	}
	return problems
}

func TestSharedClickHouseHelperIsNotUsedByTestsThatMeasureOrChangeTheServer(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files found (err %v): the guard measured nothing", err)
	}
	users := 0
	for _, file := range files {
		if file == "shared_clickhouse_guard_test.go" || file == "per_test_database_integration_test.go" {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if callsSharedHelper(string(raw)) {
			users++
		}
		for _, problem := range sharedHelperProblems(file, string(raw)) {
			t.Error(problem)
		}
	}
	if users == 0 {
		t.Fatal("no file uses the shared helper: the guard measured nothing")
	}
}

func TestSharedClickHouseGuardCatchesPlantedServerUses(t *testing.T) {
	shared := "\tquery, direct := newCHAOS3780IntegrationClient(t, ctx)\n"
	cases := map[string]string{
		"system table":      shared + `rows := direct.Query(ctx, "SELECT 1 FROM system.query_log")`,
		"system parts":      shared + `x := "system.parts"`,
		"system statement":  shared + `direct.Exec(ctx, "SYSTEM FLUSH LOGS")`,
		"stop merges":       shared + `direct.Exec(ctx, "SYSTEM STOP MERGES t")`,
		"kill":              shared + `direct.Exec(ctx, "KILL QUERY WHERE 1")`,
		"set global":        shared + `direct.Exec(ctx, "SET GLOBAL x = 1")`,
		"memory accounting": shared + "peak := memory_usage",
		"rows read":         shared + "n := read_rows",
		"memory helper":     shared + "got := statementMemorySince(t, ctx, direct, filter, mark)",
		"with options":      "\tq, d := newCHAOS3780IntegrationClientWithOptions(t, ctx, tune)\n\tx := \"system.parts\"",
	}
	for name, source := range cases {
		if len(sharedHelperProblems(name+".go", source)) == 0 {
			t.Errorf("%s: the planted server use passed the guard", name)
		}
	}
	for name, source := range map[string]string{
		"plain shared use":                     shared + `direct.Exec(ctx, "INSERT INTO t VALUES (1)")`,
		"dedicated helper with a system table": "\tq, d := newDedicatedClickHouseIntegrationClient(t, ctx)\n\tx := \"system.parts\"",
		"a definition, not a call":             "func newCHAOS3780IntegrationClient(t *testing.T) {}\nx := \"system.parts\"",
		"a comment":                            "// newCHAOS3780IntegrationClient( is mentioned\nx := \"system.parts\"",
	} {
		if problems := sharedHelperProblems(name+".go", source); len(problems) != 0 {
			t.Errorf("%s: unexpected problems %v", name, problems)
		}
	}
}

// Two tests' databases do not see each other's rows, and every database a test
// made is dropped when the test ends.
func TestPerTestDatabasesAreIsolatedAndDropped(t *testing.T) {
	ctx := context.Background()
	madeBefore, droppedBefore := perTestDatabasesMade.Load(), perTestDatabasesDropped.Load()
	var databases [2]string
	t.Run("two databases", func(t *testing.T) {
		firstQuery, first := newCHAOS3780IntegrationClient(t, ctx)
		secondQuery, second := newCHAOS3780IntegrationClient(t, ctx)
		if err := first.Exec(ctx, "CREATE TABLE rows_a (x UInt8) ENGINE = MergeTree ORDER BY x"); err != nil {
			t.Fatalf("create in the first database: %v", err)
		}
		if err := first.Exec(ctx, "INSERT INTO rows_a VALUES (1)"); err != nil {
			t.Fatalf("insert in the first database: %v", err)
		}
		if err := second.Exec(ctx, "INSERT INTO rows_a VALUES (2)"); err == nil {
			t.Fatal("the second database accepted a write to a table that only the first database created")
		}
		for i, conn := range []clickhousedriver.Conn{first, second} {
			if err := conn.QueryRow(ctx, "SELECT currentDatabase()").Scan(&databases[i]); err != nil {
				t.Fatalf("currentDatabase of connection %d: %v", i, err)
			}
		}
		// The production query clients are pointed at the same databases.
		for i, client := range []*runtimeclickhouse.Client{firstQuery, secondQuery} {
			rows, err := client.Query(ctx, "SELECT currentDatabase()", nil)
			if err != nil {
				t.Fatalf("query client %d: %v", i, err)
			}
			var viaQuery string
			for rows.Next() {
				if err := rows.Scan(&viaQuery); err != nil {
					t.Fatalf("scan query client %d: %v", i, err)
				}
			}
			_ = rows.Close()
			if viaQuery != databases[i] {
				t.Fatalf("query client %d reads database %q, want %q", i, viaQuery, databases[i])
			}
		}
		if databases[0] == databases[1] || databases[0] == "default" {
			t.Fatalf("per-test databases %v must differ and not be the default database", databases)
		}
	})
	if made := perTestDatabasesMade.Load() - madeBefore; made != 2 {
		t.Fatalf("databases created = %d, want 2", made)
	}
	if dropped := perTestDatabasesDropped.Load() - droppedBefore; dropped != 2 {
		t.Fatalf("databases dropped when the test ended = %d, want 2", dropped)
	}
	_, admin := sharedClickHouseFixture(t)
	for _, name := range databases {
		var exists uint8
		if err := admin.QueryRow(ctx, "EXISTS DATABASE "+name).Scan(&exists); err != nil {
			t.Fatalf("EXISTS DATABASE %s: %v", name, err)
		}
		if exists != 0 {
			t.Fatalf("database %s still exists after its test ended", name)
		}
	}
}

// The shared path keeps the tune hook of newCHAOS3780IntegrationClientWithOptions
// and points the query client at the per-test database (no server needed).
func TestPerTestQueryOptionsApplyTheTuneHookAndNameTheDatabase(t *testing.T) {
	plain := perTestQueryOptions("127.0.0.1:9000", "p7", nil)
	if plain.DSN != "clickhouse://acr:acr@127.0.0.1:9000/p7" {
		t.Fatalf("DSN = %q, want it to name the per-test database", plain.DSN)
	}
	tuned := perTestQueryOptions("127.0.0.1:9000", "p7", func(o *runtimeclickhouse.Options) { rows := uint(7); o.MaxResultRows = &rows })
	if tuned.MaxResultRows == nil || *tuned.MaxResultRows != 7 {
		t.Fatalf("MaxResultRows = %v, want the tune hook applied", tuned.MaxResultRows)
	}
	if tuned.DSN != plain.DSN {
		t.Fatalf("tuning changed the DSN: %q", tuned.DSN)
	}
	hooked := perTestQueryOptions("127.0.0.1:9000", "p7", func(o *runtimeclickhouse.Options) { o.DSN = "clickhouse://acr:acr@other/x" })
	if hooked.DSN != "clickhouse://acr:acr@other/x" {
		t.Fatalf("DSN = %q, want the hook applied after the default (it runs last)", hooked.DSN)
	}
}

// forbiddenFunctions is every package-level function of the given test sources
// that touches the server itself, found from its source rather than from a list:
// a function whose body matches the server-statement or server-accounting
// patterns, closed transitively over the calls between package-level functions.
func forbiddenFunctions(t *testing.T, sources map[string]string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	bodies := map[string]string{}
	calls := map[string]map[string]struct{}{}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, sources[name], 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			start, end := fset.Position(fn.Body.Pos()).Offset, fset.Position(fn.Body.End()).Offset
			bodies[fn.Name.Name] = sources[name][start:end]
			called := map[string]struct{}{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok {
						called[id.Name] = struct{}{}
					}
				}
				return true
			})
			calls[fn.Name.Name] = called
		}
	}
	forbidden := map[string]string{}
	for fn, body := range bodies {
		if m := serverStatement.FindString(body); m != "" {
			forbidden[fn] = strings.TrimSpace(m)
		} else if m := serverAccounting.FindString(body); m != "" {
			forbidden[fn] = m
		}
	}
	for changed := true; changed; {
		changed = false
		for fn, called := range calls {
			if _, done := forbidden[fn]; done {
				continue
			}
			for callee := range called {
				if why, bad := forbidden[callee]; bad {
					forbidden[fn] = callee + " -> " + why
					changed = true
					break
				}
			}
		}
	}
	return forbidden
}

// indirectSharedHelperProblems fails a file that calls the shared helper and any
// function that touches the server, directly or through other functions.
func indirectSharedHelperProblems(t *testing.T, sources map[string]string) []string {
	t.Helper()
	forbidden := forbiddenFunctions(t, sources)
	var problems []string
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	for _, name := range names {
		if !callsSharedHelper(sources[name]) {
			continue
		}
		file, err := parser.ParseFile(fset, name, sources[name], 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		seen := map[string]struct{}{}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if why, bad := forbidden[id.Name]; bad {
				if _, dup := seen[id.Name]; !dup {
					seen[id.Name] = struct{}{}
					problems = append(problems, name+" uses the shared ClickHouse helper but calls "+id.Name+", which touches the server ("+why+"): use newDedicatedClickHouseIntegrationClient")
				}
			}
			return true
		})
	}
	return problems
}

func TestSharedClickHouseHelperIsNotUsedWithFunctionsThatTouchTheServer(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files found (err %v): the guard measured nothing", err)
	}
	sources := map[string]string{}
	for _, file := range files {
		if file == "shared_clickhouse_guard_test.go" {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sources[file] = string(raw)
	}
	if forbidden := forbiddenFunctions(t, sources); len(forbidden) == 0 {
		t.Fatal("no function touches the server: the guard measured nothing")
	}
	for _, problem := range indirectSharedHelperProblems(t, sources) {
		t.Error(problem)
	}
}

func TestSharedClickHouseGuardCatchesPlantedIndirectServerUses(t *testing.T) {
	const helper = "package p\n\nfunc readQueryLog() {\n\tx := \"SELECT 1 FROM system.query_log\"\n\t_ = x\n}\n"
	const viaHelper = "package p\n\nfunc measure() {\n\treadQueryLog()\n}\n"
	shared := func(call string) string {
		return "package p\n\nfunc TestX() {\n\tquery, direct := newCHAOS3780IntegrationClient(t, ctx)\n\t" + call + "\n}\n"
	}
	cases := map[string]map[string]string{
		"a helper in another file reads system.query_log":    {"helper_test.go": helper, "shared_test.go": shared("readQueryLog()")},
		"transitive: shared calls a helper that calls it":    {"helper_test.go": helper, "mid_test.go": viaHelper, "shared_test.go": shared("measure()")},
		"a helper that runs a SYSTEM statement":              {"helper_test.go": "package p\n\nfunc flush() {\n\texec(\"SYSTEM FLUSH LOGS\")\n}\n", "shared_test.go": shared("flush()")},
		"a helper that reads memory accounting by its field": {"helper_test.go": "package p\n\nfunc peak() int {\n\treturn memory_usage\n}\n", "shared_test.go": shared("peak()")},
	}
	for name, sources := range cases {
		if len(indirectSharedHelperProblems(t, sources)) == 0 {
			t.Errorf("%s: the planted indirect server use passed the guard", name)
		}
	}
	for name, sources := range map[string]map[string]string{
		"a clean helper":                        {"helper_test.go": "package p\n\nfunc seed() {\n\texec(\"INSERT INTO t VALUES (1)\")\n}\n", "shared_test.go": shared("seed()")},
		"the server helper used by a dedicated": {"helper_test.go": helper, "dedicated_test.go": "package p\n\nfunc TestY() {\n\tq, d := newDedicatedClickHouseIntegrationClient(t, ctx)\n\treadQueryLog()\n}\n"},
	} {
		if problems := indirectSharedHelperProblems(t, sources); len(problems) != 0 {
			t.Errorf("%s: unexpected problems %v", name, problems)
		}
	}
}
