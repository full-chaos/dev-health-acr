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
// network call. No authenticated principal is available on this preview
// path, so the coverage merge's fail-open reconcile log (CHAOS-4690,
// contextfabric.MergeCoverage) carries an empty org id here -- merge
// semantics are otherwise byte-identical to SynthesizeAnswer's own call.
func BuildSynthesisPrompt(input contextfabric.SynthesisInput, maxBytes int) (string, error) {
	payload := synthesisInputFromDomain("", input)
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
// InterpretationOutputFrame is the CHAOS-4452 stage-2 shadow capture a
// non-genkit transport gets as the third field of InterpretationOutputCapture,
// built by the SAME sanitizeFrameOutput the genkit path calls.
//
// It exists for the identical reason InterpretationOutputFamily does: a
// transport-specific reimplementation would diverge silently, and the
// divergence would land inside the labelled semantic-correctness
// measurement this slice exists to make, where it would be
// indistinguishable from the model behaving differently.
//
// Deliberately NOT its own ParseInterpretationOutputFrame entry point --
// InterpretationOutputCapture's own doc comment explains why: this
// package already paid for that mistake twice (window-only, then
// family-only), and a frame-only parser would be the identical trap a
// third time, one call site away from a transport that copies the frame
// and forgets whatever joins it next.
type InterpretationOutputFrame struct {
	Frame            contextfabric.QuestionFrame
	Present          bool
	GoalsDropped     int
	TermsTruncated   int
	KindUnrecognized bool
}

type InterpretationOutputFamily struct {
	Family                      contextfabric.QuestionFamily
	FamilyUnrecognized          bool
	GroupKind                   contextfabric.SubjectKind
	GroupKindUnrecognized       bool
	ScopeAnchorTerm             string
	ScopeAnchorTermTruncated    bool
	ScopeAnchorKind             contextfabric.SubjectKind
	ScopeAnchorKindUnrecognized bool
	RequestedKind               contextfabric.SubjectKind
	RequestedKindUnrecognized   bool
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
		Family:                      capture.Family,
		FamilyUnrecognized:          capture.FamilyUnrecognized,
		GroupKind:                   capture.GroupKind,
		GroupKindUnrecognized:       capture.GroupKindUnrecognized,
		ScopeAnchorTerm:             capture.ScopeAnchorTerm,
		ScopeAnchorTermTruncated:    capture.ScopeAnchorTruncated,
		ScopeAnchorKind:             capture.ScopeAnchorKind,
		ScopeAnchorKindUnrecognized: capture.ScopeAnchorKindUnrecognized,
		RequestedKind:               capture.RequestedKind,
		RequestedKindUnrecognized:   capture.RequestedKindUnrecognized,
	}, nil
}

// InterpretationOutputCapture is the WHOLE shadow capture from one raw
// interpretation output -- window (CHAOS-3900 W0), family (CHAOS-4632) and
// frame (CHAOS-4452 stage 2) together.
//
// This exists because the three were added one slice apart each and each
// got its own parser, which meant a transport had to remember to call
// every one of them. It did not: the file-exchange transport called only
// the window parser, so every CHAOS-4632 signal arrived empty and a
// shadow-harness measurement over a live corpus would have read transport
// loss as the model never emitting them. Frame joined this struct instead
// of getting a fourth standalone parser for the SAME reason family joined
// window rather than getting its own: the fix is not another parallel
// function but ONE parser that cannot be half-called, with every signal
// delegating to it.
type InterpretationOutputCapture struct {
	Window InterpretationOutputWindow
	Family InterpretationOutputFamily
	Frame  InterpretationOutputFrame
}

