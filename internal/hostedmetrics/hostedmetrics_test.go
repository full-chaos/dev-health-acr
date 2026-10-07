package hostedmetrics_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

func TestLabelsAreClosedVocabularies(t *testing.T) {
	ctx := context.Background()
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{
		Tools: []string{"read_facts"}, ResultClasses: []string{"ok"},
		ReuseOutcomes: []string{"hit"}, RequirementOutcomes: []string{"satisfied"},
	})
	instruments.ToolCall(ctx, "read_facts", "ok", 200, time.Second)
	instruments.ToolCall(ctx, "tool-from-a-caller", "free text", 502, time.Second)
	instruments.Answer(ctx, "partial", "read_facts")
	instruments.Answer(ctx, "a question about acme", "someone")
	instruments.BudgetRefusal(ctx)
	instruments.AnswerReuse(ctx, "hit")
	instruments.AnswerReuse(ctx, "org-123")
	instruments.RequirementOutcome(ctx, "satisfied", 2)
	instruments.RequirementOutcome(ctx, "satisfied", 0)
	instruments.FactReadAbort(ctx, "no_fact_requirements")
	instruments.FactReadAbort(ctx, "fact query subjects must be unique: team:X")
	instruments.InvestigationLatency(ctx, "complete", time.Second)
	instruments.InvestigationLatency(ctx, "error", time.Second)

	want := map[string]int64{
		"acr_mcp_tool_calls_total{result_class=ok,status=2xx,tool=read_facts}": 1,
		"acr_mcp_tool_calls_total{result_class=other,status=5xx,tool=other}":   1,
		"acr_mcp_tool_latency_seconds{tool=read_facts}":                        1,
		"acr_mcp_tool_latency_seconds{tool=other}":                             1,
		"acr_answers_total{status=partial,tool=read_facts}":                    1,
		"acr_answers_total{status=other,tool=other}":                           1,
		"acr_budget_refusals_total{}":                                          1,
		"acr_answer_reuse_total{outcome=hit}":                                  1,
		"acr_answer_reuse_total{outcome=other}":                                1,
		"acr_requirement_outcomes_total{outcome=satisfied}":                    2,
		"acr_fact_read_aborts_total{cause=no_fact_requirements}":               1,
		"acr_fact_read_aborts_total{cause=other}":                              1,
		"acr_investigation_latency_seconds{status=complete}":                   1,
		"acr_investigation_latency_seconds{status=error}":                      1,
	}
	got := read()
	if len(got) != len(want) {
		t.Fatalf("cells %v, want %v", got, want)
	}
	for cell, value := range want {
		if got[cell] != value {
			t.Errorf("%s = %d, want %d", cell, got[cell], value)
		}
	}
}

func TestNilInstrumentsRecordNothing(t *testing.T) {
	var instruments *hostedmetrics.Instruments
	ctx := context.Background()
	instruments.ToolCall(ctx, "x", "y", 200, time.Second)
	instruments.Answer(ctx, "x", "y")
	instruments.BudgetRefusal(ctx)
	instruments.AnswerReuse(ctx, "x")
	instruments.RequirementOutcome(ctx, "x", 1)
	instruments.FactReadAbort(ctx, "x")
	instruments.InvestigationLatency(ctx, "x", time.Second)
}

func TestLatencyBucketsReachTheObservedP99(t *testing.T) {
	buckets := hostedmetrics.LatencyBucketsSeconds()
	if buckets[len(buckets)-1] != 300 {
		t.Fatalf("top bucket %v, want 300 s", buckets[len(buckets)-1])
	}
	for i := 1; i < len(buckets); i++ {
		if buckets[i] <= buckets[i-1] {
			t.Fatalf("buckets not increasing: %v", buckets)
		}
	}
}
