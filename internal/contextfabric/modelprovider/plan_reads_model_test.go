package modelprovider

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

// TestPlanReadsModelDefaultsToTheInterpretModel: the read planner runs on
// PlanReadsModel when one is set and on Model otherwise, on the primary
// runtime only.
func TestPlanReadsModelDefaultsToTheInterpretModel(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "primary-model", FallbackModel: "fallback-model", PlanReadsModel: "planner-model"}
	if got := runtimeConfigWithPhrasing(cfg, cfg.Model, nil).PlanReadsModel; got != "planner-model" {
		t.Fatalf("primary PlanReadsModel = %q, want planner-model", got)
	}
	if got := fallbackRuntimeConfig(cfg).PlanReadsModel; got != "" {
		t.Fatalf("fallback PlanReadsModel = %q, want none", got)
	}
	for _, tc := range []struct {
		name      string
		planner   string
		wantModel string
	}{
		{name: "unset", wantModel: DefaultModel},
		{name: "set", planner: "gpt-5.6-sol", wantModel: "gpt-5.6-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &bodyRecorder{}
			runtime, err := New(context.Background(), Config{
				Provider: DefaultProvider, BaseURL: recorder.server(t, `{"steps":[{"tool":"read_facts","kinds":["metrics"],"subject_kind":""}]}`),
				Model: DefaultModel, PlanReadsModel: tc.planner, APIKey: llm.NewSecret("sk-configured"),
				Timeout: 10 * time.Second, MaxAttempts: 1, AllowInsecureBaseURL: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			planner, ok := runtime.(contextfabric.ReadPlanningModelRuntime)
			if !ok {
				t.Fatalf("runtime %T does not plan reads", runtime)
			}
			_, receipt, err := planner.PlanReads(context.Background(), storage.Principal{OrgID: "org_test"}, contextfabric.ReadPlanningInput{
				Question: "q", MemberKind: contextfabric.SubjectTeam, OfferedKinds: []contextfabric.FactKind{contextfabric.FactMetrics}, RequestID: "request_12345678",
			})
			if err != nil {
				t.Fatalf("PlanReads() error = %v", err)
			}
			if sent := recorder.body(t, 0)["model"]; sent != tc.wantModel || receipt.Model != tc.wantModel {
				t.Fatalf("request model %v receipt model %q, want %q", sent, receipt.Model, tc.wantModel)
			}
		})
	}
}

func TestPlanReadsModelIsValidated(t *testing.T) {
	cfg := Config{Provider: DefaultProvider, Model: DefaultModel, PlanReadsModel: "/gpt-5.6-sol", APIKey: llm.NewSecret("sk-configured"), Timeout: DefaultTimeout, MaxAttempts: 1}
	if err := cfg.validate(); err == nil {
		t.Fatal("a malformed PlanReadsModel validated")
	}
	cfg.PlanReadsModel = "gpt-5.6-sol"
	if err := cfg.validate(); err != nil {
		t.Fatalf("a well-formed PlanReadsModel is refused: %v", err)
	}
}
