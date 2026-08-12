package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core"
	"github.com/firebase/genkit/go/genkit"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const SDKVersion = "v1.11.0"

const (
	defaultInterpretationPromptVersion = "context-fabric-interpretation.v1"
	defaultSynthesisPromptVersion      = "context-fabric-synthesis.v1"
	defaultSchemaVersion               = "context-fabric-model-output.v1"
	defaultEvaluatorVersion            = "context-fabric-grounding.v1"
)

type Config struct {
	Genkit                      *genkit.Genkit
	Provider                    string
	Model                       string
	ModelVersion                string
	InterpretationPromptVersion string
	SynthesisPromptVersion      string
	SchemaVersion               string
	EvaluatorVersion            string
	Timeout                     time.Duration
	MaxAttempts                 int
	MaxInputBytes               int
	Fallback                    contextfabric.ModelRuntime
}

type Runtime struct {
	generator generator
	config    Config
	now       func() time.Time
}

type generator interface {
	Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error)
	Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error)
}

type generationRequest struct {
	Model  string
	System string
	Prompt string
}

type sdkGenerator struct {
	genkit *genkit.Genkit
}

func (g sdkGenerator) Interpret(ctx context.Context, request generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	output, response, err := genkit.GenerateData[interpretationOutput](ctx, g.genkit,
		ai.WithModelName(request.Model),
		ai.WithSystem(request.System),
		ai.WithPrompt("%s", request.Prompt),
		ai.WithCustomConstrainedOutput(),
	)
	if err != nil {
		return interpretationOutput{}, contextfabric.ModelUsage{}, err
	}
	if output == nil {
		return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("genkit returned no interpretation output")
	}
	return *output, modelUsage(response), nil
}

func (g sdkGenerator) Synthesize(ctx context.Context, request generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	output, response, err := genkit.GenerateData[synthesisOutput](ctx, g.genkit,
		ai.WithModelName(request.Model),
		ai.WithSystem(request.System),
		ai.WithPrompt("%s", request.Prompt),
		ai.WithCustomConstrainedOutput(),
	)
	if err != nil {
		return synthesisOutput{}, contextfabric.ModelUsage{}, err
	}
	if output == nil {
		return synthesisOutput{}, contextfabric.ModelUsage{}, errors.New("genkit returned no synthesis output")
	}
	return *output, modelUsage(response), nil
}

func modelUsage(response *ai.ModelResponse) contextfabric.ModelUsage {
	if response == nil || response.Usage == nil {
		return contextfabric.ModelUsage{}
	}
	return contextfabric.ModelUsage{
		InputTokens:  response.Usage.InputTokens,
		OutputTokens: response.Usage.OutputTokens,
		TotalTokens:  response.Usage.TotalTokens,
	}
}

func New(config Config) (*Runtime, error) {
	if config.Genkit == nil {
		return nil, errors.New("genkit instance is required")
	}
	return newWithGenerator(config, sdkGenerator{genkit: config.Genkit})
}

func newWithGenerator(config Config, gen generator) (*Runtime, error) {
	if gen == nil {
		return nil, errors.New("structured generator is required")
	}
	for name, value := range map[string]string{
		"provider": config.Provider,
		"model":    config.Model,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return nil, fmt.Errorf("%s is required and must be bounded", name)
		}
	}
	if strings.TrimSpace(config.ModelVersion) == "" {
		config.ModelVersion = config.Model
	}
	if strings.TrimSpace(config.InterpretationPromptVersion) == "" {
		config.InterpretationPromptVersion = defaultInterpretationPromptVersion
	}
	if strings.TrimSpace(config.SynthesisPromptVersion) == "" {
		config.SynthesisPromptVersion = defaultSynthesisPromptVersion
	}
	if strings.TrimSpace(config.SchemaVersion) == "" {
		config.SchemaVersion = defaultSchemaVersion
	}
	if strings.TrimSpace(config.EvaluatorVersion) == "" {
		config.EvaluatorVersion = defaultEvaluatorVersion
	}
	if config.Timeout <= 0 {
		config.Timeout = 45 * time.Second
	}
	if config.Timeout < time.Second || config.Timeout > 2*time.Minute {
		return nil, errors.New("model timeout must be between one second and two minutes")
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 2
	}
	if config.MaxAttempts < 1 || config.MaxAttempts > 3 {
		return nil, errors.New("model attempts must be between one and three")
	}
	if config.MaxInputBytes == 0 {
		config.MaxInputBytes = 512 << 10
	}
	if config.MaxInputBytes < 8<<10 || config.MaxInputBytes > 1<<20 {
		return nil, errors.New("model input bound must be between 8 KiB and 1 MiB")
	}
	return &Runtime{generator: gen, config: config, now: time.Now}, nil
}

