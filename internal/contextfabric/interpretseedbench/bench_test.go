package interpretseedbench

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// stubInterpreter is a fake Interpreter: it returns a scripted Shape per
// (question, sample) pair and records the exact requests it saw, so tests
// can assert Run calls InterpretQuestionForSample with the right sample
// index for every question without making a real model call.
type stubInterpreter struct {
	shapeFor func(questionID string, sample int) string
	seen     []struct {
		question string
		sample   int
	}
}

func (s *stubInterpreter) InterpretQuestionForSample(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest, sample int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	s.seen = append(s.seen, struct {
		question string
		sample   int
	}{request.Question, sample})
	shape := s.shapeFor(request.Question, sample)
	return contextfabric.InterpretedQuestion{Shape: contextfabric.InvestigationShape(shape)}, contextfabric.ModelExecutionReceipt{
		Outcome: "success",
		Usage:   contextfabric.ModelUsage{InputTokens: 10 + sample, OutputTokens: 5, TotalTokens: 15 + sample},
	}, nil
}

// TestRunCallsEverySampleForEveryQuestion is the red-first pin for this
// whole package (it does not exist at all on origin/main): Run must call
// InterpretQuestionForSample exactly n times per question, with sample
// indices 0..n-1, for every question in the input list -- never skipping a
// question, never reusing a sample index, never calling more than n times.
func TestRunCallsEverySampleForEveryQuestion(t *testing.T) {
	t.Parallel()
	stub := &stubInterpreter{shapeFor: func(string, int) string { return "discovered_cohort" }}
	questions := []Question{{ID: "q1", Text: "question one"}, {ID: "q2", Text: "question two"}}

	results, err := Run(context.Background(), stub, storage.Principal{OrgID: "org_1"}, questions, 3)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 6 {
		t.Fatalf("len(results) = %d, want 6 (2 questions * 3 samples)", len(results))
	}
	if len(stub.seen) != 6 {
		t.Fatalf("interpreter saw %d calls, want 6", len(stub.seen))
	}
	wantSamplesPerQuestion := map[string]map[int]bool{"question one": {}, "question two": {}}
	for _, call := range stub.seen {
		wantSamplesPerQuestion[call.question][call.sample] = true
	}
	for question, samples := range wantSamplesPerQuestion {
		for i := 0; i < 3; i++ {
			if !samples[i] {
				t.Fatalf("question %q missing sample %d call", question, i)
			}
		}
		if len(samples) != 3 {
			t.Fatalf("question %q got %d distinct sample indices, want exactly 3", question, len(samples))
		}
	}
}

// TestRunRecordsFailedSamplesRatherThanDropping proves a failed call still
// produces a Sample (Error set, Shape empty) -- a run that silently dropped
// failures would under-report its own sample count, which is exactly the
// kind of "measurement that did not happen must FAIL loudly" defect
// AGENTS.md's verification rules warn about.
func TestRunRecordsFailedSamplesRatherThanDropping(t *testing.T) {
	t.Parallel()
	stub := &failingInterpreter{}
	questions := []Question{{ID: "q1", Text: "question one"}}

	results, err := Run(context.Background(), stub, storage.Principal{OrgID: "org_1"}, questions, 2)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2 (failures must still be recorded)", len(results))
	}
	for _, r := range results {
		if r.Error == "" {
			t.Fatalf("Sample %#v has no Error recorded for a failed call", r)
		}
		if r.Shape != "" {
			t.Fatalf("Sample %#v has a non-empty Shape for a failed call", r)
		}
	}
}

type failingInterpreter struct{}

func (failingInterpreter) InterpretQuestionForSample(context.Context, storage.Principal, contextfabric.InvestigationRequest, int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{Outcome: "unavailable"}, errBoom
}

var errBoom = errors.New("boom")

