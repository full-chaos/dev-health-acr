package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func planReadsReceipt(outcome string) ModelExecutionReceipt {
	started := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	return ModelExecutionReceipt{
		Operation: ModelOperationPlanReads, Provider: "openai", Model: "gpt-test", ModelVersion: "gpt-test",
		PromptVersion: "plan-reads-v1", SchemaVersion: "plan-reads-output-v1", EvaluatorVersion: "eval-v1",
		StartedAt: started, CompletedAt: started.Add(time.Second), Attempts: 1,
		InputDigest: strings.Repeat("a", 64), Outcome: outcome,
	}
}

type scriptedReadPlanningRuntime struct {
	plan    ReadPlan
	receipt ModelExecutionReceipt
	err     error
	calls   int
}

func (s *scriptedReadPlanningRuntime) PlanReads(context.Context, storage.Principal, ReadPlanningInput) (ReadPlan, ModelExecutionReceipt, error) {
	s.calls++
	return s.plan, s.receipt, s.err
}

type recordingReceipts struct{ recorded []ModelExecutionReceipt }

func (s *recordingReceipts) RecordModelExecution(_ context.Context, _ storage.Principal, receipt ModelExecutionReceipt) error {
	s.recorded = append(s.recorded, receipt)
	return nil
}

func TestModelExecutionReceiptValidateAdmitsPlanReads(t *testing.T) {
	t.Parallel()
	if err := planReadsReceipt("success").Validate(); err != nil {
		t.Fatalf("a plan_reads receipt is refused: %v", err)
	}
	unknown := planReadsReceipt("success")
	unknown.Operation = "plan_tools"
	if err := unknown.Validate(); err == nil {
		t.Fatal("an operation outside the vocabulary validated")
	}
}

// TestRuntimeReadPlannerRecordsEveryAttemptedCall: a receipt reaches the sink
// for a valid plan, an invalid plan and a failed call alike; only the valid
// plan yields kinds.
func TestRuntimeReadPlannerRecordsEveryAttemptedCall(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1"}
	input := ReadPlanningInput{
		Question: "q", Reason: ReadFallbackReasonJudgmentUnservable, MemberKind: SubjectTeam,
		OfferedKinds: ReadPlanOfferedKinds(readFallbackTestCapabilities(), SubjectTeam),
		Capabilities: readFallbackTestCapabilities(), RequestID: "req_1",
	}
	for _, tc := range []struct {
		name          string
		runtime       *scriptedReadPlanningRuntime
		wantKinds     []FactKind
		wantRejection ReadPlanRejection
		wantErr       error
		wantClass     string
	}{
		{
			name:      "valid plan",
			runtime:   &scriptedReadPlanningRuntime{plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics)}}, receipt: planReadsReceipt("success")},
			wantKinds: []FactKind{FactMetrics},
		},
		{
			name:          "invalid plan",
			runtime:       &scriptedReadPlanningRuntime{plan: ReadPlan{Steps: []ReadPlanStep{readsStep(FactMetrics), readsStep(FactFileHotspots)}}, receipt: planReadsReceipt("success")},
			wantRejection: ReadPlanRejectionKindNotOffered,
		},
		{
			name:      "failed call",
			runtime:   &scriptedReadPlanningRuntime{receipt: planReadsReceipt("invalid_output"), err: ErrModelOutput},
			wantErr:   ErrModelOutput,
			wantClass: "error_class=model_output",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &recordingReceipts{}
			var logs bytes.Buffer
			planner := RuntimeReadPlanner{Runtime: tc.runtime, Sink: sink, Logger: slog.New(slog.NewTextHandler(&logs, nil))}
			result := planner.PlanReads(context.Background(), principal, input)
			if tc.runtime.calls != 1 {
				t.Fatalf("runtime calls = %d, want 1", tc.runtime.calls)
			}
			if len(sink.recorded) != 1 || sink.recorded[0].Operation != ModelOperationPlanReads {
				t.Fatalf("recorded receipts = %+v, want one plan_reads receipt", sink.recorded)
			}
			if !slices.Equal(result.Kinds, tc.wantKinds) || result.Rejection != tc.wantRejection || !errors.Is(result.Err, tc.wantErr) || (tc.wantErr == nil && result.Err != nil) {
				t.Fatalf("result = %+v, want kinds %v rejection %q err %v", result, tc.wantKinds, tc.wantRejection, tc.wantErr)
			}
			line := logs.String()
			if !strings.Contains(line, "context fabric read planner decision") || !strings.Contains(line, "request_id=req_1") || !strings.Contains(line, "org_id=org_1") {
				t.Fatalf("decision line missing or unattributed:\n%s", line)
			}
			if tc.wantClass != "" && !strings.Contains(line, tc.wantClass) {
				t.Fatalf("decision line does not name %q:\n%s", tc.wantClass, line)
			}
			if tc.wantRejection != "" && !strings.Contains(line, "rejection="+string(tc.wantRejection)) {
				t.Fatalf("decision line does not name the rejection:\n%s", line)
			}
			if tc.wantRejection != "" {
				for _, want := range []string{"rejected_step=1", "rejected_tool=read_facts", "rejected_fields=kinds"} {
					if !strings.Contains(line, want) {
						t.Fatalf("decision line does not carry %q:\n%s", want, line)
					}
				}
				if strings.Contains(line, string(FactFileHotspots)) {
					t.Fatalf("decision line echoes a kind the model wrote:\n%s", line)
				}
			}
		})
	}
}

func TestRuntimeReadPlannerWithNoRuntimeFailsWithoutAReceipt(t *testing.T) {
	t.Parallel()
	sink := &recordingReceipts{}
	var logs bytes.Buffer
	result := RuntimeReadPlanner{Sink: sink, Logger: slog.New(slog.NewTextHandler(&logs, nil))}.PlanReads(context.Background(), storage.Principal{OrgID: "org_1"}, ReadPlanningInput{RequestID: "req_1"})
	if !errors.Is(result.Err, ErrModelUnavailable) || len(result.Kinds) != 0 || len(sink.recorded) != 0 {
		t.Fatalf("result = %+v recorded = %d, want ErrModelUnavailable and no receipt", result, len(sink.recorded))
	}
	if !strings.Contains(logs.String(), "error_class=no_runtime") {
		t.Fatalf("no-runtime pass did not log its class:\n%s", logs.String())
	}
}

func TestReadPlannerErrorClassIsClosed(t *testing.T) {
	t.Parallel()
	vocabulary := ReadPlannerErrorClassVocabulary()
	for _, err := range []error{
		ErrPlatformModelWithheld, context.DeadlineExceeded, ErrModelTimeout, context.Canceled, ErrModelCancelled,
		ErrModelRateLimited, ErrModelInputTooLarge, ErrModelOutput, ErrModelUnavailable, errors.New("other"),
		errors.Join(ErrModelUnavailable, ErrPlatformModelWithheld),
	} {
		if class := readPlannerErrorClass(err); !slices.Contains(vocabulary, class) {
			t.Fatalf("readPlannerErrorClass(%v) = %q, outside the vocabulary", err, class)
		}
	}
	if got := readPlannerErrorClass(errors.Join(ErrModelUnavailable, ErrPlatformModelWithheld)); got != "platform_model_withheld" {
		t.Fatalf("a withheld platform model classifies as %q", got)
	}
}