func (r *Runtime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	if err := request.Validate(); err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("interpretation request: %w", err)
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, errors.New("authenticated organization is required")
	}
	payload := interpretationInput{
		Question:             request.Question,
		Conversation:         request.Conversation,
		SubjectHints:         request.RequestedScope.SubjectHints,
		RequestedScope:       request.RequestedScope,
		TimeContext:          request.TimeContext,
		PriorSubjectReceipts: request.PriorSubjectReceipts,
	}
	encoded, err := boundedJSON(payload, r.config.MaxInputBytes)
	if err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, err
	}
	started := r.now().UTC()
	var output interpretationOutput
	var usage contextfabric.ModelUsage
	attempts, generationErr := r.withRetry(ctx, func(callCtx context.Context) error {
		var err error
		output, usage, err = r.generator.Interpret(callCtx, generationRequest{
			Model: r.config.Model, System: interpretationSystemPrompt, Prompt: string(encoded),
		})
		return err
	})
	completed := r.now().UTC()
	receipt := r.receipt(contextfabric.ModelOperationInterpret, r.config.InterpretationPromptVersion, started, completed, attempts, encoded, nil, usage, generationErr)
	if generationErr != nil {
		if r.config.Fallback != nil {
			interpreted, fallbackReceipt, fallbackErr := r.config.Fallback.InterpretQuestion(ctx, principal, request)
			receipt.FallbackUsed = true
			receipt.Outcome = "fallback"
			if fallbackErr == nil {
				return interpreted, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
		}
		return contextfabric.InterpretedQuestion{}, receipt, classifyModelError(generationErr)
	}
	interpreted, err := output.toDomain(request.TimeContext)
	if err != nil {
		receipt.Outcome = "invalid_output"
		if r.config.Fallback != nil {
			fallback, fallbackReceipt, fallbackErr := r.config.Fallback.InterpretQuestion(ctx, principal, request)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				return fallback, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
		}
		return contextfabric.InterpretedQuestion{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelOutput, err)
	}
	outputBytes, _ := json.Marshal(output)
	receipt.OutputDigest = contextfabric.DigestModelValue(outputBytes)
	receipt.Outcome = "success"
	return interpreted, receipt, nil
}

func (r *Runtime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, errors.New("authenticated organization is required")
	}
	payload := synthesisInputFromDomain(input)
	encoded, err := boundedJSON(payload, r.config.MaxInputBytes)
	if err != nil {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, err
	}
	started := r.now().UTC()
	var output synthesisOutput
	var usage contextfabric.ModelUsage
	attempts, generationErr := r.withRetry(ctx, func(callCtx context.Context) error {
		var err error
		output, usage, err = r.generator.Synthesize(callCtx, generationRequest{
			Model: r.config.Model, System: synthesisSystemPrompt, Prompt: string(encoded),
		})
		return err
	})
	completed := r.now().UTC()
	receipt := r.receipt(contextfabric.ModelOperationSynthesize, r.config.SynthesisPromptVersion, started, completed, attempts, encoded, nil, usage, generationErr)
	if generationErr != nil {
		if r.config.Fallback != nil {
			draft, fallbackReceipt, fallbackErr := r.config.Fallback.SynthesizeAnswer(ctx, principal, input)
			receipt.FallbackUsed = true
			receipt.Outcome = "fallback"
			if fallbackErr == nil {
				return draft, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
		}
		return contextfabric.SynthesisDraft{}, receipt, classifyModelError(generationErr)
	}
	draft, err := output.toDomain()
	if err == nil {
		err = draft.ValidateAgainst(input)
	}
	if err != nil {
		receipt.Outcome = "invalid_output"
		if r.config.Fallback != nil {
			fallback, fallbackReceipt, fallbackErr := r.config.Fallback.SynthesizeAnswer(ctx, principal, input)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				return fallback, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
		}
		return contextfabric.SynthesisDraft{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelOutput, err)
	}
	outputBytes, _ := json.Marshal(output)
	receipt.OutputDigest = contextfabric.DigestModelValue(outputBytes)
	receipt.Outcome = "success"
	return draft, receipt, nil
}

