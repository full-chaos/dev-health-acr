package contextfabric_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

func TestRequirementAndBudgetLinesCertifyWhatTheEngineProduces(t *testing.T) {
	t.Parallel()
	events := contextfabric.RequirementLineEventsFromEngineForTest(t)
	if len(events.Populations) == 0 || len(events.Covers) == 0 || len(events.Cardinalities) == 0 || len(events.CountScopes) == 0 || len(events.Assertions) == 0 || len(events.Accountings) == 0 {
		t.Fatalf("the engine scenarios produced population %d, cover %d, cardinality %d, count scope %d, assertion %d, accounting %d events, want every kind",
			len(events.Populations), len(events.Covers), len(events.Cardinalities), len(events.CountScopes), len(events.Assertions), len(events.Accountings))
	}
	org := map[string]any{"org_id": "org_1"}
	for _, event := range events.Populations {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordReadRequirementPopulation(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.ReadRequirementPopulation, org)
	}
	for _, event := range events.Covers {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordReadRequirementObservationCover(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.ObservationCover, org)
	}
	for _, event := range events.Cardinalities {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordMembershipCardinality(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.MembershipCardinality, org)
	}
	for _, event := range events.CountScopes {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordCountPopulationScope(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.CountPopulationScope, org)
	}
	for _, event := range events.Assertions {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordBudgetAssertion(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.BudgetAssertion, org)
	}
	for _, event := range events.Accountings {
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordItemAccounting(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.ItemAccountingDisagreement, org)
	}
}

func TestItemAccountingLineBeyondTheDeclaredBoundIsRefused(t *testing.T) {
	t.Parallel()
	events := contextfabric.RequirementLineEventsFromEngineForTest(t)
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	for range eventspec.ItemAccountingDisagreement.MaxLinesPerScope {
		sink.RecordItemAccounting(ctx, principalOrg1, events.Accountings[0])
	}
	certifyRequestLine(t, buf.Bytes(), eventspec.ItemAccountingDisagreement, nil)
	sink.RecordItemAccounting(ctx, principalOrg1, events.Accountings[0])
	certifyRefused(t, buf.Bytes(), eventspec.ItemAccountingDisagreement, "more accounting lines than the declared bound")
}

func TestLedgerDisagreementPatternAcceptsEveryFormTheLedgerWrites(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(contextfabric.LedgerDisagreementPattern())
	for _, form := range []string{"", "drivers", "claimed_facts#3", "global", "multi_group", "allocation:grant_formula", "collection:", "bucket:group", "collection:paths", "bucket:not-a-bucket"} {
		if !re.MatchString(form) {
			t.Errorf("the pattern refuses the ledger form %q", form)
		}
	}
	for _, form := range []string{"paths", "drivers:4!=5", "allocation:", "allocation:other", "Drivers"} {
		if re.MatchString(form) {
			t.Errorf("the pattern accepts %q, which no ledger writes", form)
		}
	}
}

func TestItemAccountingLineCertifiesTheInvalidDebitValuesTheLedgerWrites(t *testing.T) {
	t.Parallel()
	events := contextfabric.RequirementLineEventsFromEngineForTest(t)
	for _, disagreement := range []string{"collection:paths", "bucket:not-a-bucket"} {
		event := events.Accountings[0]
		event.Disagreement = disagreement
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordItemAccounting(ctx, principalOrg1, event)
		certifyRequestLine(t, buf.Bytes(), eventspec.ItemAccountingDisagreement, map[string]any{"org_id": "org_1", "ledger_disagreement": disagreement})
	}
}

func TestItemAccountingStagesAreTheLiteralsTheProducersPass(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				index := map[string]int{"measureAssembledAttempt": 2, "serveAtServiceCeiling": 4}[sel.Sel.Name]
				if index == 0 || index >= len(call.Args) {
					return true
				}
				if lit, ok := call.Args[index].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					value, _ := strconv.Unquote(lit.Value)
					written[value] = true
				}
				return true
			})
		}
	}
	assertStages := map[string]bool{}
	for _, stage := range contextfabric.BudgetAssertStageLogVocabulary() {
		assertStages[stage] = true
	}
	declared := map[string]bool{}
	for _, stage := range contextfabric.ItemAccountingStageLogVocabulary() {
		declared[stage] = true
	}
	for stage := range written {
		if !declared[stage] {
			t.Errorf("a producer measures an attempt under stage %q that the item accounting vocabulary does not name", stage)
		}
	}
	for stage := range declared {
		if !assertStages[stage] && !written[stage] {
			t.Errorf("the item accounting vocabulary names stage %q that no producer passes", stage)
		}
	}
	if len(written) < 8 {
		t.Fatalf("found %d stage literals in the producers, want the nine measured stages: %v", len(written), keysOf(written))
	}
}
