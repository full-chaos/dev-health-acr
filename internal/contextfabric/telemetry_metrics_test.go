package contextfabric

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func requirementVocabulary() []string {
	members := contractsv1.ContextFabricPlanRequirementOutcomeVocabulary()
	values := make([]string, 0, len(members))
	for _, member := range members {
		values = append(values, string(member))
	}
	return values
}

// The sink counts at the site of the certified line: the reuse outcome and the
// requirement outcome rows of the investigation, with the line still written.
func TestSlogEngineTelemetryCountsReuseAndRequirementOutcomes(t *testing.T) {
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{
		ReuseOutcomes: AnswerReuseOutcomeVocabulary(), RequirementOutcomes: requirementVocabulary(),
	})
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		telemetry := NewSlogEngineTelemetry(logger).WithMetrics(instruments)
		principal := storage.Principal{OrgID: "org_metrics_test"}
		telemetry.RecordAnswerReuse(context.Background(), principal, AnswerReuseHit, "v1")
		telemetry.RecordAnswerReuse(context.Background(), principal, AnswerReuseMissNoCandidate, "v1")
		telemetry.RecordAnswerReuse(context.Background(), principal, AnswerReuseOutcome("attacker_chosen_value"), "v1")
		var event CompletenessAuthorityObservation
		satisfied := slices.Index(requirementVocabulary(), string(contractsv1.ContextFabricRequirementSatisfied))
		unavailable := slices.Index(requirementVocabulary(), string(contractsv1.ContextFabricRequirementUnavailable))
		event.OutcomeRowsByKind[satisfied] = 3
		event.OutcomeRowsByKind[unavailable] = 1
		event.OutcomeRowsTotal = 4
		telemetry.RecordCompletenessAuthority(context.Background(), principal, event)
	})
	if len(records) != 4 {
		t.Fatalf("%d log records, want 4: the lines must still be written", len(records))
	}
	got := read()
	want := map[string]int64{
		"acr_answer_reuse_total{outcome=hit}":                 1,
		"acr_answer_reuse_total{outcome=miss_no_candidate}":   1,
		"acr_answer_reuse_total{outcome=other}":               1,
		"acr_requirement_outcomes_total{outcome=satisfied}":   3,
		"acr_requirement_outcomes_total{outcome=unavailable}": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("cells %v, want %v", got, want)
	}
	for cell, value := range want {
		if got[cell] != value {
			t.Errorf("%s = %d, want %d (all cells %v)", cell, got[cell], value, got)
		}
	}
}

// A reuse outcome constant that is not in the vocabulary would be counted as
// "other"; every constant of the type must be a member.
func TestAnswerReuseOutcomeVocabularyHoldsEveryOutcomeConstant(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "answer_reuse.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	vocabulary := AnswerReuseOutcomeVocabulary()
	found := 0
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || spec.Type == nil {
			return true
		}
		if ident, ok := spec.Type.(*ast.Ident); !ok || ident.Name != "AnswerReuseOutcome" {
			return true
		}
		for _, value := range spec.Values {
			literal, ok := value.(*ast.BasicLit)
			if !ok {
				continue
			}
			found++
			if !slices.Contains(vocabulary, literal.Value[1:len(literal.Value)-1]) {
				t.Errorf("reuse outcome %s is not in AnswerReuseOutcomeVocabulary", literal.Value)
			}
		}
		return true
	})
	if found != len(vocabulary) {
		t.Fatalf("parsed %d outcome constants, vocabulary holds %d", found, len(vocabulary))
	}
}
