package contextfabric_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// callsNamed returns every call of a function or method with the given name in
// the non-test sources of this package, with the call expression.
func callsNamed(t *testing.T, name string) []*ast.CallExpr {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []*ast.CallExpr
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel.Name == name {
						calls = append(calls, call)
					}
				case *ast.Ident:
					if fun.Name == name {
						calls = append(calls, call)
					}
				}
				return true
			})
		}
	}
	return calls
}

// largestGroup returns the largest number of calls that share the text of the
// argument at index (the scope key of an event that attributes on it).
func largestGroup(calls []*ast.CallExpr, index int) int {
	groups := map[string]int{}
	most := 0
	for _, call := range calls {
		key := ""
		if index < len(call.Args) {
			switch arg := call.Args[index].(type) {
			case *ast.BasicLit:
				key = arg.Value
			case *ast.SelectorExpr:
				key = arg.Sel.Name
			case *ast.Ident:
				key = arg.Name
			}
		}
		groups[key]++
		most = max(most, groups[key])
	}
	return most
}

// TestEventLineBoundsAreTheEngineCallSitesTimesThePasses recomputes the
// per-request bound of each contextfabric event whose count follows engine call
// sites from the package source: a new call site, or a loop over more
// subjects, fails here until the declared bound is reviewed.
func TestEventLineBoundsAreTheEngineCallSitesTimesThePasses(t *testing.T) {
	t.Parallel()
	const passes = 2
	cases := []struct {
		event eventspec.Event
		want  int
		why   string
	}{
		{eventspec.WindowCanonicalizationOutcome, len(callsNamed(t, "RecordWindowCanonicalization")) * passes, "RecordWindowCanonicalization call sites x passes"},
		{eventspec.PriorSubjectReceiptsSkipped, len(callsNamed(t, "RecordPriorSubjectReceiptsSkipped")) * passes, "RecordPriorSubjectReceiptsSkipped call sites x passes"},
		{eventspec.PriorSubjectReceiptSkipReason, largestGroup(callsNamed(t, "RecordPriorSubjectReceiptSkipReason"), 2) * passes, "call sites per reason x passes"},
		{eventspec.CommitAffirmationRetraction, len(callsNamed(t, "RecordCommitAffirmationRetraction")) * contractsv1.ContextFabricProjectedSubjectsMaxCount, "one call in a loop over the committed subjects"},
		{eventspec.CommitKeptByNoMemberFound, len(callsNamed(t, "RecordCommitKeptByNoMemberFound")) * contractsv1.ContextFabricProjectedSubjectsMaxCount, "one call in a loop over the committed subjects"},
		{eventspec.AnswerReuseOutcome, len(callsNamed(t, "recordReuseOutcome")) * passes, "recordReuseOutcome call sites x passes"},
		{eventspec.AnswerReuseContainment, len(callsNamed(t, "recordReuseContainment")) * passes, "recordReuseContainment call sites x passes"},
		{eventspec.StoredAnswerability, largestGroup(callsNamed(t, "RecordStoredAnswerability"), 2) * passes, "RecordStoredAnswerability call sites per surface x passes"},
		{eventspec.AnswerReuseServedRequestID, len(callsNamed(t, "RecordAnswerReuseServedRequestID")) * passes, "RecordAnswerReuseServedRequestID call sites x passes"},
		{eventspec.ReadRequirementPopulation, len(callsNamed(t, "RecordReadRequirementPopulation")) * contractsv1.ContextFabricPlanRequirementOutcomeMaxCount, "one call site, one line per outcome row of the served answer"},
		{eventspec.ObservationCover, len(callsNamed(t, "RecordReadRequirementObservationCover")) * contractsv1.ContextFabricPlanRequirementOutcomeMaxCount * passes, "one call site, one line per outcome row of each pass"},
		{eventspec.MembershipCardinality, len(callsNamed(t, "RecordMembershipCardinality")) * passes, "RecordMembershipCardinality call sites x passes"},
		{eventspec.BudgetAssertion, len(callsNamed(t, "finalizeServed")) * passes, "finalizeServed call sites (the only recording entry; the measure-only path emits nothing) x passes"},
		{eventspec.ItemAccountingDisagreement, len(callsNamed(t, "RecordItemAccounting")) * passes, "RecordItemAccounting call sites x passes"},
	}
	for _, tc := range cases {
		if tc.want == 0 {
			t.Errorf("%s: the source has no call for this event; the pin would pass an empty bound", tc.event.ID)
		}
		if tc.event.MaxLinesPerScope != tc.want {
			t.Errorf("%s declares %d lines per scope; %s = %d", tc.event.ID, tc.event.MaxLinesPerScope, tc.why, tc.want)
		}
	}
}

// TestLinesBeyondTheDeclaredBoundAreRefusedByTheCertifier writes the declared
// number of lines of two bounded events through the production sink, certifies
// them, and requires the next line in the same request to be refused.
func TestLinesBeyondTheDeclaredBoundAreRefusedByTheCertifier(t *testing.T) {
	t.Parallel()
	var served bytes.Buffer
	sink, ctx := answerLineSink(&served, slog.LevelInfo)
	for range eventspec.AnswerReuseServedRequestID.MaxLinesPerScope {
		sink.RecordAnswerReuseServedRequestID(ctx, principalOrg1, "req_stored", true)
	}
	certifyRequestLine(t, served.Bytes(), eventspec.AnswerReuseServedRequestID, nil)
	sink.RecordAnswerReuseServedRequestID(ctx, principalOrg1, "req_stored", true)
	certifyRefused(t, served.Bytes(), eventspec.AnswerReuseServedRequestID, "more served request id lines than the declared bound")

	var skipped bytes.Buffer
	sink, ctx = answerLineSink(&skipped, slog.LevelInfo)
	for range eventspec.PriorSubjectReceiptsSkipped.MaxLinesPerScope {
		sink.RecordPriorSubjectReceiptsSkipped(ctx, principalOrg1, 1)
	}
	certifyRequestLine(t, skipped.Bytes(), eventspec.PriorSubjectReceiptsSkipped, nil)
	sink.RecordPriorSubjectReceiptsSkipped(ctx, principalOrg1, 1)
	certifyRefused(t, skipped.Bytes(), eventspec.PriorSubjectReceiptsSkipped, "more receipt skip lines than the declared bound")
}
