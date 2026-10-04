package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ContextFabricSuppliedSynthesisMaxBytes bounds the raw draft object a
// client may send.
const ContextFabricSuppliedSynthesisMaxBytes = 256 << 10

// The fields of a supplied synthesis's contract that can mismatch.
const (
	ContextFabricSynthesisContractFieldModelOutputVersion = "model_output_version"
	ContextFabricSynthesisContractFieldPromptVersion      = "prompt_version"
	ContextFabricSynthesisContractFieldSystemSHA256       = "system_sha256"
	ContextFabricSynthesisContractFieldInputSHA256        = "input_sha256"
)

// The keys of the error details a supplied synthesis refusal is served under.
const (
	ContextFabricSynthesisContractDetailsKey = "synthesis_contract"
	ContextFabricSuppliedSynthesisReasonKey  = "reason"
	ContextFabricSynthesisInputDetailsKey    = "synthesis_input"
	ContextFabricSynthesisRejectionReasonKey = "rejection_reason"
)

// The closed reasons of a refusal that is neither a contract mismatch nor a
// rejected draft.
const (
	ContextFabricSuppliedSynthesisReasonInputChanged           = "input_changed"
	ContextFabricSuppliedSynthesisReasonInterpretationRequired = "supplied_interpretation_required"
)

// ContextFabricSuppliedSynthesisReasons lists the closed reasons.
func ContextFabricSuppliedSynthesisReasons() []string {
	return []string{
		ContextFabricSuppliedSynthesisReasonInputChanged,
		ContextFabricSuppliedSynthesisReasonInterpretationRequired,
	}
}

func ValidContextFabricSuppliedSynthesisReason(reason string) bool {
	for _, known := range ContextFabricSuppliedSynthesisReasons() {
		if reason == known {
			return true
		}
	}
	return false
}

// ContextFabricSuppliedSynthesis is the draft a caller wrote on its own model
// from a synthesis input. Output is the model output object in the shape of
// the synthesize_answer prompt; it is untrusted and is validated exactly as
// the service's own model output is. The four contract values name the input
// the draft was written from. All four are required: the service refuses a
// value that is absent or is not its own, and names the current contract.
type ContextFabricSuppliedSynthesis struct {
	Output             json.RawMessage `json:"output"`
	ModelOutputVersion string          `json:"model_output_version"`
	PromptVersion      string          `json:"prompt_version"`
	SystemSHA256       string          `json:"system_sha256"`
	InputSHA256        string          `json:"input_sha256"`
	ClientModel        string          `json:"client_model,omitempty"`
}

// Validate checks the shape of each value that is present. It accepts an
// absent contract value: only the service holds the contract it runs, so the
// contract gate refuses it, with the current values.
func (s ContextFabricSuppliedSynthesis) Validate() error {
	output := bytes.TrimSpace(s.Output)
	if len(s.Output) > ContextFabricSuppliedSynthesisMaxBytes {
		return fmt.Errorf("supplied_synthesis output exceeds %d bytes", ContextFabricSuppliedSynthesisMaxBytes)
	}
	if len(output) == 0 || output[0] != '{' || !json.Valid(output) {
		return fmt.Errorf("supplied_synthesis output must be a JSON object")
	}
	if (s.ModelOutputVersion != "" && !validVersion(s.ModelOutputVersion)) || (s.PromptVersion != "" && !validVersion(s.PromptVersion)) {
		return fmt.Errorf("supplied_synthesis versions violate v1 bounds")
	}
	if s.SystemSHA256 != "" && !validLowerHexSHA256(s.SystemSHA256) {
		return fmt.Errorf("supplied_synthesis system_sha256 must be 64 lowercase hex characters")
	}
	if s.InputSHA256 != "" && !validLowerHexSHA256(s.InputSHA256) {
		return fmt.Errorf("supplied_synthesis input_sha256 must be 64 lowercase hex characters")
	}
	if s.ClientModel != "" && !ValidContextFabricClientModel(s.ClientModel) {
		return fmt.Errorf("supplied_synthesis client_model violates v1 bounds")
	}
	return nil
}

// Missing lists the contract values that carry no value, in the order a
// refusal names them.
func (s ContextFabricSuppliedSynthesis) Missing() []string {
	return missingSynthesisContractValues(s.ModelOutputVersion, s.PromptVersion, s.SystemSHA256, s.InputSHA256)
}

