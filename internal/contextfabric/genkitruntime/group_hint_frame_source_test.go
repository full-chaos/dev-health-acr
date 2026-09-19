package genkitruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// parsedRuntime replays a receipt built by the production parser and receipt
// stamper, so the interpreter under test sees exactly what a transport
// produces for one raw model output.
type parsedRuntime struct {
	interpreted contextfabric.InterpretedQuestion
	receipt     contextfabric.ModelExecutionReceipt
}

func (r parsedRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return r.interpreted, r.receipt, nil
}

func (r parsedRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, nil
}

type recordingSink struct {
	recorded []contextfabric.ModelExecutionReceipt
}

func (s *recordingSink) RecordModelExecution(_ context.Context, _ storage.Principal, receipt contextfabric.ModelExecutionReceipt) error {
	s.recorded = append(s.recorded, receipt)
	return nil
}

func interpretRawGroupedMetric(t *testing.T, flatGroupKind string) (contextfabric.QuestionFamilyOutcome, contextfabric.ModelExecutionReceipt) {
	t.Helper()
	output := validInterpretationOutput()
	output.Shape = "single_subject"
	output.GroupKind = flatGroupKind
	output.QuestionFrame = &questionFrameOutput{
		Goals: []string{"count_or_aggregate"},
		SubjectExpression: &subjectExpressionOutput{
			Kind: "grouped_members", GroupKind: "repository", MemberKind: "metric",
		},
		Temporal: "current",
	}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	interpreted, capture, err := ParseInterpretationOutputSignals(raw, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatalf("ParseInterpretationOutputSignals() error = %v", err)
	}
	started := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	receipt := contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: "prompt-v1", SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1",
		StartedAt: started, CompletedAt: started, Attempts: 1,
		InputDigest: strings.Repeat("a", 64), OutputDigest: strings.Repeat("b", 64), Outcome: "success",
	}
	ApplyInterpretationCapture(&receipt, capture)
	sink := &recordingSink{}
	interpreter := contextfabric.RuntimeQuestionInterpreter{Runtime: parsedRuntime{interpreted: interpreted, receipt: receipt}, Sink: sink}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678", Question: "q",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if len(sink.recorded) == 0 {
		t.Fatal("the sink recorded no receipt")
	}
	return outcome, sink.recorded[len(sink.recorded)-1]
}

// A model output that names the group axis only inside its frame must reach
// the same repaired, servable cohort as one that also states the flat hint.
func TestGroupedMetricRepairCompletesWhenOnlyTheFrameNamesTheGroupKind(t *testing.T) {
	for _, testCase := range []struct {
		name, flat string
		wantSource contextfabric.GroupHintSource
	}{
		{"flat hint absent", "", contextfabric.GroupHintSourceFrame},
		{"flat hint stated", "repository", contextfabric.GroupHintSourceModel},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outcome, receipt := interpretRawGroupedMetric(t, testCase.flat)
			if receipt.GroupKind != contextfabric.SubjectRepository || receipt.GroupKindSource != testCase.wantSource {
				t.Fatalf("receipt group kind/source = %q/%q, want repository/%q", receipt.GroupKind, receipt.GroupKindSource, testCase.wantSource)
			}
			if receipt.FrameOutcome != contextfabric.FrameValidationOutcomeRepaired {
				t.Fatalf("frame outcome = %q, want repaired", receipt.FrameOutcome)
			}
			if outcome.Gate.Refuses() {
				t.Fatalf("gate %s refuses the repaired frame", outcome.Gate.Observable())
			}
			if outcome.Frame == nil || outcome.Frame.SubjectExpression.Kind != contextfabric.SubjectExpressionDiscoveredKind {
				t.Fatalf("served frame = %+v, want discovered_kind", outcome.Frame)
			}
		})
	}
}
