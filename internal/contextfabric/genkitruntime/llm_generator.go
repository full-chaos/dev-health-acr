package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-go/llm"
	"github.com/xeipuuv/gojsonschema"
)

// DefaultMaxOutputTokens is the output cap a model call asks for where the
// client sends one (the Anthropic Messages API requires a cap). The Chat
// Completions clients are built to send none, as the Genkit transport did.
const DefaultMaxOutputTokens = 32768

// outputFormatName is the json_schema name the Genkit OpenAI-compatible
// plugin always sent; kept so the wire request does not change.
const outputFormatName = "output"

// loggedFormatPaths remembers which operation/kind/path/reason was already
// logged at Info in this process.
var loggedFormatPaths sync.Map

// cutStopReasons are the stop reasons each client reports for a reply cut at
// its output cap (Chat Completions, Messages, Responses).
var cutStopReasons = map[string]bool{"length": true, "max_tokens": true, "max_output_tokens": true}

// errOutputSchemaMismatch is the shared-client path's twin of Genkit's own
// schema-mismatch error: a reply that is not JSON, or JSON that fails the
// output schema. classifyModelError maps it to ErrModelOutput, as it maps
// Genkit's.
var errOutputSchemaMismatch = errors.New(genkitSchemaMismatchPrefix)

const (
	llmCallFailedMessage  = "context fabric model call failed"
	llmReplyCutMessage    = "context fabric model reply hit the output cap"
	llmFormatPathMessage  = "context fabric model answer format path"
	llmSeedDroppedMessage = "context fabric model call sent without its seed"
	llmDrawRejectedReason = "context fabric model reply failed the output schema"
)

// outputSchema is one output type's JSON Schema: the map sent as the strict
// response format, and the same document compiled for the local check Genkit
// ran after every call.
type outputSchema struct {
	wire     map[string]any
	compiled *gojsonschema.Schema
	// instructions is Genkit's own JSON format instruction for this schema
	// (ai/format_json.go), which Genkit appended to the system message.
	instructions string
}

func newOutputSchema(value any) (outputSchema, error) {
	wire := inferSchemaMap(value)
	raw, err := json.Marshal(wire)
	if err != nil {
		return outputSchema{}, fmt.Errorf("encode output schema: %w", err)
	}
	compiled, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(raw))
	if err != nil {
		return outputSchema{}, fmt.Errorf("compile output schema: %w", err)
	}
	instructions := fmt.Sprintf("Output should be in JSON format and conform to the following schema:\n\n```%s```", string(raw))
	return outputSchema{wire: wire, compiled: compiled, instructions: instructions}, nil
}

// LLMClients maps each model a runtime calls (Config.Model, and
// Config.PhrasingModel when it differs) to its shared-package client.
type LLMClients map[string]llm.Provider

type llmGenerator struct {
	clients         LLMClients
	interpretation  outputSchema
	synthesis       outputSchema
	phrasing        outputSchema
	maxOutputTokens int
	logger          *slog.Logger
}

// NewWithLLM builds a Runtime whose model calls go through the shared
// dev-health-go llm clients instead of a Genkit instance. config.Genkit is
// not used; ModelRef and PhrasingModelRef must be left empty (they default to
// the bare model ids, which are the clients' keys).
func NewWithLLM(config Config, clients LLMClients) (*Runtime, error) {
	if len(clients) == 0 {
		return nil, errors.New("llm clients are required")
	}
	for model, client := range clients {
		if client == nil {
			return nil, fmt.Errorf("llm client for model %q is nil", model)
		}
		if client.Model() != model {
			return nil, fmt.Errorf("llm client keyed %q calls model %q", model, client.Model())
		}
	}
	gen := llmGenerator{clients: clients, maxOutputTokens: config.MaxOutputTokens, logger: config.Logger}
	if gen.maxOutputTokens == 0 {
		gen.maxOutputTokens = DefaultMaxOutputTokens
	}
	if gen.logger == nil {
		gen.logger = slog.Default()
	}
	var err error
	if gen.interpretation, err = newOutputSchema(interpretationOutput{}); err != nil {
		return nil, err
	}
	if gen.synthesis, err = newOutputSchema(synthesisOutput{}); err != nil {
		return nil, err
	}
	if gen.phrasing, err = newOutputSchema(phrasingOutput{}); err != nil {
		return nil, err
	}
	runtime, err := newWithGenerator(config, gen)
	if err != nil {
		return nil, err
	}
	for _, model := range []string{runtime.config.ModelRef, runtime.config.PhrasingModelRef} {
		if _, ok := clients[model]; !ok {
			return nil, fmt.Errorf("no llm client for model %q", model)
		}
	}
	return runtime, nil
}

// seedAccepted reports whether the shared client of kind sends a seed: the
// Chat Completions client does for local and Qwen; the Anthropic and Gemini
// clients refuse one before any call.
func seedAccepted(kind llm.Kind) bool {
	return kind == llm.KindLocal || kind == llm.KindQwen
}

