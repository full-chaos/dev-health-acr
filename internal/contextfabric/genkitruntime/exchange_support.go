package genkitruntime

import (
	"encoding/json"

	"github.com/firebase/genkit/go/core"

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

// InterpretationOutputWindow is the CHAOS-3900 W0 sanitized window
// classification ParseInterpretationOutputWindow returns alongside the
// domain InterpretedQuestion -- see that function's own doc comment.
type InterpretationOutputWindow struct {
	Class             contextfabric.WindowClass
	Confidence        contextfabric.WindowConfidence
	ClassUnrecognized bool
}

// ParseInterpretationOutputWindow decodes raw exactly like
// ParseInterpretationOutput, and additionally returns the SAME sanitized
// window_class/window_confidence/unrecognized capture (CHAOS-3900 W0)
// Runtime.InterpretQuestion applies to a genkit-returned interpretationOutput
// -- both call the identical sanitizeWindowOutput (runtime.go), so a
// non-genkit responder (the file-exchange trial transport) can populate its
// own ModelExecutionReceipt with a classification byte-identical to what a
// real genkit call would have captured, never a transport-specific
// reimplementation that could silently diverge. Only sanitized on a
// SUCCESSFUL toDomain (mirrors Runtime.InterpretQuestion's own ordering):
// on a toDomain/Validate failure, the returned window is the zero value.
func ParseInterpretationOutputWindow(raw []byte, defaultTime contextfabric.TimeContext) (contextfabric.InterpretedQuestion, InterpretationOutputWindow, error) {
	var output interpretationOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return contextfabric.InterpretedQuestion{}, InterpretationOutputWindow{}, err
	}
	interpreted, err := output.toDomain(defaultTime)
	if err != nil {
		return interpreted, InterpretationOutputWindow{}, err
	}
	class, confidence, unrecognized := sanitizeWindowOutput(output)
	return interpreted, InterpretationOutputWindow{Class: class, Confidence: confidence, ClassUnrecognized: unrecognized}, nil
}

// InterpretationOutputSchema reflects the exact Go type genkit's
// constrained structured output binds InterpretQuestion's model call to,
// as a JSON Schema document -- the same contract a non-genkit responder
// must be told to follow.
//
// Uses core.InferSchemaMap -- the SAME function genkit's own
// ai.WithOutputType (called by GenerateData, which sdkGenerator.Interpret
// uses) derives the constrained-output schema from. It flattens $ref/$defs
// (DoNotReference: true) exactly as production's OpenAI structured-output
// binding requires, rather than the invopop/jsonschema default nested-$ref
// form (sol review F9, upgraded from a documented gap to a fix once
// core.InferSchemaMap -- genkit's own public flattening mapper -- was
// confirmed reusable without reimplementing it).
func InterpretationOutputSchema() ([]byte, error) {
	return json.MarshalIndent(core.InferSchemaMap(interpretationOutput{}), "", "  ")
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
// Schema document -- see InterpretationOutputSchema's doc comment for why
// core.InferSchemaMap, not raw jsonschema.Reflect.
func SynthesisOutputSchema() ([]byte, error) {
	return json.MarshalIndent(core.InferSchemaMap(synthesisOutput{}), "", "  ")
}

// InterpretationOutputFamily is the CHAOS-4632 shadow capture
// ParseInterpretationOutputFamily returns alongside the domain
// InterpretedQuestion.
type InterpretationOutputFamily struct {
	Family                   contextfabric.QuestionFamily
	FamilyUnrecognized       bool
	GroupKind                contextfabric.SubjectKind
	GroupKindUnrecognized    bool
	ScopeAnchorTerm          string
	ScopeAnchorTermTruncated bool
	ScopeAnchorKind          contextfabric.SubjectKind
	RequestedKind            contextfabric.SubjectKind
}

// ParseInterpretationOutputFamily decodes raw exactly like
// ParseInterpretationOutput, and additionally returns the SAME sanitized
// CHAOS-4632 family capture Runtime.InterpretQuestion applies to a
// genkit-returned interpretationOutput.
//
// This exists for the identical reason ParseInterpretationOutputWindow
// does, and the reason is worth restating because it is the whole point of
// having ONE sanitizer: both call sanitizeFamilyOutput (runtime.go), so a
// non-genkit responder (the file-exchange trial transport) populates its
// own ModelExecutionReceipt with a capture byte-identical to what a real
// genkit call would have produced. A transport-specific reimplementation
// would diverge silently, and the divergence would land in the very
// measurement this slice exists to make -- the labelled semantic
// correctness of GroupKind and the scope anchor -- where it would be
// indistinguishable from the model behaving differently.
//
// Only sanitized on a SUCCESSFUL toDomain, mirroring both
// Runtime.InterpretQuestion's ordering and ParseInterpretationOutputWindow's:
// on a toDomain/Validate failure the returned capture is the zero value.
func ParseInterpretationOutputFamily(raw []byte, defaultTime contextfabric.TimeContext) (contextfabric.InterpretedQuestion, InterpretationOutputFamily, error) {
	var output interpretationOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return contextfabric.InterpretedQuestion{}, InterpretationOutputFamily{}, err
	}
	interpreted, err := output.toDomain(defaultTime)
	if err != nil {
		return interpreted, InterpretationOutputFamily{}, err
	}
	capture := sanitizeFamilyOutput(output)
	return interpreted, InterpretationOutputFamily{
		Family:                   capture.Family,
		FamilyUnrecognized:       capture.FamilyUnrecognized,
		GroupKind:                capture.GroupKind,
		GroupKindUnrecognized:    capture.GroupKindUnrecognized,
		ScopeAnchorTerm:          capture.ScopeAnchorTerm,
		ScopeAnchorTermTruncated: capture.ScopeAnchorTruncated,
		ScopeAnchorKind:          capture.ScopeAnchorKind,
		RequestedKind:            capture.RequestedKind,
	}, nil
}