func (r *Runtime) withRetry(ctx context.Context, fn func(context.Context) error) (int, error) {
	var last error
	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return attempt, err
		}
		callCtx, cancel := context.WithTimeout(ctx, r.config.Timeout)
		err := fn(callCtx)
		cancel()
		if err == nil {
			return attempt, nil
		}
		last = err
		if !retryable(err) || attempt == r.config.MaxAttempts {
			return attempt, err
		}
	}
	return r.config.MaxAttempts, last
}

func (r *Runtime) receipt(operation contextfabric.ModelOperation, promptVersion string, started, completed time.Time, attempts int, input, output []byte, usage contextfabric.ModelUsage, err error) contextfabric.ModelExecutionReceipt {
	outcome := "error"
	if err == nil {
		outcome = "pending_validation"
	}
	receipt := contextfabric.ModelExecutionReceipt{
		Operation: operation, Provider: r.config.Provider, Model: r.config.Model,
		ModelVersion: r.config.ModelVersion, PromptVersion: promptVersion,
		SchemaVersion: r.config.SchemaVersion, EvaluatorVersion: r.config.EvaluatorVersion,
		StartedAt: started, CompletedAt: completed, Attempts: attempts,
		InputDigest: contextfabric.DigestModelValue(input), Usage: usage, Outcome: outcome,
	}
	if len(output) > 0 {
		receipt.OutputDigest = contextfabric.DigestModelValue(output)
	}
	return receipt
}

func boundedJSON(value any, maximum int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode bounded model input: %w", err)
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("model input exceeds %d bytes", maximum)
	}
	return encoded, nil
}

func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Genkit (and well-behaved plugins) surface transport and provider
	// failures as *core.GenkitError with a gRPC-style status. Prefer that
	// structured signal over string sniffing when it is present.
	var genkitErr *core.GenkitError
	if errors.As(err, &genkitErr) {
		switch genkitErr.Status {
		case core.RESOURCE_EXHAUSTED, core.UNAVAILABLE, core.DEADLINE_EXCEEDED, core.ABORTED:
			return true
		case core.INVALID_ARGUMENT:
			// Malformed request or schema-incompatible output; the model is
			// not going to succeed against the same input, so this fails
			// closed instead of retrying (ADR 0008: invalid output fails
			// closed).
			return false
		}
	}
	lower := strings.ToLower(err.Error())
	for _, token := range []string{"429", "rate limit", "temporarily unavailable", "timeout", "deadline", "connection reset", "502", "503", "504"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// classifyModelError maps a raw generation error into one of the ACR-owned
// model runtime sentinels (ErrModelRateLimited, ErrModelOutput,
// ErrModelUnavailable) so callers can apply distinct handling and alerting
// per CHAOS-3756. Only the error class and provider status name are
// preserved in the wrapped message; the original error (which may carry
// provider response fragments) is intentionally dropped rather than
// wrapped, so raw prompt or response content never reaches logs, receipts,
// or telemetry built from this error.
func classifyModelError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var genkitErr *core.GenkitError
	if errors.As(err, &genkitErr) {
		switch genkitErr.Status {
		case core.RESOURCE_EXHAUSTED:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelRateLimited, genkitErr.Status)
		case core.INVALID_ARGUMENT:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelOutput, genkitErr.Status)
		case core.INTERNAL:
			// Genkit reports its own structured-output/schema mismatch as
			// INTERNAL (see ai.jsonHandler.ParseMessage), so INTERNAL is
			// ambiguous between a genuine provider fault and invalid model
			// output. Distinguish by the fixed prefix Genkit always uses for
			// the schema-mismatch case; never inspect or forward the rest of
			// the message, which may quote the malformed output.
			if strings.HasPrefix(genkitErr.Message, "model failed to generate output matching expected schema") {
				return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelOutput, genkitErr.Status)
			}
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelUnavailable, genkitErr.Status)
		case core.UNAVAILABLE, core.DEADLINE_EXCEEDED, core.ABORTED:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelUnavailable, genkitErr.Status)
		default:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelUnavailable, genkitErr.Status)
		}
	}
	lower := strings.ToLower(err.Error())
	for _, token := range []string{"429", "rate limit", "too many requests", "quota exceeded"} {
		if strings.Contains(lower, token) {
			return fmt.Errorf("%w: model generation failed", contextfabric.ErrModelRateLimited)
		}
	}
	return fmt.Errorf("%w: model generation failed", contextfabric.ErrModelUnavailable)
}

