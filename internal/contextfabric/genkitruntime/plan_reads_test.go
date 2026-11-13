package genkitruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

const planReadsTestModel = "gpt-5.6-luna"

func planReadsRuntime(t *testing.T, logs *bytes.Buffer, clients LLMClients, planReadsModel string) *Runtime {
	t.Helper()
	config := Config{
		Provider: "openai", Model: planReadsTestModel, PlanReadsModel: planReadsModel,
		InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
		SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Second,
		MaxAttempts: 3, MaxInputBytes: 128 << 10,
	}
	if logs != nil {
		config.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	runtime, err := NewWithLLM(config, clients)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func planReadsTestInput() contextfabric.ReadPlanningInput {
	return contextfabric.ReadPlanningInput{
		Question: "What team has the highest cycle time and appears to be stuck the most?",
		Interpretation: contextfabric.InterpretedQuestion{
			RequestedJudgment: "highest cycle time", RequestedJudgmentKind: contextfabric.RequestedJudgmentKindPerformance,
			FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}},
		},
		Reason:       contextfabric.ReadFallbackReasonJudgmentUnservable,
		MemberKind:   contextfabric.SubjectTeam,
		OfferedKinds: []contextfabric.FactKind{contextfabric.FactBlockers, contextfabric.FactMetrics, contextfabric.FactFlow},
		RequestID:    "request_12345678",
	}
}

func planReadsReply(t *testing.T, output planReadsOutput) llmReply {
	t.Helper()
	return llmReply{result: llm.Result{Text: jsonOf(t, output), Model: planReadsTestModel}}
}

// TestPlanReadsContractsAreBoundToTheirContent binds the planner's prompt and
// output schema to digests, as TestVersionedModelContractsAreBoundToTheirContent
// does for the other calls: bump the version, then the digest.
func TestPlanReadsContractsAreBoundToTheirContent(t *testing.T) {
	t.Parallel()
	schema, err := PlanReadsOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range []struct {
		name, version, content, digest string
	}{
		{name: "plan reads system prompt", version: DefaultPlanReadsPromptVersion, content: planReadsSystemPrompt, digest: "edddcb887d0e4fdfedc1514eff98d56e08338d537b47b9577cf2cb73c20ecbc9"},
		{name: "plan reads output schema", version: DefaultPlanReadsSchemaVersion, content: string(schema), digest: "9de242e6c1b67cde3966f3c0a56dbd4b35f02f79dd9df63e0d79a199660225d1"},
	} {
		sum := sha256.Sum256([]byte(binding.content))
		if got := hex.EncodeToString(sum[:]); got != binding.digest {
			t.Errorf("the %s changed but its pinned digest did not (version %s): pinned %s, actual %s. Bump the version, then the digest.", binding.name, binding.version, binding.digest, got)
		}
	}
}

func TestPlanReadsReturnsTheModelsPlanWithItsReceipt(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: planReadsTestModel, replies: []llmReply{planReadsReply(t, planReadsOutput{Steps: []planReadsStepOutput{
		{Tool: " read_facts ", Kinds: []string{"metrics", " flow"}},
		{Tool: "find_subjects", Kinds: []string{}, SubjectKind: "team"},
	}})}}
	var logs bytes.Buffer
	runtime := planReadsRuntime(t, &logs, LLMClients{planReadsTestModel: client}, "")
	plan, receipt, err := runtime.PlanReads(context.Background(), storage.Principal{OrgID: "org_1"}, planReadsTestInput())
	if err != nil {
		t.Fatalf("PlanReads() error = %v", err)
	}
	want := contextfabric.ReadPlan{Steps: []contextfabric.ReadPlanStep{
		{Tool: contextfabric.ReadPlanToolReadFacts, Kinds: []contextfabric.FactKind{contextfabric.FactMetrics, contextfabric.FactFlow}},
		{Tool: contextfabric.ReadPlanToolFindSubjects, SubjectKind: contextfabric.SubjectTeam},
	}}
	if len(plan.Steps) != len(want.Steps) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
	for i := range want.Steps {
		if plan.Steps[i].Tool != want.Steps[i].Tool || plan.Steps[i].SubjectKind != want.Steps[i].SubjectKind || !slices.Equal(plan.Steps[i].Kinds, want.Steps[i].Kinds) {
			t.Fatalf("step %d = %+v, want %+v", i, plan.Steps[i], want.Steps[i])
		}
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt invalid: %v", err)
	}
	if receipt.Operation != contextfabric.ModelOperationPlanReads || receipt.Outcome != "success" || receipt.Model != planReadsTestModel ||
		receipt.PromptVersion != DefaultPlanReadsPromptVersion || receipt.SchemaVersion != DefaultPlanReadsSchemaVersion ||
		receipt.RequestID != "request_12345678" || receipt.Attempts != 1 || receipt.OutputDigest == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(client.requests) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(client.requests))
	}
	request := client.request(0)
	if !strings.HasPrefix(request.System, planReadsSystemPrompt) || !strings.Contains(request.System, `"steps"`) {
		t.Fatal("the call did not send the planner's own system prompt and output schema")
	}
	var sent planReadsInput
	if err := json.Unmarshal([]byte(request.Prompt), &sent); err != nil {
		t.Fatalf("prompt is not the planner payload: %v", err)
	}
	if sent.Reason != "judgment_unservable" || sent.MemberKind != "team" || sent.Interpretation.RequestedJudgmentKind != "performance" ||
		!slices.Equal(sent.Interpretation.FactKinds, []string{"blockers"}) || len(sent.OfferedKinds) != 3 {
		t.Fatalf("payload = %+v", sent)
	}
	for _, offered := range sent.OfferedKinds {
		if offered.Holds == "" || offered.Holds != factKindHolds[contextfabric.FactKind(offered.Kind)] {
			t.Fatalf("offered kind %q carries %q, want its glossary line", offered.Kind, offered.Holds)
		}
	}
	if strings.Contains(request.Prompt, "capabilities") {
		t.Fatal("the payload carries the capabilities the plan is validated against")
	}
	if !strings.Contains(logs.String(), "operation=plan_reads") || !strings.Contains(logs.String(), "outcome=success") {
		t.Fatalf("decision line missing:\n%s", logs.String())
	}
}

