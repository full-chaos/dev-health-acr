// Package synthesisprompt assembles the synthesis system prompt and the
// bounded model input. It is pure text assembly with no model client, so any
// surface may serve the exact bytes the runtime sends.
package synthesisprompt

import (
	"encoding/json"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
)

// System returns the synthesis system message.
func System() string { return synthesisSystemPrompt }

const (
	// PromptVersion names the synthesis prompt text and the shape of the
	// input the service's own model is given (UserPayload); bump it on every
	// change to what that model is told. The client input (ClientPayload) is a
	// documented projection of that input, pinned by its own input_sha256.
	PromptVersion = "context-fabric-synthesis.v17"
	// OutputVersion names the model-output contract the prompt asks for.
	OutputVersion = interpretprompt.OutputVersion
)

// UserPayload returns the bounded JSON user message the synthesize call sends
// for input. orgID feeds the coverage merge's reconcile log only.
func UserPayload(orgID string, input contextfabric.SynthesisInput, maxBytes int) ([]byte, error) {
	return encodeBounded(InputFromDomain(orgID, input), maxBytes)
}

func encodeBounded(payload any, maxBytes int) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode bounded model input: %w", err)
	}
	if len(encoded) > maxBytes {
		return nil, &contextfabric.ModelInputOverflow{Bytes: len(encoded), MaxBytes: maxBytes}
	}
	return encoded, nil
}