func mergeFallbackReceipt(primary, fallback contextfabric.ModelExecutionReceipt) contextfabric.ModelExecutionReceipt {
	primary.FallbackUsed = true
	if fallback.OutputDigest != "" {
		primary.OutputDigest = fallback.OutputDigest
	}
	primary.Usage.InputTokens += fallback.Usage.InputTokens
	primary.Usage.OutputTokens += fallback.Usage.OutputTokens
	primary.Usage.TotalTokens += fallback.Usage.TotalTokens
	return primary
}

type interpretationInput struct {
	Question             string                              `json:"question"`
	Conversation         []contextfabric.ConversationTurn    `json:"conversation,omitempty"`
	SubjectHints         []contextfabric.SubjectHint         `json:"subject_hints,omitempty"`
	RequestedScope       contextfabric.RequestedScope        `json:"requested_scope,omitempty"`
	TimeContext          contextfabric.TimeContext           `json:"time_context"`
	PriorSubjectReceipts []contextfabric.BoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
}

type interpretationOutput struct {
	Shape               string                  `json:"shape" jsonschema:"enum=single_subject,enum=explicit_cohort,enum=discovered_cohort,enum=open"`
	RequestedJudgment   string                  `json:"requested_judgment"`
	SubjectTerms        []string                `json:"subject_terms,omitempty"`
	ComparisonTerms     []string                `json:"comparison_terms,omitempty"`
	TimeContext         outputTimeContext       `json:"time_context"`
	FactRequirements    []factRequirementOutput `json:"fact_requirements"`
	ClarificationNeeded bool                    `json:"clarification_needed"`
	ClarificationReason string                  `json:"clarification_reason,omitempty"`
}

type outputTimeContext struct {
	Axis  string     `json:"axis" jsonschema:"enum=current,enum=valid_time,enum=observed_time,enum=range"`
	AsOf  *time.Time `json:"as_of,omitempty"`
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}

