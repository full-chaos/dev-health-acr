package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// ContextFabricSynthesisInputDefaultMaxBytes is the size the service
	// bounds a synthesis input to when it builds one.
	ContextFabricSynthesisInputDefaultMaxBytes = 128 << 10
	// ContextFabricSynthesisInputMaxBytes bounds the input object of a
	// served synthesis input.
	ContextFabricSynthesisInputMaxBytes = 1 << 20
	// ContextFabricSynthesisInputRulesMaxCount and
	// ContextFabricSynthesisInputRuleMaxLength bound the writing rules.
	ContextFabricSynthesisInputRulesMaxCount = 16
	ContextFabricSynthesisInputRuleMaxLength = 512
)

// ContextFabricSynthesisMode names who writes the answer of a turn.
type ContextFabricSynthesisMode string

const (
	ContextFabricSynthesisModeServer ContextFabricSynthesisMode = "server"
	ContextFabricSynthesisModeClient ContextFabricSynthesisMode = "client"
)

func ValidContextFabricSynthesisMode(mode ContextFabricSynthesisMode) bool {
	return mode == ContextFabricSynthesisModeServer || mode == ContextFabricSynthesisModeClient
}

// ContextFabricSynthesisSource names who wrote the answer of a result.
type ContextFabricSynthesisSource string

const (
	ContextFabricSynthesisSourceServer ContextFabricSynthesisSource = "server"
	ContextFabricSynthesisSourceClient ContextFabricSynthesisSource = "client"
)

func ValidContextFabricSynthesisSource(source ContextFabricSynthesisSource) bool {
	return source == ContextFabricSynthesisSourceServer || source == ContextFabricSynthesisSourceClient
}

// ContextFabricSynthesisContract names the synthesis contract the input was
// built for: the model output version, the prompt version and the sha256 of
// the system message.
type ContextFabricSynthesisContract struct {
	ModelOutputVersion string `json:"model_output_version"`
	PromptVersion      string `json:"prompt_version"`
	SystemSHA256       string `json:"system_sha256"`
}

// ContextFabricSynthesisInput is the model input a caller that asked to write
// the answer writes it from: the input the service would have sent its own
// synthesis model (cut to the size bound when Bounded is set) without the
// times at which the turn looked and with the evidence window named, so
// InputSHA256, the sha256 of Input, covers the facts, the coverage and the
// identity of the evidence window. A relative window, a clamped span and a
// provider's own default window are each one standing commitment: their
// moving bounds are not part of the digest. It is served only in the turn
// that built it and is never stored.
type ContextFabricSynthesisInput struct {
	Contract    ContextFabricSynthesisContract `json:"contract"`
	Input       json.RawMessage                `json:"input"`
	InputSHA256 string                         `json:"input_sha256"`
	Bounded     bool                           `json:"bounded"`
	Rules       []string                       `json:"rules"`
}

func (s ContextFabricSynthesisInput) Validate() error {
	if !validVersion(s.Contract.ModelOutputVersion) || !validVersion(s.Contract.PromptVersion) {
		return fmt.Errorf("synthesis_input contract versions violate v1 bounds")
	}
	if !validLowerHexSHA256(s.Contract.SystemSHA256) {
		return fmt.Errorf("synthesis_input contract system_sha256 must be 64 lowercase hex characters")
	}
	if len(s.Input) > ContextFabricSynthesisInputMaxBytes {
		return fmt.Errorf("synthesis_input input exceeds %d bytes", ContextFabricSynthesisInputMaxBytes)
	}
	input := bytes.TrimSpace(s.Input)
	if len(input) == 0 || input[0] != '{' || !json.Valid(input) {
		return fmt.Errorf("synthesis_input input must be a JSON object")
	}
	if !validLowerHexSHA256(s.InputSHA256) {
		return fmt.Errorf("synthesis_input input_sha256 must be 64 lowercase hex characters")
	}
	sum := sha256.Sum256(s.Input)
	if hex.EncodeToString(sum[:]) != s.InputSHA256 {
		return fmt.Errorf("synthesis_input input_sha256 is not the sha256 of input")
	}
	if len(s.Rules) < 1 || len(s.Rules) > ContextFabricSynthesisInputRulesMaxCount {
		return fmt.Errorf("synthesis_input rules violate v1 bounds")
	}
	for _, rule := range s.Rules {
		if !stringLengthBetween(rule, 1, ContextFabricSynthesisInputRuleMaxLength) || strings.TrimSpace(rule) != rule {
			return fmt.Errorf("synthesis_input rule violates v1 bounds")
		}
	}
	return nil
}

// ContextFabricInvestigationResponse is the body of the create-investigation
// route: the stored result and, for a turn that asked to write its own
// answer, the synthesis input of that turn. The result alone is what is
// stored and what read by id serves.
type ContextFabricInvestigationResponse struct {
	ContextFabricInvestigationResult
	SynthesisInput *ContextFabricSynthesisInput `json:"synthesis_input,omitempty"`
}

func (r ContextFabricInvestigationResponse) Validate() error {
	if err := r.ContextFabricInvestigationResult.Validate(); err != nil {
		return err
	}
	if r.SynthesisInput != nil {
		return r.SynthesisInput.Validate()
	}
	return nil
}
