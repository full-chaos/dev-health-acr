// Package interpretprompt assembles the interpretation system prompt. It is
// pure text assembly with no model client, so any surface may serve the exact
// bytes the runtime sends.
package interpretprompt

import (
	"encoding/json"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// System returns the interpretation system message.
func System() string { return interpretationSystemPrompt }

// FactKindList returns the closed fact-kind set as the prompts quote it.
func FactKindList() string { return contextFabricFactKindList }

// The values below expose the prompt's own data tables so the runtime's tests
// read the same tables the prompt renders.
type ScopedMemberExample = scopedMemberExample

var (
	FlatFieldRows        = interpretationFlatFieldRows
	EmphasisWords        = interpretationEmphasisWords
	DimensionWords       = interpretationDimensionWords
	ScopedMemberExamples = interpretationScopedMemberExamples
	FactKindGlossary     = interpretationFactKindGlossary
)

const (
	// PromptVersion names the interpretation prompt text; bump it on every
	// change to what the model is told.
	PromptVersion = "context-fabric-interpretation.v21"
	// OutputVersion names the model-output contract the prompt asks for.
	OutputVersion = "context-fabric-model-output.v8"
)

type interpretationInput struct {
	Question             string                              `json:"question"`
	Conversation         []contextfabric.ConversationTurn    `json:"conversation,omitempty"`
	SubjectHints         []contextfabric.SubjectHint         `json:"subject_hints,omitempty"`
	RequestedScope       contextfabric.RequestedScope        `json:"requested_scope,omitempty"`
	TimeContext          contextfabric.TimeContext           `json:"time_context"`
	PriorSubjectReceipts []contextfabric.BoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
}

// UserPayload returns the bounded JSON user message the interpret call sends
// for request.
func UserPayload(request contextfabric.InvestigationRequest, maxBytes int) ([]byte, error) {
	encoded, err := json.Marshal(interpretationInput{
		Question:             request.Question,
		Conversation:         request.Conversation,
		SubjectHints:         request.RequestedScope.SubjectHints,
		RequestedScope:       request.RequestedScope,
		TimeContext:          request.TimeContext,
		PriorSubjectReceipts: request.PriorSubjectReceipts,
	})
	if err != nil {
		return nil, fmt.Errorf("encode bounded model input: %w", err)
	}
	if len(encoded) > maxBytes {
		return nil, &contextfabric.ModelInputOverflow{Bytes: len(encoded), MaxBytes: maxBytes}
	}
	return encoded, nil
}