// seedOf reads the decoding seed out of generationRequest.Config, which
// carries it in the shape chaos4631InterpretDecodingConfig builds.
func seedOf(config any) *int64 {
	values, ok := config.(map[string]any)
	if !ok {
		return nil
	}
	seed, ok := values["seed"].(int64)
	if !ok {
		return nil
	}
	return &seed
}

// complete runs one call and returns the reply text the output check reads:
// the compact JSON, or, when the shared client's JSON gate refused the reply,
// the reply as written (Genkit's own check unwraps a fenced reply, so the
// same recovery has to see it).
func (g llmGenerator) complete(ctx context.Context, operation string, request generationRequest, schema outputSchema) (string, contextfabric.ModelUsage, error) {
	client, ok := g.clients[request.Model]
	if !ok {
		return "", contextfabric.ModelUsage{}, fmt.Errorf("%w: no client for the requested model", contextfabric.ErrModelUnavailable)
	}
	seed := seedOf(request.Config)
	if seed != nil && !seedAccepted(client.Kind()) {
		// The client would refuse the whole call; the interpretation runs
		// unseeded instead, as it would on a provider that ignores seed.
		g.logger.DebugContext(ctx, llmSeedDroppedMessage, "operation", contextfabric.SanitizeLogAttr(operation),
			"llm_kind", contextfabric.SanitizeLogAttr(string(client.Kind())))
		seed = nil
	}
	if seed != nil && client.Kind() == llm.KindQwen {
		// DashScope takes a seed in [0, 2^31) and Workers AI's Qwen models
		// cap it too; folding keeps it derived from the question and the
		// sample, so the ensemble's samples still draw distinct seeds.
		folded := *seed % (1 << 31)
		seed = &folded
	}
	call := llm.Request{Prompt: request.Prompt, MaxOutputTokens: g.maxOutputTokens, Seed: seed}
	if client.Kind() == llm.KindAnthropic {
		// Anthropic: the schema itself, as structured outputs or one tool
		// call, picked by the client before the call.
		call.System = request.System
		call.Format = &llm.JSONFormat{Name: outputFormatName, Schema: schema.wire}
	} else {
		// Chat Completions: the request Genkit sent, byte for byte. No schema
		// on the wire (a JSON object answer) and the schema as text at the
		// end of the system message: acr's output schemas use keywords
		// OpenAI's strict mode refuses.
		call.System = request.System + schema.instructions
		call.Format = &llm.JSONFormat{Type: llm.FormatTypeJSONObject}
	}
	result, err := client.Complete(ctx, call)
	if err != nil {
		class, _ := llm.ClassOf(err)
		g.logger.DebugContext(ctx, llmCallFailedMessage, "operation", contextfabric.SanitizeLogAttr(operation),
			"llm_kind", contextfabric.SanitizeLogAttr(string(client.Kind())), "error_class", contextfabric.SanitizeLogAttr(string(class)))
		return "", contextfabric.ModelUsage{}, err
	}
	formatArgs := []any{"operation", contextfabric.SanitizeLogAttr(operation),
		"llm_kind", contextfabric.SanitizeLogAttr(string(client.Kind())), "format_path", contextfabric.SanitizeLogAttr(result.FormatPath),
		"format_reason", contextfabric.SanitizeLogAttr(result.FormatReason)}
	g.logger.DebugContext(ctx, llmFormatPathMessage, formatArgs...)
	if _, seen := loggedFormatPaths.LoadOrStore(operation+"\x00"+string(client.Kind())+"\x00"+result.FormatPath+"\x00"+result.FormatReason, true); !seen {
		// Once per process per operation and mode: which mode each
		// provider and schema actually uses is visible at the default level.
		g.logger.InfoContext(ctx, llmFormatPathMessage, formatArgs...)
	}
	if cutStopReasons[result.StopReason] {
		outputTokens := 0
		if result.Usage.OutputTokens != nil {
			outputTokens = *result.Usage.OutputTokens
		}
		g.logger.DebugContext(ctx, llmReplyCutMessage, "operation", contextfabric.SanitizeLogAttr(operation),
			"llm_kind", contextfabric.SanitizeLogAttr(string(client.Kind())), "stop_reason", contextfabric.SanitizeLogAttr(result.StopReason),
			"output_tokens", outputTokens, "max_output_tokens", g.maxOutputTokens)
	}
	usage := llmUsage(result.Usage)
	if result.Text != "" {
		return result.Text, usage, nil
	}
	return result.Unparsed, usage, nil
}

func llmUsage(usage llm.Usage) contextfabric.ModelUsage {
	var out contextfabric.ModelUsage
	if usage.InputTokens != nil {
		out.InputTokens = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		out.OutputTokens = *usage.OutputTokens
	}
	out.TotalTokens = out.InputTokens + out.OutputTokens
	return out
}