// ParseInterpretationOutputSignals decodes raw once and returns the domain
// interpretation plus the COMPLETE shadow capture. Any transport that is
// not genkit should call THIS, not a narrower parser, so a future shadow
// signal is picked up without touching the transport again.
func ParseInterpretationOutputSignals(raw []byte, defaultTime contextfabric.TimeContext) (contextfabric.InterpretedQuestion, InterpretationOutputCapture, error) {
	var output interpretationOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return contextfabric.InterpretedQuestion{}, InterpretationOutputCapture{}, err
	}
	interpreted, err := output.toDomain(defaultTime)
	if err != nil {
		return interpreted, InterpretationOutputCapture{}, err
	}
	class, confidence, unrecognized := sanitizeWindowOutput(output)
	family := sanitizeFamilyOutput(output)
	frame := sanitizeFrameOutput(output)
	return interpreted, InterpretationOutputCapture{
		Window: InterpretationOutputWindow{Class: class, Confidence: confidence, ClassUnrecognized: unrecognized},
		Family: InterpretationOutputFamily{
			Family:                      family.Family,
			FamilyUnrecognized:          family.FamilyUnrecognized,
			GroupKind:                   family.GroupKind,
			GroupKindUnrecognized:       family.GroupKindUnrecognized,
			ScopeAnchorTerm:             family.ScopeAnchorTerm,
			ScopeAnchorTermTruncated:    family.ScopeAnchorTruncated,
			ScopeAnchorKind:             family.ScopeAnchorKind,
			ScopeAnchorKindUnrecognized: family.ScopeAnchorKindUnrecognized,
			RequestedKind:               family.RequestedKind,
			RequestedKindUnrecognized:   family.RequestedKindUnrecognized,
		},
		Frame: InterpretationOutputFrame{
			Frame:            frame.Frame,
			Present:          frame.Present,
			GoalsDropped:     frame.GoalsDropped,
			TermsTruncated:   frame.TermsTruncated,
			KindUnrecognized: frame.KindUnrecognized,
		},
	}, nil
}

// ApplyInterpretationCapture stamps a complete capture onto a receipt --
// the one place a non-genkit transport needs, so it cannot copy some
// fields and forget others (which is exactly what happened when the
// file-exchange transport copied the three window fields and none of the
// family ones, and would have happened again for frame had this diff given
// frame its own parser instead of joining this one).
//
// The frame fields mirror applyFrameCapture's (runtime.go) own guard: a
// receipt with no frame in the capture must stay frame-absent, not
// zero-valued-and-therefore-indistinguishable-from-absent -- Present is
// what lets resolveFrame tell "no proposal" from "an empty one".
func ApplyInterpretationCapture(receipt *contextfabric.ModelExecutionReceipt, capture InterpretationOutputCapture) {
	receipt.WindowClass = capture.Window.Class
	receipt.WindowConfidence = capture.Window.Confidence
	receipt.WindowClassUnrecognized = capture.Window.ClassUnrecognized
	receipt.QuestionFamily = capture.Family.Family
	receipt.QuestionFamilyUnrecognized = capture.Family.FamilyUnrecognized
	receipt.GroupKind = capture.Family.GroupKind
	receipt.GroupKindUnrecognized = capture.Family.GroupKindUnrecognized
	receipt.ScopeAnchorTerm = capture.Family.ScopeAnchorTerm
	receipt.ScopeAnchorTermTruncated = capture.Family.ScopeAnchorTermTruncated
	receipt.ScopeAnchorKind = capture.Family.ScopeAnchorKind
	receipt.ScopeAnchorKindUnrecognized = capture.Family.ScopeAnchorKindUnrecognized
	receipt.RequestedSubjectKind = capture.Family.RequestedKind
	receipt.RequestedSubjectKindUnrecognized = capture.Family.RequestedKindUnrecognized
	if capture.Frame.Present {
		frame := capture.Frame.Frame
		receipt.QuestionFrame = &frame
		receipt.FrameGoalsDropped = capture.Frame.GoalsDropped
		receipt.FrameTermsTruncated = capture.Frame.TermsTruncated
		receipt.FrameKindUnrecognized = capture.Frame.KindUnrecognized
	}
}
