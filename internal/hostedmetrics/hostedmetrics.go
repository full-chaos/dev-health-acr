// Package hostedmetrics holds the OpenTelemetry instruments of the hosted MCP
// and API processes. Every label is a closed vocabulary: a value outside its
// vocabulary is recorded as "other", never as the raw string, so a caller
// cannot widen a series. No label carries an organization, a subject, a
// question or any free text.
package hostedmetrics

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instrument names. docs/observability.md documents exactly these.
const (
	ToolCallsName            = "acr_mcp_tool_calls_total"
	ToolLatencyName          = "acr_mcp_tool_latency_seconds"
	AnswersName              = "acr_answers_total"
	BudgetRefusalsName       = "acr_budget_refusals_total"
	AnswerReuseName          = "acr_answer_reuse_total"
	RequirementOutcomesName  = "acr_requirement_outcomes_total"
	FactReadAbortsName       = "acr_fact_read_aborts_total"
	InvestigationLatencyName = "acr_investigation_latency_seconds"
)

// Names lists every instrument this package creates.
func Names() []string {
	return []string{ToolCallsName, ToolLatencyName, AnswersName, BudgetRefusalsName, AnswerReuseName, RequirementOutcomesName, FactReadAbortsName, InvestigationLatencyName}
}

// LatencyBucketsSeconds are the explicit histogram boundaries. They reach 300 s
// because observed p99 is about 125 s for an MCP request and about 21 s for an
// investigation; the OTel default tops out at 10 s and would put both in the
// overflow bucket.
func LatencyBucketsSeconds() []float64 {
	return []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 45, 60, 90, 120, 180, 300}
}

const other = "other"

// answerStatuses are the investigation statuses plus "error" (no answer).
var answerStatuses = []string{"complete", "partial", "degraded", "no_match", "clarification_required", "error"}

var factReadAbortCauses = []string{"no_fact_requirements", "no_investigation_subjects", "other"}

// Vocabularies are the closed label sets a process supplies. A value outside
// its set is recorded as "other". An empty set records every value as "other".
type Vocabularies struct {
	Tools               []string
	ResultClasses       []string
	ReuseOutcomes       []string
	RequirementOutcomes []string
}

// Instruments records the hosted telemetry. A nil *Instruments records nothing,
// so a process without export runs the same code path.
type Instruments struct {
	toolCalls          metric.Int64Counter
	toolLatency        metric.Float64Histogram
	answers            metric.Int64Counter
	budgetRefusals     metric.Int64Counter
	answerReuse        metric.Int64Counter
	requirementOutcome metric.Int64Counter
	factReadAborts     metric.Int64Counter
	investigationLat   metric.Float64Histogram
	vocab              Vocabularies
}

// New creates the instruments on meter.
func New(meter metric.Meter, vocab Vocabularies) (*Instruments, error) {
	if meter == nil {
		return nil, errors.New("hostedmetrics: meter is required")
	}
	var errs []error
	counter := func(name, description string) metric.Int64Counter {
		c, err := meter.Int64Counter(name, metric.WithDescription(description))
		errs = append(errs, err)
		return c
	}
	histogram := func(name, description string) metric.Float64Histogram {
		h, err := meter.Float64Histogram(name, metric.WithDescription(description), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(LatencyBucketsSeconds()...))
		errs = append(errs, err)
		return h
	}
	in := &Instruments{
		toolCalls:          counter(ToolCallsName, "MCP tools/call requests by tool, result class and HTTP status class."),
		toolLatency:        histogram(ToolLatencyName, "MCP tools/call request latency by tool."),
		answers:            counter(AnswersName, "Investigation answers delivered by status and tool."),
		budgetRefusals:     counter(BudgetRefusalsName, "Investigations refused because the answer did not fit the response budget."),
		answerReuse:        counter(AnswerReuseName, "Answer reuse attempts by outcome."),
		requirementOutcome: counter(RequirementOutcomesName, "Plan requirement outcome rows of served investigations by outcome."),
		factReadAborts:     counter(FactReadAbortsName, "Investigations aborted in the fact read by cause."),
		investigationLat:   histogram(InvestigationLatencyName, "Investigation request latency by terminal status."),
		vocab:              vocab,
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return in, nil
}

func bucket(value string, vocabulary []string) string {
	if slices.Contains(vocabulary, value) {
		return value
	}
	return other
}

// ToolCall records one tools/call request: its counter and, when the call ran
// a tool, its latency.
func (i *Instruments) ToolCall(ctx context.Context, tool, resultClass string, httpStatus int, latency time.Duration) {
	if i == nil {
		return
	}
	tool = bucket(tool, i.vocab.Tools)
	i.toolCalls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("tool", tool),
		attribute.String("result_class", bucket(resultClass, i.vocab.ResultClasses)),
		attribute.String("status", StatusClass(httpStatus)),
	))
	i.toolLatency.Record(ctx, latency.Seconds(), metric.WithAttributes(attribute.String("tool", tool)))
}

// Answer records one delivered investigation answer.
func (i *Instruments) Answer(ctx context.Context, status, tool string) {
	if i == nil {
		return
	}
	i.answers.Add(ctx, 1, metric.WithAttributes(
		attribute.String("status", bucket(status, answerStatuses)),
		attribute.String("tool", bucket(tool, i.vocab.Tools)),
	))
}

// BudgetRefusal records one investigation refused for not fitting its budget.
func (i *Instruments) BudgetRefusal(ctx context.Context) {
	if i == nil {
		return
	}
	i.budgetRefusals.Add(ctx, 1)
}

// AnswerReuse records one answer reuse attempt.
func (i *Instruments) AnswerReuse(ctx context.Context, outcome string) {
	if i == nil {
		return
	}
	i.answerReuse.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", bucket(outcome, i.vocab.ReuseOutcomes))))
}

// RequirementOutcome records n requirement outcome rows of one outcome.
func (i *Instruments) RequirementOutcome(ctx context.Context, outcome string, n int) {
	if i == nil || n <= 0 {
		return
	}
	i.requirementOutcome.Add(ctx, int64(n), metric.WithAttributes(attribute.String("outcome", bucket(outcome, i.vocab.RequirementOutcomes))))
}

// FactReadAbort records one investigation aborted in the fact read.
func (i *Instruments) FactReadAbort(ctx context.Context, cause string) {
	if i == nil {
		return
	}
	i.factReadAborts.Add(ctx, 1, metric.WithAttributes(attribute.String("cause", bucket(cause, factReadAbortCauses))))
}

// InvestigationLatency records one investigation request's latency by its
// terminal status ("error" when no answer was produced).
func (i *Instruments) InvestigationLatency(ctx context.Context, status string, latency time.Duration) {
	if i == nil {
		return
	}
	i.investigationLat.Record(ctx, latency.Seconds(), metric.WithAttributes(attribute.String("status", bucket(status, answerStatuses))))
}

// StatusClass buckets an HTTP status.
func StatusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "unknown"
	}
}

// AnswerStatusVocabulary and FactReadAbortCauseVocabulary expose the closed
// label sets this package owns.
func AnswerStatusVocabulary() []string       { return slices.Clone(answerStatuses) }
func FactReadAbortCauseVocabulary() []string { return slices.Clone(factReadAbortCauses) }
