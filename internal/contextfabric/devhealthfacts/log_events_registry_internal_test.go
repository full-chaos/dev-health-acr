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
	"context_fabric_owning_teams_truncated":                "bound report of the owning team lookup",
	"context_fabric_tree_work_item_filter":                 "debug trace of the tree work item filter",
	"context_fabric_tree_work_item_filter_truncated":       "bound report of the tree work item filter",
	"devhealthfacts.ownership_note_omitted":                "read failure of the ownership note",
	"devhealthfacts.rule_basis_unparsed":                   "unparsed rule basis report",
	"devhealthfacts.read_failed":                           "read failure by action and error type",
	"context_fabric_fact_bytes_bound_exceeded":             "bound report of the fact byte budget",
	"context_fabric_subject_id_shape_rejected":             "rejected subject id shape report",
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
	constants := map[string]string{}
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
					if literal, ok := value.Values[i].(*ast.BasicLit); ok && literal.Kind == token.STRING {
						if unquoted, err := strconv.Unquote(literal.Value); err == nil {
							constants[name.Name] = unquoted
						}
					}
				}
			}
		}
	}
	messages = map[string]string{}
	for index, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			messageArg, isSlogShape := slogMethodMessageArg[selector.Sel.Name]
			if !isSlogShape || len(call.Args) <= messageArg {
				return true
			}
			switch argument := call.Args[messageArg].(type) {
			case *ast.BasicLit:
				if argument.Kind != token.STRING {
					return true
				}
				message, err := strconv.Unquote(argument.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", argument.Value, err)
				}
				messages[message] = names[index]
			case *ast.Ident:
				if message, ok := constants[argument.Name]; ok {
					messages[message] = names[index]
					return true
				}
				dynamic = append(dynamic, fset.Position(call.Pos()).String())
			default:
				dynamic = append(dynamic, fset.Position(call.Pos()).String())
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