// TestShapeDistributionTalliesPerQuestion pins the aggregation Run's raw
// samples feed into the PR-body distribution table.
func TestShapeDistributionTalliesPerQuestion(t *testing.T) {
	t.Parallel()
	samples := []Sample{
		{QuestionID: "q1", Shape: "discovered_cohort"},
		{QuestionID: "q1", Shape: "discovered_cohort"},
		{QuestionID: "q1", Shape: "explicit_cohort"},
		{QuestionID: "q2", Shape: "single_subject"},
	}
	dist := ShapeDistribution(samples)
	if dist["q1"]["discovered_cohort"] != 2 || dist["q1"]["explicit_cohort"] != 1 {
		t.Fatalf("dist[q1] = %#v, want discovered_cohort=2 explicit_cohort=1", dist["q1"])
	}
	if dist["q2"]["single_subject"] != 1 {
		t.Fatalf("dist[q2] = %#v, want single_subject=1", dist["q2"])
	}
}

// TestCostSummariesExcludeFailedSamples proves a failed sample's zero usage
// never drags the average down -- the Fable F2 cost-delta deliverable must
// reflect what a SUCCESSFUL turn-1 actually costs, not be diluted by
// transient failures that carry no real usage.
func TestCostSummariesExcludeFailedSamples(t *testing.T) {
	t.Parallel()
	samples := []Sample{
		{QuestionID: "q1", InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		{QuestionID: "q1", InputTokens: 200, OutputTokens: 40, TotalTokens: 240},
		{QuestionID: "q1", Error: "boom"},
	}
	summaries := CostSummaries(samples, []Question{{ID: "q1"}})
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	got := summaries[0]
	if got.Samples != 2 {
		t.Fatalf("Samples = %d, want 2 (the failed sample must be excluded)", got.Samples)
	}
	if got.AvgInputTokens != 150 || got.AvgOutputTokens != 30 || got.AvgTotalTokens != 180 {
		t.Fatalf("averages = %#v, want input=150 output=30 total=180", got)
	}
}

// fallbackInterpreter simulates a configured Fallback ModelRuntime firing:
// every call reports FallbackUsed=true on the receipt, exactly as
// genkitruntime's interpretQuestionWithSample does when the primary model
// fails and r.config.Fallback.InterpretQuestion succeeds (its doc comment
// explains why the fallback leg cannot honor `sample`).
type fallbackInterpreter struct{}

func (fallbackInterpreter) InterpretQuestionForSample(_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, _ int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{Shape: contextfabric.InvestigationShape("single_subject")},
		contextfabric.ModelExecutionReceipt{Outcome: "fallback", FallbackUsed: true, Usage: contextfabric.ModelUsage{InputTokens: 999, OutputTokens: 999, TotalTokens: 1998}},
		nil
}

// TestRunSurfacesFallbackUsed is codex round-1 finding 1's red-first pin:
// Run must propagate receipt.FallbackUsed onto the Sample it records, so a
// downstream aggregator can tell a fallback-sample-0 result apart from a
// genuine seed_i sample.
func TestRunSurfacesFallbackUsed(t *testing.T) {
	t.Parallel()
	results, err := Run(context.Background(), fallbackInterpreter{}, storage.Principal{OrgID: "org_1"}, []Question{{ID: "q1", Text: "question one"}}, 1)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].FallbackUsed {
		t.Fatalf("results = %#v, want exactly one Sample with FallbackUsed=true", results)
	}
}

// TestShapeDistributionExcludesFallbackSamples is codex round-1 finding 1's
// aggregation-side pin: a FallbackUsed sample was generated under the
// fallback model's own sample-0 decoding, never seed_i, so counting it in
// the Shape distribution would misrepresent a fallback artifact as a
// genuine derived-seed data point.
func TestShapeDistributionExcludesFallbackSamples(t *testing.T) {
	t.Parallel()
	samples := []Sample{
		{QuestionID: "q1", Shape: "discovered_cohort"},
		{QuestionID: "q1", Shape: "single_subject", FallbackUsed: true},
	}
	dist := ShapeDistribution(samples)
	if dist["q1"]["discovered_cohort"] != 1 {
		t.Fatalf("dist[q1] = %#v, want discovered_cohort=1", dist["q1"])
	}
	if dist["q1"]["single_subject"] != 0 {
		t.Fatalf("dist[q1] = %#v, the FallbackUsed sample must be excluded entirely, not just uncounted", dist["q1"])
	}
	total := 0
	for _, n := range dist["q1"] {
		total += n
	}
	if total != 1 {
		t.Fatalf("dist[q1] totals %d samples, want exactly 1 (the fallback sample dropped)", total)
	}
}

