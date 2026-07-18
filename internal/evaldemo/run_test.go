package evaldemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
)

type evaluationSample struct {
	SchemaVersion string  `json:"schema_version"`
	Metrics       Metrics `json:"metrics"`
}

func TestRun_produces_reproducible_fixture_comparison(t *testing.T) {
	// Given
	config := Config{CorpusDir: corpusDir(t), Repeat: 2}

	// When
	result, err := Run(context.Background(), config)

	// Then
	if err != nil {
		t.Fatalf("run demo: %v", err)
	}
	if result.Result != ResultComplete || result.Metrics == nil {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Repeats) != 2 {
		t.Fatalf("repeat count = %d, want 2", len(result.Repeats))
	}
	if !reflect.DeepEqual(result.Repeats[0].Tasks, result.Repeats[1].Tasks) {
		t.Fatalf("repeat task results differ:\nfirst=%#v\nsecond=%#v", result.Repeats[0].Tasks, result.Repeats[1].Tasks)
	}
	if len(result.Repeats[0].Tasks) != 3 {
		t.Fatalf("task count = %d, want 3", len(result.Repeats[0].Tasks))
	}
	for _, task := range result.Repeats[0].Tasks {
		if task.InputHash == "" || task.Context.ContextPacketID == "" {
			t.Fatalf("task is not traceable: %#v", task)
		}
	}
	if got, want := result.Metrics.Context.CitationPrecision.Value, 1.0; got == nil || *got != want {
		t.Fatalf("context citation precision = %#v, want %f", got, want)
	}
	if result.Metrics.Context.FileTestRecall.Value != nil {
		t.Fatalf("file/test recall = %#v, want not applicable for corpus without file/test identifiers", result.Metrics.Context.FileTestRecall)
	}
}

func TestRun_invalidates_fixture_failure_scenarios_without_metrics(t *testing.T) {
	tests := []struct {
		name     string
		scenario Scenario
	}{
		{name: "corrupt hash", scenario: ScenarioCorruptHash},
		{name: "empty evidence", scenario: ScenarioEmptyEvidence},
		{name: "mismatched task", scenario: ScenarioMismatchedTask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			config := Config{CorpusDir: corpusDir(t), Repeat: 1, Scenario: tt.scenario}

			// When
			result, err := Run(context.Background(), config)

			// Then
			if !errors.Is(err, ErrInvalidated) {
				t.Fatalf("error = %v, want ErrInvalidated", err)
			}
			if result.Result != ResultInvalidated || result.Metrics != nil {
				t.Fatalf("invalidated result must not contain metrics: %#v", result)
			}
		})
	}
}

func TestTaskRequest_uses_branch_scope_without_task_reference(t *testing.T) {
	// Given
	task := evalfixture.Task{TaskID: "task-branch", Scope: evalfixture.TaskScope{Branch: "main"}}

	// When
	request := taskRequest(task, ScenarioDefault, 1)

	// Then
	if request.Scope.TaskRef != "" || request.Scope.Branch != "main" {
		t.Fatalf("branch-scoped request = %#v", request.Scope)
	}
}

func TestCalculateMetrics_scores_candidate_evidence_against_fixture_labels(t *testing.T) {
	// Given
	tasks := []TaskRun{{
		ExpectedEvidenceIDs: []string{"ev-expected"},
		Context:             ContextRun{Citations: []Citation{{EvidenceRefID: "ev-unexpected"}}},
	}}

	// When
	metrics := calculateMetrics(tasks)

	// Then
	if got := *metrics.Context.FactualErrorRate.Value; got != 1 {
		t.Fatalf("factual error rate = %f, want 1", got)
	}
	if got := *metrics.Context.CitationPrecision.Value; got != 0 {
		t.Fatalf("citation precision = %f, want 0", got)
	}
	if got := *metrics.Context.IrrelevantItemRate.Value; got != 1 {
		t.Fatalf("irrelevant item rate = %f, want 1", got)
	}
}

func TestRun_matches_committed_cross_surface_sample(t *testing.T) {
	// Given
	result, err := Run(context.Background(), Config{CorpusDir: corpusDir(t), Repeat: 1})
	if err != nil {
		t.Fatalf("run demo: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "evaluation_demo.v1.json"))
	if err != nil {
		t.Fatalf("read cross-surface sample: %v", err)
	}
	var sample evaluationSample
	if err := json.Unmarshal(data, &sample); err != nil {
		t.Fatalf("decode cross-surface sample: %v", err)
	}
	if sample.SchemaVersion != "evaluation_demo.v1" {
		t.Fatalf("sample schema version = %q", sample.SchemaVersion)
	}

	// When
	got, err := json.Marshal(result.Metrics)
	if err != nil {
		t.Fatalf("marshal demo metrics: %v", err)
	}
	want, err := json.Marshal(sample.Metrics)
	if err != nil {
		t.Fatalf("marshal sample metrics: %v", err)
	}

	// Then
	if string(got) != string(want) {
		t.Fatalf("cross-surface metrics drift:\ngot:  %s\nwant: %s", got, want)
	}
}

func corpusDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "testdata", "evaluation", "v1")
}
