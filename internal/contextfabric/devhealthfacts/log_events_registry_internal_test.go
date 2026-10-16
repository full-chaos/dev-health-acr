package devhealthfacts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// operationalLogEvents are the slog messages this package writes that describe
// what a reader or the projector did, not a decision of the engine. Each has a
// reason; a message that is neither here nor declared in eventspec fails the
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
}

// logMessagesWrittenByThePackage returns every message literal passed to a
// package-level slog call in the package's non-test files, with the file that
// writes it, and the call sites whose message is not a literal.
func logMessagesWrittenByThePackage(t *testing.T) (messages map[string]string, dynamic []string) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	messages = map[string]string{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "slog" {
				return true
			}
			messageArg := 0
			switch selector.Sel.Name {
			case "Info", "Warn", "Error", "Debug":
			case "InfoContext", "WarnContext", "ErrorContext", "DebugContext":
				messageArg = 1
			default:
				return true
			}
			if len(call.Args) <= messageArg {
				return true
			}
			literal, ok := call.Args[messageArg].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				dynamic = append(dynamic, fset.Position(call.Pos()).String())
				return true
			}
			message, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", literal.Value, err)
			}
			messages[message] = name
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
