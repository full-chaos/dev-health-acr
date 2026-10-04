package synthesisprompt

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ClientAssembly is what lets a turn that asks to write its own answer be
// served: the synthesis contract this binary runs, the writing rules and the
// encoder that builds the client's model input.
func ClientAssembly() *contextfabric.ClientSynthesisAssembly {
	system := sha256.Sum256([]byte(System()))
	return &contextfabric.ClientSynthesisAssembly{
		PromptVersion:      PromptVersion,
		ModelOutputVersion: OutputVersion,
		SystemSHA256:       hex.EncodeToString(system[:]),
		Rules:              ClientRules(),
		MaxBytes:           contractsv1.ContextFabricSynthesisInputDefaultMaxBytes,
		Encode:             ClientPayload,
	}
}