// check is Genkit's ParseMessage for a schema-bound call: unwrap a fenced
// reply, require JSON, validate it against the output schema.
func (g llmGenerator) check(ctx context.Context, operation, text string, schema outputSchema) (string, error) {
	candidate := extractGenkitJSON(text)
	reason := ""
	var violations []string
	switch {
	case candidate == "":
		reason = "empty"
	case !json.Valid([]byte(candidate)):
		reason = "not_json"
	default:
		result, err := schema.compiled.Validate(gojsonschema.NewStringLoader(candidate))
		if err != nil {
			reason = "not_json"
		} else if !result.Valid() {
			reason = "schema"
			violations = schemaViolations(result)
		}
	}
	if reason == "" {
		return candidate, nil
	}
	g.logger.DebugContext(ctx, llmDrawRejectedReason, "operation", contextfabric.SanitizeLogAttr(operation),
		"reason", contextfabric.SanitizeLogAttr(reason), "reply_bytes", len(text),
		"violations", contextfabric.SanitizeLogStrings(violations))
	return candidate, fmt.Errorf("%w: %s", errOutputSchemaMismatch, reason)
}

// schemaViolations names where a reply broke the output schema: the JSON
// path and the rule only, never a value, at most five.
func schemaViolations(result *gojsonschema.Result) []string {
	var out []string
	for _, violation := range result.Errors() {
		if len(out) == 5 {
			break
		}
		out = append(out, violation.Field()+":"+violation.Type())
	}
	return out
}

func (g llmGenerator) Interpret(ctx context.Context, request generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	text, usage, err := g.complete(ctx, "interpret", request, g.interpretation)
	if err != nil {
		return interpretationOutput{}, contextfabric.ModelUsage{}, err
	}
	candidate, checkErr := g.check(ctx, "interpret", text, g.interpretation)
	var output interpretationOutput
	decodeErr := json.Unmarshal([]byte(candidate), &output)
	if checkErr != nil {
		rejection := &decodeRejection{cause: checkErr}
		if decodeErr == nil {
			rejection.output, rejection.parsed = output, true
		}
		return rejection.output, usage, rejection
	}
	if decodeErr != nil {
		return interpretationOutput{}, usage, &decodeRejection{cause: fmt.Errorf("%w: decode", errOutputSchemaMismatch)}
	}
	return output, usage, nil
}

func (g llmGenerator) Synthesize(ctx context.Context, request generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	text, usage, err := g.complete(ctx, "synthesize", request, g.synthesis)
	if err != nil {
		return synthesisOutput{}, contextfabric.ModelUsage{}, err
	}
	candidate, checkErr := g.check(ctx, "synthesize", text, g.synthesis)
	var output synthesisOutput
	decodeErr := json.Unmarshal([]byte(candidate), &output)
	if checkErr == nil && decodeErr != nil {
		checkErr = fmt.Errorf("%w: decode", errOutputSchemaMismatch)
	}
	if checkErr != nil {
		rejection := &decodeSynthesisRejection{cause: classifyModelError(checkErr), digest: contextfabric.DigestModelValue([]byte(text))}
		if decodeErr == nil {
			rejection.output, rejection.parsed = output, true
		}
		return rejection.output, usage, rejection
	}
	return output, usage, nil
}

func (g llmGenerator) Phrase(ctx context.Context, request generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	text, usage, err := g.complete(ctx, "phrase", request, g.phrasing)
	if err != nil {
		return phrasingOutput{}, contextfabric.ModelUsage{}, err
	}
	candidate, checkErr := g.check(ctx, "phrase", text, g.phrasing)
	if checkErr != nil {
		return phrasingOutput{}, usage, checkErr
	}
	var output phrasingOutput
	if err := json.Unmarshal([]byte(candidate), &output); err != nil {
		return phrasingOutput{}, usage, fmt.Errorf("%w: decode", errOutputSchemaMismatch)
	}
	return output, usage, nil
}

// classifyLLMError maps the shared client's errors to the model runtime
// sentinels the way the Genkit transport's were mapped: an HTTP 429 is rate
// limited whatever its body says (the client's class calls an
// insufficient_quota 429 auth), and every other response status is
// unavailable. Only the class and status reach the message: the client's
// errors are already sanitized, and none of its text is forwarded.
func classifyLLMError(err error) (error, bool) {
	var typed *llm.Error
	if !errors.As(err, &typed) {
		return nil, false
	}
	class, ok := llm.ClassOf(err)
	if !ok {
		return nil, false
	}
	if status := typed.StatusCode(); status == http.StatusTooManyRequests {
		return fmt.Errorf("%w: provider status %d", contextfabric.ErrModelRateLimited, status), true
	}
	switch class {
	case llm.ClassRateLimit:
		return fmt.Errorf("%w: provider class %s", contextfabric.ErrModelRateLimited, class), true
	case llm.ClassOutput:
		return fmt.Errorf("%w: provider class %s", contextfabric.ErrModelOutput, class), true
	default:
		return fmt.Errorf("%w: provider class %s", contextfabric.ErrModelUnavailable, class), true
	}
}