type factRequirementOutput struct {
	Kind       string            `json:"kind"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

func (o interpretationOutput) toDomain(defaultTime contextfabric.TimeContext) (contextfabric.InterpretedQuestion, error) {
	timeContext := contextfabric.TimeContext{Axis: contextfabric.TemporalAxis(o.TimeContext.Axis), AsOf: o.TimeContext.AsOf, Start: o.TimeContext.Start, End: o.TimeContext.End}
	if strings.TrimSpace(o.TimeContext.Axis) == "" {
		timeContext = defaultTime
	}
	requirements := make([]contextfabric.FactRequirement, 0, len(o.FactRequirements))
	seen := make(map[contextfabric.FactKind]struct{})
	for _, requirement := range o.FactRequirements {
		kind := contextfabric.FactKind(strings.TrimSpace(requirement.Kind))
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		requirements = append(requirements, contextfabric.FactRequirement{Kind: kind, Parameters: cloneStringMap(requirement.Parameters)})
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.InvestigationShape(strings.TrimSpace(o.Shape)), RequestedJudgment: strings.TrimSpace(o.RequestedJudgment),
		SubjectTerms: trimmedUnique(o.SubjectTerms), ComparisonTerms: trimmedUnique(o.ComparisonTerms),
		TimeContext: timeContext, FactRequirements: requirements,
		ClarificationNeeded: o.ClarificationNeeded, ClarificationReason: strings.TrimSpace(o.ClarificationReason),
	}
	if err := interpreted.Validate(); err != nil {
		return contextfabric.InterpretedQuestion{}, err
	}
	return interpreted, nil
}

type synthesisInput struct {
	Question         string                            `json:"question"`
	Interpretation   contextfabric.InterpretedQuestion `json:"interpretation"`
	Resolution       contextfabric.SubjectResolution   `json:"subject_resolution"`
	Cohort           *contextfabric.Cohort             `json:"cohort,omitempty"`
	Paths            []contextfabric.RelationshipPath  `json:"paths"`
	DriverCandidates []contextfabric.DriverJudgment    `json:"driver_candidates"`
	Facts            []contextfabric.CanonicalFact     `json:"canonical_facts"`
	Coverage         contextfabric.Coverage            `json:"coverage"`
}

func synthesisInputFromDomain(input contextfabric.SynthesisInput) synthesisInput {
	return synthesisInput{
		Question: input.Request.Question, Interpretation: input.Interpretation,
		Resolution: input.Graph.Resolution, Cohort: input.Graph.Cohort,
		Paths: input.Graph.Paths, DriverCandidates: input.Graph.DriverCandidates,
		Facts: input.Facts.Facts, Coverage: mergeCoverage(input.Graph.Coverage, input.Facts.Coverage),
	}
}

type synthesisOutput struct {
	Status              string                         `json:"status" jsonschema:"enum=complete,enum=partial,enum=degraded,enum=clarification_required,enum=no_match"`
	DirectJudgment      string                         `json:"direct_judgment"`
	CurrentState        string                         `json:"current_state"`
	StrongestPressures  []string                       `json:"strongest_pressures"`
	Drivers             []contextfabric.DriverJudgment `json:"drivers"`
	RemainingWork       []contextfabric.Finding        `json:"remaining_work"`
	ReadinessGaps       []contextfabric.Finding        `json:"readiness_gaps"`
	Conflicts           []contextfabric.Finding        `json:"conflicts"`
	Limitations         []string                       `json:"limitations"`
	EvidenceRefIDs      []string                       `json:"evidence_ref_ids"`
	DeterministicAnswer string                         `json:"deterministic_answer"`
	Warnings            []string                       `json:"warnings"`
}

func (o synthesisOutput) toDomain() (contextfabric.SynthesisDraft, error) {
	draft := contextfabric.SynthesisDraft{
		Status:         contextfabric.InvestigationStatus(strings.TrimSpace(o.Status)),
		DirectJudgment: strings.TrimSpace(o.DirectJudgment), CurrentState: strings.TrimSpace(o.CurrentState),
		StrongestPressures: trimmedUnique(o.StrongestPressures), Drivers: append([]contextfabric.DriverJudgment(nil), o.Drivers...),
		RemainingWork: append([]contextfabric.Finding(nil), o.RemainingWork...), ReadinessGaps: append([]contextfabric.Finding(nil), o.ReadinessGaps...),
		Conflicts: append([]contextfabric.Finding(nil), o.Conflicts...), Limitations: trimmedUnique(o.Limitations),
		EvidenceRefIDs: trimmedUnique(o.EvidenceRefIDs), DeterministicAnswer: strings.TrimSpace(o.DeterministicAnswer), Warnings: trimmedUnique(o.Warnings),
	}
	if strings.TrimSpace(draft.DeterministicAnswer) == "" {
		return contextfabric.SynthesisDraft{}, errors.New("deterministic answer is required")
	}
	return draft, nil
}

func trimmedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = values[key]
	}
	return result
}

func mergeCoverage(groups ...contextfabric.Coverage) contextfabric.Coverage {
	bySource := make(map[string]contextfabric.SourceObservation)
	reasons := make(map[string]struct{})
	partial := false
	for _, group := range groups {
		partial = partial || group.Partial
		for _, source := range group.Sources {
			bySource[source.Source] = source
		}
		for _, reason := range group.DegradedReasons {
			reasons[reason] = struct{}{}
		}
	}
	sources := make([]contextfabric.SourceObservation, 0, len(bySource))
	for _, source := range bySource {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Source < sources[j].Source })
	degraded := make([]string, 0, len(reasons))
	for reason := range reasons {
		degraded = append(degraded, reason)
	}
	sort.Strings(degraded)
	return contextfabric.Coverage{Sources: sources, Partial: partial || len(degraded) > 0, DegradedReasons: degraded}
}

var _ contextfabric.ModelRuntime = (*Runtime)(nil)
