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
	// defaultInterpretationPromptVersion is v3 as of CHAOS-3770's live
	// acceptance: v2 (CHAOS-3754) extended the interpretation system
	// prompt (prompts.go) to cover conversational-reference resolution,
	// alias/acronym/previous-name subject terms, and subjectless
	// team/project cohort framing; v3 closes the canonical fact-kind
	// vocabulary in the prompt itself. Against a real provider, v2
	// invented fact-kind names on ordinary questions
	// (InterpretedQuestion.Validate then rejected every interpretation
	// with "fact requirement violates v1 bounds"), because
	// factRequirementOutput.Kind deliberately carries NO jsonschema enum
	// -- the layering is that Genkit parses permissively and the
	// ACR-owned semantic validator owns the registry. Closing the
	// vocabulary in the prompt is the fix that keeps that layering,
	// mirroring what v3 of the synthesis prompt did for driver
	// categories. A receipt's PromptVersion is part of what a
	// replay/evaluation pipeline uses to interpret
	// ModelExecutionReceipt content (ADR 0008), so a prompt content
	// change must bump this even though the interpretationOutput schema
	// itself is unchanged.
	//
	// v4 is the same defect one layer over: the prompt stated no length or
	// count limit, but InterpretedQuestion.Validate caps
	// requested_judgment at 256 characters and rejects the interpretation
	// in full when it is longer. Measured live, that single omission was
	// the entire difference between a usable and an unusable model:
	// gpt-5-mini failed 5 of 12 investigations on it alone, every one with
	// a requested_judgment of 259-294 characters, because it writes a more
	// thorough judgment than gpt-5-nano and nothing told it not to. The
	// cap itself is a contracts/v1 bound and is deliberately NOT relaxed
	// here; v4 states it, along with the other bounds a model cannot
	// infer, and tells the model where that detail belongs instead
	// (fact_requirements).
	defaultInterpretationPromptVersion = "context-fabric-interpretation.v4"
	// defaultSynthesisPromptVersion is v3 as of CHAOS-3755's adversarial
	// review round: v2 added claimed_facts for value-level closure; v3
	// closes the driver category vocabulary (a fixed 16-value set, no
	// longer free text), requires a claimed fact's subject to be one the
	// citing driver/finding actually names, requires subject labels to
	// match the input verbatim, and marks direct_judgment/current_state/
	// deterministic_answer as advisory-only (ACR recomposes them
	// server-side and never returns the model's own text for them). v4 is
	// CHAOS-3770's live-acceptance fix, the synthesis counterpart of
	// interpretation v3 above: driver standing, derivation method,
	// epistemic status and claimed-fact kind are closed vocabularies with
	// no jsonschema enum on the shared contracts types, and driver_id /
	// finding_id / claim_id carry a minimum length and a
	// claimed_fact_ids-must-resolve rule that a model cannot infer. v3
	// stated none of them, so a real provider failed
	// SynthesisDraft.ValidateAgainst on ordinary inputs; v4 states them.
	//
	// v5 closes the class the first three fixes each patched one instance
	// of: a bound the validator enforces that the prompt never states. It
	// adds the length and count limits (title, summary, qualification,
	// affected_subjects, per-item and per-collection caps, identifier
	// upper bound) that v4 left unstated, and TestPromptsStateEveryModelFacingBound
	// aimed to make a fourth instance of the class unable to ship silently
	// -- but the table behind that test was itself hand-maintained and
	// incomplete (CHAOS-3770 F3 codex review): it never covered
	// warnings, strongest_pressures/drivers/remaining_work/readiness_gaps/
	// conflicts/limitations collection caps, or the top-level
	// evidence_ref_ids cap, so a bound could still be silently dropped
	// from the prompt without the test noticing. The oracle is now
	// mechanical (contracts/v1.ContextFabricModelFacingBounds, the single
	// source both this prompt's authors and the validator's numeric
	// literals must agree with -- see that file's doc comment), and
	// applying it here surfaced exactly the predicted class of gap: this
	// prompt's own text never stated the warnings bound at all, even
	// though ContextFabricInvestigationResult.Validate() has enforced it
	// (250 items, 4000 characters each) unchanged since v2. v6 adds that
	// one missing statement; nothing else in this prompt changes.
	defaultSynthesisPromptVersion = "context-fabric-synthesis.v6"
	defaultSchemaVersion          = "context-fabric-model-output.v1"
	defaultEvaluatorVersion       = "context-fabric-grounding.v1"
)