func TestPlanReadsRunsOnItsOwnModel(t *testing.T) {
	t.Parallel()
	const planner = "gpt-5.6-sol"
	primary := &scriptedLLM{model: planReadsTestModel, replies: []llmReply{planReadsReply(t, planReadsOutput{})}}
	own := &scriptedLLM{model: planner, replies: []llmReply{{result: llm.Result{Text: jsonOf(t, planReadsOutput{Steps: []planReadsStepOutput{{Tool: "read_facts", Kinds: []string{"metrics"}}}}), Model: planner}}}}
	runtime := planReadsRuntime(t, nil, LLMClients{planReadsTestModel: primary, planner: own}, planner)
	_, receipt, err := runtime.PlanReads(context.Background(), storage.Principal{OrgID: "org_1"}, planReadsTestInput())
	if err != nil {
		t.Fatalf("PlanReads() error = %v", err)
	}
	if len(own.requests) != 1 || len(primary.requests) != 0 || receipt.Model != planner || receipt.ModelVersion != planner {
		t.Fatalf("planner calls = %d primary calls = %d receipt model = %q version = %q", len(own.requests), len(primary.requests), receipt.Model, receipt.ModelVersion)
	}
	if _, err := NewWithLLM(Config{Provider: "openai", Model: planReadsTestModel, PlanReadsModel: planner, Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 1 << 10}, LLMClients{planReadsTestModel: primary}); err == nil {
		t.Fatal("a runtime whose planner model has no client was built")
	}
}

func TestPlanReadsRefusalIsTerminal(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: planReadsTestModel, replies: []llmReply{{result: llm.Result{Text: "", StopReason: "refusal", Model: planReadsTestModel, Usage: llm.Usage{InputTokens: intp(900), OutputTokens: intp(0)}}}}}
	runtime := planReadsRuntime(t, nil, LLMClients{planReadsTestModel: client}, "")
	_, receipt, err := runtime.PlanReads(context.Background(), storage.Principal{OrgID: "org_1"}, planReadsTestInput())
	if !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("err = %v, want ErrModelOutput", err)
	}
	if len(client.requests) != 1 || receipt.Attempts != 1 || receipt.Operation != contextfabric.ModelOperationPlanReads || receipt.Usage.InputTokens != 900 {
		t.Fatalf("provider calls = %d receipt = %+v, want one call and a plan_reads receipt", len(client.requests), receipt)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("a refused call's receipt is invalid: %v", err)
	}
}

func TestPlanReadsWithNothingToOfferMakesNoCall(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: planReadsTestModel, replies: []llmReply{planReadsReply(t, planReadsOutput{})}}
	var logs bytes.Buffer
	runtime := planReadsRuntime(t, &logs, LLMClients{planReadsTestModel: client}, "")
	input := planReadsTestInput()
	input.OfferedKinds = nil
	if _, receipt, err := runtime.PlanReads(context.Background(), storage.Principal{OrgID: "org_1"}, input); err == nil || receipt.Operation != "" || len(client.requests) != 0 {
		t.Fatalf("err = %v receipt = %+v calls = %d, want a refusal before any call", err, receipt, len(client.requests))
	}
	if !strings.Contains(logs.String(), "operation=\"\"") && !strings.Contains(logs.String(), "operation= ") {
		t.Fatalf("a call that returned before the model was reached wrote no decision line:\n%s", logs.String())
	}
}

func TestEveryFactKindHasAGlossaryLine(t *testing.T) {
	t.Parallel()
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		if factKindHolds[kind] == "" {
			t.Errorf("fact kind %q has no glossary line for the planner", kind)
		}
	}
}
