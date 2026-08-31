// Package interpretseedbench is CHAOS-4631's Shape-distribution measurement
// harness: for each of the ticket's acceptance questions, it calls
// genkitruntime's CHAOS-4631 measurement entry point
// (InterpretQuestionForSample) N times under N DISTINCT, deterministically
// derived seeds -- design doc §4.1's "seed_i = f(stable_question_hash, i)"
// scheme -- against the REAL configured model, and records what
// interpretation.Shape (and the token/latency cost) each sample produced.
//
// This is deliberately NOT a test of production behaviour: production
// (genkitruntime.Runtime.InterpretQuestion) only ever calls sample=0 (S1
// ships N=1). This package exists because the design requires the S1
// measurement to sample under the SAME N-distinct-seed scheme S2's future
// consensus resolver will use -- sampling under any other scheme (one fixed
// seed repeated N times, say) would measure a distribution the running
// system will never produce (design §4.1; ticket CHAOS-4631 body point 3).
//
// No question text, subject term, or model output ever appears in a
// telemetry event from production code as a result of this package --
// interpretseedbench is a standalone measurement tool (run manually,
// results saved to a local file for a PR/handoff table), not a production
// code path, and it prints the plain acceptance-question text to its own
// output by design (that IS the point of the tool: a human reads the
// table).
package interpretseedbench

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Question is one of the ticket's four/five acceptance-question texts.
type Question struct {
	// ID is a short, stable label for tables/JSON -- never logged by
	// production code, only used by this standalone tool's own output.
	ID   string
	Text string
}

// AcceptanceQuestions is CHAOS-4631's required measurement set: "n=5 per
// acceptance question (Q1, Q2, Q-A verbatim+clean, Q-B from CHAOS-4622)" --
// the two BAR questions (design doc §7's Q1/Q2, which must not move) plus
// CHAOS-4622's own three phrasings (Q-A typo-verbatim, Q-A cleaned, Q-B),
// which is where the design's own ground-truth replicate table (§4.1) came
// from. Text is copied verbatim from the design doc / CHAOS-4622's issue
// body -- never paraphrased, since QuestionHash (and therefore the derived
// seed) is sensitive to exact text.
var AcceptanceQuestions = []Question{
	{ID: "Q1-bar", Text: "What is the status of the Dev Health Ops project?"},
	{ID: "Q2-bar", Text: "Which teams are struggling, and why?"},
	{ID: "QA-typo", Text: "What's are the project statuses for each team, and what are the main drivers?"},
	{ID: "QA-clean", Text: "What are the project statuses for each team, and what are the main drivers?"},
	{ID: "QB", Text: "What are the statuses of the fullchaos team's projects?"},
}

// Interpreter is the subset of *genkitruntime.Runtime this package calls.
// Defined here (not imported from genkitruntime) so this package can be
// unit-tested against a stub without pulling in a real genkit instance --
// *genkitruntime.Runtime satisfies it structurally.
type Interpreter interface {
	InterpretQuestionForSample(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, sample int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error)
}