type Config struct {
	Genkit   *genkit.Genkit
	Provider string
	Model    string
	// ModelRef is the fully qualified Genkit action name used to select the
	// model at generation time -- "<plugin provider>/<model id>", e.g.
	// "openai/gpt-5-nano". Genkit resolves a model only by that namespaced
	// key (an unqualified name parses to an empty provider and matches no
	// plugin), while Provider and Model stay the plain, replay-facing
	// values recorded in every ModelExecutionReceipt. Defaults to Model
	// when empty, which is what a test double or an already-qualified
	// caller wants.
	ModelRef                    string
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
	if strings.TrimSpace(config.ModelRef) == "" {
		config.ModelRef = config.Model
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
			Model: r.config.ModelRef, System: interpretationSystemPrompt, Prompt: string(encoded),
		})
		return err
	})
	completed := r.now().UTC()
	var classifiedErr error
	if generationErr != nil {
		classifiedErr = classifyModelError(generationErr)
	}
	receipt := r.receipt(contextfabric.ModelOperationInterpret, r.config.InterpretationPromptVersion, started, completed, attempts, encoded, nil, usage, classifiedErr)
	if generationErr != nil {
		if r.config.Fallback != nil {
			interpreted, fallbackReceipt, fallbackErr := r.config.Fallback.InterpretQuestion(ctx, principal, request)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				return interpreted, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
			// Both the primary and the fallback failed (CHAOS-3770 F4): the
			// receipt must not claim FallbackUsed/Outcome="fallback", which
			// means the fallback produced usable output -- it did not -- and
			// the caller must see the FINAL (fallback) leg's own
			// classification, not the primary's. fallbackReceipt.Outcome is
			// already the fallback's own receiptOutcomeForError result (the
			// fallback is itself a ModelRuntime that builds and returns a
			// receipt on every call, success or failure), so reusing it here
			// keeps the outcome vocabulary consistent with a direct call to
			// that same fallback.
			receipt.Outcome = fallbackReceipt.Outcome
			return contextfabric.InterpretedQuestion{}, receipt, fallbackErr
		}
		return contextfabric.InterpretedQuestion{}, receipt, classifiedErr
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
			// Both legs failed -- CHAOS-3770 F4 residual: this branch (the
			// primary's output was parseable but semantically invalid) had
			// the same bug as the generation-error branch above. See its
			// comment: report the fallback's own outcome/classification,
			// not the primary's stale invalid_output/ErrModelOutput.
			receipt.Outcome = fallbackReceipt.Outcome
			return contextfabric.InterpretedQuestion{}, receipt, fallbackErr
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
			Model: r.config.ModelRef, System: synthesisSystemPrompt, Prompt: string(encoded),
		})
		return err
	})
	completed := r.now().UTC()
	var classifiedErr error
	if generationErr != nil {
		classifiedErr = classifyModelError(generationErr)
	}
	receipt := r.receipt(contextfabric.ModelOperationSynthesize, r.config.SynthesisPromptVersion, started, completed, attempts, encoded, nil, usage, classifiedErr)
	if generationErr != nil {
		if r.config.Fallback != nil {
			draft, fallbackReceipt, fallbackErr := r.config.Fallback.SynthesizeAnswer(ctx, principal, input)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				return draft, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
			// See the matching comment in InterpretQuestion: both legs
			// failed, so the receipt must reflect the fallback's own
			// (final) outcome and the caller must see its classification,
			// not the primary's.
			receipt.Outcome = fallbackReceipt.Outcome
			return contextfabric.SynthesisDraft{}, receipt, fallbackErr
		}
		return contextfabric.SynthesisDraft{}, receipt, classifiedErr
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
			// Both legs failed -- see the matching comment in
			// InterpretQuestion's semantic-invalid-output branch (CHAOS-3770
			// F4 residual).
			receipt.Outcome = fallbackReceipt.Outcome
			return contextfabric.SynthesisDraft{}, receipt, fallbackErr
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

// receipt builds the content-safe execution receipt for one generation
// attempt. err, when non-nil, must already be the classifyModelError output
// (or a context cancellation/deadline error) so the receipt Outcome reflects
// the same rate-limit/invalid-output/unavailable classification callers see
// on the returned Go error.
func (r *Runtime) receipt(operation contextfabric.ModelOperation, promptVersion string, started, completed time.Time, attempts int, input, output []byte, usage contextfabric.ModelUsage, err error) contextfabric.ModelExecutionReceipt {
	outcome := receiptOutcomeForError(err)
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

// receiptOutcomeForError maps a nil or already-classified generation error
// to the receipt Outcome vocabulary from ADR 0008 (success, fallback,
// invalid_output, unavailable), plus rate_limited for the finer-grained
// classification callers get from classifyModelError. err is expected to be
// nil or the direct result of classifyModelError; a context cancellation or
// deadline error (passed through classifyModelError unchanged) counts as
// unavailable from a receipts standpoint, since the runtime's own bounded
// deadline is what stopped the call.
func receiptOutcomeForError(err error) string {
	switch {
	case err == nil:
		return "pending_validation"
	case errors.Is(err, contextfabric.ErrModelRateLimited):
		return "rate_limited"
	case errors.Is(err, contextfabric.ErrModelOutput):
		return "invalid_output"
	default:
		return "unavailable"
	}
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

// retryable decides whether a generation failure may be retried with the
// EXACT SAME encoded payload. It classifies ONLY on structured status/code
// (a Genkit gRPC-style status, or context.DeadlineExceeded) -- never by
// sniffing an error's message text (CHAOS-3770 F2).
//
// That restriction is deliberate, not merely cautious: the OpenAI SDK's own
// error type (openai-go/internal/apierror.Error, aliased as openai.Error)
// formats Error() as "%s %q: %d %s %s" with the raw provider response body
// as the last verbatim component, and compat_oai/generate.go wraps that
// error with a plain fmt.Errorf rather than a *core.GenkitError. So by the
// time an error from the real production transport reaches this function,
// it is routinely UNSTRUCTURED, and its message can legitimately be a
// non-transient validation failure whose body happens to contain a word
// like "timeout" or the digits "502" -- retrying that with the identical
// payload would resubmit a request that can never succeed, violating the
// no-retry-same-input rule (operations.md) for no benefit. An unstructured
// error is therefore never retried by this function, full stop; genuinely
// transient transport conditions (connection resets, 429/5xx, provider
// timeouts) remain covered by the OpenAI SDK's own transport-level retry
// loop (ACR_CONTEXT_FABRIC_MODEL_MAX_TRANSPORT_RETRIES), which runs BEFORE
// its terminal error ever reaches genkitruntime.
func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Genkit (and well-behaved plugins) surface transport and provider
	// failures as *core.GenkitError with a gRPC-style status. Prefer that
	// structured signal over string sniffing when it is present -- and once
	// present, trust it completely: an unmatched status (e.g. INTERNAL, or
	// anything else Genkit's own validation paths raise) is not retryable
	// either, never falling through to string matching on its own Message.
	var genkitErr *core.GenkitError
	if errors.As(err, &genkitErr) {
		switch genkitErr.Status {
		case core.RESOURCE_EXHAUSTED, core.UNAVAILABLE, core.DEADLINE_EXCEEDED, core.ABORTED:
			return true
		default:
			// Includes INVALID_ARGUMENT (malformed request or
			// schema-incompatible output; the model is not going to
			// succeed against the same input, so this fails closed
			// instead of retrying -- ADR 0008: invalid output fails
			// closed) and every other structured status.
			return false
		}
	}
	// No structured signal: not retryable. See the function doc comment.
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
	Status             string                         `json:"status" jsonschema:"enum=complete,enum=partial,enum=degraded,enum=clarification_required,enum=no_match"`
	DirectJudgment     string                         `json:"direct_judgment"`
	CurrentState       string                         `json:"current_state"`
	StrongestPressures []string                       `json:"strongest_pressures"`
	Drivers            []contextfabric.DriverJudgment `json:"drivers"`
	RemainingWork      []contextfabric.Finding        `json:"remaining_work"`
	ReadinessGaps      []contextfabric.Finding        `json:"readiness_gaps"`
	Conflicts          []contextfabric.Finding        `json:"conflicts"`
	Limitations        []string                       `json:"limitations"`
	EvidenceRefIDs     []string                       `json:"evidence_ref_ids"`
	// ClaimedFacts is the model's restatement of the canonical fact field
	// values (from the "canonical_facts" input) that its fact-shaped
	// drivers/findings rely on. See ContextFabricClaimedFact's doc comment
	// -- SynthesisDraft.ValidateAgainst checks every entry against the
	// canonical fact bundle by exact value equality before this can ever
	// reach a persisted result.
	ClaimedFacts        []contextfabric.ClaimedFact `json:"claimed_facts"`
	DeterministicAnswer string                      `json:"deterministic_answer"`
	Warnings            []string                    `json:"warnings"`
}

func (o synthesisOutput) toDomain() (contextfabric.SynthesisDraft, error) {
	draft := contextfabric.SynthesisDraft{
		Status:         contextfabric.InvestigationStatus(strings.TrimSpace(o.Status)),
		DirectJudgment: strings.TrimSpace(o.DirectJudgment), CurrentState: strings.TrimSpace(o.CurrentState),
		StrongestPressures: trimmedUnique(o.StrongestPressures), Drivers: append([]contextfabric.DriverJudgment(nil), o.Drivers...),
		RemainingWork: append([]contextfabric.Finding(nil), o.RemainingWork...), ReadinessGaps: append([]contextfabric.Finding(nil), o.ReadinessGaps...),
		Conflicts: append([]contextfabric.Finding(nil), o.Conflicts...), Limitations: trimmedUnique(o.Limitations),
		EvidenceRefIDs: trimmedUnique(o.EvidenceRefIDs), ClaimedFacts: append([]contextfabric.ClaimedFact(nil), o.ClaimedFacts...),
		DeterministicAnswer: strings.TrimSpace(o.DeterministicAnswer), Warnings: trimmedUnique(o.Warnings),
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
