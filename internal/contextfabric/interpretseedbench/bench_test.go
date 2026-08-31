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
