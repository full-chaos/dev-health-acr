package devhealthfacts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// operationalLogEvents are the slog messages this package writes that describe
// what a reader did (reads, retries, fallbacks, bounds, budgets), not a
// decision-graph event of the engine: the scope ruling of the sweep (MCP,
// 2026-10-15) lists the devhealthfacts read, mix and tree filter lines as
// operational, to be listed in the closing inventory with their owning
// package. Each has a reason; a message that is neither here nor declared in eventspec fails the
// guard below.
var operationalLogEvents = map[string]string{
	"devhealthfacts.blocker_age_read_failed":               "read failure of one blocker age probe",
	"context_fabric_owning_teams_read_failed":              "read failure of the owning team lookup",
	"context_fabric_tree_work_item_filter":                 "debug trace of the tree work item filter",
	"context_fabric_tree_work_item_filter_truncated":       "bound report of the tree work item filter",
	"devhealthfacts.ownership_note_omitted":                "read failure of the ownership note",
	"devhealthfacts.rule_basis_unparsed":                   "unparsed rule basis report",
	"context_fabric: project pull request link tiers":      "debug trace of the project pull request link tier read",
	"devhealthfacts.read_failed":                           "read failure by action and error type",
	"investment membership scope prerequisite load failed": "read failure of the membership scope prerequisite",
	"devhealthfacts.status_since_inconsistent":             "data inconsistency report of a status since value",
	"devhealthfacts.membership_scope_fallback":             "membership scope fallback report",
	"investment membership scope cold load":                "membership scope cache load report",
	"devhealthfacts.project_mix_inputs_changed":            "project mix retry report",
	"devhealthfacts.project_mix_contended":                 "project mix contention report",
	"devhealthfacts.blocker_authorization":                 "authorization basis report of the blocker read (mode, authorized endpoint count, bound); it logs a query-plan choice (shape and count) and does not decide the answer (MCP 2026-10-16)",
	"devhealthfacts.landscape_retracted_rows":              "debug data-quality report of retraction rows",
	"devhealthfacts.read_budget_exceeded":                  "warn report of a query that hit the read budget (reader and ClickHouse code)",
	"devhealthfacts.read_stats":                            "read statistics report of one query",
}

// slogMethodMessageArg is the position of the message argument of every slog
// call shape: the package-level functions and the *slog.Logger methods share
// them.
var slogMethodMessageArg = map[string]int{
	"Info": 0, "Warn": 0, "Error": 0, "Debug": 0,
	"InfoContext": 1, "WarnContext": 1, "ErrorContext": 1, "DebugContext": 1,
	"Log": 2, "LogAttrs": 2,
}

// logMessagesWrittenByThePackage returns every message passed to a slog call
// in the package's non-test files, with the file that writes it: the
// package-level functions, the Log and LogAttrs forms, and any method of a
// logger value (the call shapes are the same on every receiver, so the scan
// does not look at the receiver). A message written as a package-level string
// constant is resolved. dynamic lists the call sites whose message is neither
// a literal nor such a constant.
func logMessagesWrittenByThePackage(t *testing.T) (messages map[string]string, dynamic []string) {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	var names []string
	if err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != "." && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, file)
		names = append(names, path)
		return nil
	}); err != nil {
		t.Fatalf("scan package: %v", err)
	}
	return collectLogMessages(fset, files, names)
}

// collectLogMessages is the analysis half of the scan, on parsed files.
func collectLogMessages(fset *token.FileSet, files []*ast.File, names []string) (messages map[string]string, dynamic []string) {
	// stringConstants maps a package-level constant to its value when the value
	// is a string literal, another such constant, or a concatenation of them.
	constantExprs := map[string]ast.Expr{}
	for index, file := range files {
		for _, spec := range file.Imports {
			if spec.Name != nil && spec.Name.Name == "." && spec.Path.Value == `"log/slog"` {
				dynamic = append(dynamic, names[index]+" (log/slog dot import: its calls cannot be followed)")
			}
		}
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				for i, name := range value.Names {
					constantExprs[name.Name] = value.Values[i]
				}
			}
		}
	}
	var evaluate func(expr ast.Expr, depth int) (string, bool)
	evaluate = func(expr ast.Expr, depth int) (string, bool) {
		if depth > 8 {
			return "", false
		}
		switch e := expr.(type) {
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return "", false
			}
			unquoted, err := strconv.Unquote(e.Value)
			return unquoted, err == nil
		case *ast.Ident:
			if inner, ok := constantExprs[e.Name]; ok {
				return evaluate(inner, depth+1)
			}
		case *ast.ParenExpr:
			return evaluate(e.X, depth+1)
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				return "", false
			}
			left, lok := evaluate(e.X, depth+1)
			right, rok := evaluate(e.Y, depth+1)
			return left + right, lok && rok
		}
		return "", false
	}
	messages = map[string]string{}
	for index, file := range files {
		// Selectors that are the function of a call; a slog-shaped selector that is
		// NOT (a method value, as in `emit := slog.Info`) cannot be followed, so
		// it is reported as dynamic.
		calledSelectors := map[*ast.SelectorExpr]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					calledSelectors[selector] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				if _, isSlogShape := slogMethodMessageArg[n.Sel.Name]; isSlogShape && !calledSelectors[n] {
					dynamic = append(dynamic, fset.Position(n.Pos()).String()+" (slog function used as a value)")
				}
			case *ast.CallExpr:
				selector, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				messageArg, isSlogShape := slogMethodMessageArg[selector.Sel.Name]
				if !isSlogShape || len(n.Args) <= messageArg {
					return true
				}
				if message, ok := evaluate(n.Args[messageArg], 0); ok {
					messages[message] = names[index]
					return true
				}
				dynamic = append(dynamic, fset.Position(n.Pos()).String())
			}
			return true
		})
	}
	return messages, dynamic
}