func missingSynthesisContractValues(modelOutputVersion, promptVersion, systemSHA256, inputSHA256 string) []string {
	var missing []string
	if modelOutputVersion == "" {
		missing = append(missing, ContextFabricSynthesisContractFieldModelOutputVersion)
	}
	if promptVersion == "" {
		missing = append(missing, ContextFabricSynthesisContractFieldPromptVersion)
	}
	if systemSHA256 == "" {
		missing = append(missing, ContextFabricSynthesisContractFieldSystemSHA256)
	}
	if inputSHA256 == "" {
		missing = append(missing, ContextFabricSynthesisContractFieldInputSHA256)
	}
	return missing
}

// ContextFabricSynthesisContractRefusal is the body of the refusal of a
// supplied synthesis whose declared contract is not the service's own.
// Mismatch names each field that is absent or differs; Current is the
// contract the service runs.
type ContextFabricSynthesisContractRefusal struct {
	Mismatch []string                       `json:"mismatch"`
	Current  ContextFabricSynthesisContract `json:"current"`
}

func (r ContextFabricSynthesisContractRefusal) Validate() error {
	if len(r.Mismatch) == 0 || len(r.Mismatch) > 4 {
		return fmt.Errorf("synthesis contract refusal must name one to four mismatched fields")
	}
	seen := make(map[string]struct{}, len(r.Mismatch))
	for _, field := range r.Mismatch {
		switch field {
		case ContextFabricSynthesisContractFieldModelOutputVersion,
			ContextFabricSynthesisContractFieldPromptVersion,
			ContextFabricSynthesisContractFieldSystemSHA256,
			ContextFabricSynthesisContractFieldInputSHA256:
		default:
			return fmt.Errorf("synthesis contract refusal names an unknown field")
		}
		if _, exists := seen[field]; exists {
			return fmt.Errorf("synthesis contract refusal fields must be unique")
		}
		seen[field] = struct{}{}
	}
	if !validVersion(r.Current.ModelOutputVersion) || !validVersion(r.Current.PromptVersion) || !validLowerHexSHA256(r.Current.SystemSHA256) {
		return fmt.Errorf("synthesis contract refusal current contract violates v1 bounds")
	}
	return nil
}

// MCPSynthesisContract is the contract a draft was written under: the
// contract and input_sha256 of the synthesis input of the first answer.
type MCPSynthesisContract struct {
	ModelOutputVersion string `json:"model_output_version"`
	PromptVersion      string `json:"prompt_version"`
	SystemSHA256       string `json:"system_sha256"`
	InputSHA256        string `json:"input_sha256"`
}

// Missing lists the values that carry no value, in the order a refusal names
// them.
func (c MCPSynthesisContract) Missing() []string {
	return missingSynthesisContractValues(c.ModelOutputVersion, c.PromptVersion, c.SystemSHA256, c.InputSHA256)
}

// MCPArgumentsCarrySynthesisWriteBack reports whether raw tool arguments
// carry synthesis_output or synthesis_contract.
func MCPArgumentsCarrySynthesisWriteBack(raw []byte) bool {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return false
	}
	_, hasOutput := keys["synthesis_output"]
	_, hasContract := keys["synthesis_contract"]
	return hasOutput || hasContract
}

// SuppliedSynthesis maps the tool's write-back arguments onto the hosted
// request field. It is nil when the caller sent neither argument.
func (r MCPInvestigateWithInterpretationRequest) SuppliedSynthesis() *ContextFabricSuppliedSynthesis {
	if len(r.SynthesisOutput) == 0 && r.SynthesisContract == nil {
		return nil
	}
	supplied := ContextFabricSuppliedSynthesis{Output: r.SynthesisOutput, ClientModel: r.ClientModel}
	if r.SynthesisContract != nil {
		supplied.ModelOutputVersion = r.SynthesisContract.ModelOutputVersion
		supplied.PromptVersion = r.SynthesisContract.PromptVersion
		supplied.SystemSHA256 = r.SynthesisContract.SystemSHA256
		supplied.InputSHA256 = r.SynthesisContract.InputSHA256
	}
	return &supplied
}

func (r MCPInvestigateWithInterpretationRequest) validateSynthesisWriteBack() error {
	hasOutput := len(r.SynthesisOutput) > 0
	hasContract := r.SynthesisContract != nil
	if !hasOutput && !hasContract {
		return nil
	}
	if hasOutput != hasContract {
		return fmt.Errorf("synthesis_output and synthesis_contract must be sent together")
	}
	if r.Synthesis != ContextFabricSynthesisModeClient {
		return fmt.Errorf("synthesis_output requires synthesis %q", ContextFabricSynthesisModeClient)
	}
	if missing := r.SynthesisContract.Missing(); len(missing) > 0 {
		return fmt.Errorf("synthesis_contract requires %s", strings.Join(missing, ", "))
	}
	return r.SuppliedSynthesis().Validate()
}