// TestCostSummariesExcludeFallbackSamples mirrors the Shape-distribution
// exclusion for cost: a fallback call spent tokens on a DIFFERENT model,
// so folding its usage into the primary model's average cost would
// misstate the very number CHAOS-4631 exists to measure.
func TestCostSummariesExcludeFallbackSamples(t *testing.T) {
	t.Parallel()
	samples := []Sample{
		{QuestionID: "q1", InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		{QuestionID: "q1", InputTokens: 999, OutputTokens: 999, TotalTokens: 1998, FallbackUsed: true},
	}
	summaries := CostSummaries(samples, []Question{{ID: "q1"}})
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	got := summaries[0]
	if got.Samples != 1 {
		t.Fatalf("Samples = %d, want 1 (the fallback sample must be excluded)", got.Samples)
	}
	if got.AvgTotalTokens != 120 {
		t.Fatalf("AvgTotalTokens = %v, want 120 (fallback's 1998 must not be averaged in)", got.AvgTotalTokens)
	}
}

// TestValidateResultsFailsLoudlyWhenEverySampleErrored is codex round-1
// finding 2's red-first pin, EXECUTED repro reproduced directly: against an
// unreachable endpoint, main() printed an all-"(error)" table and exited 0.
// ValidateResults must return a non-nil error for exactly that shape.
func TestValidateResultsFailsLoudlyWhenEverySampleErrored(t *testing.T) {
	t.Parallel()
	samples := []Sample{
		{QuestionID: "q1", Sample: 0, Error: "connection refused"},
		{QuestionID: "q1", Sample: 1, Error: "connection refused"},
	}
	if err := ValidateResults(samples); err == nil {
		t.Fatal("ValidateResults() = nil, want a non-nil error when every sample failed")
	}
}

// TestValidateResultsFailsLoudlyWhenEverySampleIsFallback is codex round 2's
// red-first pin: round 1's fix counted only Error, so a run where every
// sample errored on the primary and a configured fallback answered every
// one (zero Error, but zero usable data either -- the aggregators exclude
// FallbackUsed entirely) passed ValidateResults and still printed an empty
// distribution/cost table. EXECUTED by codex against a loopback provider.
func TestValidateResultsFailsLoudlyWhenEverySampleIsFallback(t *testing.T) {
	t.Parallel()
	samples := []Sample{
		{QuestionID: "q1", Sample: 0, Shape: "single_subject", Outcome: "fallback", FallbackUsed: true},
		{QuestionID: "q1", Sample: 1, Shape: "single_subject", Outcome: "fallback", FallbackUsed: true},
	}
	if err := ValidateResults(samples); err == nil {
		t.Fatal("ValidateResults() = nil, want a non-nil error when every sample only produced fallback data")
	}
}

// TestValidateResultsPassesWithPartialFailures proves the fix does not
// overcorrect: a handful of transient failures mixed with real samples is
// exactly the case Run's own doc comment says must NOT be treated as a
// Run-level fault.
func TestValidateResultsPassesWithPartialFailures(t *testing.T) {
	t.Parallel()
	samples := []Sample{
		{QuestionID: "q1", Sample: 0, Shape: "discovered_cohort"},
		{QuestionID: "q1", Sample: 1, Error: "connection refused"},
	}
	if err := ValidateResults(samples); err != nil {
		t.Fatalf("ValidateResults() = %v, want nil for a partial failure", err)
	}
}

// TestValidateResultsFailsOnEmptyInput guards the degenerate case: zero
// samples is not a measurement either.
func TestValidateResultsFailsOnEmptyInput(t *testing.T) {
	t.Parallel()
	if err := ValidateResults(nil); err == nil {
		t.Fatal("ValidateResults(nil) = nil, want a non-nil error")
	}
}
