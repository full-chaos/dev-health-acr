package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type planReadsKind struct {
	Kind  string `json:"kind"`
	Holds string `json:"holds"`
}

type planReadsInterpretation struct {
	RequestedJudgment     string   `json:"requested_judgment"`
	RequestedJudgmentKind string   `json:"requested_judgment_kind,omitempty"`
	FactKinds             []string `json:"fact_kinds"`
}

type planReadsInput struct {
	Question       string                  `json:"question"`
	Interpretation planReadsInterpretation `json:"interpretation"`
	Reason         string                  `json:"reason"`
	MemberKind     string                  `json:"member_kind"`
	OfferedKinds   []planReadsKind         `json:"offered_kinds"`
}

// planReadsStepOutput is one raw, unguarded step; contextfabric.ValidateReadPlan
// is the guard.
type planReadsStepOutput struct {
	Tool        string   `json:"tool"`
	Kinds       []string `json:"kinds"`
	SubjectKind string   `json:"subject_kind"`
}

type planReadsOutput struct {
	Steps []planReadsStepOutput `json:"steps"`
}

// PlanReadsOutputSchema is the inferred output schema the planner call sends.
func PlanReadsOutputSchema() ([]byte, error) {
	return json.Marshal(inferSchemaMap(planReadsOutput{}))
}

// factKindHolds maps each kind to its line of the interpretation prompt's
// fact-kind glossary, so the planner reads the same meaning the interpreter
// does.
var factKindHolds = func() map[contextfabric.FactKind]string {
	holds := map[contextfabric.FactKind]string{}
	for _, line := range strings.Split(interpretprompt.FactKindGlossary, "\n") {
		rest, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		kind, meaning, ok := strings.Cut(rest, ": ")
		if !ok {
			continue
		}
		holds[contextfabric.FactKind(kind)] = meaning
	}
	return holds
}()

func planReadsPayload(input contextfabric.ReadPlanningInput) planReadsInput {
	payload := planReadsInput{
		Question: input.Question,
		Interpretation: planReadsInterpretation{
			RequestedJudgment:     input.Interpretation.RequestedJudgment,
			RequestedJudgmentKind: string(input.Interpretation.RequestedJudgmentKind),
			FactKinds:             make([]string, 0, len(input.Interpretation.FactRequirements)),
		},
		Reason:       string(input.Reason),
		MemberKind:   string(input.MemberKind),
		OfferedKinds: make([]planReadsKind, 0, len(input.OfferedKinds)),
	}
	for _, requirement := range input.Interpretation.FactRequirements {
		payload.Interpretation.FactKinds = append(payload.Interpretation.FactKinds, string(requirement.Kind))
	}
	for _, kind := range input.OfferedKinds {
		payload.OfferedKinds = append(payload.OfferedKinds, planReadsKind{Kind: string(kind), Holds: factKindHolds[kind]})
	}
	return payload
}

func (o planReadsOutput) toDomain() contextfabric.ReadPlan {
	plan := contextfabric.ReadPlan{Steps: make([]contextfabric.ReadPlanStep, 0, len(o.Steps))}
	for _, step := range o.Steps {
		domain := contextfabric.ReadPlanStep{
			Tool:        contextfabric.ReadPlanTool(strings.TrimSpace(step.Tool)),
			SubjectKind: contextfabric.SubjectKind(strings.TrimSpace(step.SubjectKind)),
		}
		for _, kind := range step.Kinds {
			domain.Kinds = append(domain.Kinds, contextfabric.FactKind(strings.TrimSpace(kind)))
		}
		plan.Steps = append(plan.Steps, domain)
	}
	return plan
}

// PlanReads implements contextfabric.ReadPlanningModelRuntime: one bounded
// structured-output call with no fallback model, its receipt reflecting the
// attempt and never the validator's later verdict. A refusal stop reason is a
// terminal outcome of its one draw, as on every other call of this runtime.
func (r *Runtime) PlanReads(ctx context.Context, principal storage.Principal, input contextfabric.ReadPlanningInput) (contextfabric.ReadPlan, contextfabric.ModelExecutionReceipt, error) {
	var (
		receipt         contextfabric.ModelExecutionReceipt
		attemptOutcomes []attemptOutcome
	)
	defer func() {
		r.logPlanReadsDecision(ctx, principal.OrgID, input.RequestID, receipt, attemptOutcomes)
	}()
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.ReadPlan{}, contextfabric.ModelExecutionReceipt{}, errors.New("authenticated organization is required")
	}
	if len(input.OfferedKinds) == 0 {
		return contextfabric.ReadPlan{}, contextfabric.ModelExecutionReceipt{}, errors.New("read planning requires at least one offered kind")
	}
	encoded, err := boundedJSON(planReadsPayload(input), r.config.MaxInputBytes)
	if err != nil {
		return contextfabric.ReadPlan{}, contextfabric.ModelExecutionReceipt{}, err
	}
	started := r.now().UTC()
	var output planReadsOutput
	var usage contextfabric.ModelUsage
	var generationErr error
	attemptOutcomes, generationErr = r.withRetryFor(ctx, false, func(callCtx context.Context) error {
		var err error
		output, usage, err = r.generator.PlanReads(callCtx, generationRequest{
			Model: r.config.PlanReadsModelRef, System: planReadsSystemPrompt, Prompt: string(encoded),
		})
		return err
	})
	completed := r.now().UTC()
	var classifiedErr error
	if generationErr != nil {
		classifiedErr = classifyModelError(generationErr)
	}
	receipt = r.receipt(contextfabric.ModelOperationPlanReads, r.config.PlanReadsPromptVersion, started, completed, len(attemptOutcomes), encoded, nil, usage, classifiedErr)
	receipt.Model = r.config.PlanReadsModel
	receipt.ModelVersion = r.config.PlanReadsModelVersion
	receipt.SchemaVersion = DefaultPlanReadsSchemaVersion
	receipt.RequestID = input.RequestID
	if generationErr != nil {
		return contextfabric.ReadPlan{}, receipt, classifiedErr
	}
	outputBytes, _ := json.Marshal(output)
	receipt.OutputDigest = contextfabric.DigestModelValue(outputBytes)
	receipt.Outcome = "success"
	return output.toDomain(), receipt, nil
}

func (r *Runtime) logPlanReadsDecision(ctx context.Context, orgID, requestID string, receipt contextfabric.ModelExecutionReceipt, attemptOutcomes []attemptOutcome) {
	fields := eventspec.NewModelPlanReadsDecisionFields(
		requestID, decisionOrgIDHash(orgID), string(receipt.Operation), receipt.Outcome, receipt.Attempts,
		receipt.Model, receipt.ModelVersion, receipt.PromptVersion,
		len(attemptOutcomes), attemptsRetried(attemptOutcomes), formatAttemptOutcomes(attemptOutcomes), formatAttemptElapsed(attemptOutcomes),
	).SlogArgs()
	r.config.Logger.InfoContext(ctx, decisionEventMessage, fields...)
}
