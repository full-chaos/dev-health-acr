package genkitruntime

import (
	"encoding/json"

	"github.com/invopop/jsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// DefaultExchangeMaxInputBytes mirrors Config's own MaxInputBytes default
// (512KiB) for callers of the exported helpers below that have no Runtime
// instance to read it from.
const DefaultExchangeMaxInputBytes = 512 << 10

// The functions in this file exist SOLELY so an alternate, non-genkit model
// transport (CHAOS-3742's file-exchange diagnostic arm, an out-of-process
// responder) can send the EXACT SAME prompts, input payload shape, and
// output schema contract this package's real OpenAI-backed Runtime sends
// and expects -- never duplicated or reimplemented by the caller, so the
// alternate transport cannot silently drift from what production actually
// asks a model to do. They perform no network call and are not referenced
// by Runtime, New, or any production composition path (internal/runtime/
// hosted's default wiring never touches this file) -- an inert, additive
// seam, exported per the trial's "reuse the real composition, minimal
// exported seam when needed, commit and flag it" rule.

// InterpretationSystemPrompt returns the exact system prompt
// InterpretQuestion sends.
func InterpretationSystemPrompt() string { return interpretationSystemPrompt }

// SynthesisSystemPrompt returns the exact system prompt SynthesizeAnswer
// sends.
func SynthesisSystemPrompt() string { return synthesisSystemPrompt }

// BuildInterpretationPrompt renders the exact bounded-JSON user payload
// InterpretQuestion would send for this request, without performing a
// network call.
func BuildInterpretationPrompt(request contextfabric.InvestigationRequest, maxBytes int) (string, error) {
	payload := interpretationInput{
		Question:             request.Question,
		Conversation:         request.Conversation,
		SubjectHints:         request.RequestedScope.SubjectHints,
		RequestedScope:       request.RequestedScope,
		TimeContext:          request.TimeContext,
		PriorSubjectReceipts: request.PriorSubjectReceipts,
	}
	encoded, err := boundedJSON(payload, maxBytes)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ParseInterpretationOutput decodes a model's raw JSON reply using the
// exact same struct shape and domain conversion InterpretQuestion applies
// to a genkit-returned interpretationOutput.
func ParseInterpretationOutput(raw []byte, defaultTime contextfabric.TimeContext) (contextfabric.InterpretedQuestion, error) {
	var output interpretationOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return contextfabric.InterpretedQuestion{}, err
	}
	return output.toDomain(defaultTime)
}

// InterpretationOutputSchema reflects the exact Go type genkit's
// constrained structured output binds InterpretQuestion's model call to,
// as a JSON Schema document -- the same contract a non-genkit responder
// must be told to follow.
func InterpretationOutputSchema() ([]byte, error) {
	return json.MarshalIndent(jsonschema.Reflect(&interpretationOutput{}), "", "  ")
}

// BuildSynthesisPrompt renders the exact bounded-JSON user payload
// SynthesizeAnswer would send for this input, without performing a
// network call.
func BuildSynthesisPrompt(input contextfabric.SynthesisInput, maxBytes int) (string, error) {
	payload := synthesisInputFromDomain(input)
	encoded, err := boundedJSON(payload, maxBytes)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// ParseSynthesisOutput decodes a model's raw JSON reply using the exact
// same struct shape and domain conversion SynthesizeAnswer applies to a
// genkit-returned synthesisOutput.
func ParseSynthesisOutput(raw []byte) (contextfabric.SynthesisDraft, error) {
	var output synthesisOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return contextfabric.SynthesisDraft{}, err
	}
	return output.toDomain()
}

// SynthesisOutputSchema reflects the exact Go type genkit's constrained
// structured output binds SynthesizeAnswer's model call to, as a JSON
// Schema document.
func SynthesisOutputSchema() ([]byte, error) {
	return json.MarshalIndent(jsonschema.Reflect(&synthesisOutput{}), "", "  ")
}