// Sample is one (question, sample-index) measurement.
type Sample struct {
	QuestionID   string        `json:"question_id"`
	Sample       int           `json:"sample"`
	Shape        string        `json:"shape"`
	Outcome      string        `json:"outcome"`
	Error        string        `json:"error,omitempty"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	TotalTokens  int           `json:"total_tokens"`
	Duration     time.Duration `json:"duration_ns"`
}

// baseRequest builds the fixed InvestigationRequest scaffolding CHAOS-4631
// measurement calls share -- everything InterpretedQuestion.Validate/
// InvestigationRequest.Validate require, but nothing question-specific
// (Question itself is overwritten per call).
func baseRequest(requestID string) contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     requestID,
		TimeContext:   contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "acr-interpret-seed-bench", Version: "v1", Surface: "measurement"},
	}
}

// Run calls interpreter.InterpretQuestionForSample once per (question,
// sample) pair, sample = 0..n-1, for every question in questions, and
// returns one Sample per call -- including failed calls (Outcome/Error
// set, Shape empty), so a run that hits a transient failure does not
// silently under-report its own sample count.
func Run(ctx context.Context, interpreter Interpreter, principal storage.Principal, questions []Question, n int) ([]Sample, error) {
	if n < 1 {
		return nil, fmt.Errorf("n must be positive, got %d", n)
	}
	results := make([]Sample, 0, len(questions)*n)
	for _, q := range questions {
		for sample := 0; sample < n; sample++ {
			request := baseRequest(fmt.Sprintf("bench_%s_%d", q.ID, sample))
			request.Question = q.Text
			started := time.Now()
			interpreted, receipt, err := interpreter.InterpretQuestionForSample(ctx, principal, request, sample)
			duration := time.Since(started)
			result := Sample{
				QuestionID: q.ID, Sample: sample, Duration: duration,
				InputTokens: receipt.Usage.InputTokens, OutputTokens: receipt.Usage.OutputTokens,
				TotalTokens: receipt.Usage.TotalTokens, Outcome: receipt.Outcome,
			}
			if err != nil {
				result.Error = err.Error()
			} else {
				result.Shape = string(interpreted.Shape)
			}
			results = append(results, result)
		}
	}
	return results, nil
}

// ShapeDistribution tallies, per question ID, how many of its samples
// produced each Shape value (errored samples are counted under the empty
// string key, distinctly from any real Shape, so a run with failures is
// never silently mistaken for one with total agreement).
func ShapeDistribution(samples []Sample) map[string]map[string]int {
	dist := make(map[string]map[string]int)
	for _, s := range samples {
		byShape, ok := dist[s.QuestionID]
		if !ok {
			byShape = make(map[string]int)
			dist[s.QuestionID] = byShape
		}
		byShape[s.Shape]++
	}
	return dist
}

// CostSummary is the per-question average token/latency cost CHAOS-4631
// requires as "measured token-cost delta per turn-1" (Fable F2).
type CostSummary struct {
	QuestionID      string  `json:"question_id"`
	Samples         int     `json:"samples"`
	AvgInputTokens  float64 `json:"avg_input_tokens"`
	AvgOutputTokens float64 `json:"avg_output_tokens"`
	AvgTotalTokens  float64 `json:"avg_total_tokens"`
	AvgDurationMS   float64 `json:"avg_duration_ms"`
}

// CostSummaries aggregates Run's output into one CostSummary per question,
// in AcceptanceQuestions order for successful (non-empty Outcome=="success")
// samples only -- a failed call's zero usage would otherwise silently drag
// the average down and understate the real per-turn-1 cost.
func CostSummaries(samples []Sample, questions []Question) []CostSummary {
	byQuestion := make(map[string][]Sample, len(questions))
	for _, s := range samples {
		if s.Error != "" {
			continue
		}
		byQuestion[s.QuestionID] = append(byQuestion[s.QuestionID], s)
	}
	summaries := make([]CostSummary, 0, len(questions))
	for _, q := range questions {
		rows := byQuestion[q.ID]
		summary := CostSummary{QuestionID: q.ID, Samples: len(rows)}
		if len(rows) == 0 {
			summaries = append(summaries, summary)
			continue
		}
		var inputSum, outputSum, totalSum float64
		var durationSum time.Duration
		for _, r := range rows {
			inputSum += float64(r.InputTokens)
			outputSum += float64(r.OutputTokens)
			totalSum += float64(r.TotalTokens)
			durationSum += r.Duration
		}
		count := float64(len(rows))
		summary.AvgInputTokens = inputSum / count
		summary.AvgOutputTokens = outputSum / count
		summary.AvgTotalTokens = totalSum / count
		summary.AvgDurationMS = float64(durationSum.Milliseconds()) / count
		summaries = append(summaries, summary)
	}
	return summaries
}