// TestEveryLogEventThePackageWritesIsDeclaredOrOperational is the drift guard:
// a new slog message in this package must be declared in eventspec or listed
// with a reason as operational, so the registry cannot fall behind the code.
func TestEveryLogEventThePackageWritesIsDeclaredOrOperational(t *testing.T) {
	messages, dynamic := logMessagesWrittenByThePackage(t)
	if len(dynamic) > 0 {
		t.Errorf("slog calls whose message is not a string literal cannot be registered: %v", dynamic)
	}
	declared := map[string]bool{}
	for _, event := range eventspec.All {
		declared[event.Msg] = true
	}
	var unregistered []string
	for message, file := range messages {
		if declared[message] {
			if _, both := operationalLogEvents[message]; both {
				t.Errorf("%q is both declared in eventspec and listed as operational (%s)", message, file)
			}
			continue
		}
		if _, ok := operationalLogEvents[message]; !ok {
			unregistered = append(unregistered, message+" ("+file+")")
		}
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Errorf("slog messages written by this package that are neither declared in eventspec nor listed as operational: %v", unregistered)
	}
	for message := range operationalLogEvents {
		if _, written := messages[message]; !written {
			t.Errorf("operational allowlist lists %q, which this package no longer writes", message)
		}
	}
}

func parseForGuard(t *testing.T, sources map[string]string) (map[string]string, []string) {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	var names []string
	for name, src := range sources {
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
		names = append(names, name)
	}
	return collectLogMessages(fset, files, names)
}

// TestTheLogEventGuardSeesEveryCallShape: the guard is itself tested, so a call
// shape it cannot follow is reported, never skipped.
func TestTheLogEventGuardSeesEveryCallShape(t *testing.T) {
	t.Parallel()
	messages, dynamic := parseForGuard(t, map[string]string{"a.go": `package p
import ("context"; "log/slog"; l "log/slog")
const base = "devhealthfacts."
const joined = base + "joined_event"
func f(ctx context.Context, logger *slog.Logger) {
	slog.Info("literal_event")
	slog.WarnContext(ctx, "context_event")
	slog.Log(ctx, slog.LevelInfo, "log_event")
	slog.LogAttrs(ctx, slog.LevelInfo, "attrs_event")
	logger.Info("logger_event")
	l.Debug("aliased_event")
	slog.Info(joined)
}`})
	for _, want := range []string{"literal_event", "context_event", "log_event", "attrs_event", "logger_event", "aliased_event", "devhealthfacts.joined_event"} {
		if _, ok := messages[want]; !ok {
			t.Errorf("the guard did not see %q: %v", want, messages)
		}
	}
	if len(dynamic) != 0 {
		t.Errorf("no call above is dynamic, got %v", dynamic)
	}
	_, dynamic = parseForGuard(t, map[string]string{"b.go": `package p
import "log/slog"
func g(name string) {
	emit := slog.Info
	emit("hidden_event")
	slog.Info(name)
}`})
	if len(dynamic) != 2 {
		t.Errorf("a slog function used as a value and a variable message must both be reported, got %v", dynamic)
	}
	_, dynamic = parseForGuard(t, map[string]string{"c.go": `package p
import . "log/slog"
func h() { Info("dot_imported_event") }`})
	if len(dynamic) != 1 || !strings.Contains(dynamic[0], "dot import") {
		t.Errorf("a dot import of log/slog must be reported, got %v", dynamic)
	}
}
